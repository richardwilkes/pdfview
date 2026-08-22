// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd && !amd64 && !arm64

package jbig2

// Whether each kernel is worth using on an architecture nobody has benchmarked it on. Nothing is: an unmeasured
// kernel does not get to replace scalar code that works, so every architecture beyond amd64 and arm64 keeps the
// scalar path until someone settles its values from simd-bench.sh benchstat results.

const (
	// preferComposeBytes selects composeBytesSIMD for byte-aligned composition.
	preferComposeBytes = false
	// preferComposeShiftedRun selects composeShiftedRunSIMD for the interior of an unaligned placement.
	preferComposeShiftedRun = false
)
