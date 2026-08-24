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

// Whether each kernel is worth using on arm64. A kernel that loses its benchmark on one architecture gets its
// constant cleared there rather than deleted, so the other architectures keep it. These values are settled per
// architecture from simd-bench.sh benchstat results.

const (
	// preferComposeBytes selects composeBytesSIMD for byte-aligned composition.
	preferComposeBytes = true
	// preferComposeShiftedRun selects composeShiftedRunSIMD for the interior of an unaligned placement.
	preferComposeShiftedRun = true
)
