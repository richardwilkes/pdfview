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

	"github.com/richardwilkes/pdfview/internal/gfx"
)

// twoKidPages is the interior node shared by the multi-page fixtures here (and mirrored in the navigation tests).
const twoKidPages = "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>"

func TestPageContentsSingleAndArray(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: twoKidPages,
		3: "<< /Type /Page /Parent 2 0 R /Contents 5 0 R >>",
		4: "<< /Type /Page /Parent 2 0 R /Contents [5 0 R 6 0 R 9 0 R] >>",
		5: "<< /Length 7 >>\nstream\n1 0 0 m\nendstream",
		6: "<< /Length 6 >>\nstream\n10 5 l\nendstream",
	}))
	if got := string(d.PageContents(0)); got != "1 0 0 m" {
		t.Errorf("single stream: %q", got)
	}
	// The array form joins parts with a newline so tokens cannot fuse across the boundary; the dangling reference (9 0
	// R) contributes nothing.
	if got := string(d.PageContents(1)); got != "1 0 0 m\n10 5 l" {
		t.Errorf("array: %q", got)
	}
	if got := d.PageContents(99); got != nil {
		t.Errorf("bad page: %q", got)
	}
}

func TestPageResourcesInheritance(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /Resources << /ExtGState << /GS0 << /ca 0.5 >> >> >> >>", //nolint:goconst // Distinct fixture: this node carries /Resources.
		3: "<< /Type /Page /Parent 2 0 R >>",
		4: "<< /Type /Page /Parent 2 0 R /Resources << /Font << >> >> >>",
	}))
	res := d.PageResources(0)
	if res == nil || res["ExtGState"] == nil {
		t.Errorf("inherited resources: %v", res)
	}
	// The page's own /Resources replaces the inherited one entirely (ISO 32000-2 7.7.3.4).
	res = d.PageResources(1)
	if res == nil || res["ExtGState"] != nil || res["Font"] == nil {
		t.Errorf("own resources: %v", res)
	}
	if d.PageResources(7) != nil {
		t.Error("bad page returned resources")
	}
}

func TestPageCTM(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: "<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R 6 0 R] /Count 4 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [10 20 110 220] >>",
		4: "<< /Type /Page /Parent 2 0 R /MediaBox [10 20 110 220] /Rotate 90 >>",
		5: "<< /Type /Page /Parent 2 0 R /MediaBox [10 20 110 220] /Rotate 180 >>",
		6: "<< /Type /Page /Parent 2 0 R /MediaBox [10 20 110 220] /Rotate 270 >>",
	}))
	// For each rotation, the box corners must land on the display rect [0, w']×[0, h'] at the mapped spots. These
	// mappings mirror toTopLeft (pinned against MuPDF), scaled by 2.
	type probe struct {
		page  int
		x, y  float32 // PDF-space point
		wantU float32
		wantV float32
	}
	for _, tc := range []probe{
		{page: 0, x: 10, y: 220, wantU: 0, wantV: 0},     // top-left of the box
		{page: 0, x: 110, y: 20, wantU: 200, wantV: 400}, // bottom-right
		{page: 1, x: 10, y: 20, wantU: 0, wantV: 0},      // rotate 90
		{page: 1, x: 110, y: 220, wantU: 400, wantV: 200},
		{page: 2, x: 110, y: 20, wantU: 0, wantV: 0}, // rotate 180
		{page: 2, x: 10, y: 220, wantU: 200, wantV: 400},
		{page: 3, x: 110, y: 220, wantU: 0, wantV: 0}, // rotate 270
		{page: 3, x: 10, y: 20, wantU: 400, wantV: 200},
	} {
		ctm, err := d.PageCTM(tc.page, 2)
		if err != nil {
			t.Fatal(err)
		}
		u, v := ctm.ApplyXY(tc.x, tc.y)
		if u != tc.wantU || v != tc.wantV {
			t.Errorf("page %d: (%g, %g) -> (%g, %g), want (%g, %g)", tc.page, tc.x, tc.y, u, v, tc.wantU, tc.wantV)
		}
	}
	if _, err := d.PageCTM(9, 1); err == nil {
		t.Error("bad page produced a CTM")
	}
	ctm, err := d.PageCTM(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ctm == (gfx.Matrix{}) {
		t.Error("zero matrix returned for a valid page")
	}
}
