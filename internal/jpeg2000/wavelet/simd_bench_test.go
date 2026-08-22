// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package wavelet

import (
	"strconv"
	"testing"

	"github.com/richardwilkes/pdfview/internal/testrand"
)

// These benchmarks are untagged on purpose: the same bodies run in the default build and under GOEXPERIMENT=simd,
// and the only difference between the two runs is what init pointed the dispatch variables at. That is what makes a
// benchstat delta between the two runs the kernel's contribution and nothing else. See simd-bench.sh.

// benchVerticalWidths are the row widths the vertical sweeps are measured at. The height is fixed at benchVerticalH,
// split evenly between low and high rows, which is the shape every decomposition level of a real tile has.
var benchVerticalWidths = []int{256, 1024, 2048}

const benchVerticalH = 512

// BenchmarkInverse53VerticalSIMD measures the pair of 5/3 vertical sweeps in isolation — the exact function that
// calls the two int32 dispatch variables. The buffer is transformed in place and never reset: the coefficients
// wander (and wrap) across iterations, which costs int32 arithmetic nothing and keeps the measurement free of a
// memcpy the kernels would otherwise be diluted by.
func BenchmarkInverse53VerticalSIMD(b *testing.B) {
	for _, w := range benchVerticalWidths {
		b.Run("W="+strconv.Itoa(w), func(b *testing.B) {
			rnd := testrand.Rand(11)
			out := make([]int32, w*benchVerticalH)
			for i := range out {
				out[i] = int32(rnd.Next()%(1<<21)) - (1 << 20)
			}
			hlv := benchVerticalH / 2
			hhv := benchVerticalH - hlv
			b.SetBytes(int64(len(out)) * 4)
			b.ResetTimer()
			for b.Loop() {
				inverse53VerticalCas0(out, w, hlv, hhv)
			}
		})
	}
}

// BenchmarkInverse97VerticalSIMD measures the 9/7 vertical half: two scaling sweeps (vectorized) and four lifting
// sweeps (scalar; see liftRow97SIMD for why). Unlike the 5/3 case the buffer must be restored between iterations —
// the scaling sweeps multiply by K and 1/K, so repeated application would run the coefficients out to infinity and
// down into the denormal range, and denormal arithmetic would dominate the measurement. The restore happens with
// the timer stopped so it is not attributed to the transform.
func BenchmarkInverse97VerticalSIMD(b *testing.B) {
	for _, w := range benchVerticalWidths {
		b.Run("W="+strconv.Itoa(w), func(b *testing.B) {
			rnd := testrand.Rand(13)
			pristine := make([]float64, w*benchVerticalH)
			for i := range pristine {
				pristine[i] = float64(int64(rnd.Next()%2000001)-1000000) / 1024
			}
			out := make([]float64, len(pristine))
			hlv := benchVerticalH / 2
			hhv := benchVerticalH - hlv
			b.SetBytes(int64(len(out)) * 8)
			b.ResetTimer()
			for b.Loop() {
				b.StopTimer()
				copy(out, pristine)
				b.StartTimer()
				inverse97VerticalCas0(out, w, hlv, hhv)
			}
		})
	}
}

// BenchmarkSynthesize53SIMD measures a whole 5/3 subband synthesis, horizontal pass included, so the vertical
// kernels' share of a real decomposition level is visible rather than only their share of the sweep.
func BenchmarkSynthesize53SIMD(b *testing.B) {
	for _, w := range benchVerticalWidths {
		b.Run("W="+strconv.Itoa(w), func(b *testing.B) {
			ll, lh, hl, hh := benchBands53(w, benchVerticalH)
			b.SetBytes(int64(w) * benchVerticalH * 4)
			b.ResetTimer()
			for b.Loop() {
				_ = Synthesize53(ll, lh, hl, hh)
			}
		})
	}
}

// BenchmarkSynthesize97SIMD is the 9/7 counterpart of BenchmarkSynthesize53SIMD. Its input bands are read-only, so
// no restore is needed: Synthesize97 transforms into a fresh output buffer each call.
func BenchmarkSynthesize97SIMD(b *testing.B) {
	for _, w := range benchVerticalWidths {
		b.Run("W="+strconv.Itoa(w), func(b *testing.B) {
			ll, lh, hl, hh := benchBands97(w, benchVerticalH)
			b.SetBytes(int64(w) * benchVerticalH * 8)
			b.ResetTimer()
			for b.Loop() {
				_ = Synthesize97(ll, lh, hl, hh)
			}
		})
	}
}

// BenchmarkInverse53VerticalSIMDBelowGate runs a row width one element short of the 5/3 gates, where the vector
// sweeps hand the work straight back to the scalar sweeps. It is the drop rule's regression check: the vector build
// must not lose measurable time on the inputs it deliberately declines to vectorize.
func BenchmarkInverse53VerticalSIMDBelowGate(b *testing.B) {
	w := sub53RowMin - 1
	rnd := testrand.Rand(17)
	out := make([]int32, w*benchVerticalH)
	for i := range out {
		out[i] = int32(rnd.Next()%(1<<21)) - (1 << 20)
	}
	hlv := benchVerticalH / 2
	hhv := benchVerticalH - hlv
	b.ReportMetric(float64(w), "cols")
	b.ResetTimer()
	for b.Loop() {
		inverse53VerticalCas0(out, w, hlv, hhv)
	}
}
