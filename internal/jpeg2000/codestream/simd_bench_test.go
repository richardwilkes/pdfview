// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package codestream

import (
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/engine"
)

// These benchmarks are untagged on purpose: the same bodies run in the default build and under GOEXPERIMENT=simd,
// and the only difference between the two runs is what init pointed the dispatch variables at. That is what makes a
// benchstat delta between the two runs the kernel's contribution and nothing else. See simd-bench.sh.

// benchPlaneSizes are the two plane areas the kernels are measured at: a 512×512 component, which is a typical
// scanned-page tile, and a 2048×2048 one, which is past every level of cache.
var benchPlaneSizes = []struct {
	name string
	n    int
}{
	{"512x512", 512 * 512},
	{"2048x2048", 2048 * 2048},
}

// benchInt32Plane fills a plane with 12-bit-ish signed samples — the range a real component carries, and inside the
// clamp bounds the benchmarks use, so the clamp measures the pass-through case a valid codestream produces.
func benchInt32Plane(n int, seed uint64) []int32 {
	out := make([]int32, n)
	s := seed
	for i := range out {
		s += 0x9E3779B97F4A7C15
		z := s
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		z ^= z >> 31
		out[i] = int32(z%4096) - 2048
	}
	return out
}

// BenchmarkApplyRCTSIMD measures the inverse reversible colour transform through its public entry point, which calls
// the dispatch variable.
func BenchmarkApplyRCTSIMD(b *testing.B) {
	for _, size := range benchPlaneSizes {
		b.Run(size.name, func(b *testing.B) {
			y0 := benchInt32Plane(size.n, 1)
			y1 := benchInt32Plane(size.n, 2)
			y2 := benchInt32Plane(size.n, 3)
			planes := []engine.ComponentPlane{{Pix: y0}, {Pix: y1}, {Pix: y2}}
			b.SetBytes(int64(size.n) * 3 * 4)
			b.ResetTimer()
			for b.Loop() {
				ApplyRCT(planes)
			}
		})
	}
}

// BenchmarkClampPlaneSIMD measures the post-transform range clamp through its dispatch variable. Its call site is a
// few lines inside Decoder.finalizeImage's componentsOnly branch, which a benchmark cannot reach without decoding a
// whole codestream, so the variable is called here the way that site calls it.
func BenchmarkClampPlaneSIMD(b *testing.B) {
	for _, size := range benchPlaneSizes {
		b.Run(size.name, func(b *testing.B) {
			plane := benchInt32Plane(size.n, 4)
			b.SetBytes(int64(size.n) * 4)
			b.ResetTimer()
			for b.Loop() {
				clampPlaneFn(plane, -2048, 2047)
			}
		})
	}
}

// BenchmarkPaletteClampSIMD measures the palette direct-copy channel through its dispatch variable. Like the clamp
// above, the call site sits inside a method that needs a fully configured decoder.
func BenchmarkPaletteClampSIMD(b *testing.B) {
	for _, size := range benchPlaneSizes {
		b.Run(size.name, func(b *testing.B) {
			src := benchInt32Plane(size.n, 5)
			plane := make([]int32, size.n)
			b.SetBytes(int64(size.n) * 4)
			b.ResetTimer()
			for b.Loop() {
				addClampPlaneFn(plane, src, 2048, 0, 4095)
			}
		})
	}
}

// BenchmarkClampPlaneSIMDBelowGate measures a plane one element short of the gate, where the vector kernel hands the
// work straight back to the scalar loop. It is the drop rule's regression check: the vector build must not lose
// measurable time on the inputs it deliberately declines to vectorize.
func BenchmarkClampPlaneSIMDBelowGate(b *testing.B) {
	n := clampPlaneMin - 1
	plane := benchInt32Plane(n, 6)
	b.ReportMetric(float64(n), "elems")
	b.ResetTimer()
	for b.Loop() {
		clampPlaneFn(plane, -2048, 2047)
	}
}
