// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package testrand_test

import (
	"math"
	"testing"

	"github.com/richardwilkes/pdfview/internal/testrand"
)

// TestNextMatchesReferenceVector pins Next to the splitmix64 sequence for a zero state, computed independently of
// this package from the algorithm's published constants. Every SIMD test seeds this generator, so a change to a shift
// or a constant fails here first rather than as an opaque equivalence-test diff elsewhere.
func TestNextMatchesReferenceVector(t *testing.T) {
	want := []uint64{0xe220a8397b1dcdaf, 0x6e789e6aa1b965f4, 0x06c45d188009454f, 0xf88bb8a8724c81ec, 0x1b39896a51a8749b}
	var r testrand.Rand
	for i, w := range want {
		if got := r.Next(); got != w {
			t.Fatalf("Next call %d: got %#016x, want %#016x", i+1, got, w)
		}
	}
}

// TestSeedIsState pins that Rand(seed) starts from exactly that state, with no scrambling of the seed: the values were
// computed independently for a state of 0x5eed.
func TestSeedIsState(t *testing.T) {
	want := []uint64{0x09f1fd9d03f0a9b4, 0x553274161bbf8475, 0x5d5bca4696b343b3}
	r := testrand.Rand(0x5eed)
	for i, w := range want {
		if got := r.Next(); got != w {
			t.Fatalf("Next call %d for seed 0x5eed: got %#016x, want %#016x", i+1, got, w)
		}
	}
}

// TestFillDerivesFromNext pins Fill's byte choice, bits 24..31 of each Next value, against a shadow generator with the
// same seed.
func TestFillDerivesFromNext(t *testing.T) {
	r := testrand.Rand(0xf00d)
	shadow := testrand.Rand(0xf00d)
	b := make([]byte, 64)
	r.Fill(b)
	for i, got := range b {
		if want := byte(shadow.Next() >> 24); got != want {
			t.Fatalf("Fill byte %d: got %#02x, want %#02x", i, got, want)
		}
	}
}

// TestInt32sDerivesFromNext pins Int32s to its documented shape: element i is the low 32 bits of the ith Next value,
// kept at full range when i is a multiple of 16 and arithmetic-shifted down by 11 bits otherwise.
func TestInt32sDerivesFromNext(t *testing.T) {
	r := testrand.Rand(0x5eed0001)
	shadow := testrand.Rand(0x5eed0001)
	const n = 48
	out := r.Int32s(n)
	if len(out) != n {
		t.Fatalf("Int32s(%d) returned %d elements", n, len(out))
	}
	for i, got := range out {
		want := int32(uint32(shadow.Next()))
		if i%16 != 0 {
			want >>= 11
		}
		if got != want {
			t.Fatalf("Int32s element %d: got %d, want %d", i, got, want)
		}
	}
}

// TestFloat64sDerivesFromNext pins Float64s bit for bit to its documented shape: a base value in [-976.5625,
// 976.5625] built from Next, scaled up by 1024 when i is a multiple of 8 and down by 65536 when i mod 8 is 3.
func TestFloat64sDerivesFromNext(t *testing.T) {
	r := testrand.Rand(0x5eed1005)
	shadow := testrand.Rand(0x5eed1005)
	const n = 32
	out := r.Float64s(n)
	if len(out) != n {
		t.Fatalf("Float64s(%d) returned %d elements", n, len(out))
	}
	for i, got := range out {
		want := float64(int64(shadow.Next()%2000001)-1000000) / 1024
		switch i % 8 {
		case 0:
			want *= 1024
		case 3:
			want /= 65536
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("Float64s element %d: got %016x, want %016x", i, math.Float64bits(got), math.Float64bits(want))
		}
	}
}
