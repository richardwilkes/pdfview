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
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/richardwilkes/pdfview/internal/gfx"
)

// mkChar builds one axis-aligned character in top-left/y-down device space with an 0.8/-0.2 em vertical extent, the
// shape the corpus text overwhelmingly uses.
func mkChar(r rune, x, y, adv, size float32) Char {
	top, bottom := y-0.8*size, y+0.2*size
	return Char{
		Quad: gfx.Quad{
			UL: gfx.Point{X: x, Y: top},
			UR: gfx.Point{X: x + adv, Y: top},
			LL: gfx.Point{X: x, Y: bottom},
			LR: gfx.Point{X: x + adv, Y: bottom},
		},
		Origin: gfx.Point{X: x, Y: y},
		End:    gfx.Point{X: x + adv, Y: y},
		Rune:   r,
		Size:   size,
		Axis:   true,
	}
}

// mkMirroredChar builds one vertically-mirrored axis-aligned character (Trm.B == 0 && Trm.C == 0 with Trm.D > 0, so the
// ascender maps below the descender in y-down device space and the quad's UL.Y exceeds its LL.Y).
func mkMirroredChar(r rune, x, y, adv, size float32) Char {
	c := mkChar(r, x, y, adv, size)
	top, bottom := y+0.8*size, y-0.2*size
	c.Quad.UL.Y, c.Quad.UR.Y = top, top
	c.Quad.LL.Y, c.Quad.LR.Y = bottom, bottom
	return c
}

// mkMirroredWord is mkWord for vertically-mirrored characters.
func mkMirroredWord(s string, x, y, adv, size float32) (chars []Char, endX float32) {
	for _, r := range s {
		chars = append(chars, mkMirroredChar(r, x, y, adv, size))
		x += adv
	}
	return chars, x
}

// mkWord lays out s as consecutive characters starting at (x, y), advancing adv per character, and returns the
// characters plus the x just past the word.
func mkWord(s string, x, y, adv, size float32) (chars []Char, endX float32) {
	for _, r := range s {
		chars = append(chars, mkChar(r, x, y, adv, size))
		x += adv
	}
	return chars, x
}

func TestSearchCaseFoldAndBounds(t *testing.T) {
	chars, endX := mkWord("Hello", 100, 200, 10, 12)
	got := searchChars(chars, "hELLo", 100)
	if len(got) != 1 {
		t.Fatalf("expected 1 quad, got %d", len(got))
	}
	want := gfx.Quad{
		UL: gfx.Point{X: 100, Y: 200 - 0.8*12},
		UR: gfx.Point{X: endX, Y: 200 - 0.8*12},
		LL: gfx.Point{X: 100, Y: 200 + 0.2*12},
		LR: gfx.Point{X: endX, Y: 200 + 0.2*12},
	}
	if got[0] != want {
		t.Fatalf("quad = %+v, want %+v", got[0], want)
	}
}

func TestSearchGapAsWordSpace(t *testing.T) {
	// No space character between the words; a horizontal gap of at least gapSpaceEm em must satisfy the needle's space,
	// and a smaller gap must not.
	first, endX := mkWord("Kerned", 100, 200, 10, 20)
	wide, _ := mkWord("Text", endX+gapSpaceEm*20, 200, 10, 20)
	if got := searchChars(append(append([]Char(nil), first...), wide...), "Kerned Text", 100); len(got) != 1 {
		t.Fatalf("gap >= threshold: expected 1 quad, got %d", len(got))
	}
	narrow, _ := mkWord("Text", endX+gapSpaceEm*20*0.9, 200, 10, 20)
	if got := searchChars(append(append([]Char(nil), first...), narrow...), "Kerned Text", 100); len(got) != 0 {
		t.Fatalf("gap < threshold: expected 0 quads, got %d", len(got))
	}
}

func TestSearchWrappedMatch(t *testing.T) {
	// "brown" ends line 1 and "fox" starts line 2: the needle space matches the line break and the match yields one
	// quad per line, first line first.
	line1, _ := mkWord("brown", 100, 200, 10, 12)
	line2, _ := mkWord("fox", 40, 214, 10, 12)
	chars := append(append([]Char(nil), line1...), line2...)
	got := searchChars(chars, "brown fox", 100)
	if len(got) != 2 {
		t.Fatalf("expected 2 quads, got %d", len(got))
	}
	if got[0].UL.Y >= got[1].UL.Y {
		t.Fatalf("expected first-line quad first: got tops %v then %v", got[0].UL.Y, got[1].UL.Y)
	}
	// Without a needle space at the break, the word must not silently span the line break.
	if got = searchChars(chars, "brownfox", 100); len(got) != 0 {
		t.Fatalf("expected 0 quads for a wordless wrap, got %d", len(got))
	}
}

func TestSearchExtentSplit(t *testing.T) {
	// A 40-pt space amid 20-pt words diverges beyond extentSplitFraction of the quad height, so one single-line match
	// yields three quads (word, oversized space, word) — the hit-quad-split.pdf behavior.
	alpha, endX := mkWord("alpha", 100, 200, 10, 20)
	space := mkChar(' ', endX, 200, 20, 40)
	beta, _ := mkWord("beta", endX+20, 200, 10, 20)
	chars := append(append(append([]Char(nil), alpha...), space), beta...)
	got := searchChars(chars, "alpha beta", 100)
	if len(got) != 3 {
		t.Fatalf("expected 3 quads, got %d: %+v", len(got), got)
	}
	// A same-size space merges instead, keeping the FIRST character's vertical extent for the whole quad.
	chars = append(append(append([]Char(nil), alpha...), mkChar(' ', endX, 200, 10, 20)), beta...)
	if got = searchChars(chars, "alpha beta", 100); len(got) != 1 {
		t.Fatalf("expected 1 quad for uniform extents, got %d", len(got))
	}
}

func TestSearchMirroredExtent(t *testing.T) {
	// Vertically-mirrored text has a negative bottom-top extent; the merge threshold must use its magnitude or every
	// character flushes into its own quad.
	chars, endX := mkMirroredWord("alpha", 100, 200, 10, 20)
	got := searchChars(chars, "alpha", 100)
	if len(got) != 1 {
		t.Fatalf("expected 1 quad for a uniform mirrored run, got %d: %+v", len(got), got)
	}
	top, bottom := float32(200+0.8*20), float32(200-0.2*20)
	want := gfx.Quad{
		UL: gfx.Point{X: 100, Y: top},
		UR: gfx.Point{X: endX, Y: top},
		LL: gfx.Point{X: 100, Y: bottom},
		LR: gfx.Point{X: endX, Y: bottom},
	}
	if got[0] != want {
		t.Fatalf("quad = %+v, want %+v", got[0], want)
	}
	// The extent-split rule still applies in the mirrored orientation: an oversized space yields three quads.
	space := mkMirroredChar(' ', endX, 200, 20, 40)
	beta, _ := mkMirroredWord("beta", endX+20, 200, 10, 20)
	chars = append(append(append([]Char(nil), chars...), space), beta...)
	if got = searchChars(chars, "alpha beta", 100); len(got) != 3 {
		t.Fatalf("expected 3 quads, got %d: %+v", len(got), got)
	}
}

func TestSearchEmissionOrderAndNoOverlap(t *testing.T) {
	// Hits come back in emission order, not spatial order: the run at y=500 was emitted first.
	low, _ := mkWord("xx", 100, 500, 10, 12)
	high, _ := mkWord("xx", 100, 100, 10, 12)
	chars := append(append([]Char(nil), low...), high...)
	got := searchChars(chars, "xx", 100)
	if len(got) != 2 || got[0].UL.Y < got[1].UL.Y {
		t.Fatalf("expected 2 quads in emission order (y=500 first), got %+v", got)
	}
	// Matches never overlap: "aaa" contains one "aa" match, not two.
	aaa, _ := mkWord("aaa", 100, 200, 10, 12)
	if got = searchChars(aaa, "aa", 100); len(got) != 1 {
		t.Fatalf("expected 1 non-overlapping match, got %d", len(got))
	}
}

func TestSearchBudget(t *testing.T) {
	var chars []Char
	for i := range 5 {
		word, _ := mkWord("ab", 100, 100+float32(i)*20, 10, 12)
		chars = append(chars, word...)
	}
	if got := searchChars(chars, "ab", 3); len(got) != 3 {
		t.Fatalf("expected exactly 3 quads under a budget of 3, got %d", len(got))
	}
	// A match that would overflow the budget is truncated mid-match and the search stops: the wrapped match below
	// produces two quads but only the first fits.
	line1, _ := mkWord("brown", 100, 200, 10, 12)
	line2, _ := mkWord("fox", 40, 214, 10, 12)
	if got := searchChars(append(append([]Char(nil), line1...), line2...), "brown fox", 1); len(got) != 1 {
		t.Fatalf("expected 1 quad under a budget of 1, got %d", len(got))
	}
	if got := searchChars(chars, "ab", 0); got != nil {
		t.Fatalf("expected nil for a zero budget, got %d quads", len(got))
	}
}

// TestFoldEqualMatchesStringsEqualFold pins foldEqual to the strings.EqualFold spelling it replaced: over every rune,
// its whole simple-folding orbit must compare equal and the next rune (outside the orbit) must not, with each verdict
// cross-checked against EqualFold on the single-rune strings.
func TestFoldEqualMatchesStringsEqualFold(t *testing.T) {
	check := func(a, b rune) {
		t.Helper()
		if got, want := foldEqual(a, b), strings.EqualFold(string(a), string(b)); got != want {
			t.Fatalf("foldEqual(%#U, %#U) = %v, want %v", a, b, got, want)
		}
	}
	for a := rune(0); a <= unicode.MaxRune; a++ {
		if !utf8.ValidRune(a) {
			continue
		}
		check(a, a)
		inOrbit := false
		for b := unicode.SimpleFold(a); b != a; b = unicode.SimpleFold(b) {
			check(a, b)
			if !foldEqual(a, b) {
				t.Fatalf("foldEqual(%#U, %#U) = false, want true for an orbit member", a, b)
			}
			if b == a+1 {
				inOrbit = true
			}
		}
		if b := a + 1; !inOrbit && utf8.ValidRune(b) {
			check(a, b)
		}
	}
}

// TestFoldEqualInvalidRunes pins the invalid-rune handling: string(r) replaced negative, surrogate, and out-of-range
// runes with U+FFFD, so the extracted character carrying one still matches a U+FFFD needle rune (which is what decoding
// invalid UTF-8 in the needle yields) exactly as before.
func TestFoldEqualInvalidRunes(t *testing.T) {
	for _, r := range []rune{-1, -0x10000, 0xD800, 0xDFFF, unicode.MaxRune + 1, 0x7FFFFFFF} {
		if !foldEqual(r, utf8.RuneError) {
			t.Errorf("foldEqual(%d, RuneError) = false, want true", r)
		}
		if !foldEqual(utf8.RuneError, r) {
			t.Errorf("foldEqual(RuneError, %d) = false, want true", r)
		}
		if foldEqual(r, 'a') {
			t.Errorf("foldEqual(%d, 'a') = true, want false", r)
		}
	}
}

// TestSearchNonASCIICaseFold exercises folding pairs that are not a simple ASCII 0x20 apart: the Kelvin sign folds with
// k/K and the long s folds with s/S, both three-member orbits.
func TestSearchNonASCIICaseFold(t *testing.T) {
	chars, _ := mkWord("Kelvin\u017F", 100, 200, 10, 12) // A word ending in a long s.
	// The plain forms, then the Kelvin sign standing in for the leading K.
	for _, needle := range []string{"kelvins", "KELVINS", "Kelvins", "\u212Aelvins"} {
		if got := searchChars(chars, needle, 100); len(got) != 1 {
			t.Errorf("needle %q: expected 1 quad, got %d", needle, len(got))
		}
	}
	if got := searchChars(chars, "kelvint", 100); len(got) != 0 {
		t.Errorf("expected 0 quads for a non-folding mismatch, got %d", len(got))
	}
}

func TestSearchDegenerateNeedles(t *testing.T) {
	chars, _ := mkWord("Hello world", 100, 200, 10, 12)
	for _, needle := range []string{"", " ", " \t\n"} {
		if got := searchChars(chars, needle, 100); got != nil {
			t.Errorf("needle %q: expected nil, got %d quads", needle, len(got))
		}
	}
}

func TestSearchUnmappedRuneBreaksMatch(t *testing.T) {
	chars, endX := mkWord("ab", 100, 200, 10, 12)
	chars = append(chars, mkChar(0, endX, 200, 10, 12)) // No Unicode mapping: never matches anything.
	word, _ := mkWord("cd", endX+10, 200, 10, 12)
	chars = append(chars, word...)
	if got := searchChars(chars, "abcd", 100); len(got) != 0 {
		t.Fatalf("expected 0 quads across an unmapped rune, got %d", len(got))
	}
	if got := searchChars(chars, "ab", 100); len(got) != 1 {
		t.Fatalf("expected the prefix to still match, got %d quads", len(got))
	}
}

func TestSearchRotatedRun(t *testing.T) {
	// A 90°-rotated run: baseline advances through device y, so the perpendicular line-break test must keep it a single
	// line, and the non-axis assembly spans first to last corner.
	chars := make([]Char, 0, 7)
	x, y := float32(100), float32(400)
	for _, r := range "Rotated" {
		size := float32(12)
		chars = append(chars, Char{
			Quad: gfx.Quad{
				UL: gfx.Point{X: x - 0.8*size, Y: y},
				UR: gfx.Point{X: x - 0.8*size, Y: y - 10},
				LL: gfx.Point{X: x + 0.2*size, Y: y},
				LR: gfx.Point{X: x + 0.2*size, Y: y - 10},
			},
			Origin: gfx.Point{X: x, Y: y},
			End:    gfx.Point{X: x, Y: y - 10},
			Rune:   r,
			Size:   size,
		})
		y -= 10
	}
	got := searchChars(chars, "Rotated", 100)
	if len(got) != 1 {
		t.Fatalf("expected 1 quad, got %d", len(got))
	}
	want := gfx.Quad{
		UL: chars[0].Quad.UL,
		UR: chars[len(chars)-1].Quad.UR,
		LL: chars[0].Quad.LL,
		LR: chars[len(chars)-1].Quad.LR,
	}
	if got[0] != want {
		t.Fatalf("quad = %+v, want %+v", got[0], want)
	}
}
