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
	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// op dispatches one operator. Operators with missing or mistyped operands are skipped, as are unknown ones — the
// operand list is discarded either way by the caller, which is the viewer-conventional recovery that keeps hostile or
// sloppy content from desynchronizing anything.
//
//nolint:gocyclo // A flat dispatch table over the content operator set; a map of closures would just hide the same fan-out.
func (in *interp) op(word string) {
	if in.t3Shape == t3Mask || in.suppressColor {
		// After d1, a Type 3 charproc is a pure shape: its color operators are ignored so the caller's fill color
		// paints the glyph (ISO 32000-2 9.6.4). An uncolored tiling pattern's cell is likewise a stencil painted with
		// the pattern color (8.7.3.3), so its color operators are suppressed too.
		switch word {
		case "g", "G", "rg", "RG", "k", "K", "cs", "CS", "sc", "SC", "scn", "SCN":
			return
		}
	}
	switch word {
	// ---- graphics state ----
	case "q":
		in.opSave()
	case "Q":
		in.opRestore()
	case "cm":
		if v, ok := in.floats(6); ok {
			// Guard the resulting CTM's finiteness (like opTm): finite operands can still multiply to a NaN/Inf CTM,
			// which the path/stroke/shading paints pass straight to the device without re-checking.
			if m := (gfx.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}).Mul(in.gs.ctm); m.IsFinite() {
				in.gs.ctm = m
			}
		}
	case "w":
		if v, ok := in.float1(); ok && v >= 0 && isFinitePt(v, 0) {
			in.gs.sp.Width = v
		}
	case "J":
		if v, ok := in.int1(); ok && v >= 0 && v <= 2 {
			in.gs.sp.Cap = gfx.LineCap(v)
		}
	case "j":
		if v, ok := in.int1(); ok && v >= 0 && v <= 2 {
			in.gs.sp.Join = gfx.LineJoin(v)
		}
	case "M":
		if v, ok := in.float1(); ok && isFinitePt(v, 0) && v > 0 {
			in.gs.sp.MiterLimit = v
		}
	case "d":
		in.opDash()
	case "ri", "i":
		// Rendering intent and flatness tolerance: accepted, no observable effect in this renderer.
	case "gs":
		in.opExtGState()

	// ---- path construction ----
	case "m":
		if v, ok := in.floats(2); ok && isFinitePt(v[0], v[1]) {
			in.path.MoveTo(v[0], v[1])
			in.cur = gfx.Point{X: v[0], Y: v[1]}
			in.start = in.cur
			in.hasCur = true
		}
	case "l":
		if v, ok := in.floats(2); ok && in.hasCur && isFinitePt(v[0], v[1]) {
			in.path.LineTo(v[0], v[1])
			in.cur = gfx.Point{X: v[0], Y: v[1]}
		}
	case "c":
		if v, ok := in.floats(6); ok && in.hasCur && isFinitePt(v[0], v[1]) && isFinitePt(v[2], v[3]) && isFinitePt(v[4], v[5]) {
			in.path.CubicTo(v[0], v[1], v[2], v[3], v[4], v[5])
			in.cur = gfx.Point{X: v[4], Y: v[5]}
		}
	case "v":
		if v, ok := in.floats(4); ok && in.hasCur && isFinitePt(v[0], v[1]) && isFinitePt(v[2], v[3]) {
			in.path.CubicTo(in.cur.X, in.cur.Y, v[0], v[1], v[2], v[3])
			in.cur = gfx.Point{X: v[2], Y: v[3]}
		}
	case "y":
		if v, ok := in.floats(4); ok && in.hasCur && isFinitePt(v[0], v[1]) && isFinitePt(v[2], v[3]) {
			in.path.CubicTo(v[0], v[1], v[2], v[3], v[2], v[3])
			in.cur = gfx.Point{X: v[2], Y: v[3]}
		}
	case "h":
		if in.hasCur {
			in.path.Close()
			in.cur = in.start
		}
	case "re":
		if v, ok := in.floats(4); ok && isFinitePt(v[0], v[1]) && isFinitePt(v[2], v[3]) {
			in.path.Rect(v[0], v[1], v[2], v[3])
			in.cur = gfx.Point{X: v[0], Y: v[1]}
			in.start = in.cur
			in.hasCur = true
		}

	// ---- path painting ----
	case "S":
		in.paintPath(false, false, true, false)
	case "s":
		in.paintPath(false, false, true, true)
	case "f", "F":
		in.paintPath(true, false, false, false)
	case "f*":
		in.paintPath(true, true, false, false)
	case "B":
		in.paintPath(true, false, true, false)
	case "B*":
		in.paintPath(true, true, true, false)
	case "b":
		in.paintPath(true, false, true, true)
	case "b*":
		in.paintPath(true, true, true, true)
	case "n":
		in.paintPath(false, false, false, false)

	// ---- clipping ----
	case "W":
		in.pending = clipNonZero
	case "W*":
		in.pending = clipEvenOdd

	// ---- color ----
	case "g":
		if v, ok := in.floats(1); ok {
			in.gs.fillSpace, in.gs.fillComps, in.gs.fillPattern = pdfcolor.DeviceGray, v, nil
		}
	case "G":
		if v, ok := in.floats(1); ok {
			in.gs.strokeSpace, in.gs.strokeComps, in.gs.strokePattern = pdfcolor.DeviceGray, v, nil
		}
	case "rg":
		if v, ok := in.floats(3); ok {
			in.gs.fillSpace, in.gs.fillComps, in.gs.fillPattern = pdfcolor.DeviceRGB, v, nil
		}
	case "RG":
		if v, ok := in.floats(3); ok {
			in.gs.strokeSpace, in.gs.strokeComps, in.gs.strokePattern = pdfcolor.DeviceRGB, v, nil
		}
	case "k":
		if v, ok := in.floats(4); ok {
			in.gs.fillSpace, in.gs.fillComps, in.gs.fillPattern = pdfcolor.DeviceCMYK, v, nil
		}
	case "K":
		if v, ok := in.floats(4); ok {
			in.gs.strokeSpace, in.gs.strokeComps, in.gs.strokePattern = pdfcolor.DeviceCMYK, v, nil
		}
	case "cs":
		if name, ok := in.name1(); ok {
			in.gs.fillPattern = nil // A fresh space has no selected pattern until an scn names one.
			if space, spaceOK := in.colorSpace(name); spaceOK {
				in.gs.fillSpace, in.gs.fillComps = space, space.Initial()
			} else {
				// The viewer-conventional fallback for an unresolvable space: gray black.
				in.gs.fillSpace, in.gs.fillComps = pdfcolor.DeviceGray, pdfcolor.DeviceGray.Initial()
			}
		}
	case "CS":
		if name, ok := in.name1(); ok {
			in.gs.strokePattern = nil
			if space, spaceOK := in.colorSpace(name); spaceOK {
				in.gs.strokeSpace, in.gs.strokeComps = space, space.Initial()
			} else {
				in.gs.strokeSpace, in.gs.strokeComps = pdfcolor.DeviceGray, pdfcolor.DeviceGray.Initial()
			}
		}
	case "sc", "scn":
		in.gs.fillComps = in.componentsFor(in.gs.fillSpace)
		in.gs.fillPattern, in.gs.fillPatCTM = in.patternFor(in.gs.fillSpace)
	case "SC", "SCN":
		in.gs.strokeComps = in.componentsFor(in.gs.strokeSpace)
		in.gs.strokePattern, in.gs.strokePatCTM = in.patternFor(in.gs.strokeSpace)

	// ---- XObjects and shadings ----
	case "Do":
		in.opDo()
	case "sh":
		in.opShading()

	// ---- text objects and text state ----
	case "BT":
		in.opBeginText()
	case "ET":
		in.opEndText()
	case "Tc":
		if v, ok := in.float1(); ok && isFinitePt(v, 0) {
			in.gs.text.charSpacing = v
		}
	case "Tw":
		if v, ok := in.float1(); ok && isFinitePt(v, 0) {
			in.gs.text.wordSpacing = v
		}
	case "Tz":
		if v, ok := in.float1(); ok && isFinitePt(v, 0) {
			in.gs.text.scale = v / 100
		}
	case "TL":
		if v, ok := in.float1(); ok && isFinitePt(v, 0) {
			in.gs.text.leading = v
		}
	case "Tf":
		in.opTf()
	case "Tr":
		if v, ok := in.int1(); ok && v >= 0 && v <= 7 {
			in.gs.text.mode = int(v)
		}
	case "Ts":
		if v, ok := in.float1(); ok && isFinitePt(v, 0) {
			in.gs.text.rise = v
		}
	case "Td":
		if v, ok := in.floats(2); ok {
			in.textMove(v[0], v[1])
		}
	case "TD":
		if v, ok := in.floats(2); ok && isFinitePt(v[0], v[1]) {
			in.gs.text.leading = -v[1]
			in.textMove(v[0], v[1])
		}
	case "Tm":
		in.opTm()
	case "T*":
		in.textMove(0, -in.gs.text.leading)
	case "Tj":
		in.opShowString()
	case "TJ":
		in.opTJ()
	case "'":
		in.opNextLineShow()
	case "\"":
		in.opSpacedShow()

	// ---- Type 3 glyph metrics ----
	case "d0":
		if in.t3Shape != t3None {
			in.t3Shape = t3Colored // The proc paints with its own colors.
		}
	case "d1":
		if in.t3Shape != t3None {
			in.t3Shape = t3Mask // The proc is a shape; the caller's fill color applies (see the guard above).
		}

	// ---- recognized no-ops: marked content, compatibility ----
	case "BMC", "BDC", "EMC", "MP", "DP", "BX", "EX":

	default:
		// Unknown operator: skipped; the caller resets the operand list.
	}
}

// opSave implements q. Pushes beyond the depth cap are ignored but counted, so their matching Qs are ignored
// symmetrically.
func (in *interp) opSave() {
	if len(in.gsStack) >= maxQDepth {
		in.qOverflow++
		return
	}
	in.gsStack = append(in.gsStack, in.gs.clone())
	in.gs.clips = 0
}

// opRestore implements Q. Restores are ignored when they would cross the executing stream's boundary (qFloor) or match
// an ignored overflow push.
func (in *interp) opRestore() {
	if in.qOverflow > 0 {
		in.qOverflow--
		return
	}
	if len(in.gsStack) <= in.qFloor {
		return
	}
	in.restoreState()
}

// paintPath implements the path-painting operators: optional close, fill, stroke, then the deferred W/W* clip, and
// finally the path reset. Fill precedes stroke (B and friends), which matters under transparency.
func (in *interp) paintPath(fill, evenOdd, stroke, closeFirst bool) {
	if closeFirst && in.hasCur {
		in.path.Close()
		in.cur = in.start
	}
	if !in.path.IsEmpty() {
		if fill && in.marks(in.gs.fillSpace, in.gs.fillPattern) {
			in.masked(in.gs.fillAlpha, func() {
				in.dev.FillPath(in.path, evenOdd, in.gs.ctm, in.fillPaint())
			})
		}
		if stroke && in.marks(in.gs.strokeSpace, in.gs.strokePattern) {
			in.masked(in.gs.strokeAlpha, func() {
				in.dev.StrokePath(in.path, &in.gs.sp, in.gs.ctm, in.strokePaint())
			})
		}
	}
	if in.pending != clipNone {
		// The clip applies even when the path is empty (clipping everything out), matching viewer behavior.
		in.dev.ClipPath(in.path, in.pending == clipEvenOdd, in.gs.ctm)
		in.gs.clips++
		in.pending = clipNone
	}
	in.path = &gfx.Path{}
	in.hasCur = false
}

// marks reports whether painting with the space produces marks: a /Pattern space marks only once an scn has selected a
// usable pattern; Separation /None never marks by definition (its color resolves transparent).
func (in *interp) marks(space pdfcolor.Space, pat *patternRes) bool {
	if _, isPattern := space.(*pdfcolor.Pattern); isPattern {
		return pat != nil
	}
	return true
}

func (in *interp) fillPaint() device.Paint {
	p := device.Paint{
		Color: in.gs.fillSpace.ToNRGBA(in.gs.fillComps),
		Alpha: in.gs.fillAlpha,
		Blend: in.gs.blend,
	}
	in.applyPattern(&p, in.gs.fillSpace, in.gs.fillPattern, in.gs.fillPatCTM, in.gs.fillComps)
	return p
}

func (in *interp) strokePaint() device.Paint {
	p := device.Paint{
		Color: in.gs.strokeSpace.ToNRGBA(in.gs.strokeComps),
		Alpha: in.gs.strokeAlpha,
		Blend: in.gs.blend,
	}
	in.applyPattern(&p, in.gs.strokeSpace, in.gs.strokePattern, in.gs.strokePatCTM, in.gs.strokeComps)
	return p
}

// componentsFor implements sc/scn/SC/SCN: the leading numeric operands, at most the space's component count
// (uncolored-pattern operands are followed by the pattern name, which patternFor consumes).
func (in *interp) componentsFor(space pdfcolor.Space) []float32 {
	maxN := space.NComponents()
	if maxN == 0 {
		return nil
	}
	return in.leadingFloats(maxN)
}

// opDash implements d: a dash array plus phase. The array is truncated to maxDashEntries and non-finite or negative
// entries invalidate it (rendered solid), the leniency deployed viewers apply; deeper sanitization (odd counts,
// all-zero patterns) is the raster device's concern.
func (in *interp) opDash() {
	if len(in.operands) < 2 {
		return
	}
	arr, ok := in.operands[0].(cos.Array)
	if !ok {
		return
	}
	phase, ok := cos.AsReal(in.operands[1])
	if !ok {
		return
	}
	dash := make([]float32, 0, min(len(arr), maxDashEntries))
	for _, entry := range arr {
		if len(dash) >= maxDashEntries {
			break
		}
		v, numOK := cos.AsReal(entry)
		if !numOK || v < 0 || !isFinitePt(float32(v), 0) {
			return // Invalid dash arrays leave the previous dash in effect (skip the operator).
		}
		dash = append(dash, float32(v))
	}
	if !isFinitePt(float32(phase), 0) {
		return // A non-finite dash phase would flow to the stroker's dash offset; skip the operator.
	}
	in.gs.sp.Dash = dash
	in.gs.sp.DashPhase = float32(phase)
}

// resolvedDashLengths prepares an ExtGState /D dash-length array for opDash. Content-stream operands are always direct,
// so opDash reads the entries without resolving them, but a /D array lives in the object graph where `[[3 0 R 2] 0]` is
// legal; the entries are resolved into a copy, since the original belongs to the document's object cache. The
// maxDashEntries truncation mirrors opDash's own, so the trailing entries it would ignore are never loaded.
func (in *interp) resolvedDashLengths(obj cos.Object) cos.Object {
	arr, ok := cos.AsArray(in.doc.Resolve(obj))
	if !ok {
		return cos.Null{} // Not an array: opDash's own type check skips the operator, leaving the previous dash.
	}
	out := make(cos.Array, 0, min(len(arr), maxDashEntries))
	for _, entry := range arr {
		if len(out) >= maxDashEntries {
			break
		}
		out = append(out, in.doc.Resolve(entry))
	}
	return out
}

// opExtGState implements gs: apply the supported subset of an ExtGState dictionary (line parameters, dash, constant
// alpha, blend mode, soft mask). The remaining entries (font, transfer functions, ...) are ignored.
func (in *interp) opExtGState() {
	name, ok := in.name1()
	if !ok {
		return
	}
	obj, ok := in.resource("ExtGState", name)
	if !ok {
		return
	}
	dict, ok := cos.AsDict(in.doc.Resolve(obj))
	if !ok {
		return
	}
	if v, has := d64(in.doc, dict, "LW"); has && v >= 0 {
		if f := float32(v); isFinitePt(f, 0) {
			in.gs.sp.Width = f
		}
	}
	if v, has := in.doc.GetInt(dict, "LC"); has && v >= 0 && v <= 2 {
		in.gs.sp.Cap = gfx.LineCap(v)
	}
	if v, has := in.doc.GetInt(dict, "LJ"); has && v >= 0 && v <= 2 {
		in.gs.sp.Join = gfx.LineJoin(v)
	}
	if v, has := d64(in.doc, dict, "ML"); has {
		if f := float32(v); isFinitePt(f, 0) && f > 0 {
			in.gs.sp.MiterLimit = f
		}
	}
	if arr, has := in.doc.GetArray(dict, "D"); has && len(arr) == 2 {
		saved := in.operands
		in.operands = []cos.Object{in.resolvedDashLengths(arr[0]), in.doc.Resolve(arr[1])}
		in.opDash()
		in.operands = saved
	}
	if v, has := d64(in.doc, dict, "CA"); has && v >= 0 && v <= 1 {
		in.gs.strokeAlpha = v
	}
	if v, has := d64(in.doc, dict, "ca"); has && v >= 0 && v <= 1 {
		in.gs.fillAlpha = v
	}
	if bm, has := dict["BM"]; has {
		in.gs.blend = blendFor(in.doc, bm)
	}
	if smObj, has := dict["SMask"]; has {
		in.gs.softMask = in.softMaskFor(name, smObj)
		in.gs.softMaskCTM = in.gs.ctm // The mask anchors to the CTM at gs time (oracle-pinned; softmask.go).
	}
}

// softMaskFor parses an ExtGState /SMask entry with per-frame caching keyed by the ExtGState resource name (the
// CTM-independent part is cached; the anchor CTM is captured per invocation).
func (in *interp) softMaskFor(gsName cos.Name, obj cos.Object) *softMaskRes {
	frame := &in.frames[len(in.frames)-1]
	if frame.softMasks == nil {
		frame.softMasks = make(map[cos.Name]*softMaskRes)
	}
	if sm, ok := frame.softMasks[gsName]; ok {
		return sm
	}
	sm := in.parseSoftMask(obj)
	frame.softMasks[gsName] = sm
	return sm
}

// d64 resolves dict[key] as a float64.
func d64(d *cos.Document, dict cos.Dict, key cos.Name) (float64, bool) {
	return cos.AsReal(d.Resolve(dict[key]))
}

// blendFor maps a /BM value (a name, or an array whose first supported name wins) to a blend mode. Unrecognized names
// mean Normal, as the standard requires.
func blendFor(d *cos.Document, obj cos.Object) device.Blend {
	resolved := d.Resolve(obj)
	if arr, ok := resolved.(cos.Array); ok {
		for _, entry := range arr {
			if name, nameOK := cos.AsName(d.Resolve(entry)); nameOK {
				if blend, known := blendNames[name]; known {
					return blend
				}
			}
		}
		return device.BlendNormal
	}
	if name, ok := cos.AsName(resolved); ok {
		if blend, known := blendNames[name]; known {
			return blend
		}
	}
	return device.BlendNormal
}

var blendNames = map[cos.Name]device.Blend{
	"Normal": device.BlendNormal, "Compatible": device.BlendNormal,
	"Multiply": device.BlendMultiply, "Screen": device.BlendScreen, "Overlay": device.BlendOverlay,
	"Darken": device.BlendDarken, "Lighten": device.BlendLighten, "ColorDodge": device.BlendColorDodge,
	"ColorBurn": device.BlendColorBurn, "HardLight": device.BlendHardLight, "SoftLight": device.BlendSoftLight,
	"Difference": device.BlendDifference, "Exclusion": device.BlendExclusion, "Hue": device.BlendHue,
	"Saturation": device.BlendSaturation, "Color": device.BlendColor, "Luminosity": device.BlendLuminosity,
}

// opDo implements Do. Image XObjects decode through internal/imaging and draw; form XObjects execute under the
// recursion depth cap and a cycle set so self-referential forms terminate.
func (in *interp) opDo() {
	name, ok := in.name1()
	if !ok {
		return
	}
	raw, ok := in.resource("XObject", name)
	if !ok {
		return
	}
	stream, ok := cos.AsStream(in.doc.Resolve(raw))
	if !ok {
		return
	}
	subtype, _ := in.doc.GetName(stream.Dict, "Subtype")
	if subtype == "Image" {
		in.drawImageXObject(raw, stream)
		return
	}
	if subtype != "Form" {
		return // /PS and anything unrecognized draw nothing.
	}
	in.execForm(raw, stream)
}

// execForm runs a form XObject's content under the full form discipline — recursion depth cap, reference cycle set, a
// work-budget charge for the body on every invocation (the cycle set stops re-entry, not repetition), q + /Matrix
// concat + /BBox clip + own-/Resources frame + fresh per-stream state, then Q — against the current graphics state.
// opDo dispatches here; RunAnnot enters here directly for annotation appearance streams (which are form XObjects
// positioned by the caller's CTM).
func (in *interp) execForm(raw cos.Object, stream *cos.Stream) {
	if in.formDepth >= maxFormDepth {
		return
	}
	ref, isRef := raw.(cos.Ref)
	if isRef {
		if in.active[ref] {
			return
		}
		in.active[ref] = true
		defer delete(in.active, ref)
	}
	body, ok := in.streamBody(raw, stream)
	if !ok {
		return
	}
	// A form executes like q; cm /Matrix; W-clip /BBox; its content; Q — with its own resources and a fresh per-stream
	// state (operand list, path, pending clip).
	if len(in.gsStack) >= maxQDepth {
		return // No room to save state; skipping the form entirely is the only balanced choice.
	}
	in.opSave()
	if v, has := numbers6(in.doc, stream.Dict, "Matrix"); has {
		// Guard the concatenated CTM's finiteness (like cm/replayMask): finite operands can still multiply to a
		// NaN/Inf CTM, which transformAABB/ClipPath and the form body's paints pass on without re-checking.
		if m := (gfx.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}).Mul(in.gs.ctm); m.IsFinite() {
			in.gs.ctm = m
		}
	}
	// A /Group /S /Transparency form composites as a transparency group (ISO 32000-2 11.6.6): the current soft mask,
	// constant alpha, and blend apply once to the group's composite (the mask via the replay, the alpha/blend via
	// BeginGroup), and the group's interior starts with those reset. Painting a group via Do is a nonstroking
	// operation, so the FILL alpha composites it.
	inGroup, isolated, knockout := in.transparencyGroup(stream.Dict)
	maskWrapped := false
	if inGroup {
		bboxDev := gfx.Rect{}
		if bbox, has := rectFrom(in.doc, stream.Dict, "BBox"); has {
			bboxDev = transformAABB(bbox, in.gs.ctm)
		}
		in.dev.BeginGroup(bboxDev, isolated, knockout, in.gs.blend, in.gs.fillAlpha)
		if sm := in.gs.softMask; sm != nil {
			in.replayMask(sm, in.gs.softMaskCTM)
			maskWrapped = true
		}
		in.gs.fillAlpha, in.gs.strokeAlpha, in.gs.blend, in.gs.softMask = 1, 1, device.BlendNormal, nil
	}
	if bbox, has := rectFrom(in.doc, stream.Dict, "BBox"); has {
		clip := &gfx.Path{}
		clip.Rect(bbox.X0, bbox.Y0, bbox.X1-bbox.X0, bbox.Y1-bbox.Y0)
		in.dev.ClipPath(clip, false, in.gs.ctm)
		in.gs.clips++
	}
	resources := in.res[len(in.res)-1]
	if formRes, has := in.doc.GetDict(stream.Dict, "Resources"); has {
		resources = formRes
	}
	in.res = append(in.res, resources)
	in.frames = append(in.frames, resFrame{spaces: map[cos.Name]pdfcolor.Space{}})
	savedPath, savedCur, savedStart, savedHasCur, savedPending := in.path, in.cur, in.start, in.hasCur, in.pending
	in.path, in.hasCur, in.pending = &gfx.Path{}, false, clipNone
	in.formDepth++
	in.exec(body)
	in.formDepth--
	in.path, in.cur, in.start, in.hasCur, in.pending = savedPath, savedCur, savedStart, savedHasCur, savedPending
	in.frames = in.frames[:len(in.frames)-1]
	in.res = in.res[:len(in.res)-1]
	if inGroup {
		// The BBox clip must pop before the mask/group layers close, keeping the device's push/pop nesting well-formed;
		// opRestore then only restores the graphics state.
		in.popClips(0)
		if maskWrapped {
			in.dev.PopMask()
		}
		in.dev.EndGroup()
	}
	in.opRestore()
}

// transparencyGroup reports whether a form XObject's dictionary declares a transparency group, with its isolation (/I)
// and knockout (/K) attributes.
func (in *interp) transparencyGroup(dict cos.Dict) (isGroup, isolated, knockout bool) {
	groupDict, has := in.doc.GetDict(dict, "Group")
	if !has {
		return false, false, false
	}
	if s, _ := in.doc.GetName(groupDict, "S"); s != "Transparency" {
		return false, false, false
	}
	isolated, _ = cos.AsBool(in.doc.Resolve(groupDict["I"]))
	knockout, _ = cos.AsBool(in.doc.Resolve(groupDict["K"]))
	return true, isolated, knockout
}

// numbers6 reads dict[key] as six finite numbers.
func numbers6(d *cos.Document, dict cos.Dict, key cos.Name) ([6]float32, bool) {
	var out [6]float32
	arr, ok := d.GetArray(dict, key)
	if !ok || len(arr) != 6 {
		return out, false
	}
	for i, entry := range arr {
		v, numOK := cos.AsReal(d.Resolve(entry))
		if !numOK || !isFinitePt(float32(v), 0) {
			return out, false
		}
		out[i] = float32(v)
	}
	return out, true
}

// rectFrom reads dict[key] as a normalized rectangle.
func rectFrom(d *cos.Document, dict cos.Dict, key cos.Name) (gfx.Rect, bool) {
	arr, ok := d.GetArray(dict, key)
	if !ok || len(arr) < 4 {
		return gfx.Rect{}, false
	}
	var vals [4]float32
	for i := range vals {
		v, numOK := cos.AsReal(d.Resolve(arr[i]))
		if !numOK || !isFinitePt(float32(v), 0) {
			return gfx.Rect{}, false
		}
		vals[i] = float32(v)
	}
	return gfx.Rect{X0: vals[0], Y0: vals[1], X1: vals[2], Y1: vals[3]}.Normalize(), true
}
