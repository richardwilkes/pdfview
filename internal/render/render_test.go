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
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
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

// helveticaFont loads a substituted standard-14 Helvetica through the real font pipeline (rendering via the
// bundled Liberation Sans), giving the text tests genuine outlines without fixture files.
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
	// The 24 px 'H' covers roughly x 4..17, y 11..28 (cap height ≈ 0.72 em): ink must exist there and the
	// area above the cap height must stay empty.
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
	// 'O' at 60 px/em: a hairline stroke inks only the two contour outlines, so its total coverage must be
	// well under the fill's ring area (a StrokeText that accidentally filled would match the fill's total).
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
	// Ink only inside the glyph: the stems are covered, the region above the cap height is not, and the
	// counter between the stems stays empty.
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
	// A clip-text run whose glyphs produce no outlines (substituted .notdef) accumulates an empty region:
	// the finalized clip admits nothing, and PopClip restores.
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

// TestTilingDenormalStepTerminates is the regression test for the only hang the M8 veraPDF soak found
// (verapdf-a018-tiling.pdf, decision log 2026-07-11): a denormal tile step overflows the float32 lattice
// division to ±Inf, whose int conversion saturates to MaxInt64, and the pre-fix replay loop `for j := j0;
// j <= j1; j++` never terminated because j++ wraps past MaxInt64. The fill must complete (via the bounded
// image-shader fallback) — run under a watchdog so a regression fails fast instead of hanging the suite.
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

// TestGlyphBlitMatchesDirectFill pins the M8 glyph-coverage-cache invariants: the three ways a solid-color
// glyph can reach pixels — the direct pixmap composite (no clip), the DrawImage route (under a non-rect
// clip), and the merged-outline DrawPath fill (translucent paint forces it) — must agree everywhere within
// ±2 per channel, since all three apply the same analytic-AA coverage and differ only in compositing
// rounding. A byte-level divergence beyond that means the cache no longer reproduces the fill.
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
