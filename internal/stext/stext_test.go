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
	"image/color"
	"runtime"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
	"weak"

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
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

// TestSearchLeadingSpaceNeedle pins a needle whose first rune is whitespace: at the match's start the whitespace branch
// consumes no characters, so the synthesized alternatives must be tested against the predecessor of the starting
// character.
func TestSearchLeadingSpaceNeedle(t *testing.T) {
	first, endX := mkWord("Kerned", 100, 200, 10, 20)
	wide, wideEndX := mkWord("Text", endX+gapSpaceEm*20, 200, 10, 20)
	got := searchChars(append(append([]Char(nil), first...), wide...), " Text", 100)
	if len(got) != 1 {
		t.Fatalf("gap >= threshold: expected 1 quad, got %d", len(got))
	}
	// The quad covers only the matched characters; the gap itself contributes nothing.
	want := gfx.Quad{
		UL: gfx.Point{X: endX + gapSpaceEm*20, Y: 200 - 0.8*20},
		UR: gfx.Point{X: wideEndX, Y: 200 - 0.8*20},
		LL: gfx.Point{X: endX + gapSpaceEm*20, Y: 200 + 0.2*20},
		LR: gfx.Point{X: wideEndX, Y: 200 + 0.2*20},
	}
	if got[0] != want {
		t.Fatalf("quad = %+v, want %+v", got[0], want)
	}
	narrow, _ := mkWord("Text", endX+gapSpaceEm*20*0.9, 200, 10, 20)
	if got = searchChars(append(append([]Char(nil), first...), narrow...), " Text", 100); len(got) != 0 {
		t.Fatalf("gap < threshold: expected 0 quads, got %d", len(got))
	}
	// A line break satisfies it too, and the empty leading segment yields no quad of its own.
	line1, _ := mkWord("brown", 100, 200, 10, 12)
	line2, line2EndX := mkWord("fox", 40, 214, 10, 12)
	if got = searchChars(append(append([]Char(nil), line1...), line2...), " fox", 100); len(got) != 1 {
		t.Fatalf("line break: expected 1 quad, got %d: %+v", len(got), got)
	}
	if got[0].UL.X != 40 || got[0].UR.X != line2EndX {
		t.Fatalf("line-break quad spans %v..%v, want 40..%v", got[0].UL.X, got[0].UR.X, line2EndX)
	}
	spaced, _ := mkWord("a b", 100, 200, 10, 12)
	if got = searchChars(spaced, " b", 100); len(got) != 1 {
		t.Fatalf("space character: expected 1 quad, got %d", len(got))
	}
	// With no predecessor at all, a leading-space needle fails at the start of the stream.
	if got = searchChars(wide, " Text", 100); len(got) != 0 {
		t.Fatalf("stream start: expected 0 quads, got %d", len(got))
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

// TestSearchQuadMergeVerticalFuzz pins add_quad's vertical fuzz against an oversized inter-word space. A 40-pt space
// amid 20-pt words puts its lower-left corner 4 pt below the corner the open quad ends at, exactly the space's own
// quadVFuzzEm (0.1 × 40), so it does not merge; its 40-pt right edge then sits 4 pt from the following 20-pt word
// against a fuzz of 2, so the single-line match yields three quads (word, space, word), as hit-quad-split.pdf does.
func TestSearchQuadMergeVerticalFuzz(t *testing.T) {
	alpha, endX := mkWord("alpha", 100, 200, 10, 20)
	space := mkChar(' ', endX, 200, 20, 40)
	beta, betaEndX := mkWord("beta", endX+20, 200, 10, 20)
	chars := append(append(append([]Char(nil), alpha...), space), beta...)
	got := searchChars(chars, "alpha beta", 100)
	if len(got) != 3 {
		t.Fatalf("expected 3 quads, got %d: %+v", len(got), got)
	}
	// Each quad covers exactly the characters that merged into it, in stream order.
	for i, want := range [3][2]float32{{100, endX}, {endX, endX + 20}, {endX + 20, betaEndX}} {
		if got[i].UL.X != want[0] || got[i].UR.X != want[1] {
			t.Errorf("quad %d spans %v..%v, want %v..%v", i, got[i].UL.X, got[i].UR.X, want[0], want[1])
		}
	}
	// A same-size space has corners identical to its neighbors', so the whole match merges into one quad.
	chars = append(append(append([]Char(nil), alpha...), mkChar(' ', endX, 200, 10, 20)), beta...)
	if got = searchChars(chars, "alpha beta", 100); len(got) != 1 {
		t.Fatalf("expected 1 quad for uniform extents, got %d: %+v", len(got), got)
	}
}

// TestSearchQuadRaisedCharacter brackets the vertical fuzz exactly over a text rise (an exponent, a footnote marker, or
// a formula's digit). The comparison is strict, so a character shifted by exactly quadVFuzzEm of its em splits the hit
// while one shifted a hair less does not: the boundary MuPDF lands on, which hit-quad-rise.pdf holds the corpus to.
func TestSearchQuadRaisedCharacter(t *testing.T) {
	for _, tc := range []struct {
		want int
		rise float32
	}{
		{rise: 0.05 * 20, want: 1},  // Well inside the fuzz.
		{rise: 0.09 * 20, want: 1},  // Just inside it.
		{rise: 0.1 * 20, want: 3},   // Exactly at it: < is strict, so the digit and the O each start a quad.
		{rise: 0.25 * 20, want: 3},  // Well past it.
		{rise: -0.1 * 20, want: 3},  // Dropped rather than raised: the distances are absolute.
		{rise: -0.09 * 20, want: 1}, // And bracketed the same way below the baseline.
	} {
		chars, _ := mkWord("H", 100, 200, 10, 20)
		chars = append(chars, mkChar('2', 110, 200-tc.rise, 10, 20))
		tail, _ := mkWord("O", 120, 200, 10, 20)
		if got := searchChars(append(chars, tail...), "H2O", 100); len(got) != tc.want {
			t.Errorf("rise %v: expected %d quads, got %d: %+v", tc.rise, tc.want, len(got), got)
		}
	}
	// The fuzz scales by the size of the character being merged, not by the size of the run it is joining, so a smaller
	// character is held to a tighter bound: a 12-pt digit sitting flat on the same baseline as 20-pt letters stands
	// 6.4 pt off at the top and 1.6 pt off at the bottom, and even the 1.6 is past its own 1.2-pt fuzz.
	chars, _ := mkWord("H", 100, 200, 10, 20)
	chars = append(chars, mkChar('2', 110, 200, 6, 12))
	tail, _ := mkWord("O", 116, 200, 10, 20)
	if got := searchChars(append(chars, tail...), "H2O", 100); len(got) != 3 {
		t.Fatalf("small digit: expected 3 quads, got %d: %+v", len(got), got)
	}
}

// TestSearchQuadWordGapBridged covers the synthesized space (see segmentQuads): without it a gap of quadHFuzzEm or more
// would split a hit MuPDF reports whole, as text-std14's "Kerned Text" needle would.
func TestSearchQuadWordGapBridged(t *testing.T) {
	// A 0.7 em gap: past the 0.5 em horizontal fuzz, yet the hit is one quad reaching from alpha's leading corners to
	// beta's trailing ones, the gap included.
	alpha, endX := mkWord("alpha", 100, 200, 10, 20)
	beta, betaEndX := mkWord("beta", endX+0.7*20, 200, 10, 20)
	got := searchChars(append(append([]Char(nil), alpha...), beta...), "alpha beta", 100)
	if len(got) != 1 {
		t.Fatalf("expected 1 quad across a word gap, got %d: %+v", len(got), got)
	}
	want := gfx.Quad{
		UL: gfx.Point{X: 100, Y: 200 - 0.8*20},
		UR: gfx.Point{X: betaEndX, Y: 200 - 0.8*20},
		LL: gfx.Point{X: 100, Y: 200 + 0.2*20},
		LR: gfx.Point{X: betaEndX, Y: 200 + 0.2*20},
	}
	if got[0] != want {
		t.Fatalf("quad = %+v, want %+v", got[0], want)
	}
	// A kerning gap too narrow to read as a word space gets no synthesized space, and add_quad's own horizontal fuzz
	// absorbs it: the two halves still merge, with no needle space involved at all.
	kerned, kernedEndX := mkWord("beta", endX+0.15*20, 200, 10, 20)
	if got = searchChars(append(append([]Char(nil), alpha...), kerned...), "alphabeta", 100); len(got) != 1 {
		t.Fatalf("expected 1 quad across a kerning gap, got %d: %+v", len(got), got)
	}
	if got[0].UL.X != 100 || got[0].UR.X != kernedEndX {
		t.Fatalf("kerned quad spans %v..%v, want 100..%v", got[0].UL.X, got[0].UR.X, kernedEndX)
	}
	// When the vertical test fails AT the gap, the new quad opens on the synthesized space — at the pen the previous
	// character left behind — so it covers the gap rather than starting at the character past it. Here the 40-pt word's
	// ascender stands 16 pt above the 20-pt word's against a fuzz of 4.
	big, bigEndX := mkWord("beta", endX+0.7*20, 200, 20, 40)
	if got = searchChars(append(append([]Char(nil), alpha...), big...), "alpha beta", 100); len(got) != 2 {
		t.Fatalf("expected 2 quads across a size change at the gap, got %d: %+v", len(got), got)
	}
	if got[1].UL.X != endX || got[1].UR.X != bigEndX {
		t.Fatalf("second quad spans %v..%v, want %v..%v (from the pen)", got[1].UL.X, got[1].UR.X, endX, bigEndX)
	}
	if got[1].UL.Y != 200-0.8*40 {
		t.Fatalf("second quad's top = %v, want the incoming word's %v", got[1].UL.Y, 200-0.8*40)
	}
}

// TestSearchQuadTrailingCorners pins that a merge replaces the open quad's trailing corners with the merged character's
// rather than unioning the two, so the quad carries the last character's height on its right edge. Two words of
// different sizes on one baseline show the difference, and the fuzz scaling by the incoming size.
func TestSearchQuadTrailingCorners(t *testing.T) {
	small, endX := mkWord("ab", 100, 200, 10, 20)
	// 24 pt against 20: the tops stand 3.2 pt apart against the incoming character's fuzz of 2.4, so the words split.
	big, _ := mkWord("cd", endX, 200, 12, 24)
	if got := searchChars(append(append([]Char(nil), small...), big...), "abcd", 100); len(got) != 2 {
		t.Fatalf("24 pt after 20 pt: expected 2 quads, got %d: %+v", len(got), got)
	}
	// 22 pt against 20: 1.6 pt at the top and 0.4 pt at the bottom, both inside the incoming fuzz of 2.2, so they
	// merge — and the merged quad is lopsided, 20 pt tall on the left and 22 pt tall on the right.
	near, _ := mkWord("cd", endX, 200, 11, 22)
	got := searchChars(append(append([]Char(nil), small...), near...), "abcd", 100)
	if len(got) != 1 {
		t.Fatalf("22 pt after 20 pt: expected 1 quad, got %d: %+v", len(got), got)
	}
	last := near[len(near)-1].Quad
	want := gfx.Quad{UL: small[0].Quad.UL, UR: last.UR, LL: small[0].Quad.LL, LR: last.LR}
	if got[0] != want {
		t.Fatalf("quad = %+v, want %+v", got[0], want)
	}
	if got[0].UL.Y == got[0].UR.Y {
		t.Fatalf("quad %+v has a level top edge; a union would, but replacing the trailing corners must not", got[0])
	}
	// 22.5 pt against 20 is the case that separates the two possible scalings: the tops stand exactly 2 pt apart, which
	// is inside the incoming character's fuzz of 2.25 but not inside the 2.0 the preceding character's size would have
	// given.
	scaled, _ := mkWord("cd", endX, 200, 11, 22.5)
	if got = searchChars(append(append([]Char(nil), small...), scaled...), "abcd", 100); len(got) != 1 {
		t.Fatalf("22.5 pt after 20 pt: expected 1 quad, got %d: %+v", len(got), got)
	}
}

func TestSearchMirroredExtent(t *testing.T) {
	// Vertically mirrored text puts the ascender below the descender in y-down space; a uniform mirrored run must still
	// come back as one quad rather than one per character.
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
	// The vertical fuzz still splits in the mirrored orientation: an oversized space yields three quads.
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

// TestFoldEqualMatchesStringsEqualFold cross-checks foldEqual against strings.EqualFold on single-rune strings: over
// every rune, its whole simple-folding orbit must compare equal and the next rune (outside the orbit) must not.
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

// TestFoldEqualInvalidRunes pins the invalid-rune handling: negative, surrogate, and out-of-range runes fold as U+FFFD,
// as string(r) would spell them, so an extracted character carrying one still matches a U+FFFD needle rune (what
// decoding invalid UTF-8 in the needle yields).
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
	// A 90°-rotated run: the baseline advances through device y, so the perpendicular line-break test must keep it one
	// line, and the one merge rule must walk it into a single first-to-last-corner quad.
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

// mkRun builds a one-glyph run for r at (x, y) with the given em size. The font is metric-free (zero ascender and
// descender), which is all the recording path needs: the tests below count characters, not quads.
func mkRun(r rune, x, y, size float32) *device.TextRun {
	return &device.TextRun{
		Font: &font.Font{},
		Glyphs: []device.Glyph{{
			Trm:     gfx.Matrix{A: size, D: size, E: x, F: y},
			Unicode: r,
			Advance: 0.5,
		}},
	}
}

// runes returns the recorded characters' runes, as a string, for comparing recordings compactly.
func runes(chars []Char) string {
	var sb strings.Builder
	for _, c := range chars {
		sb.WriteRune(c.Rune)
	}
	return sb.String()
}

func TestRecordDedupsRepeatedDeliveries(t *testing.T) {
	// Render mode 6 delivers one run through fill, stroke and clip back-to-back; mode 3 delivers through IgnoreText.
	dev := New()
	first := mkRun('A', 100, 200, 12)
	dev.FillText(first, device.Paint{})
	dev.StrokeText(first, &gfx.StrokeParams{}, device.Paint{})
	dev.ClipText(first)
	second := mkRun('B', 112, 200, 12)
	dev.IgnoreText(second)
	dev.IgnoreText(second)
	if got := runes(dev.Chars()); got != "AB" {
		t.Fatalf("recorded %q, want %q", got, "AB")
	}
}

func TestRecordDedupsAcrossMaskReplay(t *testing.T) {
	// A run painted under a soft mask: the interpreter replays the mask ahead of the fill and again ahead of the
	// stroke, so mask-body text arrives between the two deliveries of the same run.
	dev := New()
	run := mkRun('A', 100, 200, 12)
	maskRun := mkRun('m', 10, 20, 6)
	dev.BeginMask(gfx.Rect{}, true, color.NRGBA{}, nil)
	dev.FillText(maskRun, device.Paint{})
	dev.StrokeText(maskRun, &gfx.StrokeParams{}, device.Paint{})
	dev.EndMask()
	dev.FillText(run, device.Paint{})
	dev.PopMask()
	secondReplay := mkRun('m', 10, 20, 6)
	dev.BeginMask(gfx.Rect{}, true, color.NRGBA{}, nil)
	dev.FillText(secondReplay, device.Paint{})
	dev.EndMask()
	dev.StrokeText(run, &gfx.StrokeParams{}, device.Paint{})
	dev.ClipText(run)
	dev.PopMask()
	if got := runes(dev.Chars()); got != "mAm" {
		t.Fatalf("recorded %q, want %q", got, "mAm")
	}
}

func TestRecordDedupsAcrossNestedMasks(t *testing.T) {
	// The mask body may itself paint under a soft mask, so the saved run identities nest.
	dev := New()
	outer := mkRun('A', 100, 200, 12)
	middle := mkRun('m', 10, 20, 6)
	inner := mkRun('i', 1, 2, 3)
	dev.FillText(outer, device.Paint{})
	dev.BeginMask(gfx.Rect{}, true, color.NRGBA{}, nil)
	dev.BeginMask(gfx.Rect{}, true, color.NRGBA{}, nil)
	dev.FillText(inner, device.Paint{})
	dev.StrokeText(inner, &gfx.StrokeParams{}, device.Paint{})
	dev.EndMask()
	dev.FillText(middle, device.Paint{})
	dev.StrokeText(middle, &gfx.StrokeParams{}, device.Paint{})
	dev.PopMask()
	dev.EndMask()
	dev.StrokeText(outer, &gfx.StrokeParams{}, device.Paint{})
	dev.PopMask()
	if got := runes(dev.Chars()); got != "Aim" {
		t.Fatalf("recorded %q, want %q", got, "Aim")
	}
}

func TestRecordRetainsOnlyTheCurrentRun(t *testing.T) {
	// The device must not pin the page's glyph stream: only the run needed to spot the next repeated delivery stays
	// reachable through it.
	const count = 16
	dev := New()
	refs := make([]weak.Pointer[device.TextRun], 0, count)
	for i := range count {
		run := mkRun(rune('a'+i), float32(i)*10, 200, 12)
		refs = append(refs, weak.Make(run))
		dev.FillText(run, device.Paint{})
	}
	if len(dev.Chars()) != count {
		t.Fatalf("recorded %d characters, want %d", len(dev.Chars()), count)
	}
	runtime.GC()
	runtime.GC()
	live := 0
	for _, ref := range refs {
		if ref.Value() != nil {
			live++
		}
	}
	if live > 1 {
		t.Fatalf("%d of %d runs still reachable, want at most 1", live, count)
	}
}

// TestRecordCapsCharacters pins the maxChars cap against the shape that motivates it (see maxChars).
func TestRecordCapsCharacters(t *testing.T) {
	const perRun = 4096
	dev := New()
	glyphs := make([]device.Glyph, perRun)
	for i := range glyphs {
		glyphs[i] = device.Glyph{Trm: gfx.Matrix{A: 12, D: 12, E: float32(i) * 6, F: 200}, Unicode: 'a', Advance: 0.5}
	}
	// A fresh run pointer per delivery: the dedup guard keys on identity, so each of these records its glyphs.
	for range maxChars/perRun + 4 {
		dev.FillText(&device.TextRun{Font: &font.Font{}, Glyphs: glyphs}, device.Paint{})
	}
	if got := len(dev.Chars()); got != maxChars {
		t.Fatalf("recorded %d characters, want the cap of %d", got, maxChars)
	}
	// The cap must not disturb what was recorded before it: the last recorded run is still intact and searchable.
	if got := dev.Chars()[maxChars-1].Rune; got != 'a' {
		t.Errorf("the last recorded character is %q, want 'a'", got)
	}
	// A run arriving past the cap is dropped whole rather than partially, and recording stays a no-op afterwards.
	dev.FillText(&device.TextRun{Font: &font.Font{}, Glyphs: glyphs}, device.Paint{})
	if got := len(dev.Chars()); got != maxChars {
		t.Errorf("a post-cap run grew the recording to %d characters", got)
	}
}

// mkLigatureRun builds a one-glyph run whose glyph draws a ligature: lead starts the extraction and rest follows it, as
// a one-to-many /ToUnicode mapping supplies. Like mkRun the font is metric-free, so the recorded quads have no height;
// TestRecordFillerGeometry and the text-ligature goldens pin the vertical extent.
func mkLigatureRun(lead rune, rest []rune, x, y, size, adv float32) *device.TextRun {
	return &device.TextRun{
		Font: &font.Font{},
		Glyphs: []device.Glyph{{
			Trm:     gfx.Matrix{A: size, D: size, E: x, F: y},
			Unicode: lead,
			Rest:    rest,
			Advance: adv,
		}},
	}
}

// TestRecordSpellsOutOneToManyToUnicode pins that a glyph whose /ToUnicode target holds several runes contributes one
// character per rune, not just the first: otherwise the fl glyph of "Reflect" contributes only its f and no search for
// the word finds the page (richardwilkes/gcs#1092).
func TestRecordSpellsOutOneToManyToUnicode(t *testing.T) {
	dev := New()
	dev.FillText(mkLigatureRun('f', []rune{'l'}, 100, 200, 10, 0.6), device.Paint{})
	if got := runes(dev.Chars()); got != "fl" {
		t.Fatalf("recorded %q, want %q", got, "fl")
	}
	chars := dev.Chars()
	base, filler := chars[0], chars[1]
	if filler.Origin != base.End || filler.End != base.End {
		t.Errorf("filler spans %+v-%+v, want both at the base's end %+v", filler.Origin, filler.End, base.End)
	}
	if filler.Size != base.Size || filler.Axis != base.Axis {
		t.Errorf("filler size/axis = %v/%v, want the base's %v/%v", filler.Size, filler.Axis, base.Size, base.Axis)
	}
	// The word the glyph spells is findable, and the leading rune alone still is.
	if got := searchChars(chars, "fl", 8); len(got) != 1 {
		t.Errorf("searching %q found %d quads, want 1", "fl", len(got))
	}
	if got := searchChars(chars, "f", 8); len(got) != 1 {
		t.Errorf("searching %q found %d quads, want 1", "f", len(got))
	}
}

// TestRecordFillerGeometry pins the filler quad: zero-width at the base's advance end, still spanning the font's full
// ascender-to-descender height.
func TestRecordFillerGeometry(t *testing.T) {
	dev := New()
	base := mkChar('f', 100, 200, 6, 10)
	dev.chars = append(dev.chars, base)
	dev.recordFillers(&dev.chars[0], []rune{'l'})
	if len(dev.chars) != 2 {
		t.Fatalf("recorded %d characters, want 2", len(dev.chars))
	}
	want := gfx.Quad{
		UL: gfx.Point{X: base.End.X, Y: base.Quad.UL.Y},
		UR: gfx.Point{X: base.End.X, Y: base.Quad.UL.Y},
		LL: gfx.Point{X: base.End.X, Y: base.Quad.LL.Y},
		LR: gfx.Point{X: base.End.X, Y: base.Quad.LL.Y},
	}
	if got := dev.chars[1].Quad; got != want {
		t.Errorf("filler quad = %+v, want %+v", got, want)
	}
}

// TestRecordDecomposesLigatureCodePoints covers the other route to several letters per glyph: the Unicode
// alphabetic-presentation ligatures, which a glyph named "fl" reaches through the AGL with no /ToUnicode involved.
func TestRecordDecomposesLigatureCodePoints(t *testing.T) {
	for _, tc := range []struct {
		want string
		r    rune
	}{
		{r: 0xFB00, want: "ff"},
		{r: 0xFB01, want: "fi"},
		{r: 0xFB02, want: "fl"},
		{r: 0xFB03, want: "ffi"},
		{r: 0xFB04, want: "ffl"},
		{r: 0xFB05, want: "st"},
		{r: 0xFB06, want: "st"},
		{r: 'A', want: "A"},    // Every other rune passes through untouched.
		{r: 0xFB07, want: "﬇"}, // Including the unassigned code point just past the set.
	} {
		dev := New()
		dev.FillText(mkLigatureRun(tc.r, nil, 100, 200, 10, 0.6), device.Paint{})
		if got := runes(dev.Chars()); got != tc.want {
			t.Errorf("%#U recorded %q, want %q", tc.r, got, tc.want)
		}
	}
}

// TestRecordDecomposesLigaturesWithinOneToMany pins that the two routes compose the way MuPDF's do: every rune a
// one-to-many /ToUnicode target supplies reaches the ligature decomposition as its own character, the leading one
// included.
func TestRecordDecomposesLigaturesWithinOneToMany(t *testing.T) {
	dev := New()
	dev.FillText(mkLigatureRun(0xFB01, []rune{'x', 0xFB02}, 100, 200, 10, 0.6), device.Paint{})
	if got := runes(dev.Chars()); got != "fixfl" {
		t.Fatalf("recorded %q, want %q", got, "fixfl")
	}
	// Only the first character carries the glyph's advance; every other stands at the pen the glyph left behind.
	chars := dev.Chars()
	for i, c := range chars[1:] {
		if c.Origin != chars[0].End || c.End != chars[0].End {
			t.Errorf("character %d spans %+v-%+v, want both at %+v", i+1, c.Origin, c.End, chars[0].End)
		}
	}
}

// TestRecordCapsCharactersAcrossFillers pins that the extra characters a glyph spells out are subject to the same cap
// as any other: a file may not walk past it by hiding characters behind one glyph.
func TestRecordCapsCharactersAcrossFillers(t *testing.T) {
	rest := make([]rune, 255) // What one code's /ToUnicode target can hold, capped as MuPDF's PDF_MRANGE_CAP caps it.
	for i := range rest {
		rest[i] = 'a'
	}
	glyphs := make([]device.Glyph, 4096)
	for i := range glyphs {
		glyphs[i] = device.Glyph{
			Trm:     gfx.Matrix{A: 12, D: 12, E: float32(i) * 6, F: 200},
			Unicode: 'a',
			Rest:    rest,
			Advance: 0.5,
		}
	}
	dev := New()
	for range 8 {
		// A fresh run pointer per delivery: the dedup guard keys on identity, so each of these records its glyphs.
		dev.FillText(&device.TextRun{Font: &font.Font{}, Glyphs: glyphs}, device.Paint{})
		if got := len(dev.Chars()); got > maxChars {
			t.Fatalf("recorded %d characters, past the cap of %d", got, maxChars)
		}
	}
	if got := len(dev.Chars()); got != maxChars {
		t.Fatalf("recorded %d characters, want the cap of %d", got, maxChars)
	}
}
