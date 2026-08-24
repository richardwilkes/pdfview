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
// benchmark all stay.
//
// Settled from simd-bench.sh benchstat results on an Intel Xeon W-2191B (Skylake-X): the 5/3 vertical sweeps lost
// 10-13% against the scalar loops there — the arm64 win does not carry over — so both are off. The 9/7 scale sweep
// ran with its constant already false, so its on-state has no amd64 measurement; it stays off. The whole package's
// scalar code was a few percent slower under the experiment build on that box even with every kernel off, so a retest
// must compare against the default build, not only in-binary A/B.
const (
	preferSub53Sweep   = false
	preferAdd53Sweep   = false
	preferScale97Sweep = false
)
