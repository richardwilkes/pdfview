// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package vecmath

import "simd/archsimd"

// archKernelsSupported reports whether this CPU has every instruction the module's kernels compile to on amd64. AVX2
// (Haswell, 2013, and every Intel and AMD core since) is the floor. A disassembly of every kernel's 128- and 256-bit
// variant shows nothing past it: the arithmetic, compares, shifts, blends and converts are AVX, while the broadcasts
// (VPBROADCASTB/W/D/Q, and VBROADCASTSS/SD from a register), the masked partial loads and stores (VPMASKMOVD/Q), the
// 256-bit integer lanes and VEXTRACTI128 are AVX2. Nothing is AVX-512, and nothing is a fused multiply-add, so FMA is
// deliberately not required: demanding it would switch the kernels off — and skip their wiring tests — under
// Rosetta 2, which offers AVX2 without FMA.
func archKernelsSupported() bool {
	return archsimd.X86.AVX2()
}
