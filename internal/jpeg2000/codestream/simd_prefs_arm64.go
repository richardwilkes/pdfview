// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package codestream

// Which kernels this architecture prefers. A false constant leaves that dispatch variable on its scalar default, so a
// kernel that loses its benchmark is switched off here rather than deleted — the code, its equivalence tests, and its
// benchmark all stay, and the decision is one word.
//
// These values are settled per architecture from simd-bench.sh benchstat results. The coordinator settles the arm64
// values once this area has landed; nothing here should be changed on a hunch.
const (
	preferApplyRCT      = true
	preferClampPlane    = true
	preferAddClampPlane = true
)
