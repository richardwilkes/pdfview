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

import "simd"

// init points the dispatch variables at the vector kernels this architecture prefers. The simd package falls back to
// a pure-Go emulation when the target has no vector unit, and that emulation is slower than the scalar loops it would
// replace, so nothing is repointed there at all; past that guard, each kernel is switched on by its own constant from
// the simd_prefs_* file for the architecture.
func init() {
	if simd.Emulated() {
		return
	}
	if preferApplyRCT {
		applyRCTFn = applyRCTSIMD
	}
	if preferClampPlane {
		clampPlaneFn = clampPlaneSIMD
	}
	if preferAddClampPlane {
		addClampPlaneFn = addClampPlaneSIMD
	}
}

// applyRCTSIMD is the vector form of the inverse Reversible Color Transform loop in ApplyRCT. The three slices have
// the same length and do not overlap; ApplyRCT's callers always pass three distinct component planes.
//
// Per element, matching applyRCTScalar exactly: g = y0 - ((y1+y2)>>2), r = y2+g, b = y1+g, then y0=r, y1=g, y2=b.
// Int32s.Add wraps like Go's int32 addition and Int32s.ShiftAllRight is arithmetic (both pinned by
// internal/vecmath's tests), so the two operations the transform leans on carry over unchanged. Every lane is loaded
// before any lane is stored, which is what makes the vector form see the same pre-transform snapshot the scalar loop
// sees.
func applyRCTSIMD(y0, y1, y2 []int32) {
	if len(y0) < applyRCTMin {
		applyRCTScalar(y0, y1, y2)
		return
	}
	var probe simd.Int32s
	lanes := probe.Len()
	n := len(y0)
	i := 0
	for ; i+lanes <= n; i += lanes {
		a := simd.LoadInt32s(y0[i:])
		b := simd.LoadInt32s(y1[i:])
		c := simd.LoadInt32s(y2[i:])
		g := a.Sub(b.Add(c).ShiftAllRight(2))
		r := c.Add(g)
		bl := b.Add(g)
		r.Store(y0[i:])
		g.Store(y1[i:])
		bl.Store(y2[i:])
	}
	if i < n {
		a, _ := simd.LoadInt32sPart(y0[i:])
		b, _ := simd.LoadInt32sPart(y1[i:])
		c, _ := simd.LoadInt32sPart(y2[i:])
		g := a.Sub(b.Add(c).ShiftAllRight(2))
		r := c.Add(g)
		bl := b.Add(g)
		r.StorePart(y0[i:])
		g.StorePart(y1[i:])
		bl.StorePart(y2[i:])
	}
}

// clampPlaneSIMD clamps every sample of p into [lo, hi] in place.
//
// Unordered bounds go to the scalar loop: Max-then-Min and the scalar "below lo, else above hi" chain agree on every
// input only when lo <= hi, and a component precision wide enough to overflow the int32 bound computation in
// finalizeImage produces lo > hi.
func clampPlaneSIMD(p []int32, lo, hi int32) {
	if len(p) < clampPlaneMin || lo > hi {
		clampPlaneScalar(p, lo, hi)
		return
	}
	var probe simd.Int32s
	lanes := probe.Len()
	lov := simd.BroadcastInt32s(lo)
	hiv := simd.BroadcastInt32s(hi)
	n := len(p)
	i := 0
	for ; i+lanes <= n; i += lanes {
		simd.LoadInt32s(p[i:]).Max(lov).Min(hiv).Store(p[i:])
	}
	if i < n {
		v, _ := simd.LoadInt32sPart(p[i:])
		v.Max(lov).Min(hiv).StorePart(p[i:])
	}
}

// addClampPlaneSIMD writes clamp(src[i]+offset, lo, hi) into dst for every element of dst. The add wraps exactly like
// Go's int32 addition, so an overflowing offset lands on the same value the scalar loop produces before the clamp.
//
// Unordered bounds go to the scalar loop for the reason clampPlaneSIMD gives, and so does a src shorter than dst: the
// scalar loop's out-of-range panic is the documented behaviour there, and a partial vector load would silently
// substitute zeros for it.
func addClampPlaneSIMD(dst, src []int32, offset, lo, hi int32) {
	if len(dst) < addClampPlaneMin || lo > hi || len(src) < len(dst) {
		addClampPlaneScalar(dst, src, offset, lo, hi)
		return
	}
	var probe simd.Int32s
	lanes := probe.Len()
	offv := simd.BroadcastInt32s(offset)
	lov := simd.BroadcastInt32s(lo)
	hiv := simd.BroadcastInt32s(hi)
	n := len(dst)
	i := 0
	for ; i+lanes <= n; i += lanes {
		simd.LoadInt32s(src[i:]).Add(offv).Max(lov).Min(hiv).Store(dst[i:])
	}
	if i < n {
		v, _ := simd.LoadInt32sPart(src[i:])
		v.Add(offv).Max(lov).Min(hiv).StorePart(dst[i:])
	}
}
