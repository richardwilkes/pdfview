// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package stext

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/content"
	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// corpusChars interprets one corpus page's content at scale 1 (page space) against a fresh structured-text device,
// exactly as the engine's extraction seam does — page content first, then annotation appearance streams. It is the
// only way to get REAL character geometry in front of these tests: the synthetic layouts elsewhere in this package
// are uniform by construction, and it is the corpus's kerning, mixed sizes, and rotation that make the selection
// heuristics worth pinning.
func corpusChars(t *testing.T, file string, pageNumber int) []Char {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testfiles", "corpus", file))
	if err != nil {
		t.Fatal(err)
	}
	document, err := doc.Open(data)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	ctm, err := document.PageCTM(pageNumber, 1)
	if err != nil {
		t.Fatalf("PageCTM(%d): %v", pageNumber, err)
	}
	dev := New()
	if body := document.PageContents(pageNumber); len(body) > 0 {
		content.Run(document.COS(), document.PageResources(pageNumber), body, ctm, dev, nil)
	}
	annots := content.NewAnnotRun(nil)
	for _, a := range document.Annotations(pageNumber) {
		annots.Annot(document.COS(), document.PageResources(pageNumber), a.Raw, a.Stream, a.Transform.Mul(ctm), dev)
	}
	return dev.Chars()
}

// TestSelectionQuadsMatchSearchQuads is the guard the whole selection model hangs on: highlighting a range must paint
// exactly what searching for that range's text paints. Both go through segmentQuads, so this pins that Page's line
// runs and range intersection hand it the same character slices the matcher does — over real corpus geometry, where
// the line breaks, the oversized-space and raised-character splits, and the rotated runs actually occur. The
// comparison is exact: these are the same float32 corners the oracle-pinned quad-parity goldens hold search to, so any
// drift here is drift away from MuPDF too.
func TestSelectionQuadsMatchSearchQuads(t *testing.T) {
	for _, tc := range []struct {
		file    string
		needles []string
	}{
		// The goldens' own needles for this page, including "brown fox" (wraps a line) and "Kerned Text" (a 0.5 em TJ
		// gap standing in for the space).
		{file: "text-std14.pdf", needles: []string{"Hello", "hello world", "brown fox", "QUICK", "Spaced words", "Kerned Text"}},
		// A dense real-world form: thousands of characters, many repeats of each needle, mixed sizes.
		{file: "irs-fw9.pdf", needles: []string{"backup withholding", "Taxpayer", "Certification", "Name"}},
		// The oversized-space fixture: "alpha beta" is one line but three quads.
		{file: "hit-quad-split.pdf", needles: []string{"alpha beta", "subject to backup withholding"}},
		// The raised-character fixture: "H2O" is one quad below a 0.1 em rise of the digit and three from there on.
		{file: "hit-quad-rise.pdf", needles: []string{"H2O"}},
		// Non-axis-aligned text, which the one merge rule walks into a single first-to-last-corner quad.
		{file: "rotate90.pdf", needles: []string{"Rotated"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			chars := corpusChars(t, tc.file, 0)
			page := NewPage(chars)
			if page.Len() != len(chars) {
				t.Fatalf("Len = %d, want %d", page.Len(), len(chars))
			}
			for _, needle := range tc.needles {
				runes := []rune(needle)
				var all []gfx.Quad
				matches := 0
				for i := 0; i < len(chars); {
					quads, end, ok := matchAt(chars, i, runes)
					if !ok {
						i++
						continue
					}
					matches++
					got := page.Quads(i, end)
					if len(got) != len(quads) {
						t.Errorf("%q at [%d,%d): Quads returned %d quads, the match has %d", needle, i, end,
							len(got), len(quads))
					} else {
						for k := range got {
							if got[k] != quads[k] {
								t.Errorf("%q at [%d,%d) quad %d:\n got %+v\nwant %+v", needle, i, end, k, got[k],
									quads[k])
							}
						}
					}
					all = append(all, got...)
					i = end
				}
				if matches == 0 {
					t.Fatalf("%q was not found; the fixture no longer exercises this case", needle)
				}
				// The per-match comparison above is against one match's quads; this is against the whole search, which
				// also pins that no match was skipped or double-counted.
				want := searchChars(chars, needle, math.MaxInt)
				if len(all) != len(want) {
					t.Fatalf("%q: selection produced %d quads across %d matches, Search produced %d", needle,
						len(all), matches, len(want))
				}
				for i := range want {
					if all[i] != want[i] {
						t.Errorf("%q quad %d:\n got %+v\nwant %+v", needle, i, all[i], want[i])
					}
				}
			}
		})
	}
}

// mkRotatedChar builds one character of a line advancing down the page (a 90-degree rotation): the baseline runs
// through +y, so the ascender and descender extend through x and lineBreakBetween must measure its offsets through x
// as well.
func mkRotatedChar(r rune, x, y, adv, size float32) Char {
	left, right := x-0.2*size, x+0.8*size
	return Char{
		Quad: gfx.Quad{
			UL: gfx.Point{X: right, Y: y},
			UR: gfx.Point{X: right, Y: y + adv},
			LL: gfx.Point{X: left, Y: y},
			LR: gfx.Point{X: left, Y: y + adv},
		},
		Origin: gfx.Point{X: x, Y: y},
		End:    gfx.Point{X: x, Y: y + adv},
		Rune:   r,
		Size:   size,
	}
}

// mkFiller builds the zero-width character a ligature's extra letter is recorded as: it sits at the pen (Origin ==
// End), so advanceDir can say nothing about which way its line runs.
func mkFiller(r rune, x, y, size float32) Char {
	c := mkChar(r, x, y, 0, size)
	return Char{Quad: c.Quad, Origin: c.Origin, End: c.Origin, Rune: r, Size: size, Axis: true}
}

func TestNewLinesSplitsOnLineBreakBetween(t *testing.T) {
	// A run on one baseline is one line while its gaps stay under the along-line threshold, however wide the words
	// themselves are spaced.
	chars, endX := mkWord("Hello", 100, 200, 10, 12)
	spaced, _ := mkWord("World", endX+spaceMaxDistEm*12*0.9, 200, 10, 12)
	chars = append(chars, spaced...)
	if lines := newLines(chars); len(lines) != 1 {
		t.Fatalf("one baseline: got %d lines, want 1", len(lines))
	} else if lines[0].start != 0 || lines[0].end != len(chars) {
		t.Fatalf("one baseline: line covers [%d,%d), want [0,%d)", lines[0].start, lines[0].end, len(chars))
	}

	// Each of the two distances lineBreakBetween measures breaks a line on its own. Perpendicular first, with the text
	// carrying straight on along the baseline so only that distance is in question.
	first, endX := mkWord("brown", 100, 200, 10, 12)
	next, _ := mkWord("fox", endX, 200+baseMaxDistEm*12*1.01, 10, 12)
	if lines := newLines(append(append([]Char(nil), first...), next...)); len(lines) != 2 {
		t.Fatalf("offset past the baseline threshold: got %d lines, want 2", len(lines))
	} else if lines[0].end != len(first) || lines[1].start != len(first) {
		t.Fatalf("split at %d/%d, want %d", lines[0].end, lines[1].start, len(first))
	}
	under, _ := mkWord("fox", endX, 200+baseMaxDistEm*12*0.99, 10, 12)
	if lines := newLines(append(append([]Char(nil), first...), under...)); len(lines) != 1 {
		t.Fatalf("offset under the baseline threshold: got %d lines, want 1", len(lines))
	}

	// Then along the baseline. A wrapped line is exactly this case: it starts a long way back along the advance
	// direction while its baseline has moved by less than one em, so the perpendicular test alone would swallow it.
	wrapped, _ := mkWord("fox", 40, 200+baseMaxDistEm*12*0.99, 10, 12)
	if lines := newLines(append(append([]Char(nil), first...), wrapped...)); len(lines) != 2 {
		t.Fatalf("wrapped line: got %d lines, want 2", len(lines))
	}
	// A forward jump under the threshold is a wide word gap on one line; one at the threshold is a break.
	gapped, _ := mkWord("fox", endX+spaceMaxDistEm*12*0.99, 200, 10, 12)
	if lines := newLines(append(append([]Char(nil), first...), gapped...)); len(lines) != 1 {
		t.Fatalf("gap under the along-line threshold: got %d lines, want 1", len(lines))
	}
	far, _ := mkWord("fox", endX+spaceMaxDistEm*12*1.01, 200, 10, 12)
	if lines := newLines(append(append([]Char(nil), first...), far...)); len(lines) != 2 {
		t.Fatalf("gap past the along-line threshold: got %d lines, want 2", len(lines))
	}

	// The thresholds scale with the size of the character being placed, not the one before it: a raised character
	// half the size of its neighbors breaks the line at half the absolute offset.
	small := mkChar('2', endX, 200-baseMaxDistEm*6*1.01, 10, 6)
	if lines := newLines(append(append([]Char(nil), first...), small)); len(lines) != 2 {
		t.Fatalf("a small raised character past its own threshold: got %d lines, want 2", len(lines))
	}
	small = mkChar('2', endX, 200-baseMaxDistEm*6*0.99, 10, 6)
	if lines := newLines(append(append([]Char(nil), first...), small)); len(lines) != 1 {
		t.Fatalf("a small raised character under its own threshold: got %d lines, want 1", len(lines))
	}

	// Rotated text advances through y, so its own offsets are along the line rather than across it.
	rotated := make([]Char, 0, len("Rotated"))
	for i, r := range "Rotated" {
		rotated = append(rotated, mkRotatedChar(r, 100, 200+float32(i)*10, 10, 12))
	}
	lines := newLines(rotated)
	if len(lines) != 1 {
		t.Fatalf("rotated run: got %d lines, want 1", len(lines))
	}
	if lines[0].ux != 0 || lines[0].uy != 1 {
		t.Fatalf("rotated run direction = (%v, %v), want (0, 1)", lines[0].ux, lines[0].uy)
	}
	if got, want := lines[0].bounds, (gfx.Rect{X0: 100 - 0.2*12, Y0: 200, X1: 100 + 0.8*12, Y1: 270}); got != want {
		t.Fatalf("rotated run bounds = %+v, want %+v", got, want)
	}

	// A line that opens with a filler must take its direction from the first character that actually advances: the
	// filler's degenerate advance would otherwise hand the whole line the horizontal fallback.
	withFiller := append([]Char{mkFiller('l', 100, 200, 12)}, rotated...)
	if lines = newLines(withFiller); len(lines) != 1 {
		t.Fatalf("filler-led run: got %d lines, want 1", len(lines))
	} else if lines[0].ux != 0 || lines[0].uy != 1 {
		t.Fatalf("filler-led run direction = (%v, %v), want (0, 1)", lines[0].ux, lines[0].uy)
	}
}

func TestIndexAtBoundaries(t *testing.T) {
	const (
		x0, y, adv, size = 100, 200, 10, 12
		count            = 5
	)
	chars, endX := mkWord("Hello", x0, y, adv, size)
	page := NewPage(chars)
	if got := page.IndexAt(gfx.Point{X: x0 - 20, Y: y}); got != 0 {
		t.Errorf("before the first character: got %d, want 0", got)
	}
	for i := range count {
		left := gfx.Point{X: x0 + float32(i)*adv + adv*0.25, Y: y}
		if got := page.IndexAt(left); got != i {
			t.Errorf("left half of character %d: got %d, want %d", i, got, i)
		}
		right := gfx.Point{X: x0 + float32(i)*adv + adv*0.75, Y: y}
		if got := page.IndexAt(right); got != i+1 {
			t.Errorf("right half of character %d: got %d, want %d", i, got, i+1)
		}
	}
	if got := page.IndexAt(gfx.Point{X: endX + 100, Y: y}); got != count {
		t.Errorf("past the last character: got %d, want %d", got, count)
	}
	// An empty page has no position but the one before everything.
	if got := NewPage(nil).IndexAt(gfx.Point{X: 10, Y: 10}); got != 0 {
		t.Errorf("empty page: got %d, want 0", got)
	}
	if got := (*Page)(nil).IndexAt(gfx.Point{X: 10, Y: 10}); got != 0 {
		t.Errorf("nil page: got %d, want 0", got)
	}
}

func TestIndexAtPicksNearestLine(t *testing.T) {
	// Two columns of two lines each, emitted column by column as a two-column PDF draws them: the left column's
	// characters all precede the right column's in the stream.
	const size, adv = 12, 10
	topLeft, _ := mkWord("aaaaa", 0, 100, adv, size)
	bottomLeft, _ := mkWord("bbbbb", 0, 130, adv, size)
	topRight, _ := mkWord("ccccc", 200, 100, adv, size)
	bottomRight, _ := mkWord("ddddd", 200, 130, adv, size)
	var chars []Char
	for _, run := range [][]Char{topLeft, bottomLeft, topRight, bottomRight} {
		chars = append(chars, run...)
	}
	page := NewPage(chars)
	if len(page.lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(page.lines))
	}
	// The gutter's center is equidistant from the left column's right edge (50) and the right column's left edge
	// (200). The tie must resolve toward the earlier line, so the caret lands at the end of the left column's top
	// line rather than at the start of the right column's — the behavior a text editor gives a click in a gutter.
	if got, want := page.IndexAt(gfx.Point{X: 125, Y: 100}), len(topLeft); got != want {
		t.Errorf("gutter: got %d, want %d (the end of the top-left line)", got, want)
	}
	// A point far below the page belongs to the nearest line above it, per column.
	if got, want := page.IndexAt(gfx.Point{X: 25, Y: 1000}), len(topLeft); got < want || got > want+len(bottomLeft) {
		t.Errorf("below the left column: got %d, want a position within the bottom-left line", got)
	}
	last := len(chars)
	if got := page.IndexAt(gfx.Point{X: 225, Y: 1000}); got <= last-len(bottomRight) || got > last {
		t.Errorf("below the right column: got %d, want a position within the last line", got)
	}
	// A point above everything picks the first line, and one far to the left of it stays at that line's start.
	if got := page.IndexAt(gfx.Point{X: -500, Y: -500}); got != 0 {
		t.Errorf("above and left of everything: got %d, want 0", got)
	}
}

func TestTextSynthesizesSpacesAndNewlines(t *testing.T) {
	// A 20 pt em makes the gapSpaceEm threshold exactly 4 pt, which float32 holds exactly: at 12 pt the threshold is
	// 2.4, and a character placed at endX + 2.4 comes back a fraction of a ulp SHORT of the threshold.
	const size, adv = 20, 10
	gap := float32(gapSpaceEm * size)

	// A gap at least gapSpaceEm em wide is the word space the stream itself never recorded.
	kerned, endX := mkWord("Kerned", 100, 200, adv, size)
	text, _ := mkWord("Text", endX+gap, 200, adv, size)
	page := NewPage(append(append([]Char(nil), kerned...), text...))
	if got, want := page.Text(0, page.Len()), "Kerned Text"; got != want {
		t.Errorf("kerned gap: Text = %q, want %q", got, want)
	}
	// A sub-threshold gap is just tight typesetting, not a space.
	tight, _ := mkWord("Text", endX+gap*0.9, 200, adv, size)
	page = NewPage(append(append([]Char(nil), kerned...), tight...))
	if got, want := page.Text(0, page.Len()), "KernedText"; got != want {
		t.Errorf("sub-threshold gap: Text = %q, want %q", got, want)
	}

	// A space the stream already carries is never doubled by the synthesized one, whichever side the gap falls on.
	chars := []Char{mkChar('a', 100, 200, adv, size), mkChar(' ', 110, 200, adv, size), mkChar('b', 125, 200, adv, size)}
	if got, want := NewPage(chars).Text(0, 3), "a b"; got != want {
		t.Errorf("gap after a space character: Text = %q, want %q", got, want)
	}
	chars = []Char{mkChar('a', 100, 200, adv, size), mkChar(' ', 115, 200, adv, size), mkChar('b', 125, 200, adv, size)}
	if got, want := NewPage(chars).Text(0, 3), "a b"; got != want {
		t.Errorf("gap before a space character: Text = %q, want %q", got, want)
	}

	// A line break is one newline, not one per character pair that crosses it, and a selection ending at the break
	// carries no trailing newline.
	line1, _ := mkWord("brown", 100, 200, adv, size)
	line2, _ := mkWord("fox", 40, 214, adv, size)
	page = NewPage(append(append([]Char(nil), line1...), line2...))
	if got, want := page.Text(0, page.Len()), "brown\nfox"; got != want {
		t.Errorf("line break: Text = %q, want %q", got, want)
	}
	if got, want := page.Text(0, len(line1)), "brown"; got != want {
		t.Errorf("selection ending at the break: Text = %q, want %q", got, want)
	}
	if got, want := page.Text(len(line1), page.Len()), "fox"; got != want {
		t.Errorf("selection starting at the break: Text = %q, want %q", got, want)
	}

	// A character the font gives no Unicode mapping for holds its index and its geometry but spells nothing.
	unmapped := mkChar(0, 110, 200, adv, size)
	page = NewPage([]Char{mkChar('a', 100, 200, adv, size), unmapped, mkChar('b', 120, 200, adv, size)})
	if got, want := page.Text(0, 3), "ab"; got != want {
		t.Errorf("unmapped rune: Text = %q, want %q", got, want)
	}
	if page.Len() != 3 {
		t.Errorf("unmapped rune: Len = %d, want 3", page.Len())
	}

	// Clamping and ordering: no pair of indices can panic, and a reversed pair reads forwards.
	if got, want := page.Text(-100, 100), "ab"; got != want {
		t.Errorf("out-of-range indices: Text = %q, want %q", got, want)
	}
	if got, want := page.Text(3, 0), "ab"; got != want {
		t.Errorf("reversed indices: Text = %q, want %q", got, want)
	}
	if got := page.Text(2, 2); got != "" {
		t.Errorf("empty range: Text = %q, want an empty string", got)
	}
	if got := (*Page)(nil).Text(0, 5); got != "" {
		t.Errorf("nil page: Text = %q, want an empty string", got)
	}
}

func TestWordAtStopsAtGapsAndLineBreaks(t *testing.T) {
	// See TestTextSynthesizesSpacesAndNewlines for why the em is 20 rather than 12.
	const size, adv = 20, 10
	gap := float32(gapSpaceEm * size)
	hello, endX := mkWord("Hello", 100, 200, adv, size)
	world, _ := mkWord("World", endX+gap, 200, adv, size)
	next, _ := mkWord("Next", 100, 214, adv, size)
	chars := append(append(append([]Char(nil), hello...), world...), next...)
	page := NewPage(chars)
	for _, tc := range []struct {
		name             string
		index            int
		wantStart, wantE int
	}{
		{name: "inside the first word", index: 2, wantStart: 0, wantE: 5},
		{name: "first character", index: 0, wantStart: 0, wantE: 5},
		{name: "last character before the gap", index: 4, wantStart: 0, wantE: 5},
		{name: "first character after the gap", index: 5, wantStart: 5, wantE: 10},
		{name: "last character of the line", index: 9, wantStart: 5, wantE: 10},
		{name: "first character of the next line", index: 10, wantStart: 10, wantE: 14},
		{name: "clamped past the end", index: 500, wantStart: 10, wantE: 14},
		{name: "clamped below zero", index: -7, wantStart: 0, wantE: 5},
	} {
		if start, end := page.WordAt(tc.index); start != tc.wantStart || end != tc.wantE {
			t.Errorf("%s: WordAt(%d) = (%d, %d), want (%d, %d)", tc.name, tc.index, start, end, tc.wantStart, tc.wantE)
		}
	}

	// Whitespace and unmapped characters are their own selection: there is no word to grow.
	chars = []Char{
		mkChar('a', 100, 200, adv, size),
		mkChar(' ', 110, 200, adv, size),
		mkChar('b', 120, 200, adv, size),
		mkChar(0, 130, 200, adv, size),
	}
	page = NewPage(chars)
	if start, end := page.WordAt(1); start != 1 || end != 2 {
		t.Errorf("on a space: WordAt(1) = (%d, %d), want (1, 2)", start, end)
	}
	if start, end := page.WordAt(3); start != 3 || end != 4 {
		t.Errorf("on an unmapped character: WordAt(3) = (%d, %d), want (3, 4)", start, end)
	}
	// The words on either side of those characters stop at them rather than absorbing them.
	if start, end := page.WordAt(0); start != 0 || end != 1 {
		t.Errorf("before a space: WordAt(0) = (%d, %d), want (0, 1)", start, end)
	}
	if start, end := page.WordAt(2); start != 2 || end != 3 {
		t.Errorf("between a space and an unmapped character: WordAt(2) = (%d, %d), want (2, 3)", start, end)
	}
	if start, end := NewPage(nil).WordAt(0); start != 0 || end != 0 {
		t.Errorf("empty page: WordAt = (%d, %d), want (0, 0)", start, end)
	}
	if start, end := (*Page)(nil).WordAt(3); start != 0 || end != 0 {
		t.Errorf("nil page: WordAt = (%d, %d), want (0, 0)", start, end)
	}
}

func TestLineAtCoversTheStream(t *testing.T) {
	line1, _ := mkWord("brown", 100, 200, 10, 12)
	line2, _ := mkWord("fox", 40, 214, 10, 12)
	page := NewPage(append(append([]Char(nil), line1...), line2...))
	for i := range page.Len() {
		start, end := page.LineAt(i)
		if i < len(line1) {
			if start != 0 || end != len(line1) {
				t.Errorf("LineAt(%d) = (%d, %d), want (0, %d)", i, start, end, len(line1))
			}
			continue
		}
		if start != len(line1) || end != page.Len() {
			t.Errorf("LineAt(%d) = (%d, %d), want (%d, %d)", i, start, end, len(line1), page.Len())
		}
	}
	// Len() is an insertion point, not a character, so it names the last line rather than nothing.
	if start, end := page.LineAt(page.Len()); start != len(line1) || end != page.Len() {
		t.Errorf("LineAt(Len()) = (%d, %d), want (%d, %d)", start, end, len(line1), page.Len())
	}
	if start, end := page.LineAt(-5); start != 0 || end != len(line1) {
		t.Errorf("LineAt(-5) = (%d, %d), want (0, %d)", start, end, len(line1))
	}
	if start, end := NewPage(nil).LineAt(0); start != 0 || end != 0 {
		t.Errorf("empty page: LineAt = (%d, %d), want (0, 0)", start, end)
	}
	if start, end := (*Page)(nil).LineAt(0); start != 0 || end != 0 {
		t.Errorf("nil page: LineAt = (%d, %d), want (0, 0)", start, end)
	}
	if got := (*Page)(nil).Quads(0, 5); got != nil {
		t.Errorf("nil page: Quads = %+v, want nil", got)
	}
	if got := (*Page)(nil).Len(); got != 0 {
		t.Errorf("nil page: Len = %d, want 0", got)
	}
}

// TestIndexAtSharesAGlyphAmongItsLetters pins the hit test over a glyph that spells several characters. The fillers
// all sit at the glyph's pen, so measured by their own geometry every caret position of the group is at its trailing
// edge — and a point just short of the pen would land between the first letter and the second, inside a shape the
// page shows no boundary in. The group instead shares the glyph's advance evenly: each letter owns an equal slice, a
// point in the trailing slice lands after all of the letters, and a caret can land between any two of them.
func TestIndexAtSharesAGlyphAmongItsLetters(t *testing.T) {
	const x0, y, adv, size = 100, 200, 10, 12
	// "ﬂow": the ligature records "f" with the glyph's whole advance and "l" as a filler at its pen.
	rest, _ := mkWord("ow", x0+adv, y, adv, size)
	two := append([]Char{mkChar('f', x0, y, adv, size), mkFiller('l', x0+adv, y, size)}, rest...)
	// "ﬃ": one glyph, three letters, two of them fillers.
	three := []Char{mkChar('f', x0, y, adv, size), mkFiller('f', x0+adv, y, size), mkFiller('i', x0+adv, y, size)}
	for _, tc := range []struct {
		name  string
		chars []Char
		at    float32 // In advances from the glyph's origin.
		want  int
	}{
		{name: "two letters: left of the glyph", chars: two, at: -0.5, want: 0},
		{name: "two letters: first letter's slice", chars: two, at: 0.2, want: 0},
		{name: "two letters: between the letters", chars: two, at: 0.4, want: 1},
		{name: "two letters: second letter's slice", chars: two, at: 0.6, want: 1},
		{name: "two letters: just short of the pen", chars: two, at: 0.99, want: 2},
		{name: "two letters: next glyph's leading half", chars: two, at: 1.2, want: 2},
		{name: "two letters: next glyph's trailing half", chars: two, at: 1.8, want: 3},
		{name: "three letters: first slice", chars: three, at: 0.1, want: 0},
		{name: "three letters: second slice", chars: three, at: 0.3, want: 1},
		{name: "three letters: third slice", chars: three, at: 0.6, want: 2},
		{name: "three letters: just short of the pen", chars: three, at: 0.95, want: 3},
		{name: "three letters: past the glyph", chars: three, at: 3, want: 3},
	} {
		if got := NewPage(tc.chars).IndexAt(gfx.Point{X: x0 + tc.at*adv, Y: y}); got != tc.want {
			t.Errorf("%s: IndexAt(%v advances) = %d, want %d", tc.name, tc.at, got, tc.want)
		}
	}
}
