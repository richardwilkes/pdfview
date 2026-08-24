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
	"image"
	"os"
	"path/filepath"
	"testing"
)

// openInternal opens a corpus document for the white-box tests below and releases it when the test ends.
func openInternal(t *testing.T, name string) *Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", name))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(data, 0)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(d.Release)
	return d
}

// TestTextPageSpaceMatchesTheRenderSpec pins the agreement the public ForSize test can only observe where text
// happens to reach: a TextPage's pixel space is the one renderSpec.extents hands the render, scale and bounds both.
// The bounds matter beyond the scale because a fit spec carries the caller's box and extents clamps to it — a clamp
// that bites only when the float32 extent multiply rounds past the box, which no rectangle on these pages is near
// enough to the edge to reveal from the outside.
func TestTextPageSpaceMatchesTheRenderSpec(t *testing.T) {
	for _, tc := range []struct {
		name                string
		file                string
		maxWidth, maxHeight int
		clamps              bool
	}{
		{name: "tiny box", file: "irs-fw9.pdf", maxWidth: 200, maxHeight: 200},
		{name: "height-limited", file: "irs-fw9.pdf", maxWidth: 813, maxHeight: 611},
		{name: "width-limited", file: "text-std14.pdf", maxWidth: 500, maxHeight: 999},
		{name: "larger than the page", file: "text-std14.pdf", maxWidth: 1000, maxHeight: 999},
		// Past roughly 17,000 px the float32 extent multiply outgrows renderExtent's 0.001 epsilon (see renderSpec),
		// so the unclamped height lands one row past the box the caller asked to fit within. Extraction is scale-free,
		// so labeling for an image this size costs nothing and allocates nothing.
		{name: "the clamp bites", file: "rotate90.pdf", maxWidth: 19205, maxHeight: 19205, clamps: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openInternal(t, tc.file)
			pg, err := d.eng.loadPage(0)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := fitSpec(pg, tc.maxWidth, tc.maxHeight)
			if err != nil {
				t.Fatalf("fitSpec: %v", err)
			}
			width, height := spec.extents(pg)
			unclampedW, unclampedH := renderSpec{scale: spec.scale}.extents(pg)
			if bit := unclampedW != width || unclampedH != height; bit != tc.clamps {
				t.Fatalf("the box clamp bit = %v, want %v (clamped %dx%d, unclamped %dx%d); the case no longer tests "+
					"what it names", bit, tc.clamps, width, height, unclampedW, unclampedH)
			}
			base, err := d.TextPage(0, 72)
			if err != nil {
				t.Fatal(err)
			}
			fitted, err := base.ForSize(tc.maxWidth, tc.maxHeight)
			if err != nil {
				t.Fatalf("ForSize: %v", err)
			}
			want := pixelSpace{scale: spec.scale, bounds: image.Rect(0, 0, width, height)}
			if fitted.space != want {
				t.Errorf("ForSize labeled the text %+v, want the render spec's %+v", fitted.space, want)
			}
			// The page extent travels with the text, so a second re-labeling is sized from the page rather than from
			// whatever the first one left behind.
			if fitted.pg != *pg {
				t.Errorf("the fitted page carries extent %+v, want %+v", fitted.pg, *pg)
			}
			if again, aerr := fitted.ForSize(tc.maxWidth, tc.maxHeight); aerr != nil {
				t.Errorf("re-labeling a re-labeled page: %v", aerr)
			} else if again.space != want {
				t.Errorf("re-labeling a re-labeled page gave %+v, want %+v", again.space, want)
			}
		})
	}
}

// TestTextPageSpaceMatchesTheRenderedImage pins the same agreement against the image itself for the sizes a test can
// afford to rasterize: the rectangle the caller receives is the rectangle the text was labeled against.
func TestTextPageSpaceMatchesTheRenderedImage(t *testing.T) {
	d := openInternal(t, "irs-fw9.pdf")
	base, err := d.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	for _, box := range [][2]int{{200, 200}, {813, 611}, {1000, 999}} {
		rendered, rerr := d.RenderPageForSize(0, box[0], box[1], 0, "")
		if rerr != nil {
			t.Fatalf("box %v: %v", box, rerr)
		}
		fitted, ferr := base.ForSize(box[0], box[1])
		if ferr != nil {
			t.Fatalf("box %v: %v", box, ferr)
		}
		if fitted.space.bounds != rendered.Image.Rect {
			t.Errorf("box %v: the text is labeled for %v, the image is %v", box, fitted.space.bounds,
				rendered.Image.Rect)
		}
	}
	// The dpi entry point and AtDPI label from the same authority too.
	for _, dpi := range []int{72, 150, 432} {
		rendered, rerr := d.RenderPage(0, dpi, 0, "")
		if rerr != nil {
			t.Fatalf("%d dpi: %v", dpi, rerr)
		}
		if got := base.AtDPI(dpi); got.space.bounds != rendered.Image.Rect {
			t.Errorf("%d dpi: AtDPI labeled for %v, the image is %v", dpi, got.space.bounds, rendered.Image.Rect)
		}
	}
}
