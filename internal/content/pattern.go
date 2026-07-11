// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package content

import (
	"image/color"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// Patterns (ISO 32000-2 8.7.3-4). scn with a /Pattern color space selects a pattern resource: a shading
// pattern (PatternType 2) becomes a Paint.Shading payload, a tiling pattern (PatternType 1) a Paint.Tiling
// payload whose Replay closure re-enters the interpreter for one cell's content. Pattern space is anchored to
// the default space of the content stream that selected the pattern — the /Matrix composed with the CTM in
// effect at that stream's start (streamCTM) — so the pattern stays put while the drawing CTM changes.

// namePattern is the /Pattern name, shared by the color-space switch and the resource lookups.
const namePattern cos.Name = "Pattern"

// patternRes is one resolved /Pattern resource: exactly one of sh and tile is set.
type patternRes struct {
	sh     *shading.Shading
	tile   *tilingRes
	matrix gfx.Matrix // the pattern /Matrix (pattern space -> stream default space)
}

// tilingRes is a parsed tiling pattern's replayable content.
type tilingRes struct {
	resources cos.Dict
	body      []byte
	ref       cos.Ref
	bbox      gfx.Rect
	xstep     float32
	ystep     float32
	hasRef    bool
	uncolored bool // PaintType 2: the cell content is a stencil painted with the scn-supplied color
}

// patternFor resolves the pattern selected by an scn/SCN operator: the trailing name operand looked up in the
// current /Pattern resources. It returns the pattern and the composed pattern-space→device matrix. A non-
// pattern space, a missing name, or an unusable pattern yields nil (the paint will not mark).
func (in *interp) patternFor(space pdfcolor.Space) (*patternRes, gfx.Matrix) {
	if _, isPattern := space.(*pdfcolor.Pattern); !isPattern {
		return nil, gfx.Matrix{}
	}
	if len(in.operands) == 0 {
		return nil, gfx.Matrix{}
	}
	name, ok := cos.AsName(in.operands[len(in.operands)-1])
	if !ok {
		return nil, gfx.Matrix{}
	}
	pat := in.resolvePattern(name)
	if pat == nil {
		return nil, gfx.Matrix{}
	}
	return pat, pat.matrix.Mul(in.streamCTM)
}

// resolvePattern parses /Pattern[name] with per-frame caching (failures cache as nil).
func (in *interp) resolvePattern(name cos.Name) *patternRes {
	frame := &in.frames[len(in.frames)-1]
	if frame.patterns == nil {
		frame.patterns = make(map[cos.Name]*patternRes)
	}
	if pat, ok := frame.patterns[name]; ok {
		return pat
	}
	pat := in.parsePattern(name)
	frame.patterns[name] = pat
	return pat
}

func (in *interp) parsePattern(name cos.Name) *patternRes {
	raw, ok := in.resource(namePattern, name)
	if !ok {
		return nil
	}
	resolved := in.doc.Resolve(raw)
	dict, ok := cos.AsDict(resolved)
	if !ok {
		return nil
	}
	kind, _ := in.doc.GetInt(dict, "PatternType")
	pat := &patternRes{matrix: gfx.Identity()}
	if v, has := numbers6(in.doc, dict, "Matrix"); has {
		pat.matrix = gfx.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}
	}
	switch kind {
	case 1:
		stream, isStream := cos.AsStream(resolved)
		if !isStream {
			return nil
		}
		tile := in.parseTiling(raw, stream)
		if tile == nil {
			return nil
		}
		pat.tile = tile
	case 2:
		sh, err := shading.Parse(in.doc, dict["Shading"])
		if err != nil {
			return nil
		}
		pat.sh = sh
	default:
		return nil
	}
	return pat
}

// parseTiling reads a tiling pattern stream's cell parameters and body.
func (in *interp) parseTiling(raw cos.Object, stream *cos.Stream) *tilingRes {
	bbox, ok := rectFrom(in.doc, stream.Dict, "BBox")
	if !ok || bbox.X1 <= bbox.X0 || bbox.Y1 <= bbox.Y0 {
		return nil
	}
	body, err := in.doc.StreamData(stream)
	if err != nil {
		return nil
	}
	tile := &tilingRes{body: body, bbox: bbox}
	if ref, isRef := raw.(cos.Ref); isRef {
		tile.ref, tile.hasRef = ref, true
	}
	tile.resources, _ = in.doc.GetDict(stream.Dict, "Resources")
	paintType, _ := in.doc.GetInt(stream.Dict, "PaintType")
	tile.uncolored = paintType == 2
	tile.xstep = stepValue(in.doc, stream.Dict, "XStep", bbox.X1-bbox.X0)
	tile.ystep = stepValue(in.doc, stream.Dict, "YStep", bbox.Y1-bbox.Y0)
	return tile
}

// stepValue reads a tile step, degrading zero, missing, or non-finite values to the cell extent (the leniency
// viewers apply) and folding negative steps to their magnitude (spacing is a distance).
func stepValue(d *cos.Document, dict cos.Dict, key cos.Name, fallback float32) float32 {
	v, ok := d64(d, dict, key)
	if !ok || !isFinitePt(float32(v), 0) || v == 0 {
		return fallback
	}
	if v < 0 {
		v = -v
	}
	return float32(v)
}

// applyPattern fills paint's pattern payload for one side's graphics state.
func (in *interp) applyPattern(p *device.Paint, space pdfcolor.Space, pat *patternRes, patCTM gfx.Matrix, comps []float32) {
	pspace, isPattern := space.(*pdfcolor.Pattern)
	if !isPattern || pat == nil {
		return
	}
	p.PatternCTM = patCTM
	if pat.sh != nil {
		p.Shading = pat.sh
		return
	}
	tile := pat.tile
	var cellColor color.NRGBA
	if tile.uncolored {
		cellColor = color.NRGBA{A: 255} // Black when the pattern space has no usable base.
		if pspace.Base != nil {
			cellColor = pspace.Base.ToNRGBA(comps)
		}
		p.Color = cellColor
	}
	p.Tiling = &device.Tiling{
		Replay: in.tilingReplay(tile, cellColor),
		BBox:   tile.bbox,
		XStep:  tile.xstep,
		YStep:  tile.ystep,
	}
}

// tilingReplay builds the Replay closure for one tiling pattern: it runs the cell content through a child
// interpreter against the given device, sharing this interpreter's recursion guards and work budget so cyclic
// or hostile patterns terminate. For uncolored patterns the cell's own color operators are suppressed and
// everything paints with cellColor (ISO 32000-2 8.7.3.3).
func (in *interp) tilingReplay(tile *tilingRes, cellColor color.NRGBA) func(device.Device, gfx.Matrix) {
	return func(dev device.Device, ctm gfx.Matrix) {
		if in.formDepth >= maxFormDepth {
			return
		}
		if tile.hasRef {
			if in.active[tile.ref] {
				return
			}
			in.active[tile.ref] = true
			defer delete(in.active, tile.ref)
		}
		child := newInterp(in.doc, tile.resources, ctm, dev, in.st)
		child.active = in.active
		child.formDepth = in.formDepth + 1
		child.budget = in.budget
		if tile.uncolored {
			child.suppressColor = true
			comps := []float32{float32(cellColor.R) / 255, float32(cellColor.G) / 255, float32(cellColor.B) / 255}
			child.gs.fillSpace, child.gs.fillComps = pdfcolor.DeviceRGB, comps
			child.gs.strokeSpace, child.gs.strokeComps = pdfcolor.DeviceRGB, append([]float32(nil), comps...)
		}
		child.exec(tile.body)
		in.budget = child.budget
	}
}

// opShading implements sh: paint the named shading across the current clip (ISO 32000-2 8.7.4.2). The paint
// carries the fill alpha and blend mode; geometry and colors come from the shading itself.
func (in *interp) opShading() {
	name, ok := in.name1()
	if !ok {
		return
	}
	sh := in.shadingFor(name)
	if sh == nil {
		return
	}
	in.masked(in.gs.fillAlpha, func() {
		in.dev.FillShading(sh, in.gs.ctm, device.Paint{Alpha: in.gs.fillAlpha, Blend: in.gs.blend})
	})
}

// shadingFor parses /Shading[name] with per-frame caching (failures cache as nil).
func (in *interp) shadingFor(name cos.Name) *shading.Shading {
	frame := &in.frames[len(in.frames)-1]
	if frame.shadings == nil {
		frame.shadings = make(map[cos.Name]*shading.Shading)
	}
	if sh, ok := frame.shadings[name]; ok {
		return sh
	}
	var sh *shading.Shading
	if obj, ok := in.resource("Shading", name); ok {
		if parsed, err := shading.Parse(in.doc, obj); err == nil {
			sh = parsed
		}
	}
	frame.shadings[name] = sh
	return sh
}
