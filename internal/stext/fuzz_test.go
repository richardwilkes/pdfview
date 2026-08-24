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
	"testing"
	"unicode"

	"github.com/richardwilkes/pdfview/internal/gfx"
)

// FuzzStext drives the search matcher with an arbitrary needle over an arbitrary synthetic character layout (decoded
// from the fuzz bytes: rune choice, gaps, line breaks, size jumps, rotation, and non-finite coordinates). Nothing may
// panic — degenerate sizes, NaN/Inf geometry, unmapped runes, and hostile needles included — and the quad budget must
// be respected exactly (the search seam's contract).
func FuzzStext(f *testing.F) {
	f.Add("GURPS", []byte{'G', 2, 0, 'U', 2, 0, 'R', 2, 0, 'P', 2, 0, 'S', 2, 0}, 10)
	f.Add("brown fox", []byte{'b', 2, 0, 'r', 2, 0, 'o', 2, 0, 'w', 2, 0, 'n', 2, 1, 'f', 2, 0, 'o', 2, 0, 'x', 2, 0}, 2)
	f.Add("a b", []byte{'a', 2, 0, ' ', 9, 2, 'b', 2, 0}, 100)
	f.Add(" \t\n", []byte{0, 0, 4, 1, 1, 8}, 0)
	f.Fuzz(func(t *testing.T, needle string, layout []byte, maxQuads int) {
		chars := charsFromLayout(layout)
		out := searchChars(chars, needle, maxQuads)
		if maxQuads <= 0 {
			if out != nil {
				t.Fatalf("non-positive budget %d returned %d quads", maxQuads, len(out))
			}
			return
		}
		if len(out) > maxQuads {
			t.Fatalf("budget %d exceeded: %d quads", maxQuads, len(out))
		}
	})
}

// charsFromLayout decodes fuzz bytes into a character stream: each 3-byte group is (rune byte, gap nibble × advance,
// flags). Flag bits: 1 = line break before, 2 = double size, 4 = zero size, 8 = non-finite coordinate, 16 = rotated
// (non-axis). The count is capped to keep individual executions fast.
func charsFromLayout(layout []byte) []Char {
	const maxChars = 2048
	var chars []Char
	x, y := float32(50), float32(50)
	size := float32(12)
	for i := 0; i+2 < len(layout) && len(chars) < maxChars; i += 3 {
		r := rune(layout[i])
		if r == 0xfe { // Exercise a multi-byte rune and the fold path.
			r = 'İ'
		}
		flags := layout[i+2]
		if flags&1 != 0 {
			x, y = 50, y+size*1.2
		} else {
			x += float32(layout[i+1]&0x0f) * size / 10
		}
		cs := size
		if flags&2 != 0 {
			cs *= 2
		}
		if flags&4 != 0 {
			cs = 0
		}
		cx := x
		if flags&8 != 0 {
			cx = float32(math.Inf(1))
		}
		c := mkChar(r, cx, y, cs*0.6, cs)
		if flags&16 != 0 {
			c.Axis = false
			c.End = gfx.Point{X: cx, Y: y - cs*0.6}
		}
		chars = append(chars, c)
		x += cs * 0.6
	}
	return chars
}

// FuzzSelection drives the selection model over the same hostile synthetic layouts FuzzStext builds for the matcher —
// NaN and Inf coordinates, zero and doubled sizes, rotation, line breaks, unmapped runes — with fuzzed indices and a
// fuzzed hit-test point. Nothing may panic, every answer must land inside the page it came from, and the model must
// stay self-consistent: the line and the word around an index contain that index, and a word never carries the
// whitespace the geometry would have separated it at.
func FuzzSelection(f *testing.F) {
	f.Add([]byte{'G', 2, 0, 'U', 2, 0, 'R', 2, 0, 'P', 2, 0, 'S', 2, 0}, 0, 5, float32(50), float32(50))
	f.Add([]byte{'b', 2, 0, 'r', 2, 1, 'o', 2, 0, 'w', 9, 2, 'n', 2, 16}, 3, 1, float32(-1e9), float32(1e9))
	f.Add([]byte{0, 0, 4, 1, 1, 8, ' ', 9, 2}, -7, 1<<30, float32(0), float32(0))
	// A non-finite coordinate on the character a line takes its advance direction from, followed by word-sized gaps:
	// the direction is NaN, so every merge comparison in segmentQuads is false and each synthesized space stands as a
	// quad of its own. This shape puts more quads on a page than it has characters.
	f.Add([]byte("00800A02 020"), 0, 4, float32(50), float32(50))
	f.Fuzz(func(t *testing.T, layout []byte, start, end int, x, y float32) {
		page := NewPage(charsFromLayout(layout))
		n := page.Len()
		if index := page.IndexAt(gfx.Point{X: x, Y: y}); index < 0 || index > n {
			t.Fatalf("IndexAt = %d, outside [0, %d]", index, n)
		}
		if text := page.Text(start, end); len([]rune(text)) > 2*n {
			// Each character contributes at most its own rune plus one separator standing before it.
			t.Fatalf("Text returned %d runes over a page of %d characters", len([]rune(text)), n)
		}
		// Each character contributes at most its own quad plus the quad of the word space synthesized ahead of it
		// (see segmentQuads), the same doubling the separator standing before a character allows Text.
		if quads := page.Quads(start, end); len(quads) > 2*n {
			t.Fatalf("Quads returned %d quads over a page of %d characters", len(quads), n)
		}
		for _, index := range []int{start, end} {
			wordStart, wordEnd := page.WordAt(index)
			if wordStart < 0 || wordEnd > n || wordStart > wordEnd {
				t.Fatalf("WordAt(%d) = (%d, %d), outside [0, %d]", index, wordStart, wordEnd, n)
			}
			lineStart, lineEnd := page.LineAt(index)
			if lineStart < 0 || lineEnd > n || lineStart > lineEnd {
				t.Fatalf("LineAt(%d) = (%d, %d), outside [0, %d]", index, lineStart, lineEnd, n)
			}
			if n == 0 {
				continue
			}
			// Both ranges are the neighborhood OF an index, so the clamped index has to be inside them, and a word
			// can never reach outside the line it grew in.
			clamped := min(max(index, 0), n-1)
			if clamped < wordStart || clamped >= wordEnd {
				t.Fatalf("WordAt(%d) = (%d, %d), which does not contain %d", index, wordStart, wordEnd, clamped)
			}
			if clamped < lineStart || clamped >= lineEnd {
				t.Fatalf("LineAt(%d) = (%d, %d), which does not contain %d", index, lineStart, lineEnd, clamped)
			}
			if wordStart < lineStart || wordEnd > lineEnd {
				t.Fatalf("WordAt(%d) = (%d, %d) reaches outside its line (%d, %d)", index, wordStart, wordEnd,
					lineStart, lineEnd)
			}
			// A character that cannot be part of a word is a selection of its own; anything else grows a word, and a
			// word stops where the matcher would synthesize a space or a line break, so its text carries neither.
			if !isWordChar(page.chars[clamped]) {
				if wordStart != clamped || wordEnd != clamped+1 {
					t.Fatalf("WordAt(%d) = (%d, %d) over a non-word character, want (%d, %d)", index, wordStart,
						wordEnd, clamped, clamped+1)
				}
				continue
			}
			for _, r := range page.Text(wordStart, wordEnd) {
				if unicode.IsSpace(r) {
					t.Fatalf("WordAt(%d) = (%d, %d) spells %q, which carries whitespace", index, wordStart, wordEnd,
						page.Text(wordStart, wordEnd))
				}
			}
		}
	})
}
