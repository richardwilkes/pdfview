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

// Which of this package's vector kernels are worth dispatching to on arm64. A kernel whose benchmark does not beat
// the scalar code it replaces on a given architecture is turned off here rather than deleted, since the same kernel
// can be the right choice on another one. The values are settled from simd-bench.sh benchstat runs.
//
// arm64 values are settled by the coordinator from benchstat runs on this repo's reference arm64 machine.
const preferUnpremultiply = true
