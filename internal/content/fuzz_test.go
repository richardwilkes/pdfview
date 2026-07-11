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
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
	"github.com/richardwilkes/pdfview/internal/store"
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

func (b *balanceDevice) FillPath(_ *gfx.Path, _ bool, _ gfx.Matrix, paint device.Paint) {
	b.replayTiling(paint)
}

func (b *balanceDevice) StrokePath(_ *gfx.Path, _ *gfx.StrokeParams, _ gfx.Matrix, paint device.Paint) {
	b.replayTiling(paint)
}
func (b *balanceDevice) ClipPath(*gfx.Path, bool, gfx.Matrix) { b.depth++ }

// replayTiling exercises a tiling paint's Replay closure exactly like the raster device would, so the child
// interpreter it spawns (with its recursion guards and shared work budget) is under fuzz too.
func (b *balanceDevice) replayTiling(paint device.Paint) {
	if paint.Tiling != nil {
		paint.Tiling.Replay(b, gfx.Identity())
	}
}

func (b *balanceDevice) ClipStrokePath(*gfx.Path, *gfx.StrokeParams, gfx.Matrix) { b.depth++ }
func (b *balanceDevice) FillText(run *device.TextRun, _ device.Paint)            { walkGlyphs(run) }

func (b *balanceDevice) StrokeText(run *device.TextRun, _ *gfx.StrokeParams, _ device.Paint) {
	walkGlyphs(run)
}

func (b *balanceDevice) ClipText(run *device.TextRun)   { walkGlyphs(run) }
func (b *balanceDevice) EndTextClip()                   { b.depth++ }
func (b *balanceDevice) IgnoreText(run *device.TextRun) { walkGlyphs(run) }

// walkGlyphs pulls each glyph's outline exactly like the raster device would, so the fuzzer reaches the
// outline-extraction path (which must degrade, never panic, on hostile font programs).
func walkGlyphs(run *device.TextRun) {
	for i := range run.Glyphs {
		run.Font.GlyphPath(run.Glyphs[i].GID)
	}
}
func (b *balanceDevice) FillImage(*imaging.Image, gfx.Matrix, float64)          {}
func (b *balanceDevice) FillImageMask(*imaging.Image, gfx.Matrix, device.Paint) {}
func (b *balanceDevice) ClipImageMask(*imaging.Image, gfx.Matrix)               { b.depth++ }

func (b *balanceDevice) PopClip()                                               { b.pop() }
func (b *balanceDevice) BeginGroup(gfx.Rect, bool, bool, device.Blend, float64) {}
func (b *balanceDevice) EndGroup()                                              {}
func (b *balanceDevice) BeginMask(gfx.Rect, bool, color.NRGBA)                  {}
func (b *balanceDevice) EndMask()                                               {}
func (b *balanceDevice) PopMask()                                               {}
func (b *balanceDevice) FillShading(*shading.Shading, gfx.Matrix, device.Paint) {}

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
7 0 obj
<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>
endobj
8 0 obj
<< /Type /Font /Subtype /TrueType /BaseFont /Junk-Bold /FirstChar 60 /Widths [500 600 700]
   /Encoding << /BaseEncoding /WinAnsiEncoding /Differences [60 /alpha /bogusname 300 /x] >>
   /FontDescriptor 9 0 R >>
endobj
9 0 obj
<< /Type /FontDescriptor /FontName /Junk-Bold /Flags 4 /MissingWidth 250 /Ascent 700 /Descent -200
   /FontFile2 10 0 R >>
endobj
10 0 obj
<< /Length 16 >>
stream
notarealsfnt0000
endstream
endobj
11 0 obj
<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 50 0] /Function 6 0 R /Extend [true false] >>
endobj
12 0 obj
<< /PatternType 2 /Shading 11 0 R /Matrix [1 0 0 1 2 3] >>
endobj
13 0 obj
<< /PatternType 1 /PaintType 1 /BBox [0 0 6 6] /XStep 5 /YStep 5
   /Resources << /Pattern << /P1 13 0 R >> >> /Length 45 >>
stream
0 0 3 3 re f /Pattern cs /P1 scn 1 1 4 4 re f
endstream
endobj
14 0 obj
<< /ShadingType 4 /ColorSpace /DeviceRGB /BitsPerCoordinate 8 /BitsPerComponent 8 /BitsPerFlag 8
   /Decode [0 100 0 100 0 1 0 1 0 1] /Filter /ASCIIHexDecode /Length 37 >>
stream
000000ff00000064000030ff000064ffff00>
endstream
endobj
trailer
<< /Root 1 0 R /Size 15 >>
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
		catColorSpc:  cos.Dict{"CS0": cos.Ref{Num: 4}, "CS1": cos.Ref{Num: 5}},
		"Font":       cos.Dict{"F1": cos.Ref{Num: 7}, "F2": cos.Ref{Num: 8}},
		"Shading":    cos.Dict{"SH1": cos.Ref{Num: 11}, "SH2": cos.Ref{Num: 14}},
		catPattern:   cos.Dict{"P1": cos.Ref{Num: 13}, "P2": cos.Ref{Num: 12}},
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
	f.Add([]byte("BT /F2 9 Tf 110 Tz 1 Tc 2 Tw 14 TL 3 Ts 40 700 Td [(ab) -500 (<=>)] TJ T* (x) ' 4 5 (y) \" ET"))
	f.Add([]byte("BT 7 Tr /F1 8 Tf 1 0 0 1 40 40 Tm (clip me) Tj ET 0 0 9 9 re f"))
	f.Add([]byte("BT 4 Tr /F2 8 Tf (fill+clip) Tj q ET Q BT 2 Tr (fs) Tj"))
	f.Add([]byte("[3 1] 0.5 d 2 w 1 J 0 0 m 5 5 l 10 0 l S"))
	f.Add([]byte("Q Q Q W W* n"))
	f.Add([]byte("/SH1 sh /SH2 sh"))
	f.Add([]byte("/Pattern cs /P2 scn 0 0 20 20 re f /Pattern CS /P2 SCN 0 0 m 9 9 l S"))
	f.Add([]byte("/Pattern cs /P1 scn 0 0 20 20 re f BT /F1 8 Tf (pat) Tj ET"))
	doc, res := fuzzResources()
	f.Fuzz(func(t *testing.T, data []byte) {
		dev := &balanceDevice{}
		// Alternate between a tiny budgeted store (constant eviction) and none (per-Run caches) so both
		// cache layers fuzz.
		var st *store.Store
		if len(data)%2 == 0 {
			st = store.New(256)
		}
		Run(doc, res, data, gfx.Matrix{A: 1.5, D: -1.5, F: 100}, dev, st)
		if dev.depth != 0 {
			t.Fatalf("clip depth %d after Run", dev.depth)
		}
	})
}
