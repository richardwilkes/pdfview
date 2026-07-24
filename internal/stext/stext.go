// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package stext implements the structured-text device: it records every character the content-stream interpreter emits
// — through any text verb, in emission order, unclipped — and provides fz_search_stext_page-compatible search over the
// recorded characters.
//
// The device deliberately ignores clip pushes: MuPDF's structured-text extraction is unclipped, so text scissored away
// by a clip path is still searchable, and invisible text (render mode 3, arriving through IgnoreText) is recorded too.
// Character quads are computed exactly as pinned against the oracle: Trm × [0..advance, descender..ascender], in the
// coordinate space of the interpreter pass's CTM. Search hits therefore come back in that same space; the engine seam
// runs the pass at scale 1 so they are page-space values matching the goldens' searchRaw quads bit-for-bit.
package stext

import (
	"math"
	"unicode"
	"unicode/utf8"

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Char is one positioned character as the interpreter emitted it: its bounding quad, baseline start and end, and em
// size in the pass's device units, plus the extraction rune used for matching.
type Char struct {
	// Quad is the character's bounds, Trm × [0..advance, descender..ascender]. Its corner order matches the oracle's
	// searchRaw quads (upper-left, upper-right, lower-left, lower-right in the text's orientation).
	Quad gfx.Quad
	// Origin and End are the baseline start (Trm × (0,0)) and end (Trm × (advance,0)).
	Origin gfx.Point
	End    gfx.Point
	// Rune is the extraction/search value (0 when the font provides no Unicode mapping; such characters never match a
	// needle, exactly as pinned against the oracle).
	Rune rune
	// Size is the em size in device units (the vertical scale of the Trm).
	Size float32
	// Axis reports an axis-aligned Trm (no rotation or skew).
	Axis bool
}

// Device records the characters of every text run the interpreter emits. It implements device.Device; all non-text
// operations (paths, images, clips, groups, masks) are ignored via the embedded Null.
type Device struct {
	device.Null
	// seen deduplicates runs the interpreter delivers through several verbs (fill+stroke for render mode 2, fill+clip
	// for mode 4, ...) by run identity; each run's characters are recorded once, at first delivery.
	seen  map[*device.TextRun]struct{}
	chars []Char
}

// New returns an empty structured-text device.
func New() *Device {
	return &Device{seen: make(map[*device.TextRun]struct{})}
}

// FillText implements device.Device.
func (d *Device) FillText(run *device.TextRun, _ device.Paint) { d.record(run) }

// StrokeText implements device.Device.
func (d *Device) StrokeText(run *device.TextRun, _ *gfx.StrokeParams, _ device.Paint) { d.record(run) }

// ClipText implements device.Device.
func (d *Device) ClipText(run *device.TextRun) { d.record(run) }

// IgnoreText implements device.Device.
func (d *Device) IgnoreText(run *device.TextRun) { d.record(run) }

// Chars returns the recorded characters in emission order. The slice is owned by the device; callers must not mutate
// it.
func (d *Device) Chars() []Char {
	return d.chars
}

// record appends run's characters, once per run regardless of how many verbs delivered it.
func (d *Device) record(run *device.TextRun) {
	if _, ok := d.seen[run]; ok {
		return
	}
	d.seen[run] = struct{}{}
	asc, desc := run.Font.Ascender(), run.Font.Descender()
	for _, g := range run.Glyphs {
		d.chars = append(d.chars, Char{
			Quad: gfx.Quad{
				UL: g.Trm.Apply(gfx.Point{X: 0, Y: asc}),
				UR: g.Trm.Apply(gfx.Point{X: g.Advance, Y: asc}),
				LL: g.Trm.Apply(gfx.Point{X: 0, Y: desc}),
				LR: g.Trm.Apply(gfx.Point{X: g.Advance, Y: desc}),
			},
			Origin: g.Trm.Apply(gfx.Point{}),
			End:    g.Trm.Apply(gfx.Point{X: g.Advance}),
			Rune:   g.Unicode,
			Size:   float32(math.Hypot(float64(g.Trm.C), float64(g.Trm.D))),
			Axis:   g.Trm.B == 0 && g.Trm.C == 0,
		})
	}
}

// Search finds needle in the recorded characters and returns the hit quads in emission order, at most maxQuads of them
// (a match that would overflow the budget is truncated and the search stops, so the count is exact — matching the
// original implementation, whose fixed quad buffer fz_search_stext_page filled and no further). The matching rules
// replicate fz_search_stext_page black-box, as pinned by the quad-parity tests and the probe corpus: Unicode simple
// case folding for non-space runes; a needle whitespace rune matches a run of extracted whitespace characters, a
// horizontal gap of at least gapSpaceEm (a synthesized inter-word space), or a line break; a word never silently spans
// a line break; matches do not overlap; each match yields one quad per line touched, split further by segmentQuads'
// vertical-extent rule. A needle with no non-space rune returns no hits.
func (d *Device) Search(needle string, maxQuads int) []gfx.Quad {
	return searchChars(d.chars, needle, maxQuads)
}

// Matcher thresholds, in em fractions of the preceding character's size, pinned behaviorally against the oracle: a
// horizontal gap of at least gapSpaceEm reads as a word space (MuPDF's stext synthesizes a space there — text-std14's
// "Kerned Text" needle carries a 0.5 em TJ gap); baseline origins offset by more than lineBreakEm perpendicular to the
// advance direction are different lines (measured perpendicular so rotated text advancing through device y stays one
// line).
const (
	gapSpaceEm  = 0.2
	lineBreakEm = 0.1
)

// extentSplitFraction is the relative vertical-extent divergence beyond which the oracle starts a new hit quad within
// one line. Probing brackets it in (0.101, 0.113) of the current quad's height (20-pt text merged a 22.6-pt space and
// split a 22.9-pt one — hit-quad-split.pdf); 1/9 sits inside the bracket. Corpus quads all sit far from the threshold;
// if a real file ever lands near it, re-bisect with more probes first.
const extentSplitFraction = 1.0 / 9

func searchChars(chars []Char, needle string, maxQuads int) []gfx.Quad {
	runes := []rune(needle)
	hasWordRune := false
	for _, r := range runes {
		if !unicode.IsSpace(r) {
			hasWordRune = true
			break
		}
	}
	if !hasWordRune || maxQuads <= 0 {
		return nil
	}
	var out []gfx.Quad
	for i := 0; i < len(chars) && len(out) < maxQuads; {
		quads, end, ok := matchAt(chars, i, runes)
		if !ok {
			i++
			continue
		}
		for _, q := range quads {
			if len(out) == maxQuads {
				break
			}
			out = append(out, q)
		}
		i = end
	}
	return out
}

// matchAt attempts a needle match starting at chars[start], returning the per-line quads and the index just past the
// match.
func matchAt(chars []Char, start int, needle []rune) (quads []gfx.Quad, end int, ok bool) {
	pos := start
	segStart := start
	var segments [][2]int
	for _, r := range needle {
		if unicode.IsSpace(r) {
			consumed := false
			for pos < len(chars) && isSpaceChar(chars[pos]) {
				pos++
				consumed = true
			}
			if pos > start && pos < len(chars) {
				prev, cur := chars[pos-1], chars[pos]
				switch {
				case lineBreakBetween(prev, cur):
					segments = append(segments, [2]int{segStart, pos})
					segStart = pos
					consumed = true
				case gapBetween(prev, cur) >= gapSpaceEm*prev.Size:
					consumed = true
				}
			}
			if !consumed {
				return nil, 0, false
			}
			continue
		}
		if pos >= len(chars) || isSpaceChar(chars[pos]) || chars[pos].Rune == 0 || !foldEqual(chars[pos].Rune, r) {
			return nil, 0, false
		}
		if pos > segStart && lineBreakBetween(chars[pos-1], chars[pos]) {
			return nil, 0, false // A word may not silently span a line break.
		}
		pos++
	}
	segments = append(segments, [2]int{segStart, pos})
	for _, seg := range segments {
		if seg[0] >= seg[1] {
			continue
		}
		quads = append(quads, segmentQuads(chars[seg[0]:seg[1]])...)
	}
	return quads, pos, true
}

func isSpaceChar(c Char) bool { return unicode.IsSpace(c.Rune) }

// foldEqual reports whether a and b are the same rune under Unicode simple case folding. It is the rune-level
// equivalent of strings.EqualFold over their single-rune strings — the matcher's innermost comparison, run once per
// (character, needle-rune) pair, so the two heap strings that spelling allocated dominated search cost on text-heavy
// pages. Runes that string() could not have encoded (negative, surrogate, or above unicode.MaxRune) fold to
// utf8.RuneError, exactly as the conversion would have replaced them.
func foldEqual(a, b rune) bool {
	if !utf8.ValidRune(a) {
		a = utf8.RuneError
	}
	if !utf8.ValidRune(b) {
		b = utf8.RuneError
	}
	if a == b {
		return true
	}
	// A simple-folding orbit is a short cycle through the case variants of a rune; walking a's finds b iff they fold
	// together.
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}

// advanceDir is the unit vector of a character's baseline advance ((1, 0) for a degenerate advance).
func advanceDir(c Char) (ux, uy float64) {
	dx, dy := float64(c.End.X-c.Origin.X), float64(c.End.Y-c.Origin.Y)
	n := math.Hypot(dx, dy)
	if n == 0 {
		return 1, 0
	}
	return dx / n, dy / n
}

// lineBreakBetween reports whether cur starts a new line: its baseline origin is offset from prev's perpendicular to
// prev's advance direction.
func lineBreakBetween(prev, cur Char) bool {
	ux, uy := advanceDir(prev)
	dx, dy := float64(cur.Origin.X-prev.Origin.X), float64(cur.Origin.Y-prev.Origin.Y)
	return math.Abs(ux*dy-uy*dx) > float64(lineBreakEm*prev.Size)
}

// gapBetween is the signed distance along prev's advance direction from prev's end to cur's origin (negative when
// kerning tucks cur backward).
func gapBetween(prev, cur Char) float32 {
	ux, uy := advanceDir(prev)
	return float32(ux*float64(cur.Origin.X-prev.End.X) + uy*float64(cur.Origin.Y-prev.End.Y))
}

// segmentQuads assembles one line's matched characters into hit quads, reproducing the oracle's grouping (pinned by
// irs-fw9 and hit-quad-split.pdf): characters extend the current quad horizontally while their vertical extent stays
// within extentSplitFraction of the quad's height — measured against the extent the quad's FIRST character established,
// which is never stretched by later merged characters — and a character diverging further (such as a much larger
// inter-word space) closes the quad and starts its own. Non-axis-aligned text (rotated) keeps the first/last-corner
// assembly; the corpus exercises only uniform-extent rotated runs.
func segmentQuads(seg []Char) []gfx.Quad {
	axis := true
	for _, c := range seg {
		if !c.Axis {
			axis = false
			break
		}
	}
	if !axis {
		first, last := seg[0].Quad, seg[len(seg)-1].Quad
		return []gfx.Quad{{UL: first.UL, UR: last.UR, LL: first.LL, LR: last.LR}}
	}
	var out []gfx.Quad
	var top, bottom, minX, maxX float32
	open := false
	flush := func() {
		if open {
			out = append(out, gfx.Quad{
				UL: gfx.Point{X: minX, Y: top},
				UR: gfx.Point{X: maxX, Y: top},
				LL: gfx.Point{X: minX, Y: bottom},
				LR: gfx.Point{X: maxX, Y: bottom},
			})
			open = false
		}
	}
	for _, c := range seg {
		cTop, cBottom := c.Quad.UL.Y, c.Quad.LL.Y
		cMinX, cMaxX := min(c.Quad.UL.X, c.Quad.UR.X), max(c.Quad.UL.X, c.Quad.UR.X)
		if open {
			// Absolute value: vertically-mirrored axis-aligned text (Trm.D > 0) puts bottom above top, and a negative
			// limit would reject every merge, fragmenting the line into one quad per character.
			limit := float64(bottom-top) * extentSplitFraction
			if limit < 0 {
				limit = -limit
			}
			if math.Abs(float64(cTop-top)) <= limit && math.Abs(float64(cBottom-bottom)) <= limit {
				minX, maxX = min(minX, cMinX), max(maxX, cMaxX)
				continue
			}
			flush()
		}
		top, bottom, minX, maxX = cTop, cBottom, cMinX, cMaxX
		open = true
	}
	flush()
	return out
}
