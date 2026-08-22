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

// Which kernels this architecture prefers over the scalar code they replace. See simd_prefs_arm64.go for what settles
// these. Settled 2026-08-21 from a simd-bench.sh run on an Intel Xeon W-2191B (Skylake-X): the polarity invert wins
// 4.3x, the stencil threshold 4.9-32x, and the /SMask composite 6.9-23x on every mask shape — its scalar baseline is
// far more branch-bound on this microarchitecture than on the arm64 reference, so even the shape that loses on arm64
// wins big here. The JPX 1-component normalization managed only 1.02-1.03x, below the 1.10x bar, so it is off; its
// scalar byte-narrow tail dominates on this box.
const (
	preferInvertBytes    = true
	preferThreshold      = true
	preferNormalizePlane = false
	preferCompositeAlpha = true
)
