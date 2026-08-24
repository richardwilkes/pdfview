// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package pdfview

import (
	"reflect"
	"simd"
	"testing"

	"github.com/richardwilkes/pdfview/internal/testrand"
	"github.com/richardwilkes/pdfview/internal/vecmath"
)

// scalarUnpremultiply returns what unpremultiplyPixelsScalar makes of a copy of pix.
func scalarUnpremultiply(pix []byte) []byte {
	out := make([]byte, len(pix))
	copy(out, pix)
	unpremultiplyPixelsScalar(out)
	return out
}

// vectorUnpremultiply returns what unpremultiplySIMD makes of a copy of pix.
func vectorUnpremultiply(pix []byte) []byte {
	out := make([]byte, len(pix))
	copy(out, pix)
	unpremultiplySIMD(out)
	return out
}

// diff reports the index of the first byte that differs, or -1.
func diff(got, want []byte) int {
	for i := range want {
		if got[i] != want[i] {
			return i
		}
	}
	return -1
}

// TestUnpremultiplyWiring pins that the dispatch variable points where preferUnpremultiply says: at the vector kernel
// when preferred, else at the scalar loop. Everything else in this file tests the kernel directly.
func TestUnpremultiplyWiring(t *testing.T) {
	if !vecmath.KernelsSupported() {
		t.Skip("this machine cannot run the vector kernels, so init deliberately leaves the scalar dispatch in place")
	}
	got := reflect.ValueOf(unpremultiplyPixelsFn).Pointer()
	want := reflect.ValueOf(unpremultiplyPixelsScalar).Pointer()
	if preferUnpremultiply {
		want = reflect.ValueOf(unpremultiplySIMD).Pointer()
	}
	if got != want {
		t.Fatalf("unpremultiplyPixelsFn is wired to %#x, want %#x (preferUnpremultiply=%v)",
			got, want, preferUnpremultiply)
	}
}

// TestUnpremultiplySIMDExhaustive proves the kernel over every (component, alpha) pair: the reciprocal multiply is not
// exact on its own (hence the nudge and the multiply-back correction), so only the full 256×256 grid is evidence that
// the correction lands on the integer quotient every time. The green and blue channels carry derived components, so
// the reassembly of a pixel is proven alongside the arithmetic; alpha 0 and 255 prove the pass-through arms.
func TestUnpremultiplySIMDExhaustive(t *testing.T) {
	for a := range 256 {
		pix := make([]byte, 256*4)
		for c := range 256 {
			pix[c*4] = byte(c)
			pix[c*4+1] = byte(255 - c)
			pix[c*4+2] = byte((c * 7) & 0xff)
			pix[c*4+3] = byte(a)
		}
		want := scalarUnpremultiply(pix)
		got := vectorUnpremultiply(pix)
		if i := diff(got, want); i >= 0 {
			t.Fatalf("alpha %d, pixel %d, channel %d: premultiplied %d, got %d, want %d",
				a, i/4, i%4, pix[i], got[i], want[i])
		}
	}
}

// TestUnpremultiplySIMDMatchesScalar walks every buffer length from empty through four vectors plus a few bytes: the
// full-vector body, every tail length LoadPart/StorePart handle, and trailing bytes that do not complete a pixel (which
// both arms must leave as they found them). Random bytes rather than valid premultiplied pixels exercise the clamp.
func TestUnpremultiplySIMDMatchesScalar(t *testing.T) {
	wasMin := unpremultiplyMin
	t.Cleanup(func() { unpremultiplyMin = wasMin })
	unpremultiplyMin = 0
	var probe simd.Uint8s
	lanes := probe.Len()
	rng := testrand.Rand(0x9d0f)
	for n := range 4*lanes + 8 {
		pix := make([]byte, n)
		rng.Fill(pix)
		want := scalarUnpremultiply(pix)
		got := vectorUnpremultiply(pix)
		if i := diff(got, want); i >= 0 {
			t.Fatalf("length %d, byte %d: got %d, want %d", n, i, got[i], want[i])
		}
	}
}

// TestUnpremultiplySIMDAlphaMixes mixes pass-through alphas with translucent ones inside one vector and one chunk,
// which is what the mask-and-select and the whole-chunk skip exist for: a lane that should be left alone must come
// back untouched, and a chunk must not be skipped when one pixel in it needs work.
func TestUnpremultiplySIMDAlphaMixes(t *testing.T) {
	wasMin := unpremultiplyMin
	t.Cleanup(func() { unpremultiplyMin = wasMin })
	unpremultiplyMin = 0
	var probe simd.Uint8s
	pixels := probe.Len() * unpremultiplyChunkVectors * 2
	rng := testrand.Rand(0x51ce)
	for _, pattern := range [][]byte{
		{0, 255, 1, 128, 254, 0, 7, 255},
		{255},
		{0},
		{255, 255, 255, 255, 255, 255, 255, 3},
		{0, 0, 0, 0, 0, 0, 0, 250},
	} {
		for n := range pixels {
			pix := make([]byte, n*4)
			rng.Fill(pix)
			for i := range n {
				pix[i*4+3] = pattern[i%len(pattern)]
			}
			want := scalarUnpremultiply(pix)
			got := vectorUnpremultiply(pix)
			if i := diff(got, want); i >= 0 {
				t.Fatalf("pattern %v, pixels %d, byte %d: got %d, want %d", pattern, n, i, got[i], want[i])
			}
		}
	}
}

// TestUnpremultiplySIMDGate drives the kernel across both sides of its length gate. Below the gate it must hand the
// buffer to the scalar loop and still come back with the same bytes; above it, it does the work itself.
func TestUnpremultiplySIMDGate(t *testing.T) {
	wasMin := unpremultiplyMin
	t.Cleanup(func() { unpremultiplyMin = wasMin })
	rng := testrand.Rand(0x6a7e)
	for _, gate := range []int{0, 4, 64, 256} {
		unpremultiplyMin = gate
		for _, n := range []int{gate - 4, gate, gate + 4, gate + 64} {
			if n < 0 {
				continue
			}
			pix := make([]byte, n)
			rng.Fill(pix)
			want := scalarUnpremultiply(pix)
			got := vectorUnpremultiply(pix)
			if i := diff(got, want); i >= 0 {
				t.Fatalf("gate %d, length %d, byte %d: got %d, want %d", gate, n, i, got[i], want[i])
			}
		}
	}
}
