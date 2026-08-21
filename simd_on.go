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

import "simd"

// init points the package's dispatch variables at the vector kernels this architecture prefers (see
// simd_prefs_arm64.go and its siblings). On hardware with no vector unit the simd package emulates every operation in
// scalar Go, which is slower than the scalar code it would replace, so nothing is repointed there at all.
func init() {
	if simd.Emulated() {
		return
	}
	if preferUnpremultiply {
		unpremultiplyPixelsFn = unpremultiplySIMD
	}
}

// unpremultiplyMin is the smallest pix buffer, in bytes, unpremultiplySIMD handles itself; below it the kernel calls
// the scalar loop instead, since a few broadcasts of setup cost more than the whole buffer does. It is a var so the
// equivalence test can sweep both sides of it.
var unpremultiplyMin = 256

// unpremultiplyChunkVectors is how many vectors one chunk of the two-pass walk covers. The pass-through test reduces
// its accumulator once per chunk, so a larger chunk spreads that cost thinner, and a smaller one skips at a finer
// grain when a page mixes opaque and translucent areas. 16 vectors is 64 pixels on a 128-bit machine.
const unpremultiplyChunkVectors = 16

// unpremultiplyNudge scales the reciprocal 1/a just below its true value, by one part in four million: far more than
// the float32 rounding error the reciprocal multiply can accumulate over this kernel's inputs (about one part in
// eight million on a quotient of at most 65152), and far less than the one part in 65152 that would change which
// integer the quotient truncates to. See unpremultiplyChannel for what it buys.
const unpremultiplyNudge = 1 - 1.0/(1<<22)

// unpremultiplyReduceMax is the widest vector, in 32-bit lanes, unpremultiplyChunkKeeps can reduce. 32 covers every
// vector length the simd package reports today with room to spare; a wider machine reports every chunk as needing
// work rather than silently ignoring the lanes past the buffer.
const unpremultiplyReduceMax = 32

// unpremultiplySIMD is the vector form of unpremultiplyPixelsScalar, with identical results for every buffer. It walks whole vectors of bytes, reinterpreting each as one pixel per uint32 lane (little-endian, so the
// alpha byte is the lane's high byte), and leaves any trailing bytes that do not complete a pixel untouched, exactly
// as the scalar loop's i+3 < len(pix) bound does.
//
// A vector is always a whole number of pixels wide: the vector byte count is at least 16 and a power of two, so it is
// a multiple of 4, which also makes the final partial vector a whole number of pixels.
//
// The walk is two passes over a chunk at a time rather than one pass over the buffer, because the scalar loop it
// replaces does almost nothing for a fully opaque page — a strided byte compare, no writes at all — and a page that
// is entirely opaque or entirely transparent outside its content is the common case, not the exception. A chunk whose
// alphas are all 0 or 255 is therefore skipped whole, which keeps its pixels out of the store traffic; the rest pay
// one extra read of data the first pass just pulled into L1.
func unpremultiplySIMD(pix []byte) {
	if len(pix) < unpremultiplyMin {
		unpremultiplyPixelsScalar(pix)
		return
	}
	var probe simd.Uint8s
	lanes := probe.Len()
	n := len(pix) &^ 3
	// Go's SSA has no loop-invariant code motion, so every broadcast written inside the loop would be rebuilt on
	// each iteration. Building them once here and threading them through is what keeps the kernel's instruction count
	// close to its operation count.
	one := simd.BroadcastUint32s(1)
	low := simd.BroadcastUint32s(0xff)
	nudge := simd.BroadcastFloat32s(unpremultiplyNudge)
	for base := 0; base < n; base += lanes * unpremultiplyChunkVectors {
		chunk := pix[base:min(base+lanes*unpremultiplyChunkVectors, n)]
		if unpremultiplyChunkKeeps(chunk, lanes) {
			continue
		}
		i := 0
		for ; i+lanes <= len(chunk); i += lanes {
			unpremultiplyVec(simd.LoadUint8s(chunk[i:]), one, low, nudge).Store(chunk[i:])
		}
		if i < len(chunk) {
			v, _ := simd.LoadUint8sPart(chunk[i:])
			unpremultiplyVec(v, one, low, nudge).StorePart(chunk[i:])
		}
	}
}

// unpremultiplyChunkKeeps reports whether every pixel in the chunk is one the scalar loop would leave alone — alpha 0
// or alpha 255 — in which case the chunk needs neither the arithmetic nor the store.
//
// The test is (px + 1<<24) & 0xfe000000, which is zero exactly when the alpha byte is 0 or 255: adding one to that
// byte carries out of the word for 255 and leaves 1 for 0, and every other value keeps a bit inside the mask. It runs
// on the whole pixel word, so it needs no shift, and the lower bytes cannot reach the mask because nothing is added
// below bit 24. Or-ing the results is a reduction that only has to be resolved once per chunk.
func unpremultiplyChunkKeeps(chunk []byte, lanes int) bool {
	var probe simd.Uint32s
	if probe.Len() > unpremultiplyReduceMax {
		return false
	}
	step := simd.BroadcastUint32s(1 << 24)
	mask := simd.BroadcastUint32s(0xfe000000)
	acc := simd.BroadcastUint32s(0)
	i := 0
	for ; i+lanes <= len(chunk); i += lanes {
		acc = acc.Or(simd.LoadUint8s(chunk[i:]).ReshapeToUint32s().Add(step).And(mask))
	}
	if i < len(chunk) {
		// The zero-filled lanes past the end of the chunk read as alpha 0, which is a pass-through, so they leave the
		// accumulator alone.
		v, _ := simd.LoadUint8sPart(chunk[i:])
		acc = acc.Or(v.ReshapeToUint32s().Add(step).And(mask))
	}
	var reduce [unpremultiplyReduceMax]uint32
	acc.StorePart(reduce[:])
	for _, v := range reduce[:probe.Len()] {
		if v != 0 {
			return false
		}
	}
	return true
}

// unpremultiplyVec unpremultiplies one vector's worth of RGBA pixels. Fully transparent and fully opaque pixels are
// returned untouched, which is what makes the tail safe: LoadUint8sPart zero-fills the lanes past the end of the
// buffer, those lanes read as a == 0, and StorePart never writes them back anyway.
func unpremultiplyVec(v simd.Uint8s, one, low simd.Uint32s, nudge simd.Float32s) simd.Uint8s {
	px := v.ReshapeToUint32s()
	a := px.ShiftAllRight(24)
	keep := a.Less(one).Or(a.Equal(low))
	// The a == 0 lanes are discarded by keep, but they must not divide by zero on the way there, so every lane
	// divides by at least 1. One reciprocal serves all three channels, and it is deliberately the nudged one
	// unpremultiplyChannel's correction is built around.
	div := a.Max(one)
	recip := nudge.Div(div.ConvertToInt32().ConvertToFloat32())
	half := a.ShiftAllRight(1)
	r := unpremultiplyChannel(px.And(low), div, half, recip, one, low)
	g := unpremultiplyChannel(px.ShiftAllRight(8).And(low), div, half, recip, one, low)
	b := unpremultiplyChannel(px.ShiftAllRight(16).And(low), div, half, recip, one, low)
	straight := r.Or(g.ShiftAllLeft(8)).Or(b.ShiftAllLeft(16)).Or(a.ShiftAllLeft(24))
	return px.IfElse(keep, straight).ReshapeToUint8s()
}

// unpremultiplyChannel is the lane-wise (c*0xff + a/2) / a of the scalar unpremultiply, clamped to 0xff. c holds one
// channel per lane, a the matching alpha (never zero), half the matching a/2, and recip the matching 1/a.
//
// The numerator and the alpha are both integers well inside float32's exact range, but the reciprocal multiply that
// replaces the divide is not, so the truncated quotient does not land on the integer quotient by itself. recip is
// therefore 1/a scaled by unpremultiplyNudge, which puts the product strictly below the true quotient and no more
// than a small fraction below it: the estimate is then either right or exactly one too low, never too high, and one
// multiply back — (q+1)*a still fitting inside the numerator means q was one too low — corrects it. Both halves of
// that claim are proven over the whole 256×256 input domain by TestUnpremultiplySIMDExhaustive.
func unpremultiplyChannel(c, a, half simd.Uint32s, recip simd.Float32s, one, low simd.Uint32s) simd.Uint32s {
	num := c.Mul(low).Add(half)
	q := num.ConvertToInt32().ConvertToFloat32().Mul(recip).ConvertToInt32().ConvertToUint32()
	return q.Add(one.Masked(q.Add(one).Mul(a).LessEqual(num))).Min(low)
}
