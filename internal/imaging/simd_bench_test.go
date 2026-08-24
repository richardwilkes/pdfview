// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package imaging

import (
	"fmt"
	"testing"

	"github.com/richardwilkes/pdfview/internal/testrand"
)

// BenchmarkInvertBytesSIMD measures the JBIG2 polarity flip at the row width a 300 dpi bilevel scan of a letter page
// produces (2550 columns packed one bit per pixel, so 319 bytes), over a page's worth of rows. The kernel is
// dispatched per row, as decodeJBIG2Plane does it, so the per-call overhead is part of the measurement.
func BenchmarkInvertBytesSIMD(b *testing.B) {
	const (
		rowBytes = 319
		rows     = 3300
	)
	rng := testrand.Rand(0xb0d1)
	src := make([]byte, rowBytes*rows)
	dst := make([]byte, rowBytes*rows)
	rng.Fill(src)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for b.Loop() {
		for y := range rows {
			invertBytesFn(dst[y*rowBytes:(y+1)*rowBytes], src[y*rowBytes:(y+1)*rowBytes])
		}
	}
}

// BenchmarkThresholdSIMD measures the DCT stencil threshold over a letter page of 150 dpi gray samples, on two data
// shapes. Noise makes the scalar loop's branch unpredictable; the gradient is the opposite extreme, one flip per row,
// where the branch predictor gets everything right. A real stencil sits between them, so the pair brackets the win
// instead of quoting only the flattering half.
func BenchmarkThresholdSIMD(b *testing.B) {
	const (
		w = 1275
		h = 1650
	)
	rng := testrand.Rand(0xb0d2)
	noise := make([]byte, w*h)
	rng.Fill(noise)
	gradient := make([]byte, w*h)
	for i := range gradient {
		gradient[i] = byte(i % w * 256 / w)
	}
	alpha := make([]byte, w*h)
	for _, shape := range []struct {
		name string
		gray []byte
	}{{name: "noise", gray: noise}, {name: "gradient", gray: gradient}} {
		b.Run(shape.name, func(b *testing.B) {
			b.SetBytes(int64(w * h))
			b.ResetTimer()
			for b.Loop() {
				thresholdFn(alpha, shape.gray, false)
			}
		})
	}
}

// BenchmarkNormalizePlaneSIMD measures the JPX 1-component normalization over a 1024x1024 plane at the precisions a
// real codestream carries: 8 (no shift), 12 (a right shift), and 16 (a wider one). The samples span exactly the
// component's own range, so the clamps behave as they do in practice rather than saturating on every lane.
func BenchmarkNormalizePlaneSIMD(b *testing.B) {
	const count = 1024 * 1024
	dst := make([]byte, count)
	for _, precision := range []int{8, 12, 16} {
		b.Run(fmt.Sprintf("precision=%d", precision), func(b *testing.B) {
			rng := testrand.Rand(0xb0d3)
			norm := newJPXNorm(precision)
			span := uint32(norm.maxVal + 1)
			in := make([]int32, count)
			for i := range in {
				in[i] = int32(uint32(rng.Next())%span) - int32(norm.offset)
			}
			b.SetBytes(int64(count))
			b.ResetTimer()
			for b.Loop() {
				normalizePlaneFn(dst, in, norm)
			}
		})
	}
}

// BenchmarkCompositeAlphaSIMD measures the /SMask composite on the equal-dimension path over a 1024x1024 image, the
// arrangement every soft mask that matches its image lands on. Three mask shapes bracket the answer: noise, where the
// scalar loop's a == 255 early-out never fires; a vignette, the shape a real soft mask has, whose opaque middle is
// contiguous enough for the kernel's own run skip to find; and a scattered mask that is opaque except for one pixel
// in sixteen, which defeats both skips and is the kernel's worst case.
//
// The image is not restored between iterations. Nothing here is timing-dependent on the pixel values: the vector arm
// is branch-free and the scalar arm branches only on the mask, which never changes.
func BenchmarkCompositeAlphaSIMD(b *testing.B) {
	const (
		w = 1024
		h = 1024
	)
	rng := testrand.Rand(0xb0d4)
	noise := make([]byte, w*h)
	rng.Fill(noise)
	vignette := make([]byte, w*h)
	for y := range h {
		edge := min(y, h-1-y)
		v := byte(255)
		if edge < h/8 {
			v = byte(edge * 255 / (h / 8))
		}
		for x := range w {
			vignette[y*w+x] = v
		}
	}
	scattered := make([]byte, w*h)
	for i := range scattered {
		if scattered[i] = 255; i%16 == 0 {
			scattered[i] = byte(rng.Next())
		}
	}
	for _, shape := range []struct {
		name  string
		plane []byte
	}{{name: "noise", plane: noise}, {name: "vignette", plane: vignette}, {name: "scattered", plane: scattered}} {
		b.Run(shape.name, func(b *testing.B) {
			pix := make([]byte, w*h*4)
			rng.Fill(pix)
			img := &Image{Pix: pix, Width: w, Height: h}
			b.SetBytes(int64(w * h * 4))
			b.ResetTimer()
			for b.Loop() {
				compositeAlpha(img, shape.plane, w, h)
			}
		})
	}
}
