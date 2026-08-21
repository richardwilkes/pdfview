// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd && !amd64 && !arm64

package codestream

// Which kernels this architecture prefers: none of them. Only arm64 and amd64 have been benchmarked, and an
// unmeasured architecture gets the scalar loops rather than a guess. Turn one on here only with numbers from
// simd-bench.sh on that hardware.
const (
	preferApplyRCT      = false
	preferClampPlane    = false
	preferAddClampPlane = false
)
