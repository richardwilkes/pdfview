// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package jbig2

import (
	"simd"

	"github.com/richardwilkes/pdfview/internal/vecmath"
)

// init points the dispatch variables at the kernels this architecture prefers (see simd_prefs_<arch>.go). Nothing is
// repointed unless vecmath.KernelsSupported says the machine can run them: it is false where the simd package emulates
// lanes in scalar Go, which is slower than the scalar code the kernels replace, and on an amd64 CPU with AVX but not
// AVX2, where the kernels' broadcasts would fault.
func init() {
	if !vecmath.KernelsSupported() {
		return
	}
	if preferComposeBytes {
		composeBytesFn = composeBytesSIMD
	}
	if preferComposeShiftedRun {
		composeShiftedRunFn = composeShiftedRunSIMD
	}
}

// composeBytesMin is the smallest byte-aligned run, in bytes, that composeBytesSIMD handles itself; anything shorter
// goes back to the scalar composeBytes. It is one 128-bit vector: below that the kernel would be its masked tail and
// nothing else, and the partial load and store are calls, which a run of eight bytes cannot pay for. It is a var so
// the equivalence tests can force either side of it. The unaligned kernel's gate is in simd_dispatch.go, because the
// call site tests that one too.
var composeBytesMin = 16

// composeBytesSIMD is the vector form of composeBytes: it applies op to whole bytes of two byte-aligned rows. Rows
// below composeBytesMin go back to the scalar composeBytes, so the vector loop always has at least one full vector of
// work, and the ragged end of a row is one masked load/store pair. The masked pair is two calls, not instructions, but
// with a full vector ahead of it it still measured faster than finishing the row scalar.
//
// Slicing dst to len(src) up front reproduces the scalar loop's behavior when dst is the shorter: it indexes dst by the
// range over src, so a short dst panics there and must panic here.
func composeBytesSIMD(dst, src []byte, op ComposeOp) {
	if len(src) < composeBytesMin {
		composeBytes(dst, src, op)
		return
	}
	switch op {
	case ComposeOr, ComposeAnd, ComposeXor, ComposeXnor:
	case ComposeReplace:
		copy(dst, src)
		return
	default:
		// An op outside the enum composes nothing, which is what the scalar switch does with it.
		return
	}
	dst = dst[:len(src)]
	var probe simd.Uint8s
	lanes := probe.Len()
	n := len(src)
	i := 0
	for ; i+lanes <= n; i += lanes {
		composeVec(simd.LoadUint8s(dst[i:]), simd.LoadUint8s(src[i:]), op).Store(dst[i:])
	}
	if i < n {
		d, _ := simd.LoadUint8sPart(dst[i:])
		s, _ := simd.LoadUint8sPart(src[i:])
		composeVec(d, s, op).StorePart(dst[i:])
	}
}

// composeShiftedRunSIMD composes the interior whole destination bytes of one row of an unaligned placement and returns
// the source column to continue from — srcX itself when it declines, which leaves the run to the caller's scalar loop
// exactly as composeShiftedRunScalar would.
//
// The scalar loop it fronts produces one destination byte per iteration, through readBits and a composeByte switch;
// about seven of every eight symbol placements in a text region land off a byte boundary, so that loop is where
// scanned-text pages spend their composition time.
//
// The kernel takes the interior only. dstX must be byte-aligned, which the caller establishes by letting its scalar
// loop write the head partial byte first; the trailing partial byte and anything below a full run go back to that
// loop. Inside the interior srcX and dstX differ by a multiple of eight, so srcX&7 is one value for the whole run and a
// single pair of shift amounts realigns the entire row.
//
// The length gate is tested here as well as at the call site: the call site's copy spares a short row the call, and
// this one makes a direct call — every equivalence test makes them — behave the same way.
//
// Destination byte k reads source bytes k and k+1, so n destination bytes need n+1 source bytes. The last one exists
// whenever shift is nonzero, because the run's final source bit lies in it; the explicit length checks below are belt
// and braces, and declining is always correct because the caller's scalar loop still handles everything.
func composeShiftedRunSIMD(dst *Image, dstY int32, src *Image, srcY, dstX, srcX, endX int32, op ComposeOp) int32 {
	shift := uint(srcX & 7)
	if shift == 0 || dstX&7 != 0 || op < ComposeOr || op > ComposeReplace {
		// A byte-aligned source is composeBytes' job and has no n+1st source byte; an unaligned destination or an op
		// outside the enum is declined too.
		return srcX
	}
	if endX-srcX < composeShiftedRunMin {
		return composeShiftedRunScalar(dst, dstY, src, srcY, dstX, srcX, endX, op)
	}
	n := int((endX - srcX) >> 3)
	if n <= 0 {
		return srcX
	}
	dstRow := dst.row(dstY)
	srcRow := src.row(srcY)
	sq := int(srcX >> 3)
	dq := int(dstX >> 3)
	if sq < 0 || dq < 0 || sq+n+1 > len(srcRow) || dq+n > len(dstRow) {
		return srcX
	}
	srcBytes := srcRow[sq : sq+n+1]
	dstBytes := dstRow[dq : dq+n]
	mul, keep, lowS, hghS := newRealignConstants(shift)
	var probe simd.Uint8s
	lanes := probe.Len()
	j := 0
	for ; j+lanes <= n; j += lanes {
		v := realignBytes(simd.LoadUint8s(srcBytes[j:]), simd.LoadUint8s(srcBytes[j+1:]), mul, keep, lowS, hghS)
		composeVec(simd.LoadUint8s(dstBytes[j:]), v, op).Store(dstBytes[j:])
	}
	// The tail finishes scalar: the same realign a byte at a time, then upstream's composeByte over a full mask. A
	// masked vector store would do it in one pass, but the partial load and store are calls, and four of them on every
	// row of a symbol placement cost more than these few bytes do.
	for ; j < n; j++ {
		dstBytes[j] = composeByte(dstBytes[j], srcBytes[j]<<shift|srcBytes[j+1]>>(8-shift), 0xFF, op)
	}
	return srcX + int32(n)<<3
}

// composeVec applies one JBIG2 composition operator to a vector of destination bytes and the source bytes already
// aligned to them. XNOR has no lane form and is Not(Xor(a, b)).
func composeVec(dst, src simd.Uint8s, op ComposeOp) simd.Uint8s {
	switch op {
	case ComposeOr:
		return dst.Or(src)
	case ComposeAnd:
		return dst.And(src)
	case ComposeXor:
		return dst.Xor(src)
	case ComposeXnor:
		return dst.Xor(src).Not()
	default:
		return src
	}
}

// newRealignConstants builds what realignBytes needs for one shift amount, 1..7, broadcast once per row rather than
// per vector. Four results rather than a struct of four: a struct of vectors comes back through memory and the loop
// reloads it every chunk, while four results stay in registers, which measured about eight percent on a page-width
// row.
func newRealignConstants(shift uint) (mul, keep, lowS, hghS simd.Uint16s) {
	bits := uint16(1<<shift - 1)
	return simd.BroadcastUint16s(1 << shift), simd.BroadcastUint16s(^(bits << 8)), simd.BroadcastUint16s(bits),
		simd.BroadcastUint16s(bits << 8)
}

// realignBytes returns, for every lane, the source byte a scalar readBits would assemble: the low 8-shift bits of a's
// byte moved up, plus the high shift bits of the byte after it, which is b's lane. b is the same row loaded one byte
// later, so lane k of a and lane k of b are adjacent source bytes and no cross-lane shuffle is needed — the portable
// API has none.
//
// Uint8s has no shift, so the work happens in Uint16 lanes over the same bits (a reinterpretation: lane j is bytes 2j
// and 2j+1, little-endian). The simd package's shift-all methods take only a constant shift amount, and this shift is
// the placement's, known at run time, so the left shift is a multiply by 2^shift from a register and the one right
// shift left in the expression is by a literal 8. The alternative, a seven-way unroll with constant shifts per
// placement offset, was rejected: this form costs about five extra lane operations per vector and buys a branch-free
// loop and one copy of the code.
//
// Three terms, each landing bits a per-byte shift would have landed:
//
//   - a*2^shift shifts both of a lane's bytes left in place. The low byte is finished; the high byte is right except
//     for the low byte's top bits spilled into it, which keep clears.
//   - b*2^shift spills into each lane's high byte exactly the top bits of the byte below it, which the low byte's
//     output needs. Shifting that down by 8 puts them in the low byte, where lowS keeps them.
//   - (b>>8)*2^shift isolates the lane's high source byte first, so its own top bits spill up into the high byte
//     rather than being lost, which the high byte's output needs. hghS keeps them.
func realignBytes(a, b simd.Uint8s, mul, keep, lowS, hghS simd.Uint16s) simd.Uint8s {
	av := a.ReshapeToUint16s()
	bv := b.ReshapeToUint16s()
	return av.Mul(mul).And(keep).
		Or(bv.Mul(mul).ShiftAllRight(8).And(lowS)).
		Or(bv.ShiftAllRight(8).Mul(mul).And(hghS)).
		ReshapeToUint8s()
}
