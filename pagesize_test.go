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
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview"
)

// TestPageRenderSizeMatchesRenderPage pins the property PageRenderSize exists for: the dimensions it reports for a
// page at a given dpi are exactly the dimensions of the image a RenderPage call at that dpi produces, so a caller can
// lay out a whole document from PageRenderSize alone and every later render lands in its slot to the pixel. It walks
// the corpus, skipping files that fail to open or require authentication, and compares every page (capped per file to
// bound the suite's render time) at more than one dpi. Pages RenderPage itself cannot load are skipped — PageRenderSize
// stops at the load stage, so raster-stage failures have no dimensions to compare.
func TestPageRenderSizeMatchesRenderPage(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testfiles", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	const maxPagesPerFile = 5
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			data, rerr := os.ReadFile(filepath.Join("testfiles", "corpus", entry.Name()))
			if rerr != nil {
				t.Fatal(rerr)
			}
			doc, derr := pdfview.New(data, 0)
			if derr != nil {
				t.Skipf("New: %v", derr)
			}
			defer doc.Release()
			if doc.RequiresAuthentication() {
				t.Skip("requires authentication")
			}
			for pageNum := range min(doc.PageCount(), maxPagesPerFile) {
				for _, dpi := range []int{72, 144} {
					width, height, serr := doc.PageRenderSize(pageNum, dpi)
					page, perr := doc.RenderPage(pageNum, dpi, 0, "")
					if perr != nil {
						continue
					}
					if serr != nil {
						t.Errorf("page %d dpi %d: PageRenderSize error = %v but RenderPage succeeded", pageNum, dpi, serr)
						continue
					}
					if got := page.Image.Rect; width != got.Dx() || height != got.Dy() {
						t.Errorf("page %d dpi %d: PageRenderSize = %dx%d, RenderPage image = %dx%d",
							pageNum, dpi, width, height, got.Dx(), got.Dy())
					}
				}
			}
		})
	}
}

// TestPageSizeContract pins the error contract of PageSize and PageRenderSize on a live document — out-of-range pages
// report ErrInvalidPageNumber, release flips every call to ErrDocumentReleased — plus the sanity of the values a valid
// page reports, including the sub-72 dpi floor both PageRenderSize and RenderPage share via dpiToScale.
func TestPageSizeContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", "glaive.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()
	width, height, err := doc.PageSize(0)
	if err != nil {
		t.Fatalf("PageSize error = %v", err)
	}
	if width <= 0 || height <= 0 {
		t.Errorf("PageSize = %vx%v, want positive extents", width, height)
	}
	pxWidth, pxHeight, err := doc.PageRenderSize(0, 0)
	if err != nil {
		t.Fatalf("PageRenderSize error = %v", err)
	}
	page, err := doc.RenderPage(0, 0, 0, "")
	if err != nil {
		t.Fatalf("RenderPage error = %v", err)
	}
	if got := page.Image.Rect; pxWidth != got.Dx() || pxHeight != got.Dy() {
		t.Errorf("dpi 0: PageRenderSize = %dx%d, RenderPage image = %dx%d", pxWidth, pxHeight, got.Dx(), got.Dy())
	}
	for _, pageNum := range []int{-1, doc.PageCount()} {
		if _, _, err = doc.PageSize(pageNum); !errors.Is(err, pdfview.ErrInvalidPageNumber) {
			t.Errorf("PageSize(%d) error = %v, want ErrInvalidPageNumber", pageNum, err)
		}
		if _, _, err = doc.PageRenderSize(pageNum, 72); !errors.Is(err, pdfview.ErrInvalidPageNumber) {
			t.Errorf("PageRenderSize(%d) error = %v, want ErrInvalidPageNumber", pageNum, err)
		}
	}
	doc.Release()
	if _, _, err = doc.PageSize(0); !errors.Is(err, pdfview.ErrDocumentReleased) {
		t.Errorf("PageSize after release error = %v, want ErrDocumentReleased", err)
	}
	if _, _, err = doc.PageRenderSize(0, 72); !errors.Is(err, pdfview.ErrDocumentReleased) {
		t.Errorf("PageRenderSize after release error = %v, want ErrDocumentReleased", err)
	}
}
