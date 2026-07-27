// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

import (
	"os"
	"testing"
)

// TestRenderSpecExtentsClampToRequestedBox pins that a fit-to-size render never returns an image larger than the box
// it was asked to fit within. The fit scale is computed in float64 while renderExtent redoes the multiply in float32
// and ceils with a fixed 0.001 epsilon, so above roughly 17,000 px the float32 rounding error outgrows that epsilon:
// a 67 pt wide page asked to fit 17,199 px produces an unclamped extent of 17,200. The first arm pins that overshoot
// so the clamp cannot be quietly removed as dead code; the second pins the clamp itself.
func TestRenderSpecExtentsClampToRequestedBox(t *testing.T) {
	const (
		pageWidth  = 67
		pageHeight = 20
		maxWidth   = 17199
		maxHeight  = 1 << 20 // Large enough that the width is what binds the fit scale.
	)
	pg := &page{width: pageWidth, height: pageHeight}
	scale := float64(maxWidth) / pageWidth
	if got := renderExtent(pageWidth, scale); got != maxWidth+1 {
		t.Fatalf("renderExtent(%d, %v) = %d, want the %d overshoot this clamp exists for",
			pageWidth, scale, got, maxWidth+1)
	}
	width, height := renderSpec{scale: scale, maxWidth: maxWidth, maxHeight: maxHeight}.extents(pg)
	if width != maxWidth {
		t.Errorf("clamped width = %d, want %d", width, maxWidth)
	}
	if height > maxHeight || height <= 0 {
		t.Errorf("clamped height = %d, want it within (0, %d]", height, maxHeight)
	}
	// Without caps — the RenderPage path — the extents are whatever the scale produces.
	if width, height = (renderSpec{scale: scale}).extents(pg); width != maxWidth+1 {
		t.Errorf("uncapped width = %d, want %d", width, maxWidth+1)
	}
	if height <= 0 {
		t.Errorf("uncapped height = %d, want a positive extent", height)
	}
}

// TestReusedDeviceRespectsCacheBudget pins that the raster device kept between renders is charged against the
// documented maxCacheSize budget. It is not part of the store, so nothing else bounds it: a document rendered once at
// high dpi would otherwise hold a width*height*4 surface for its whole lifetime no matter how small a cache the caller
// asked for, and an application holding N such documents would pay N surfaces.
func TestReusedDeviceRespectsCacheBudget(t *testing.T) {
	data, err := os.ReadFile("testfiles/corpus/vectors.pdf")
	if err != nil {
		t.Fatal(err)
	}
	// vectors.pdf is a 200x200 pt page, so a 72 dpi render allocates a 200*200*4 = 160,000 byte surface.
	const surface = 200 * 200 * 4
	for _, tc := range []struct {
		name         string
		maxCacheSize uint64
		retained     bool
	}{
		{name: "unlimited", maxCacheSize: 0, retained: true},
		{name: "budget above the surface", maxCacheSize: surface + 1, retained: true},
		{name: "budget exactly the surface", maxCacheSize: surface, retained: true},
		{name: "budget below the surface", maxCacheSize: surface - 1, retained: false},
		{name: "tiny budget", maxCacheSize: 1, retained: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, docErr := New(data, tc.maxCacheSize)
			if docErr != nil {
				t.Fatal(docErr)
			}
			defer doc.Release()
			rendered, renderErr := doc.RenderPage(0, 72, 0, "")
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			if b := rendered.Image.Bounds(); b.Dx() != 200 || b.Dy() != 200 {
				t.Fatalf("expected a 200x200 image, got %v", b)
			}
			if retained := doc.eng.dev != nil; retained != tc.retained {
				t.Errorf("device retained = %v, want %v for maxCacheSize %d", retained, tc.retained, tc.maxCacheSize)
			}
			// Whether the surface is kept must not change what was rendered, so a second render still succeeds and
			// produces the same dimensions.
			again, againErr := doc.RenderPage(0, 72, 0, "")
			if againErr != nil {
				t.Fatal(againErr)
			}
			if again.Image.Bounds() != rendered.Image.Bounds() {
				t.Errorf("second render %v differs from the first %v", again.Image.Bounds(), rendered.Image.Bounds())
			}
		})
	}
}

// TestRestoreToCountContainsPanic pins that a panic raised by the canvas restore itself is contained rather than
// escaping DrawPage. RestoreToCount pops device clip stacks and composites any open SaveLayer — exactly the state a
// panic mid-transparency-group leaves behind — so the restore that matters most is also the one most able to panic. A
// nil canvas is the cheapest way to make it panic.
func TestRestoreToCountContainsPanic(t *testing.T) {
	if restoreToCount(nil, 0) {
		t.Error("restoreToCount reported success on a canvas that cannot be restored")
	}
}
