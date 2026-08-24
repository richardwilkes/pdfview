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
)

// The companion to bbox_overflow_test.go: those cases cover overflow while a rectangle is built (origin-plus-extent),
// these overflow while a finite path is transformed. Path coordinates are validated where they are produced and every
// consumer then maps them into device space, so a finite region under a large-but-finite matrix can reach the
// rasterizer with ±Inf corners. Both regressions cliff exactly at float32's maximum of 3.4028e38: the same content one
// scale step lower renders correctly.

// zeros38 is 38 zeros: PDF has no exponent notation, so every coordinate near float32's range is spelled out.
var zeros38 = strings.Repeat("0", 38)

// hugeCoord is a legal float32 that crosses float32's maximum under a few doublings: 1e38 × 4 is +Inf.
var hugeCoord = "1" + zeros38

// clipPDF builds a 200x200 page that clips to a rectangle spanning -1e38..1e38 (in user space, before the scale) and
// then fills a 10x10 red square, both under a uniform scale of s. The clip is far larger than the page at every scale,
// so it selects everything and the fill must paint its full (10s)² pixels.
func clipPDF(s float64) string {
	body := fmt.Sprintf("%[1]v 0 0 %[1]v 0 0 cm -%[2]s -%[2]s %[3]s %[3]s re W n 1 0 0 rg 0 0 10 10 re f",
		s, hugeCoord, "2"+zeros38)
	return onePagePDF(body)
}

// onePagePDF wraps a content stream in a 200x200 single-page document with no resources.
func onePagePDF(body string) string {
	return fmt.Sprintf(`%%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Resources << >> >>
endobj
4 0 obj
<< /Length %d >>
stream
%s
endstream
endobj
trailer
<< /Root 1 0 R /Size 5 >>
startxref
0
%%%%EOF
`, len(body), body)
}

// TestClipPathSurvivesOverRangeTransform pins that a clip finite in user space survives a transform that overflows its
// corners (past a scale of 3.4 here). ClipPath validates the source coordinates and then transforms them; an unusable
// clip intersects to nothing and everything drawn under it vanishes.
func TestClipPathSurvivesOverRangeTransform(t *testing.T) {
	for _, s := range []float64{1, 2, 3, 3.4, 4, 10} {
		want := int(10*s) * int(10*s)
		if got := paintedPixels(t, clipPDF(s)); got != want {
			t.Errorf("at scale %v the fill under an over-range clip painted %d pixels, want %d: the clip degenerated",
				s, got, want)
		}
	}
}

// shadingPDF builds a 200x200 page that paints an axial shading covering the whole page under a uniform scale of s,
// with the given /BBox (a raw PDF array, or empty for no box at all). The shading extends in both directions, so it
// covers all 40 000 pixels whenever its box does not cut it back.
func shadingPDF(bbox string, s float64) string {
	body := fmt.Sprintf("q %[1]v 0 0 %[1]v 0 0 cm /Sh0 sh Q", s)
	if bbox != "" {
		bbox = "/BBox " + bbox + " "
	}
	return fmt.Sprintf(`%%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R
   /Resources << /Shading << /Sh0 5 0 R >> >> >>
endobj
4 0 obj
<< /Length %d >>
stream
%s
endstream
endobj
5 0 obj
<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 200 200] /Extend [true true] %s/Function 6 0 R >>
endobj
6 0 obj
<< /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >>
endobj
trailer
<< /Root 1 0 R /Size 7 >>
startxref
0
%%%%EOF
`, len(body), body, bbox)
}

// TestShadingOverRangeBBoxStillPaints pins that a shading /BBox that maps past float32's range clips nothing. The box's
// entries are validated individually, but the box is clipped in the shading's target space and the mapping into it
// produced ±Inf corners, so the shading painted nothing past a scale of 1.13.
func TestShadingOverRangeBBoxStillPaints(t *testing.T) {
	const want = 200 * 200
	overRange := fmt.Sprintf("[-%[1]s -%[1]s %[1]s %[1]s]", "3"+zeros38)
	for _, s := range []float64{1, 1.13, 1.14, 2} {
		if got := paintedPixels(t, shadingPDF("", s)); got != want {
			t.Fatalf("at scale %v the reference shading (no /BBox) painted %d pixels, want %d", s, got, want)
		}
		if got := paintedPixels(t, shadingPDF(overRange, s)); got != want {
			t.Errorf("at scale %v the shading under an over-range /BBox painted %d pixels, want %d: the clip"+
				" degenerated", s, got, want)
		}
	}
	// A box that genuinely cuts the shading back must still do so, so the guard cannot be a blanket skip.
	if got := paintedPixels(t, shadingPDF("[0 0 100 200]", 1)); got != 100*200 {
		t.Errorf("an ordinary shading /BBox clipped to %d pixels, want %d", got, 100*200)
	}
}

// TestOverflowingRectDropsOnlyItself pins the re operator's corner check: buildPath drops a path whole when any point
// is non-finite, so a rectangle whose x+w corner overflows must be dropped on its own rather than take every subpath
// in the same construction with it.
func TestOverflowingRectDropsOnlyItself(t *testing.T) {
	const want = 100 * 100
	huge := "3" + zeros38 // x + w = 6e38, past float32's maximum.
	if got := paintedPixels(t, onePagePDF("1 0 0 rg 0 0 100 100 re f")); got != want {
		t.Fatalf("the reference render painted %d pixels, want %d", got, want)
	}
	body := fmt.Sprintf("1 0 0 rg 0 0 100 100 re %[1]s %[1]s %[1]s %[1]s re f", huge)
	if got := paintedPixels(t, onePagePDF(body)); got != want {
		t.Errorf("a path holding one overflowing re painted %d pixels, want %d: the whole path was discarded", got,
			want)
	}
	// Order must not matter: the overflowing rectangle is dropped whether it precedes or follows the good one.
	body = fmt.Sprintf("1 0 0 rg %[1]s %[1]s %[1]s %[1]s re 0 0 100 100 re f", huge)
	if got := paintedPixels(t, onePagePDF(body)); got != want {
		t.Errorf("a path opening with an overflowing re painted %d pixels, want %d", got, want)
	}
}
