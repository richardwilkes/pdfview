// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package doc_test

import (
	"testing"

	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// apStream is a minimal form-XObject appearance stream body (object 5 in these fixtures).
const apStream = "<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Length 20 >>\nstream\n1 0 0 rg 0 0 10 10 re\nendstream"

// annotDoc builds a one-page document whose /Annots array holds exactly the given annotation body (object 4);
// object 5 is a 10x10 appearance stream and further objects may be supplied for /Parent chains and state dicts.
func annotDoc(t *testing.T, annot string, extra map[int]string) *doc.Document {
	t.Helper()
	objects := map[int]string{
		1: catalogObj,
		2: pagesOneKid,
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Annots [4 0 R] >>",
		4: annot,
		5: apStream,
	}
	for num, body := range extra {
		objects[num] = body
	}
	return mustOpen(t, pdf(objects))
}

// TestAnnotationSelection pins the probe-pinned gates: which annotations yield a renderable appearance at all.
// See the M8 /AP decision-log entry in plan.md for the oracle probes behind each case.
func TestAnnotationSelection(t *testing.T) {
	cases := []struct {
		extra map[int]string
		name  string
		annot string
		want  int
	}{
		{nil, "square with N stream", "<< /Type /Annot /Subtype /Square /F 4 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 1},
		{nil, "no flags draws", "<< /Type /Annot /Subtype /Square /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 1},
		{nil, "invisible flag hides", "<< /Type /Annot /Subtype /Square /F 1 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 0},
		{nil, "hidden flag hides", "<< /Type /Annot /Subtype /Square /F 2 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 0},
		{nil, "noview flag hides", "<< /Type /Annot /Subtype /Square /F 32 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 0},
		{nil, "print flag draws", "<< /Type /Annot /Subtype /Square /F 4 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 1},
		{nil, "link never draws", "<< /Type /Annot /Subtype /Link /F 4 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 0},
		{nil, "popup never draws", "<< /Type /Annot /Subtype /Popup /F 4 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 0},
		{nil, "unknown subtype draws", "<< /Type /Annot /Subtype /Whatever /F 4 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 1},
		{nil, "widget without FT hides", "<< /Type /Annot /Subtype /Widget /F 4 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 0},
		{nil, "widget with FT draws", "<< /Type /Annot /Subtype /Widget /FT /Btn /F 4 /Rect [10 10 60 40] /AP << /N 5 0 R >> >>", 1},
		{
			map[int]string{6: "<< /FT /Tx /T (f) >>"},
			"widget FT via parent", "<< /Type /Annot /Subtype /Widget /F 4 /Rect [10 10 60 40] /Parent 6 0 R /AP << /N 5 0 R >> >>",
			1,
		},
		{
			map[int]string{6: "<< /T (a) /Parent 7 0 R >>", 7: "<< /T (b) /Parent 6 0 R >>"},
			"widget parent cycle terminates", "<< /Type /Annot /Subtype /Widget /F 4 /Rect [10 10 60 40] /Parent 6 0 R /AP << /N 5 0 R >> >>",
			0,
		},
		{nil, "dict N with AS", "<< /Type /Annot /Subtype /Widget /FT /Btn /F 4 /Rect [10 10 60 40] /AP << /N << /On 5 0 R >> >> /AS /On >>", 1},
		{nil, "dict N missing AS state", "<< /Type /Annot /Subtype /Widget /FT /Btn /F 4 /Rect [10 10 60 40] /AP << /N << /On 5 0 R >> >> /AS /Off >>", 0},
		{nil, "dict N without AS", "<< /Type /Annot /Subtype /Widget /FT /Btn /F 4 /Rect [10 10 60 40] /AP << /N << /On 5 0 R >> >> >>", 0},
		{nil, "D only never draws", "<< /Type /Annot /Subtype /Widget /FT /Btn /F 4 /Rect [10 10 60 40] /AP << /D 5 0 R >> >>", 0},
		{nil, "stream N ignores stray AS", "<< /Type /Annot /Subtype /Widget /FT /Btn /F 4 /Rect [10 10 60 40] /AP << /N 5 0 R >> /AS /Off >>", 1},
		{nil, "no AP draws nothing", "<< /Type /Annot /Subtype /Square /F 4 /Rect [10 10 60 40] /C [1 0 0] >>", 0},
		{nil, "degenerate rect skips", "<< /Type /Annot /Subtype /Square /F 4 /Rect [10 10 10 40] /AP << /N 5 0 R >> >>", 0},
		{nil, "missing rect skips", "<< /Type /Annot /Subtype /Square /F 4 /AP << /N 5 0 R >> >>", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := annotDoc(t, tc.annot, tc.extra)
			if got := len(d.Annotations(0)); got != tc.want {
				t.Errorf("Annotations(0) returned %d appearances, want %d", got, tc.want)
			}
		})
	}
}

// TestAnnotationTransform pins the ISO 32000-2 12.5.5 placement math: /BBox through /Matrix, the resulting
// axis-aligned box carried onto the normalized /Rect.
func TestAnnotationTransform(t *testing.T) {
	// Identity matrix: BBox [0 0 10 10] onto Rect [100 200 200 240] scales x by 10, y by 4.
	d := annotDoc(t, "<< /Type /Annot /Subtype /Square /F 4 /Rect [100 200 200 240] /AP << /N 5 0 R >> >>", nil)
	annots := d.Annotations(0)
	if len(annots) != 1 {
		t.Fatalf("got %d annots, want 1", len(annots))
	}
	checkPoint := func(m gfx.Matrix, x, y, wantX, wantY float32) {
		t.Helper()
		gx, gy := m.ApplyXY(x, y)
		if gx != wantX || gy != wantY {
			t.Errorf("transform(%v,%v) = (%v,%v), want (%v,%v)", x, y, gx, gy, wantX, wantY)
		}
	}
	checkPoint(annots[0].Transform, 0, 0, 100, 200)
	checkPoint(annots[0].Transform, 10, 10, 200, 240)

	// Rotation: BBox [0 0 10 20] through Matrix [0 1 -1 0 0 0] spans [-20 0 0 10]; onto Rect [200 700 300 740]
	// the composite (Matrix then Transform) sends form (0,0) to (300,700) and form (10,20) to (200,740).
	rotAP := "<< /Type /XObject /Subtype /Form /BBox [0 0 10 20] /Matrix [0 1 -1 0 0 0] /Length 20 >>\nstream\n1 0 0 rg 0 0 10 20 re\nendstream"
	d = annotDoc(t, "<< /Type /Annot /Subtype /Square /F 4 /Rect [200 700 300 740] /AP << /N 5 0 R >> >>",
		map[int]string{5: rotAP})
	annots = d.Annotations(0)
	if len(annots) != 1 {
		t.Fatalf("got %d annots, want 1", len(annots))
	}
	tr := annots[0].Transform
	checkPoint(tr, 0, 0, 300, 700)    // Matrix(0,0) = (0,0)
	checkPoint(tr, -20, 10, 200, 740) // Matrix(10,20) = (-20,10)

	// A reversed /Rect normalizes before the mapping.
	d = annotDoc(t, "<< /Type /Annot /Subtype /Square /F 4 /Rect [200 240 100 200] /AP << /N 5 0 R >> >>", nil)
	annots = d.Annotations(0)
	if len(annots) != 1 {
		t.Fatalf("got %d annots, want 1", len(annots))
	}
	checkPoint(annots[0].Transform, 0, 0, 100, 200)

	// Degenerate /BBox yields nothing.
	degAP := "<< /Type /XObject /Subtype /Form /BBox [0 0 0 0] /Length 20 >>\nstream\n1 0 0 rg 0 0 10 10 re\nendstream"
	d = annotDoc(t, "<< /Type /Annot /Subtype /Square /F 4 /Rect [100 200 200 240] /AP << /N 5 0 R >> >>",
		map[int]string{5: degAP})
	if got := len(d.Annotations(0)); got != 0 {
		t.Errorf("degenerate BBox: got %d annots, want 0", got)
	}
}
