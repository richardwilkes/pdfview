// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package wavelet

// Which sweeps this architecture prefers. A false constant leaves that dispatch variable on its scalar default, so a
// kernel that loses its benchmark is switched off here rather than deleted — the code, its equivalence tests, and its
// benchmark all stay, and the decision is one word.
//
// These values are settled per architecture from simd-bench.sh benchstat results; nothing here should be changed on
// a hunch. Settled 2026-08-21 on an Apple M4 Max: the 5/3 sweeps hold 1.48-1.53x inside the full vertical synthesis,
// so they stay on. The 9/7 scale sweep is float64 — two lanes per 128-bit vector — and measured 1.06-1.19x in
// isolation but only 1.00-1.01x inside the full 9/7 vertical, below the 1.10x bar, so it is off here. Its kernel,
// tests, and benchmark remain for a retune on wider vectors.
const (
	preferSub53Sweep   = true
	preferAdd53Sweep   = true
	preferScale97Sweep = false
)
