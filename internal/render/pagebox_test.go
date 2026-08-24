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
	"math"
	"testing"

	"github.com/richardwilkes/canvas/geom"

	"github.com/richardwilkes/pdfview/internal/gfx"
)

// TestSnapPageBox pins the outward whole-pixel snapping the page-box clip depends on: the snapped rectangle must be
// exactly the pixels a page-sized surface covers (the far edge ceiled with the same 0.001 epsilon renderExtent
// applies), and the snapping must decline any map that takes the box off the pixel axes.
func TestSnapPageBox(t *testing.T) {
	rotate90 := gfx.Matrix{B: 1, C: -1} // Quarter turn: still axis-preserving, but through the B/C entries.
	skew := gfx.Matrix{A: 1, B: 0.5, C: 0, D: 1}
	for _, tc := range []struct {
		name          string
		ctm           gfx.Matrix
		width, height float32
		want          geom.Rect
		ok            bool
	}{
		{
			name: "identity whole pixels", ctm: gfx.Identity(), width: 200, height: 200,
			want: geom.RectLTRB(0, 0, 200, 200), ok: true,
		},
		{
			// 595.2 x 841.8 pt (A4 as glaive.pdf writes it) at scale 1 covers 596 x 842 pixels, matching the surface
			// a 72 dpi RenderPage allocates.
			name: "fractional extent rounds out", ctm: gfx.Identity(), width: 595.2, height: 841.8,
			want: geom.RectLTRB(0, 0, 596, 842), ok: true,
		},
		{
			// 200 pt at 100 dpi is 277.78 px, which the surface rounds up to 278.
			name: "scaled fractional extent", ctm: gfx.Scale(100.0/72, 100.0/72), width: 200, height: 200,
			want: geom.RectLTRB(0, 0, 278, 278), ok: true,
		},
		{
			// Slop below the epsilon must not buy an extra row: 200.0005 stays 200 columns wide.
			name: "slop below the epsilon", ctm: gfx.Identity(), width: 200.0005, height: 200.0005,
			want: geom.RectLTRB(0, 0, 200, 200), ok: true,
		},
		{
			name: "translated to a fractional origin", ctm: gfx.Translate(10.5, -3.25), width: 100, height: 100,
			want: geom.RectLTRB(10, -4, 111, 97), ok: true,
		},
		{
			// A y-flip maps the origin corner below the far corner; the snapped rect must still be sorted.
			name: "flipped", ctm: gfx.Matrix{A: 1, D: -1, F: 100}, width: 100, height: 100,
			want: geom.RectLTRB(0, 0, 100, 100), ok: true,
		},
		{name: "quarter turn", ctm: rotate90, width: 100, height: 50, want: geom.RectLTRB(-50, 0, 0, 100), ok: true},
		{name: "skewed", ctm: skew, width: 100, height: 100},
		{name: "non-finite", ctm: gfx.Scale(float32(math.Inf(1)), 1), width: 100, height: 100},
		{name: "overflowing extent", ctm: gfx.Scale(math.MaxFloat32, 1), width: math.MaxFloat32, height: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := snapPageBox(tc.width, tc.height, tc.ctm)
			if ok != tc.ok {
				t.Fatalf("snapPageBox ok = %v, want %v (got %v)", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("snapPageBox = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClipPageBoxBoundsDrawing pins that ClipPageBox confines drawing under every map snapPageBox accepts, the quarter
// turn included. A 40x40 page box on an 80x80 surface must leave the rest untouched.
func TestClipPageBoxBoundsDrawing(t *testing.T) {
	var wide gfx.Path
	wide.Rect(-100, -100, 400, 400) // Covers the whole surface, so only the clip can keep it off.
	for _, tc := range []struct {
		name string
		ctm  gfx.Matrix
		// paint is where the 40x40 page box lands on the surface under ctm.
		x0, y0, x1, y1 int
	}{
		{name: "axis aligned", ctm: gfx.Identity(), x0: 0, y0: 0, x1: 40, y1: 40},
		{name: "scaled", ctm: gfx.Scale(1.5, 1.5), x0: 0, y0: 0, x1: 60, y1: 60},
		{name: "translated", ctm: gfx.Translate(20, 20), x0: 20, y0: 20, x1: 60, y1: 60},
		// A quarter turn about the origin puts the box at x in [-40, 0], so the translate brings it back on surface.
		{name: "quarter turn", ctm: gfx.Matrix{B: 1, C: -1, E: 60, F: 10}, x0: 20, y0: 10, x1: 60, y1: 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDevice(t, 80, 80)
			d.ClipPageBox(40, 40, tc.ctm)
			d.FillPath(&wide, false, gfx.Identity(), redPaint())
			pix, stride, err := d.Pixels()
			if err != nil {
				t.Fatal(err)
			}
			for y := range 80 {
				for x := range 80 {
					px := pixelAt(t, pix, stride, x, y)
					inside := x >= tc.x0 && x < tc.x1 && y >= tc.y0 && y < tc.y1
					if inside && px != [4]uint8{255, 0, 0, 255} {
						t.Fatalf("pixel (%d, %d) inside the page box is %v, want opaque red", x, y, px)
					}
					if !inside && px[3] != 0 {
						t.Fatalf("pixel (%d, %d) outside the page box was painted: %v", x, y, px)
					}
				}
			}
		})
	}
}

// TestClipPageBoxSkewedFallback covers the branch snapPageBox declines: a skewed map has no pixel-aligned box, so the
// exact transformed parallelogram is pushed as a clip path instead.
func TestClipPageBoxSkewedFallback(t *testing.T) {
	var wide gfx.Path
	wide.Rect(-100, -100, 400, 400)
	d := newDevice(t, 80, 80)
	// Shear in y: the box's top edge stays on y = 0 while its right edge slides down to y = 40.
	d.ClipPageBox(40, 40, gfx.Matrix{A: 1, B: 1, D: 1})
	d.FillPath(&wide, false, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if px := pixelAt(t, pix, stride, 2, 10); px != [4]uint8{255, 0, 0, 255} {
		t.Errorf("pixel inside the sheared box is %v, want opaque red", px)
	}
	if px := pixelAt(t, pix, stride, 39, 5); px[3] != 0 {
		t.Errorf("pixel above the sheared box was painted: %v", px)
	}
	if px := pixelAt(t, pix, stride, 70, 70); px[3] != 0 {
		t.Errorf("pixel outside the sheared box was painted: %v", px)
	}
}
