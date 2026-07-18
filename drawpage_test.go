// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/canvas/geom"
	"github.com/richardwilkes/canvas/imagecore"
	"github.com/richardwilkes/canvas/surface"

	"github.com/richardwilkes/pdfview"
)

// glaiveName is the corpus name of the GLAIVE fixture.
const glaiveName = "glaive"

// TestDrawPage pins the DrawPage contract: drawing onto a caller-created raster surface with ctm =
// geom.ScaleMatrix(dpi/72, dpi/72) reproduces RenderPage's output at that dpi. The page CTM composition is
// bit-identical between the two paths, so content without text compares byte-exact. Text renders through RenderPage's
// per-glyph coverage blits but DrawPage's merged-outline fills; the two composite identically except where adjacent
// glyphs' antialiasing fringes overlap — the merged path unions the outlines where per-glyph coverage composites twice
// — so pages with text compare on the fraction of diverging pixels (measured worst across these arms: 0.004% of pixels
// over Δ24, 0.032% over Δ8, all of it isolated fringe pixels further amplified by the straight-alpha comparison at
// near-zero alpha).
func TestDrawPage(t *testing.T) {
	for _, tc := range []struct {
		name string
		page int
		dpi  int
		text bool // has text: allow the fringe-pixel divergence described above
	}{
		{name: "vectors", page: 0, dpi: 72},
		{name: "vectors", page: 0, dpi: 100},
		{name: "shading-axial", page: 0, dpi: 72},
		{name: glaiveName, page: 0, dpi: 72, text: true},
		{name: glaiveName, page: 1, dpi: 150, text: true},
		{name: "annotations", page: 0, dpi: 72, text: true},
	} {
		t.Run(fmt.Sprintf("%s-p%d-%ddpi", tc.name, tc.page, tc.dpi), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testfiles", "corpus", tc.name+".pdf"))
			if err != nil {
				t.Fatal(err)
			}
			d, err := pdfview.New(data, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Release()
			rendered, err := d.RenderPage(tc.page, tc.dpi, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			img := rendered.Image
			width := img.Rect.Dx()
			height := img.Rect.Dy()
			surf := surface.NewRasterN32Premul(int32(width), int32(height), nil)
			if surf == nil {
				t.Fatal("unable to create surface")
			}
			c := surf.Canvas()
			saves := c.SaveCount()
			scale := float32(tc.dpi) / 72
			ctm := geom.ScaleMatrix(scale, scale)
			if err = d.DrawPage(c, tc.page, ctm); err != nil {
				t.Fatal(err)
			}
			if got := c.SaveCount(); got != saves {
				t.Fatalf("DrawPage changed the canvas save count: %d != %d", got, saves)
			}
			pix := readSurfaceNRGBA(t, surf, width, height)
			if len(pix) != len(img.Pix) {
				t.Fatalf("pixel buffer length mismatch: %d != %d", len(pix), len(img.Pix))
			}
			total := width * height
			var over8, over24 int
			for p := range total {
				worst := 0
				for j := p * 4; j < p*4+4; j++ {
					delta := int(pix[j]) - int(img.Pix[j])
					if delta < 0 {
						delta = -delta
					}
					if delta > worst {
						worst = delta
					}
				}
				if !tc.text && worst > 0 {
					t.Fatalf("DrawPage output diverges from RenderPage at pixel %d: delta %d", p, worst)
				}
				if worst > 8 {
					over8++
				}
				if worst > 24 {
					over24++
				}
			}
			// ~3x headroom over the measured worst (0.004% over Δ24, 0.032% over Δ8) so a genuine regression still
			// trips.
			if pctOver24 := 100 * float64(over24) / float64(total); pctOver24 > 0.012 {
				t.Fatalf("DrawPage output diverges from RenderPage: %.4f%% of pixels over Δ24", pctOver24)
			}
			if pctOver8 := 100 * float64(over8) / float64(total); pctOver8 > 0.1 {
				t.Fatalf("DrawPage output diverges from RenderPage: %.4f%% of pixels over Δ8", pctOver8)
			}
		})
	}
}

// readSurfaceNRGBA reads the surface back premultiplied and converts to straight alpha exactly as renderPage does
// (round half up), so the bytes are comparable to RenderPage's image.
func readSurfaceNRGBA(t *testing.T, surf *surface.Surface, width, height int) []byte {
	t.Helper()
	snap := surf.MakeImageSnapshot()
	if snap == nil {
		t.Fatal("unable to snapshot surface")
	}
	stride := width * 4
	pix := make([]byte, stride*height)
	info := imagecore.ImageInfo{
		Width:     int32(width),
		Height:    int32(height),
		ColorType: imagecore.ColorTypeRGBA8888,
		AlphaType: imagecore.AlphaTypePremul,
	}
	if !snap.ReadPixels(info, pix, stride, 0, 0, imagecore.CachingDisallow) {
		t.Fatal("unable to read surface pixels")
	}
	for i := 0; i+3 < len(pix); i += 4 {
		switch a := pix[i+3]; a {
		case 0, 255:
		default:
			for j := i; j < i+3; j++ {
				v := (int(pix[j])*0xff + int(a)/2) / int(a)
				if v > 0xff {
					v = 0xff
				}
				pix[j] = uint8(v)
			}
		}
	}
	return pix
}

// TestDrawPageErrors pins DrawPage's error contract: page-number validation, nil canvas rejection, and the
// released-document sentinel, none of which may panic or touch the canvas.
func TestDrawPageErrors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", "vectors.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	surf := surface.NewRasterN32Premul(8, 8, nil)
	if surf == nil {
		t.Fatal("unable to create surface")
	}
	c := surf.Canvas()
	if err = d.DrawPage(c, -1, geom.IdentityMatrix()); !errors.Is(err, pdfview.ErrInvalidPageNumber) {
		t.Fatalf("expected ErrInvalidPageNumber, got %v", err)
	}
	if err = d.DrawPage(c, 99, geom.IdentityMatrix()); !errors.Is(err, pdfview.ErrInvalidPageNumber) {
		t.Fatalf("expected ErrInvalidPageNumber, got %v", err)
	}
	if err = d.DrawPage(nil, 0, geom.IdentityMatrix()); !errors.Is(err, pdfview.ErrUnableToCreateImage) {
		t.Fatalf("expected ErrUnableToCreateImage, got %v", err)
	}
	d.Release()
	if err = d.DrawPage(c, 0, geom.IdentityMatrix()); !errors.Is(err, pdfview.ErrDocumentReleased) {
		t.Fatalf("expected ErrDocumentReleased, got %v", err)
	}
}
