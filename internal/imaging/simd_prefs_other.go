// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd && !amd64 && !arm64

package imaging

// Which kernels this architecture prefers over the scalar code they replace. See simd_prefs_arm64.go for what settles
// these. No architecture outside amd64 and arm64 has been benchmarked, so every kernel stays off and the dispatch
// variables keep their scalar defaults.
const (
	preferInvertBytes    = false
	preferThreshold      = false
	preferNormalizePlane = false
	preferCompositeAlpha = false
)
