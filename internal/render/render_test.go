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
	"bytes"
	"fmt"
	"image/color"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/richardwilkes/canvas/geom"
	"github.com/richardwilkes/canvas/imagecore"
	"github.com/richardwilkes/canvas/path"
	"github.com/richardwilkes/canvas/raster"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
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

// TestNewRejectsAbovePixelCap covers the surface cap at several aspect ratios. The rejection happens before any
// allocation, so no memory is committed for these sizes.
func TestNewRejectsAbovePixelCap(t *testing.T) {
	for _, size := range [][2]int{
		{MaxSurfacePixels + 1, 1},
		{1, MaxSurfacePixels + 1},
		{MaxSurfacePixels/3 + 1, 3},
		{MaxSurfacePixels/7 + 1, 7},
		{1 << 15, 1 << 15},
		{math.MaxInt32, math.MaxInt32},
	} {
		if _, err := New(size[0], size[1]); err == nil {
			t.Errorf("size %v accepted; its %d pixels exceed the cap of %d", size,
				int64(size[0])*int64(size[1]), MaxSurfacePixels)
		}
	}
	// Sizes at exactly the cap must be allowed whatever the aspect ratio, so the root package's OverallMaxPixels
	// default rejects everything this would. Allocating them costs a gigabyte, so only the size check is exercised.
	for _, size := range [][2]int{
		{MaxSurfacePixels, 1},
		{1, MaxSurfacePixels},
		{MaxSurfacePixels / 3, 3},
		{MaxSurfacePixels / 7, 7},
		{1 << 14, 1 << 14},
	} {
		if !sizeAllowed(size[0], size[1]) {
			t.Errorf("size %v rejected; its %d pixels are within the cap of %d", size,
				int64(size[0])*int64(size[1]), MaxSurfacePixels)
		}
	}
	// The cap is stated as a byte budget, so pin the pixel count to it at 4 bytes per pixel.
	if MaxSurfacePixels*4 != 1<<30 {
		t.Errorf("expected a 1 GiB cap at 4 bytes per pixel, got %d bytes", MaxSurfacePixels*4)
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
func helveticaFont(t testing.TB) *font.Font {
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
func glyphRun(t testing.TB, f *font.Font, code uint32, trm, ctm gfx.Matrix) *device.TextRun {
	t.Helper()
	gid := f.GID(code, 1)
	if gid == 0 {
		t.Fatalf("code %d unmapped", code)
	}
	return &device.TextRun{
		Font:   f,
		Glyphs: []device.Glyph{{Trm: trm, GID: gid, Code: code, Advance: f.Width(code, 1)}},
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
	gid := f.GID('A', 1)
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
	gid := f.GID('A', 1)
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

// TestGlyphMaskScratchReuseIsClean pins that the per-miss scratch the mask renderer reuses across glyphs — the coverage
// surface, the transformed outline path and the fill paint — carries nothing from the glyph before it. A glyph rendered
// after a much larger, ink-heavy one must come out byte-identical to the same glyph on a device that has drawn nothing
// else: a clear that misses part of the region it is about to read back, or an outline path left un-rewound, would show
// up as stray coverage in the second render. Reset drops the mask cache but keeps the scratch, so the second glyph is
// still a miss and still lands on the storage the first one dirtied.
func TestGlyphMaskScratchReuseIsClean(t *testing.T) {
	f := helveticaFont(t)
	small := gfx.Matrix{A: 9.31, D: -9.31}.Mul(gfx.Translate(4.27, 26.53)) // fractional phase: always a miss
	pixels := func(d *Device) []byte {
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	alone := newDevice(t, 32, 32)
	alone.FillText(glyphRun(t, f, 'o', small, gfx.Identity()), redPaint())
	want := pixels(alone)
	after := newDevice(t, 32, 32)
	// 'W' at 30 px is both wider and taller than the 9 px 'o', so it grows the scratch surface well past what the 'o'
	// needs and leaves ink across it.
	big := gfx.Matrix{A: 30.17, D: -30.17}.Mul(gfx.Translate(0.41, 30.29))
	after.FillText(glyphRun(t, f, 'W', big, gfx.Identity()), redPaint())
	after.Reset()
	after.FillText(glyphRun(t, f, 'o', small, gfx.Identity()), redPaint())
	got := pixels(after)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("glyph rendered after a larger one differs at byte %d: %d, want %d", i, got[i], want[i])
		}
	}
}

// TestGlyphMaskMissAllocationsBounded pins the cost of a coverage-cache miss. A budget too small to retain the planes
// makes every glyph of every render a miss, so the miss has to stay cheap: what it may allocate is the plane itself,
// the mask that owns it and the cache's own bookkeeping. The scratch surface, the region clear, the transformed outline
// path and the fill paint are reused or done in place and must not show up here — each of those was one or two
// allocations per glyph before, and a regression that restores any of them lifts the count past maskMissAllocCeiling.
func TestGlyphMaskMissAllocationsBounded(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 256, 64)
	codes := []uint32{'H', 'e', 'l', 'o', 'W', 'r', 'd', 'A'}
	glyphs := make([]device.Glyph, len(codes))
	run := &device.TextRun{Font: f, Glyphs: glyphs, CTM: gfx.Identity()}
	n := 0
	draw := func() {
		for i, code := range codes {
			gid := f.GID(code, 1)
			if gid == 0 {
				t.Fatalf("code %d unmapped", code)
			}
			// A phase no earlier pass used keys a fresh cache entry, so every glyph here misses.
			trm := gfx.Matrix{A: 11, D: -11, E: float32(i)*14 + float32(n%31)/31, F: 40 + float32(n%17)/17}
			glyphs[i] = device.Glyph{Trm: trm, GID: gid, Code: code}
		}
		d.FillText(run, redPaint())
		d.glyphMasks = nil // Stand in for a budget that retains nothing, without steering the path taken.
		n++
	}
	draw() // Warm the glyph-outline cache, which is not what is being measured here.
	if got := testing.AllocsPerRun(50, draw) / float64(len(codes)); got > maskMissAllocCeiling {
		t.Fatalf("a glyph mask miss allocated %.1f times, want at most %d", got, maskMissAllocCeiling)
	}
}

// BenchmarkGlyphMaskMiss measures a coverage-cache miss end to end — outline fill into the scratch surface, coverage
// copy, composite — which is what every glyph costs under a budget too small to retain the planes.
func BenchmarkGlyphMaskMiss(b *testing.B) {
	f := helveticaFont(b)
	d, err := New(256, 64)
	if err != nil {
		b.Fatal(err)
	}
	codes := []uint32{'H', 'e', 'l', 'o', 'W', 'r', 'd', 'A'}
	gids := make([]uint32, len(codes))
	for i, code := range codes {
		if gids[i] = f.GID(code, 1); gids[i] == 0 {
			b.Fatalf("code %d unmapped", code)
		}
	}
	glyphs := make([]device.Glyph, len(codes))
	run := &device.TextRun{Font: f, Glyphs: glyphs, CTM: gfx.Identity()}
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		for i := range codes {
			trm := gfx.Matrix{A: 11, D: -11, E: float32(i)*14 + float32(n%31)/31, F: 40 + float32(n%17)/17}
			glyphs[i] = device.Glyph{Trm: trm, GID: gids[i], Code: codes[i]}
		}
		d.FillText(run, redPaint())
		d.glyphMasks = nil
		n++
	}
}

// A degenerate text matrix whose device bounds are finite but enormous must fall back to the outline fill (plane nil),
// not overflow the floor/ceil and slip an all-zero coverage plane past the size gate that silently drops the glyph.
func TestRenderGlyphMaskRejectsHugeFiniteBounds(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	gid := f.GID('H', 1)
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

// A glyph whose device origin is finite but enormous (Trm passes IsFinite, yet E/F reach ~3.4e38) must not reach the
// direct mask blit, where int(ox)/int(oy) overflow. The fast path folds it into the leftover outline instead, so a
// normal glyph blitted in the same run stays byte-for-byte identical to rendering that glyph alone.
func TestBlitGlyphHugeOriginDoesNotCorruptSibling(t *testing.T) {
	f := helveticaFont(t)
	trm := gfx.Matrix{A: 24, D: -24}.Mul(gfx.Translate(2, 28)) // on-screen, visible
	render := func(glyphs []device.Glyph) []byte {
		d := newDevice(t, 32, 32)
		d.FillText(&device.TextRun{Font: f, Glyphs: glyphs, CTM: gfx.Identity()}, redPaint())
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	normal := device.Glyph{Trm: trm, GID: f.GID('H', 1), Code: 'H', Advance: f.Width('H', 1)}
	// Same glyph, but translated far off any real surface: int(floor(3e30)) would overflow the blit's origin math.
	huge := device.Glyph{Trm: gfx.Matrix{A: 24, D: -24, E: 3e30, F: 3e30}, GID: f.GID('H', 1), Code: 'H', Advance: f.Width('H', 1)}
	alone := render([]device.Glyph{normal})
	withHuge := render([]device.Glyph{normal, huge})
	if len(alone) != len(withHuge) {
		t.Fatalf("pixel length mismatch: %d vs %d", len(alone), len(withHuge))
	}
	for i := range alone {
		if alone[i] != withHuge[i] {
			t.Fatalf("huge-origin glyph perturbed the surface at byte %d: %d vs %d", i, alone[i], withHuge[i])
		}
	}
	// Sanity: the normal glyph actually inked, so the equality above is not comparing two blank surfaces.
	if !inkIn(alone, 32*4, 0, 0, 31, 31) {
		t.Fatal("normal glyph produced no ink")
	}
}

// A run of nothing but huge-origin glyphs must blit cleanly to a blank surface without panicking on the overflowing
// float→int origin conversion.
func TestBlitGlyphHugeOriginBlankNoPanic(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 16, 16)
	huge := device.Glyph{Trm: gfx.Matrix{A: 8, D: -8, E: -3e30, F: 3e30}, GID: f.GID('H', 1), Code: 'H', Advance: f.Width('H', 1)}
	d.FillText(&device.TextRun{Font: f, Glyphs: []device.Glyph{huge}, CTM: gfx.Identity()}, redPaint())
	pix, _, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if inkIn(pix, 16*4, 0, 0, 15, 15) {
		t.Fatal("off-surface glyph left ink")
	}
}

// coveragePlane must degrade to nil on a nil pixmap — the same guard compositeMask and Pixels apply — rather than
// dereferencing it. renderGlyphMask makes that check itself before it zeroes the region it is about to read back, so
// this pins the helper's own contract.
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

// maskBounds must tell the two empty bboxes apart. The zero rect is the "not computed" signal — a caller that supplies
// no box may still draw mask content — so it keeps the page-sized path (degrade, never erase), as do non-finite and
// absurd corners, which carry no information either. A POSITIONED box with no area is a real answer: the mask content
// is clipped to it and cannot rasterize anything, so the span reduces to its constant outside coverage (ok false).
func TestMaskBoundsEmptyBBoxes(t *testing.T) {
	d := newDevice(t, 40, 32)
	base := d.c.TotalMatrix()
	for _, tc := range []struct {
		name     string
		bbox     gfx.Rect
		wantOK   bool
		wantFull bool
	}{
		{name: "zero rect", bbox: gfx.Rect{}, wantOK: true, wantFull: true},
		{name: "non-finite corner", bbox: gfx.Rect{X0: 4, Y0: 4, X1: float32(math.Inf(1)), Y1: 20}, wantOK: true, wantFull: true},
		{name: "absurd corners", bbox: gfx.Rect{X0: -1e30, Y0: -1e30, X1: 1e30, Y1: 1e30}, wantOK: true, wantFull: true},
		{name: "collapsed in x", bbox: gfx.Rect{X0: 10, Y0: 4, X1: 10, Y1: 28}},
		{name: "collapsed in y", bbox: gfx.Rect{X0: 4, Y0: 12, X1: 36, Y1: 12}},
		{name: "collapsed to a point", bbox: gfx.Rect{X0: 10, Y0: 12, X1: 10, Y1: 12}},
		{name: "wholly off surface", bbox: gfx.Rect{X0: 100, Y0: 100, X1: 120, Y1: 120}},
		{name: "reversed corners", bbox: gfx.Rect{X0: 22, Y0: 26, X1: 6, Y1: 8}, wantOK: true},
	} {
		x0, y0, w, h, ok := d.maskBounds(tc.bbox, &base)
		if ok != tc.wantOK {
			t.Errorf("%s: maskBounds ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		full := x0 == 0 && y0 == 0 && w == 40 && h == 32
		if full != tc.wantFull {
			t.Errorf("%s: maskBounds = (%d,%d,%d,%d); page-sized: %v, want %v", tc.name, x0, y0, w, h, full, tc.wantFull)
		}
	}
}

// The same distinction through BeginMask: a /BBox that collapses under the anchor CTM reaches the device as a
// positioned box with no area, and must commit no offscreen surface at all. The interpreter wraps EVERY painting
// operation in its own Begin/End/Pop cycle, so falling back to the page-sized path would allocate, prefill, read back,
// and scan a full page-sized offscreen per fill, stroke, glyph run, and image. The mask covers nothing, so an alpha
// mask's coverage is zero everywhere and the masked op is erased.
func TestSoftMaskCollapsedBBoxNeedsNoSurface(t *testing.T) {
	for _, bbox := range []gfx.Rect{
		{X0: 10, Y0: 4, X1: 10, Y1: 28},
		{X0: 4, Y0: 12, X1: 36, Y1: 12},
		{X0: 10, Y0: 12, X1: 10, Y1: 12},
	} {
		d := newDevice(t, 40, 32)
		d.BeginMask(bbox, false, color.NRGBA{}, nil)
		if ms := d.maskStack[0]; ms.surf != nil || !ms.constant {
			t.Errorf("bbox %v: surf != nil: %v, constant: %v; want no surface and a constant mask",
				bbox, ms.surf != nil, ms.constant)
		}
		if d.maskBytes != 0 {
			t.Errorf("bbox %v: %d offscreen bytes committed for a mask that cannot mark", bbox, d.maskBytes)
		}
		var whole gfx.Path
		whole.Rect(0, 0, 40, 32)
		d.FillPath(&whole, false, gfx.Identity(), redPaint())
		d.EndMask()
		d.FillPath(&whole, false, gfx.Identity(), redPaint())
		d.PopMask()
		pix, stride, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		for _, xy := range [][2]int{{0, 0}, {10, 12}, {20, 16}, {39, 31}} {
			if got := pixelAt(t, pix, stride, xy[0], xy[1]); got[3] != 0 {
				t.Errorf("bbox %v: pixel (%d,%d) = %v; a mask that cannot mark must erase its content",
					bbox, xy[0], xy[1], got)
			}
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

// The float-space extent clamps (shading.GridSize's own and clampDim's here) must survive an over-range extent with
// their grids clamped to the maximum, not collapsed to 1×1. Both dimensions are observable indirectly: the function
// grid is sampled once per cell, and the tiling cell's replay matrix carries the tile width per pattern-space unit.
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
	if s := d.functionShader(sh, gfx.Identity(), gfx.Identity()); s == nil {
		t.Fatal("function shading with an over-range device extent produced no shader")
	}
	if want := shading.MaxGridDim * shading.MaxGridDim; calls != want {
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

// gridfit's 90/270 branch (A==D==0) must snap the x axis from the C/E pair and the y axis from the B/F pair: with
// A==0 the device x is C*v+E and with D==0 the device y is B*u+F. This pins that pairing — the branch's comment once
// inverted it (claiming C/F for x and B/E for y), and a maintainer trusting the wrong comment could swap the code.
func TestGridfitRotatedSnapsXFromCEyFromBF(t *testing.T) {
	m := gfx.Matrix{A: 0, B: 10.3, C: -7.6, D: 0, E: 3.2, F: 5.9}
	got := gridfit(m)
	wantC, wantE := snapSpan(m.C, m.E)
	wantB, wantF := snapSpan(m.B, m.F)
	if got.C != wantC || got.E != wantE {
		t.Errorf("x axis: got C=%v E=%v, want C=%v E=%v (must snap from the C/E pair)", got.C, got.E, wantC, wantE)
	}
	if got.B != wantB || got.F != wantF {
		t.Errorf("y axis: got B=%v F=%v, want B=%v F=%v (must snap from the B/F pair)", got.B, got.F, wantB, wantF)
	}
	if got.A != 0 || got.D != 0 {
		t.Errorf("A/D must pass through unchanged as 0, got A=%v D=%v", got.A, got.D)
	}
	// The inverted pairing (C/F for x, B/E for y) would land elsewhere, so this test discriminates the two.
	if badC, badE := snapSpan(m.C, m.F); got.C == badC && got.E == badE {
		t.Fatal("x axis snapped from the C/F pair — the inverted pairing the comment fix corrects")
	}
}

// snapSpan's interval arithmetic must not overflow: the sum off+extent once happened in float32, so two large finite
// components (a `2e38 0 0 2e38 2e38 2e38 cm` image CTM) overflowed to ±Inf and the snapped extent came back Inf or NaN.
// Whatever the span, the result must stay finite, keep the span's direction, and still contain the original interval —
// grid fitting may only expand it.
func TestSnapSpanLargeComponentsStayFinite(t *testing.T) {
	for _, tc := range [][2]float32{
		{2e38, 2e38},
		{-2e38, 2e38},
		{2e38, -2e38},
		{-2e38, -2e38},
		{3e38, 3e38},
		{math.MaxFloat32, math.MaxFloat32},
		{1, 3e38},
		{3e38, 1},
	} {
		extent, off := snapSpan(tc[0], tc[1])
		if !isFinite32(extent) || !isFinite32(off) {
			t.Errorf("snapSpan(%v, %v) = (%v, %v); both must stay finite", tc[0], tc[1], extent, off)
			continue
		}
		if (extent < 0) != (tc[0] < 0) {
			t.Errorf("snapSpan(%v, %v) extent %v flipped the span's direction", tc[0], tc[1], extent)
		}
		lo, hi := float64(tc[1]), float64(tc[1])+float64(tc[0])
		if lo > hi {
			lo, hi = hi, lo
		}
		got0, got1 := float64(off), float64(off)+float64(extent)
		if got0 > got1 {
			got0, got1 = got1, got0
		}
		if got0 > lo || got1 < hi {
			t.Errorf("snapSpan(%v, %v) = (%v, %v): [%v, %v] no longer contains [%v, %v]",
				tc[0], tc[1], extent, off, got0, got1, lo, hi)
		}
	}
	// Ordinary spans must still snap outward to whole pixels, in either direction.
	for _, tc := range [4][4]float32{
		{10.3, 3.2, 11, 3},
		{-10.3, 13.5, -11, 14},
		{8, 4, 8, 4},
		{0.25, 7.5, 1, 7},
	} {
		if extent, off := snapSpan(tc[0], tc[1]); extent != tc[2] || off != tc[3] {
			t.Errorf("snapSpan(%v, %v) = (%v, %v), want (%v, %v)", tc[0], tc[1], extent, off, tc[2], tc[3])
		}
	}
}

// gridfit runs AFTER drawImage's caller has validated the CTM, so it must never be what makes one non-finite: the
// matrix flows into drawImage's matrix(ctm) and, for stencils, into FillImageMask's flip.Mul(fit) and on to
// preparePaint. Both snapping branches are covered (axis-aligned and the 90/270 one).
func TestGridfitLargeCTMStaysFinite(t *testing.T) {
	for _, m := range []gfx.Matrix{
		{A: 2e38, D: 2e38, E: 2e38, F: 2e38},
		{B: 2e38, C: 2e38, E: 2e38, F: 2e38},
		{A: -3e38, D: 3e38, E: 3e38, F: -3e38},
		{B: -3e38, C: 3e38, E: -3e38, F: 3e38},
		{A: math.MaxFloat32, D: math.MaxFloat32, E: -math.MaxFloat32, F: -math.MaxFloat32},
	} {
		if !m.IsFinite() {
			t.Fatalf("test setup: %v is not finite to begin with", m)
		}
		if got := gridfit(m); !got.IsFinite() {
			t.Errorf("gridfit(%v) = %v; a finite CTM must stay finite", m, got)
		}
	}
}

// And through the image entry points the fitted matrix reaches: an image drawn under such a CTM must leave a surface
// that still reads back, rather than carrying Inf/NaN geometry into canvas.
func TestImageWithLargeCTMDoesNotPoisonSurface(t *testing.T) {
	huge := gfx.Matrix{A: 2e38, D: 2e38, E: 2e38, F: 2e38}
	img := &imaging.Image{Pix: []byte{255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255, 255, 255, 0, 255}, Width: 2, Height: 2}
	stencil := &imaging.Image{Pix: []byte{255, 0, 0, 255}, Width: 2, Height: 2, Stencil: true, HasAlpha: true}
	for _, tc := range []struct {
		draw func(d *Device)
		name string
	}{
		{name: "image", draw: func(d *Device) { d.FillImage(img, huge, device.Paint{Alpha: 1}) }},
		{name: "stencil", draw: func(d *Device) { d.FillImageMask(stencil, huge, redPaint()) }},
	} {
		d := newDevice(t, 16, 16)
		tc.draw(d)
		if _, _, err := d.Pixels(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
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

// Whatever the extension factors and whatever offsets the stops carry, the position array gradientRamp hands to canvas
// must be a valid gradient ramp: one entry per color, every entry finite, inside [0, 1], and non-decreasing. A large
// one-sided extension compresses the whole original span into ~1e-6 of the ramp — below float32's resolution there — so
// the mapped offsets collapse onto each other, and nothing else validates them before they cross into canvas.
func TestGradientRampPositionsAreAValidRamp(t *testing.T) {
	sampled := make([]shading.Stop, 256)
	for i := range sampled {
		sampled[i] = shading.Stop{Offset: float32(i) / 255, Color: color.NRGBA{R: uint8(i), A: 255}}
	}
	// Offsets no parser produces today, so the guard is structural rather than incidental.
	hostile := []shading.Stop{
		{Offset: float32(math.NaN()), Color: color.NRGBA{A: 255}},
		{Offset: 0.75, Color: color.NRGBA{R: 255, A: 255}},
		{Offset: 0.25, Color: color.NRGBA{G: 255, A: 255}},
		{Offset: 1e30, Color: color.NRGBA{B: 255, A: 255}},
		{Offset: -1e30, Color: color.NRGBA{A: 255}},
	}
	for _, stops := range [][]shading.Stop{sampled, hostile} {
		for _, e := range [][2]float32{
			{0, 0},
			{1, 0},
			{0, 1},
			{0.5, 2},
			{maxExtendFactor, 0},
			{0, maxExtendFactor},
			{maxExtendFactor, maxExtendFactor},
		} {
			colors, pos := gradientRamp(stops, e[0], e[1])
			if len(colors) != len(pos) {
				t.Fatalf("stops=%d e=%v: %d colors but %d positions", len(stops), e, len(colors), len(pos))
			}
			prev := float32(0)
			for i, v := range pos {
				switch {
				case !isFinite32(v):
					t.Fatalf("stops=%d e=%v: position %d is %v", len(stops), e, i, v)
				case v < 0 || v > 1:
					t.Fatalf("stops=%d e=%v: position %d is %v, outside [0, 1]", len(stops), e, i, v)
				case v < prev:
					t.Fatalf("stops=%d e=%v: position %d is %v, below its predecessor %v", len(stops), e, i, v, prev)
				}
				prev = v
			}
		}
	}
	// An unextended ramp still reproduces the stop offsets exactly — the clamping may not perturb the ordinary case.
	_, pos := gradientRamp(sampled, 0, 0)
	for i, v := range pos {
		if v != sampled[i].Offset {
			t.Fatalf("unextended position %d is %v, want the stop's own offset %v", i, v, sampled[i].Offset)
		}
	}
}

// Axial and radial shaders with no color stops must degrade to a nil shader (no shading painted) rather than panic on
// stops[0].
func TestShaderEmptyStopsNoPanic(t *testing.T) {
	d := newDevice(t, 8, 8)
	axial := &shading.Shading{Kind: shading.KindAxial, Coords: [6]float32{0, 0, 8, 8}, Extend: [2]bool{true, true}}
	if s := d.axialShader(axial, gfx.Identity(), gfx.Identity()); s != nil {
		t.Error("axialShader with no stops returned a shader")
	}
	radial := &shading.Shading{Kind: shading.KindRadial, Coords: [6]float32{0, 0, 1, 8, 8, 4}, Extend: [2]bool{true, true}}
	if s := d.radialShader(radial, gfx.Identity(), gfx.Identity()); s != nil {
		t.Error("radialShader with no stops returned a shader")
	}
}

// textRun builds a multi-glyph run for text, advancing by step device pixels per glyph so each glyph lands on its own
// subpixel phase (a fresh coverage-cache key apiece).
func textRun(t *testing.T, f *font.Font, text string, size, x, y, step float32) *device.TextRun {
	t.Helper()
	run := &device.TextRun{Font: f, CTM: gfx.Identity()}
	for i, r := range text {
		code := uint32(r)
		gid := f.GID(code, 1)
		if gid == 0 {
			t.Fatalf("code %d unmapped", code)
		}
		trm := gfx.Matrix{A: size, D: -size}.Mul(gfx.Translate(x+float32(i)*step, y))
		run.Glyphs = append(run.Glyphs, device.Glyph{Trm: trm, GID: gid, Code: code, Advance: f.Width(code, 1)})
	}
	return run
}

// The per-device map (no store wired) has no eviction of its own. At its cap it must not simply stop accepting: that
// retires the cache for the rest of the render, leaving every later glyph appearance to rebuild a plane and throw it
// away with no prospect of a hit. Dropping the map keeps live planes capped while the page goes on caching. The cap is
// on bytes rather than entries — one plane may be maxGlyphMaskDim² = 64 KiB and an ordinary text glyph a few hundred
// bytes, so an entry count says nothing about the memory held.
func TestGlyphMaskCacheKeepsCachingWhenMapFull(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	d.glyphMasks = make(map[glyphMaskKey]*glyphMask)
	for i := range 256 {
		plane := make([]byte, maxGlyphMaskDim*maxGlyphMaskDim)
		d.glyphMasks[glyphMaskKey{gid: uint32(i) + 1}] = &glyphMask{plane: plane, w: maxGlyphMaskDim, h: maxGlyphMaskDim}
		d.glyphMaskBytes += glyphMaskSize(maxGlyphMaskDim, maxGlyphMaskDim) * 2
	}
	if d.glyphMaskBytes <= maxGlyphMaskBytes {
		t.Fatalf("the prefill (%d bytes) did not reach the %d-byte cap", d.glyphMaskBytes, maxGlyphMaskBytes)
	}
	gid := f.GID('H', 1)
	gp := d.glyphPath(f, gid)
	if gp == nil {
		t.Fatal("no glyph path")
	}
	trm := gfx.Matrix{A: 24, D: -24}.Mul(gfx.Translate(2, 28))
	g := &device.Glyph{Trm: trm, GID: gid, Code: 'H'}
	mask := d.glyphMask(f, g, gp, 0.5, 0.25)
	if mask == nil || mask.plane == nil {
		t.Fatal("no coverage plane rendered")
	}
	if d.glyphMaskBytes > maxGlyphMaskBytes {
		t.Errorf("the map holds %d bytes of planes, past its %d-byte cap", d.glyphMaskBytes, maxGlyphMaskBytes)
	}
	if again := d.glyphMask(f, g, gp, 0.5, 0.25); again != mask {
		t.Error("the plane rendered past the cap was not cached; the map stopped accepting entries")
	}
}

// A mask's canvas image is only needed by the DrawImage route (a glyph under a non-rectangular clip, or one straddling
// the clip interior); the direct pixmap composite nearly every glyph takes reads the coverage plane itself. Wrapping
// the plane eagerly would allocate for every cache miss, so it must be built on first use — and still be usable then.
func TestGlyphMaskImageBuiltLazily(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	gid := f.GID('H', 1)
	gp := d.glyphPath(f, gid)
	if gp == nil {
		t.Fatal("no glyph path")
	}
	g := &device.Glyph{Trm: gfx.Matrix{A: 24, D: -24}.Mul(gfx.Translate(2, 28)), GID: gid, Code: 'H'}
	mask, _ := d.renderGlyphMask(g, gp, 0, 0)
	if mask == nil || mask.plane == nil {
		t.Fatal("no coverage plane rendered")
	}
	if mask.img != nil {
		t.Error("the canvas image was built before anything asked for it")
	}
	img := mask.image()
	if img == nil {
		t.Fatal("image() built nothing")
	}
	if img.Width() != mask.w || img.Height() != mask.h {
		t.Errorf("image is %dx%d, want %dx%d", img.Width(), img.Height(), mask.w, mask.h)
	}
	if second := mask.image(); second != img {
		t.Error("image() rebuilt the wrapper instead of reusing it")
	}
}

// The store is a pure cache: a budget of any size — including one too small to ever retain a coverage plane — must
// leave rendered text byte-identical, because a blit and the merged-outline fill it replaces agree only within ±1 of
// compositing rounding. Nothing about cache occupancy may therefore steer a glyph onto a different path (the same
// contract TestCacheBudget pins for a whole document).
func TestTextIdenticalWhateverTheStoreBudget(t *testing.T) {
	f := helveticaFont(t)
	render := func(st *store.Store) []byte {
		t.Helper()
		d := newDevice(t, 64, 32)
		d.SetStore(st)
		d.FillText(textRun(t, f, "Hunt", 18, 2.31, 24.63, 12.37), redPaint())
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	unlimited := render(store.New(0))
	if !inkIn(unlimited, 64*4, 0, 0, 63, 31) {
		t.Fatal("the reference render drew nothing")
	}
	for _, budget := range []uint64{1, 64, 1 << 20} {
		comparePixels(t, render(store.New(budget)), unlimited, 64*4, fmt.Sprintf("text under a %d-byte budget", budget))
	}
	comparePixels(t, render(nil), unlimited, 64*4, "text with no store wired") // the per-render map instead
}

// PopMask must be inert for a span EndMask never closed: with no EndMask there is no masked-content layer, so ms.layer
// is still zero and restoring to it would unwind the canvas past the interpreter's own saves — on the mask surface's
// canvas rather than the page's, at that. The span must instead close the way EndMask would: the page canvas back, the
// text clip back, the mask surface (and its byte charge) released, and nothing applied to the page.
func TestPopMaskWithoutEndMask(t *testing.T) {
	for _, tc := range []struct {
		name string
		bbox gfx.Rect
	}{
		{name: "with mask surface", bbox: gfx.Rect{X0: 0, Y0: 0, X1: 16, Y1: 16}},
		{name: "no mask surface", bbox: gfx.Rect{X0: 1000, Y0: 1000, X1: 1010, Y1: 1010}}, // wholly off the surface
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDevice(t, 16, 16)
			var clip gfx.Path
			clip.Rect(0, 0, 8, 16) // interpreter state the unpaired PopMask must not unwind
			d.ClipPath(&clip, false, gfx.Identity())
			page := d.c
			base := d.c.SaveCount()
			d.BeginMask(tc.bbox, false, color.NRGBA{}, nil)
			var content gfx.Path
			content.Rect(0, 0, 16, 16)
			d.FillPath(&content, false, gfx.Identity(), redPaint()) // mask content, never turned into a plane
			d.PopMask()                                             // no EndMask for this span
			if d.c != page {
				t.Error("PopMask left the canvas pointed at the mask surface")
			}
			if got := d.c.SaveCount(); got != base {
				t.Errorf("save count %d after the unpaired PopMask, want %d", got, base)
			}
			if d.maskBytes != 0 {
				t.Errorf("mask byte charge not refunded: %d left", d.maskBytes)
			}
			if len(d.maskStack) != 0 {
				t.Errorf("mask stack not unwound: %d left", len(d.maskStack))
			}
			// The clip open before the span must still be there, and still poppable.
			var p gfx.Path
			p.Rect(0, 0, 16, 16)
			d.FillPath(&p, false, gfx.Identity(), redPaint())
			pix, stride, err := d.Pixels()
			if err != nil {
				t.Fatal(err)
			}
			if inkIn(pix, stride, 8, 0, 15, 15) {
				t.Error("ink outside the clip open before the mask span: the save stack was unwound")
			}
			d.PopClip()
			d.FillPath(&p, false, gfx.Identity(), device.Paint{Color: color.NRGBA{G: 255, A: 255}, Alpha: 1})
			if pix, stride, err = d.Pixels(); err != nil {
				t.Fatal(err)
			}
			if !inkIn(pix, stride, 8, 8, 15, 15) {
				t.Error("the clip was never restored after PopMask")
			}
		})
	}
}

// EndMask must be idempotent: a repeated call for the same span used to take the no-surface branch and restore to a
// guard count only that branch ever sets, unwinding the whole canvas save stack — including the clip the interpreter
// still expects to pop — and then open a second masked-content layer whose count overwrote the first.
func TestEndMaskIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name string
		bbox gfx.Rect
	}{
		{name: "with mask surface", bbox: gfx.Rect{X0: 0, Y0: 0, X1: 16, Y1: 16}},
		{name: "no mask surface", bbox: gfx.Rect{X0: 1000, Y0: 1000, X1: 1010, Y1: 1010}}, // wholly off the surface
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDevice(t, 16, 16)
			var clip gfx.Path
			clip.Rect(0, 0, 8, 16) // interpreter state the stray EndMask must not unwind
			d.ClipPath(&clip, false, gfx.Identity())
			base := d.c.SaveCount()
			d.BeginMask(tc.bbox, false, color.NRGBA{}, nil)
			var content gfx.Path
			content.Rect(0, 0, 16, 8)
			d.FillPath(&content, false, gfx.Identity(), redPaint()) // mask content: the top half is opaque
			d.EndMask()
			want := d.c.SaveCount()
			d.EndMask()
			if got := d.c.SaveCount(); got != want {
				t.Errorf("repeated EndMask moved the canvas save count from %d to %d", want, got)
			}
			var p gfx.Path
			p.Rect(0, 0, 16, 16)
			d.FillPath(&p, false, gfx.Identity(), redPaint())
			d.PopMask()
			if got := d.c.SaveCount(); got != base {
				t.Errorf("save count %d after PopMask, want %d", got, base)
			}
			d.PopClip()
			pix, stride, err := d.Pixels()
			if err != nil {
				t.Fatal(err)
			}
			if inkIn(pix, stride, 8, 0, 15, 15) {
				t.Error("ink outside the clip open before the mask span: the save stack was unwound")
			}
			// Painting after the popped clip must reach everywhere again, which it cannot if the stack was destroyed.
			d.FillPath(&p, false, gfx.Identity(), device.Paint{Color: color.NRGBA{G: 255, A: 255}, Alpha: 1})
			pix, stride, err = d.Pixels()
			if err != nil {
				t.Fatal(err)
			}
			if !inkIn(pix, stride, 8, 8, 15, 15) {
				t.Error("the clip was never restored after PopMask")
			}
		})
	}
}

// A mixed-/Extend axial gradient projects the surface corners onto its axis; the corner and the axis endpoint are both
// finite, but their float32 difference can overflow to ±Inf, and Inf*0 — the ordinary case for an axis-aligned
// gradient, where dx or dy is exactly 0 — is NaN. Go's min/max propagate that into the extension factors, hence into
// the extended endpoints and every stop offset, and on into canvas.
func TestAxialSpanFiniteWhenCornerProjectionOverflows(t *testing.T) {
	p0 := geom.Point{X: 3e38, Y: 0}
	corners := [4]gfx.Point{{X: -3e38, Y: 0}, {X: -2e38, Y: 0}, {X: -3e38, Y: 1}, {X: -2e38, Y: 1}}
	sMin, sMax := axialSpan(p0, 0, 1, 1, corners) // dx == 0: a vertical gradient
	if math.IsNaN(sMin) || math.IsNaN(sMax) {
		t.Fatalf("overflowing corner projection yielded NaN span [%v, %v]", sMin, sMax)
	}
	if sMin > 0 || sMax < 1 {
		t.Errorf("span [%v, %v] no longer covers the gradient's own [0, 1]", sMin, sMax)
	}
}

// The same overflow through the whole shader: the ramp handed to canvas must carry finite stop offsets, and the
// gradient's endpoints must stay finite.
func TestAxialShaderMixedExtendOverflowStaysFinite(t *testing.T) {
	d := newDevice(t, 1, 1)
	sh := &shading.Shading{
		Kind:   shading.KindAxial,
		Coords: [6]float32{3e38, 0, 3e38, 1}, // vertical axis at the far edge of float32
		Extend: [2]bool{true, false},
		Stops:  []shading.Stop{{Offset: 0, Color: color.NRGBA{R: 255, A: 255}}, {Offset: 1, Color: color.NRGBA{B: 255, A: 255}}},
	}
	// Inverting this maps the surface corners to about -3e38 in shading space, so every corner projection overflows.
	local := gfx.Matrix{A: 1e-38, D: 1, E: 3}
	corners, ok := d.coverageCorners(local)
	if !ok {
		t.Fatal("test setup: corners are not finite")
	}
	for _, c := range corners {
		if c.X > -1e38 {
			t.Fatalf("test setup: corner %v is not far enough out to overflow the projection", c)
		}
	}
	if s := d.axialShader(sh, local, local); s == nil {
		t.Fatal("axialShader returned no shader")
	}
	sMin, _ := axialSpan(geom.Point{X: sh.Coords[0], Y: sh.Coords[1]}, 0, 1, 1, corners)
	e0 := float32(min(-sMin+1, maxExtendFactor))
	_, pos := gradientRamp(sh.Stops, e0, 0)
	for i, v := range pos {
		if !isFinite32(v) {
			t.Fatalf("stop offset %d is %v", i, v)
		}
	}
}

// radialExtension's search runs on differences of finite-but-enormous coordinates, which overflow in float32 the same
// way; its factor must stay finite and inside the cap however hostile the geometry.
func TestRadialExtensionStaysFinite(t *testing.T) {
	corners := [4]gfx.Point{{X: -3e38, Y: -3e38}, {X: 3e38, Y: -3e38}, {X: -3e38, Y: 3e38}, {X: 3e38, Y: 3e38}}
	for _, atStart := range []bool{true, false} {
		e := radialExtension(gfx.Point{X: -3e38, Y: 0}, gfx.Point{X: 3e38, Y: 0}, 3e38, 0, corners, atStart)
		if !isFinite32(e) || e < 0 || e > maxExtendFactor {
			t.Errorf("atStart=%v: extension %v out of range", atStart, e)
		}
	}
}

// A radial extension can drive the extended radius or center past float32's range even when /Coords is finite; only
// finite circles may cross into canvas, so the shader degrades to nil (the shading is skipped) instead.
func TestRadialShaderRejectsNonFiniteExtension(t *testing.T) {
	d := newDevice(t, 8, 8)
	stops := []shading.Stop{{Offset: 0, Color: color.NRGBA{R: 255, A: 255}}, {Offset: 1, Color: color.NRGBA{B: 255, A: 255}}}
	// r1 + e1*(r1-r0) overflows: the extended outer radius is +Inf.
	huge := &shading.Shading{
		Kind:   shading.KindRadial,
		Coords: [6]float32{0, 0, 0, 1, 0, 3e38},
		Extend: [2]bool{false, true},
		Stops:  stops,
	}
	if s := d.radialShader(huge, gfx.Identity(), gfx.Identity()); s != nil {
		t.Error("non-finite extended geometry reached canvas")
	}
	// An ordinary mixed-extend radial must still build its shader.
	sane := &shading.Shading{
		Kind:   shading.KindRadial,
		Coords: [6]float32{4, 4, 1, 4, 4, 3},
		Extend: [2]bool{false, true},
		Stops:  stops,
	}
	if s := d.radialShader(sane, gfx.Identity(), gfx.Identity()); s == nil {
		t.Error("ordinary mixed-extend radial rejected")
	}
}

// drawMesh builds every triangle in one reused scratch path, so each draw must start from an empty path: a stale one
// would carry earlier triangles into later draws (painting them in the wrong color), and a scratch shared across draws
// must leave the second draw of a mesh identical to the first.
func TestDrawMeshScratchPathIsClearPerTriangle(t *testing.T) {
	red := color.NRGBA{R: 255, A: 255}
	green := color.NRGBA{G: 255, A: 255}
	sh := &shading.Shading{
		Kind: shading.KindFreeTriangle,
		Triangles: []shading.Triangle{
			{P: [3]gfx.Point{{X: 1, Y: 1}, {X: 9, Y: 1}, {X: 1, Y: 9}}, Color: red},
			{P: [3]gfx.Point{{X: 19, Y: 11}, {X: 11, Y: 19}, {X: 19, Y: 19}}, Color: green},
		},
	}
	paint := device.Paint{Alpha: 1, Shading: sh, PatternCTM: gfx.Identity()}
	render := func(draws int) []byte {
		t.Helper()
		d := newDevice(t, 20, 20)
		var p gfx.Path
		p.Rect(0, 0, 20, 20)
		for range draws {
			d.FillPath(&p, false, gfx.Identity(), paint)
		}
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	pix := render(1)
	stride := 20 * 4
	if got := pixelAt(t, pix, stride, 2, 2); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("first triangle painted %v, want opaque red", got)
	}
	if got := pixelAt(t, pix, stride, 17, 17); got != [4]uint8{0, 255, 0, 255} {
		t.Errorf("second triangle painted %v, want opaque green", got)
	}
	if got := pixelAt(t, pix, stride, 17, 2); got != [4]uint8{0, 0, 0, 0} {
		t.Errorf("area covered by neither triangle painted %v, want transparent", got)
	}
	comparePixels(t, render(2), pix, stride, "mesh drawn twice through the reused scratch path")
}

// TestBuildPathDropsNonFiniteGeometry pins the policy at the single seam every gfx.Path crosses into canvas through:
// ±Inf/NaN coordinates are not geometry a rasterizer can act on, so the path is dropped whole rather than partially
// built (which would fabricate segments the producer never described). Producers validate their own coordinates, but a
// value derived from validated ones — a rectangle's X1-X0 extent — can still overflow, and the failure then surfaces as
// "this form renders nothing" with no diagnostic.
func TestBuildPathDropsNonFiniteGeometry(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  gfx.Point
	}{
		{"inf", gfx.Point{X: float32(math.Inf(1)), Y: 0}},
		{"neg inf", gfx.Point{X: 0, Y: float32(math.Inf(-1))}},
		{"nan", gfx.Point{X: float32(math.NaN()), Y: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p gfx.Path
			p.MoveTo(0, 0)
			p.LineTo(10, 0)
			p.LineTo(tc.bad.X, tc.bad.Y)
			p.Close()
			if got := buildPath(&p, false); !got.IsEmpty() {
				t.Errorf("path with a %s point built %v verbs; it must be dropped whole", tc.name, got.Bounds())
			}
			// A wholly finite path still converts, so the guard cannot be a blanket rejection.
			var good gfx.Path
			good.Rect(0, 0, 10, 10)
			if buildPath(&good, false).IsEmpty() {
				t.Error("a finite path was dropped")
			}
		})
	}
}

// TestTilingCellClipSurvivesOverRangeBBox verifies the cell clip rasterizeTile builds spells the pattern /BBox corner
// by corner. The extent form (X1-X0) overflows to +Inf for a box spanning more than float32's range — -1e38..3e38 here
// — and the non-finite corners that yields clip the whole cell away, so a tiling pattern with an over-wide box paints
// nothing at all.
func TestTilingCellClipSurvivesOverRangeBBox(t *testing.T) {
	for _, tc := range []struct {
		name string
		bbox gfx.Rect
	}{
		{caseValidTile, gfx.Rect{X1: 20, Y1: 20}},
		{caseOverflowTile, gfx.Rect{X0: -1e38, Y0: -1e38, X1: 3e38, Y1: 3e38}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDevice(t, 20, 20)
			replays, emptyClips := 0, 0
			tiling := &device.Tiling{
				Replay: func(dev device.Device, _ gfx.Matrix) {
					replays++
					cell, ok := dev.(*Device)
					if !ok {
						t.Fatalf("cell device is %T, not the render device", dev)
					}
					if cell.c.IsClipEmpty() {
						emptyClips++
					}
				},
				BBox:  tc.bbox,
				XStep: 20,
				YStep: 20,
			}
			if img := d.rasterizeTile(tiling, gfx.Identity(), 20, 20); img == nil {
				t.Fatal("cell rasterization failed")
			}
			if replays == 0 {
				t.Fatal("the cell content never replayed")
			}
			if emptyClips != 0 {
				t.Errorf("%d of %d cell replays ran under an empty clip: the /BBox clip degenerated", emptyClips,
					replays)
			}
		})
	}
}

// Sub-test case names for the over-range /BBox tables.
const (
	caseValidTile    = "valid"
	caseOverflowTile = "overflow"
)

// TestRasterImageAlphaTypeFollowsPixels verifies rasterImage's alpha declaration follows the decoded pixels. The
// declaration is a promise to canvas about the surface's contents, and an image whose color space produced transparent
// pixels on its own (a /Separation /None space) must not be announced as opaque — a consumer taking that at its word
// paints a solid rectangle where the file asks for nothing.
func TestRasterImageAlphaTypeFollowsPixels(t *testing.T) {
	transparent := &imaging.Image{
		Pix: make([]byte, 4), Width: 1, Height: 1, HasAlpha: true, // A /Separation /None pixel: all four bytes zero.
	}
	if got := rasterImage(transparent).AlphaType(); got == imagecore.AlphaTypeOpaque {
		t.Error("a transparent image is declared opaque")
	}
	opaque := &imaging.Image{Pix: []byte{10, 20, 30, 255}, Width: 1, Height: 1}
	if got := rasterImage(opaque).AlphaType(); got != imagecore.AlphaTypeOpaque {
		t.Errorf("opaque image declared %v, want AlphaTypeOpaque", got)
	}
}

// TestTilingReplayReusesCellClipPath verifies the per-cell /BBox clip is built into one reusable scratch path rather
// than cloned per cell. A single fill replays up to maxReplayTiles cells and each needs only a transformed copy of the
// same five-point box, so the clone was this loop's whole allocation cost — the same reasoning meshScratch and maskPath
// are built on. The scratch must also carry the CURRENT cell's box, which is what proves the loop rebuilds it rather
// than reusing stale contents.
func TestTilingReplayReusesCellClipPath(t *testing.T) {
	d := newDevice(t, 64, 64)
	var p gfx.Path
	p.Rect(0, 0, 64, 64)
	const step = 16
	replays, distinct, misplaced := 0, 0, 0
	var first *path.Path
	paint := device.Paint{
		Alpha: 1,
		Tiling: &device.Tiling{
			Replay: func(dev device.Device, ctm gfx.Matrix) {
				replays++
				cell, ok := dev.(*Device)
				if !ok {
					t.Fatalf("replay device is %T, not the render device", dev)
				}
				if cell.tileScratch == nil {
					t.Fatal("no scratch path: the cell clip was built somewhere else")
				}
				switch {
				case first == nil:
					first = cell.tileScratch
				case first != cell.tileScratch:
					distinct++
				}
				// The scratch must hold this cell's box in device space, not a leftover from an earlier cell.
				wantLeft, wantTop := ctm.ApplyXY(0, 0)
				if b := cell.tileScratch.Bounds(); b.Left != wantLeft || b.Top != wantTop {
					misplaced++
				}
			},
			BBox:  gfx.Rect{X1: step, Y1: step},
			XStep: step,
			YStep: step,
		},
		PatternCTM: gfx.Identity(),
	}
	d.FillPath(&p, false, gfx.Identity(), paint)
	if replays < 4 {
		t.Fatalf("the fill replayed %d cells; the lattice path was not taken", replays)
	}
	if distinct != 0 {
		t.Errorf("%d of %d cell replays saw a fresh clip path; one scratch must serve them all", distinct, replays)
	}
	if misplaced != 0 {
		t.Errorf("%d of %d cell clips did not match their cell's box: the scratch was not rebuilt per cell",
			misplaced, replays)
	}
}

// TestTilingReplayCellCTMStaysFinite pins the precondition every replayed cell inherits. A cell's device offset is
// float32(i)*XStep*PatternCTM.A + …: both factors are validated, their product is not, and the matrix built from it
// becomes a child interpreter's INITIAL CTM — which drawpage.go rejects its own caller's matrix up front to keep
// finite, because cm and a form's /Matrix only check the products they compute and leave a poisoned gs.ctm poisoned for
// the rest of the cell. Cells whose offset leaves float32's range sit far outside the surface, so they are skipped; the
// ones that land on it must still replay.
func TestTilingReplayCellCTMStaysFinite(t *testing.T) {
	d := newDevice(t, 20, 20)
	var p gfx.Path
	p.Rect(0, 0, 20, 20)
	replays, nonFinite := 0, 0
	// Determinant -1e38, so the matrix still inverts; the step then multiplies past float32's range against A and D.
	patCTM := gfx.Matrix{A: 1e19, D: -1e19, F: 200}
	paint := device.Paint{
		Alpha: 1,
		Tiling: &device.Tiling{
			Replay: func(_ device.Device, ctm gfx.Matrix) {
				replays++
				if !ctm.IsFinite() {
					nonFinite++
					t.Logf("cell replayed under %+v", ctm)
				}
			},
			BBox:  gfx.Rect{X1: 1, Y1: 1},
			XStep: 1e20,
			YStep: 1e20,
		},
		PatternCTM: patCTM,
	}
	if !patCTM.IsFinite() {
		t.Fatal("the pattern matrix is itself non-finite; it must pass patternFor's guard to exercise the new one")
	}
	d.FillPath(&p, false, gfx.Identity(), paint)
	if replays == 0 {
		t.Fatal("no cell replayed; the lattice path was not taken, so nothing was exercised")
	}
	if nonFinite != 0 {
		t.Errorf("%d of %d cell replays received a non-finite CTM", nonFinite, replays)
	}
}

// TestTextOutlineDropsOverflowingGlyph covers the other half of the text-side transform overflow: textOutline checks
// that the Trm is finite and then transforms the glyph's outline through it, which is not the same thing — an
// em-normalized outline still overflows once the Trm nears float32's maximum. The merged outline is one path, so a
// single ±Inf point would make the whole run's geometry unusable: its fill, StrokeText's path, and the clip ClipText
// and EndTextClip accumulate.
func TestTextOutlineDropsOverflowingGlyph(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	gid := f.GID('H', 1)
	if gid == 0 {
		t.Fatal("'H' unmapped")
	}
	normal := device.Glyph{Trm: gfx.Matrix{A: 24, D: -24}.Mul(gfx.Translate(2, 28)), GID: gid, Code: 'H'}
	// Every entry is a legal float32 and the matrix passes IsFinite, yet A*x + E crosses float32's maximum for any
	// outline point past 0.13 em — which 'H' has in quantity.
	huge := device.Glyph{Trm: gfx.Matrix{A: 3e38, D: -3e38, E: 3e38, F: -3e38}, GID: gid, Code: 'H'}
	if !huge.Trm.IsFinite() {
		t.Fatal("the test's Trm is itself non-finite; it must pass the existing guard to exercise the new one")
	}
	outline := func(glyphs ...device.Glyph) *path.Path {
		return d.textOutline(&device.TextRun{Font: f, Glyphs: glyphs, CTM: gfx.Identity()}, nil)
	}
	alone := outline(normal)
	if alone.IsEmpty() {
		t.Fatal("the normal glyph produced no outline; the comparisons below would be vacuous")
	}
	if got, want := outline(normal, huge).Bounds(), alone.Bounds(); got != want {
		t.Errorf("the overflowing glyph reached the merged outline: bounds %v, want %v", got, want)
	}
	if only := outline(huge); !only.IsEmpty() {
		t.Errorf("a run of nothing but overflowing glyphs built an outline bounded by %v, want an empty path",
			only.Bounds())
	}
}

// A clip whose path is finite but leaves float32's range once transformed selects the whole surface, so it must be
// skipped outright: intersecting with the ±Inf-cornered path canvas would build empties the clip and everything drawn
// under it vanishes. PopClip must still balance, and an ordinary clip must still restrict.
func TestClipPathSkipsOverRangeTransform(t *testing.T) {
	d := newDevice(t, 20, 20)
	var clip gfx.Path
	clip.RectCorners(-1e38, -1e38, 1e38, 1e38) // Finite corners; ×10 below is ±Inf.
	d.ClipPath(&clip, false, gfx.Scale(10, 10))
	if d.c.IsClipEmpty() {
		t.Fatal("the over-range clip emptied the clip region; a region that large selects everything")
	}
	var p gfx.Path
	p.Rect(0, 0, 20, 20)
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	d.PopClip()
	// The level still popped cleanly, so a genuine clip pushed after it still restricts.
	var half gfx.Path
	half.Rect(0, 0, 8, 20)
	d.ClipPath(&half, false, gfx.Identity())
	d.FillPath(&p, false, gfx.Identity(), device.Paint{Color: color.NRGBA{G: 255, A: 255}, Alpha: 1})
	d.PopClip()
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 15, 15); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("under the over-range clip the fill painted %v, want opaque red", got)
	}
	if got := pixelAt(t, pix, stride, 4, 4); got != [4]uint8{0, 255, 0, 255} {
		t.Errorf("inside the following clip = %v, want opaque green", got)
	}
	if got := pixelAt(t, pix, stride, 15, 4); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("outside the following clip = %v: the clip stack did not unwind", got)
	}
}

// TestSpillCopiesBoundedInFloatSpace covers the tiling-cell spill count, which was computed as
// int(math.Ceil(float64(extent/step))) - 1 before any bound was applied. extent is a float32 difference of two
// individually validated /BBox corners, so it reaches +Inf for a box spanning more than float32's range, and int(+Inf)
// is implementation-defined: amd64 wraps to MinInt64 and arm64 saturates to MaxInt64. The two happened to agree after
// the -1 wrapped back around, but only by accident. Like every sibling conversion in this package, the bound belongs in
// float space, before the conversion.
func TestSpillCopiesBoundedInFloatSpace(t *testing.T) {
	inf := float32(math.Inf(1))
	for _, tc := range []struct {
		name         string
		extent, step float32
		want         int
	}{
		{"cell within the step", 5, 10, 0},
		{"cell equal to the step", 10, 10, 0},
		{"one cell of spill", 15, 10, 1},
		{"two cells of spill", 25, 10, 2},
		{"capped", 1e6, 1, maxTileCopies},
		{"overflowing extent", inf, 1, maxTileCopies},
		{"overflowing ratio", 3e38, 1e-38, maxTileCopies},
		{"NaN extent", float32(math.NaN()), 1, 0},
		{"NaN step", 10, float32(math.NaN()), 0},
		{"zero step", 10, 0, maxTileCopies},
		{"negative step", 10, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spillCopies(tc.extent, tc.step)
			if got != tc.want {
				t.Fatalf("spillCopies(%v, %v) = %d, want %d", tc.extent, tc.step, got, tc.want)
			}
			if got < 0 || got > maxTileCopies {
				t.Fatalf("spillCopies(%v, %v) = %d, outside [0, %d]", tc.extent, tc.step, got, maxTileCopies)
			}
		})
	}
}

// funcShadingFor returns a small function-based shading whose evaluations are counted, and a matrix that realizes it on
// a grid of the given square size.
func funcShadingFor(evals *int, extent float32) (*shading.Shading, gfx.Matrix) {
	sh := &shading.Shading{
		Kind:   shading.KindFunction,
		Domain: [4]float32{0, 1, 0, 1},
		Matrix: gfx.Matrix{A: extent, D: extent},
		ColorAt: func(x, y float32) color.NRGBA {
			*evals++
			return color.NRGBA{R: uint8(x * 255), G: uint8(y * 255), A: 255}
		},
	}
	return sh, gfx.Identity()
}

// A function-based shading's realized grid is the same image for the same shading at the same grid size — where it
// lands on the surface lives in the shader's matrix, not in the image — so it must be evaluated once and reused instead
// of re-running the whole grid (up to shading.MaxGridArea function evaluations and a 1 MB allocation) per painting
// operation. That was measured at 10 ms per sh operator with the cheapest possible /Function and 304 ms with a
// 200-instruction type 4 program, both linear in the operator count.
func TestFunctionShaderCachesRealizedGrid(t *testing.T) {
	t.Run("with a store", func(t *testing.T) {
		st := store.New(0)
		evals := 0
		sh, local := funcShadingFor(&evals, 16)
		for range 3 { // Three draws, on three devices: one realization must serve them all.
			d := newDevice(t, 32, 32)
			d.SetStore(st)
			if s := d.functionShader(sh, local, local); s == nil {
				t.Fatal("no function shader")
			}
		}
		w, h, _ := sh.GridSize(local)
		if evals != w*h {
			t.Errorf("the grid was evaluated %d times, want the %d cells of one realization", evals, w*h)
		}
		// A different grid size is a different image and must be realized on its own.
		d := newDevice(t, 32, 32)
		d.SetStore(st)
		if s := d.functionShader(sh, gfx.Matrix{A: 2, D: 2}, gfx.Matrix{A: 2, D: 2}); s == nil {
			t.Fatal("no function shader at the second scale")
		}
		if evals == w*h {
			t.Error("a rescaled grid reused the cached realization")
		}
		// A different shading must not read another's cached grid.
		otherEvals := 0
		other, otherLocal := funcShadingFor(&otherEvals, 16)
		if s := d.functionShader(other, otherLocal, otherLocal); s == nil {
			t.Fatal("no function shader for the second shading")
		}
		if otherEvals != w*h {
			t.Errorf("a different shading evaluated %d cells, want %d: it reused the cached grid", otherEvals, w*h)
		}
	})
	t.Run("without a store", func(t *testing.T) {
		// The per-render map is the storeless fallback: repeated draws on one device still realize once.
		evals := 0
		sh, local := funcShadingFor(&evals, 16)
		d := newDevice(t, 32, 32)
		for range 3 {
			if s := d.functionShader(sh, local, local); s == nil {
				t.Fatal("no function shader")
			}
		}
		w, h, _ := sh.GridSize(local)
		if evals != w*h {
			t.Errorf("the grid was evaluated %d times, want the %d cells of one realization", evals, w*h)
		}
		// Reset drops the per-render map, since without a store nothing keeps its keyed shading pointers alive.
		d.Reset()
		if s := d.functionShader(sh, local, local); s == nil {
			t.Fatal("no function shader after Reset")
		}
		if evals != 2*w*h {
			t.Errorf("the grid was evaluated %d times across the reset, want %d", evals, 2*w*h)
		}
	})
}

// Every /Domain entry is validated finite on its own, but the extent of a wide box like [-3e38 3e38 0 1] overflows the
// float32 subtraction to +Inf. Computed that way, every cell of the grid samples the function at x = +Inf (one wrong
// flat grid, paid for at up to shading.MaxGridArea evaluations) and the placement matrix built from the same extent
// carries the infinity into canvas, which silently drops the whole fill.
func TestFunctionShaderOverflowingDomainExtent(t *testing.T) {
	t.Run("the grid spans the domain", func(t *testing.T) {
		var xs []float32
		sh := &shading.Shading{
			Kind:   shading.KindFunction,
			Domain: [4]float32{-3e38, 3e38, 0, 1},
			Matrix: gfx.Identity(),
			ColorAt: func(x, _ float32) color.NRGBA {
				xs = append(xs, x)
				return color.NRGBA{A: 255}
			},
		}
		d := newDevice(t, 32, 32)
		if s := d.functionShader(sh, gfx.Identity(), gfx.Identity()); s == nil {
			t.Fatal("function shading with a wide domain produced no shader")
		}
		w, h, ok := sh.GridSize(gfx.Identity())
		if !ok || w*h < 2 {
			t.Fatalf("GridSize = %d x %d (ok = %v), want a multi-cell grid", w, h, ok)
		}
		if len(xs) != w*h {
			t.Fatalf("the grid was evaluated %d times, want the %d cells of a %d x %d grid", len(xs), w*h, w, h)
		}
		for _, x := range xs {
			if !isFinite32(x) {
				t.Fatalf("a grid cell sampled the function at x = %v", x)
			}
		}
		if xs[0] == xs[len(xs)-1] {
			t.Errorf("every grid cell sampled x = %v, want samples spanning the domain", xs[0])
		}
	})
	t.Run("a non-finite placement is refused before the grid is evaluated", func(t *testing.T) {
		// A domain that wide, squeezed to a sub-pixel device extent, is realized on a single cell — so the cell extent is
		// the whole overflowing span and the shader's local matrix goes non-finite even though GridSize's corner check
		// passes (it maps the domain corners, which stay finite under the tiny scale).
		evals := 0
		sh := &shading.Shading{
			Kind:   shading.KindFunction,
			Domain: [4]float32{-3e38, 3e38, 0, 1},
			Matrix: gfx.Matrix{A: 1e-39, D: 1},
			ColorAt: func(_, _ float32) color.NRGBA {
				evals++
				return color.NRGBA{A: 255}
			},
		}
		if _, _, ok := sh.GridSize(gfx.Identity()); !ok {
			t.Fatal("GridSize refused the shading, so the placement gate is not what is under test here")
		}
		d := newDevice(t, 32, 32)
		if s := d.functionShader(sh, gfx.Identity(), gfx.Identity()); s != nil {
			t.Error("a shading whose local matrix is non-finite produced a shader")
		}
		if evals != 0 {
			t.Errorf("the grid was evaluated %d times for a fill canvas cannot place", evals)
		}
	})
}

// The cached grid must paint exactly what a freshly realized one paints; a stale or misindexed image would show up as a
// pixel difference between the first draw of a shading and every later one.
func TestFunctionShaderCachedGridPaintsIdentically(t *testing.T) {
	render := func(draws int) []byte {
		t.Helper()
		evals := 0
		sh, _ := funcShadingFor(&evals, 24)
		d := newDevice(t, 24, 24)
		for range draws {
			d.FillShading(sh, gfx.Identity(), device.Paint{Alpha: 1})
		}
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	first := render(1)
	repeated := render(3) // The second and third draws come from the cache.
	if !bytes.Equal(first, repeated) {
		t.Error("a cached function-shading grid painted differently from a freshly realized one")
	}
}

// TestFillPathMeshOverRangeTransformStillPaints pins FillPath's mesh branch at the buildPath seam. The branch hands
// canvas a path already in device space, which is past what buildPath guarantees — a path whose own coordinates are
// finite still lands on ±Inf under a large-but-finite CTM — and fillMeshInto turns that path into a clip. Clipping to
// ±Inf corners empties the clip and the mesh vanishes, where a region float32 cannot express is one that covers
// everything: it must degrade to no clip, the way ClipPath and withShadingBBox already degrade.
func TestFillPathMeshOverRangeTransformStillPaints(t *testing.T) {
	sh := &shading.Shading{
		Kind: shading.KindFreeTriangle,
		Triangles: []shading.Triangle{
			{P: [3]gfx.Point{{X: 0, Y: 0}, {X: 20, Y: 0}, {X: 0, Y: 20}}, Color: color.NRGBA{R: 255, A: 255}},
		},
	}
	paint := device.Paint{Alpha: 1, Shading: sh, PatternCTM: gfx.Identity()}
	d := newDevice(t, 20, 20)
	var p gfx.Path
	p.RectCorners(-1e38, -1e38, 1e38, 1e38) // Finite corners; the ×10 CTM below puts them past float32's range.
	d.FillPath(&p, false, gfx.Scale(10, 10), paint)
	pix, _, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, 20*4, 2, 2); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("the mesh painted %v at (2,2), want opaque red: the over-range fill path emptied its own clip", got)
	}
}

// TestBlitLeftoverDropsOverflowingGlyph pins the coverage-blit fast path's leftover outline at the same seam the
// merged-outline path is pinned at (TestTextOutlineDropsOverflowingGlyph). Glyphs reaching the leftover branch are
// exactly the ones renderGlyphMask declined — including declining BECAUSE the transformed outline corners were
// non-finite — so without the bounds test a glyph the slow path skips entirely crosses into canvas with ±Inf
// coordinates on the fast path, which every ordinary opaque solid fill takes. Its neighbors in that same leftover
// outline must be unaffected.
func TestBlitLeftoverDropsOverflowingGlyph(t *testing.T) {
	f := helveticaFont(t)
	gid := f.GID('H', 1)
	if gid == 0 {
		t.Fatal("'H' unmapped")
	}
	// Too tall for a coverage plane (maxGlyphMaskDim), so this one takes the leftover outline rather than a blit.
	big := device.Glyph{Trm: gfx.Matrix{A: 600, D: -600}.Mul(gfx.Translate(20, 600)), GID: gid, Code: 'H'}
	// Finite Trm, but A*x + E crosses float32's maximum for any outline point past ~0.13 em.
	huge := device.Glyph{Trm: gfx.Matrix{A: 3e38, D: -3e38, E: 3e38, F: -3e38}, GID: gid, Code: 'H'}
	if !huge.Trm.IsFinite() {
		t.Fatal("the test's Trm is itself non-finite; it must pass the existing guard to exercise the new one")
	}
	render := func(glyphs ...device.Glyph) []byte {
		t.Helper()
		d := newDevice(t, 640, 640)
		d.FillText(&device.TextRun{Font: f, Glyphs: glyphs, CTM: gfx.Identity()}, redPaint())
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	alone := render(big)
	if !inkIn(alone, 640*4, 0, 0, 639, 639) {
		t.Fatal("the oversized glyph produced no ink; the comparison below would be vacuous")
	}
	comparePixels(t, render(big, huge), alone, 640*4, "an overflowing glyph shared the leftover outline")
	if blank := render(huge); inkIn(blank, 640*4, 0, 0, 639, 639) {
		t.Error("a run of nothing but overflowing glyphs left ink")
	}
}

// The per-render glyph path map has no eviction of its own. At its cap it must not simply stop accepting — that retires
// the cache for the rest of the render, leaving every later glyph to re-convert its outline and throw it away with no
// prospect of a hit. Its sibling glyphMask documents exactly that reasoning; the two must behave the same way.
func TestGlyphPathCacheKeepsCachingWhenMapFull(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	d.glyphPaths = make(map[glyphKey]*path.Path, maxCachedGlyphPaths)
	for i := range maxCachedGlyphPaths {
		d.glyphPaths[glyphKey{gid: uint32(i) + 1}] = nil
	}
	gid := f.GID('H', 1)
	if gid == 0 {
		t.Fatal("'H' unmapped")
	}
	first := d.glyphPath(f, gid)
	if first == nil {
		t.Fatal("no glyph path converted")
	}
	if len(d.glyphPaths) > maxCachedGlyphPaths {
		t.Errorf("the map holds %d entries, past its %d cap", len(d.glyphPaths), maxCachedGlyphPaths)
	}
	if again := d.glyphPath(f, gid); again != first {
		t.Error("the path converted past the cap was not cached; the map stopped accepting entries")
	}
}

// A coverage plane's cache key carries the full float32 Trm linear part and the exact subpixel phase of the glyph
// origin, so distinct keys accumulate with GLYPHS DRAWN rather than with the distinct resources the rest of the store
// holds. Under the unlimited budget New(buffer, 0) selects, the store never evicts anything, so retaining planes there
// would make "no limit" mean memory proportional to every glyph the document has ever rendered. They cache in the
// byte-capped per-device map instead — which survives Reset while a store is wired, so re-rendering the same page at
// the same size, the warm protocol the blit path exists for, still hits.
func TestGlyphMasksBypassUnlimitedStore(t *testing.T) {
	f := helveticaFont(t)
	st := store.New(0)
	d := newDevice(t, 96, 64)
	d.SetStore(st)
	if d.maskStore() != nil {
		t.Error("an unlimited store was chosen as the coverage-plane backing; nothing there would ever be evicted")
	}
	// Two batches of the same glyphs at different subpixel phases: no new glyph outlines, only new mask keys.
	d.FillText(textRun(t, f, "Hello", 12, 2, 40, 7.3), redPaint())
	if len(d.glyphMasks) == 0 {
		t.Fatal("no coverage planes were cached anywhere")
	}
	afterFirst := st.Used()
	if afterFirst == 0 {
		t.Fatal("the store recorded no usage; the glyph outlines should be in it")
	}
	d.FillText(textRun(t, f, "Hello", 12, 2.37, 40.11, 7.91), redPaint())
	if got := st.Used(); got != afterFirst {
		t.Errorf("the store grew from %d to %d bytes for glyph instances that named no new resource: coverage planes "+
			"are accumulating in a cache that never evicts", afterFirst, got)
	}
	// The planes outlive Reset while a store keeps their keyed *font.Font alive; without one nothing does.
	held := len(d.glyphMasks)
	d.Reset()
	if len(d.glyphMasks) != held {
		t.Errorf("Reset dropped %d cached planes; a re-render at the same size would rebuild every one",
			held-len(d.glyphMasks))
	}
	noStore := newDevice(t, 96, 64)
	noStore.FillText(textRun(t, f, "Hello", 12, 2, 40, 7.3), redPaint())
	noStore.Reset()
	if len(noStore.glyphMasks) != 0 {
		t.Error("planes keyed by a *font.Font nothing keeps alive survived Reset")
	}
	// A store with a real budget does back them: it evicts under that budget, which is the bound the cache needs.
	bounded := newDevice(t, 96, 64)
	bounded.SetStore(store.New(1 << 20))
	if bounded.maskStore() == nil {
		t.Error("a budgeted store was not chosen as the coverage-plane backing")
	}
	bounded.FillText(textRun(t, f, "Hello", 12, 2, 40, 7.3), redPaint())
	if len(bounded.glyphMasks) != 0 {
		t.Error("planes were cached per device alongside a budgeted store that already holds them")
	}
}

// TestFillImageBlends verifies FillImage composites under the paint's blend mode. The device call carries a full Paint
// for exactly this reason: an image's color source is its own samples, but the blend the graphics state selected still
// applies to it, the way it applies to a path fill or an image mask. A mid-gray image multiplied over a red backdrop
// must darken the red; before the fix the call took only an alpha and the paint defaulted to Src-over, leaving the
// image opaque over the backdrop.
func TestFillImageBlends(t *testing.T) {
	gray := &imaging.Image{Pix: []byte{128, 128, 128, 255}, Width: 1, Height: 1}
	square := gfx.Matrix{A: 20, D: 20}
	draw := func(blend device.Blend) [4]uint8 {
		t.Helper()
		d := newDevice(t, 20, 20)
		var p gfx.Path
		p.Rect(0, 0, 20, 20)
		d.FillPath(&p, false, gfx.Identity(), redPaint())
		d.FillImage(gray, square, device.Paint{Alpha: 1, Blend: blend})
		pix, stride, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pixelAt(t, pix, stride, 10, 10)
	}
	if got := draw(device.BlendNormal); got != [4]uint8{128, 128, 128, 255} {
		t.Errorf("Normal = %v, want the image replacing the backdrop", got)
	}
	// Multiply of gray 128 over red (255, 0, 0): the red channel halves, the others stay at zero.
	got := draw(device.BlendMultiply)
	if got[0] < 126 || got[0] > 130 || got[1] != 0 || got[2] != 0 || got[3] != 255 {
		t.Errorf("Multiply = %v, want ~{128, 0, 0, 255}", got)
	}
}

// A mixed-/Extend gradient's extension has to reach the surface's own pixels, so coverageCorners must invert the
// shading→DEVICE map. Inverting the shader's local matrix instead carries the device corners through the drawing CTM
// first, sizing the extension by ctm(deviceCorners): wherever the drawing CTM is smaller than the pattern CTM — a page
// rendered below scale 1, or any cm that shrinks the drawing CTM — the extension comes out too small and part of the
// surface is left unpainted. At scale 1 the usual page CTM's y-flip is an involution and the two agree exactly, which
// is why the goldens never caught it; the scale-1 rows below pin that agreement.
func TestMixedExtendCoversSurfaceUnderShrinkingCTM(t *testing.T) {
	blue := color.NRGBA{B: 255, A: 255}
	stops := []shading.Stop{{Offset: 0, Color: color.NRGBA{R: 255, A: 255}}, {Offset: 1, Color: blue}}
	// The device pixels the extended end must reach at each scale, well past where the unextended span ends.
	probes := [][2]int{{60, 50}, {95, 50}, {99, 99}}
	for _, tc := range []struct {
		sh   *shading.Shading
		name string
	}{
		{
			name: "axial",
			// The span runs from device x=0 to x=10; /Extend [false true] must carry the end color to x=99.
			sh: &shading.Shading{
				Kind:   shading.KindAxial,
				Coords: [6]float32{0, 0, 10, 0},
				Extend: [2]bool{false, true},
				Stops:  stops,
			},
		},
		{
			// Concentric circles at device (10, 10) growing by 1.5 units per parametric step: covering the far corner
			// (about 127 units out) needs an extension factor of 128, while the corners the buggy sizing produced at
			// scale 0.1 reach only about 90 units and settle for 64 — a radius of 97.5, short of the probes below.
			name: "radial",
			sh: &shading.Shading{
				Kind:   shading.KindRadial,
				Coords: [6]float32{10, 10, 0, 10, 10, 1.5},
				Extend: [2]bool{false, true},
				Stops:  stops,
			},
		},
	} {
		for _, scale := range []float32{1, 0.5, 0.1} {
			d := newDevice(t, 100, 100)
			// A page rendered at scale: the drawing CTM flips y and shrinks, while the pattern CTM stays device space.
			ctm := gfx.Matrix{A: scale, D: -scale, F: 100}
			var box gfx.Path
			box.Rect(-1e4, -1e4, 2e4, 2e4) // covers the whole surface at every scale under test
			d.FillPath(&box, false, ctm, device.Paint{Shading: tc.sh, PatternCTM: gfx.Identity(), Alpha: 1})
			pix, stride, err := d.Pixels()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range probes {
				got := pixelAt(t, pix, stride, p[0], p[1])
				if got[3] != 255 || got[2] < 200 {
					t.Errorf("%s at scale %v: pixel (%d,%d) = %v, want the extended end color painted opaque",
						tc.name, scale, p[0], p[1], got)
				}
			}
		}
	}
}

// A function-based shading is realized "at device resolution", which means device PIXELS: the grid must be sized from
// the pattern→device map, not from the shader's local matrix, which carries the drawing CTM's inverse. Sizing from
// local scales the grid by that inverse in both directions — a magnifying cm renders the shading blocky (the 100-unit
// /Matrix below drops from 101x101 cells to 6x6 under `cm 20 0 0 20 0 0`), while a shrinking one inflates it toward
// shading.MaxGridArea even though internal/content charged the work budget from the pattern CTM (budget.go's
// shadingPaintCost). shading.GridSize exists so both packages size from the same numbers; content passes the pattern
// CTM, so this asserts against that.
func TestFunctionShaderGridSizedFromPatternCTM(t *testing.T) {
	for _, ctm := range []gfx.Matrix{
		gfx.Identity(),
		{A: 20, D: 20},     // a magnifying cm: sizing from local would drop the grid to 6x6
		{A: 0.05, D: 0.05}, // a shrinking one: sizing from local would inflate it to the area cap
		{A: 4, D: -4, F: 100},
	} {
		evals := 0
		sh := &shading.Shading{
			Kind:   shading.KindFunction,
			Domain: [4]float32{0, 1, 0, 1},
			Matrix: gfx.Matrix{A: 100, D: 100}, // a 100x100 device-unit span under the pattern CTM
			ColorAt: func(_, _ float32) color.NRGBA {
				evals++
				return color.NRGBA{A: 255}
			},
		}
		patCTM := gfx.Identity()
		d := newDevice(t, 128, 128)
		if _, ok := d.preparePaint(device.Paint{Shading: sh, PatternCTM: patCTM, Alpha: 1}, &ctm); !ok {
			t.Fatalf("ctm %v: preparePaint refused the shading", ctm)
		}
		// The grid internal/content priced this paint at, from the same matrix. (The half-pixel sample shift
		// preparePaint folds in is a translation, which leaves the domain's extents — and so the grid — untouched.)
		w, h, ok := sh.GridSize(patCTM)
		if !ok {
			t.Fatalf("ctm %v: GridSize refused the pattern CTM", ctm)
		}
		if evals != w*h {
			t.Errorf("ctm %v: the grid was evaluated %d times, want the %d cells (%dx%d) the pattern CTM implies",
				ctm, evals, w*h, w, h)
		}
	}
}

// allocatedDuring reports the bytes fn allocated in total, so a transient peak counts even when nothing survives it.
func allocatedDuring(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A transparency group's layer must be sized to the group's device-space bbox. canvas falls back to the whole current
// clip when the bounds hint is nil, and internal/content pushes the form's /BBox clip only AFTER BeginGroup, so a file
// nesting groups to maxFormDepth otherwise holds that many page-sized premultiplied layers at once even when every
// group is a small stamp — about 400 MB on top of a 300 dpi letter page, and up to 12 GiB at the documented
// OverallMaxPixels. The soft-mask path bounds exactly this cost with maxMaskPages and bbox-sized surfaces.
func TestNestedGroupLayersSizedToBBox(t *testing.T) {
	const (
		dim    = 1024 // 4 MB per page-sized layer
		nested = 12   // internal/content's maxFormDepth
		// Twelve page-sized layers are 48 MB; twelve stamp-sized ones are a few kilobytes. The ceiling leaves plenty of
		// room for canvas's own per-layer bookkeeping without admitting even one page-sized layer per group.
		ceiling = 8 << 20
	)
	d := newDevice(t, dim, dim)
	stamp := gfx.Rect{X0: 10, Y0: 10, X1: 30, Y1: 30}
	allocated := allocatedDuring(func() {
		for range nested {
			d.BeginGroup(stamp, true, false, device.BlendMultiply, 1)
		}
		for range nested {
			d.EndGroup()
		}
	})
	if allocated > ceiling {
		t.Errorf("%d groups nested over a %vx%v stamp on a %dx%d surface allocated %d bytes, want at most %d",
			nested, stamp.X1-stamp.X0, stamp.Y1-stamp.Y0, dim, dim, allocated, ceiling)
	}
}

// The layer bounds are a hard clip on the group's extent, which is the /BBox clip the interpreter pushes one step
// later — so nothing the group paints outside its box may reach the surface. An uncomputed bbox (the zero rect) still
// means "the group can mark anywhere", the reading BeginMask gives it.
func TestGroupLayerHonorsBBox(t *testing.T) {
	paint := func(bbox gfx.Rect) []byte {
		t.Helper()
		d := newDevice(t, 64, 64)
		d.BeginGroup(bbox, true, false, device.BlendNormal, 1)
		var full gfx.Path
		full.Rect(0, 0, 64, 64)
		d.FillPath(&full, false, gfx.Identity(), redPaint())
		d.EndGroup()
		pix, _, err := d.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		return pix
	}
	bounded := paint(gfx.Rect{X0: 0, Y0: 0, X1: 16, Y1: 16})
	if got := pixelAt(t, bounded, 64*4, 8, 8); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("inside the group's bbox = %v, want the fill", got)
	}
	if got := pixelAt(t, bounded, 64*4, 40, 40); got != [4]uint8{} {
		t.Errorf("outside the group's bbox = %v, want nothing painted", got)
	}
	if got := pixelAt(t, paint(gfx.Rect{}), 64*4, 40, 40); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("with no computed bbox = %v, want the fill: the group may mark anywhere", got)
	}
}

// Reset must put the surface's own canvas back before it unwinds and clears. A render that ended with a soft-mask span
// still open left d.c on that span's offscreen canvas (BeginMask swaps it, EndMask swaps it back), so without the
// restore the reused device unwinds and clears the MASK surface, keeps drawing into it, and Pixels hands back the
// previous page's untouched pixels. The interpreter's balanced Begin/End/Pop pairing keeps that unreachable today, the
// way EndMask's ended guard and PopMask's !ended guard defend the same invariant from the other side.
func TestResetRestoresSurfaceCanvas(t *testing.T) {
	d := newDevice(t, 16, 16)
	var box gfx.Path
	box.Rect(0, 0, 16, 16)
	d.FillPath(&box, false, gfx.Identity(), redPaint()) // the "previous page"
	d.BeginMask(gfx.Rect{X0: 0, Y0: 0, X1: 8, Y1: 8}, false, color.NRGBA{}, nil)
	if d.c == d.surf.Canvas() {
		t.Fatal("test setup: BeginMask did not swap the canvas, so the reset has nothing to put back")
	}
	d.Reset()
	if d.c != d.surf.Canvas() {
		t.Fatal("Reset left the device drawing into the mask surface")
	}
	d.FillPath(&box, false, gfx.Identity(), device.Paint{Color: color.NRGBA{G: 255, A: 255}, Alpha: 1})
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 8, 8); got != [4]uint8{0, 255, 0, 255} {
		t.Errorf("pixel after the reset = %v, want the green drawn into the surface", got)
	}
}
