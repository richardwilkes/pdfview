// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package pdfview

// Which of this package's vector kernels are worth dispatching to on amd64. A kernel whose benchmark does not beat
// the scalar code it replaces on a given architecture is turned off here rather than deleted, since the same kernel
// can be the right choice on another one. The values are settled from simd-bench.sh benchstat runs.
//
// Settled 2026-08-21 from a simd-bench.sh run on an Intel Xeon W-2191B (Skylake-X): 1.4x on an opaque page, 4.7x on
// banded translucency, 1.7x on scattered translucency.
const preferUnpremultiply = true
