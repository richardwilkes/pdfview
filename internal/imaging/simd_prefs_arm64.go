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

// Which kernels this architecture prefers over the scalar code they replace. The values are settled per architecture
// from simd-bench.sh benchstat results, not from a portable rule: a kernel that loses its benchmark here is turned
// off here and left on elsewhere, rather than deleted.
//
// Measured on an Apple M4 Max (128-bit vectors), a page-scale run of each: the JBIG2 polarity flip 5.5x, the DCT
// stencil threshold 11x against a smooth plane and 85x against noise (the scalar form's branch is what it beats), the
// JPX 1-component normalization 1.3x, and the equal-dimension /SMask composite 2.9x on a real soft mask's shape but
// 0.8x on a mask whose opaque pixels are scattered rather than in runs. The composite's spread is why these are
// constants rather than a rule.
const (
	preferInvertBytes    = true
	preferThreshold      = true
	preferNormalizePlane = true
	preferCompositeAlpha = true
)
