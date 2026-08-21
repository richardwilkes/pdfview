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
// these. The amd64 values are provisional mirrors of arm64's until they are benchmarked on real amd64 hardware.
const (
	preferInvertBytes    = true
	preferThreshold      = true
	preferNormalizePlane = true
	preferCompositeAlpha = true
)
