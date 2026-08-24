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
	// The 24 px 'H' covers roughly x 4..17, y 11..28: ink must exist there and nothing above the cap height.
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
	// A clip-text run whose glyphs produce no outlines (substituted .notdef) clips everything; PopClip still restores.
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

// TestTilingDenormalStepTerminates pins the fix for verapdf-a018-tiling.pdf: a denormal tile step overflows the
// float32 lattice division to ±Inf, whose int conversion saturates to MaxInt64, and an unbounded replay loop then
// never terminates (j++ wraps). The fill must take the image-shader fallback; the watchdog fails fast instead of
// hanging the suite.
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

// TestGlyphBlitMatchesDirectFill pins the glyph-coverage-cache invariant: the three ways a solid-color glyph reaches
// pixels — the direct pixmap composite (no clip), the DrawImage route (non-rect clip) and the merged-outline DrawPath
// fill (translucent paint) — apply the same analytic-AA coverage and may differ only by compositing rounding: ±2 per
// channel for the image route, ±3 for the merged fill.
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

// TestStemDarkenedBlitMatchesDirectFill extends TestGlyphBlitMatchesDirectFill to a darkening device: the mask path
// dilates each glyph with the same pen the merged path applies per run, so the three routes must still agree. It also
// pins that darkening adds ink at body size.
func TestStemDarkenedBlitMatchesDirectFill(t *testing.T) {
	f := helveticaFont(t)
	trm := gfx.Matrix{A: 24.37, B: 0, C: 0, D: -24.37}.Mul(gfx.Translate(2.31, 27.63)) // fractional phase on purpose
	render := func(dark bool, prep func(d *Device), paint device.Paint) []byte {
		d := newDevice(t, 32, 32)
		d.SetStemDarkening(dark)
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
	direct := render(true, nil, redPaint())
	var octagon gfx.Path // large non-rect clip fully covering the glyph: forces the DrawImage route
	octagon.MoveTo(10, -40)
	octagon.LineTo(70, 16)
	octagon.LineTo(10, 72)
	octagon.LineTo(-50, 16)
	octagon.Close()
	viaCanvas := render(true, func(d *Device) { d.ClipPath(&octagon, false, gfx.Identity()) }, redPaint())
	nearOpaque := redPaint()
	nearOpaque.Alpha = 254.4 / 255 // folds to alpha 254: forces the merged-outline DrawPath fill
	merged := render(true, nil, nearOpaque)
	for i := range direct {
		if delta(direct[i], viaCanvas[i]) > 2 {
			t.Fatalf("darkened direct blit diverges from canvas image draw at byte %d: %d vs %d",
				i, direct[i], viaCanvas[i])
		}
		if delta(direct[i], merged[i]) > 3 {
			t.Fatalf("darkened direct blit diverges from merged outline fill at byte %d: %d vs %d",
				i, direct[i], merged[i])
		}
	}
	ink := func(pix []byte) (total uint64) {
		for i := 3; i < len(pix); i += 4 {
			total += uint64(pix[i])
		}
		return total
	}
	exact := render(false, nil, redPaint())
	if ink(direct) <= ink(exact) {
		t.Fatalf("stem darkening added no ink: %d vs %d", ink(direct), ink(exact))
	}
}

// TestGlyphMaskScratchReuseIsClean pins that the scratch renderGlyphMask reuses across misses — the coverage surface,
// the outline path and the fill paint — carries nothing over: a glyph rendered after a larger, ink-heavy one must be
// byte-identical to the same glyph rendered alone. Reset drops the mask cache but keeps the scratch, so the second
// glyph is still a miss on the storage the first one dirtied.
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
	// 'W' at 30 px grows the scratch surface well past what the 9 px 'o' needs and leaves ink across it.
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

// TestGlyphMaskMissAllocationsBounded pins the cost of a coverage-cache miss, which a budget too small to retain planes
// pays for every glyph: it may allocate the plane, the mask that owns it and the cache's bookkeeping. The scratch
// surface, the region clear, the outline path and the fill paint are reused or done in place; restoring an allocation
// for any of them lifts the count past maskMissAllocCeiling.
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

// A glyph whose device origin is finite but enormous must not reach the direct mask blit, where int(ox)/int(oy)
// overflow; the fast path folds it into the leftover outline, so a normal glyph in the same run renders exactly as it
// does alone.
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
	// The normal glyph must ink, or the equality above compares two blank surfaces.
	if !inkIn(alone, 32*4, 0, 0, 31, 31) {
		t.Fatal("normal glyph produced no ink")
	}
}

// A run of nothing but huge-origin glyphs must leave a blank surface without panicking on the float→int origin
// conversion.
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

// coveragePlane must return nil for a nil pixmap rather than dereference it; renderGlyphMask checks first, so this
// pins the helper's own contract.
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

// Page-sized masks must stop at the byte budget, well before the depth cap, and still unwind cleanly. The first span
// always fits, since the budget is a multiple of the page.
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

// A bbox-sized mask surface must produce exactly the pixels a page-sized one does: the rendered coverage inside the
// box and the out-of-bbox sample value outside it (zero for an alpha mask, the /BC backdrop's luminosity for a
// luminosity one, both through /TR). The zero rect keeps the page-sized path, so it renders the reference.
func TestSoftMaskBBoxSizedPlaneMatchesFullPage(t *testing.T) {
	// A /TR LUT mapping 0 to nonzero coverage: the area outside the bbox then survives the mask, and the bbox-sized plane
	// must reproduce that with its outside value.
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
			// The mask paints a rectangle inside the box; the masked content covers the whole surface.
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

// A mask whose bbox lies wholly off the surface reduces to its constant outside coverage, so an alpha mask erases the
// masked op; "degrade, never erase" applies only to masks whose surface could not be created.
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

// maskBounds must tell the two empty bboxes apart: the zero rect, non-finite and absurd corners carry no information
// and keep the page-sized path, while a positioned box with no area cannot rasterize anything (ok false).
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

// The same distinction through BeginMask: a positioned box with no area must commit no offscreen surface (the
// interpreter wraps every painting operation in its own Begin/End/Pop cycle, so a page-sized fallback would cost a
// page per operation), and an alpha mask that covers nothing erases the masked op.
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

// wrappedOnto returns a device wrapping host's canvas after translating it by (dx, dy), as DrawPage does for a caller
// who has already transformed their canvas. Pixels come back through host.
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

// A wrapped device draws under the caller's canvas matrix, so a soft mask must rasterize its content and apply its
// plane in the pixels the masked content lands in: masking through a translated canvas must match an owned device with
// the translation folded into the content matrices.
func TestWrappedCanvasSoftMaskRegistersWithContent(t *testing.T) {
	// bbox is in the space the device is handed, which for a wrapped canvas is still one caller matrix away from the
	// pixels; sizing the mask surface must map it through that matrix.
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

// sh covers the device surface with a rectangle in surface pixels; on a wrapped canvas that rectangle must be pulled
// back through the caller's matrix, or the shading misses part of the surface.
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

// clampDim must bound an over-range extent before the float→int conversion, which is implementation-defined and
// differs between amd64 and arm64; each over-range case must land on a bound on every platform.
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

// An over-range extent must clamp the function grid (shading.GridSize) and the tile (clampDim) to their maximum, not
// collapse them to 1×1. The grid is observable by its sample count, the tile by its replay matrix's x scale.
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

// gridfit's 90/270 branch (A==D==0) must snap x from the C/E pair and y from the B/F pair: with A==0 device x is
// C*v+E, and with D==0 device y is B*u+F.
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

// snapSpan's interval arithmetic must not overflow: off+extent in float32 reaches ±Inf for large finite components (a
// `2e38 0 0 2e38 2e38 2e38 cm` image CTM). The result must stay finite, keep the span's direction and still contain
// the original interval.
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

// gridfit runs after the CTM is validated, so it must never make a finite matrix non-finite; the result flows into
// drawImage and, for stencils, into FillImageMask's flip.Mul(fit). Both snapping branches are covered.
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

// An image drawn under a large finite CTM must leave a surface that still reads back rather than carry Inf/NaN
// geometry into canvas.
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

// With a store wired a tiling cell must rasterize once and serve every draw and render at the same scale; a different
// key, a different scale or no key at all must each replay again.
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
	// No store wired: every call rasterizes.
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

// Whatever the extension factors and stop offsets, the positions gradientRamp hands canvas must be a valid ramp: one
// per color, finite, inside [0, 1] and non-decreasing. Nothing else validates them before they cross into canvas.
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

// At its byte cap the per-device map must drop its contents and go on caching rather than refuse new entries, which
// would retire the cache for the rest of the render.
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

// Only the DrawImage route needs a mask's canvas image, so it must be built on first use rather than per miss, and be
// usable then.
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

// The store is a pure cache: a budget of any size, even one too small to retain a plane, must leave rendered text
// byte-identical, so cache occupancy may never steer a glyph onto a different path (the contract TestCacheBudget pins
// for a whole document).
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

// PopMask on a span EndMask never closed must not restore to the zero ms.layer, which would unwind past the
// interpreter's own saves on the mask surface's canvas; it must close the span the way EndMask would and apply nothing.
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

// EndMask must be idempotent: a repeated call must not take the no-surface branch, which would restore to a guard
// count only that branch sets (unwinding the whole save stack) and open a second masked-content layer.
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

// axialSpan's corner projections must survive a float32 overflow of corner minus endpoint: Inf*0 (dx or dy is 0 for
// an axis-aligned gradient) is NaN, which would propagate into the extension factors.
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

// drawMesh builds every triangle in one reused scratch path, so a stale path would carry earlier triangles into later
// ones in the wrong color, and a second draw of the mesh must match the first.
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

// TestBuildPathDropsNonFiniteGeometry pins buildPath's policy: a path with any ±Inf/NaN coordinate is dropped whole
// rather than partially built, which would fabricate segments the producer never described.
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

// TestTilingCellClipSurvivesOverRangeBBox pins that rasterizeTile builds the /BBox clip corner by corner: the extent
// form (X1-X0) overflows to +Inf for a box spanning more than float32's range, and the clip would then empty the whole
// cell.
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

// Sub-test case names for the over-range /BBox table.
const (
	caseValidTile    = "valid"
	caseOverflowTile = "overflow"
)

// TestRasterImageAlphaTypeFollowsPixels pins that an image whose color space produced transparent pixels (a
// /Separation /None space) is not declared opaque, which would paint a solid rectangle where the file asks for nothing.
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

// TestTilingReplayReusesCellClipPath pins that every cell's /BBox clip is built in one scratch path (a fill replays up
// to maxReplayTiles cells) and that the scratch holds the current cell's box, so it is rebuilt rather than stale.
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

// TestTilingReplayCellCTMStaysFinite pins that a replayed cell's CTM is finite: the offset i*XStep*PatternCTM.A can
// overflow though both factors are finite, and the interpreter assumes its initial CTM is finite. Cells that far out
// are skipped; the ones on the surface must still replay.
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

// TestTextOutlineDropsOverflowingGlyph pins that a glyph whose outline overflows under a finite Trm is dropped: one
// ±Inf point would poison the run's merged path, which is also its stroke path and its text-clip contribution.
func TestTextOutlineDropsOverflowingGlyph(t *testing.T) {
	f := helveticaFont(t)
	d := newDevice(t, 32, 32)
	gid := f.GID('H', 1)
	if gid == 0 {
		t.Fatal("'H' unmapped")
	}
	normal := device.Glyph{Trm: gfx.Matrix{A: 24, D: -24}.Mul(gfx.Translate(2, 28)), GID: gid, Code: 'H'}
	// Finite Trm, but A*x + E crosses float32's maximum for any outline point past ~0.13 em.
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

// A clip whose path leaves float32's range once transformed selects the whole surface, so it must be skipped:
// intersecting with the ±Inf-cornered path empties the clip. PopClip must still balance and a later clip still
// restrict.
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

// spillCopies must bound the count in float space: extent is a float32 difference of two validated /BBox corners and
// reaches +Inf for a box spanning more than float32's range, and int(+Inf) is implementation-defined.
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

// A function-based shading's grid depends only on the shading and the grid size, so it must be realized once and
// reused rather than re-evaluated (up to shading.MaxGridArea function calls and a 1 MB allocation) per painting
// operation.
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

// A /Domain like [-3e38 3e38 0 1] has finite entries but a float32 extent of +Inf, which would sample every grid cell
// at x = +Inf and carry the infinity into the placement matrix, where canvas drops the whole fill.
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
		// Squeezed to a sub-pixel device extent the domain realizes on one cell, so the cell extent is the whole overflowing
		// span and the local matrix goes non-finite although GridSize's corner check passes.
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

// FillPath's mesh branch clips to the device-space path, so a path that leaves float32's range under a finite CTM must
// degrade to no clip, the way ClipPath and withShadingBBox do, rather than empty the clip and lose the mesh.
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

// TestBlitLeftoverDropsOverflowingGlyph is TestTextOutlineDropsOverflowingGlyph for blitTextRun's leftover outline: a
// glyph renderGlyphMask declined for non-finite corners must not reach canvas through the fast path, and its neighbors
// in the leftover must be unaffected.
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

// At its cap the per-render glyph path map must drop its contents and go on caching rather than refuse new entries,
// as the glyph mask map does.
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

// Coverage planes must bypass an unlimited store, which never evicts, and cache in the byte-capped per-device map
// instead; that map survives Reset while a store is wired, so a re-render at the same size still hits (see maskStore).
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
	// A budgeted store does back them: its eviction is the bound the cache needs.
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

// TestFillImageBlends pins that FillImage composites under the paint's blend mode: an image's color source is its own
// samples, but the graphics state's blend still applies to it.
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

// A mixed-/Extend gradient's extension must reach the surface's own pixels, so coverageCorners must invert the
// shading→device map and not the shader's local matrix, which sizes the extension too small wherever the drawing CTM
// shrinks (see coverageCorners). At scale 1 the two agree; those rows pin that.
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
			// Concentric circles at device (10, 10) growing 1.5 units per parametric step: covering the far corner (about
			// 127 units out) needs an extension factor of 128; sizing from local at scale 0.1 settles for 64, a radius of
			// 97.5, short of the probes below.
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

// The function-shading grid must be sized from the pattern→device map, not the shader's local matrix (see
// functionShader); internal/content charges its budget from the same map through shading.GridSize, so this asserts
// against that.
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
		// The grid internal/content priced this paint at; preparePaint's half-pixel shift is a translation and leaves the
		// grid untouched.
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

// A transparency group's layer must be sized to the group's bbox: the interpreter pushes the /BBox clip only after
// BeginGroup, so groups nested to maxFormDepth would otherwise hold that many page-sized layers at once (about 400 MB
// at 300 dpi letter).
func TestNestedGroupLayersSizedToBBox(t *testing.T) {
	const (
		dim    = 1024 // 4 MB per page-sized layer
		nested = 12   // internal/content's maxFormDepth
		// Twelve page-sized layers are 48 MB, twelve stamp-sized ones a few kilobytes; the ceiling leaves room for canvas's
		// per-layer bookkeeping without admitting one page-sized layer.
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

// The layer bounds clip the group to its box, as the /BBox clip pushed one step later does; the zero rect still means
// the group can mark anywhere.
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

// Reset must put the surface's own canvas back before it unwinds and clears: after a render that left a soft-mask span
// open, d.c is still the mask surface's canvas, and the reused device would otherwise clear and draw into that.
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
