package content

import (
	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// op dispatches one operator. Operators with missing or mistyped operands are skipped, as are unknown ones —
// the operand list is discarded either way by the caller, which is the viewer-conventional recovery that keeps
// hostile or sloppy content from desynchronizing anything.
//
//nolint:gocyclo // A flat dispatch table over the content operator set; a map of closures would just hide the same fan-out.
func (in *interp) op(word string) {
	if in.t3Shape == t3Mask {
		// After d1, a Type 3 charproc is a pure shape: its color operators are ignored so the caller's fill
		// color paints the glyph (ISO 32000-2 9.6.4).
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
			in.gs.ctm = gfx.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}.Mul(in.gs.ctm)
		}
	case "w":
		if v, ok := in.float1(); ok && v >= 0 {
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
		if v, ok := in.float1(); ok {
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
			in.gs.fillSpace, in.gs.fillComps = pdfcolor.DeviceGray, v
		}
	case "G":
		if v, ok := in.floats(1); ok {
			in.gs.strokeSpace, in.gs.strokeComps = pdfcolor.DeviceGray, v
		}
	case "rg":
		if v, ok := in.floats(3); ok {
			in.gs.fillSpace, in.gs.fillComps = pdfcolor.DeviceRGB, v
		}
	case "RG":
		if v, ok := in.floats(3); ok {
			in.gs.strokeSpace, in.gs.strokeComps = pdfcolor.DeviceRGB, v
		}
	case "k":
		if v, ok := in.floats(4); ok {
			in.gs.fillSpace, in.gs.fillComps = pdfcolor.DeviceCMYK, v
		}
	case "K":
		if v, ok := in.floats(4); ok {
			in.gs.strokeSpace, in.gs.strokeComps = pdfcolor.DeviceCMYK, v
		}
	case "cs":
		if name, ok := in.name1(); ok {
			if space, spaceOK := in.colorSpace(name); spaceOK {
				in.gs.fillSpace, in.gs.fillComps = space, space.Initial()
			} else {
				// The viewer-conventional fallback for an unresolvable space: gray black.
				in.gs.fillSpace, in.gs.fillComps = pdfcolor.DeviceGray, pdfcolor.DeviceGray.Initial()
			}
		}
	case "CS":
		if name, ok := in.name1(); ok {
			if space, spaceOK := in.colorSpace(name); spaceOK {
				in.gs.strokeSpace, in.gs.strokeComps = space, space.Initial()
			} else {
				in.gs.strokeSpace, in.gs.strokeComps = pdfcolor.DeviceGray, pdfcolor.DeviceGray.Initial()
			}
		}
	case "sc", "scn":
		in.gs.fillComps = in.componentsFor(in.gs.fillSpace)
	case "SC", "SCN":
		in.gs.strokeComps = in.componentsFor(in.gs.strokeSpace)

	// ---- XObjects and shadings ----
	case "Do":
		in.opDo()
	case "sh":
		// Shadings paint at M8; recognized and skipped until then.

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

// opRestore implements Q. Restores are ignored when they would cross the executing stream's boundary (qFloor)
// or match an ignored overflow push.
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

// paintPath implements the path-painting operators: optional close, fill, stroke, then the deferred W/W* clip,
// and finally the path reset. Fill precedes stroke (B and friends), which matters under transparency.
func (in *interp) paintPath(fill, evenOdd, stroke, closeFirst bool) {
	if closeFirst && in.hasCur {
		in.path.Close()
		in.cur = in.start
	}
	if !in.path.IsEmpty() {
		if fill && in.marks(in.gs.fillSpace) {
			in.dev.FillPath(in.path, evenOdd, in.gs.ctm, in.fillPaint())
		}
		if stroke && in.marks(in.gs.strokeSpace) {
			in.dev.StrokePath(in.path, &in.gs.sp, in.gs.ctm, in.strokePaint())
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

// marks reports whether painting with the space produces marks at this milestone. Pattern paints arrive with
// M8; Separation /None never marks by definition.
func (in *interp) marks(space pdfcolor.Space) bool {
	if _, isPattern := space.(*pdfcolor.Pattern); isPattern {
		return false
	}
	return true
}

func (in *interp) fillPaint() device.Paint {
	return device.Paint{
		Color: in.gs.fillSpace.ToNRGBA(in.gs.fillComps),
		Alpha: in.gs.fillAlpha,
		Blend: in.gs.blend,
	}
}

func (in *interp) strokePaint() device.Paint {
	return device.Paint{
		Color: in.gs.strokeSpace.ToNRGBA(in.gs.strokeComps),
		Alpha: in.gs.strokeAlpha,
		Blend: in.gs.blend,
	}
}

// componentsFor implements sc/scn/SC/SCN: the leading numeric operands, at most the space's component count
// (uncolored-pattern operands may be followed by a pattern name, which M8 will consume; it is ignored here).
func (in *interp) componentsFor(space pdfcolor.Space) []float32 {
	maxN := space.NComponents()
	if maxN == 0 {
		return nil
	}
	return in.leadingFloats(maxN)
}

// opDash implements d: a dash array plus phase. The array is truncated to maxDashEntries and non-finite or
// negative entries invalidate it (rendered solid), the leniency deployed viewers apply; deeper sanitization
// (odd counts, all-zero patterns) is the raster device's concern.
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
	in.gs.sp.Dash = dash
	in.gs.sp.DashPhase = float32(phase)
}

// opExtGState implements gs: apply the M4 subset of an ExtGState dictionary (line parameters, dash, constant
// alpha, blend mode). The remaining entries (font, soft mask, transfer functions, ...) are ignored until their
// milestones.
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
		in.gs.sp.Width = float32(v)
	}
	if v, has := in.doc.GetInt(dict, "LC"); has && v >= 0 && v <= 2 {
		in.gs.sp.Cap = gfx.LineCap(v)
	}
	if v, has := in.doc.GetInt(dict, "LJ"); has && v >= 0 && v <= 2 {
		in.gs.sp.Join = gfx.LineJoin(v)
	}
	if v, has := d64(in.doc, dict, "ML"); has {
		in.gs.sp.MiterLimit = float32(v)
	}
	if arr, has := in.doc.GetArray(dict, "D"); has && len(arr) == 2 {
		saved := in.operands
		in.operands = []cos.Object{in.doc.Resolve(arr[0]), in.doc.Resolve(arr[1])}
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
}

// d64 resolves dict[key] as a float64.
func d64(d *cos.Document, dict cos.Dict, key cos.Name) (float64, bool) {
	return cos.AsReal(d.Resolve(dict[key]))
}

// blendFor maps a /BM value (a name, or an array whose first supported name wins) to a blend mode.
// Unrecognized names mean Normal, as the standard requires.
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
	body, err := in.doc.StreamData(stream)
	if err != nil {
		return
	}
	// A form executes like q; cm /Matrix; W-clip /BBox; its content; Q — with its own resources and a fresh
	// per-stream state (operand list, path, pending clip).
	if len(in.gsStack) >= maxQDepth {
		return // No room to save state; skipping the form entirely is the only balanced choice.
	}
	in.opSave()
	if v, has := numbers6(in.doc, stream.Dict, "Matrix"); has {
		in.gs.ctm = gfx.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}.Mul(in.gs.ctm)
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
	in.spaces = append(in.spaces, map[cos.Name]pdfcolor.Space{})
	savedPath, savedCur, savedStart, savedHasCur, savedPending := in.path, in.cur, in.start, in.hasCur, in.pending
	in.path, in.hasCur, in.pending = &gfx.Path{}, false, clipNone
	in.formDepth++
	in.exec(body)
	in.formDepth--
	in.path, in.cur, in.start, in.hasCur, in.pending = savedPath, savedCur, savedStart, savedHasCur, savedPending
	in.spaces = in.spaces[:len(in.spaces)-1]
	in.res = in.res[:len(in.res)-1]
	in.opRestore()
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
