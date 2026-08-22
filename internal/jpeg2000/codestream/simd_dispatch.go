// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package codestream

// Dispatch variables for this package's per-sample loops. Each one defaults to the scalar implementation that sits
// beside the loop it came from, so the default build calls exactly the code it always did through one indirect call
// per plane. Under GOEXPERIMENT=simd, simd_on.go's init repoints them at the vector kernels — unless the simd package
// is emulating vectors in software, where the scalar loops are faster and the defaults stand.
//
// The dispatch is per plane, never per sample: an indirect call the size of a whole component plane costs nothing
// measurable, and the call sites stay one token different from upstream's.
var (
	applyRCTFn      = applyRCTScalar
	clampPlaneFn    = clampPlaneScalar
	addClampPlaneFn = addClampPlaneScalar
)

// Length gates. Each vector kernel hands anything shorter than its gate back to the scalar function: below these
// element counts the vector form has to pay for the loop setup before it wins anything back. They are declared here,
// in the untagged file, so the benchmarks and the wiring tests can see them in both builds, and they are vars rather
// than consts so the equivalence tests can drive both sides of every gate.
//
// 8 is where the vector clamp measured even with the scalar loop on an M4 Max; it is ahead from 8 upward and behind
// below it (n=6 was 19% slower, n=2 twice as slow). Everything under the gate costs one extra non-inlined call —
// about 2ns, once per plane, which no decode will ever notice.
var (
	applyRCTMin      = 8
	clampPlaneMin    = 8
	addClampPlaneMin = 8
)
