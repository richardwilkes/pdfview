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
// These values are settled per architecture from simd-bench.sh benchstat results. Settled 2026-08-21 on an Intel
// Xeon W-2191B (Skylake-X): RCT 1.4-1.5x, plane clamp 1.5-1.8x, palette add+clamp 1.8-2.0x. The below-gate clamp
// call costs ~2.5 ns more than the inline scalar loop, a one-time cost per tiny plane that the gate makes irrelevant.
const (
	preferApplyRCT      = true
	preferClampPlane    = true
	preferAddClampPlane = true
)
