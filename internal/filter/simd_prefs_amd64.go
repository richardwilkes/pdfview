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

// Which kernels this architecture prefers over the scalar code they replace. See simd_prefs_arm64.go for what settles
// these. Settled 2026-08-21 from a simd-bench.sh run on an Intel Xeon W-2191B (Skylake-X): the row add wins 4.3-6.0x
// in isolation and halves the end-to-end PNG Up predictor time.
const preferAddRows = true
