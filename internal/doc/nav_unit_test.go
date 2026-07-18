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
	"math"
	"strconv"
	"testing"

	"github.com/richardwilkes/pdfview/internal/doc"
)

// The expected values in this file were captured by running the MuPDF oracle over probe documents with exactly
// these structures (offset box origins, each rotation, each destination kind, crop intersections). They pin
// behavior the committed corpus does not cover.

const (
	pagesOneKid = "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"
	pageBox200  = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>"
)

// navDoc builds a single-page document with the given page attributes and annotations.
func navDoc(t *testing.T, pageExtra, annots string) *doc.Document {
	t.Helper()
	objects := map[int]string{
		1: catalogObj,
		2: pagesOneKid,
		3: "<< /Type /Page /Parent 2 0 R " + pageExtra + " >>",
	}
	if annots != "" {
		objects[3] = "<< /Type /Page /Parent 2 0 R " + pageExtra + " /Annots [4 0 R] >>"
		objects[4] = annots
	}
	return mustOpen(t, pdf(objects))
}

func checkSize(t *testing.T, d *doc.Document, pageNumber int, wantW, wantH float32) {
	t.Helper()
	w, h, err := d.PageSize(pageNumber)
	if err != nil {
		t.Fatalf("PageSize(%d): %v", pageNumber, err)
	}
	if w != wantW || h != wantH {
		t.Errorf("PageSize(%d) = (%v, %v), want (%v, %v)", pageNumber, w, h, wantW, wantH)
	}
}

func checkLink(t *testing.T, got doc.Link, wantRect [4]float32, wantPage int, wantX, wantY float32) {
	t.Helper()
	if got.X0 != wantRect[0] || got.Y0 != wantRect[1] || got.X1 != wantRect[2] || got.Y1 != wantRect[3] {
		t.Errorf("link rect = [%v %v %v %v], want %v", got.X0, got.Y0, got.X1, got.Y1, wantRect)
	}
	if got.Page != wantPage {
		t.Errorf("link Page = %d, want %d", got.Page, wantPage)
	}
	checkCoord(t, "DestX", got.DestX, wantX)
	checkCoord(t, "DestY", got.DestY, wantY)
}

// checkCoord compares a coordinate, treating NaN as a value (expected NaN must be NaN).
func checkCoord(t *testing.T, label string, got, want float32) {
	t.Helper()
	if math.IsNaN(float64(want)) {
		if !math.IsNaN(float64(got)) {
			t.Errorf("%s = %v, want NaN", label, got)
		}
		return
	}
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func nan() float32 {
	return float32(math.NaN())
}

// TestRotationMapping pins the top-left mapping for every rotation with an offset box origin, matching the
// oracle probes: MediaBox [10 20 310 220], /Rect [30 50 80 70], /Dest [3 0 R /XYZ 40 60 0].
func TestRotationMapping(t *testing.T) {
	for _, tc := range []struct {
		rect     [4]float32
		w, h     float32
		destX    float32
		destY    float32
		rotation int
	}{
		{rotation: 0, w: 300, h: 200, rect: [4]float32{20, 150, 70, 170}, destX: 30, destY: 160},
		{rotation: 90, w: 200, h: 300, rect: [4]float32{30, 20, 50, 70}, destX: 40, destY: 30},
		{rotation: 180, w: 300, h: 200, rect: [4]float32{230, 30, 280, 50}, destX: 270, destY: 40},
		{rotation: 270, w: 200, h: 300, rect: [4]float32{150, 230, 170, 280}, destX: 160, destY: 270},
	} {
		d := navDoc(t, "/MediaBox [10 20 310 220] /Rotate "+strconv.Itoa(tc.rotation),
			"<< /Type /Annot /Subtype /Link /Rect [30 50 80 70] /Dest [3 0 R /XYZ 40 60 0] >>")
		checkSize(t, d, 0, tc.w, tc.h)
		links := d.Links(0)
		if len(links) != 1 {
			t.Fatalf("rotate %d: got %d links, want 1", tc.rotation, len(links))
		}
		checkLink(t, links[0], tc.rect, 0, tc.destX, tc.destY)
	}
}

// TestRotateNormalization pins the rounding of out-of-spec /Rotate values to the nearest multiple of 90
// (normalized into [0,360) first), observed by probing MuPDF: 45, 100, 450, -90, and -450 all swap the axes.
func TestRotateNormalization(t *testing.T) {
	for _, tc := range []struct {
		rotate  string
		swapped bool
	}{
		{rotate: "45", swapped: true},   // Tie rounds up to 90.
		{rotate: "100", swapped: true},  // Rounds to 90.
		{rotate: "450", swapped: true},  // Normalizes to 90.
		{rotate: "-90", swapped: true},  // Normalizes to 270.
		{rotate: "-450", swapped: true}, // Normalizes to 270.
		{rotate: "-100", swapped: true}, // Normalizes to 260, rounds to 270.
		{rotate: "-315", swapped: true}, // Normalizes to 45, tie rounds up to 90.
		{rotate: "140", swapped: false}, // Rounds to 180.
		{rotate: "44", swapped: false},  // Rounds to 0.
		{rotate: "315", swapped: false}, // Tie rounds up to 360 ≡ 0.
		{rotate: "-45", swapped: false}, // Normalizes to 315, tie rounds up to 360 ≡ 0.
	} {
		d := navDoc(t, "/MediaBox [0 0 300 200] /Rotate "+tc.rotate, "")
		w, h, err := d.PageSize(0)
		if err != nil {
			t.Fatalf("PageSize: %v", err)
		}
		if got := w == 200 && h == 300; got != tc.swapped {
			t.Errorf("/Rotate %s: size (%v, %v), swapped = %v, want %v", tc.rotate, w, h, got, tc.swapped)
		}
	}
}

// TestCropBoxIntersection pins CropBox ∩ MediaBox (oracle-probed): a crop inside the media box wins; a crop
// larger than the media box is clipped to it; a crop that does not intersect it falls back to the media box.
func TestCropBoxIntersection(t *testing.T) {
	d := navDoc(t, "/MediaBox [0 0 300 200] /CropBox [50 50 250 180]",
		"<< /Type /Annot /Subtype /Link /Rect [60 60 100 80] /Dest [3 0 R /XYZ 70 90 0] >>")
	checkSize(t, d, 0, 200, 130)
	links := d.Links(0)
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	checkLink(t, links[0], [4]float32{10, 100, 50, 120}, 0, 20, 90)

	d = navDoc(t, "/MediaBox [0 0 300 200] /CropBox [-50 -50 400 300]", "")
	checkSize(t, d, 0, 300, 200)

	d = navDoc(t, "/MediaBox [0 0 300 200] /CropBox [400 400 500 500]", "")
	checkSize(t, d, 0, 300, 200)
}

// TestDefaultMediaBox: a page without any usable /MediaBox falls back to US Letter.
func TestDefaultMediaBox(t *testing.T) {
	d := navDoc(t, "", "")
	checkSize(t, d, 0, 612, 792)
	d = navDoc(t, "/MediaBox [0 0 0 0]", "")
	checkSize(t, d, 0, 612, 792)
}

// TestInheritedPageAttributes: /MediaBox and /Rotate inherit from ancestor /Pages nodes, and a page's own
// entries override them (ISO 32000-2 7.7.3.4).
func TestInheritedPageAttributes(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200] /Rotate 90 >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
		4: "<< /Type /Page /Parent 2 0 R /Rotate 0 /MediaBox [0 0 100 50] >>",
	}))
	checkSize(t, d, 0, 200, 300) // Inherited box and rotation.
	checkSize(t, d, 1, 100, 50)  // Own values override.
}

// TestDestinationKinds pins the coordinate semantics of each destination kind (oracle-probed): the mapped
// point, with NaN for every slot the kind does not define or that is null.
func TestDestinationKinds(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: pagesOneKid,
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Annots [4 0 R 5 0 R 6 0 R 7 0 R 8 0 R 9 0 R] >>",
		4: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest [3 0 R /FitH 120] >>",
		5: "<< /Type /Annot /Subtype /Link /Rect [20 0 30 10] /Dest [3 0 R /FitV 60] >>",
		6: "<< /Type /Annot /Subtype /Link /Rect [40 0 50 10] /Dest [3 0 R /FitR 40 50 100 90] >>",
		7: "<< /Type /Annot /Subtype /Link /Rect [60 0 70 10] /Dest [3 0 R /XYZ null null null] >>",
		8: "<< /Type /Annot /Subtype /Link /Rect [80 0 90 10] /Dest [0 /XYZ 10 20 0] >>",
		9: "<< /Type /Annot /Subtype /Link /Rect [90 0 95 10] /Dest [3 0 R /Fit] >>",
	}))
	links := d.Links(0)
	if len(links) != 6 {
		t.Fatalf("got %d links, want 6", len(links))
	}
	checkLink(t, links[0], [4]float32{0, 190, 10, 200}, 0, nan(), 80)     // FitH: y = top, mapped.
	checkLink(t, links[1], [4]float32{20, 190, 30, 200}, 0, 60, nan())    // FitV: x = left.
	checkLink(t, links[2], [4]float32{40, 190, 50, 200}, 0, 40, 110)      // FitR: (left, top).
	checkLink(t, links[3], [4]float32{60, 190, 70, 200}, 0, nan(), nan()) // XYZ with null slots.
	checkLink(t, links[4], [4]float32{80, 190, 90, 200}, 0, 10, 180)      // Integer page index, 0-based.
	checkLink(t, links[5], [4]float32{90, 190, 95, 200}, 0, nan(), nan()) // Fit: no point at all.
}

// TestNamedDestinations covers both named-destination stores: the old-style /Dests dictionary (PDF 1.1,
// name-keyed) and the /Names → /Dests name tree (byte-string keys, /Kids with /Limits), including a /GoTo
// action naming the destination and a value wrapped in a /D dictionary.
func TestNamedDestinations(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /Dests 10 0 R /Names << /Dests 11 0 R >> >>",
		2: "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Annots [5 0 R 6 0 R 7 0 R] >>",
		4: pageBox200,
		// Old-style dictionary, value wrapped in /D.
		10: "<< /OldStyle << /D [4 0 R /XYZ 5 190 0] >> >>",
		// Name tree: a root with kids and limits.
		11: "<< /Kids [12 0 R 13 0 R] >>",
		12: "<< /Limits [(aaa) (mmm)] /Names [(alpha) [4 0 R /XYZ 20 180 0] (beta) [4 0 R /Fit]] >>",
		13: "<< /Limits [(nnn) (zzz)] /Names [(omega) [4 0 R /XYZ 30 170 0]] >>",
		5:  "<< /Type /Annot /Subtype /Link /Rect [10 10 20 20] /Dest /OldStyle >>",
		6:  "<< /Type /Annot /Subtype /Link /Rect [30 10 40 20] /A << /S /GoTo /D (omega) >> >>",
		7:  "<< /Type /Annot /Subtype /Link /Rect [50 10 60 20] /Dest (alpha) >>",
	}))
	links := d.Links(0)
	if len(links) != 3 {
		t.Fatalf("got %d links, want 3", len(links))
	}
	checkLink(t, links[0], [4]float32{10, 180, 20, 190}, 1, 5, 10)  // Old-style /Dests, via /D wrapper.
	checkLink(t, links[1], [4]float32{30, 180, 40, 190}, 1, 30, 30) // Name tree, right-hand kid, via /GoTo.
	checkLink(t, links[2], [4]float32{50, 180, 60, 190}, 1, 20, 20) // Name tree, left-hand kid, direct /Dest.
	for i, l := range links {
		if l.External || l.URI != "" {
			t.Errorf("link %d: expected internal link with empty URI, got external=%v uri=%q", i, l.External, l.URI)
		}
	}
}

// TestNamedDestinationMisses: unknown names and names outside every kid's limits resolve to page -1 (the
// public API drops such links), while broken limits do not hide a kid's names.
func TestNamedDestinationMisses(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /Names << /Dests 11 0 R >> >>",
		2: pagesOneKid,
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Annots [5 0 R 6 0 R] >>",
		// A kid whose limits are broken (not strings) must still be searched.
		11: "<< /Kids [12 0 R] >>",
		12: "<< /Limits [1 2] /Names [(target) [3 0 R /XYZ 5 195 0]] >>",
		5:  "<< /Type /Annot /Subtype /Link /Rect [10 10 20 20] /Dest (target) >>",
		6:  "<< /Type /Annot /Subtype /Link /Rect [30 10 40 20] /Dest (missing) >>",
	}))
	links := d.Links(0)
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2", len(links))
	}
	checkLink(t, links[0], [4]float32{10, 180, 20, 190}, 0, 5, 5)
	if links[1].Page != -1 {
		t.Errorf("missing named dest resolved to page %d, want -1", links[1].Page)
	}
}

// TestURIActionClassification pins fz_is_external_link semantics: a scheme makes a URI external; "#page=" and
// "#nameddest=" fragments resolve internally (the zoom x,y values are already top-left coordinates); anything
// else is an unresolvable internal link (page -1). Unsupported action kinds produce no link at all, and
// non-link annotations are skipped.
func TestURIActionClassification(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R /Names << /Dests 11 0 R >> >>",
		2:  "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		3:  "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Annots [5 0 R 6 0 R 7 0 R 8 0 R 9 0 R 10 0 R 12 0 R] >>",
		4:  pageBox200,
		11: "<< /Names [(spot) [4 0 R /XYZ 15 185 0]] >>",
		5:  "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (https://example.com/x) >> >>",
		6:  "<< /Type /Annot /Subtype /Link /Rect [0 20 10 30] /A << /S /URI /URI (mailto:a@b.c) >> >>",
		7:  "<< /Type /Annot /Subtype /Link /Rect [0 40 10 50] /A << /S /URI /URI (#page=2&zoom=100,30,50) >> >>",
		8:  "<< /Type /Annot /Subtype /Link /Rect [0 60 10 70] /A << /S /URI /URI (#nameddest=spot) >> >>",
		9:  "<< /Type /Annot /Subtype /Link /Rect [0 80 10 90] /A << /S /URI /URI (relative/path.html) >> >>",
		10: "<< /Type /Annot /Subtype /Link /Rect [0 100 10 110] /A << /S /JavaScript /JS (app.alert(1)) >> >>",
		12: "<< /Type /Annot /Subtype /Text /Rect [0 120 10 130] /Contents (note) >>",
	}))
	links := d.Links(0)
	if len(links) != 5 {
		t.Fatalf("got %d links, want 5", len(links))
	}
	if !links[0].External || links[0].URI != "https://example.com/x" {
		t.Errorf("link 0 = %+v, want external https URI", links[0])
	}
	if !links[1].External || links[1].URI != "mailto:a@b.c" {
		t.Errorf("link 1 = %+v, want external mailto URI", links[1])
	}
	if links[2].External || links[2].Page != 1 {
		t.Errorf("link 2 = %+v, want internal page 1", links[2])
	}
	checkCoord(t, "fragment DestX", links[2].DestX, 30)
	checkCoord(t, "fragment DestY", links[2].DestY, 50)
	if links[3].External || links[3].Page != 1 {
		t.Errorf("link 3 = %+v, want internal page 1 via nameddest", links[3])
	}
	checkCoord(t, "nameddest DestX", links[3].DestX, 15)
	checkCoord(t, "nameddest DestY", links[3].DestY, 15)
	if links[4].External || links[4].Page != -1 || links[4].URI != "" {
		t.Errorf("link 4 = %+v, want unresolvable internal (page -1, empty URI)", links[4])
	}
}

// TestOutlineTree covers nesting, /GoTo action destinations, an item with a URI action (kept, page -1), and a
// /Next cycle (the walk terminates and keeps the entries seen up to the repeat).
func TestOutlineTree(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R /Outlines 10 0 R >>",
		2:  pagesOneKid,
		3:  pageBox200,
		10: "<< /Type /Outlines /First 11 0 R /Last 12 0 R /Count 2 >>",
		11: "<< /Title (One) /Parent 10 0 R /Next 12 0 R /Dest [3 0 R /XYZ 10 150 0] /First 13 0 R /Last 13 0 R /Count -1 >>",
		12: "<< /Title (Two) /Parent 10 0 R /Prev 11 0 R /A << /S /GoTo /D [3 0 R /FitH 100] >> >>",
		13: "<< /Title (Child) /Parent 11 0 R /A << /S /URI /URI (https://example.com) >> >>",
	}))
	root := d.Outline()
	if root == nil || root.Next == nil {
		t.Fatal("expected two top-level outline items")
	}
	if root.Title != "One" || root.Page != 0 {
		t.Errorf("item 0 = %q page %d, want One page 0", root.Title, root.Page)
	}
	checkCoord(t, "item 0 X", root.X, 10)
	checkCoord(t, "item 0 Y", root.Y, 50)
	if root.Down == nil || root.Down.Title != "Child" || root.Down.Page != -1 {
		t.Errorf("expected child item with page -1, got %+v", root.Down)
	}
	two := root.Next
	if two.Title != "Two" || two.Page != 0 || two.Next != nil || two.Down != nil {
		t.Errorf("item 1 = %+v, want Two, page 0, no children or siblings", two)
	}
	checkCoord(t, "item 1 X", two.X, nan())
	checkCoord(t, "item 1 Y", two.Y, 100)
}

// TestOutlineCycle: a /Next chain that loops back terminates at the repeat rather than hanging or ballooning.
func TestOutlineCycle(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R /Outlines 10 0 R >>",
		2:  pagesOneKid,
		3:  pageBox200,
		10: "<< /Type /Outlines /First 11 0 R >>",
		11: "<< /Title (A) /Next 12 0 R >>",
		12: "<< /Title (B) /Next 11 0 R >>", // Loops back to A.
	}))
	count := 0
	for item := d.Outline(); item != nil && count < 10; item = item.Next {
		count++
	}
	if count != 2 {
		t.Errorf("cyclic outline yielded %d items, want 2", count)
	}
}

// TestGoToWithoutDest: a /GoTo action with no /D produces no link at all (matching MuPDF's skip), unlike a
// present-but-unresolvable destination.
func TestGoToWithoutDest(t *testing.T) {
	d := navDoc(t, "/MediaBox [0 0 200 200]",
		"<< /Type /Annot /Subtype /Link /Rect [10 10 20 20] /A << /S /GoTo >> >>")
	if links := d.Links(0); len(links) != 0 {
		t.Errorf("got %d links, want 0", len(links))
	}
}

// TestLinksPageRange: out-of-range page numbers yield nil rather than panicking.
func TestLinksPageRange(t *testing.T) {
	d := navDoc(t, "/MediaBox [0 0 200 200]", "")
	if d.Links(-1) != nil || d.Links(1) != nil {
		t.Error("expected nil links for out-of-range pages")
	}
	if _, _, err := d.PageSize(-1); err == nil {
		t.Error("expected an error for PageSize(-1)")
	}
	if d.Outline() != nil {
		t.Error("expected nil outline for a document without /Outlines")
	}
}
