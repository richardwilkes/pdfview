// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package imaging

import "testing"

// A mask finer than its base image must composite onto the finer grid, keeping the mask's own detail: the oracle paints
// an image and its mask each at its own resolution, so decimating the mask onto the base's grid loses coverage the
// oracle keeps.
func TestCompositeAlphaFinerMask(t *testing.T) {
	const (
		baseDim = 2
		maskDim = 8
	)
	img := &Image{Width: baseDim, Height: baseDim, Pix: make([]byte, baseDim*baseDim*4)}
	for i := range baseDim * baseDim {
		img.Pix[i*4] = uint8(10 + i) // A per-base-pixel marker to check the base survives the expansion.
		img.Pix[i*4+3] = 255
	}
	plane := make([]byte, maskDim*maskDim)
	for y := range maskDim {
		for x := range maskDim {
			if (x+y)%2 == 0 {
				plane[y*maskDim+x] = 255
			}
		}
	}
	compositeAlpha(img, plane, maskDim, maskDim)
	if img.Width != maskDim || img.Height != maskDim {
		t.Fatalf("composite is %dx%d, want %dx%d", img.Width, img.Height, maskDim, maskDim)
	}
	if len(img.Pix) != maskDim*maskDim*4 {
		t.Fatalf("composite has %d pixel bytes, want %d", len(img.Pix), maskDim*maskDim*4)
	}
	if !img.HasAlpha {
		t.Fatal("composite did not report alpha")
	}
	for y := range maskDim {
		for x := range maskDim {
			off := (y*maskDim + x) * 4
			wantAlpha := byte(0)
			if (x+y)%2 == 0 {
				wantAlpha = 255
			}
			if got := img.Pix[off+3]; got != wantAlpha {
				t.Fatalf("alpha at (%d,%d) is %d, want %d", x, y, got, wantAlpha)
			}
			base := (y*baseDim/maskDim)*baseDim + x*baseDim/maskDim
			if got, want := img.Pix[off], uint8(10+base); got != want {
				t.Fatalf("red at (%d,%d) is %d, want %d", x, y, got, want)
			}
		}
	}
}

// A mask no finer than its base image on either axis composites on the base's own grid, the arrangement the /SMask
// goldens pin: the expansion must not disturb it.
func TestCompositeAlphaCoarserMaskKeepsBaseGrid(t *testing.T) {
	const (
		baseDim = 8
		maskDim = 2
	)
	img := &Image{Width: baseDim, Height: baseDim, Pix: make([]byte, baseDim*baseDim*4)}
	for i := range baseDim * baseDim {
		img.Pix[i*4+3] = 255 //nolint:mnd // Opaque before the mask applies.
	}
	plane := []byte{255, 0, 0, 255}
	compositeAlpha(img, plane, maskDim, maskDim)
	if img.Width != baseDim || img.Height != baseDim {
		t.Fatalf("composite is %dx%d, want %dx%d", img.Width, img.Height, baseDim, baseDim)
	}
	for y := range baseDim {
		for x := range baseDim {
			wantAlpha := byte(0)
			if (x < baseDim/2) == (y < baseDim/2) {
				wantAlpha = 255
			}
			if got := img.Pix[(y*baseDim+x)*4+3]; got != wantAlpha {
				t.Fatalf("alpha at (%d,%d) is %d, want %d", x, y, got, wantAlpha)
			}
		}
	}
}
