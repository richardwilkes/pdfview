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
	"bytes"
	"errors"
	"image"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview"
	"github.com/richardwilkes/pdfview/internal/render"
)

func TestPDF(t *testing.T) {
	data, err := os.ReadFile("testfiles/corpus/glaive.pdf")
	if err != nil {
		t.Fatal(err)
	}

	var doc *pdfview.Document
	doc, err = pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()

	if pageCount := doc.PageCount(); pageCount != 2 {
		t.Fatalf("expected 2 pages, got %d", pageCount)
	}

	toc := doc.TableOfContents(100)
	if len(toc) != 66 {
		t.Fatalf("expected 66 TOC entries, got %d", len(toc))
	}

	// The TOC headings are very long, so spot-check a few.
	checkTOCEntry(t, toc, 0, "GLAIVE Mini (GMi) ", 0, 69, 163)
	checkTOCEntry(t, toc, 12, "Semibalanced: A new ", 0, 81, 680)
	checkTOCEntry(t, toc, 60, "What's that odd ", 1, 446, 691)

	var page *pdfview.RenderedPage
	page, err = doc.RenderPage(0, 100, 20, "GURPS")
	if err != nil {
		t.Fatal(err)
	}

	if len(page.SearchHits) != 9 {
		t.Fatalf("expected 9 search hits, got %d", len(page.SearchHits))
	}
	for i, one := range []image.Rectangle{
		image.Rect(152, 180, 193, 194),
		image.Rect(162, 208, 204, 221),
		image.Rect(265, 684, 306, 698),
		image.Rect(484, 311, 526, 324),
		image.Rect(670, 384, 712, 398),
		image.Rect(600, 567, 660, 585),
		image.Rect(180, 1131, 226, 1145),
		image.Rect(69, 126, 125, 143),
		image.Rect(425, 86, 460, 97),
	} {
		if page.SearchHits[i] != one {
			t.Errorf("search hit rect %d doesn't match, expected %v, got %v", i, one, page.SearchHits[i])
		}
	}

	if len(page.Links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(page.Links))
	}
	for i, one := range []pdfview.PageLink{
		{
			PageNumber: -1,
			URI:        "http://www.gamesdiner.com/glaive_mini",
			Bounds:     image.Rect(69, 163, 149, 180),
		},
		{
			PageNumber: -1,
			URI:        "http://www.gamesdiner.com",
			Bounds:     image.Rect(472, 1128, 604, 1145),
		},
	} {
		if *page.Links[i] != one {
			t.Errorf("link %d doesn't match, expected %#v, got %#v", i, one, *page.Links[i])
		}
	}

	if page.Image == nil {
		t.Fatal("expected image data, got nil")
	}
	if page.Image.Stride != 3308 {
		t.Errorf("expected an image stride of 3308, got %d", page.Image.Stride)
	}
	expectedBounds := image.Rect(0, 0, 827, 1170)
	if page.Image.Rect != expectedBounds {
		t.Errorf("expected an image bounds of %v, got %v", expectedBounds, page.Image.Rect)
	}

	// A negative page number is rejected before it reaches the engine.
	if _, err = doc.RenderPage(-1, 100, 20, ""); !errors.Is(err, pdfview.ErrInvalidPageNumber) {
		t.Errorf("expected ErrInvalidPageNumber for a negative page, got %v", err)
	}
	if _, err = doc.RenderPageForSize(-1, 800, 800, 20, ""); !errors.Is(err, pdfview.ErrInvalidPageNumber) {
		t.Errorf("expected ErrInvalidPageNumber for a negative page, got %v", err)
	}

	// A search with maxHits <= 0 must not panic and yields no hits.
	page, err = doc.RenderPage(0, 100, 0, "GURPS")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.SearchHits) != 0 {
		t.Errorf("expected 0 search hits with maxHits of 0, got %d", len(page.SearchHits))
	}
}

func TestMalformedPDF(t *testing.T) {
	// A valid %PDF prefix over garbage passes the prefix check and fails in the parser; that must surface as
	// ErrUnableToOpenPDF, not a panic.
	if _, err := pdfview.New([]byte("%PDF-1.7\nnot a real pdf"), 0); !errors.Is(err, pdfview.ErrUnableToOpenPDF) {
		t.Fatalf("expected ErrUnableToOpenPDF for a malformed document, got %v", err)
	}
}

func TestUseAfterRelease(t *testing.T) {
	data, err := os.ReadFile("testfiles/corpus/glaive.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}

	doc.Release()

	// A second Release is a no-op.
	doc.Release()

	if got := doc.PageCount(); got != 0 {
		t.Errorf("expected PageCount of 0 after release, got %d", got)
	}
	if got := doc.RequiresAuthentication(); got {
		t.Errorf("expected RequiresAuthentication of false after release, got %v", got)
	}
	if got := doc.Authenticate(""); got != 0 {
		t.Errorf("expected Authenticate status of 0 after release, got %v", got)
	}
	if got := doc.TableOfContents(100); got != nil {
		t.Errorf("expected nil TableOfContents after release, got %v", got)
	}
	if _, err = doc.RenderPage(0, 100, 20, ""); !errors.Is(err, pdfview.ErrDocumentReleased) {
		t.Errorf("expected ErrDocumentReleased from RenderPage after release, got %v", err)
	}
	if _, err = doc.RenderPageForSize(0, 800, 800, 20, ""); !errors.Is(err, pdfview.ErrDocumentReleased) {
		t.Errorf("expected ErrDocumentReleased from RenderPageForSize after release, got %v", err)
	}
}

func TestRenderPageForSizeLimits(t *testing.T) {
	data, err := os.ReadFile("testfiles/corpus/glaive.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()

	page, err := doc.RenderPageForSize(0, 800, 800, 0, "")
	if err != nil {
		t.Fatalf("unexpected error rendering for size: %v", err)
	}
	if page.Image == nil {
		t.Fatal("expected image data, got nil")
	}
	if b := page.Image.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 || b.Dx() > 800 || b.Dy() > 800 {
		t.Errorf("rendered image %v does not fit within 800x800", b)
	}

	for _, sz := range []struct{ w, h int }{{0, 800}, {800, 0}, {-1, 800}, {800, -1}} {
		if _, err = doc.RenderPageForSize(0, sz.w, sz.h, 0, ""); !errors.Is(err, pdfview.ErrInvalidPageSize) {
			t.Errorf("expected ErrInvalidPageSize for target size %dx%d, got %v", sz.w, sz.h, err)
		}
	}

	// Both render paths reject a request past OverallMaxPixels with ErrImageTooLarge before allocating.
	defer func(prev int) { pdfview.OverallMaxPixels = prev }(pdfview.OverallMaxPixels)
	pdfview.OverallMaxPixels = 100
	if _, err = doc.RenderPageForSize(0, 800, 800, 0, ""); !errors.Is(err, pdfview.ErrImageTooLarge) {
		t.Errorf("expected ErrImageTooLarge from RenderPageForSize when exceeding OverallMaxPixels, got %v", err)
	}
	if _, err = doc.RenderPage(0, 100, 0, ""); !errors.Is(err, pdfview.ErrImageTooLarge) {
		t.Errorf("expected ErrImageTooLarge from RenderPage when exceeding OverallMaxPixels, got %v", err)
	}
}

// TestOverLargeRenderUsesImageTooLarge pins that OverallMaxPixels defaults to the raster surface's own pixel cap: a
// request between the two would pass every guard and fail inside the surface allocation as ErrUnableToCreateImage
// instead of the documented ErrImageTooLarge.
func TestOverLargeRenderUsesImageTooLarge(t *testing.T) {
	if pdfview.OverallMaxPixels != render.MaxSurfacePixels {
		t.Fatalf("OverallMaxPixels default %d does not match the surface cap %d", pdfview.OverallMaxPixels,
			render.MaxSurfacePixels)
	}
	data, err := os.ReadFile("testfiles/corpus/glaive.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()

	// Derive the over-large request from the page's size at scale 1.
	base, err := doc.RenderPage(0, 72, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	b := base.Image.Bounds()
	// Aim halfway between the surface cap and math.MaxInt32/4, the former default. Only RenderPageForSize can ask for
	// such a size; RenderPage clamps its scale at 10x.
	target := float64(pdfview.OverallMaxPixels+math.MaxInt32/4) / 2
	scale := math.Sqrt(target / (float64(b.Dx()) * float64(b.Dy())))
	maxWidth := int(math.Ceil(float64(b.Dx()) * scale))
	maxHeight := int(math.Ceil(float64(b.Dy()) * scale))
	if _, err = doc.RenderPageForSize(0, maxWidth, maxHeight, 0, ""); !errors.Is(err, pdfview.ErrImageTooLarge) {
		t.Errorf("expected ErrImageTooLarge for a %dx%d RenderPageForSize request, got %v", maxWidth, maxHeight, err)
	}
}

// internalLinkPDF is a two-page document with two internal links on page 0, both targeting page 1: one via an explicit
// /XYZ destination and one via a named destination (Chapter2) that resolves to a /Fit destination with no point.
// startxref 0 makes the engine rebuild the xref.
const internalLinkPDF = `%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R /Names << /Dests 6 0 R >> >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Annots [5 0 R 7 0 R] >>
endobj
4 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>
endobj
5 0 obj
<< /Type /Annot /Subtype /Link /Rect [10 10 90 30] /Border [0 0 0] /Dest [4 0 R /XYZ 30 150 0] >>
endobj
6 0 obj
<< /Names [(Chapter2) [4 0 R /Fit]] >>
endobj
7 0 obj
<< /Type /Annot /Subtype /Link /Rect [10 40 90 60] /Border [0 0 0] /A << /S /GoTo /D (Chapter2) >> >>
endobj
trailer
<< /Root 1 0 R /Size 8 >>
startxref
0
%%EOF
`

func TestInternalLinks(t *testing.T) {
	doc, err := pdfview.New([]byte(internalLinkPDF), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()

	page, err := doc.RenderPage(0, 72, 0, "") // 72 dpi: scale 1, so DestPoint values are page points.
	if err != nil {
		t.Fatal(err)
	}

	if len(page.Links) != 2 {
		t.Fatalf("expected 2 internal links, got %d", len(page.Links))
	}
	// /XYZ 30 150 on a 200-tall page is (30, 50) in top-left/y-down space; /Fit has no point and resolves to (0, 0).
	// Match by DestPoint rather than order.
	var sawXYZ, sawFit bool
	for i, l := range page.Links {
		if l.PageNumber != 1 {
			t.Errorf("link %d: expected PageNumber 1 (0-based second page), got %d", i, l.PageNumber)
		}
		if l.URI != "" {
			t.Errorf("link %d: expected empty URI for an internal link, got %q", i, l.URI)
		}
		switch l.DestPoint {
		case image.Pt(30, 50):
			sawXYZ = true
		case image.Pt(0, 0):
			sawFit = true
		default:
			t.Errorf("link %d: unexpected DestPoint %v", i, l.DestPoint)
		}
	}
	if !sawXYZ {
		t.Error("expected a link with the /XYZ DestPoint (30, 50)")
	}
	if !sawFit {
		t.Error("expected a link with the /Fit DestPoint (0, 0)")
	}
}

// TestStemDarkening pins the public switch: it defaults to enabled, darkened text carries more ink than the exact
// fill, the toggle takes effect on the same document — whose reused device and store must not serve coverage planes
// rasterized under the other setting — and re-enabling reproduces the darkened render byte for byte.
func TestStemDarkening(t *testing.T) {
	data, err := os.ReadFile("testfiles/corpus/text-type1.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()
	if !doc.StemDarkening() {
		t.Fatal("stem darkening should default to enabled")
	}
	renderPix := func() []byte {
		page, renderErr := doc.RenderPage(0, 150, 0, "")
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		return page.Image.Pix
	}
	// Most PDF pages paint no background, so summed alpha is summed text coverage.
	ink := func(pix []byte) (total uint64) {
		for i := 3; i < len(pix); i += 4 {
			total += uint64(pix[i])
		}
		return total
	}
	dark := renderPix()
	doc.SetStemDarkening(false)
	if doc.StemDarkening() {
		t.Fatal("SetStemDarkening(false) did not stick")
	}
	exact := renderPix()
	if ink(dark) <= ink(exact) {
		t.Fatalf("darkened render has no more ink than the exact fill: %d vs %d", ink(dark), ink(exact))
	}
	doc.SetStemDarkening(true)
	if !bytes.Equal(dark, renderPix()) {
		t.Fatal("re-enabling stem darkening did not reproduce the darkened render; a cached plane leaked across the toggle")
	}
}

func checkTOCEntry(t *testing.T, toc []*pdfview.TOCEntry, index int, prefix string, pageNumber, pageX, pageY int) {
	t.Helper()
	if !strings.HasPrefix(toc[index].Title, prefix) {
		t.Errorf("TOC entry %d's Title does not start with %q, instead is %q", index, prefix, toc[index].Title)
	}
	if toc[index].PageNumber != pageNumber {
		t.Errorf("TOC entry %d's PageNumber is not %d, got %d", index, pageNumber, toc[index].PageNumber)
	}
	if toc[index].PageX != pageX {
		t.Errorf("TOC entry %d's PageX is not %d, got %d", index, pageX, toc[index].PageX)
	}
	if toc[index].PageY != pageY {
		t.Errorf("TOC entry %d's PageY is not %d, got %d", index, pageY, toc[index].PageY)
	}
	if toc[index].Children != nil {
		t.Errorf("TOC entry %d's Children expected to be nil", index)
	}
}
