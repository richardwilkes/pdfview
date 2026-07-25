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
	"fmt"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview"
)

// overRangeBBox spans -1e38..3e38: each corner is a legal float32, but the extent the origin-plus-width spelling of a
// rectangle computes is 4e38, which overflows to +Inf. A /BBox that large clips nothing, so the content inside it must
// paint in full.
var overRangeBBox = fmt.Sprintf("[%[1]s %[1]s %[2]s %[2]s]",
	"-1"+strings.Repeat("0", 38), "3"+strings.Repeat("0", 38))

// bboxPDF builds a 200x200 page whose content invokes a form XObject with the given /BBox. The form paints a 100x100
// red square at the page origin — 10 000 pixels at 72 dpi, where a page point is a pixel.
func bboxPDF(bbox string) string {
	const formBody = "1 0 0 rg 0 0 100 100 re f"
	const pageBody = "/Fm0 Do"
	return fmt.Sprintf(`%%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R
   /Resources << /XObject << /Fm0 5 0 R >> >> >>
endobj
4 0 obj
<< /Length %d >>
stream
%s
endstream
endobj
5 0 obj
<< /Type /XObject /Subtype /Form /BBox %s /Length %d >>
stream
%s
endstream
endobj
trailer
<< /Root 1 0 R /Size 6 >>
startxref
0
%%%%EOF
`, len(pageBody), pageBody, bbox, len(formBody), formBody)
}

// paintedPixels renders the page at 72 dpi (one pixel per point) and counts the pixels with any coverage.
func paintedPixels(t *testing.T, pdf string) int {
	t.Helper()
	doc, err := pdfview.New([]byte(pdf), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()
	page, err := doc.RenderPage(0, 72, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for i := 3; i < len(page.Image.Pix); i += 4 {
		if page.Image.Pix[i] != 0 {
			count++
		}
	}
	return count
}

// TestFormOverRangeBBoxStillPaints is the end-to-end form of the /BBox extent overflow: every entry of the box is
// validated individually, but the clip built from it used to be spelled origin-plus-extent, so a box wider than
// float32's range produced ±Inf corners and clipped the form's content away entirely. A box that large clips nothing,
// so both renders must paint the same 100x100 square.
func TestFormOverRangeBBoxStillPaints(t *testing.T) {
	const want = 100 * 100
	if got := paintedPixels(t, bboxPDF("[0 0 200 200]")); got != want {
		t.Fatalf("the reference render painted %d pixels, want %d", got, want)
	}
	if got := paintedPixels(t, bboxPDF(overRangeBBox)); got != want {
		t.Errorf("the form under an over-range /BBox painted %d pixels, want %d: the clip degenerated", got, want)
	}
}

// maskPDF builds a 200x200 page that fills a 100x100 red square under a luminosity soft mask whose group paints white
// over the left half of the given /BBox. Only the covered half of the fill survives the mask.
func maskPDF(bbox string) string {
	const maskBody = "1 g 0 0 50 200 re f"
	const pageBody = "/GS0 gs 1 0 0 rg 0 0 100 100 re f"
	return fmt.Sprintf(`%%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R
   /Resources << /ExtGState << /GS0 6 0 R >> >> >>
endobj
4 0 obj
<< /Length %d >>
stream
%s
endstream
endobj
5 0 obj
<< /Type /XObject /Subtype /Form /BBox %s /Group << /S /Transparency /CS /DeviceGray >> /Length %d >>
stream
%s
endstream
endobj
6 0 obj
<< /Type /ExtGState /SMask << /S /Luminosity /G 5 0 R >> >>
endobj
trailer
<< /Root 1 0 R /Size 7 >>
startxref
0
%%%%EOF
`, len(pageBody), pageBody, bbox, len(maskBody), maskBody)
}

// TestSoftMaskOverRangeBBoxKeepsItsClip is the end-to-end form of the mask-space /BBox extent overflow. Its failure is
// the opposite of the form clip's and worse: the ±Inf corners lost the clip rather than emptying it, so mask content
// confined to the box lit the whole page instead — the masked-away half of the fill reappeared.
func TestSoftMaskOverRangeBBoxKeepsItsClip(t *testing.T) {
	const want = 50 * 100 // The mask lights x in [0, 50) of the 100x100 fill.
	if got := paintedPixels(t, maskPDF("[0 0 200 200]")); got != want {
		t.Fatalf("the reference render painted %d pixels, want %d", got, want)
	}
	if got := paintedPixels(t, maskPDF(overRangeBBox)); got != want {
		t.Errorf("under an over-range mask /BBox %d pixels painted, want %d: the mask's own clip was lost", got, want)
	}
}
