// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package testrand holds the splitmix64 generator the SIMD equivalence tests and benchmarks share. The tests need
// reproducible bytes and coefficients: gosec G404 bars math/rand in this repository, and the standard library's
// generators are not guaranteed to stay byte-stable across releases. The package carries no build tag and never
// imports the simd package, so the same generator compiles into the default and GOEXPERIMENT=simd builds alike.
// internal/jbig2 keeps its own copy on purpose: it is vendored third-party code that imports nothing else from this
// repository (see its PROVENANCE.md).
package testrand

// Rand is a splitmix64 generator whose state is the value itself, so Rand(seed) is ready to use.
type Rand uint64

// Next returns the next value in the sequence.
func (r *Rand) Next() uint64 {
	*r += 0x9e3779b97f4a7c15
	z := uint64(*r)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// Fill overwrites b with pseudorandom bytes.
func (r *Rand) Fill(b []byte) {
	for i := range b {
		b[i] = byte(r.Next() >> 24)
	}
}

// Int32s returns n coefficients spread across the signed range: most land in [-2^20, 2^20), the size real subband
// coefficients reach, and every 16th is a full-range value so the wrap-around add and the arithmetic shift are
// exercised at the extremes too.
func (r *Rand) Int32s(n int) []int32 {
	out := make([]int32, n)
	for i := range out {
		v := int32(uint32(r.Next()))
		if i%16 == 0 {
			out[i] = v
		} else {
			out[i] = v >> 11
		}
	}
	return out
}

// Float64s returns n coefficients whose exponents span the range a 9/7 subband actually carries, so bit-exactness
// comparisons are not all made on similar magnitudes.
func (r *Rand) Float64s(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		v := float64(int64(r.Next()%2000001)-1000000) / 1024
		switch i % 8 {
		case 0:
			v *= 1024
		case 3:
			v /= 65536
		}
		out[i] = v
	}
	return out
}
