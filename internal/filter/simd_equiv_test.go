// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package filter

import (
	"bytes"
	"math"
	"reflect"
	"simd"
	"testing"
)

// simdRand is a splitmix64 generator: four lines of arithmetic with a fixed seed, so these tests are reproducible
// without math/rand (which gosec's G404 flags) and without a dependency on the standard library's generator staying
// byte-stable across releases.
type simdRand struct {
	state uint64
}

// next returns the next value in the sequence.
func (r *simdRand) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// fill overwrites b with pseudorandom bytes.
func (r *simdRand) fill(b []byte) {
	for i := range b {
		b[i] = byte(r.next() >> 24)
	}
}

// wiring is one dispatch variable and the two implementations it can hold, plus this architecture's verdict on which
// of them it should be holding.
type wiring struct {
	fn     func(dst, src []byte)
	kernel func(dst, src []byte)
	scalar func(dst, src []byte)
	prefer bool
}

// TestSIMDWiring locks that init pointed every dispatch variable where simd_prefs_<arch>.go says it belongs: at the
// vector kernel where this architecture prefers it, and at the scalar function where its benchmark did not earn the
// swap. A refactor cannot silently leave the experiment build running the scalar code the rest of this file compares
// against, nor silently switch on a kernel that was turned off deliberately.
func TestSIMDWiring(t *testing.T) {
	if simd.Emulated() {
		t.Skip("vector operations are emulated on this target, so init deliberately keeps the scalar dispatch")
	}
	for name, w := range map[string]wiring{
		"addRows": {fn: addRowsFn, kernel: addRowsSIMD, scalar: addRowsScalar, prefer: preferAddRows},
	} {
		want, label := w.scalar, "scalar implementation"
		if w.prefer {
			want, label = w.kernel, "SIMD kernel"
		}
		if reflect.ValueOf(w.fn).Pointer() != reflect.ValueOf(want).Pointer() {
			t.Fatalf("%s: dispatch fn is not the %s", name, label)
		}
	}
}

// TestAddRowsSIMDMatchesScalar walks every row length from 0 through three vectors plus a few bytes, so each run
// covers the full-vector body, every tail length the LoadPart/StorePart pair has to handle, and the empty row. Each
// length runs with the gate open (the vector body) and shut (the kernel's own fallback to addRowsScalar), so both
// sides of the gate are proven rather than just the one this machine's row lengths happen to reach.
//
// The data deliberately includes rows of 0xff against small values, since the whole point of the kernel using Add
// rather than AddSaturated is what happens when a sum passes 255.
func TestAddRowsSIMDMatchesScalar(t *testing.T) {
	var probe simd.Uint8s
	lanes := probe.Len()
	rng := simdRand{state: 0x5eed1}
	for n := range 3*lanes + 5 {
		row := make([]byte, n)
		prev := make([]byte, n)
		for pass := range 2 {
			if pass == 0 {
				rng.fill(row)
				rng.fill(prev)
			} else {
				// The wrap case, held apart from the random data so a failure names it directly.
				for i := range row {
					row[i] = 0xff
					prev[i] = byte(i%7) + 1
				}
			}
			want := bytes.Clone(row)
			addRowsScalar(want, prev)
			for _, gate := range []int{1, math.MaxInt} {
				was := addRowsMin
				addRowsMin = gate
				got := bytes.Clone(row)
				addRowsSIMD(got, prev)
				addRowsMin = was
				if !bytes.Equal(got, want) {
					t.Fatalf("addRowsSIMD(%d bytes, pass %d, gate %d): got %v, want %v", n, pass, gate, got, want)
				}
			}
		}
	}
}

// TestPNGUpPredictorSIMDMatchesScalar drives the real dispatch site: the same payload runs through pngPredictor with
// the dispatch variable pointed at the kernel and again with it pointed at the scalar function, and the two outputs
// must be byte-identical. Row lengths straddle the vector width in both directions, and the payload mixes Up rows
// with the serial filters so a regression cannot hide behind rows the kernel never sees.
func TestPNGUpPredictorSIMDMatchesScalar(t *testing.T) {
	var probe simd.Uint8s
	lanes := probe.Len()
	rng := simdRand{state: 0xfeed2}
	for _, columns := range []int{1, 3, lanes - 1, lanes, lanes + 1, 2 * lanes, 3*lanes + 7, 613} {
		for _, colors := range []int{1, 3, 4} {
			p := Params{Predictor: 15, Colors: colors, BitsPerComponent: 8, Columns: columns}
			rowLen := colors * columns
			const rows = 9
			data := make([]byte, 0, rows*(rowLen+1))
			for r := range rows {
				// Filter types cycle so Up (2) lands next to None, Sub, Average, and Paeth rows.
				data = append(data, byte(r%5))
				row := make([]byte, rowLen)
				rng.fill(row)
				data = append(data, row...)
			}
			was := addRowsFn
			addRowsFn = addRowsSIMD
			vector, err := pngPredictor(p, bytes.Clone(data), 1<<24)
			addRowsFn = addRowsScalar
			scalar, scalarErr := pngPredictor(p, bytes.Clone(data), 1<<24)
			addRowsFn = was
			if err != nil || scalarErr != nil {
				t.Fatalf("columns=%d colors=%d: pngPredictor failed: vector=%v scalar=%v", columns, colors, err,
					scalarErr)
			}
			if !bytes.Equal(vector, scalar) {
				t.Fatalf("columns=%d colors=%d: vector and scalar PNG predictors disagree", columns, colors)
			}
		}
	}
}
