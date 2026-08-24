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

// Which of this package's vector kernels are worth dispatching to on arm64. A kernel whose benchmark does not beat the
// scalar code on an architecture is turned off here rather than deleted, since it can be the right choice on another.
// The values come from simd-bench.sh benchstat runs.
//
// Measured on an Apple M4 Max: the glyph blit wins up to 1.6x on realistic coverage shapes and the finiteness scan wins
// ~2x, so both stay on. The luminosity plane lost a reproducible 2.5% at 512x512 and 1024x1024 — the scalar LUT gather
// is half the loop and cannot vectorize, and the scalar weighted sum is already cheap — so it is off; its kernel,
// tests, and benchmark stay for a retune on wider vectors.
const (
	preferCompositeMask = true
	preferMaskLuma      = false
	preferAllFinite     = true
)
