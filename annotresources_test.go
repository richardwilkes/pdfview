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
	"image/color"
	"testing"

	"github.com/richardwilkes/pdfview"
)

// inheritedAnnotResourcesPDF is a 60x20 page with three annotations whose shared appearance stream has no /Resources of
// its own, so each reaches the red-square form through the page's indirect /Resources dictionary.
const inheritedAnnotResourcesPDF = `%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 60 20] /Resources 8 0 R /Annots [4 0 R 5 0 R 6 0 R] >>
endobj
4 0 obj
<< /Type /Annot /Subtype /Square /Rect [0 0 20 20] /F 4 /AP << /N 7 0 R >> >>
endobj
5 0 obj
<< /Type /Annot /Subtype /Square /Rect [20 0 40 20] /F 4 /AP << /N 7 0 R >> >>
endobj
6 0 obj
<< /Type /Annot /Subtype /Square /Rect [40 0 60 20] /F 4 /AP << /N 7 0 R >> >>
endobj
7 0 obj
<< /Type /XObject /Subtype /Form /BBox [0 0 20 20] /Length 7 >>
stream
/Fm0 Do
endstream
endobj
8 0 obj
<< /XObject << /Fm0 9 0 R >> >>
endobj
9 0 obj
<< /Type /XObject /Subtype /Form /BBox [0 0 20 20] /Length 24 >>
stream
1 0 0 rg 0 0 20 20 re f
endstream
endobj
trailer
<< /Root 1 0 R /Size 10 >>
startxref
0
%%EOF
`

// TestAnnotationsShareThePageResources pins that runAnnots resolves the page's /Resources once per pass and every
// annotation still paints from it.
func TestAnnotationsShareThePageResources(t *testing.T) {
	doc, err := pdfview.New([]byte(inheritedAnnotResourcesPDF), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()
	page, err := doc.RenderPage(0, 72, 0, "") // 72 dpi: one pixel per point.
	if err != nil {
		t.Fatal(err)
	}
	red := color.NRGBA{R: 255, A: 255}
	for i, x := range []int{10, 30, 50} {
		if got := page.Image.NRGBAAt(x, 10); got != red {
			t.Errorf("annotation %d (center x=%d) painted %v, want %v: its appearance stream did not reach the "+
				"page's resources", i, x, got, red)
		}
	}
}
