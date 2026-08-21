// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package render

import (
	"encoding/binary"
	"math"
	"simd"
	"unsafe"

	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/vecmath"
)

// init points the package's dispatch variables at the vector kernels this architecture prefers (see
// simd_prefs_arm64.go and its siblings). On hardware with no vector unit the simd package emulates every operation in
// scalar Go, which is slower than the scalar code it would replace, so nothing is repointed there at all.
func init() {
	if simd.Emulated() {
		return
	}
	if preferCompositeMask {
		compositeMaskSpanFn = compositeMaskSpanSIMD
	}
	if preferMaskLuma {
		lumaPlaneFn = maskLumaPlaneSIMD
	}
	if preferAllFinite {
		allFiniteFn = allFiniteSIMD
	}
}

// Length gates: below these the per-call setup (broadcasts, the scratch fill, the reduction) costs more than the
// scalar function the kernel replaces, so the kernel calls that function instead. They are vars so the equivalence
// tests can sweep both sides of each.
var (
	// compositeMaskSpanMin is the shortest clipped glyph span, in pixels, the vector blit handles itself. Only the
	// slivers a clip leaves at a glyph's edge are shorter, and there the group machinery's fixed cost is the whole
	// cost.
	compositeMaskSpanMin = 4
	// maskLumaMin is the smallest luminosity plane, in pixels, the vector weighted sum handles itself.
	maskLumaMin = 64
	// allFiniteMin is the shortest point run the vector finiteness scan handles itself.
	allFiniteMin = 32
)

// maskLumaChunk is how many pixels maskLumaPlaneSIMD converts per pass. The vector half writes its quotients to a
// scratch row and the LUT gather then walks that row scalar-wise, so the row is deliberately small enough to stay in
// L1 between the two passes rather than sized to the whole plane (which is a full mask bbox, often megapixels).
const maskLumaChunk = 256

// compositeMaskGroup is how many pixels the blit scans and then acts on at a time. A glyph's coverage plane is mostly
// runs of 0 outside the outline and runs of 255 inside it, with the interesting values only along the edges, so the
// kernel looks at a group before touching it. 64 pixels measured best of 16, 32 and 64 on this repo's arm64 machine:
// it is wider than most glyph spans, so a span is usually one group and the scan, the widening and the arithmetic
// each run once over it, and it is still fine-grained enough that a big glyph's transparent margin is skipped rather
// than composited. Every vector width the simd package reports divides it evenly, so a group is a whole number of
// vectors.
const compositeMaskGroup = 64

// compositeMaskSpanSIMD is the vector form of compositeMaskSpanScalar, with identical results for every input. drow
// is the clipped destination span and cov the matching run of coverage bytes, which are widened into a group-sized
// scratch on the stack on the way to 32-bit lanes — the portable simd API has no u8→u32 widen, and a group is small
// enough that the scratch never leaves L1 or the frame.
//
// The scalar loop's c == 0 and c == 255 arms are special cases of its general formula, not different math, so the
// vector arithmetic below drops the switch entirely: with inv = 255-c, c == 0 gives (dst*255 + 127)/255 == dst, and
// c == 255 gives (src*255 + 127)/255 == src in every channel (255 for alpha, which is the srcWord the scalar arm
// stores). TestCompositeMaskSwitchArmsMatchFormula pins both.
//
// The switch still earns its keep a group at a time, though, which is why the group scan is here: what makes the
// scalar loop fast on glyph coverage is that it writes nothing for a transparent pixel and one word for an opaque
// one, and a vector kernel that ran the full arithmetic over every pixel regardless would lose to it on everything
// but the antialiased edges. Or-ing and and-ing the group's bytes together — eight at a time through a uint64, so the
// scan is a fraction of an operation per pixel — says which of the three cases the whole group is in.
func compositeMaskSpanSIMD(drow []uint32, cov []byte, srcR, srcG, srcB uint32) {
	if len(drow) < compositeMaskSpanMin {
		compositeMaskSpanScalar(drow, cov, srcR, srcG, srcB)
		return
	}
	n := min(len(drow), len(cov))
	// Every broadcast below sits inside the branch that needs it: a span is one or two groups wide at body text
	// sizes, so hoisting them here would build all four for spans that turn out to need none (Go's SSA has neither
	// loop-invariant code motion nor sinking, so where they are written is where they are built).
	var probe simd.Uint32s
	lanes := probe.Len()
	for base := 0; base < n; base += compositeMaskGroup {
		end := min(base+compositeMaskGroup, n)
		group := cov[base:end]
		or, and := scanCoverage(group)
		if or == 0 { // Fully transparent: the scalar loop writes nothing here, so neither does this.
			continue
		}
		dst := drow[base:end]
		if and == ^uint64(0) { // Fully opaque: every pixel becomes the source word.
			sw := simd.BroadcastUint32s(srcR | srcG<<8 | srcB<<16 | 0xff<<24)
			i := 0
			for ; i+lanes <= len(dst); i += lanes {
				sw.Store(dst[i:])
			}
			if i < len(dst) {
				sw.StorePart(dst[i:])
			}
			continue
		}
		// The widened coverage is declared here rather than at the top of the function so that a group the two
		// branches above dispose of never pays for zeroing it.
		var cov32 [compositeMaskGroup]uint32
		for i, c := range group {
			cov32[i] = uint32(c)
		}
		sr := simd.BroadcastUint32s(srcR)
		sg := simd.BroadcastUint32s(srcG)
		sb := simd.BroadcastUint32s(srcB)
		i := 0
		for ; i+lanes <= len(dst); i += lanes {
			compositeMaskVec(simd.LoadUint32s(dst[i:]), simd.LoadUint32s(cov32[i:]), sr, sg, sb).Store(dst[i:])
		}
		if i < len(dst) {
			dv, _ := simd.LoadUint32sPart(dst[i:])
			cv, _ := simd.LoadUint32sPart(cov32[i:len(dst)])
			compositeMaskVec(dv, cv, sr, sg, sb).StorePart(dst[i:])
		}
	}
}

// scanCoverage reduces a run of coverage bytes to the or and the and of all of them, eight bytes at a time: the run
// is entirely transparent exactly when or is 0, and entirely opaque exactly when and has every bit set. The byte
// order the load uses does not matter, since nothing here reads a position back — and neither does reading a byte
// twice, which is what lets a run that does not divide by eight finish with a load that overlaps the one before it
// rather than a byte-at-a-time loop. A run shorter than eight bytes has no load to overlap, so its bytes are
// broadcast into all eight positions instead, which folds them into the same pair of accumulators.
func scanCoverage(cov []byte) (or, and uint64) {
	const ones = 0x0101010101010101
	and = ^uint64(0)
	i := 0
	for ; i+8 <= len(cov); i += 8 {
		w := binary.NativeEndian.Uint64(cov[i:])
		or |= w
		and &= w
	}
	if i == len(cov) {
		return or, and
	}
	if len(cov) >= 8 {
		w := binary.NativeEndian.Uint64(cov[len(cov)-8:])
		return or | w, and & w
	}
	for ; i < len(cov); i++ {
		w := uint64(cov[i]) * ones
		or |= w
		and &= w
	}
	return or, and
}

// compositeMaskVec source-over-composites one vector of premultiplied destination pixels with the solid source color,
// one pixel per 32-bit lane and one coverage value per lane in c.
func compositeMaskVec(dst, c, srcR, srcG, srcB simd.Uint32s) simd.Uint32s {
	full := simd.BroadcastUint32s(0xff)
	inv := full.Sub(c)
	r := compositeMaskChannel(srcR, c, dst.And(full), inv)
	g := compositeMaskChannel(srcG, c, dst.ShiftAllRight(8).And(full), inv)
	b := compositeMaskChannel(srcB, c, dst.ShiftAllRight(16).And(full), inv)
	// The source alpha is 0xff (the coverage plane is tinted by an opaque color) and the destination's alpha byte is
	// the word's high byte, so it needs no masking after the shift.
	a := compositeMaskChannel(full, c, dst.ShiftAllRight(24), inv)
	return r.Or(g.ShiftAllLeft(8)).Or(b.ShiftAllLeft(16)).Or(a.ShiftAllLeft(24))
}

// compositeMaskChannel is the lane-wise (src*c + dst*inv + 127) / 255 of the scalar blit. The largest numerator is
// 0xff*c + 0xff*(255-c) + 127 = 65152, inside the domain vecmath.UDiv255 is proven exact over.
func compositeMaskChannel(src, c, dst, inv simd.Uint32s) simd.Uint32s {
	return vecmath.UDiv255(src.Mul(c).Add(dst.Mul(inv)).Add(simd.BroadcastUint32s(127)))
}

// maskLumaPlaneSIMD is the vector form of lumaPlaneScalar, with identical results for every input. It converts
// chunk-sized runs of the RGBA readback in pix to maskLuma's weighted quotient in a scratch row on the stack, then
// walks each run scalar-wise through maskNeutralLUT into plane — the portable simd API has no gather, so the LUT
// lookup stays where it is.
func maskLumaPlaneSIMD(plane, pix []byte) {
	if len(plane) < maskLumaMin {
		lumaPlaneScalar(plane, pix)
		return
	}
	var scratch [maskLumaChunk]uint32
	full := simd.BroadcastUint32s(0xff)
	wr := simd.BroadcastUint32s(78)
	wg := simd.BroadcastUint32s(159)
	wb := simd.BroadcastUint32s(15)
	bias := simd.BroadcastUint32s(126)
	var probe simd.Uint32s
	lanes := probe.Len()
	for base := 0; base < len(plane); base += maskLumaChunk {
		n := min(maskLumaChunk, len(plane)-base)
		src := pix[base*4:]
		i := 0
		for ; i+lanes <= n; i += lanes {
			maskLumaVec(simd.LoadUint8s(src[i*4:]), full, wr, wg, wb, bias).Store(scratch[i:])
		}
		if i < n {
			// The zero-filled lanes past the end of the run convert to 0 and are never read back: StorePart writes
			// only the n-i entries the run actually has.
			v, _ := simd.LoadUint8sPart(src[i*4 : n*4])
			maskLumaVec(v, full, wr, wg, wb, bias).StorePart(scratch[i:n])
		}
		// The index is masked to a byte to index the LUT without a bounds check: the largest quotient the weights
		// can produce is (252*255 + 126)/252 = 255, so the mask never changes a value, it just says so in a way the
		// compiler can use.
		out := plane[base : base+n]
		for j, t := range scratch[:n] {
			out[j] = maskNeutralLUT[byte(t)]
		}
	}
}

// maskLumaVec is the lane-wise (78*r + 159*g + 15*b + 126) / 252 of maskLuma over one vector of RGBA pixels, one pixel
// per 32-bit lane (little-endian, so the red byte is the lane's low byte). The largest numerator is 252*255 + 126 =
// 64386, inside the domain vecmath.UDiv252 is proven exact over.
func maskLumaVec(v simd.Uint8s, full, wr, wg, wb, bias simd.Uint32s) simd.Uint32s {
	px := v.ReshapeToUint32s()
	r := px.And(full).Mul(wr)
	g := px.ShiftAllRight(8).And(full).Mul(wg)
	b := px.ShiftAllRight(16).And(full).Mul(wb)
	return vecmath.UDiv252(r.Add(g).Add(b).Add(bias))
}

// pointFloats is how many float32 coordinates a gfx.Point holds. The blank constant beside it fails to compile if the
// struct ever stops being exactly that many packed float32s, which is the layout allFiniteSIMD reinterprets.
const (
	pointFloats = 2
	_           = uint(unsafe.Sizeof(gfx.Point{}) - pointFloats*unsafe.Sizeof(float32(0)))
	_           = uint(pointFloats*unsafe.Sizeof(float32(0)) - unsafe.Sizeof(gfx.Point{}))
)

// allFiniteReduceMax is the widest vector, in 32-bit lanes, allFiniteSIMD can reduce at the end of its scan. 16 covers
// every vector length the simd package reports today (512 bits); a wider machine takes the scalar path rather than
// silently ignoring the lanes past the buffer.
const allFiniteReduceMax = 16

// allFiniteSIMD is the vector form of allFiniteScalar, with identical results for every input. gfx.Point is two packed
// float32s (asserted above), so the points' backing array is a contiguous run of coordinates that can be scanned
// without regard to which coordinate belongs to which point.
//
// NaN fails every ordered comparison and ±Inf fails this one, so a single |v| <= MaxFloat32 mask catches both, and
// and-accumulating the masks defers the whole decision to one reduction at the end. That is the same answer the scalar
// loop's early return gives: both report whether ANY coordinate is non-finite, and neither has side effects.
func allFiniteSIMD(pts []gfx.Point) bool {
	var probe simd.Float32s
	lanes := probe.Len()
	if lanes > allFiniteReduceMax || len(pts) == 0 || len(pts) < allFiniteMin {
		return allFiniteScalar(pts)
	}
	coords := unsafe.Slice((*float32)(unsafe.Pointer(&pts[0])), len(pts)*pointFloats)
	limit := simd.BroadcastFloat32s(math.MaxFloat32)
	acc := simd.BroadcastInt32s(-1)
	i := 0
	for ; i+lanes <= len(coords); i += lanes {
		acc = acc.And(simd.LoadFloat32s(coords[i:]).Abs().LessEqual(limit).ToInt32s())
	}
	if i < len(coords) {
		// The zero-filled lanes past the end are finite, so they leave the accumulator alone.
		v, _ := simd.LoadFloat32sPart(coords[i:])
		acc = acc.And(v.Abs().LessEqual(limit).ToInt32s())
	}
	var reduce [allFiniteReduceMax]int32
	acc.StorePart(reduce[:])
	for _, v := range reduce[:lanes] {
		if v == 0 {
			return false
		}
	}
	return true
}
