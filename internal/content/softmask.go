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
	"github.com/richardwilkes/pdfview/internal/function"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Soft masks (ISO 32000-2 11.6.5). The ExtGState /SMask entry installs a mask that gates every subsequent painting
// operation's alpha until /SMask /None (or Q) clears it. The mask's coordinates are anchored to the CTM in effect when
// the gs operator set it — NOT the CTM at paint time (oracle-pinned: a cm between gs and the paint does not move the
// mask) — composed with the mask form's own /Matrix. For each wrapped painting operation the interpreter emits
// BeginMask, replays the mask form's content (as an isolated group: alpha, blend, and soft mask reset), then EndMask,
// the operation itself (with its constant alpha and blend lifted into an enclosing BeginGroup so the mask gates alpha
// BEFORE the blend composite — oracle-pinned by the blend-under-mask probe), then PopMask.

// softMaskRes is one parsed ExtGState /SMask value (the CTM-independent part; the anchoring CTM is captured separately
// in the graphics state, mirroring fillPattern/fillPatCTM).
type softMaskRes struct {
	resources  cos.Dict
	body       []byte
	transfer   []byte
	ref        cos.Ref
	matrix     gfx.Matrix // the mask form's /Matrix (mask space -> anchor space)
	bbox       gfx.Rect   // the mask form's /BBox (mask space)
	backdrop   color.NRGBA
	hasRef     bool
	luminosity bool
}

// parseSoftMask resolves an ExtGState /SMask entry: nil for /None (and anything unusable, the viewer- conventional
// degrade — an unusable mask must not silently erase content).
func (in *interp) parseSoftMask(obj cos.Object) *softMaskRes {
	resolved := in.doc.Resolve(obj)
	dict, ok := cos.AsDict(resolved)
	if !ok {
		return nil // /None, null, or garbage.
	}
	raw := dict["G"]
	stream, ok := cos.AsStream(in.doc.Resolve(raw))
	if !ok {
		return nil
	}
	bbox, ok := rectFrom(in.doc, stream.Dict, "BBox")
	if !ok {
		return nil
	}
	body, err := in.doc.StreamData(stream)
	if err != nil {
		return nil
	}
	sm := &softMaskRes{body: body, bbox: bbox, matrix: gfx.Identity(), backdrop: color.NRGBA{A: 255}}
	if ref, isRef := raw.(cos.Ref); isRef {
		sm.ref, sm.hasRef = ref, true
	}
	if v, has := numbers6(in.doc, stream.Dict, "Matrix"); has {
		sm.matrix = gfx.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}
	}
	sm.resources, _ = in.doc.GetDict(stream.Dict, "Resources")
	s, _ := in.doc.GetName(dict, "S")
	sm.luminosity = s == "Luminosity"
	if sm.luminosity {
		// /BC is interpreted in the mask group's /CS color space; default black. The converted NRGBA is the mask
		// surface's prefill, so areas outside the group's BBox take the backdrop's luminosity (oracle-pinned: /BC [1]
		// with a small BBox leaves the outside fully unmasked).
		space := pdfcolor.DeviceGray // Space-typed; the /CS default for luminosity masks.
		if groupDict, has := in.doc.GetDict(stream.Dict, "Group"); has {
			if csObj, hasCS := groupDict["CS"]; hasCS {
				if parsed, csErr := pdfcolor.Parse(in.doc, csObj); csErr == nil {
					space = parsed
				}
			}
		}
		comps := space.Initial()
		if arr, has := in.doc.GetArray(dict, "BC"); has {
			comps = comps[:0]
			for _, entry := range arr {
				if v, numOK := cos.AsReal(in.doc.Resolve(entry)); numOK {
					comps = append(comps, float32(v))
				}
			}
		}
		bc := space.ToNRGBA(comps)
		bc.A = 255
		sm.backdrop = bc
	}
	sm.transfer = in.parseTransfer(dict["TR"])
	return sm
}

// parseTransfer samples a /TR transfer function into a 256-entry LUT; nil means identity (/Identity, absent, or
// unusable).
func (in *interp) parseTransfer(obj cos.Object) []byte {
	if obj == nil {
		return nil
	}
	resolved := in.doc.Resolve(obj)
	if name, ok := cos.AsName(resolved); ok && name == "Identity" {
		return nil
	}
	fn, err := function.Parse(in.doc, resolved)
	if err != nil {
		return nil
	}
	lut := make([]byte, 256)
	input := make([]float32, 1)
	for i := range lut {
		input[0] = float32(i) / 255
		out := fn.Eval(input)
		v := float32(0)
		if len(out) > 0 {
			v = out[0]
		}
		switch {
		case !(v > 0): // Catches NaN too.
			lut[i] = 0
		case v >= 1:
			lut[i] = 255
		default:
			lut[i] = byte(v*255 + 0.5)
		}
	}
	return lut
}

// transformAABB maps a rectangle through a matrix and returns the axis-aligned bounding box of the result.
func transformAABB(r gfx.Rect, m gfx.Matrix) gfx.Rect {
	x0, y0 := m.ApplyXY(r.X0, r.Y0)
	x1, y1 := m.ApplyXY(r.X1, r.Y0)
	x2, y2 := m.ApplyXY(r.X0, r.Y1)
	x3, y3 := m.ApplyXY(r.X1, r.Y1)
	return gfx.Rect{
		X0: min(x0, x1, x2, x3), Y0: min(y0, y1, y2, y3),
		X1: max(x0, x1, x2, x3), Y1: max(y0, y1, y2, y3),
	}
}

// masked wraps one painting emission in the active soft mask: BeginGroup (lifting the op's constant alpha and blend to
// the composite), mask replay, the op itself with alpha 1 / blend Normal / no mask, PopMask, EndGroup. Without an
// active mask the op emits directly. alpha is the op's constant alpha (fill or stroke side, per the caller).
func (in *interp) masked(alpha float64, body func()) {
	sm := in.gs.softMask
	if sm == nil {
		body()
		return
	}
	blend := in.gs.blend
	needGroup := alpha < 1 || blend != device.BlendNormal
	if needGroup {
		in.dev.BeginGroup(gfx.Rect{}, true, false, blend, alpha)
	}
	in.replayMask(sm, in.gs.softMaskCTM)
	savedFill, savedStroke, savedBlend, savedMask := in.gs.fillAlpha, in.gs.strokeAlpha, in.gs.blend, in.gs.softMask
	in.gs.fillAlpha, in.gs.strokeAlpha, in.gs.blend, in.gs.softMask = 1, 1, device.BlendNormal, nil
	body()
	in.gs.fillAlpha, in.gs.strokeAlpha, in.gs.blend, in.gs.softMask = savedFill, savedStroke, savedBlend, savedMask
	in.dev.PopMask()
	if needGroup {
		in.dev.EndGroup()
	}
}

// replayMask emits BeginMask, runs the mask form's content with the form-XObject discipline (depth cap, cycle set,
// shared budget; the mask group renders as an isolated group: alpha 1, blend Normal, no soft mask), and emits EndMask.
// anchor is the CTM captured when the gs operator installed the mask. When the content cannot replay (recursion
// limits), the mask degrades to its backdrop alone; the Begin/End pairing always holds.
func (in *interp) replayMask(sm *softMaskRes, anchor gfx.Matrix) {
	// Finite operands can still multiply to a NaN/Inf CTM (and a finite CTM against a large /BBox can overflow the
	// mapped corners), so the bbox is validated before it crosses the device seam: an unusable one degrades to the empty
	// rect rather than handing the device geometry it must re-check. The content replay below is skipped for the same
	// reason.
	ctm := sm.matrix.Mul(anchor)
	bbox := gfx.Rect{}
	if ctm.IsFinite() {
		if mapped := transformAABB(sm.bbox, ctm); mapped.IsFinite() {
			bbox = mapped
		}
	}
	in.dev.BeginMask(bbox, sm.luminosity, sm.backdrop, sm.transfer)
	defer in.dev.EndMask()
	if in.formDepth >= maxFormDepth || len(in.gsStack) >= maxQDepth || !ctm.IsFinite() {
		return
	}
	if sm.hasRef {
		if in.active[sm.ref] {
			return
		}
		in.active[sm.ref] = true
		defer delete(in.active, sm.ref)
	}
	in.opSave()
	in.gs.ctm = ctm
	in.gs.fillAlpha, in.gs.strokeAlpha, in.gs.blend, in.gs.softMask = 1, 1, device.BlendNormal, nil
	clip := &gfx.Path{}
	clip.Rect(sm.bbox.X0, sm.bbox.Y0, sm.bbox.X1-sm.bbox.X0, sm.bbox.Y1-sm.bbox.Y0)
	in.dev.ClipPath(clip, false, in.gs.ctm)
	in.gs.clips++
	resources := in.res[len(in.res)-1]
	if sm.resources != nil {
		resources = sm.resources
	}
	in.res = append(in.res, resources)
	in.frames = append(in.frames, resFrame{spaces: map[cos.Name]pdfcolor.Space{}})
	savedPath, savedCur, savedStart, savedHasCur, savedPending := in.path, in.cur, in.start, in.hasCur, in.pending
	in.path, in.hasCur, in.pending = &gfx.Path{}, false, clipNone
	in.formDepth++
	in.exec(sm.body)
	in.formDepth--
	in.path, in.cur, in.start, in.hasCur, in.pending = savedPath, savedCur, savedStart, savedHasCur, savedPending
	in.frames = in.frames[:len(in.frames)-1]
	in.res = in.res[:len(in.res)-1]
	in.opRestore()
}
