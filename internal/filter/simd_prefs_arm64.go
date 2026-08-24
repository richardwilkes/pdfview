// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package filter

// Which kernels this architecture prefers over the scalar code they replace. The values are settled per architecture
// from simd-bench.sh benchstat results, not from a portable rule: a kernel that loses its benchmark here is turned off
// here and left on elsewhere, rather than deleted.
//
// Measured on an Apple M4 Max (128-bit vectors): the PNG Up reconstruction runs 7.3x faster in vector form at every
// realistic row width, and 2.6x end to end through pngPredictor.
const preferAddRows = true
