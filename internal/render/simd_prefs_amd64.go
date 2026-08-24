// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package render

// Which of this package's vector kernels are worth dispatching to on amd64. A kernel whose benchmark does not beat the
// scalar code on an architecture is turned off here rather than deleted, since it can be the right choice on another.
// The values come from simd-bench.sh benchstat runs.
//
// Measured on an Intel Xeon W-2191B (Skylake-X). The finiteness scan wins 3.2-3.7x. The glyph blit passes on the grid's
// geomean (1.15x): 40x50 glyphs win 1.4-1.7x and ramp-heavy coverage wins at every size, but pure zero/full spans on
// 12x16 glyphs regress 18-26% — body-text rows mix zero, ramp, and full inside one span, which lands nearer the ramp
// column, so it stays on; flip it if profiles of small-text pages disagree. The luminosity plane's on-state has no
// amd64 measurement, so it stays off.
const (
	preferCompositeMask = true
	preferMaskLuma      = false
	preferAllFinite     = true
)
