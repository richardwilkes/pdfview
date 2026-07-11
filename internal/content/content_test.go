package content

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// Recorded operation names.
const (
	opFill    = "fill"
	opStroke  = "stroke"
	opClip    = "clip"
	opPopClip = "popclip"
)

// Resource names shared across the tests and the fuzz harness.
const (
	resFormName  cos.Name = "Fm0"
	resGSName    cos.Name = "GS0"
	catXObject   cos.Name = "XObject"
	catExtGState cos.Name = "ExtGState"
)

// call is one recorded device call.
type call struct {
	op      string
	path    *gfx.Path
	paint   device.Paint
	sp      gfx.StrokeParams
	ctm     gfx.Matrix
	evenOdd bool
}

// recorder records device calls and enforces the push/pop balance contract.
type recorder struct {
	t     *testing.T
	calls []call
	depth int
}

func (r *recorder) add(c *call) { r.calls = append(r.calls, *c) }

func (r *recorder) FillPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix, paint device.Paint) {
	r.add(&call{op: opFill, path: p.Clone(), evenOdd: evenOdd, ctm: ctm, paint: paint})
}

func (r *recorder) StrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix, paint device.Paint) {
	r.add(&call{op: opStroke, path: p.Clone(), sp: sp.Clone(), ctm: ctm, paint: paint})
}

func (r *recorder) ClipPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix) {
	r.depth++
	r.add(&call{op: opClip, path: p.Clone(), evenOdd: evenOdd, ctm: ctm})
}

func (r *recorder) ClipStrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix) {
	r.depth++
	r.add(&call{op: "clipstroke", path: p.Clone(), sp: sp.Clone(), ctm: ctm})
}

func (r *recorder) PopClip() {
	r.depth--
	if r.depth < 0 {
		r.t.Fatal("PopClip underflow: the interpreter broke the balance contract")
	}
	r.add(&call{op: opPopClip})
}

func (r *recorder) FillText(*device.TextRun, device.Paint)                      {}
func (r *recorder) StrokeText(*device.TextRun, *gfx.StrokeParams, device.Paint) {}
func (r *recorder) ClipText(*device.TextRun)                                    {}
func (r *recorder) IgnoreText(*device.TextRun)                                  {}
func (r *recorder) FillImage(*imaging.Image, gfx.Matrix, float64)               {}
func (r *recorder) FillImageMask(*imaging.Image, gfx.Matrix, device.Paint)      {}

func (r *recorder) ClipImageMask(*imaging.Image, gfx.Matrix) {
	r.depth++
	r.add(&call{op: "clipimagemask"})
}

func (r *recorder) BeginGroup(gfx.Rect, bool, bool, device.Blend, float64) {}
func (r *recorder) EndGroup()                                              {}
func (r *recorder) BeginMask(gfx.Rect, bool, color.NRGBA)                  {}
func (r *recorder) EndMask()                                               {}
func (r *recorder) PopMask()                                               {}
func (r *recorder) FillShading(*shading.Shading, gfx.Matrix, float64)      {}

// run interprets content against a fresh recorder, with an optional document/resources pair.
func run(t *testing.T, d *cos.Document, resources cos.Dict, content string) *recorder {
	t.Helper()
	if d == nil {
		var err error
		if d, err = cos.Open([]byte(minimalPDF("<< >>"))); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{t: t}
	Run(d, resources, []byte(content), gfx.Identity(), rec)
	if rec.depth != 0 {
		t.Fatalf("device clip depth %d after Run; the auto-unwind failed", rec.depth)
	}
	return rec
}

// minimalPDF wraps object bodies ("1 0 obj ... endobj" fragments) into a parseable document.
func minimalPDF(bodies ...string) string {
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, body := range bodies {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Catalog >>\nendobj\n", len(bodies)+1)
	fmt.Fprintf(&b, "trailer\n<< /Root %d 0 R /Size %d >>\nstartxref\n0\n%%%%EOF\n", len(bodies)+1, len(bodies)+2)
	return b.String()
}

func ops(rec *recorder) []string {
	out := make([]string, len(rec.calls))
	for i := range rec.calls {
		out[i] = rec.calls[i].op
	}
	return out
}

func wantOps(t *testing.T, rec *recorder, want ...string) {
	t.Helper()
	got := ops(rec)
	if len(got) != len(want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ops = %v, want %v", got, want)
		}
	}
}

func TestPathOpsAndPainting(t *testing.T) {
	rec := run(t, nil, nil, "10 20 m 30 40 l 1 2 3 4 5 6 c 7 8 9 10 v 1 2 3 4 y h S")
	wantOps(t, rec, opStroke)
	p := rec.calls[0].path
	wantVerbs := []gfx.PathVerb{gfx.MoveTo, gfx.LineTo, gfx.CubicTo, gfx.CubicTo, gfx.CubicTo, gfx.ClosePath}
	if len(p.Verbs) != len(wantVerbs) {
		t.Fatalf("verbs %v", p.Verbs)
	}
	for i, v := range wantVerbs {
		if p.Verbs[i] != v {
			t.Fatalf("verb %d = %d want %d", i, p.Verbs[i], v)
		}
	}
	// Points: [0] m, [1] l, [2..4] c, [5..7] v, [8..10] y.
	if p.Points[2] != (gfx.Point{X: 1, Y: 2}) || p.Points[4] != (gfx.Point{X: 5, Y: 6}) {
		t.Errorf("cubic points wrong: %v", p.Points)
	}
	if p.Points[5] != (gfx.Point{X: 5, Y: 6}) { // v's first control = current point
		t.Errorf("v control: %v", p.Points[5])
	}
	if p.Points[9] != (gfx.Point{X: 3, Y: 4}) || p.Points[10] != (gfx.Point{X: 3, Y: 4}) {
		t.Errorf("y controls: %v", p.Points[8:11])
	}
}

func TestFillModesAndCombined(t *testing.T) {
	rec := run(t, nil, nil, "0 0 10 10 re f 0 0 10 10 re f* 0 0 10 10 re B* 0 0 10 10 re n")
	wantOps(t, rec, opFill, opFill, opFill, opStroke)
	if rec.calls[0].evenOdd || !rec.calls[1].evenOdd || !rec.calls[2].evenOdd {
		t.Error("even-odd flags wrong")
	}
}

func TestColorOperators(t *testing.T) {
	rec := run(t, nil, nil, `1 0 0 rg 0 0 1 1 re f
0.5 g 0 0 1 1 re f
0 0 0.8 0 k 0 0 1 1 re f
0 0 1 RG 0 0 1 1 re S`)
	wantOps(t, rec, opFill, opFill, opFill, opStroke)
	if got := rec.calls[0].paint.Color; got != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("rg: %v", got)
	}
	if got := rec.calls[1].paint.Color; got != (color.NRGBA{R: 127, G: 127, B: 127, A: 255}) {
		t.Errorf("0.5 g: %v (must match the oracle's observed 127)", got)
	}
	// The CMYK anchor observed from the vectors golden: (0, 0, 0.8, 0) -> (255, 243, 79).
	if got := rec.calls[2].paint.Color; got != (color.NRGBA{R: 255, G: 243, B: 79, A: 255}) {
		t.Errorf("k: %v (oracle says 255,243,79)", got)
	}
	if got := rec.calls[3].paint.Color; got != (color.NRGBA{B: 255, A: 255}) {
		t.Errorf("RG: %v", got)
	}
}

func TestSaveRestore(t *testing.T) {
	rec := run(t, nil, nil, `1 0 0 rg 2 0 0 2 0 0 cm q 0 1 0 rg 1 0 0 1 5 5 cm 0 0 1 1 re f Q 0 0 1 1 re f`)
	wantOps(t, rec, opFill, opFill)
	inner := rec.calls[0]
	if inner.paint.Color != (color.NRGBA{G: 255, A: 255}) {
		t.Errorf("inner color: %v", inner.paint.Color)
	}
	if inner.ctm != (gfx.Matrix{A: 2, D: 2, E: 10, F: 10}) {
		t.Errorf("inner ctm: %v", inner.ctm)
	}
	outer := rec.calls[1]
	if outer.paint.Color != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("Q did not restore color: %v", outer.paint.Color)
	}
	if outer.ctm != (gfx.Matrix{A: 2, D: 2}) {
		t.Errorf("Q did not restore ctm: %v", outer.ctm)
	}
}

func TestClipEmittedAfterPaint(t *testing.T) {
	rec := run(t, nil, nil, "0 0 10 10 re W n 0 0 5 5 re f")
	wantOps(t, rec, opClip, opFill, opPopClip)
	if rec.calls[0].evenOdd {
		t.Error("W produced an even-odd clip")
	}
	rec = run(t, nil, nil, "0 0 10 10 re W* f 0 0 5 5 re f")
	wantOps(t, rec, opFill, opClip, opFill, opPopClip)
	if !rec.calls[1].evenOdd {
		t.Error("W* lost its even-odd flag")
	}
}

func TestClipRestoredByQ(t *testing.T) {
	rec := run(t, nil, nil, "q 0 0 10 10 re W n Q 0 0 5 5 re f")
	wantOps(t, rec, opClip, opPopClip, opFill)
}

func TestUnbalancedSaveUnwinds(t *testing.T) {
	rec := run(t, nil, nil, "q q 0 0 10 10 re W n q 1 0 0 rg")
	// Three clips? No: one W clip at depth 3; stream ends; unwind must pop it exactly once.
	wantOps(t, rec, opClip, opPopClip)
}

func TestUnbalancedRestoreIgnored(t *testing.T) {
	rec := run(t, nil, nil, "Q Q Q 1 0 0 rg 0 0 1 1 re f")
	wantOps(t, rec, opFill)
	if rec.calls[0].paint.Color != (color.NRGBA{R: 255, A: 255}) {
		t.Error("state damaged by underflowing Q")
	}
}

func TestUnknownOperatorSkipped(t *testing.T) {
	rec := run(t, nil, nil, "1 0 0 rg 42 frobnicate 0 0 1 1 re f")
	wantOps(t, rec, opFill)
	if rec.calls[0].paint.Color != (color.NRGBA{R: 255, A: 255}) {
		t.Error("unknown operator disturbed state")
	}
}

func TestOperandFloodStillPaints(t *testing.T) {
	var b strings.Builder
	for range 10000 {
		b.WriteString("7 ")
	}
	b.WriteString("frobnicate 0 0 10 10 re f")
	rec := run(t, nil, nil, b.String())
	wantOps(t, rec, opFill)
	p := rec.calls[0].path
	if p.Points[0] != (gfx.Point{X: 0, Y: 0}) || p.Points[2] != (gfx.Point{X: 10, Y: 10}) {
		t.Errorf("rect corrupted by operand flood: %v", p.Points)
	}
}

func TestTextObjectSkippedSafely(t *testing.T) {
	rec := run(t, nil, nil, `BT /F1 24 Tf 1 0 0 1 60 100 Tm (some (nested) text with ) escapes) Tj
[ (arrays) -250 (too) ] TJ T* ET 1 0 0 rg 0 0 1 1 re f`)
	wantOps(t, rec, opFill)
}

func TestInlineImageSkipped(t *testing.T) {
	// Without /L: the payload contains a lone EI-lookalike inside binary that lacks the delimiters, then a
	// real EI. With /L: length-led skip.
	rec := run(t, nil, nil, "BI /W 2 /H 2 /BPC 8 /CS /G ID \x00EIx\xff\x01 EI 0 0 1 1 re f")
	wantOps(t, rec, opFill)
	rec = run(t, nil, nil, "BI /W 1 /H 1 /BPC 8 /CS /G /L 4 ID \x00EI\x01 EI 0 0 2 2 re f")
	wantOps(t, rec, opFill)
	if rec.calls[0].path.Points[2] != (gfx.Point{X: 2, Y: 2}) {
		t.Error("content after length-led inline image mis-lexed")
	}
}

func TestDashAndLineParams(t *testing.T) {
	rec := run(t, nil, nil, "4 w 1 J 2 j 3.5 M [6 3] 1.5 d 0 0 m 10 10 l S")
	wantOps(t, rec, opStroke)
	sp := rec.calls[0].sp
	if sp.Width != 4 || sp.Cap != gfx.RoundCap || sp.Join != gfx.BevelJoin || sp.MiterLimit != 3.5 {
		t.Errorf("stroke params: %+v", sp)
	}
	if len(sp.Dash) != 2 || sp.Dash[0] != 6 || sp.Dash[1] != 3 || sp.DashPhase != 1.5 {
		t.Errorf("dash: %v phase %v", sp.Dash, sp.DashPhase)
	}
	// A negative dash entry invalidates the array (previous dash kept — here none).
	rec = run(t, nil, nil, "[6 -3] 0 d 0 0 m 10 10 l S")
	if sp = rec.calls[0].sp; len(sp.Dash) != 0 {
		t.Errorf("negative dash entries accepted: %v", sp.Dash)
	}
}

func TestExtGState(t *testing.T) {
	pdf := minimalPDF(`<< /Type /ExtGState /LW 7 /LC 1 /LJ 1 /ML 2.5 /D [[4 2] 1] /CA 0.25 /ca 0.5 /BM /Multiply >>`)
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catExtGState: cos.Dict{resGSName: cos.Ref{Num: 1}}}
	rec := run(t, d, res, "/GS0 gs 0 0 10 10 re B")
	wantOps(t, rec, opFill, opStroke)
	fill, stroke := rec.calls[0], rec.calls[1]
	if fill.paint.Alpha != 0.5 || stroke.paint.Alpha != 0.25 {
		t.Errorf("alphas: fill %v stroke %v", fill.paint.Alpha, stroke.paint.Alpha)
	}
	if fill.paint.Blend != device.BlendMultiply {
		t.Errorf("blend: %v", fill.paint.Blend)
	}
	sp := stroke.sp
	if sp.Width != 7 || sp.Cap != gfx.RoundCap || sp.Join != gfx.RoundJoin || sp.MiterLimit != 2.5 {
		t.Errorf("gs stroke params: %+v", sp)
	}
	if len(sp.Dash) != 2 || sp.Dash[0] != 4 || sp.Dash[1] != 2 || sp.DashPhase != 1 {
		t.Errorf("gs dash: %v phase %v", sp.Dash, sp.DashPhase)
	}
}

func TestColorSpaceResources(t *testing.T) {
	pdf := minimalPDF(`[ /Indexed /DeviceRGB 1 <FF0000 00FF00> ]`)
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{"ColorSpace": cos.Dict{"CS0": cos.Ref{Num: 1}}}
	rec := run(t, d, res, "/CS0 cs 1 sc 0 0 1 1 re f 0 0 1 1 re f")
	// First fill: index 1 -> green. Second fill: same color persists.
	wantOps(t, rec, opFill, opFill)
	if rec.calls[0].paint.Color != (color.NRGBA{G: 255, A: 255}) {
		t.Errorf("indexed color: %v", rec.calls[0].paint.Color)
	}
	// cs resets to the initial color (index 0 -> red).
	rec = run(t, d, res, "/CS0 cs 0 0 1 1 re f")
	if rec.calls[0].paint.Color != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("initial indexed color: %v", rec.calls[0].paint.Color)
	}
	// Unresolvable spaces fall back to gray black.
	rec = run(t, d, res, "/Missing cs 0 0 1 1 re f")
	if rec.calls[0].paint.Color != (color.NRGBA{A: 255}) {
		t.Errorf("fallback color: %v", rec.calls[0].paint.Color)
	}
}

func TestPatternSpaceSkipsPaint(t *testing.T) {
	rec := run(t, nil, nil, "/Pattern cs /P0 scn 0 0 10 10 re f 1 0 0 rg 0 0 1 1 re f")
	wantOps(t, rec, opFill) // Only the rg fill paints until patterns land at M8.
	if rec.calls[0].paint.Color != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("post-pattern color: %v", rec.calls[0].paint.Color)
	}
}

func TestFormXObject(t *testing.T) {
	form := `<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Matrix [2 0 0 2 0 0] /Length 24 >>
stream
1 0 0 rg 0 0 5 5 re f
endstream`
	d, err := cos.Open([]byte(minimalPDF(form)))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catXObject: cos.Dict{resFormName: cos.Ref{Num: 1}}}
	rec := run(t, d, res, "0 1 0 rg /Fm0 Do 0 0 1 1 re f")
	wantOps(t, rec, opClip, opFill, opPopClip, opFill)
	inner := rec.calls[1]
	if inner.ctm != (gfx.Matrix{A: 2, D: 2}) {
		t.Errorf("form matrix not applied: %v", inner.ctm)
	}
	if inner.paint.Color != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("form fill color: %v", inner.paint.Color)
	}
	after := rec.calls[3]
	if after.paint.Color != (color.NRGBA{G: 255, A: 255}) {
		t.Errorf("state leaked out of the form: %v", after.paint.Color)
	}
	if after.ctm != gfx.Identity() {
		t.Errorf("ctm leaked out of the form: %v", after.ctm)
	}
}

func TestFormCycleTerminates(t *testing.T) {
	form := `<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Resources << /XObject << /Self 1 0 R >> >> /Length 30 >>
stream
0 0 1 1 re f /Self Do
endstream`
	d, err := cos.Open([]byte(minimalPDF(form)))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catXObject: cos.Dict{resFormName: cos.Ref{Num: 1}}}
	rec := run(t, d, res, "/Fm0 Do")
	// The self-reference is cut by the cycle set after the first invocation: exactly one fill.
	fills := 0
	for _, c := range rec.calls {
		if c.op == opFill {
			fills++
		}
	}
	if fills != 1 {
		t.Errorf("self-referential form painted %d fills", fills)
	}
}

func TestFormResourcesRestored(t *testing.T) {
	form := `<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Length 1 >>
stream

endstream`
	d, err := cos.Open([]byte(minimalPDF(form)))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{
		catXObject:   cos.Dict{resFormName: cos.Ref{Num: 1}},
		catExtGState: cos.Dict{resGSName: cos.Dict{"ca": cos.Real(0.5)}},
	}
	// The page's ExtGState must still resolve after a form (with no own resources) ran.
	rec := run(t, d, res, "/Fm0 Do /GS0 gs 0 0 1 1 re f")
	last := rec.calls[len(rec.calls)-1]
	if last.op != opFill || last.paint.Alpha != 0.5 {
		t.Errorf("resources broken after form: %v alpha %v", last.op, last.paint.Alpha)
	}
}

func TestStrokeParamsIsolatedPerCall(t *testing.T) {
	// The recorded StrokeParams must not alias the interpreter's live state.
	rec := run(t, nil, nil, "[1 1] 0 d 0 0 m 5 5 l S [9 9] 0 d 0 0 m 5 5 l S")
	if rec.calls[0].sp.Dash[0] != 1 || rec.calls[1].sp.Dash[0] != 9 {
		t.Errorf("dash aliasing: %v then %v", rec.calls[0].sp.Dash, rec.calls[1].sp.Dash)
	}
}

func TestLineToWithoutCurrentPointIgnored(t *testing.T) {
	rec := run(t, nil, nil, "10 10 l 20 20 l f 0 0 m 10 10 l S")
	wantOps(t, rec, opStroke)
}
