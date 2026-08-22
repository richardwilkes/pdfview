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

import "simd"

// init swaps the dispatch variables over to the vector kernels this architecture prefers. simd.Emulated is true on a
// target whose vector operations the toolchain lowers to scalar loops, where every kernel is strictly slower than the
// portable code it replaces, so the scalar defaults stay in place wholesale there; past that, each kernel is decided
// on its own benchmarked merits by the constants in simd_prefs_<arch>.go.
func init() {
	if simd.Emulated() {
		return
	}
	if preferAddRows {
		addRowsFn = addRowsSIMD
	}
}

// addRowsMin is the shortest row addRowsSIMD handles itself; below it the call goes back to addRowsScalar, because
// the per-call setup outweighs the few lanes there are to add. One full vector on a 128-bit target is where the
// crossover sits: measured on an Apple M4, a 16-byte row runs 5.0ns scalar against 3.0ns vector and a 24-byte row
// 7.1ns against 6.1ns, while an 8-byte row loses (2.8ns against 3.4ns). It is a var rather than a const so the
// equivalence tests can sweep both sides of the gate.
var addRowsMin = 16

// addRowsSIMD is addRowsScalar with the row added a vector at a time.
//
// The lane operation is the wrap-around Uint8s.Add, which is what RFC 2083 section 6.3 specifies and what the scalar
// dst[i] += src[i] does. AddSaturated would clamp every reconstructed sample whose sum passes 255 and silently
// corrupt the image; internal/vecmath's TestPinUint8sAddWraps pins the distinction.
func addRowsSIMD(dst, src []byte) {
	if len(dst) < addRowsMin {
		addRowsScalar(dst, src)
		return
	}
	src = src[:len(dst)]
	var probe simd.Uint8s
	lanes := probe.Len()
	i := 0
	for ; i+lanes <= len(dst); i += lanes {
		simd.LoadUint8s(dst[i:]).Add(simd.LoadUint8s(src[i:])).Store(dst[i:])
	}
	if i < len(dst) {
		a, _ := simd.LoadUint8sPart(dst[i:])
		b, _ := simd.LoadUint8sPart(src[i:])
		a.Add(b).StorePart(dst[i:])
	}
}
