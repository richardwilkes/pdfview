// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package jbig2

// Whether each kernel is worth using on amd64. A kernel that loses its benchmark on one architecture gets its
// constant cleared there rather than being deleted, so the other architectures keep it. These values are settled per
// architecture from simd-bench.sh benchstat results; these are provisional mirrors of the arm64 values until they
// are benchmarked on real amd64 hardware.

const (
	// preferComposeBytes selects composeBytesSIMD for byte-aligned composition.
	preferComposeBytes = true
	// preferComposeShiftedRun selects composeShiftedRunSIMD for the interior of an unaligned placement. The realign
	// step leans on 16-bit lane multiplies, which amd64 has as PMULLW, so the shape should carry over; the shifted
	// benchmarks are the ones to watch when this is settled for real.
	preferComposeShiftedRun = true
)
