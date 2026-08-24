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

// Which of this package's vector kernels are worth dispatching to on arm64; see simd_prefs_amd64.go for the policy. The
// values come from simd-bench.sh benchstat runs on the reference arm64 machine.
const preferUnpremultiply = true
