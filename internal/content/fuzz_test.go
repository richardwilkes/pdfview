package content

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// balanceDevice panics on any push/pop violation, so the fuzzer surfaces balance bugs as crashes.
type balanceDevice struct {
	depth int
}

func (b *balanceDevice) pop() {
	b.depth--
	if b.depth < 0 {
		panic("device pop underflow")
	}
}

func (b *balanceDevice) FillPath(*gfx.Path, bool, gfx.Matrix, device.Paint)                {}
func (b *balanceDevice) StrokePath(*gfx.Path, *gfx.StrokeParams, gfx.Matrix, device.Paint) {}
func (b *balanceDevice) ClipPath(*gfx.Path, bool, gfx.Matrix)                              { b.depth++ }
func (b *balanceDevice) ClipStrokePath(*gfx.Path, *gfx.StrokeParams, gfx.Matrix)           { b.depth++ }
func (b *balanceDevice) FillText(*device.TextRun, device.Paint)                            {}
func (b *balanceDevice) StrokeText(*device.TextRun, *gfx.StrokeParams, device.Paint)       {}
func (b *balanceDevice) ClipText(*device.TextRun)                                          {}
func (b *balanceDevice) IgnoreText(*device.TextRun)                                        {}
func (b *balanceDevice) FillImage(*imaging.Image, gfx.Matrix, float64)                     {}
func (b *balanceDevice) FillImageMask(*imaging.Image, gfx.Matrix, device.Paint)            {}
func (b *balanceDevice) ClipImageMask(*imaging.Image, gfx.Matrix)                          { b.depth++ }
func (b *balanceDevice) PopClip()                                                          { b.pop() }
func (b *balanceDevice) BeginGroup(gfx.Rect, bool, bool, device.Blend, float64)            {}
func (b *balanceDevice) EndGroup()                                                         {}
func (b *balanceDevice) BeginMask(gfx.Rect, bool, color.NRGBA)                             {}
func (b *balanceDevice) EndMask()                                                          {}
func (b *balanceDevice) PopMask()                                                          {}
func (b *balanceDevice) FillShading(*shading.Shading, gfx.Matrix, float64)                 {}

// fuzzResourcePDF gives the fuzzer real resources to reach into: a self-referential form, an ExtGState, an
// Indexed color space, and a Separation with a calculator tint.
const fuzzResourcePDF = `%PDF-1.7
1 0 obj
<< /Type /Catalog >>
endobj
2 0 obj
<< /Type /XObject /Subtype /Form /BBox [0 0 50 50] /Matrix [1 0 0 1 5 5]
   /Resources << /XObject << /F 2 0 R >> >> /Length 26 >>
stream
0 0 10 10 re f q /F Do Q
endstream
endobj
3 0 obj
<< /Type /ExtGState /LW 3 /ca 0.5 /CA 0.25 /BM /Multiply /D [[2 2] 0] >>
endobj
4 0 obj
[ /Indexed /DeviceRGB 1 <FF000000FF00> ]
endobj
5 0 obj
[ /Separation /Spot /DeviceCMYK 6 0 R ]
endobj
6 0 obj
<< /FunctionType 4 /Domain [0 1] /Range [0 1 0 1 0 1 0 1] /Length 32 >>
stream
{ dup dup dup 0.5 mul exch pop }
endstream
endobj
trailer
<< /Root 1 0 R /Size 7 >>
startxref
0
%%EOF
`

func fuzzResources() (*cos.Document, cos.Dict) {
	d, err := cos.Open([]byte(fuzzResourcePDF))
	if err != nil {
		panic(err)
	}
	res := cos.Dict{
		catXObject:   cos.Dict{resFormName: cos.Ref{Num: 2}},
		catExtGState: cos.Dict{resGSName: cos.Ref{Num: 3}},
		"ColorSpace": cos.Dict{"CS0": cos.Ref{Num: 4}, "CS1": cos.Ref{Num: 5}},
	}
	return d, res
}

// FuzzContent drives the interpreter with arbitrary content streams against the canned resource set. The
// balance device turns any push/pop violation into a panic, and Run must neither panic nor hang: all work is
// cap-bounded (plan.md "Resource limits & robustness").
func FuzzContent(f *testing.F) {
	for _, name := range []string{"vectors.pdf", "rotate90.pdf"} {
		if data, err := os.ReadFile(filepath.Join("..", "..", "testfiles", "corpus", name)); err == nil {
			f.Add(data) // Whole files also lex as content: their operators are junk but exercise recovery.
		}
	}
	f.Add([]byte("q 1 0 0 rg 20 20 60 40 re f Q"))
	f.Add([]byte("q q q 0 0 10 10 re W n"))
	f.Add([]byte("/CS0 cs 1 sc 0 0 5 5 re f /CS1 cs 0.5 scn 0 0 5 5 re f"))
	f.Add([]byte("/GS0 gs 0 0 m 10 10 l S"))
	f.Add([]byte("/Fm0 Do"))
	f.Add([]byte("BI /W 2 /H 2 /BPC 8 /CS /G ID \x00\x01\x02\x03 EI 0 0 1 1 re f"))
	f.Add([]byte("BT /F1 12 Tf (text (nested) \\) here) Tj ET"))
	f.Add([]byte("[3 1] 0.5 d 2 w 1 J 0 0 m 5 5 l 10 0 l S"))
	f.Add([]byte("Q Q Q W W* n"))
	doc, res := fuzzResources()
	f.Fuzz(func(t *testing.T, data []byte) {
		dev := &balanceDevice{}
		Run(doc, res, data, gfx.Matrix{A: 1.5, D: -1.5, F: 100}, dev)
		if dev.depth != 0 {
			t.Fatalf("clip depth %d after Run", dev.depth)
		}
	})
}
