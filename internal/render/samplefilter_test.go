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
	"testing"

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
)

// drawEdgeImage renders a samples x samples image split into a black half and a white half, without /Interpolate,
// magnified onto a square device of the given size, and returns a middle row's red channel. Up to 2x the oracle blends
// the sample boundary; past 2x it reproduces it hard (see blendsSamples).
func drawEdgeImage(t *testing.T, samples, deviceSize int) []byte {
	t.Helper()
	d, err := New(deviceSize, deviceSize)
	if err != nil {
		t.Fatal(err)
	}
	pix := make([]byte, samples*samples*4)
	for y := range samples {
		for x := range samples {
			off := (y*samples + x) * 4
			if x >= samples/2 {
				pix[off], pix[off+1], pix[off+2] = 255, 255, 255
			}
			pix[off+3] = 255
		}
	}
	img := &imaging.Image{Width: samples, Height: samples, Pix: pix}
	d.FillImage(img, gfx.Matrix{A: float32(deviceSize), D: float32(deviceSize)}, device.Paint{Alpha: 1})
	pixels, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	row := make([]byte, deviceSize)
	for x := range deviceSize {
		row[x] = pixels[deviceSize/2*stride+x*4]
	}
	return row
}

// countIntermediate reports how many entries are neither of the two source values, which is exactly the blended
// boundary a linear filter produces and a nearest one cannot.
func countIntermediate(row []byte) int {
	n := 0
	for _, v := range row {
		if v != 0 && v != 255 {
			n++
		}
	}
	return n
}

// An image magnified less than 2x has its sample boundary blended even without /Interpolate.
func TestImageBlendsBelowDoubleMagnification(t *testing.T) {
	const samples = 8
	row := drawEdgeImage(t, samples, samples*3/2) // 1.5x.
	if countIntermediate(row) == 0 {
		t.Errorf("no blended pixels at 1.5x magnification: %v", row)
	}
}

// Past 2x magnification the oracle drops back to unfiltered sampling, so the boundary must stay hard.
func TestImageDoesNotBlendAtOrAboveDoubleMagnification(t *testing.T) {
	const samples = 8
	for _, size := range []int{samples * 5 / 2, samples * 4} { // 2.5x and 4x.
		if row := drawEdgeImage(t, samples, size); countIntermediate(row) != 0 {
			t.Errorf("blended pixels magnifying %d samples onto %d pixels: %v", samples, size, row)
		}
	}
}

// TestBlendsSamplesPredicate pins the per-axis behavior: either axis inside the band turns blending on, either axis
// past 2x turns it back off, and minification never blends.
func TestBlendsSamplesPredicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctm  gfx.Matrix
		want bool
	}{
		{name: "unmagnified", ctm: gfx.Matrix{A: 10, D: 10}, want: false},
		{name: "minified", ctm: gfx.Matrix{A: 5, D: 5}, want: false},
		{name: "in band both axes", ctm: gfx.Matrix{A: 15, D: 15}, want: true},
		{name: "in band x only", ctm: gfx.Matrix{A: 15, D: 10}, want: true},
		{name: "in band y only", ctm: gfx.Matrix{A: 10, D: 15}, want: true},
		{name: "exactly double", ctm: gfx.Matrix{A: 20, D: 20}, want: true},
		{name: "past double both axes", ctm: gfx.Matrix{A: 21, D: 21}, want: false},
		{name: "past double x only", ctm: gfx.Matrix{A: 21, D: 15}, want: false},
		{name: "past double y only", ctm: gfx.Matrix{A: 15, D: 21}, want: false},
		{name: "rotated in band", ctm: gfx.Matrix{B: 15, C: -15}, want: true},
		{name: "rotated past double", ctm: gfx.Matrix{B: 21, C: -21}, want: false},
		{name: "huge components stay finite", ctm: gfx.Matrix{A: 3e38, D: 3e38}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blendsSamples(tc.ctm, 10, 10); got != tc.want {
				t.Errorf("blendsSamples(%v) = %v, want %v", tc.ctm, got, tc.want)
			}
		})
	}
}

// TestMaskedOutSamplesDoNotTintTheBackground pins that an alpha-carrying image reaches canvas premultiplied: the
// integer-translation sprite lane blits the bytes unchanged onto the premultiplied surface, so a masked-out sample that
// kept its color would add that color to whatever it covers.
func TestMaskedOutSamplesDoNotTintTheBackground(t *testing.T) {
	const dim = 8
	d, err := New(dim, dim)
	if err != nil {
		t.Fatal(err)
	}
	background := &imaging.Image{Width: 1, Height: 1, Pix: []byte{200, 200, 100, 255}}
	d.FillImage(background, gfx.Matrix{A: dim, D: dim}, device.Paint{Alpha: 1})
	pix := make([]byte, dim*dim*4)
	for i := range dim * dim {
		// Fully masked out (alpha stays zero), but the samples keep their color.
		copy(pix[i*4:], []byte{40, 80, 160})
	}
	// A y-flipped 1:1 draw, the arrangement a PDF image CTM produces and the one that lands in the sprite lane.
	d.FillImage(&imaging.Image{Width: dim, Height: dim, HasAlpha: true, Pix: pix},
		gfx.Matrix{A: dim, D: -dim, F: dim}, device.Paint{Alpha: 1})
	pixels, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	for y := range dim {
		for x := range dim {
			off := y*stride + x*4
			if pixels[off] != 200 || pixels[off+1] != 200 || pixels[off+2] != 100 {
				t.Fatalf("masked-out image tinted (%d,%d): %v", x, y, pixels[off:off+4])
			}
		}
	}
}
