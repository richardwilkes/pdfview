// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package filter

import (
	"fmt"
	"testing"
)

// upRowLens are the row lengths a scanned or rendered page actually produces: an 8.5in letter page of RGB samples at
// 72, 150, and (in CMYK) 300 dpi. Everything here is measured at those, not at a power of two.
var upRowLens = []int{3 * 612, 3 * 1275, 4 * 2550}

// benchRand is the same splitmix64 generator the equivalence tests use, repeated here because this file carries no
// build tag and must compile in the default build too.
type benchRand struct {
	state uint64
}

// next returns the next value in the sequence.
func (r *benchRand) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// fill overwrites b with pseudorandom bytes.
func (r *benchRand) fill(b []byte) {
	for i := range b {
		b[i] = byte(r.next() >> 24)
	}
}

// BenchmarkAddRowsSIMD measures the PNG Up reconstruction through its dispatch variable, which is what pngPredictor
// calls per row. Both builds run this same body; only what addRowsFn points at differs.
func BenchmarkAddRowsSIMD(b *testing.B) {
	for _, rowLen := range upRowLens {
		b.Run(fmt.Sprintf("row=%d", rowLen), func(b *testing.B) {
			rng := benchRand{state: 0xb1a5e}
			row := make([]byte, rowLen)
			prev := make([]byte, rowLen)
			rng.fill(row)
			rng.fill(prev)
			b.SetBytes(int64(rowLen))
			b.ResetTimer()
			for b.Loop() {
				addRowsFn(row, prev)
			}
		})
	}
}

// BenchmarkPNGUpPredictorSIMD measures the whole predictor stage over a page of Up-filtered rows, allocation and row
// copies included, so the kernel's share of a realistic decode is visible rather than just its own inner loop.
func BenchmarkPNGUpPredictorSIMD(b *testing.B) {
	const rows = 64
	for _, rowLen := range upRowLens {
		b.Run(fmt.Sprintf("row=%d", rowLen), func(b *testing.B) {
			rng := benchRand{state: 0xb1a5f}
			p := Params{Predictor: 15, Colors: 1, BitsPerComponent: 8, Columns: rowLen}
			data := make([]byte, 0, rows*(rowLen+1))
			for range rows {
				data = append(data, 2) // The Up filter.
				row := make([]byte, rowLen)
				rng.fill(row)
				data = append(data, row...)
			}
			work := make([]byte, len(data))
			b.SetBytes(int64(rows * rowLen))
			b.ResetTimer()
			for b.Loop() {
				copy(work, data)
				if _, err := pngPredictor(p, work, 1<<24); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
