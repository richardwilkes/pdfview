// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package vecmath

import (
	"encoding/binary"
	"simd"
	"testing"
)

// domainMax is the upper end of the input domain both helpers document as exact.
const domainMax = 0xFFFF

// TestUDiv255 proves UDiv255 over its whole documented domain: every n in [0, 0xFFFF] goes through a vector lane and
// is compared against the scalar quotient.
func TestUDiv255(t *testing.T) {
	forEachInDomain(t, func(in, out []uint32) {
		UDiv255(simd.LoadUint32s(in)).Store(out)
		for i, got := range out {
			if want := in[i] / 255; got != want {
				t.Fatalf("UDiv255 lane %d: n=%d, got %d, want %d", i, in[i], got, want)
			}
		}
	})
}

// TestUDiv252 proves UDiv252 over its whole documented domain the same way. An off-by-one in the biased form would
// shift luminosity soft-mask values by one code point across whole images.
func TestUDiv252(t *testing.T) {
	forEachInDomain(t, func(in, out []uint32) {
		UDiv252(simd.LoadUint32s(in)).Store(out)
		for i, got := range out {
			if want := in[i] / 252; got != want {
				t.Fatalf("UDiv252 lane %d: n=%d, got %d, want %d", i, in[i], got, want)
			}
		}
	})
}

// forEachInDomain walks [0, domainMax] one vector at a time, handing check a filled input buffer and a scratch output
// buffer of one vector's worth of lanes. Lanes past domainMax in the final batch repeat domainMax, which that batch
// already covers, so padding cannot mask a failure.
func forEachInDomain(t *testing.T, check func(in, out []uint32)) {
	t.Helper()
	var probe simd.Uint32s
	lanes := probe.Len()
	in := make([]uint32, lanes)
	out := make([]uint32, lanes)
	for base := 0; base <= domainMax; base += lanes {
		for i := range in {
			n := base + i
			if n > domainMax {
				n = domainMax
			}
			in[i] = uint32(n)
		}
		check(in, out)
	}
}

// TestPinUint8sAddWraps pins that Uint8s.Add is modular (255+1 == 0) and that saturation is a separate method.
//
// Kernel that relies on it: addRowsSIMD in internal/filter/simd_on.go, the PNG Up predictor undo. RFC 2083 section
// 6.3 defines the reconstruction as byte addition modulo 256 (row[i] += prev[i]), so the kernel adds raw Uint8s lanes
// with no clamp. If Add saturated, every reconstructed sample that overflows would stick at 255 and the decoded image
// would be wrong, not merely clipped.
func TestPinUint8sAddWraps(t *testing.T) {
	var probe simd.Uint8s
	lanes := probe.Len()
	a := make([]uint8, lanes)
	b := make([]uint8, lanes)
	out := make([]uint8, lanes)
	for i := range lanes {
		a[i] = 255
		b[i] = 1
	}
	simd.LoadUint8s(a).Add(simd.LoadUint8s(b)).Store(out)
	for i, got := range out {
		if got != 0 {
			t.Fatalf("Uint8s.Add lane %d: 255+1 gave %d, want 0 (Add must wrap, not saturate)", i, got)
		}
	}
	simd.LoadUint8s(a).AddSaturated(simd.LoadUint8s(b)).Store(out)
	for i, got := range out {
		if got != 255 {
			t.Fatalf("Uint8s.AddSaturated lane %d: 255+1 gave %d, want 255", i, got)
		}
	}
}

// TestPinInt32sShiftAllRightIsArithmetic pins that Int32s.ShiftAllRight replicates the sign bit rather than shifting
// in zeros, so it is floor division by a power of two on negative lanes.
//
// Kernels that rely on it: sub53RowSIMD and add53RowSIMD in internal/jpeg2000/wavelet/simd_on.go, the reversible 5/3
// inverse DWT. Its lifting steps are L[i] -= (H[i-1]+H[i]+2)>>2 and H[i] += (L[i]+L[i+1])>>1 over signed
// coefficients, and JPEG 2000 specifies those shifts as floor division. Coefficients are routinely negative; a logical
// shift would turn a small negative value into one near 2^31 and destroy the tile.
func TestPinInt32sShiftAllRightIsArithmetic(t *testing.T) {
	var probe simd.Int32s
	lanes := probe.Len()
	in := make([]int32, lanes)
	out := make([]int32, lanes)
	for i := range lanes {
		in[i] = -8
	}
	simd.LoadInt32s(in).ShiftAllRight(1).Store(out)
	for i, got := range out {
		if got != -4 {
			t.Fatalf("Int32s.ShiftAllRight lane %d: -8>>1 gave %d, want -4 (shift must be arithmetic)", i, got)
		}
	}
	for i := range lanes {
		in[i] = -1
	}
	simd.LoadInt32s(in).ShiftAllRight(31).Store(out)
	for i, got := range out {
		if got != -1 {
			t.Fatalf("Int32s.ShiftAllRight lane %d: -1>>31 gave %d, want -1", i, got)
		}
	}
	for i := range lanes {
		in[i] = 9
	}
	simd.LoadInt32s(in).ShiftAllRight(1).Store(out)
	for i, got := range out {
		if got != 4 {
			t.Fatalf("Int32s.ShiftAllRight lane %d: 9>>1 gave %d, want 4", i, got)
		}
	}
}

// TestPinFloat32sConvertToInt32Truncates pins that Float32s.ConvertToInt32 rounds toward zero (2.9 becomes 2 and
// -2.9 becomes -2) rather than rounding to nearest or flooring.
//
// Kernel that relies on it: unpremultiplyChannel in simd_on.go at the repository root, which replaces the scalar
// divide by alpha with a nudged reciprocal multiply and truncates the product. The nudge is built so the truncated
// estimate is right or exactly one too low, never too high, and the correction step that follows assumes that; a
// round-to-nearest conversion would break it.
func TestPinFloat32sConvertToInt32Truncates(t *testing.T) {
	values := []float32{2.9, -2.9, 2.5, -2.5, 0.75, -0.75, 7.999, -7.999}
	wants := []int32{2, -2, 2, -2, 0, 0, 7, -7}
	var probe simd.Float32s
	lanes := probe.Len()
	in := make([]float32, lanes)
	out := make([]int32, lanes)
	for i := range lanes {
		in[i] = values[i%len(values)]
	}
	simd.LoadFloat32s(in).ConvertToInt32().Store(out)
	for i, got := range out {
		if want := wants[i%len(wants)]; got != want {
			t.Fatalf("Float32s.ConvertToInt32 lane %d: %v gave %d, want %d (must truncate toward zero)",
				i, in[i], got, want)
		}
	}
}

// TestPinUint8sReshapeToUint32sIsLittleEndian pins that a reshape from byte lanes to word lanes keeps little-endian
// memory order: word lane j is the four bytes at 4j..4j+3, low byte first.
//
// Kernels that rely on it: every kernel that loads RGBA bytes and works on them as packed pixel words, which is
// maskLumaVec in internal/render/simd_on.go, compositeAlphaSIMD in internal/imaging/simd_on.go, and unpremultiplyVec
// in simd_on.go at the repository root. The packed layout is little-endian on the wire too: internal/render/render.go
// writes rows out with binary.LittleEndian.PutUint32. If a reshape permuted lanes, pixels would come back with their
// channels reversed.
func TestPinUint8sReshapeToUint32sIsLittleEndian(t *testing.T) {
	var byteProbe simd.Uint8s
	var wordProbe simd.Uint32s
	byteLanes := byteProbe.Len()
	wordLanes := wordProbe.Len()
	if byteLanes != wordLanes*4 {
		t.Fatalf("vector shape: Uint8s has %d lanes but Uint32s has %d; expected a 4:1 ratio", byteLanes, wordLanes)
	}
	src := make([]uint8, byteLanes)
	for i := range src {
		src[i] = uint8(i)
	}
	out := make([]uint32, wordLanes)
	simd.LoadUint8s(src).ReshapeToUint32s().Store(out)
	for j, got := range out {
		if want := binary.LittleEndian.Uint32(src[j*4 : (j+1)*4]); got != want {
			t.Fatalf("Uint8s.ReshapeToUint32s lane %d: got %#08x, want %#08x (lane order must be little-endian "+
				"memory order)", j, got, want)
		}
	}
}
