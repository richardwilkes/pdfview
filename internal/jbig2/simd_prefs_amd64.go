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
// constant cleared there rather than being deleted, so the other architectures keep it. Settled 2026-08-21 from a
// simd-bench.sh run on an Intel Xeon W-2191B (Skylake-X).

const (
	// preferComposeBytes selects composeBytesSIMD for byte-aligned composition: 1.7x at 16 bytes up to 4.2x at 1 KB,
	// with the below-gate cost held to a few nanoseconds by the length gate.
	preferComposeBytes = true
	// preferComposeShiftedRun selects composeShiftedRunSIMD for the interior of an unaligned placement. The PMULLW
	// prediction held: 2.2x at 96 columns and 16.6x at page width, with parity below the gate.
	preferComposeShiftedRun = true
)
