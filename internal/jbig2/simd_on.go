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

// init points the package's dispatch variables at the vector kernels this architecture prefers (see the
// simd_prefs_<arch>.go files). Nothing is repointed unless vecmath.KernelsSupported says the machine can run the
// kernels: that is false both where the simd package emulates every operation in scalar Go, which is slower than the
// scalar code the kernels replace, and on an amd64 CPU with AVX but not AVX2, which the simd package drives in
// hardware even though the kernels' broadcasts would fault there. Past that gate, each kernel is installed only where
// its preference constant says its benchmarks earned it. The equivalence tests call the kernels directly, so they are
// still proven under emulation and on a machine that declines them.
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
// it hands back to the scalar composeBytes, because the setup a kernel pays only comes back on a run of real length.
// It is one vector on this machine: below that the kernel would be its masked tail and nothing else, and the partial
// load and store are calls, which a run of eight bytes cannot pay for. It is a var, not a const, so the equivalence
// tests can force either side of it. The unaligned kernel's gate is in simd_dispatch.go, because the call site tests
// that one too.
var composeBytesMin = 16

// composeBytesSIMD is the vector form of composeBytes: it applies op to whole bytes of two byte-aligned rows. Rows
// too short to earn the setup go back to the scalar composeBytes, so what reaches the vector loop always has at
// least a vector's worth of real work, and the ragged end of a row is one masked load/store pair rather than a byte
// loop. The masked pair is not free — the partial load and store are real calls, not instructions — but with a full
// vector guaranteed ahead of it, it still measured faster here than finishing the row scalar.
//
// Slicing dst to len(src) up front reproduces the scalar loops' behavior when dst is the shorter of the two: they
// index dst by the range over src, so a short dst panics there and must panic here.
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

// composeShiftedRunSIMD composes the interior whole destination bytes of one row of an unaligned placement, and
// returns the source column to continue from — srcX itself when it declines, which leaves the run entirely to the
// caller's scalar loop, exactly as composeShiftedRunScalar would have.
//
// The scalar loop this fronts produces one destination byte per iteration: a readBits call that reassembles the
// source byte from two shifted halves, then composeByte with a switch on op inside the loop. Since roughly seven of
// every eight symbol placements in a text region land off a byte boundary, that loop is where scanned-text pages
// spend their composition time.
//
// The kernel takes the interior only. dstX must be byte-aligned, which the caller establishes by letting its scalar
// loop write the head partial byte first; the trailing partial byte and anything left below a full run go back to
// that same loop. Inside the interior every destination byte covers exactly eight source bits at a fixed offset —
// srcX and dstX differ by a multiple of eight, so srcX&7 is one value for the whole run — which is what lets a single
// pair of shift amounts realign the entire row.
//
// The length gate is tested here as well as at the call site. The call site's copy is what keeps a short row from
// paying for a call; this one is what makes a direct call — every equivalence test makes them — behave the same way.
//
// Bounds: destination byte k reads source bytes k and k+1, so a run of n destination bytes needs n+1 source bytes.
// The last one exists whenever shift is nonzero, because the run's final source bit then lies in it; the explicit
// length checks below are belt and braces, and a declining return is always correct because the caller's scalar loop
// still handles everything.
func composeShiftedRunSIMD(dst *Image, dstY int32, src *Image, srcY, dstX, srcX, endX int32, op ComposeOp) int32 {
	shift := uint(srcX & 7)
	if shift == 0 || dstX&7 != 0 || op < ComposeOr || op > ComposeReplace {
		// A byte-aligned source is composeBytes' job, and it needs no n+1st source byte to do it.
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
	// The bytes past the last whole vector finish scalar, with the same realign written out one byte at a time and
	// upstream's composeByte over a full mask. A masked vector store would do them in one pass, but the partial load
	// and store are calls rather than instructions, and paying for four of them on every row of a symbol placement
	// cost more than these few bytes do.
	for ; j < n; j++ {
		dstBytes[j] = composeByte(dstBytes[j], srcBytes[j]<<shift|srcBytes[j+1]>>(8-shift), 0xFF, op)
	}
	return srcX + int32(n)<<3
}

// composeVec applies one JBIG2 composition operator to a vector of destination bytes and the source bytes already
// aligned to them. XNOR has no direct lane form and is the documented Not(Xor(a, b)).
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

// newRealignConstants builds everything realignBytes needs for one shift amount, which is 1..7. They are broadcast
// once per row, not per vector, and every one of them is a plain runtime value — which is the whole reason the
// realign has the shape it does; see realignBytes.
//
// Four results rather than one struct of four, because a struct of vectors comes back through memory and the loop
// then reloads it every chunk. Four results stay in registers, which measured about eight percent on a page-width
// row.
func newRealignConstants(shift uint) (mul, keep, lowS, hghS simd.Uint16s) {
	bits := uint16(1<<shift - 1)
	return simd.BroadcastUint16s(1 << shift), simd.BroadcastUint16s(^(bits << 8)), simd.BroadcastUint16s(bits),
		simd.BroadcastUint16s(bits << 8)
}

// realignBytes returns, for every lane, the source byte a scalar readBits would have assembled: the low 8-shift bits
// of a's byte moved up, plus the high shift bits of the byte that follows it, which is b's lane. b is the same row
// loaded one byte later, so lane k of a and lane k of b are adjacent source bytes and no cross-lane shuffle is needed
// — which matters, because the portable API has none.
//
// Uint8s carries no shift at all, so the work happens in Uint16 lanes over the same bits: a reinterpretation, not a
// conversion, where lane j is bytes 2j and 2j+1 of the byte view, little-endian. Nor can those lanes be shifted by a
// runtime amount — go1.27's simd package accepts only a literal shift, and this shift is the placement's, known only
// at run time. So the left shift is a multiply by 2^shift, which is the same operation and takes its operand from a
// register, and the one right shift left in the expression is by a literal 8.
//
// The other way out of the literal-only shift is a seven-way unroll, one arm per placement offset, each with its own
// constant shifts. This form was taken instead: it costs about five extra lane operations per vector, and buys a
// branch-free loop and one copy of the code to be right about.
//
// Read it as three terms, each landing bits that a per-byte shift would have landed:
//
//   - a*2^shift gives both of a lane's bytes shifted left in place. The low byte is finished. The high byte is right
//     except that the low byte's top bits spilled into it, which keep clears.
//   - b*2^shift spills, into each lane's high byte, exactly the top bits of the byte below it — the bits the low
//     byte's output needs. Shifting that down by 8 puts them in the low byte, where lowS keeps them and drops the
//     rest.
//   - (b>>8)*2^shift isolates the lane's high source byte first, so its own top bits spill up into the high byte
//     rather than being lost, which is what the high byte's output needs. hghS keeps them.
func realignBytes(a, b simd.Uint8s, mul, keep, lowS, hghS simd.Uint16s) simd.Uint8s {
	av := a.ReshapeToUint16s()
	bv := b.ReshapeToUint16s()
	return av.Mul(mul).And(keep).
		Or(bv.Mul(mul).ShiftAllRight(8).And(lowS)).
		Or(bv.ShiftAllRight(8).Mul(mul).And(hghS)).
		ReshapeToUint8s()
}
