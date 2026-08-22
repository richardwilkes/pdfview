// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package imaging

import (
	"simd"

	"github.com/richardwilkes/pdfview/internal/vecmath"
)

// init swaps the dispatch variables over to the vector kernels this architecture prefers. simd.Emulated is true on a
// target whose vector operations the toolchain lowers to scalar loops, where every kernel is strictly slower than the
// portable code it replaces, so the scalar defaults stay in place wholesale there; past that, each kernel is decided
// on its own benchmarked merits by the constants in simd_prefs_<arch>.go.
func init() {
	if simd.Emulated() {
		return
	}
	if preferInvertBytes {
		invertBytesFn = invertBytesSIMD
	}
	if preferThreshold {
		thresholdFn = thresholdSIMD
	}
	if preferNormalizePlane {
		normalizePlaneFn = normalizePlaneSIMD
	}
	if preferCompositeAlpha {
		compositeAlphaFn = compositeAlphaSIMD
	}
}

// The gates each kernel applies to its own work before deciding to vector-process it. Below its gate a kernel hands
// the call straight back to the scalar function, because the setup costs more than the few lanes it would save. They
// are vars rather than consts so the equivalence tests can sweep both sides of each gate. Each is measured in the
// units its kernel counts: bytes for the two byte kernels, samples for the JPX normalizer, and whole pixels for the
// alpha composite.
var (
	invertBytesMin    = 16
	thresholdMin      = 16
	normalizePlaneMin = 32
	compositeAlphaMin = 16
)

// invertBytesSIMD is invertBytesScalar a vector at a time: the ones' complement of src into dst.
func invertBytesSIMD(dst, src []byte) {
	if len(dst) < invertBytesMin {
		invertBytesScalar(dst, src)
		return
	}
	src = src[:len(dst)]
	var probe simd.Uint8s
	lanes := probe.Len()
	i := 0
	for ; i+lanes <= len(dst); i += lanes {
		simd.LoadUint8s(src[i:]).Not().Store(dst[i:])
	}
	if i < len(dst) {
		v, _ := simd.LoadUint8sPart(src[i:])
		v.Not().StorePart(dst[i:])
	}
}

// thresholdSIMD is thresholdScalar a vector at a time: 255 into dst wherever gray's sample is below 128 and 0
// elsewhere, or the reverse when invert is set.
//
// Uint8s has no ordered comparison, so the "< 128" test is the sign-bit test v&0x80 == 0, which is exact for this one
// threshold and no other. invert is loop-invariant, so it is hoisted into a swap of the IfElse arms rather than
// checked per lane. The scalar function only ever stores 255, leaving the rest of a freshly allocated (so zero-filled)
// plane alone; writing the zeros back explicitly is what makes the vector form a plain full-width store.
func thresholdSIMD(dst, gray []byte, invert bool) {
	if len(dst) < thresholdMin {
		thresholdScalar(dst, gray, invert)
		return
	}
	gray = gray[:len(dst)]
	hi, lo := simd.BroadcastUint8s(255), simd.BroadcastUint8s(0)
	if invert {
		hi, lo = lo, hi
	}
	sign := simd.BroadcastUint8s(0x80)
	zero := simd.BroadcastUint8s(0)
	var probe simd.Uint8s
	lanes := probe.Len()
	i := 0
	for ; i+lanes <= len(dst); i += lanes {
		hi.IfElse(simd.LoadUint8s(gray[i:]).And(sign).Equal(zero), lo).Store(dst[i:])
	}
	if i < len(dst) {
		v, _ := simd.LoadUint8sPart(gray[i:])
		hi.IfElse(v.And(sign).Equal(zero), lo).StorePart(dst[i:])
	}
}

// int32Safe reports whether this normalizer's arithmetic fits the 32-bit lanes normalizePlaneSIMD computes in. Every
// precision through 31 does: clamping the sample into [−offset, maxVal−offset] before the offset is added bounds
// every intermediate by 2^p−1, and the two clamp bounds are ±2^(p−1) at worst. Precision 32 does not — its offset
// alone is 2^31 — and stays on the int64 scalar path, where it has always been. It lives here rather than beside
// jpxNorm because nothing in the default build has any use for it.
func (n jpxNorm) int32Safe() bool {
	return n.offset <= 1<<30
}

// normalizeChunk is how many samples normalizePlaneSIMD converts per pass. The portable API has no narrowing lane
// conversion, so the int32 results have to land somewhere a scalar loop can pick their low bytes out of; a chunk of
// this size keeps that staging buffer (1KB) inside L1 no matter how large the plane is.
const normalizeChunk = 256

// normalizePlaneSIMD is normalizePlaneScalar with the clamp and offset done a vector at a time.
//
// jpxNorm.at clamps v+offset into [0, maxVal] in int64. The kernel clamps v into [−offset, maxVal−offset] first and
// adds the offset afterwards, which yields the same value — the clamp is monotone and the offset is a constant — with
// every intermediate inside int32. See jpxNorm.int32Safe for why that is enough, and why precision 32 is handed back
// to the scalar function along with any plane too short to be worth the setup.
//
// The precision shift is not part of the vector half. ShiftAll* is a compiler intrinsic whose distance has to be a
// compile-time constant, and this one is the component's precision, known only at run time. It costs nothing to fold
// into the narrowing pass, which is already scalar for want of a narrowing lane conversion.
func normalizePlaneSIMD(dst []byte, samples []int32, n jpxNorm) {
	if len(dst) < normalizePlaneMin || !n.int32Safe() {
		normalizePlaneScalar(dst, samples, n)
		return
	}
	samples = samples[:len(dst)]
	lo := simd.BroadcastInt32s(int32(-n.offset))
	hi := simd.BroadcastInt32s(int32(n.maxVal - n.offset))
	offset := simd.BroadcastInt32s(int32(n.offset))
	shift := n.shift
	var probe simd.Int32s
	lanes := probe.Len()
	var scratch [normalizeChunk]int32
	for base := 0; base < len(dst); base += normalizeChunk {
		end := min(base+normalizeChunk, len(dst))
		buf := scratch[:end-base]
		src := samples[base:end]
		i := 0
		for ; i+lanes <= len(buf); i += lanes {
			simd.LoadInt32s(src[i:]).Max(lo).Min(hi).Add(offset).Store(buf[i:])
		}
		if i < len(buf) {
			v, _ := simd.LoadInt32sPart(src[i:])
			v.Max(lo).Min(hi).Add(offset).StorePart(buf[i:])
		}
		out := dst[base:end]
		if n.up {
			for j, v := range buf {
				out[j] = byte(v << shift)
			}
		} else {
			for j, v := range buf {
				out[j] = byte(v >> shift)
			}
		}
	}
}

// maxVectorBytes bounds the machine's widest byte vector, which the experiment's API caps at 512 bits. It sizes the
// one-vector landing pad andBytes reduces through; a wider vector than this would overrun it, so the length is
// asserted rather than assumed.
const maxVectorBytes = 64

// andBytes returns the bitwise AND of every byte of b, or 255 for an empty slice. It is how compositeAlphaSIMD
// recognizes a fully opaque run: the AND is 255 exactly when every byte is.
func andBytes(b []byte) byte {
	acc := byte(0xff)
	var probe simd.Uint8s
	lanes := probe.Len()
	i := 0
	if lanes <= maxVectorBytes && len(b) >= lanes {
		v := simd.BroadcastUint8s(0xff)
		for ; i+lanes <= len(b); i += lanes {
			v = v.And(simd.LoadUint8s(b[i:]))
		}
		var out [maxVectorBytes]byte
		v.Store(out[:lanes])
		for _, x := range out[:lanes] {
			acc &= x
		}
	}
	for ; i < len(b); i++ {
		acc &= b[i]
	}
	return acc
}

// compositeAlphaChunk is how many pixels one pass of compositeAlphaSIMD widens into its staging buffer. The portable
// API has no widening lane conversion, so the mask's bytes reach the 32-bit lanes through a scalar fill; a chunk of
// this size keeps that buffer (2KB) and the 8KB of pixels it scales inside L1 no matter how large the image is.
const compositeAlphaChunk = 512

// compositeAlphaSIMD specializes compositeAlphaScalar's equal-dimension case, where the mask matches the image pixel
// for pixel and no sampling is needed: the plane and the image's alpha bytes advance in lockstep, so the whole
// composite is one flat pass. Every other shape — a mask coarser than the image on either axis, an image whose pixel
// buffer is short, or a plane too small to be worth the setup — goes back to the scalar function.
//
// Pixels are worked on as packed little-endian words (R, G, B, A from the low byte up, which is what a reshape of the
// byte lanes yields), so the alpha byte is a shift away and the other three channels ride through untouched under an
// AND. The divide is vecmath.UDiv255, exact over this product's domain: two byte channels top out at 65025.
//
// The scalar function skips pixels whose plane byte is 255 rather than computing them. Computing them anyway is safe
// because alpha*255/255 == alpha exactly, so the skip changes nothing but speed. Per pixel that skip is not something
// a vector kernel can keep, but per chunk it is, and it is worth keeping: real soft masks are smooth, so a fully
// opaque run of them is common and costs only the scan that recognizes it. The same scan supplies the HasAlpha
// answer, which is likewise not per pixel — the flag goes up when any plane byte differs from 255, which is exactly
// what an AND across the plane reports.
func compositeAlphaSIMD(img *Image, plane []byte, mw, mh int) {
	n := img.Width * img.Height
	if n < compositeAlphaMin || mw != img.Width || mh != img.Height || len(img.Pix) < n*4 {
		compositeAlphaScalar(img, plane, mw, mh)
		return
	}
	pix := img.Pix[:n*4]
	plane = plane[:n]
	rgb := simd.BroadcastUint32s(0x00ffffff)
	var probe simd.Uint32s
	lanes := probe.Len()
	var scratch [compositeAlphaChunk]uint32
	acc := byte(0xff)
	for base := 0; base < len(plane); base += compositeAlphaChunk {
		end := min(base+compositeAlphaChunk, len(plane))
		chunk := plane[base:end]
		all := andBytes(chunk)
		acc &= all
		if all == 0xff {
			continue // Every pixel here would be multiplied by 255/255, which is what it already holds.
		}
		buf := scratch[:end-base]
		for i, a := range chunk {
			buf[i] = uint32(a)
		}
		words := pix[base*4 : end*4]
		i := 0
		for ; i+lanes <= len(buf); i += lanes {
			w := simd.LoadUint8s(words[i*4:]).ReshapeToUint32s()
			a := vecmath.UDiv255(w.ShiftAllRight(24).Mul(simd.LoadUint32s(buf[i:])))
			w.And(rgb).Or(a.ShiftAllLeft(24)).ReshapeToUint8s().Store(words[i*4:])
		}
		if i < len(buf) {
			part, _ := simd.LoadUint8sPart(words[i*4:])
			w := part.ReshapeToUint32s()
			m, _ := simd.LoadUint32sPart(buf[i:])
			a := vecmath.UDiv255(w.ShiftAllRight(24).Mul(m))
			w.And(rgb).Or(a.ShiftAllLeft(24)).ReshapeToUint8s().StorePart(words[i*4:])
		}
	}
	if acc != 0xff {
		img.HasAlpha = true
	}
}
