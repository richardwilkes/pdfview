// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package render

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/richardwilkes/canvas/raster"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/shading"
	"github.com/richardwilkes/pdfview/internal/store"
)

func newDevice(t *testing.T, w, h int) *Device {
	t.Helper()
	d, err := New(w, h)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func pixelAt(t *testing.T, pix []byte, stride, x, y int) [4]uint8 {
	t.Helper()
	i := y*stride + x*4
	return [4]uint8{pix[i], pix[i+1], pix[i+2], pix[i+3]}
}

func redPaint() device.Paint {
	return device.Paint{Color: color.NRGBA{R: 255, A: 255}, Alpha: 1}
}

func TestNewRejectsBadSizes(t *testing.T) {
	for _, size := range [][2]int{{0, 10}, {10, 0}, {-1, 5}} {
		if _, err := New(size[0], size[1]); err == nil {
			t.Errorf("size %v accepted", size)
		}
	}
}

func TestFillPathPixels(t *testing.T) {
	d := newDevice(t, 20, 20)
	var p gfx.Path
	p.Rect(5, 5, 10, 10)
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 10, 10); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("interior = %v", got)
	}
	if got := pixelAt(t, pix, stride, 2, 2); got != [4]uint8{0, 0, 0, 0} {
		t.Errorf("outside = %v (surface must start transparent)", got)
	}
}

func TestFillRespectsCTM(t *testing.T) {
	d := newDevice(t, 20, 20)
	var p gfx.Path
	p.Rect(0, 0, 5, 5)
	d.FillPath(&p, false, gfx.Translate(10, 10), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 12, 12); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("translated interior = %v", got)
	}
	if got := pixelAt(t, pix, stride, 2, 2); got[3] != 0 {
		t.Errorf("origin painted despite translation: %v", got)
	}
}

func TestAlphaPremultiplied(t *testing.T) {
	d := newDevice(t, 8, 8)
	var p gfx.Path
	p.Rect(0, 0, 8, 8)
	paint := redPaint()
	paint.Alpha = 0.5 // folded constant alpha: premul bytes must be scaled by coverage×alpha
	d.FillPath(&p, false, gfx.Identity(), paint)
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	got := pixelAt(t, pix, stride, 4, 4)
	if got[3] != 128 || got[0] != 128 || got[1] != 0 {
		t.Errorf("half-alpha premul pixel = %v", got)
	}
}

func TestClipRestrictsAndPops(t *testing.T) {
	d := newDevice(t, 20, 20)
	var clip gfx.Path
	clip.Rect(0, 0, 8, 20)
	d.ClipPath(&clip, false, gfx.Identity())
	var p gfx.Path
	p.Rect(0, 0, 20, 20)
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	d.PopClip()
	// After the pop, fills reach the whole surface again.
	var p2 gfx.Path
	p2.Rect(0, 12, 20, 8)
	d.FillPath(&p2, false, gfx.Identity(), device.Paint{Color: color.NRGBA{G: 255, A: 255}, Alpha: 1})
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 4, 4); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("inside clip = %v", got)
	}
	if got := pixelAt(t, pix, stride, 15, 4); got[3] != 0 {
		t.Errorf("outside clip painted: %v", got)
	}
	if got := pixelAt(t, pix, stride, 15, 15); got != [4]uint8{0, 255, 0, 255} {
		t.Errorf("after PopClip = %v", got)
	}
}

func TestStrokeAndDash(t *testing.T) {
	d := newDevice(t, 21, 40)
	var p gfx.Path
	p.MoveTo(10.5, 0)
	p.LineTo(10.5, 40)
	sp := gfx.StrokeParams{Width: 3, MiterLimit: 10}
	d.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 10, 20); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("stroke center = %v", got)
	}
	if got := pixelAt(t, pix, stride, 2, 20); got[3] != 0 {
		t.Errorf("far from stroke painted: %v", got)
	}

	// Dashed: on for 8, off for 8 — y=4 is on, y=12 is off.
	d2 := newDevice(t, 21, 40)
	sp.Dash = []float32{8, 8}
	d2.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err = d2.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 10, 4); got[3] == 0 {
		t.Error("dash 'on' segment missing")
	}
	if got := pixelAt(t, pix, stride, 10, 12); got[3] != 0 {
		t.Errorf("dash 'off' segment painted: %v", got)
	}
}

func TestOddDashDoubles(t *testing.T) {
	// A single-entry array [4] means on 4, off 4 (PDF's odd-count repetition).
	d := newDevice(t, 5, 32)
	var p gfx.Path
	p.MoveTo(2.5, 0)
	p.LineTo(2.5, 32)
	sp := gfx.StrokeParams{Width: 2, MiterLimit: 10, Dash: []float32{4}}
	d.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 2, 2); got[3] == 0 {
		t.Error("on segment missing")
	}
	if got := pixelAt(t, pix, stride, 2, 6); got[3] != 0 {
		t.Errorf("off segment painted: %v", got)
	}
}

func TestAllZeroDashIsSolid(t *testing.T) {
	d := newDevice(t, 5, 16)
	var p gfx.Path
	p.MoveTo(2.5, 0)
	p.LineTo(2.5, 16)
	sp := gfx.StrokeParams{Width: 2, MiterLimit: 10, Dash: []float32{0, 0}}
	d.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	for _, y := range []int{2, 8, 14} {
		if got := pixelAt(t, pix, stride, 2, y); got[3] == 0 {
			t.Errorf("all-zero dash gap at y=%d", y)
		}
	}
}

func TestHairline(t *testing.T) {
	d := newDevice(t, 9, 9)
	var p gfx.Path
	p.MoveTo(0, 4.5)
	p.LineTo(9, 4.5)
	sp := gfx.StrokeParams{Width: 0, MiterLimit: 10}
	d.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 4, 4); got[3] == 0 {
		t.Error("hairline drew nothing")
	}
}

// helveticaFont loads a substituted standard-14 Helvetica through the real font pipeline (rendering via the bundled
// Liberation Sans), giving the text tests genuine outlines without fixture files.
func helveticaFont(t *testing.T) *font.Font {
	t.Helper()
	var b strings.Builder
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")
	b.WriteString("2 0 obj\n<< /Type /Catalog >>\nendobj\n")
	b.WriteString("trailer\n<< /Root 2 0 R /Size 3 >>\nstartxref\n0\n%%EOF\n")
	d, err := cos.Open([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	dict, ok := cos.AsDict(d.LoadObject(1))
	if !ok {
		t.Fatal("font dict unavailable")
	}
	f, err := font.Load(d, dict)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// glyphRun builds a one-glyph run for code, with the glyph-space em box mapped to device space by trm.
func glyphRun(t *testing.T, f *font.Font, code uint32, trm, ctm gfx.Matrix) *device.TextRun {
	t.Helper()
	gid := f.GID(code)
	if gid == 0 {
		t.Fatalf("code %d unmapped", code)
	}
	return &device.TextRun{
		Font:   f,
		Glyphs: []device.Glyph{{Trm: trm, GID: gid, Code: code, Advance: f.Width(code)}},
		CTM:    ctm,
	}
}

// inkIn reports whether any pixel in the (inclusive) rectangle has nonzero alpha.
func inkIn(pix []byte, stride, x0, y0, x1, y1 int) bool {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if pix[y*stride+x*4+3] != 0 {
				return true
			}
		}
	}
	return false
}

func TestFillTextPixels(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	// Glyph space is y-up with the baseline at y=0: scale to 24 px/em and place the baseline at y=28.
	trm := gfx.Matrix{A: 24, D: -24}.Mul(gfx.Translate(2, 28))
	d.FillText(glyphRun(t, f, 'H', trm, gfx.Identity()), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	// The 24 px 'H' covers roughly x 4..17, y 11..28 (cap height ≈ 0.72 em): ink must exist there and the area above
	// the cap height must stay empty.
	if !inkIn(pix, stride, 3, 12, 18, 27) {
		t.Fatal("FillText drew nothing where the glyph belongs")
	}
	if inkIn(pix, stride, 0, 0, 31, 8) {
		t.Error("ink above the glyph's cap height")
	}
	// The counter between the stems (above the crossbar) must be empty: nonzero winding on real contours.
	if inkIn(pix, stride, 9, 13, 11, 15) {
		t.Error("ink inside the 'H' counter")
	}
}

func TestFillTextNotdefSubstituteDrawsNothing(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 16, 16)
	run := &device.TextRun{
		Font:   f,
		Glyphs: []device.Glyph{{Trm: gfx.Matrix{A: 12, D: -12, F: 14}, GID: 0}},
		CTM:    gfx.Identity(),
	}
	d.FillText(run, redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if inkIn(pix, stride, 0, 0, 15, 15) {
		t.Error("substituted .notdef painted")
	}
}

// inkTotal sums the alpha channel over the whole surface.
func inkTotal(pix []byte, stride, w, h int) int {
	total := 0
	for y := range h {
		for x := range w {
			total += int(pix[y*stride+x*4+3])
		}
	}
	return total
}

func TestStrokeTextPen(t *testing.T) {
	f := helveticaFont(t)
	// 'O' at 60 px/em: a hairline stroke inks only the two contour outlines, so its total coverage must be well under
	// the fill's ring area (a StrokeText that accidentally filled would match the fill's total).
	trm := gfx.Matrix{A: 60, D: -60}.Mul(gfx.Translate(4, 58))
	sp := gfx.StrokeParams{Width: 0, MiterLimit: 10}
	run := glyphRun(t, f, 'O', trm, gfx.Identity())

	dFill := newDevice(t, 64, 64)
	dFill.FillText(run, redPaint())
	fillPix, stride, err := dFill.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	fillInk := inkTotal(fillPix, stride, 64, 64)
	if fillInk == 0 {
		t.Fatal("fill reference drew nothing")
	}

	dStroke := newDevice(t, 64, 64)
	dStroke.StrokeText(run, &sp, redPaint())
	strokePix, _, err := dStroke.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	strokeInk := inkTotal(strokePix, stride, 64, 64)
	if strokeInk == 0 {
		t.Fatal("StrokeText drew nothing")
	}
	if strokeInk >= fillInk*3/5 {
		t.Errorf("hairline stroke ink %d vs fill ink %d; stroke looks like a fill", strokeInk, fillInk)
	}

	// A degenerate CTM must draw nothing (no meaningful pen exists).
	d2 := newDevice(t, 64, 64)
	d2.StrokeText(glyphRun(t, f, 'O', trm, gfx.Matrix{}), &sp, redPaint())
	pix2, _, err := d2.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if inkTotal(pix2, stride, 64, 64) != 0 {
		t.Error("degenerate CTM still painted")
	}
}

func TestTextClipRestrictsAndPops(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	trm := gfx.Matrix{A: 24, D: -24}.Mul(gfx.Translate(2, 28))
	d.ClipText(glyphRun(t, f, 'H', trm, gfx.Identity()))
	d.EndTextClip()
	var p gfx.Path
	p.Rect(0, 0, 32, 32)
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	d.PopClip()
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	// Ink only inside the glyph: the stems are covered, the region above the cap height is not, and the counter between
	// the stems stays empty.
	if !inkIn(pix, stride, 3, 12, 18, 27) {
		t.Fatal("text clip admitted no ink")
	}
	if inkIn(pix, stride, 0, 0, 31, 8) {
		t.Error("ink above the glyph within the text clip")
	}
	if inkIn(pix, stride, 9, 13, 11, 15) {
		t.Error("ink inside the 'H' counter within the text clip")
	}
	// After PopClip, painting reaches everywhere again.
	var p2 gfx.Path
	p2.Rect(0, 0, 4, 4)
	d.FillPath(&p2, false, gfx.Identity(), device.Paint{Color: color.NRGBA{G: 255, A: 255}, Alpha: 1})
	pix, stride, err = d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if !inkIn(pix, stride, 0, 0, 3, 3) {
		t.Error("PopClip did not restore the clip")
	}
}

func TestEmptyTextClipClipsEverything(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 16, 16)
	// A clip-text run whose glyphs produce no outlines (substituted .notdef) accumulates an empty region: the finalized
	// clip admits nothing, and PopClip restores.
	run := &device.TextRun{Font: f, Glyphs: []device.Glyph{{Trm: gfx.Matrix{A: 12, D: -12, F: 14}, GID: 0}}, CTM: gfx.Identity()}
	d.ClipText(run)
	d.EndTextClip()
	var p gfx.Path
	p.Rect(0, 0, 16, 16)
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if inkIn(pix, stride, 0, 0, 15, 15) {
		t.Error("empty text clip admitted ink")
	}
	d.PopClip()
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	pix, stride, err = d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if !inkIn(pix, stride, 0, 0, 15, 15) {
		t.Error("PopClip did not restore after empty text clip")
	}
}

func TestGlyphPathCacheReuse(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 8, 8)
	gid := f.GID('A')
	p1 := d.glyphPath(f, gid)
	p2 := d.glyphPath(f, gid)
	if p1 == nil || p1 != p2 {
		t.Errorf("glyph path not cached: %p vs %p", p1, p2)
	}
}

func TestGlyphPathStoreSharedAcrossRenders(t *testing.T) {
	f := helveticaFont(t)
	st := store.New(0)
	d1 := newDevice(t, 8, 8)
	d1.SetStore(st)
	d2 := newDevice(t, 8, 8)
	d2.SetStore(st)
	gid := f.GID('A')
	p1 := d1.glyphPath(f, gid)
	p2 := d2.glyphPath(f, gid) // A different render (device) hits the same document store.
	if p1 == nil || p1 != p2 {
		t.Errorf("glyph path not shared through the store: %p vs %p", p1, p2)
	}
	if st.Used() == 0 {
		t.Error("store recorded no usage")
	}
	// A budget too small for anything must still yield paths (converted fresh each time).
	tiny := store.New(1)
	d3 := newDevice(t, 8, 8)
	d3.SetStore(tiny)
	if p := d3.glyphPath(f, gid); p == nil || p.CountVerbs() == 0 {
		t.Error("tiny store lost the glyph path")
	}
	if tiny.Used() > 1 {
		t.Errorf("tiny store exceeded budget: %d", tiny.Used())
	}
}

func TestEvenOddFill(t *testing.T) {
	d := newDevice(t, 20, 20)
	var p gfx.Path
	p.Rect(0, 0, 20, 20)
	p.Rect(5, 5, 10, 10)
	d.FillPath(&p, true, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 2, 2); got[3] == 0 {
		t.Error("outer ring missing")
	}
	if got := pixelAt(t, pix, stride, 10, 10); got[3] != 0 {
		t.Errorf("even-odd hole painted: %v", got)
	}
}

// TestTilingDenormalStepTerminates is the regression test for the only hang the veraPDF corpus soak found
// (verapdf-a018-tiling.pdf): a denormal tile step overflows the float32 lattice division to ±Inf, whose int conversion
// saturates to MaxInt64, and the pre-fix replay loop `for j := j0; j <= j1; j++` never terminated because j++ wraps
// past MaxInt64. The fill must complete (via the bounded image-shader fallback) — run under a watchdog so a regression
// fails fast instead of hanging the suite.
func TestTilingDenormalStepTerminates(t *testing.T) {
	d := newDevice(t, 50, 50)
	var p gfx.Path
	p.Rect(0, 0, 50, 50)
	paint := device.Paint{
		Alpha: 1,
		Tiling: &device.Tiling{
			Replay: func(dev device.Device, ctm gfx.Matrix) {
				var cell gfx.Path
				cell.Rect(0, 0, 10, 10)
				dev.FillPath(&cell, false, ctm, redPaint())
			},
			BBox:  gfx.Rect{X0: 0, Y0: 0, X1: 100, Y1: 100},
			XStep: 15,
			YStep: 1.173e-38, // the A018 /YStep magnitude after the interpreter folds its sign
		},
		PatternCTM: gfx.Identity(),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.FillPath(&p, false, gfx.Identity(), paint)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("tiling fill with denormal step did not terminate")
	}
}

// TestGlyphBlitMatchesDirectFill pins the glyph-coverage-cache invariants: the three ways a solid-color glyph can reach
// pixels — the direct pixmap composite (no clip), the DrawImage route (under a non-rect clip), and the merged-outline
// DrawPath fill (translucent paint forces it) — must agree everywhere within ±2 per channel, since all three apply the
// same analytic-AA coverage and differ only in compositing rounding. A byte-level divergence beyond that means the
// cache no longer reproduces the fill.
func TestGlyphBlitMatchesDirectFill(t *testing.T) {
	f := helveticaFont(t)
	trm := gfx.Matrix{A: 24.37, B: 0, C: 0, D: -24.37}.Mul(gfx.Translate(2.31, 27.63)) // fractional phase on purpose
	render := func(prep func(d *Device), paint device.Paint) []byte {
		d := newDevice(t, 32, 32)
		if prep != nil {
			prep(d)
		}
		d.FillText(glyphRun(t, f, 'H', trm, gfx.Identity()), paint)
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	direct := render(nil, redPaint())
	var octagon gfx.Path // large non-rect clip fully covering the glyph: forces the DrawImage route
	octagon.MoveTo(10, -40)
	octagon.LineTo(70, 16)
	octagon.LineTo(10, 72)
	octagon.LineTo(-50, 16)
	octagon.Close()
	viaCanvas := render(func(d *Device) { d.ClipPath(&octagon, false, gfx.Identity()) }, redPaint())
	nearOpaque := redPaint()
	nearOpaque.Alpha = 254.4 / 255 // folds to alpha 254: forces the merged-outline DrawPath fill
	merged := render(nil, nearOpaque)
	for i := range direct {
		if delta(direct[i], viaCanvas[i]) > 2 {
			t.Fatalf("direct blit diverges from canvas image draw at byte %d: %d vs %d", i, direct[i], viaCanvas[i])
		}
		if delta(direct[i], merged[i]) > 3 {
			t.Fatalf("direct blit diverges from merged outline fill at byte %d: %d vs %d", i, direct[i], merged[i])
		}
	}
}

func delta(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// A degenerate text matrix whose device bounds are finite but enormous must fall back to the outline fill (plane nil),
// not overflow the floor/ceil and slip an all-zero coverage plane past the size gate that silently drops the glyph.
func TestRenderGlyphMaskRejectsHugeFiniteBounds(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	gid := f.GID('H')
	if gid == 0 {
		t.Fatal("'H' unmapped")
	}
	gp := d.glyphPath(f, gid)
	if gp == nil {
		t.Fatal("no glyph path")
	}
	g := &device.Glyph{Trm: gfx.Matrix{A: 1e30, D: -1e30}, GID: gid}
	mask, _ := d.renderGlyphMask(g, gp, 0, 0)
	if mask == nil {
		t.Fatal("nil mask")
	}
	if mask.plane != nil {
		t.Fatalf("huge finite bounds produced a %dx%d coverage plane instead of the outline fallback", mask.w, mask.h)
	}
}

// coveragePlane must degrade to nil on a nil pixmap — the same guard compositeMask and Pixels apply — so a scratch
// surface with no backing store makes renderGlyphMask fall back to the outline fill instead of dereferencing nil.
func TestCoveragePlaneNilPixmap(t *testing.T) {
	if plane := coveragePlane(nil, 4, 3); plane != nil {
		t.Fatalf("nil pixmap yielded a %d-byte plane; want nil so the caller degrades", len(plane))
	}
	pm := raster.NewPixmap(2, 2)
	pm.Pix[0] = 0xAB << 24
	pm.Pix[1] = 0xCD << 24
	pm.Pix[2] = 0x11 << 24
	pm.Pix[3] = 0x22 << 24
	plane := coveragePlane(pm, 2, 2)
	if want := []byte{0xAB, 0xCD, 0x11, 0x22}; len(plane) != len(want) {
		t.Fatalf("got %d-byte plane, want %d", len(plane), len(want))
	} else {
		for i, w := range want {
			if plane[i] != w {
				t.Fatalf("plane[%d] = %#x, want %#x", i, plane[i], w)
			}
		}
	}
}

// A malformed /TR LUT shorter than 256 entries must be ignored (treated as identity), not indexed by an arbitrary
// 0–255 mask value, which would panic.
func TestBeginMaskShortTransferLUTNoPanic(t *testing.T) {
	d := newDevice(t, 8, 8)
	d.BeginMask(gfx.Rect{}, false, color.NRGBA{}, []byte{0, 1, 2})
	var p gfx.Path
	p.Rect(0, 0, 8, 8)
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	d.EndMask()
	d.PopMask()
	if _, _, err := d.Pixels(); err != nil {
		t.Fatal(err)
	}
}

// Soft-mask nesting beyond maxMaskDepth must degrade to the no-surface path rather than allocating another offscreen
// surface, and the Begin/End/Pop pairing must still unwind cleanly. The boxes here are small enough that the byte
// budget cannot be what bites (TestBeginMaskByteBudgetDegrades covers that).
func TestBeginMaskDepthCapDegrades(t *testing.T) {
	d := newDevice(t, 8, 8)
	const depth = maxMaskDepth + 3
	small := gfx.Rect{X0: 3, Y0: 3, X1: 4, Y1: 4}
	for range depth {
		d.BeginMask(small, false, color.NRGBA{}, nil)
	}
	for i, ms := range d.maskStack {
		switch {
		case i < maxMaskDepth && ms.surf == nil:
			t.Errorf("mask %d within the cap has no surface", i)
		case i >= maxMaskDepth && ms.surf != nil:
			t.Errorf("mask %d beyond the cap allocated a surface", i)
		}
	}
	for range depth {
		d.EndMask()
		d.PopMask()
	}
	if len(d.maskStack) != 0 {
		t.Fatalf("mask stack not unwound: %d left", len(d.maskStack))
	}
	if d.maskBytes != 0 {
		t.Fatalf("mask byte charge not refunded: %d left", d.maskBytes)
	}
}

// The depth cap bounds the COUNT of open spans, not their bytes; page-sized masks must additionally stop at the byte
// budget, well before the depth cap, and still unwind cleanly. The first span always fits (the budget is a multiple of
// the page), so a mask covering the whole page is never degraded on its own account.
func TestBeginMaskByteBudgetDegrades(t *testing.T) {
	d := newDevice(t, 8, 8)
	const depth = maxMaskPages + 2
	page := gfx.Rect{X0: 0, Y0: 0, X1: 8, Y1: 8}
	for range depth {
		d.BeginMask(page, false, color.NRGBA{}, nil)
	}
	surfaces := 0
	for _, ms := range d.maskStack {
		if ms.surf != nil {
			surfaces++
		}
	}
	if surfaces == 0 || surfaces > maxMaskPages {
		t.Errorf("%d page-sized mask surfaces open at once, want 1..%d", surfaces, maxMaskPages)
	}
	if d.maskBytes > d.maskByteBudget() {
		t.Errorf("open mask surfaces hold %d bytes, over the %d budget", d.maskBytes, d.maskByteBudget())
	}
	for range depth {
		d.EndMask()
		d.PopMask()
	}
	if d.maskBytes != 0 {
		t.Fatalf("mask byte charge not refunded: %d left", d.maskBytes)
	}
}

// The mask surface, its readback, and the coverage plane are sized to the mask's bbox rather than the page, so a mask
// covering a corner of the page must produce exactly the pixels a page-sized mask surface produced: inside the box the
// rendered coverage, outside it the value an out-of-bbox sample has (zero for an alpha mask, the /BC backdrop's
// luminosity for a luminosity one, both through /TR). The zero rect is the "no usable bbox" signal that keeps the
// page-sized path, so it renders the reference.
func TestSoftMaskBBoxSizedPlaneMatchesFullPage(t *testing.T) {
	// A /TR LUT that maps 0 to a non-zero coverage: the area outside the bbox then survives the mask, which is the case
	// the bbox-sized plane has to reproduce with its own outside value rather than by scanning page pixels.
	lifted := make([]byte, 256)
	for i := range lifted {
		lifted[i] = uint8(64 + i*191/255)
	}
	for _, tc := range []struct {
		name       string
		transfer   []byte
		backdrop   color.NRGBA
		luminosity bool
	}{
		{name: "alpha"},
		{name: "alpha with lifted /TR", transfer: lifted},
		{name: "luminosity black /BC", luminosity: true, backdrop: color.NRGBA{A: 255}},
		{name: "luminosity gray /BC", luminosity: true, backdrop: color.NRGBA{R: 128, G: 128, B: 128, A: 255}},
		{
			name: "luminosity gray /BC with lifted /TR", luminosity: true,
			backdrop: color.NRGBA{R: 128, G: 128, B: 128, A: 255}, transfer: lifted,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The mask paints a disc inside the box; the masked content covers the whole surface.
			bbox := gfx.Rect{X0: 6, Y0: 8, X1: 22, Y1: 26}
			render := func(pass gfx.Rect) ([]byte, int, int, int) {
				d := newDevice(t, 40, 32)
				d.BeginMask(pass, tc.luminosity, tc.backdrop, tc.transfer)
				w, h := d.maskStack[0].w, d.maskStack[0].h
				var maskShape gfx.Path
				maskShape.Rect(8, 10, 12, 14)
				d.FillPath(&maskShape, false, gfx.Identity(), redPaint())
				d.EndMask()
				var content gfx.Path
				content.Rect(0, 0, 40, 32)
				d.FillPath(&content, false, gfx.Identity(), redPaint())
				d.PopMask()
				pix, stride, err := d.Pixels()
				if err != nil {
					t.Fatal(err)
				}
				return pix, stride, w, h
			}
			want, stride, fullW, fullH := render(gfx.Rect{})
			if fullW != 40 || fullH != 32 {
				t.Fatalf("the zero bbox must keep the page-sized plane, got %dx%d", fullW, fullH)
			}
			got, _, w, h := render(bbox)
			if w >= 40 || h >= 32 {
				t.Fatalf("bbox %v produced a %dx%d plane; want one smaller than the 40x32 page", bbox, w, h)
			}
			comparePixels(t, got, want, stride, "bbox-sized soft mask")
		})
	}
}

// A mask whose bbox lies entirely off the surface has no rasterizable content at all, so it reduces to its constant
// outside coverage — the masked op must be erased for an alpha mask, not left unmasked (the "degrade, never erase" path
// is for masks whose surface could not be created, not for masks that legitimately cover nothing).
func TestSoftMaskOffSurfaceBBoxMasksEverything(t *testing.T) {
	d := newDevice(t, 16, 16)
	d.BeginMask(gfx.Rect{X0: 100, Y0: 100, X1: 120, Y1: 120}, false, color.NRGBA{}, nil)
	if ms := d.maskStack[0]; ms.surf != nil || !ms.constant {
		t.Fatalf("off-surface bbox allocated a surface (surf != nil: %v, constant: %v)", ms.surf != nil, ms.constant)
	}
	var content gfx.Path
	content.Rect(0, 0, 16, 16)
	d.EndMask()
	d.FillPath(&content, false, gfx.Identity(), redPaint())
	d.PopMask()
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	for _, xy := range [][2]int{{0, 0}, {8, 8}, {15, 15}} {
		if got := pixelAt(t, pix, stride, xy[0], xy[1]); got[3] != 0 {
			t.Errorf("pixel (%d,%d) = %v; content under a fully off-surface mask must not mark", xy[0], xy[1], got)
		}
	}
}

// wrappedOnto returns a device drawing onto host's canvas after applying shift to it, as DrawPage's Wrap does for a
// caller who has already transformed their canvas. Pixels come back through host.
func wrappedOnto(t *testing.T, host *Device, dx, dy float32) *Device {
	t.Helper()
	host.c.Translate(dx, dy)
	d, err := Wrap(host.c)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// comparePixels fails on the first byte where two renders of the same content diverge.
func comparePixels(t *testing.T, got, want []byte, stride int, label string) {
	t.Helper()
	for i := range want {
		if got[i] != want[i] {
			p := i / 4
			t.Fatalf("%s: pixel (%d,%d) byte %d = %d, want %d", label, p%(stride/4), p/(stride/4), i%4, got[i], want[i])
		}
	}
}

// A device wrapping a caller's canvas draws under whatever matrix that canvas already carries, so a soft mask must
// rasterize its content and apply its coverage plane in the same device pixels the masked content lands in. Masking
// through a translated canvas must therefore match masking through an owned device with the translation folded into
// the content matrices — the mask surface is at identity and PopMask's DstIn rectangle is in surface pixels, so both
// have to compensate for the caller's matrix.
func TestWrappedCanvasSoftMaskRegistersWithContent(t *testing.T) {
	// bbox is the mask content's box in the space the DEVICE is handed (the interpreter's device space), which for a
	// wrapped canvas is still one caller matrix away from the pixels — sizing the mask surface has to map it through
	// that matrix or the plane lands in the wrong pixels.
	draw := func(d *Device, ctm gfx.Matrix, sized bool) {
		var maskArea gfx.Path
		maskArea.Rect(-16, -12, 20, 20) // device (0,0)-(20,20)
		var content gfx.Path
		content.Rect(-16, -12, 40, 40) // the whole surface
		bbox := gfx.Rect{}
		if sized {
			x0, y0 := ctm.ApplyXY(-16, -12)
			x1, y1 := ctm.ApplyXY(4, 8)
			bbox = gfx.Rect{X0: x0, Y0: y0, X1: x1, Y1: y1}
		}
		d.BeginMask(bbox, false, color.NRGBA{}, nil)
		d.FillPath(&maskArea, false, ctm, redPaint())
		d.EndMask()
		d.FillPath(&content, false, ctm, redPaint())
		d.PopMask()
	}
	ref := newDevice(t, 40, 40)
	draw(ref, gfx.Translate(16, 12), false)
	want, stride, err := ref.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	// The reference itself must show the mask gating the content, or the comparison below proves nothing.
	for _, tc := range []struct {
		x, y   int
		opaque bool
	}{{5, 5, true}, {19, 19, true}, {30, 5, false}, {5, 30, false}, {30, 30, false}} {
		if got := pixelAt(t, want, stride, tc.x, tc.y); (got[3] == 255) != tc.opaque {
			t.Fatalf("reference pixel (%d,%d) = %v, want opaque=%v", tc.x, tc.y, got, tc.opaque)
		}
	}
	compare := func(d *Device, label string) {
		t.Helper()
		got, _, pixErr := d.Pixels()
		if pixErr != nil {
			t.Fatal(pixErr)
		}
		comparePixels(t, got, want, stride, label)
	}
	for _, sized := range []bool{false, true} {
		host := newDevice(t, 40, 40)
		draw(wrappedOnto(t, host, 16, 12), gfx.Identity(), sized)
		compare(host, fmt.Sprintf("masked fill through a translated canvas (sized bbox: %v)", sized))
	}
	// The same content masked through a bbox-sized plane on an owned device must match the page-sized reference too.
	sizedRef := newDevice(t, 40, 40)
	draw(sizedRef, gfx.Translate(16, 12), true)
	compare(sizedRef, "masked fill with a bbox-sized plane")
}

// The sh operator paints across the whole clip by covering the device surface, a rectangle in surface pixels. On a
// wrapped canvas carrying the caller's matrix that rectangle has to be pulled back into the canvas's local space, or
// the shading under- and over-covers by exactly the caller's transform.
func TestWrappedCanvasFillShadingCoversSurface(t *testing.T) {
	sh := &shading.Shading{
		Kind:   shading.KindAxial,
		Coords: [6]float32{0, 0, 20, 0},
		Extend: [2]bool{true, true},
		Stops: []shading.Stop{
			{Offset: 0, Color: color.NRGBA{R: 255, A: 255}},
			{Offset: 1, Color: color.NRGBA{B: 255, A: 255}},
		},
	}
	paint := device.Paint{Alpha: 1}
	ref := newDevice(t, 40, 40)
	ref.FillShading(sh, gfx.Translate(16, 12), paint)
	want, stride, err := ref.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	for _, xy := range [][2]int{{0, 0}, {39, 39}, {20, 20}} { // the reference must cover the whole surface
		if got := pixelAt(t, want, stride, xy[0], xy[1]); got[3] != 255 {
			t.Fatalf("reference pixel (%d,%d) = %v, want opaque", xy[0], xy[1], got)
		}
	}
	host := newDevice(t, 40, 40)
	wrappedOnto(t, host, 16, 12).FillShading(sh, gfx.Identity(), paint)
	got, _, err := host.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	comparePixels(t, got, want, stride, "sh through a translated canvas")
}

// clampDim must apply its bounds in float space: Go's float→int conversion is implementation-defined for operands that
// do not fit and the platforms disagree (amd64 saturates to math.MinInt64, which an int-space clamp rounds back UP to
// 1 — the exact opposite of the clamp — while arm64 saturates high), so an over-range extent must be bounded before it
// is converted. The cases below are the ones whose conversion is undefined; each must land on a bound on every
// platform.
func TestClampDimClampsBeforeConverting(t *testing.T) {
	for _, tc := range []struct {
		v    float32
		maxV int
		want int
	}{
		{v: float32(math.NaN()), maxV: 512, want: 1},
		{v: float32(math.Inf(-1)), maxV: 512, want: 1},
		{v: -1e30, maxV: 512, want: 1},
		{v: 0, maxV: 512, want: 1},
		{v: 0.9, maxV: 512, want: 1},
		{v: 1, maxV: 512, want: 1},
		{v: 7.9, maxV: 512, want: 7},
		{v: 512, maxV: 512, want: 512},
		{v: 1e30, maxV: 512, want: 512},
		{v: float32(math.Inf(1)), maxV: 2048, want: 2048},
		{v: 1e30, maxV: 2048, want: 2048},
	} {
		if got := clampDim(tc.v, tc.maxV); got != tc.want {
			t.Errorf("clampDim(%v, %d) = %d, want %d", tc.v, tc.maxV, got, tc.want)
		}
	}
}

// The two clampDim call sites must survive an over-range extent with their grids clamped to the maximum, not collapsed
// to 1×1. Both dimensions are observable indirectly: the function grid is sampled once per cell, and the tiling cell's
// replay matrix carries the tile width per pattern-space unit.
func TestOverRangeExtentsKeepFullGridDimensions(t *testing.T) {
	d := newDevice(t, 32, 32)
	calls := 0
	sh := &shading.Shading{
		Kind:   shading.KindFunction,
		Domain: [4]float32{0, 1, 0, 1},
		Matrix: gfx.Matrix{A: 1e30, D: 1e30}, // a device extent no int can hold
		ColorAt: func(x, _ float32) color.NRGBA {
			calls++
			return color.NRGBA{R: uint8(x * 255), A: 255}
		},
	}
	if s := d.functionShader(sh, gfx.Identity()); s == nil {
		t.Fatal("function shading with an over-range device extent produced no shader")
	}
	if want := maxFunctionDim * maxFunctionDim; calls != want {
		t.Errorf("function shading sampled a %d-cell grid, want the clamped %d", calls, want)
	}
	var replayCTM gfx.Matrix
	tiling := &device.Tiling{
		Replay: func(_ device.Device, ctm gfx.Matrix) { replayCTM = ctm },
		BBox:   gfx.Rect{X0: 0, Y0: 0, X1: 1, Y1: 4},
		XStep:  1,
		YStep:  4,
	}
	// patCTM scales x by 1e30 (the overflowing dimension) and y by 2 (keeping the cell surface small).
	if s := d.tileShader(tiling, gfx.Identity(), gfx.Matrix{A: 1e30, D: 2}); s == nil {
		t.Fatal("tiling pattern with an over-range cell size produced no shader")
	}
	if replayCTM.A != maxTileDim { // XStep is 1, so the window's x scale is the tile width in pixels
		t.Errorf("tiling cell rasterized %v pixels wide, want the clamped %d", replayCTM.A, maxTileDim)
	}
}

// tilingFor builds a tiling paint whose cell paints one red square, counting the replays it takes.
func tilingFor(key any, replays *int) device.Paint {
	return device.Paint{
		Alpha: 1,
		Tiling: &device.Tiling{
			Replay: func(dev device.Device, ctm gfx.Matrix) {
				*replays++
				var cell gfx.Path
				cell.Rect(1, 1, 6, 6)
				dev.FillPath(&cell, false, ctm, redPaint())
			},
			Key:   key,
			BBox:  gfx.Rect{X0: 0, Y0: 0, X1: 8, Y1: 8},
			XStep: 8,
			YStep: 8,
		},
		PatternCTM: gfx.Identity(),
	}
}

// A tiling pattern's rasterized cell is the same image for the same content at the same device scale, so with a store
// wired it must be rasterized once and reused — across draws and across renders (devices) — instead of allocating and
// replaying a fresh cell surface per painting operation. Only the pattern identity the interpreter supplies makes that
// safe: a different key, a different scale, or no key at all must each replay again.
func TestTileShaderCachesCellInStore(t *testing.T) {
	st := store.New(0)
	replays := 0
	shaderFor := func(paint device.Paint, patCTM gfx.Matrix) {
		t.Helper()
		d := newDevice(t, 32, 32)
		d.SetStore(st)
		if s := d.tileShader(paint.Tiling, gfx.Identity(), patCTM); s == nil {
			t.Fatal("no tile shader")
		}
	}
	key := "pattern 7" // the device treats the interpreter's identity as an opaque comparable value
	shaderFor(tilingFor(key, &replays), gfx.Identity())
	if replays != 1 {
		t.Fatalf("first tile rasterization took %d replays, want 1", replays)
	}
	shaderFor(tilingFor(key, &replays), gfx.Identity()) // a later draw, a later render: the cached cell must serve both
	if replays != 1 {
		t.Errorf("the same pattern at the same scale replayed again (%d replays total)", replays)
	}
	shaderFor(tilingFor(key, &replays), gfx.Matrix{A: 2, D: 2}) // a different device scale is a different cell image
	if replays != 2 {
		t.Errorf("a rescaled cell was not re-rasterized (%d replays total)", replays)
	}
	shaderFor(tilingFor("pattern 8", &replays), gfx.Identity()) // a different pattern
	if replays != 3 {
		t.Errorf("a different pattern reused the cached cell (%d replays total)", replays)
	}
	before := replays
	shaderFor(tilingFor(nil, &replays), gfx.Identity())
	shaderFor(tilingFor(nil, &replays), gfx.Identity())
	if replays != before+2 {
		t.Errorf("an unkeyed pattern was cached (%d replays, want %d)", replays-before, 2)
	}
	// No store wired: every call rasterizes, exactly as before the cache existed.
	noStore := 0
	paint := tilingFor(key, &noStore)
	for range 2 {
		d := newDevice(t, 32, 32)
		if s := d.tileShader(paint.Tiling, gfx.Identity(), gfx.Identity()); s == nil {
			t.Fatal("no tile shader without a store")
		}
	}
	if noStore != 2 {
		t.Errorf("storeless device replayed %d times, want 2", noStore)
	}
}

// The cached cell must paint exactly what a freshly rasterized one paints; a stale or misindexed image would show up
// as a pixel difference between the first draw of a pattern and every later one.
func TestTileShaderCachedCellPaintsIdentically(t *testing.T) {
	render := func(st *store.Store) []byte {
		t.Helper()
		replays := 0
		d := newDevice(t, 32, 32)
		d.SetStore(st)
		var p gfx.Path
		p.Rect(4, 4, 24, 24)
		d.StrokePath(&p, &gfx.StrokeParams{Width: 6, MiterLimit: 10}, gfx.Identity(),
			tilingFor("pattern 11", &replays))
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	st := store.New(0)
	want := render(st) // populates the store
	got := render(st)  // served from it
	comparePixels(t, got, want, 32*4, "tiling pattern drawn from the cached cell")
	if fresh := render(store.New(0)); len(fresh) != len(want) {
		t.Fatal("unexpected pixel length")
	} else {
		comparePixels(t, fresh, want, 32*4, "tiling pattern drawn from a fresh cell")
	}
}

// gradientRamp must not index an empty stop slice, even when boundary extensions are requested.
func TestGradientRampEmptyStops(t *testing.T) {
	for _, e := range [][2]float32{{0, 0}, {0.5, 0.5}} {
		colors, pos := gradientRamp(nil, e[0], e[1])
		if colors != nil || pos != nil {
			t.Fatalf("e=%v: expected nil ramp, got %v / %v", e, colors, pos)
		}
	}
}

// Axial and radial shaders with no color stops must degrade to a nil shader (no shading painted) rather than panic on
// stops[0].
func TestShaderEmptyStopsNoPanic(t *testing.T) {
	d := newDevice(t, 8, 8)
	axial := &shading.Shading{Kind: shading.KindAxial, Coords: [6]float32{0, 0, 8, 8}, Extend: [2]bool{true, true}}
	if s := d.axialShader(axial, gfx.Identity()); s != nil {
		t.Error("axialShader with no stops returned a shader")
	}
	radial := &shading.Shading{Kind: shading.KindRadial, Coords: [6]float32{0, 0, 1, 8, 8, 4}, Extend: [2]bool{true, true}}
	if s := d.radialShader(radial, gfx.Identity()); s != nil {
		t.Error("radialShader with no stops returned a shader")
	}
}
