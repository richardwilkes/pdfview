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
