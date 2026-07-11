package content

import (
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Text objects and show operators (ISO 32000-2 9.4). The composed per-glyph matrix is
//
//	Trm = [Tfs·Th 0, 0 Tfs, 0 Ts] · Tm · CTM
//
// with the glyph advance applied to Tm after each glyph: tx = ((w0 − TJ/1000)·Tfs + Tc + Tw)·Th, where Tw
// applies only to single-byte code 32. Text-state parameters live in the graphics state (textParams); Tm/Tlm
// live on the interpreter and reset at BT. Ops arriving outside BT..ET are processed against the stream's
// initial identity matrices, the leniency deployed viewers apply to sloppy content.

// opBeginText implements BT.
func (in *interp) opBeginText() {
	if in.inText {
		in.opEndText() // A nested BT force-closes the previous text object (hostile or sloppy content).
	}
	in.inText = true
	in.tm, in.tlm = gfx.Identity(), gfx.Identity()
}

// opEndText implements ET, and force-closes truncated text objects at stream end. Any accumulated text-clip
// runs are finalized into a single device clip level, popped by Q/unwind like every other clip.
func (in *interp) opEndText() {
	if in.textClipRuns > 0 {
		in.dev.EndTextClip()
		in.gs.clips++
		in.textClipRuns = 0
	}
	in.inText = false
}

// opTf implements Tf. Like the oracle, a failed font load aborts the operator and keeps the previous font
// and size (its interpreter drops the operator on error); text then continues in the prior font.
func (in *interp) opTf() {
	if len(in.operands) < 2 {
		return
	}
	name, ok := cos.AsName(in.operands[0])
	if !ok {
		return
	}
	size, ok := cos.AsReal(in.operands[1])
	if !ok || !isFinitePt(float32(size), 0) {
		return
	}
	f, ok := in.loadFont(name)
	if !ok {
		return
	}
	in.gs.text.font = f
	in.gs.text.size = float32(size)
}

// loadFont resolves /Resources /Font <name> and loads it, caching per reference for this Run (failures are
// cached as nil so hostile content cannot force repeated parses).
func (in *interp) loadFont(name cos.Name) (*font.Font, bool) {
	raw, ok := in.resource("Font", name)
	if !ok {
		return nil, false
	}
	ref, isRef := raw.(cos.Ref)
	if isRef {
		if f, cached := in.fonts[ref]; cached {
			return f, f != nil
		}
	}
	var f *font.Font
	if dict, isDict := cos.AsDict(in.doc.Resolve(raw)); isDict {
		if loaded, err := font.Load(in.doc, dict); err == nil {
			f = loaded
		}
	}
	if isRef && len(in.fonts) < maxCachedFonts {
		in.fonts[ref] = f
	}
	return f, f != nil
}

// textMove implements Td (and the T*/'/" leading moves): translate the line matrix and restart the text
// matrix from it.
func (in *interp) textMove(tx, ty float32) {
	if !isFinitePt(tx, ty) {
		return
	}
	in.tlm = gfx.Translate(tx, ty).Mul(in.tlm)
	in.tm = in.tlm
}

// opTm implements Tm.
func (in *interp) opTm() {
	v, ok := in.floats(6)
	if !ok {
		return
	}
	m := gfx.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}
	if !m.IsFinite() {
		return
	}
	in.tlm = m
	in.tm = m
}

// opShowString implements Tj: show the single string operand as one run.
func (in *interp) opShowString() {
	s, ok := in.string1()
	if !ok {
		return
	}
	run := in.newRun()
	if run == nil {
		return
	}
	in.appendGlyphs(run, s)
	in.emitRun(run)
}

// opTJ implements TJ: strings show, numbers kick the text matrix by −n/1000 in text space. One run spans the
// whole array.
func (in *interp) opTJ() {
	if len(in.operands) < 1 {
		return
	}
	arr, ok := in.operands[0].(cos.Array)
	if !ok {
		return
	}
	run := in.newRun()
	if run == nil {
		return
	}
	ts := &in.gs.text
	for _, el := range arr {
		if s, isStr := el.(cos.String); isStr {
			in.appendGlyphs(run, s)
			continue
		}
		if n, isNum := cos.AsReal(el); isNum && isFinitePt(float32(n), 0) {
			tx := float32(-n) / 1000 * ts.size * ts.scale
			in.tm = gfx.Translate(tx, 0).Mul(in.tm)
		}
	}
	in.emitRun(run)
}

// opNextLineShow implements ' (move to next line, then show).
func (in *interp) opNextLineShow() {
	in.textMove(0, -in.gs.text.leading)
	in.opShowString()
}

// opSpacedShow implements " (set word and character spacing, move to next line, then show). The operands are
// aw ac string.
func (in *interp) opSpacedShow() {
	if len(in.operands) < 3 {
		return
	}
	aw, okW := cos.AsReal(in.operands[0])
	ac, okC := cos.AsReal(in.operands[1])
	if !okW || !okC || !isFinitePt(float32(aw), float32(ac)) {
		return
	}
	in.gs.text.wordSpacing = float32(aw)
	in.gs.text.charSpacing = float32(ac)
	in.operands = in.operands[2:] // The string becomes the leading operand for the ' behavior.
	in.opNextLineShow()
}

// newRun starts a text run for the current font, or nil when no usable font or matrix is in effect (the
// show operator is then skipped entirely, matching the oracle's no-font recovery).
func (in *interp) newRun() *device.TextRun {
	ts := &in.gs.text
	if ts.font == nil || !in.tm.IsFinite() || !in.gs.ctm.IsFinite() {
		return nil
	}
	return &device.TextRun{Font: ts.font, WMode: ts.font.WMode()}
}

// appendGlyphs decodes one string operand into positioned glyphs, advancing the text matrix per glyph. The
// glyph count drains the per-Run operator budget so huge strings cannot amplify work unboundedly.
func (in *interp) appendGlyphs(run *device.TextRun, s []byte) {
	ts := &in.gs.text
	ts.font.ForEachCode(s, func(code uint32, oneByte bool) bool {
		if in.budget < 0 {
			return false
		}
		in.budget--
		trm := gfx.Matrix{A: ts.size * ts.scale, D: ts.size, F: ts.rise}.Mul(in.tm).Mul(in.gs.ctm)
		w0 := ts.font.Width(code)
		if trm.IsFinite() {
			run.Glyphs = append(run.Glyphs, device.Glyph{
				Trm:     trm,
				Code:    code,
				Unicode: ts.font.Unicode(code),
				Advance: w0,
			})
		}
		tx := w0*ts.size + ts.charSpacing
		if oneByte && code == 32 {
			tx += ts.wordSpacing
		}
		in.tm = gfx.Translate(tx*ts.scale, 0).Mul(in.tm)
		return true
	})
}

// emitRun dispatches a completed run per the text render mode (ISO 32000-2 9.3.6): modes 0-2 paint, 3 is
// recorded but invisible, and 4-7 additionally (or only) accumulate the text clip. Pattern color spaces do
// not paint until M8, exactly like path painting.
func (in *interp) emitRun(run *device.TextRun) {
	if run == nil || len(run.Glyphs) == 0 {
		return
	}
	mode := in.gs.text.mode
	fill := mode == 0 || mode == 2 || mode == 4 || mode == 6
	stroke := mode == 1 || mode == 2 || mode == 5 || mode == 6
	if fill && in.marks(in.gs.fillSpace) {
		in.dev.FillText(run, in.fillPaint())
	}
	if stroke && in.marks(in.gs.strokeSpace) {
		in.dev.StrokeText(run, &in.gs.sp, in.strokePaint())
	}
	if mode == 3 {
		in.dev.IgnoreText(run)
	}
	if mode >= 4 {
		in.dev.ClipText(run)
		in.textClipRuns++
	}
}

// string1 returns the single leading string operand.
func (in *interp) string1() (cos.String, bool) {
	if len(in.operands) < 1 {
		return nil, false
	}
	s, ok := in.operands[0].(cos.String)
	return s, ok
}
