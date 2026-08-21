// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package wavelet

// Dispatch variables for the vertical halves of the two inverse transforms. Each one defaults to the scalar
// implementation that sits beside the loop it came from, so the default build calls exactly the code it always did
// through one indirect call per sweep. Under GOEXPERIMENT=simd, simd_on.go's init repoints them at the vector
// kernels — unless the simd package is emulating vectors in software, where the scalar loops are faster and the
// defaults stand.
//
// The dispatch is per sweep, never per row: a whole sweep is hundreds of rows, so the indirect call is free, while a
// per-row one cost the scalar path around 16% at row widths below the gate — a call in the row-loop body is enough
// to change how that loop is compiled.
var (
	sub53SweepFn   = sub53SweepScalar
	add53SweepFn   = add53SweepScalar
	scale97SweepFn = scale97SweepScalar
)

// Row-width gates. Each vector sweep hands anything narrower than its gate straight back to the scalar sweep: below
// these widths the per-row call into the row kernel costs more than the vector arithmetic saves. They are declared
// here, in the untagged file, so the benchmarks and the wiring tests can see them in both builds, and they are vars
// rather than consts so the equivalence tests can drive both sides of every gate.
//
// Measured crossovers on an M4 Max, sweeping W with the gates forced to 1: the 5/3 sweeps break even at W=24 and are
// 6% ahead at 32, 16% at 64, 31% at 128; the 9/7 scaling sweep — float64, so two lanes to the 5/3's four — is still
// 14% behind at W=64, breaks even near 96, and peaks around 14-19% ahead at 128-256. The gates sit at the first
// width where each is clearly ahead rather than at break-even, because everything below the gate pays one extra
// non-inlined call per sweep (about 2ns against a sweep that costs microseconds) and nothing else.
var (
	sub53RowMin   = 32
	add53RowMin   = 32
	scaleRow97Min = 128
)
