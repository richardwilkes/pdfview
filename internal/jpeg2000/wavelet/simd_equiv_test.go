// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package wavelet

import (
	"math"
	"reflect"
	"simd"
	"testing"

	"github.com/richardwilkes/pdfview/internal/testrand"
	"github.com/richardwilkes/pdfview/internal/vecmath"
)

// simdTestLengths returns the row widths every kernel is swept over: zero, every width inside the first vector, the
// exact vector multiples, and each of those plus and minus one, so both the full-vector loop and the LoadPart /
// StorePart tail are hit at every alignment. int32 vectors hold at least as many lanes as float64 vectors, so the
// int32 sweep covers the float64 tails as well.
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
// constants say it should: at the vector sweep where the constant is true, and left on the scalar default where it is
// false. A refactor can then neither drop a sweep that is switched on nor smuggle in one that is switched off.
func TestSIMDWiring(t *testing.T) {
	if !vecmath.KernelsSupported() {
		t.Skip("this machine cannot run the vector kernels, so init deliberately leaves the scalar dispatch in place")
	}
	for name, k := range map[string]struct {
		got, vector, scalar any
		prefer              bool
	}{
		"sub53SweepFn":   {sub53SweepFn, sub53SweepSIMD, sub53SweepScalar, preferSub53Sweep},
		"add53SweepFn":   {add53SweepFn, add53SweepSIMD, add53SweepScalar, preferAdd53Sweep},
		"scale97SweepFn": {scale97SweepFn, scale97SweepSIMD, scale97SweepScalar, preferScale97Sweep},
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

// TestSub53RowSIMDEquivalence compares the 5/3 update row kernel against the scalar expression it replaces, element
// for element, over the full width sweep.
func TestSub53RowSIMDEquivalence(t *testing.T) {
	for _, w := range simdTestLengths() {
		rnd := testrand.Rand(0x5EED1001)
		e := rnd.Int32s(w)
		hl := rnd.Int32s(w)
		hr := rnd.Int32s(w)
		want := append([]int32(nil), e...)
		sub53RowSIMD(e, hl, hr)
		for x := range want {
			want[x] -= (hl[x] + hr[x] + 2) >> 2
		}
		for x := range w {
			if e[x] != want[x] {
				t.Fatalf("sub53RowSIMD W=%d index %d: got %d, want %d (hl %d, hr %d)", w, x, e[x], want[x], hl[x], hr[x])
			}
		}
	}
}

// TestAdd53RowSIMDEquivalence compares the 5/3 predict row kernel against the scalar expression it replaces.
func TestAdd53RowSIMDEquivalence(t *testing.T) {
	for _, w := range simdTestLengths() {
		rnd := testrand.Rand(0x5EED1002)
		o := rnd.Int32s(w)
		ll := rnd.Int32s(w)
		lr := rnd.Int32s(w)
		want := append([]int32(nil), o...)
		add53RowSIMD(o, ll, lr)
		for x := range want {
			want[x] += (ll[x] + lr[x]) >> 1
		}
		for x := range w {
			if o[x] != want[x] {
				t.Fatalf("add53RowSIMD W=%d index %d: got %d, want %d (ll %d, lr %d)", w, x, o[x], want[x], ll[x], lr[x])
			}
		}
	}
}

// TestSweep53SIMDEquivalence compares the two vector sweeps against the scalar sweeps they replace, across shapes
// that cover an odd width, an odd height, a band with no high rows at all, and both sides of the width gates — below
// the gate a sweep must hand off to its scalar twin and still be right.
func TestSweep53SIMDEquivalence(t *testing.T) {
	savedSub, savedAdd := sub53RowMin, add53RowMin
	t.Cleanup(func() { sub53RowMin, add53RowMin = savedSub, savedAdd })
	for _, gate := range []int{0, savedSub, 1 << 30} {
		sub53RowMin, add53RowMin = gate, gate
		for _, shape := range [][3]int{{1, 1, 0}, {2, 1, 1}, {7, 3, 2}, {16, 2, 1}, {33, 9, 8}, {64, 32, 32}, {129, 4, 4}} {
			w, hlv, hhv := shape[0], shape[1], shape[2]
			rnd := testrand.Rand(0x5EED1003)
			vec := rnd.Int32s(w * (hlv + hhv))
			sca := append([]int32(nil), vec...)
			if hhv > 0 {
				sub53SweepSIMD(vec, w, hlv, hhv)
				sub53SweepScalar(sca, w, hlv, hhv)
			}
			add53SweepSIMD(vec, w, hlv, hhv)
			add53SweepScalar(sca, w, hlv, hhv)
			for i := range sca {
				if vec[i] != sca[i] {
					t.Fatalf("5/3 sweeps gate=%d W=%d hlv=%d hhv=%d index %d: simd %d != scalar %d",
						gate, w, hlv, hhv, i, vec[i], sca[i])
				}
			}
		}
	}
}

// TestSynthesize53SIMDThroughDispatch drives the public transform, once with the dispatch variables on the kernels
// and once on the scalar sweeps, and requires identical output bands. This is what tests the call site rather than
// the kernels: a sweep called in the wrong order, or skipped when hhv is zero, shows up here and nowhere else.
func TestSynthesize53SIMDThroughDispatch(t *testing.T) {
	savedSub, savedAdd := sub53SweepFn, add53SweepFn
	t.Cleanup(func() { sub53SweepFn, add53SweepFn = savedSub, savedAdd })
	for _, shape := range [][2]int{{1, 1}, {2, 2}, {7, 5}, {16, 3}, {33, 17}, {64, 64}, {129, 8}} {
		w, h := shape[0], shape[1]
		rnd := testrand.Rand(0x5EED1004)
		wl, hlv := (w+1)/2, (h+1)/2
		wh, hhv := w-wl, h-hlv
		mk := func(bw, bh int) Band { return Band{W: bw, H: bh, Data: rnd.Int32s(bw * bh)} }
		ll, lh, hl, hh := mk(wl, hlv), mk(wl, hhv), mk(wh, hlv), mk(wh, hhv)

		sub53SweepFn, add53SweepFn = sub53SweepSIMD, add53SweepSIMD
		vec := Synthesize53(ll, lh, hl, hh)

		sub53SweepFn, add53SweepFn = sub53SweepScalar, add53SweepScalar
		sca := Synthesize53(ll, lh, hl, hh)

		if len(vec.Data) != len(sca.Data) {
			t.Fatalf("Synthesize53 %dx%d: simd produced %d samples, scalar %d", w, h, len(vec.Data), len(sca.Data))
		}
		for i := range sca.Data {
			if vec.Data[i] != sca.Data[i] {
				t.Fatalf("Synthesize53 %dx%d index %d: simd %d != scalar %d", w, h, i, vec.Data[i], sca.Data[i])
			}
		}
	}
}

// TestScaleRow97SIMDEquivalence compares the 9/7 scaling row kernel against the scalar loop bit for bit — via
// math.Float64bits, not an epsilon — over the full width sweep and both scaling constants.
func TestScaleRow97SIMDEquivalence(t *testing.T) {
	for _, k := range []float64{c97K, 1.0 / c97K} {
		for _, w := range simdTestLengths() {
			rnd := testrand.Rand(0x5EED1005)
			r := rnd.Float64s(w)
			want := append([]float64(nil), r...)
			scaleRow97SIMD(r, k)
			for x := range want {
				want[x] *= k
			}
			for x := range w {
				if math.Float64bits(r[x]) != math.Float64bits(want[x]) {
					t.Fatalf("scaleRow97SIMD k=%v W=%d index %d: got %016x, want %016x",
						k, w, x, math.Float64bits(r[x]), math.Float64bits(want[x]))
				}
			}
		}
	}
}

// TestScale97SweepSIMDEquivalence compares the vector scaling sweep against the scalar one bit for bit, on both
// sides of the width gate.
func TestScale97SweepSIMDEquivalence(t *testing.T) {
	savedGate := scaleRow97Min
	t.Cleanup(func() { scaleRow97Min = savedGate })
	for _, gate := range []int{0, savedGate, 1 << 30} {
		scaleRow97Min = gate
		for _, shape := range [][3]int{{1, 1, 0}, {2, 1, 1}, {7, 3, 2}, {16, 2, 1}, {33, 9, 8}, {64, 32, 32}, {129, 4, 4}} {
			w, hlv, hhv := shape[0], shape[1], shape[2]
			rnd := testrand.Rand(0x5EED1006)
			vec := rnd.Float64s(w * (hlv + hhv))
			sca := append([]float64(nil), vec...)
			scale97SweepSIMD(vec, w, hlv, hhv)
			scale97SweepScalar(sca, w, hlv, hhv)
			for i := range sca {
				if math.Float64bits(vec[i]) != math.Float64bits(sca[i]) {
					t.Fatalf("scale97Sweep gate=%d W=%d hlv=%d hhv=%d index %d: simd %016x != scalar %016x",
						gate, w, hlv, hhv, i, math.Float64bits(vec[i]), math.Float64bits(sca[i]))
				}
			}
		}
	}
}

// TestSynthesize97SIMDThroughDispatch drives the public 9/7 transform, once with the dispatch variable on the kernel
// and once on the scalar sweep, and requires bit-identical output. Four lifting sweeps of accumulating float64
// rounding run downstream of the scaling the kernel does, so any deviation it introduced is amplified here rather
// than hidden.
func TestSynthesize97SIMDThroughDispatch(t *testing.T) {
	saved := scale97SweepFn
	t.Cleanup(func() { scale97SweepFn = saved })
	for _, shape := range [][2]int{{1, 1}, {2, 2}, {7, 5}, {16, 3}, {33, 17}, {64, 64}, {129, 8}} {
		w, h := shape[0], shape[1]
		rnd := testrand.Rand(0x5EED1007)
		wl, hlv := (w+1)/2, (h+1)/2
		wh, hhv := w-wl, h-hlv
		mk := func(bw, bh int) BandF { return BandF{W: bw, H: bh, Data: rnd.Float64s(bw * bh)} }
		ll, lh, hl, hh := mk(wl, hlv), mk(wl, hhv), mk(wh, hlv), mk(wh, hhv)

		scale97SweepFn = scale97SweepSIMD
		vec := Synthesize97(ll, lh, hl, hh)

		scale97SweepFn = scale97SweepScalar
		sca := Synthesize97(ll, lh, hl, hh)

		if len(vec.Data) != len(sca.Data) {
			t.Fatalf("Synthesize97 %dx%d: simd produced %d samples, scalar %d", w, h, len(vec.Data), len(sca.Data))
		}
		for i := range sca.Data {
			if math.Float64bits(vec.Data[i]) != math.Float64bits(sca.Data[i]) {
				t.Fatalf("Synthesize97 %dx%d index %d: simd %016x != scalar %016x",
					w, h, i, math.Float64bits(vec.Data[i]), math.Float64bits(sca.Data[i]))
			}
		}
	}
}

// TestLiftRow97SIMDMatchesUnfusedScalar pins liftRow97SIMD against the *unfused* form of the lifting expression —
// the product rounded to float64 and then subtracted — which is what a separate Mul and Sub compute. The kernel is
// exact against that form on every target.
//
// It is not, however, exact against the loop in inverse97VerticalCas0 wherever the compiler contracts that loop into
// a fused multiply-add, which is why the kernel has no dispatch variable. TestLiftRow97ScalarFusionReport below says
// which of the two this target does.
func TestLiftRow97SIMDMatchesUnfusedScalar(t *testing.T) {
	for _, c := range []float64{c97Alpha, c97Beta, c97Gamma, c97Delta} {
		for _, w := range simdTestLengths() {
			rnd := testrand.Rand(0x5EED1008)
			dst := rnd.Float64s(w)
			a := rnd.Float64s(w)
			b := rnd.Float64s(w)
			want := append([]float64(nil), dst...)
			liftRow97SIMD(dst, a, b, c)
			for x := range want {
				want[x] -= float64(c * (a[x] + b[x]))
			}
			for x := range w {
				if math.Float64bits(dst[x]) != math.Float64bits(want[x]) {
					t.Fatalf("liftRow97SIMD c=%v W=%d index %d: got %016x, want %016x",
						c, w, x, math.Float64bits(dst[x]), math.Float64bits(want[x]))
				}
			}
		}
	}
}

// TestLiftRow97ScalarFusionReport logs, for the target the suite runs on, whether the compiler contracts
// `dst -= c*(a+b)` into a fused multiply-add. That decides whether liftRow97SIMD could be wired into
// inverse97VerticalCas0 as written (no fusion: its separate Mul and Sub match) or would need a fused Float64s.MulAdd
// (fusion: they do not). It asserts nothing; both answers are legitimate.
func TestLiftRow97ScalarFusionReport(t *testing.T) {
	rnd := testrand.Rand(0x5EED1009)
	a := rnd.Float64s(4096)
	b := rnd.Float64s(4096)
	base := rnd.Float64s(4096)
	fused := append([]float64(nil), base...)
	unfused := append([]float64(nil), base...)
	for x := range fused {
		fused[x] -= c97Delta * (a[x] + b[x])
		unfused[x] -= float64(c97Delta * (a[x] + b[x]))
	}
	differing := 0
	for x := range fused {
		if math.Float64bits(fused[x]) != math.Float64bits(unfused[x]) {
			differing++
		}
	}
	if differing == 0 {
		t.Logf("scalar 9/7 lifting is NOT contracted into an FMA on this target: liftRow97SIMD as written would be " +
			"bit-identical to the scalar loop")
		return
	}
	t.Logf("scalar 9/7 lifting IS contracted into an FMA on this target: %d of %d samples differ by one rounding, "+
		"so liftRow97SIMD as written must stay out of inverse97VerticalCas0", differing, len(fused))
}
