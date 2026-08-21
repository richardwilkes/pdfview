// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package imaging

import (
	"bytes"
	"math"
	"reflect"
	"simd"
	"testing"
)

// simdRand is a splitmix64 generator: four lines of arithmetic with a fixed seed, so these tests are reproducible
// without math/rand (which gosec's G404 flags) and without depending on the standard library's generator staying
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

// byteLanes is the machine's Uint8s width, which the tail sweeps below are expressed in.
func byteLanes() int {
	var probe simd.Uint8s
	return probe.Len()
}

// sweepGate runs check once with the named gate wide open, so the vector body runs, and once with it shut, so the
// kernel's own fallback to the scalar function runs. Both must produce the same answer, which is what makes a length
// gate a performance decision rather than a correctness one.
func sweepGate(t *testing.T, gate *int, check func()) {
	t.Helper()
	was := *gate
	defer func() { *gate = was }()
	for _, v := range []int{1, math.MaxInt} {
		*gate = v
		check()
	}
}

// wiring is one dispatch variable and the two implementations it can hold, plus this architecture's verdict on which
// of them it should be holding.
type wiring struct {
	fn     any
	kernel any
	scalar any
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
		"invertBytes": {
			fn: invertBytesFn, kernel: invertBytesSIMD, scalar: invertBytesScalar, prefer: preferInvertBytes,
		},
		"threshold": {
			fn: thresholdFn, kernel: thresholdSIMD, scalar: thresholdScalar, prefer: preferThreshold,
		},
		"normalizePlane": {
			fn: normalizePlaneFn, kernel: normalizePlaneSIMD, scalar: normalizePlaneScalar,
			prefer: preferNormalizePlane,
		},
		"compositeAlpha": {
			fn: compositeAlphaFn, kernel: compositeAlphaSIMD, scalar: compositeAlphaScalar,
			prefer: preferCompositeAlpha,
		},
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

// TestInvertBytesSIMDMatchesScalar walks every length from 0 through three vectors plus a few bytes, covering the
// full-vector body, every tail length the LoadPart/StorePart pair sees, and the empty row — each on both sides of the
// kernel's length gate.
func TestInvertBytesSIMDMatchesScalar(t *testing.T) {
	lanes := byteLanes()
	rng := simdRand{state: 0x1b16}
	for n := range 3*lanes + 5 {
		src := make([]byte, n)
		rng.fill(src)
		want := make([]byte, n)
		invertBytesScalar(want, src)
		sweepGate(t, &invertBytesMin, func() {
			got := make([]byte, n)
			invertBytesSIMD(got, src)
			if !bytes.Equal(got, want) {
				t.Fatalf("invertBytesSIMD(%d bytes, gate %d): got %v, want %v", n, invertBytesMin, got, want)
			}
		})
	}
}

// TestThresholdSIMDMatchesScalar sweeps tail lengths, both /Decode polarities, and both sides of the gate. The sample
// values deliberately include 0, 127, 128, and 255 at fixed positions, since the kernel replaces an unsigned "< 128"
// comparison with a sign-bit test and 127 against 128 is exactly where that equivalence has to hold.
func TestThresholdSIMDMatchesScalar(t *testing.T) {
	lanes := byteLanes()
	rng := simdRand{state: 0x7412}
	for n := range 3*lanes + 5 {
		gray := make([]byte, n)
		rng.fill(gray)
		edges := []byte{0, 127, 128, 255, 1, 254, 126, 129}
		for i := range min(n, len(edges)) {
			gray[i] = edges[i]
		}
		for _, invert := range []bool{false, true} {
			want := make([]byte, n)
			thresholdScalar(want, gray, invert)
			sweepGate(t, &thresholdMin, func() {
				got := make([]byte, n)
				thresholdSIMD(got, gray, invert)
				if !bytes.Equal(got, want) {
					t.Fatalf("thresholdSIMD(%d bytes, invert=%v, gate %d): got %v, want %v", n, invert, thresholdMin,
						got, want)
				}
			})
		}
	}
}

// TestNormalizePlaneSIMDMatchesScalar checks the JPX component normalizer against the scalar function at every
// precision, including the one the kernel declines. The sample values include the saturating extremes on both ends,
// the exact clamp boundaries, and the int32 limits, since the kernel reorders the clamp and the offset add to keep
// the arithmetic inside 32 bits and that reordering is only correct at those boundaries.
func TestNormalizePlaneSIMDMatchesScalar(t *testing.T) {
	var probe simd.Int32s
	lanes := probe.Len()
	rng := simdRand{state: 0x3ec1}
	for precision := 1; precision <= 32; precision++ {
		n := newJPXNorm(precision)
		for _, count := range []int{
			0, 1, lanes - 1, lanes, lanes + 1, 3*lanes + 3, normalizeChunk - 1,
			normalizeChunk, normalizeChunk + lanes + 1,
		} {
			samples := make([]int32, count)
			for i := range samples {
				samples[i] = int32(rng.next())
			}
			edges := []int32{
				0, 1, -1, int32(-n.offset), int32(-n.offset) - 1, int32(n.maxVal - n.offset),
				int32(n.maxVal-n.offset) + 1, math.MaxInt32, math.MinInt32,
			}
			for i := range min(count, len(edges)) {
				samples[i] = edges[i]
			}
			want := make([]byte, count)
			normalizePlaneScalar(want, samples, n)
			sweepGate(t, &normalizePlaneMin, func() {
				got := make([]byte, count)
				normalizePlaneSIMD(got, samples, n)
				if !bytes.Equal(got, want) {
					t.Fatalf("normalizePlaneSIMD(precision=%d, %d samples, gate %d): got %v, want %v", precision,
						count, normalizePlaneMin, got, want)
				}
			})
		}
	}
}

// TestNormalizePlaneInt32SafeCovers32Bit states the one precision the vector path must decline: at 32 the offset
// alone is 2^31, which no int32 lane holds.
func TestNormalizePlaneInt32SafeCovers32Bit(t *testing.T) {
	for precision := 1; precision <= 32; precision++ {
		n := newJPXNorm(precision)
		if want := precision <= 31; n.int32Safe() != want {
			t.Fatalf("precision %d: int32Safe reported %v, want %v", precision, n.int32Safe(), want)
		}
	}
}

// TestCompositeAlphaSIMDMatchesScalar requires the kernel and the scalar function to agree on both the resulting
// pixels and the HasAlpha flag. The pixel counts straddle the vector width and the kernel's staging-chunk boundary,
// the mask planes cover the cases the two skips distinguish (all opaque, all transparent, mixed, and one hole in an
// otherwise opaque run), and the shapes cover both the equal-dimension case the kernel specializes and the coarser
// mask it must hand back.
func TestCompositeAlphaSIMDMatchesScalar(t *testing.T) {
	var probe simd.Uint32s
	lanes := probe.Len()
	rng := simdRand{state: 0xa1fa}
	counts := []int{
		1, lanes - 1, lanes, lanes + 1, 3*lanes + 1, compositeAlphaChunk - 1, compositeAlphaChunk,
		compositeAlphaChunk + lanes + 1,
	}
	for _, count := range counts {
		for _, mode := range []string{"random", "opaque", "zero", "one-hole"} {
			pix := make([]byte, count*4)
			rng.fill(pix)
			plane := make([]byte, count)
			switch mode {
			case "random":
				rng.fill(plane)
			case "opaque":
				for i := range plane {
					plane[i] = 255
				}
			case "zero": // A plane of zeros: every pixel goes fully transparent.
			case "one-hole":
				for i := range plane {
					plane[i] = 255
				}
				plane[count-1] = 254
			}
			// The equal-dimension shape the kernel specializes, and a coarser mask it must decline.
			for _, dims := range [][2]int{{count, 1}, {1, 1}} {
				want := &Image{Pix: bytes.Clone(pix), Width: count, Height: 1}
				compositeAlphaScalar(want, plane, dims[0], dims[1])
				sweepGate(t, &compositeAlphaMin, func() {
					got := &Image{Pix: bytes.Clone(pix), Width: count, Height: 1}
					compositeAlphaSIMD(got, plane, dims[0], dims[1])
					if !bytes.Equal(got.Pix, want.Pix) {
						t.Fatalf("compositeAlphaSIMD(%d pixels, %s, mask %dx%d, gate %d): pixels disagree", count,
							mode, dims[0], dims[1], compositeAlphaMin)
					}
					if got.HasAlpha != want.HasAlpha {
						t.Fatalf("compositeAlphaSIMD(%d pixels, %s, mask %dx%d, gate %d): HasAlpha %v, want %v",
							count, mode, dims[0], dims[1], compositeAlphaMin, got.HasAlpha, want.HasAlpha)
					}
				})
			}
		}
	}
}

// TestCompositeAlphaSIMDExhaustiveProduct proves the alpha product over its entire domain: every (alpha, mask) pair
// there is, checked against the integer formula the scalar function uses. vecmath.UDiv255 is exact only up to 0xFFFF
// and this product tops out at 65025, so the whole domain fits in one sweep.
func TestCompositeAlphaSIMDExhaustiveProduct(t *testing.T) {
	const count = 256
	pix := make([]byte, count*4)
	plane := make([]byte, count)
	for a := range 256 {
		for i := range count {
			pix[i*4] = 0x11
			pix[i*4+1] = 0x22
			pix[i*4+2] = 0x33
			pix[i*4+3] = byte(i)
			plane[i] = byte(a)
		}
		img := &Image{Pix: pix, Width: count, Height: 1}
		compositeAlphaSIMD(img, plane, count, 1)
		if img.HasAlpha != (a != 255) {
			t.Fatalf("compositeAlphaSIMD reported HasAlpha=%v for mask %d", img.HasAlpha, a)
		}
		for i := range count {
			want := byte(uint32(i) * uint32(a) / 255)
			if got := pix[i*4+3]; got != want {
				t.Fatalf("alpha %d * mask %d / 255: got %d, want %d", i, a, got, want)
			}
			if pix[i*4] != 0x11 || pix[i*4+1] != 0x22 || pix[i*4+2] != 0x33 {
				t.Fatalf("alpha %d * mask %d / 255: color channels were disturbed (%v)", i, a, pix[i*4:i*4+3])
			}
		}
	}
}
