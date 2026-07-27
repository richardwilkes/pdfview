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
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/canvas/geom"
	"github.com/richardwilkes/canvas/surface"

	"github.com/richardwilkes/pdfview"
)

// TestZeroValueDocument pins that the exported Document's zero value — and a nil *Document — answer every method with
// the value documented for a released document rather than panicking. Document embeds an unexported pointer, so both
// would otherwise dereference nil on the very first statement of each method (the mutex lives behind that pointer),
// contradicting the package's promise that panics never escape the public API.
func TestZeroValueDocument(t *testing.T) {
	surf := surface.NewRasterN32Premul(8, 8, nil)
	if surf == nil {
		t.Fatal("unable to create surface")
	}
	for _, tc := range []struct {
		doc  *pdfview.Document
		name string
	}{
		{name: "zero value", doc: &pdfview.Document{}},
		{name: "nil pointer", doc: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.doc
			if got := doc.PageCount(); got != 0 {
				t.Errorf("PageCount = %d, want 0", got)
			}
			if got := doc.RequiresAuthentication(); got {
				t.Error("RequiresAuthentication = true, want false")
			}
			if got := doc.Authenticate("secret"); got != 0 {
				t.Errorf("Authenticate = %d, want 0", got)
			}
			if got := doc.TableOfContents(72); got != nil {
				t.Errorf("TableOfContents = %v, want nil", got)
			}
			if _, err := doc.RenderPage(0, 72, 0, ""); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("RenderPage error = %v, want ErrDocumentReleased", err)
			}
			if _, err := doc.RenderPageForSize(0, 800, 800, 0, ""); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("RenderPageForSize error = %v, want ErrDocumentReleased", err)
			}
			if err := doc.DrawPage(surf.Canvas(), 0, geom.IdentityMatrix()); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("DrawPage error = %v, want ErrDocumentReleased", err)
			}
			doc.Release() // Idempotent and safe with nothing behind it.
			doc.Release()
		})
	}
}

// TestOpenFailuresNeverReportPDFContext pins that ErrUnableToCreatePDFContext is not among the errors New can return.
// It is exported and documented, but only for source compatibility with the MuPDF-based binding this package succeeds,
// where it reported a failure to create the library's fz_context; the pure-Go engine has no such context to establish.
func TestOpenFailuresNeverReportPDFContext(t *testing.T) {
	corpus, err := os.ReadFile(filepath.Join("testfiles", "corpus", "vectors.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		// want is the sentinel New must report, or nil when the outcome is not pinned — the repair scan can and does
		// rescue some damaged input, and what matters here is only that the dead sentinel never appears.
		want error
		name string
		data []byte
	}{
		{name: "empty", data: nil, want: pdfview.ErrNotPDFData},
		{name: "not a pdf", data: []byte("this is plain text, not a document at all"), want: pdfview.ErrNotPDFData},
		{name: "header only", data: []byte("%PDF-1.7\n"), want: pdfview.ErrUnableToOpenPDF},
		{name: "no objects", data: []byte("%PDF-1.7\ntrailer\n<< >>\nstartxref\n0\n%%EOF\n"), want: pdfview.ErrUnableToOpenPDF},
		{name: "truncated", data: corpus[:len(corpus)/3]},
		{name: "shredded", data: append([]byte("%PDF-1.7\n"), corpus[len(corpus)/2:]...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, openErr := pdfview.New(tc.data, 0)
			if doc != nil {
				doc.Release()
			}
			if errors.Is(openErr, pdfview.ErrUnableToCreatePDFContext) {
				t.Fatalf("New reported ErrUnableToCreatePDFContext, which this package never returns")
			}
			if tc.want != nil && !errors.Is(openErr, tc.want) {
				t.Errorf("New error = %v, want %v", openErr, tc.want)
			}
		})
	}
}

// letterLinkPDF is a one-page US Letter document carrying a single link annotation whose /Rect is the whole page. At
// 150 dpi the page's 792 pt height scales to a 1650-row image, but 792 × (150/72) evaluated in float64 lands a hair
// above 1650, so an unbounded ceiling reports the link's bottom edge at row 1651. No xref is supplied (startxref 0) so
// the engine rebuilds it.
const letterLinkPDF = `%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Annots [4 0 R] >>
endobj
4 0 obj
<< /Type /Annot /Subtype /Link /Rect [0 0 612 792] /A << /S /URI /URI (https://example.com) >> >>
endobj
trailer
<< /Root 1 0 R /Size 5 >>
startxref
0
%%EOF
`

// TestLinkBoundsWithinRenderedImage pins the documented contract that every returned coordinate lives in the pixel
// space of the rendered image. The image extent is sized by a float32 multiply (the engine's geometry precision, which
// the MuPDF dimension goldens are pinned to) while link and hit coordinates are scaled in float64 (which the MuPDF
// coordinate goldens are pinned to); the two disagree in the last bits, so a full-page link would otherwise be
// reported one row below the image it is supposed to index into.
func TestLinkBoundsWithinRenderedImage(t *testing.T) {
	doc, err := pdfview.New([]byte(letterLinkPDF), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()
	for _, dpi := range []int{72, 100, 150, 200, 300} {
		page, renderErr := doc.RenderPage(0, dpi, 0, "")
		if renderErr != nil {
			t.Fatalf("%d dpi: %v", dpi, renderErr)
		}
		if len(page.Links) != 1 {
			t.Fatalf("%d dpi: expected 1 link, got %d", dpi, len(page.Links))
		}
		bounds := page.Links[0].Bounds
		if !bounds.In(page.Image.Rect) {
			t.Errorf("%d dpi: link bounds %v are not inside the %v image", dpi, bounds, page.Image.Rect)
		}
		// The link covers the whole page, so its bounds must be the whole image rather than merely fitting inside it.
		if bounds != page.Image.Rect {
			t.Errorf("%d dpi: link bounds %v, want the full image %v", dpi, bounds, page.Image.Rect)
		}
	}
}

// mixedSizeLinkPDF is a two-page document whose pages differ in size: page 0 is 200x200 pt and carries a link to an
// explicit /XYZ destination on page 1, which is 400x400 pt. A fit-to-size render of page 0 therefore scales page 0 and
// page 1 by different factors. No xref is supplied (startxref 0) so the engine rebuilds it.
const mixedSizeLinkPDF = `%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Annots [5 0 R] >>
endobj
4 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 400] >>
endobj
5 0 obj
<< /Type /Annot /Subtype /Link /Rect [10 170 90 190] /Dest [4 0 R /XYZ 100 300 0] >>
endobj
trailer
<< /Root 1 0 R /Size 6 >>
startxref
0
%%EOF
`

// TestLinkDestPointUsesDestinationPageScale pins that DestPoint is reported in the pixel space of the destination
// page's rendered image rather than the rendered page's. RenderPageForSize derives its scale per page from that page's
// own bounds, so a link into a differently sized page is in a different pixel space; scaling the point by the rendered
// page's factor put it somewhere the destination image does not have.
func TestLinkDestPointUsesDestinationPageScale(t *testing.T) {
	doc, err := pdfview.New([]byte(mixedSizeLinkPDF), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()

	// RenderPage scales every page alike, so the destination point is simply the page-space point times the dpi
	// factor: (100, 400-300) = (100, 100) page points at 144 dpi (scale 2) is (200, 200).
	page, err := doc.RenderPage(0, 144, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(page.Links))
	}
	if got := page.Links[0].DestPoint; got != image.Pt(200, 200) {
		t.Errorf("RenderPage DestPoint = %v, want (200, 200)", got)
	}

	// Fitting page 0 into 400x400 scales it by 2, but page 1 is 400x400 pt and so is scaled by 1. The destination
	// point must come back in page 1's space — (100, 100) — not page 0's (200, 200).
	sized, err := doc.RenderPageForSize(0, 400, 400, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sized.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(sized.Links))
	}
	link := sized.Links[0]
	if link.PageNumber != 1 {
		t.Fatalf("expected the link to target page 1, got %d", link.PageNumber)
	}
	if got := link.DestPoint; got != image.Pt(100, 100) {
		t.Errorf("RenderPageForSize DestPoint = %v, want (100, 100) in the destination page's space", got)
	}
	// The rendered page's own coordinates still use its own scale, so the link's hot zone is page 0's rect doubled.
	if got, want := link.Bounds, image.Rect(20, 20, 180, 60); got != want {
		t.Errorf("RenderPageForSize link Bounds = %v, want %v", got, want)
	}
	// The destination page rendered on its own agrees with the point reported for it.
	dest, err := doc.RenderPageForSize(1, 400, 400, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !link.DestPoint.In(dest.Image.Rect) {
		t.Errorf("DestPoint %v is not inside the destination page's %v image", link.DestPoint, dest.Image.Rect)
	}
}
