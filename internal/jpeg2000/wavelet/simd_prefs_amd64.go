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
// These values are settled per architecture from simd-bench.sh benchstat results. They are provisional mirrors of the
// arm64 file until somebody runs simd-bench.sh on real amd64 hardware. The 9/7 scaling sweep is the one whose answer
// is most likely to differ: on arm64 it measured below the bar (1.00-1.01x in context, two float64 lanes) and is off,
// but 256-bit vectors double its lane count, so retest it first on AVX2 hardware.
const (
	preferSub53Sweep   = true
	preferAdd53Sweep   = true
	preferScale97Sweep = false
)
