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

// TestZeroValueDocument pins that the zero Document, a nil *Document, and a released document answer every method with
// the value documented for a released document. Document embeds an unexported pointer that holds the mutex, so the
// first two would otherwise dereference nil on each method's first statement, contradicting the promise that panics
// never escape the public API.
func TestZeroValueDocument(t *testing.T) {
	surf := surface.NewRasterN32Premul(8, 8, nil)
	if surf == nil {
		t.Fatal("unable to create surface")
	}
	// Named openErr so the err the subtests declare does not shadow it.
	releasedDoc, openErr := pdfview.New([]byte(letterLinkPDF), 0)
	if openErr != nil {
		t.Fatal(openErr)
	}
	releasedDoc.Release()
	for _, tc := range []struct {
		doc  *pdfview.Document
		name string
	}{
		{name: "zero value", doc: &pdfview.Document{}},
		{name: "nil pointer", doc: nil},
		{name: "released", doc: releasedDoc},
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
			if got := doc.PageLabel(0); got != "" {
				t.Errorf("PageLabel = %q, want an empty string", got)
			}
			if got := doc.PageLabels(); got != nil {
				t.Errorf("PageLabels = %v, want nil", got)
			}
			if got := doc.PagesWithLabel("1"); got != nil {
				t.Errorf("PagesWithLabel = %v, want nil", got)
			}
			if got := doc.HasPageLabels(); got {
				t.Error("HasPageLabels = true, want false")
			}
			if _, _, err := doc.PageSize(0); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("PageSize error = %v, want ErrDocumentReleased", err)
			}
			if _, _, err := doc.PageRenderSize(0, 72); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("PageRenderSize error = %v, want ErrDocumentReleased", err)
			}
			if _, err := doc.RenderPage(0, 72, 0, ""); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("RenderPage error = %v, want ErrDocumentReleased", err)
			}
			if _, err := doc.RenderPageForSize(0, 800, 800, 0, ""); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("RenderPageForSize error = %v, want ErrDocumentReleased", err)
			}
			if _, err := doc.TextPage(0, 72); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("TextPage error = %v, want ErrDocumentReleased", err)
			}
			if err := doc.DrawPage(surf.Canvas(), 0, geom.IdentityMatrix()); !errors.Is(err, pdfview.ErrDocumentReleased) {
				t.Errorf("DrawPage error = %v, want ErrDocumentReleased", err)
			}
			doc.Release() // Idempotent and safe with nothing behind it.
			doc.Release()
		})
	}
}

// TestZeroValueTextPage pins the same contract for TextPage: its zero value and a nil *TextPage answer every method as
// a page with no text does. Both are reachable: TextPage returns nil alongside an error, and an embedding struct starts
// with the zero value.
func TestZeroValueTextPage(t *testing.T) {
	for _, tc := range []struct {
		page *pdfview.TextPage
		name string
	}{
		{name: "zero value", page: &pdfview.TextPage{}},
		{name: "nil pointer", page: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := tc.page
			if got := page.Len(); got != 0 {
				t.Errorf("Len = %d, want 0", got)
			}
			if got := page.IndexAt(image.Pt(10, 10)); got != 0 {
				t.Errorf("IndexAt = %d, want 0", got)
			}
			if start, end := page.WordAt(3); start != 0 || end != 0 {
				t.Errorf("WordAt = (%d, %d), want (0, 0)", start, end)
			}
			if start, end := page.LineAt(3); start != 0 || end != 0 {
				t.Errorf("LineAt = (%d, %d), want (0, 0)", start, end)
			}
			if got := page.Text(0, 10); got != "" {
				t.Errorf("Text = %q, want an empty string", got)
			}
			if got := page.Highlights(0, 10); got != nil {
				t.Errorf("Highlights = %v, want nil", got)
			}
			if got := page.Search("needle", 10); got != nil {
				t.Errorf("Search = %v, want nil", got)
			}
			// A page with no extent fits no box, so ForSize reports ErrInvalidPageSize; AtDPI re-labels the empty page.
			if _, err := page.ForSize(800, 600); !errors.Is(err, pdfview.ErrInvalidPageSize) {
				t.Errorf("ForSize error = %v, want ErrInvalidPageSize", err)
			}
			scaled := page.AtDPI(150)
			if got := scaled.Len(); got != 0 {
				t.Errorf("AtDPI(150).Len = %d, want 0", got)
			}
			if got := scaled.Text(0, 10); got != "" {
				t.Errorf("AtDPI(150).Text = %q, want an empty string", got)
			}
			if got := scaled.Highlights(0, 10); got != nil {
				t.Errorf("AtDPI(150).Highlights = %v, want nil", got)
			}
			if got := scaled.Search("needle", 10); got != nil {
				t.Errorf("AtDPI(150).Search = %v, want nil", got)
			}
			if got := scaled.IndexAt(image.Pt(10, 10)); got != 0 {
				t.Errorf("AtDPI(150).IndexAt = %d, want 0", got)
			}
		})
	}
}

// TestOpenFailuresNeverReportPDFContext pins that New never returns ErrUnableToCreatePDFContext, which exists only for
// source compatibility with the MuPDF-based binding this package succeeds.
func TestOpenFailuresNeverReportPDFContext(t *testing.T) {
	corpus, err := os.ReadFile(filepath.Join("testfiles", "corpus", "vectors.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		// want is the sentinel New must report, or nil when the outcome is not pinned: the repair scan rescues some
		// damaged input.
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

// letterLinkPDF is a one-page US Letter document with a single link annotation whose /Rect is the whole page. At 150
// dpi the 792 pt height scales to a 1650-row image, but 792 × (150/72) in float64 lands a hair above 1650, so an
// unbounded ceiling reports the link's bottom edge at row 1651. startxref 0 makes the engine rebuild the xref.
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

// TestLinkBoundsWithinRenderedImage pins that every link coordinate lives in the rendered image's pixel space. The
// image extent is a float32 multiply (pinned by the MuPDF dimension goldens) while link coordinates scale in float64
// (pinned by the MuPDF coordinate goldens); the two disagree in the last bits, so a full-page link would otherwise be
// reported one row below the image.
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
		// The link covers the whole page, so its bounds must be the whole image.
		if bounds != page.Image.Rect {
			t.Errorf("%d dpi: link bounds %v, want the full image %v", dpi, bounds, page.Image.Rect)
		}
	}
}

// mixedSizeLinkPDF is a two-page document: page 0 is 200x200 pt and links to an explicit /XYZ destination on page 1,
// which is 400x400 pt, so a fit-to-size render of page 0 scales the two pages by different factors. startxref 0 makes
// the engine rebuild the xref.
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

// TestLinkDestPointUsesDestinationPageScale pins that DestPoint is in the pixel space of the destination page's
// rendered image, which RenderPageForSize scales from that page's own bounds, not the rendered page's.
func TestLinkDestPointUsesDestinationPageScale(t *testing.T) {
	doc, err := pdfview.New([]byte(mixedSizeLinkPDF), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()

	// RenderPage scales every page alike: (100, 400-300) page points at 144 dpi (scale 2) is (200, 200).
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

	// Fitting page 0 into 400x400 scales it by 2 but page 1 (400x400 pt) by 1, so the point is (100, 100), not
	// (200, 200).
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
	// The link's own bounds still use page 0's scale.
	if got, want := link.Bounds, image.Rect(20, 20, 180, 60); got != want {
		t.Errorf("RenderPageForSize link Bounds = %v, want %v", got, want)
	}
	// The destination page's own render contains the point.
	dest, err := doc.RenderPageForSize(1, 400, 400, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !link.DestPoint.In(dest.Image.Rect) {
		t.Errorf("DestPoint %v is not inside the destination page's %v image", link.DestPoint, dest.Image.Rect)
	}
}
