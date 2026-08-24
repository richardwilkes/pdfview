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
	"strings"
	"sync"
	"testing"

	"github.com/richardwilkes/pdfview"
)

// openCorpus opens a corpus document and releases it when the test ends.
func openCorpus(t *testing.T, name string) *pdfview.Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", name))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(doc.Release)
	return doc
}

// textPage extracts page 0 of a corpus document at the given dpi.
func textPage(t *testing.T, name string, dpi int) *pdfview.TextPage {
	t.Helper()
	tp, err := openCorpus(t, name).TextPage(0, dpi)
	if err != nil {
		t.Fatalf("TextPage(%s): %v", name, err)
	}
	return tp
}

// findText returns the first character index whose text reads exactly s, failing the test when there is none. Indices
// count characters, so this holds only for text spelled one character per rune, as every needle here is.
func findText(t *testing.T, tp *pdfview.TextPage, s string) int {
	t.Helper()
	n := len([]rune(s))
	for i := 0; i+n <= tp.Len(); i++ {
		if tp.Text(i, i+n) == s {
			return i
		}
	}
	t.Fatalf("the fixture no longer contains %q", s)
	return -1
}

// TestTextPageText pins what a whole-page copy produces for the standard-14 fixture: the lines in the order the
// content stream draws them, one newline per baseline change, and — the point of the "Kerned Text" line — exactly one
// space where the file has no space character at all, only a -500 TJ kern the matcher reads as a word gap.
func TestTextPageText(t *testing.T) {
	tp := textPage(t, "text-std14.pdf", 72)
	const want = "Hello World\n" +
		"The quick brown\n" +
		"fox jumps over the lazy dog.\n" +
		"hello world\n" +
		"Spaced words here\n" +
		"Kerned Text\n" +
		"Bold statement!"
	got := tp.Text(0, tp.Len())
	if got != want {
		t.Fatalf("Text =\n%q\nwant\n%q", got, want)
	}
	// The synthesized space is not a character: the line holds ten characters and reads as eleven runes.
	lineStart, lineEnd := tp.LineAt(findText(t, tp, "Kerned"))
	if line := tp.Text(lineStart, lineEnd); line != "Kerned Text" {
		t.Errorf("the kerned line reads %q, want %q", line, "Kerned Text")
	}
	if n := lineEnd - lineStart; n != len("KernedText") {
		t.Errorf("the kerned line holds %d characters, want %d — the space must be synthesized, not recorded", n,
			len("KernedText"))
	}
	// WordAt stops at the synthesized space, as a search would.
	if start, end := tp.WordAt(lineStart); tp.Text(start, end) != "Kerned" {
		t.Errorf("WordAt at the start of the kerned line selected %q, want %q", tp.Text(start, end), "Kerned")
	}
}

// TestTextPageLigature pins that a glyph spelling several characters selects and highlights as the characters it
// spells, not as the glyph: the extra characters occupy their own indices (so a selection can start between them) but
// carry no advance, so the whole ligature still paints as one rectangle rather than one per character.
func TestTextPageLigature(t *testing.T) {
	tp := textPage(t, "text-ligature.pdf", 72)
	const want = "sacrifice\nflower\noffice\n𝐀AZ\naaabac\nflute"
	if got := tp.Text(0, tp.Len()); got != want {
		t.Fatalf("Text =\n%q\nwant\n%q", got, want)
	}
	// The "flower" line opens with a single glyph carrying U+FB02, which extraction decomposes into "fl".
	start, end := tp.LineAt(findText(t, tp, "flower"))
	if got := tp.Text(start, start+2); got != "fl" {
		t.Errorf("the ligature's two indices read %q, want %q", got, "fl")
	}
	if hits := tp.Highlights(start, start+2); len(hits) != 1 {
		t.Errorf("highlighting the ligature produced %d rectangles, want 1: %v", len(hits), hits)
	} else if hits[0].Empty() {
		t.Errorf("the ligature's highlight is empty: %v", hits[0])
	}
	if hits := tp.Highlights(start, end); len(hits) != 1 {
		t.Errorf("highlighting the whole word produced %d rectangles, want 1: %v", len(hits), hits)
	}
	// A word selection over the ligature covers the letters it spells.
	if s, e := tp.WordAt(start); tp.Text(s, e) != "flower" {
		t.Errorf("WordAt on the ligature selected %q, want %q", tp.Text(s, e), "flower")
	}
	// The ligature shares its width between the letters it spells: a click in its leading part sits before "f", at
	// its center between "f" and "l", at its trailing edge after both. The fillers' own geometry (all at the pen)
	// would put the last click between "f" and "l" too.
	glyph := tp.Highlights(start, start+2)
	if len(glyph) != 1 {
		t.Fatalf("the ligature highlighted as %d rectangles, want 1: %v", len(glyph), glyph)
	}
	y := (glyph[0].Min.Y + glyph[0].Max.Y) / 2
	eighth := glyph[0].Dx() / 8
	for _, tc := range []struct {
		name    string
		x, want int
	}{
		{name: "leading part", x: glyph[0].Min.X + eighth, want: start},
		{name: "center", x: (glyph[0].Min.X + glyph[0].Max.X) / 2, want: start + 1},
		{name: "trailing edge", x: glyph[0].Max.X - eighth, want: start + 2},
	} {
		if got := tp.IndexAt(image.Pt(tc.x, y)); got != tc.want {
			t.Errorf("%s of the ligature %v: IndexAt(%d, %d) = %d, want %d", tc.name, glyph[0], tc.x, y, got, tc.want)
		}
	}
}

// TestTextPageIndexAtRoundTrip pins the two directions against each other: the center of a character's own highlight
// must resolve to a caret within the shape that highlight paints. For a glyph spelling several letters that shape is
// the whole glyph, so the caret may land at any boundary the glyph's width is shared among. It runs at more than one
// dpi because the point makes the trip through the pixel space both ways.
func TestTextPageIndexAtRoundTrip(t *testing.T) {
	for _, name := range []string{"text-std14.pdf", "text-type1.pdf", "text-ligature.pdf"} {
		for _, dpi := range []int{72, 150} {
			doc := openCorpus(t, name)
			tp, err := doc.TextPage(0, dpi)
			if err != nil {
				t.Fatalf("%s at %d dpi: %v", name, dpi, err)
			}
			if tp.Len() == 0 {
				t.Fatalf("%s: no text was extracted", name)
			}
			// A character that paints nothing shares the glyph of the character before it, so the group a rectangle
			// stands for runs to the next character that paints something of its own.
			var starts []int
			for i := range tp.Len() {
				if len(tp.Highlights(i, i+1)) > 0 {
					starts = append(starts, i)
				}
			}
			if len(starts) == 0 {
				t.Fatalf("%s at %d dpi: no character painted anything", name, dpi)
			}
			if starts[0] != 0 {
				t.Errorf("%s at %d dpi: the page opens with %d characters that paint nothing", name, dpi, starts[0])
			}
			for k, i := range starts {
				hits := tp.Highlights(i, i+1)
				if len(hits) != 1 {
					t.Fatalf("%s at %d dpi: character %d highlighted as %d rectangles, want 1", name, dpi, i,
						len(hits))
				}
				groupEnd := tp.Len()
				if k+1 < len(starts) {
					groupEnd = starts[k+1]
				}
				center := image.Pt((hits[0].Min.X+hits[0].Max.X)/2, (hits[0].Min.Y+hits[0].Max.Y)/2)
				if got := tp.IndexAt(center); got < i || got > groupEnd {
					t.Errorf("%s at %d dpi: IndexAt(%v) = %d for the glyph at %d spelling %q (%v), want a caret in "+
						"[%d, %d]", name, dpi, center, got, i, tp.Text(i, groupEnd), hits[0], i, groupEnd)
				}
			}
		}
	}
}

// findAllFold returns the start of every character range equal to s under simple case folding, the folding the matcher
// uses. For a needle with no space in it this is the same rule as search, since only a synthesized space or a line
// break could separate the two.
func findAllFold(tp *pdfview.TextPage, s string) []int {
	var out []int
	n := len([]rune(s))
	for i := 0; i+n <= tp.Len(); i++ {
		if strings.EqualFold(tp.Text(i, i+n), s) {
			out = append(out, i)
		}
	}
	return out
}

// TestTextPageForSizeMatchesRenderPageForSize pins that a fit-to-box image and the text labeled for it are the same
// pixel space exactly. RenderPageForSize scales by min(maxWidth/width, maxHeight/height), which is no whole dpi, so no
// TextPage made from a dpi can name a pixel of that image. The comparison is against the render's own SearchHits, the
// same rectangles in the same order, since search and selection both funnel through quadToRect.
func TestTextPageForSizeMatchesRenderPageForSize(t *testing.T) {
	const needle = "Taxpayer"
	doc := openCorpus(t, "irs-fw9.pdf")
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	for _, box := range [][2]int{
		{200, 200},  // Width-limited and tiny.
		{813, 611},  // Height-limited, and neither extent a whole multiple of the page's.
		{1000, 999}, // Larger than the page renders at 72 dpi.
	} {
		rendered, rerr := doc.RenderPageForSize(0, box[0], box[1], 1000, needle)
		if rerr != nil {
			t.Fatalf("box %v: RenderPageForSize: %v", box, rerr)
		}
		fitted, ferr := tp.ForSize(box[0], box[1])
		if ferr != nil {
			t.Fatalf("box %v: ForSize: %v", box, ferr)
		}
		matches := findAllFold(fitted, needle)
		if len(matches) == 0 {
			t.Fatalf("box %v: the fixture no longer contains %q", box, needle)
		}
		got := make([]image.Rectangle, 0, len(matches))
		for _, i := range matches {
			got = append(got, fitted.Highlights(i, i+len(needle))...)
		}
		if len(got) != len(rendered.SearchHits) {
			t.Fatalf("box %v: %d selection rectangles over %d matches, the render reports %d search hits", box,
				len(got), len(matches), len(rendered.SearchHits))
		}
		for i := range got {
			if got[i] != rendered.SearchHits[i] {
				t.Errorf("box %v: rectangle %d = %v, the render's search hit is %v", box, i, got[i],
					rendered.SearchHits[i])
			}
		}
		// Every rectangle lives in the image the caller gets back.
		for _, hit := range fitted.Highlights(0, fitted.Len()) {
			if !hit.In(rendered.Image.Rect) {
				t.Fatalf("box %v: highlight %v is not inside the %v image", box, hit, rendered.Image.Rect)
			}
		}
	}
}

// TestTextPageForSizeRejectsWhatRenderPageForSizeRejects pins that the two agree on the boxes they refuse, so a caller
// never holds text labeled for an image the render would not have produced.
func TestTextPageForSizeRejectsWhatRenderPageForSizeRejects(t *testing.T) {
	doc := openCorpus(t, "text-std14.pdf")
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                string
		maxWidth, maxHeight int
	}{
		{name: "zero width", maxWidth: 0, maxHeight: 100},
		{name: "zero height", maxWidth: 100, maxHeight: 0},
		{name: "negative width", maxWidth: -800, maxHeight: 600},
		{name: "both zero", maxWidth: 0, maxHeight: 0},
		{name: "past the pixel cap", maxWidth: 1 << 20, maxHeight: 1 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, renderErr := doc.RenderPageForSize(0, tc.maxWidth, tc.maxHeight, 0, "")
			if renderErr == nil {
				t.Fatalf("RenderPageForSize accepted %dx%d; the case no longer tests a refusal", tc.maxWidth,
					tc.maxHeight)
			}
			fitted, fitErr := tp.ForSize(tc.maxWidth, tc.maxHeight)
			if !errors.Is(fitErr, renderErr) {
				t.Errorf("ForSize error = %v, want the render's %v", fitErr, renderErr)
			}
			if fitted != nil {
				t.Errorf("ForSize returned a page along with its error: %v", fitted)
			}
		})
	}
	// A nil receiver reports what a page with no extent reports rather than dereferencing nil.
	var none *pdfview.TextPage
	if _, err = none.ForSize(100, 100); !errors.Is(err, pdfview.ErrInvalidPageSize) {
		t.Errorf("nil receiver: ForSize error = %v, want ErrInvalidPageSize", err)
	}
	if got := none.AtDPI(72); got != nil {
		t.Errorf("nil receiver: AtDPI = %v, want nil", got)
	}
}

// TestTextPageAtDPIMatchesAFreshExtraction pins that re-labeling is the identical answer to extracting at that dpi,
// character for character and pixel for pixel: the license for a viewer to hold one TextPage per page instead of one
// per zoom level.
func TestTextPageAtDPIMatchesAFreshExtraction(t *testing.T) {
	doc := openCorpus(t, "irs-fw9.pdf")
	base, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	for _, dpi := range []int{72, 96, 150, 432} {
		want, wantErr := doc.TextPage(0, dpi)
		if wantErr != nil {
			t.Fatalf("%d dpi: %v", dpi, wantErr)
		}
		got := base.AtDPI(dpi)
		if got.Len() != want.Len() {
			t.Fatalf("%d dpi: Len = %d, want %d", dpi, got.Len(), want.Len())
		}
		if got.Text(0, got.Len()) != want.Text(0, want.Len()) {
			t.Errorf("%d dpi: the re-labeled text differs from a fresh extraction", dpi)
		}
		gotHits, wantHits := got.Highlights(0, got.Len()), want.Highlights(0, want.Len())
		if len(gotHits) != len(wantHits) {
			t.Fatalf("%d dpi: %d highlights, want %d", dpi, len(gotHits), len(wantHits))
		}
		for i := range wantHits {
			if gotHits[i] != wantHits[i] {
				t.Errorf("%d dpi: highlight %d = %v, want %v", dpi, i, gotHits[i], wantHits[i])
			}
		}
		for _, pt := range []image.Point{{}, {X: 50, Y: 40}, {X: 300, Y: 200}, {X: 1 << 20, Y: 1 << 20}} {
			if gotIndex, wantIndex := got.IndexAt(pt), want.IndexAt(pt); gotIndex != wantIndex {
				t.Errorf("%d dpi: IndexAt(%v) = %d, want %d", dpi, pt, gotIndex, wantIndex)
			}
		}
	}
	// Re-labeling never disturbs the receiver, and a labeling for the size it already carries is that same labeling.
	if got, want := base.Highlights(0, base.Len()), base.AtDPI(72).Highlights(0, base.Len()); len(got) != len(want) {
		t.Fatalf("re-labeling at the original dpi produced %d highlights, want %d", len(want), len(got))
	} else {
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("re-labeling at the original dpi moved highlight %d from %v to %v", i, got[i], want[i])
			}
		}
	}
}

// TestTextPageRelabelSurvivesRelease pins that neither re-labeling method needs the document: they take no lock and
// touch no engine state, so a viewer can zoom after Release and without blocking a render.
func TestTextPageRelabelSurvivesRelease(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", "text-std14.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	wantText := tp.Text(0, tp.Len())
	scaled := tp.AtDPI(144)
	fitted, err := tp.ForSize(800, 800)
	if err != nil {
		t.Fatal(err)
	}
	wantScaled := scaled.Highlights(0, scaled.Len())
	wantFitted := fitted.Highlights(0, fitted.Len())

	doc.Release()

	after, err := tp.ForSize(800, 800)
	if err != nil {
		t.Fatalf("ForSize after Release: %v", err)
	}
	for _, tc := range []struct {
		name string
		page *pdfview.TextPage
		want []image.Rectangle
	}{
		{name: "AtDPI before Release", page: scaled, want: wantScaled},
		{name: "ForSize before Release", page: fitted, want: wantFitted},
		{name: "ForSize after Release", page: after, want: wantFitted},
		{name: "AtDPI after Release", page: tp.AtDPI(144), want: wantScaled},
	} {
		if got := tc.page.Text(0, tc.page.Len()); got != wantText {
			t.Errorf("%s: the text changed", tc.name)
		}
		got := tc.page.Highlights(0, tc.page.Len())
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %d highlights, want %d", tc.name, len(got), len(tc.want))
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s: highlight %d = %v, want %v", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// TestTextPageRotatedPage pins the model on a page whose /Rotate turns the text sideways in the rendered image: the
// line is taller than it is wide, and hit testing still resolves along the direction the text advances rather than
// along x.
func TestTextPageRotatedPage(t *testing.T) {
	doc := openCorpus(t, "rotate90.pdf")
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tp.Text(0, tp.Len()), "Rotated"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	if start, end := tp.LineAt(0); start != 0 || end != tp.Len() {
		t.Fatalf("LineAt(0) = (%d, %d), want (0, %d)", start, end, tp.Len())
	}
	hits := tp.Highlights(0, tp.Len())
	if len(hits) != 1 {
		t.Fatalf("the whole line highlighted as %d rectangles, want 1: %v", len(hits), hits)
	}
	if hits[0].Dy() <= hits[0].Dx() {
		t.Errorf("the rotated line's highlight %v is wider than it is tall", hits[0])
	}
	width, height, err := doc.PageRenderSize(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	if !hits[0].In(image.Rect(0, 0, width, height)) {
		t.Errorf("the highlight %v is not inside the %dx%d image", hits[0], width, height)
	}
	for i := range tp.Len() {
		char := tp.Highlights(i, i+1)
		if len(char) != 1 {
			t.Fatalf("character %d highlighted as %d rectangles, want 1", i, len(char))
		}
		center := image.Pt((char[0].Min.X+char[0].Max.X)/2, (char[0].Min.Y+char[0].Max.Y)/2)
		if got := tp.IndexAt(center); got != i && got != i+1 {
			t.Errorf("IndexAt(%v) = %d for character %d", center, got, i)
		}
	}
}

// TestTextPageUnmappedRunes pins that extraction never spells a character it has no Unicode for: a font that leaves a
// code unmapped contributes an index and a highlight, but never a NUL to the copied text.
func TestTextPageUnmappedRunes(t *testing.T) {
	for _, name := range []string{"text-type0-cid0.pdf", "text-type0-cid2.pdf"} {
		doc := openCorpus(t, name)
		for pageNumber := range doc.PageCount() {
			tp, err := doc.TextPage(pageNumber, 72)
			if err != nil {
				t.Fatalf("%s page %d: %v", name, pageNumber, err)
			}
			text := tp.Text(0, tp.Len())
			if strings.ContainsRune(text, 0) {
				t.Errorf("%s page %d: the extracted text contains U+0000: %q", name, pageNumber, text)
			}
		}
	}
}

// TestTextPageInvisibleTextIncluded pins the documented contract that extraction is what search sees: text drawn in
// render mode 3 (invisible — the mode a scanned page's OCR layer uses) is selectable, because it is searchable.
func TestTextPageInvisibleTextIncluded(t *testing.T) {
	tp := textPage(t, "text-trmodes.pdf", 72)
	if text := tp.Text(0, tp.Len()); !strings.Contains(text, "Ghost words") {
		t.Fatalf("the invisible line is missing from the extracted text: %q", text)
	}
	start, end := tp.LineAt(findText(t, tp, "Ghost words"))
	if got := tp.Text(start, end); got != "Ghost words" {
		t.Errorf("the invisible line reads %q, want %q", got, "Ghost words")
	}
	if hits := tp.Highlights(start, end); len(hits) != 1 {
		t.Errorf("the invisible line highlighted as %d rectangles, want 1: %v", len(hits), hits)
	}
}

// TestTextPageHighlightsWithinImage pins that every rectangle lives in the pixel space of the image RenderPage produces
// at the same dpi: extents are sized in float32 and coordinates scaled in float64, so a page-edge character would
// otherwise name a row the image does not have.
func TestTextPageHighlightsWithinImage(t *testing.T) {
	doc := openCorpus(t, "irs-f1040.pdf")
	for _, dpi := range []int{72, 150, 432} {
		width, height, err := doc.PageRenderSize(0, dpi)
		if err != nil {
			t.Fatalf("%d dpi: %v", dpi, err)
		}
		bounds := image.Rect(0, 0, width, height)
		tp, err := doc.TextPage(0, dpi)
		if err != nil {
			t.Fatalf("%d dpi: %v", dpi, err)
		}
		if tp.Len() == 0 {
			t.Fatalf("%d dpi: no text was extracted", dpi)
		}
		for _, hit := range tp.Highlights(0, tp.Len()) {
			if !hit.In(bounds) {
				t.Fatalf("%d dpi: highlight %v is not inside the %v image", dpi, hit, bounds)
			}
		}
		for i := range tp.Len() {
			for _, hit := range tp.Highlights(i, i+1) {
				if !hit.In(bounds) {
					t.Fatalf("%d dpi: character %d highlight %v is not inside the %v image", dpi, i, hit, bounds)
				}
			}
		}
	}
}

// TestTextPageEmptyPage pins that a page with no text at all is a valid, inert TextPage rather than an error or a nil.
func TestTextPageEmptyPage(t *testing.T) {
	tp := textPage(t, "vectors.pdf", 72)
	if tp == nil {
		t.Fatal("TextPage returned nil for a page with no text")
	}
	if got := tp.Len(); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
	if got := tp.IndexAt(image.Pt(50, 50)); got != 0 {
		t.Errorf("IndexAt = %d, want 0", got)
	}
	if got := tp.Text(0, 10); got != "" {
		t.Errorf("Text = %q, want an empty string", got)
	}
	if got := tp.Highlights(0, 10); got != nil {
		t.Errorf("Highlights = %v, want nil", got)
	}
	if start, end := tp.WordAt(0); start != 0 || end != 0 {
		t.Errorf("WordAt = (%d, %d), want (0, 0)", start, end)
	}
	if start, end := tp.LineAt(0); start != 0 || end != 0 {
		t.Errorf("LineAt = (%d, %d), want (0, 0)", start, end)
	}
}

// TestTextPageRangeClamping pins that no pair of indices a caller can produce — a backwards drag, an index held over
// from another page, arithmetic gone wrong — can panic or reach outside the page.
func TestTextPageRangeClamping(t *testing.T) {
	tp := textPage(t, "text-std14.pdf", 72)
	whole := tp.Text(0, tp.Len())
	for _, tc := range []struct {
		name       string
		start, end int
	}{
		{name: "reversed", start: 10, end: 3},
		{name: "negative start", start: -100, end: 5},
		{name: "both negative", start: -100, end: -50},
		{name: "past the end", start: 5, end: 1 << 30},
		{name: "both past the end", start: 1 << 30, end: 1 << 30},
		{name: "zero width", start: 7, end: 7},
		{name: "min int", start: -1 << 62, end: 1 << 62},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := tp.Text(tc.start, tc.end)
			if len(text) > len(whole) {
				t.Errorf("Text returned %d bytes, more than the whole page's %d", len(text), len(whole))
			}
			tp.Highlights(tc.start, tc.end)
		})
	}
	// The single-index methods clamp the same way.
	for _, index := range []int{-1 << 62, -1, 0, tp.Len(), 1 << 30} {
		start, end := tp.WordAt(index)
		if start < 0 || end > tp.Len() || start > end {
			t.Errorf("WordAt(%d) = (%d, %d), outside [0, %d]", index, start, end, tp.Len())
		}
		if start, end = tp.LineAt(index); start < 0 || end > tp.Len() || start > end {
			t.Errorf("LineAt(%d) = (%d, %d), outside [0, %d]", index, start, end, tp.Len())
		}
	}
	// A point nowhere near the page still resolves to a caret position.
	for _, pt := range []image.Point{{X: -1 << 30, Y: -1 << 30}, {X: 1 << 30, Y: 1 << 30}, {X: 0, Y: 1 << 30}} {
		if got := tp.IndexAt(pt); got < 0 || got > tp.Len() {
			t.Errorf("IndexAt(%v) = %d, outside [0, %d]", pt, got, tp.Len())
		}
	}
}

// TestTextPageSurvivesRelease pins the documented lifetime: a TextPage holds no reference to its document, so releasing
// the document leaves the extracted text working.
func TestTextPageSurvivesRelease(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", "text-std14.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	before := tp.Text(0, tp.Len())
	length := tp.Len()
	hits := tp.Highlights(0, length)
	index := tp.IndexAt(image.Pt(50, 30))

	doc.Release()

	if got := tp.Len(); got != length {
		t.Errorf("after Release: Len = %d, want %d", got, length)
	}
	if got := tp.Text(0, tp.Len()); got != before {
		t.Errorf("after Release: Text = %q, want %q", got, before)
	}
	if got := tp.IndexAt(image.Pt(50, 30)); got != index {
		t.Errorf("after Release: IndexAt = %d, want %d", got, index)
	}
	got := tp.Highlights(0, length)
	if len(got) != len(hits) {
		t.Fatalf("after Release: %d highlights, want %d", len(got), len(hits))
	}
	for i := range hits {
		if got[i] != hits[i] {
			t.Errorf("after Release: highlight %d = %v, want %v", i, got[i], hits[i])
		}
	}
	// Extraction from the released document reports an error rather than panicking.
	if _, err = doc.TextPage(0, 72); err == nil {
		t.Error("TextPage on a released document returned no error")
	}
}

// TestOverallMaxTextChars pins that the retained-text cap is honored and what it keeps stays usable: the excess is
// dropped, leaving a shorter but coherent page rather than an error. The global is saved and restored, as the
// OverallMaxPixels tests do.
func TestOverallMaxTextChars(t *testing.T) {
	doc := openCorpus(t, "irs-f1040.pdf")
	full, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 50
	if full.Len() <= limit {
		t.Fatalf("the fixture holds only %d characters; the cap would not bite", full.Len())
	}
	defer func(prev int) { pdfview.OverallMaxTextChars = prev }(pdfview.OverallMaxTextChars)
	pdfview.OverallMaxTextChars = limit
	capped, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	if capped.Len() > limit {
		t.Fatalf("Len = %d, past the cap of %d", capped.Len(), limit)
	}
	if capped.Len() == 0 {
		t.Fatal("the cap dropped everything")
	}
	// What was recorded is intact: the capped page's text is the start of the full page's.
	if got, want := capped.Text(0, capped.Len()), full.Text(0, capped.Len()); got != want {
		t.Errorf("capped text = %q, want the full page's leading %q", got, want)
	}
	if hits := capped.Highlights(0, capped.Len()); len(hits) == 0 {
		t.Error("the capped page produced no highlights")
	}
}

// TestTextPageConcurrent exercises the documented goroutine-safety of both sides at once: the Document serializes the
// extraction passes internally, and each TextPage is immutable so any number of readers may share one. Meaningful
// under -race, where an unsynchronized engine call or a mutated Page would be reported.
func TestTextPageConcurrent(t *testing.T) {
	doc := openCorpus(t, "irs-fw9.pdf")
	shared, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	want := shared.Text(0, shared.Len())
	if want == "" {
		t.Fatal("no text was extracted")
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Every goroutine reads the shared page while others extract new ones and render.
			if got := shared.Text(0, shared.Len()); got != want {
				t.Errorf("goroutine %d: the shared page's text changed", i)
			}
			shared.Highlights(0, shared.Len())
			shared.IndexAt(image.Pt(100+i, 200))
			shared.WordAt(i * 7)
			shared.LineAt(i * 7)
			own, textErr := doc.TextPage(0, 72+i)
			if textErr != nil {
				t.Errorf("goroutine %d: TextPage: %v", i, textErr)
				return
			}
			if got := own.Text(0, own.Len()); got != want {
				t.Errorf("goroutine %d: extracted different text than the shared page", i)
			}
			if _, renderErr := doc.RenderPage(0, 72, 10, "backup withholding"); renderErr != nil {
				t.Errorf("goroutine %d: RenderPage: %v", i, renderErr)
			}
		}()
	}
	wg.Wait()
}
