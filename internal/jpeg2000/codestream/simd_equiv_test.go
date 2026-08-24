// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package codestream

import (
	"reflect"
	"simd"
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/engine"
	"github.com/richardwilkes/pdfview/internal/testrand"
	"github.com/richardwilkes/pdfview/internal/vecmath"
)

// simdTestLengths returns the element counts every kernel is swept over: zero, every length inside the first vector,
// the exact vector multiples, and each of those plus and minus one, so both the full-vector loop and the LoadPart /
// StorePart tail are hit at every alignment. The sweep starts well below the gates and ends well above them, so each
// kernel's internal fallback to the scalar function is exercised alongside its vector path.
func simdTestLengths() []int {
	var probe simd.Int32s
	lanes := probe.Len()
	lengths := make([]int, 0, 4*lanes+8)
	for n := 0; n <= 2*lanes; n++ {
		lengths = append(lengths, n)
	}
	for _, m := range []int{3, 4, 5, 8} {
		lengths = append(lengths, m*lanes-1, m*lanes, m*lanes+1)
	}
	return lengths
}

// TestSIMDWiring locks that init pointed every dispatch variable exactly where this architecture's preference
// constants say it should: at the vector kernel where the constant is true, and left on the scalar default where it
// is false. A refactor can then neither drop a kernel that is switched on nor smuggle in one that is switched off.
func TestSIMDWiring(t *testing.T) {
	if !vecmath.KernelsSupported() {
		t.Skip("this machine cannot run the vector kernels, so init deliberately leaves the scalar dispatch in place")
	}
	for name, k := range map[string]struct {
		got, vector, scalar any
		prefer              bool
	}{
		"applyRCTFn":      {applyRCTFn, applyRCTSIMD, applyRCTScalar, preferApplyRCT},
		"clampPlaneFn":    {clampPlaneFn, clampPlaneSIMD, clampPlaneScalar, preferClampPlane},
		"addClampPlaneFn": {addClampPlaneFn, addClampPlaneSIMD, addClampPlaneScalar, preferAddClampPlane},
	} {
		want, which := k.scalar, "the scalar implementation"
		if k.prefer {
			want, which = k.vector, "the simd kernel"
		}
		if reflect.ValueOf(k.got).Pointer() != reflect.ValueOf(want).Pointer() {
			t.Fatalf("%s: dispatch fn is not %s, which this architecture's preference constant selects", name, which)
		}
	}
}

// TestApplyRCTSIMDEquivalence compares the vector kernel against the scalar function it replaces, element for
// element, over a length sweep that crosses applyRCTMin in both directions — below the gate the kernel must hand off
// to the scalar function and still be right.
func TestApplyRCTSIMDEquivalence(t *testing.T) {
	savedGate := applyRCTMin
	t.Cleanup(func() { applyRCTMin = savedGate })
	for _, gate := range []int{0, savedGate, 1 << 30} {
		applyRCTMin = gate
		for _, n := range simdTestLengths() {
			rnd := testrand.Rand(0x5EED0001)
			y0, y1, y2 := rnd.Int32s(n), rnd.Int32s(n), rnd.Int32s(n)
			w0, w1, w2 := append([]int32(nil), y0...), append([]int32(nil), y1...), append([]int32(nil), y2...)
			applyRCTSIMD(y0, y1, y2)
			applyRCTScalar(w0, w1, w2)
			for i := range n {
				if y0[i] != w0[i] || y1[i] != w1[i] || y2[i] != w2[i] {
					t.Fatalf("applyRCTSIMD gate=%d n=%d index %d: simd (%d,%d,%d) != scalar (%d,%d,%d)",
						gate, n, i, y0[i], y1[i], y2[i], w0[i], w1[i], w2[i])
				}
			}
		}
	}
}

// TestApplyRCTSIMDThroughDispatch drives ApplyRCT itself, once with the dispatch variable on the kernel and once on
// the scalar function, and requires identical planes. That is what tests the call site rather than the kernel: a
// mis-sliced call or the shortest-plane bound applied to the wrong quantity shows up here and nowhere else. The
// longer planes' tails must also come back untouched.
func TestApplyRCTSIMDThroughDispatch(t *testing.T) {
	saved := applyRCTFn
	t.Cleanup(func() { applyRCTFn = saved })
	rnd := testrand.Rand(0x5EED0002)
	y0, y1, y2 := rnd.Int32s(64), rnd.Int32s(40), rnd.Int32s(80)
	w0, w1, w2 := append([]int32(nil), y0...), append([]int32(nil), y1...), append([]int32(nil), y2...)

	applyRCTFn = applyRCTSIMD
	ApplyRCT([]engine.ComponentPlane{{Pix: y0}, {Pix: y1}, {Pix: y2}})

	applyRCTFn = applyRCTScalar
	ApplyRCT([]engine.ComponentPlane{{Pix: w0}, {Pix: w1}, {Pix: w2}})

	for i := range y0 {
		if y0[i] != w0[i] {
			t.Fatalf("ApplyRCT Y0 index %d: simd %d != scalar %d", i, y0[i], w0[i])
		}
	}
	for i := range y2 {
		if y2[i] != w2[i] {
			t.Fatalf("ApplyRCT Y2 index %d: simd %d != scalar %d", i, y2[i], w2[i])
		}
	}
}

// TestClampPlaneSIMDEquivalence compares clampPlaneSIMD against clampPlaneScalar over the same length sweep, at
// several precisions and on both sides of the gate. Precision 33 is the overflow case the kernel refuses: the int32
// bound computation wraps and hands it lo > hi, where Max-then-Min and the scalar chain disagree.
func TestClampPlaneSIMDEquivalence(t *testing.T) {
	savedGate := clampPlaneMin
	t.Cleanup(func() { clampPlaneMin = savedGate })
	for _, gate := range []int{0, savedGate, 1 << 30} {
		clampPlaneMin = gate
		for _, precision := range []int{1, 2, 8, 12, 16, 31, 33} {
			lo := int32(-(1 << uint(precision-1)))
			hi := int32(1<<uint(precision-1)) - 1
			for _, n := range simdTestLengths() {
				rnd := testrand.Rand(0x5EED0003)
				got := rnd.Int32s(n)
				want := append([]int32(nil), got...)
				clampPlaneSIMD(got, lo, hi)
				clampPlaneScalar(want, lo, hi)
				for i := range n {
					if got[i] != want[i] {
						t.Fatalf("clampPlaneSIMD gate=%d precision=%d n=%d index %d: got %d, want %d",
							gate, precision, n, i, got[i], want[i])
					}
				}
			}
		}
	}
}

// TestClampPlaneSIMDTailUntouched pins that the StorePart tail writes exactly the elements it was handed and not a
// lane more — the sentinel values past the end of the plane must survive.
func TestClampPlaneSIMDTailUntouched(t *testing.T) {
	var probe simd.Int32s
	lanes := probe.Len()
	for n := 1; n <= 3*lanes; n++ {
		buf := make([]int32, n+lanes)
		rnd := testrand.Rand(0x5EED0004)
		copy(buf, rnd.Int32s(n+lanes))
		guard := append([]int32(nil), buf[n:]...)
		clampPlaneSIMD(buf[:n], -128, 127)
		for i, v := range guard {
			if buf[n+i] != v {
				t.Fatalf("clampPlaneSIMD n=%d wrote past the slice at +%d: got %d, want %d", n, i, buf[n+i], v)
			}
		}
	}
}

// TestAddClampPlaneSIMDEquivalence compares addClampPlaneSIMD against addClampPlaneScalar across the precisions that
// decide the offset and the index ceiling, on both sides of the gate.
func TestAddClampPlaneSIMDEquivalence(t *testing.T) {
	savedGate := addClampPlaneMin
	t.Cleanup(func() { addClampPlaneMin = savedGate })
	for _, gate := range []int{0, savedGate, 1 << 30} {
		addClampPlaneMin = gate
		for _, precision := range []int{1, 4, 8, 12, 16, 33} {
			offset := int32(1) << uint(precision-1)
			maxIdx := int32(1)<<uint(precision) - 1
			for _, n := range simdTestLengths() {
				rnd := testrand.Rand(0x5EED0005)
				src := rnd.Int32s(n)
				got := make([]int32, n)
				want := make([]int32, n)
				addClampPlaneSIMD(got, src, offset, 0, maxIdx)
				addClampPlaneScalar(want, src, offset, 0, maxIdx)
				for i := range n {
					if got[i] != want[i] {
						t.Fatalf("addClampPlaneSIMD gate=%d precision=%d n=%d index %d: got %d, want %d (src %d)",
							gate, precision, n, i, got[i], want[i], src[i])
					}
				}
			}
		}
	}
}

// TestAddClampPlaneSIMDShortSource pins that a source shorter than the destination still panics, as the scalar loop
// does, rather than being quietly zero-filled by a partial vector load.
func TestAddClampPlaneSIMDShortSource(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("addClampPlaneSIMD with a short source did not panic")
		}
	}()
	addClampPlaneSIMD(make([]int32, 64), make([]int32, 32), 128, 0, 255)
}
