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

import "simd"

// init points the dispatch variables at the vector sweeps this architecture prefers. The simd package falls back to a
// pure-Go emulation when the target has no vector unit, and that emulation is slower than the scalar loops it would
// replace, so nothing is repointed there at all; past that guard, each sweep is switched on by its own constant from
// the simd_prefs_* file for the architecture.
func init() {
	if simd.Emulated() {
		return
	}
	if preferSub53Sweep {
		sub53SweepFn = sub53SweepSIMD
	}
	if preferAdd53Sweep {
		add53SweepFn = add53SweepSIMD
	}
	if preferScale97Sweep {
		scale97SweepFn = scale97SweepSIMD
	}
}

// sub53SweepSIMD is the vector form of the 5/3 vertical update sweep. Rows narrower than the gate go back to the
// scalar sweep whole, so the gate is paid once per sweep rather than once per row.
func sub53SweepSIMD(out []int32, W, hlv, hhv int) {
	if W < sub53RowMin {
		sub53SweepScalar(out, W, hlv, hhv)
		return
	}
	for i := 0; i < hlv; i++ {
		sub53RowSIMD(out[2*i*W:2*i*W+W], highRow53(out, W, hhv, i-1), highRow53(out, W, hhv, i))
	}
}

// add53SweepSIMD is the vector form of the 5/3 vertical prediction sweep.
func add53SweepSIMD(out []int32, W, hlv, hhv int) {
	if W < add53RowMin {
		add53SweepScalar(out, W, hlv, hhv)
		return
	}
	for i := 0; i < hhv; i++ {
		add53RowSIMD(out[(2*i+1)*W:(2*i+1)*W+W], lowRow53(out, W, hlv, i), lowRow53(out, W, hlv, i+1))
	}
}

// scale97SweepSIMD is the vector form of the pair of 9/7 vertical scaling sweeps.
func scale97SweepSIMD(out []float64, W, hlv, hhv int) {
	if W < scaleRow97Min {
		scale97SweepScalar(out, W, hlv, hhv)
		return
	}
	for i := 0; i < hlv; i++ {
		scaleRow97SIMD(lowRow97(out, W, hlv, i), c97K)
	}
	for i := 0; i < hhv; i++ {
		scaleRow97SIMD(highRow97(out, W, hhv, i), 1.0/c97K)
	}
}

// sub53RowSIMD is one row of the 5/3 update sweep: e[x] -= (hl[x]+hr[x]+2)>>2 for every x in e. hl and hr are at
// least as long as e and neither overlaps it — inverse53VerticalCas0 only ever pairs an even (low) output row with
// odd (high) output rows, and hl and hr are the same row when the symmetric extension folds them together, which the
// kernel does not care about because it only reads them.
//
// Int32s.Add wraps exactly like Go's int32 addition and Int32s.ShiftAllRight replicates the sign bit, so the
// floor-division the reversible transform is specified in terms of carries over unchanged. Both facts are pinned by
// internal/vecmath's tests.
func sub53RowSIMD(e, hl, hr []int32) {
	var probe simd.Int32s
	lanes := probe.Len()
	two := simd.BroadcastInt32s(2)
	n := len(e)
	i := 0
	for ; i+lanes <= n; i += lanes {
		l := simd.LoadInt32s(hl[i:])
		r := simd.LoadInt32s(hr[i:])
		simd.LoadInt32s(e[i:]).Sub(l.Add(r).Add(two).ShiftAllRight(2)).Store(e[i:])
	}
	if i < n {
		l, _ := simd.LoadInt32sPart(hl[i:])
		r, _ := simd.LoadInt32sPart(hr[i:])
		v, _ := simd.LoadInt32sPart(e[i:])
		v.Sub(l.Add(r).Add(two).ShiftAllRight(2)).StorePart(e[i:])
	}
}

// add53RowSIMD is one row of the 5/3 prediction sweep: o[x] += (ll[x]+lr[x])>>1 for every x in o. ll and lr are at
// least as long as o and neither overlaps it; see sub53RowSIMD for why that holds.
func add53RowSIMD(o, ll, lr []int32) {
	var probe simd.Int32s
	lanes := probe.Len()
	n := len(o)
	i := 0
	for ; i+lanes <= n; i += lanes {
		l := simd.LoadInt32s(ll[i:])
		r := simd.LoadInt32s(lr[i:])
		simd.LoadInt32s(o[i:]).Add(l.Add(r).ShiftAllRight(1)).Store(o[i:])
	}
	if i < n {
		l, _ := simd.LoadInt32sPart(ll[i:])
		r, _ := simd.LoadInt32sPart(lr[i:])
		v, _ := simd.LoadInt32sPart(o[i:])
		v.Add(l.Add(r).ShiftAllRight(1)).StorePart(o[i:])
	}
}

// scaleRow97SIMD is one row of a 9/7 scaling sweep: r[x] *= k for every x in r.
//
// A lone multiply has one rounding in both the scalar and the vector form, so this kernel is bit-identical to the
// loop it replaces on every target. That is not true of the lifting sweeps; see liftRow97SIMD.
func scaleRow97SIMD(r []float64, k float64) {
	var probe simd.Float64s
	lanes := probe.Len()
	kv := simd.BroadcastFloat64s(k)
	n := len(r)
	i := 0
	for ; i+lanes <= n; i += lanes {
		simd.LoadFloat64s(r[i:]).Mul(kv).Store(r[i:])
	}
	if i < n {
		v, _ := simd.LoadFloat64sPart(r[i:])
		v.Mul(kv).StorePart(r[i:])
	}
}

// liftRow97SIMD is the vector form of a 9/7 vertical lifting sweep: dst[x] -= c*(a[x]+b[x]) for every x in dst.
//
// IT IS DELIBERATELY NOT WIRED INTO inverse97VerticalCas0, AND MUST NOT BE WITHOUT A DECISION ON THE POLICY BELOW.
// There is no dispatch variable for it and no init line; it is kept, and pinned by the equivalence tests against the
// unfused scalar form, so the measurement and the decision have something concrete to sit on.
//
// The scalar loop it would replace is a subtract of a product, which Go's compiler is free to contract into a single
// fused multiply-add. On arm64 it does: the four lifting sweeps in wavelet97.go compile to FMSUBD, one rounding for
// the whole expression. This kernel is a separate Mul and Sub — two roundings — so on a target whose scalar code
// fuses, roughly a quarter of the lanes come out one ulp away from the scalar result. That is a real difference in
// decoded output, and internal/jpeg2000's decode vectors are held to OpenJPEG's bytes, so pdfview's policy for this
// package is that a vector kernel must be bit-identical to the scalar code it replaces.
//
// The fix would be a fused Float64s.MulAdd, which reproduces FMSUBD exactly — but that is the very operation the
// area's ground rules forbid, because on a target whose scalar code does NOT fuse (amd64 below GOAMD64=v3) MulAdd
// would be the arm of the pair that diverges. Either way the correct form is target-dependent, so the kernel stays
// out of the pipeline until that is settled.
func liftRow97SIMD(dst, a, b []float64, c float64) {
	var probe simd.Float64s
	lanes := probe.Len()
	cv := simd.BroadcastFloat64s(c)
	n := len(dst)
	i := 0
	for ; i+lanes <= n; i += lanes {
		s := simd.LoadFloat64s(a[i:]).Add(simd.LoadFloat64s(b[i:]))
		simd.LoadFloat64s(dst[i:]).Sub(s.Mul(cv)).Store(dst[i:])
	}
	if i < n {
		av, _ := simd.LoadFloat64sPart(a[i:])
		bv, _ := simd.LoadFloat64sPart(b[i:])
		dv, _ := simd.LoadFloat64sPart(dst[i:])
		dv.Sub(av.Add(bv).Mul(cv)).StorePart(dst[i:])
	}
}
