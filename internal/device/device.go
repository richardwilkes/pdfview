// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package device defines the seam between the content-stream interpreter (internal/content) and its consumers: the
// raster device (internal/render) and the structured-text device (internal/stext). One interpreter pass drives N
// devices through Tee, so a render call walks each content stream exactly once.
//
// Contract: the interpreter owns all PDF semantics — colorspace and function resolution, graphics-state tracking,
// recursion and resource limits — and guarantees balanced push/pop pairing: every
// ClipPath/ClipStrokePath/ClipImageMask/EndTextClip is matched by a later PopClip, every BeginGroup by an EndGroup, and
// every BeginMask by an EndMask and then a PopMask, even when the content stream itself is unbalanced or truncated.
// Devices therefore never need defensive stack checks. Geometry arrives in the coordinate space of the ctm argument's
// source (user space); the ctm maps it to device space.
package device

import (
	"image/color"

	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// Blend identifies a PDF blend mode (ISO 32000-2 11.3.5). The zero value is Normal. The Compatible name maps to Normal,
// and unrecognized names must be treated as Normal by the producer (the interpreter).
type Blend uint8

// Blend values, in the order the standard lists them.
const (
	BlendNormal Blend = iota
	BlendMultiply
	BlendScreen
	BlendOverlay
	BlendDarken
	BlendLighten
	BlendColorDodge
	BlendColorBurn
	BlendHardLight
	BlendSoftLight
	BlendDifference
	BlendExclusion
	BlendHue
	BlendSaturation
	BlendColor
	BlendLuminosity
)

// Paint describes how a fill or stroke is painted. Exactly one of the three sources is active: a Shading or Tiling
// pattern when the respective pointer is non-nil, otherwise the solid Color (already resolved to the rendered RGB space
// by the interpreter via internal/color; for an uncolored tiling pattern the Color is the scn-supplied pattern color
// the cell content paints with). Alpha is the folded constant alpha (the ca/CA graphics-state parameter combined with
// any enclosing group's alpha), in [0, 1]. PatternCTM maps the active pattern source's own space (pattern space for
// Tiling, shading target space for Shading) to device space — the pattern /Matrix composed with the CTM in effect at
// the start of the content stream that selected the pattern, so pattern geometry stays anchored while the drawing CTM
// changes (ISO 32000-2 8.7.3.1).
type Paint struct {
	Shading    *shading.Shading
	Tiling     *Tiling
	Alpha      float64
	PatternCTM gfx.Matrix
	Color      color.NRGBA
	Blend      Blend
}

// Tiling describes a tiling-pattern paint source (ISO 32000-2 8.7.3): the pattern cell's bounding box and spacing in
// pattern space, and a replay function that runs one cell's content against a device with the given
// pattern-space→target CTM. Replay is only valid for the duration of the device call that received the Paint; it honors
// the interpreter's recursion and work budgets (a cyclic or over-deep pattern replays nothing).
type Tiling struct {
	Replay func(dev Device, ctm gfx.Matrix)
	// Key, when non-nil, identifies the cell content for caching a rasterization of it: two Tilings with equal Keys and
	// equal cell geometry replay identical content, so a device may reuse one's rendered cell for the other. It is a
	// comparable value usable as a map key. nil means the cell must be replayed on every use — the interpreter withholds
	// a key when Replay would paint nothing (a pattern already active in the replay stack, or one at the recursion cap)
	// or when the pattern has no stable identity to key on.
	Key   any
	BBox  gfx.Rect
	XStep float32
	YStep float32
}

// Glyph is one positioned glyph in a text run. Trm is the fully composed glyph-space→device-space matrix for this glyph
// (text-space parameters, text matrix, and ctm folded together — glyph space here is the em-normalized space where an
// advance of 1.0 is one em); Advance is the glyph's advance in that space. Unicode is the extraction/search value (0
// when unknown).
type Glyph struct {
	Trm     gfx.Matrix
	GID     uint32
	Code    uint32
	Unicode rune
	Advance float32
}

// TextRun is a run of glyphs sharing one font and writing mode, produced by one show-text operator. CTM is the graphics
// state's user→device matrix at emission time: each glyph's Trm already folds it in, but glyph STROKING needs it
// separately, because stroke parameters are user-space quantities the pen picks up under the CTM alone — not under the
// text matrix or font size (ISO 32000-2 9.3.6).
type TextRun struct {
	Font   *font.Font
	Glyphs []Glyph
	CTM    gfx.Matrix
	WMode  uint8 // 0 horizontal, 1 vertical
}

// Device receives the interpreter's drawing operations. Implementations may ignore any call (the null device ignores
// all of them) but must honor the stack discipline documented on the package: pushes and pops arrive balanced.
type Device interface {
	// FillPath fills p (interpreted with the even-odd rule when evenOdd is set, non-zero winding otherwise).
	FillPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix, paint Paint)
	// StrokePath strokes p with the given stroke parameters (user-space units; ctm applies to the pen too).
	StrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix, paint Paint)
	// ClipPath pushes a clip: subsequent drawing is restricted to p's fill region until the matching PopClip.
	ClipPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix)
	// ClipStrokePath pushes a clip restricted to the stroked region of p.
	ClipStrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix)
	// FillText fills the glyphs of run.
	FillText(run *TextRun, paint Paint)
	// StrokeText strokes the glyphs of run.
	StrokeText(run *TextRun, sp *gfx.StrokeParams, paint Paint)
	// ClipText accumulates run into the pending text clip; the interpreter finalizes the accumulation with EndTextClip
	// when the enclosing text object ends.
	ClipText(run *TextRun)
	// EndTextClip pushes the accumulated text clip as a single clip level, popped by one later PopClip. The interpreter
	// emits it exactly once per text object that produced ClipText calls (including at forced text-object end when a
	// stream truncates), so ClipText accumulations never span text objects.
	EndTextClip()
	// IgnoreText reports glyphs that paint nothing (text render mode 3): the structured-text device records them, the
	// raster device ignores them.
	IgnoreText(run *TextRun)
	// FillImage draws img under ctm (the unit square of image space maps to ctm's parallelogram).
	FillImage(img *imaging.Image, ctm gfx.Matrix, alpha float64)
	// FillImageMask stencils paint through img's mask bits.
	FillImageMask(img *imaging.Image, ctm gfx.Matrix, paint Paint)
	// ClipImageMask pushes a clip restricted to img's mask bits.
	ClipImageMask(img *imaging.Image, ctm gfx.Matrix)
	// PopClip pops the most recent clip push.
	PopClip()
	// BeginGroup opens a transparency group; content until the matching EndGroup composites as a unit, with the group's
	// constant alpha and blend mode applied once at that composite (ISO 32000-2 11.6.6: the producer resets its
	// alpha/blend/soft-mask state for the group's interior). isolated groups composite their interior against a
	// transparent backdrop; knockout groups let each interior object knock out the ones before it.
	BeginGroup(bbox gfx.Rect, isolated, knockout bool, blend Blend, alpha float64)
	// EndGroup closes the innermost group.
	EndGroup()
	// BeginMask starts soft-mask content: drawing until EndMask defines the mask. For a luminosity mask the mask
	// surface starts at the backdrop color (/BC composited under the mask group, ISO 32000-2 11.6.5.2) and the mask
	// value is the rendered luminosity; for an alpha mask it starts transparent and the mask value is the rendered
	// alpha. transfer, when non-nil, is the /TR transfer function sampled to a 256-entry LUT applied to the mask value.
	BeginMask(bbox gfx.Rect, luminosity bool, backdrop color.NRGBA, transfer []byte)
	// EndMask switches from mask content to masked content.
	EndMask()
	// PopMask applies the mask to the content drawn since EndMask and pops it.
	PopMask()
	// FillShading paints sh across the current clip region under ctm; paint supplies the folded alpha and blend mode
	// (its color/pattern payloads are ignored — the shading is the color source).
	FillShading(sh *shading.Shading, ctm gfx.Matrix, paint Paint)
}
