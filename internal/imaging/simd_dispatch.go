// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package imaging

// The dispatch variables for this package's vectorizable loops. Each defaults to the portable scalar implementation,
// which is what the call sites call. Under the goexperiment.simd build tag, simd_on.go's init repoints them at the
// vector kernels unless the target only emulates vector operations, where the kernels would be slower. Every kernel
// carries its own gate and hands work it does not want back to the scalar function, so switching these is safe at any
// size and for any argument shape.
//
// simd_wiring_test.go locks the default wiring; simd_equiv_test.go locks the experiment's.
var (
	invertBytesFn    = invertBytesScalar
	thresholdFn      = thresholdScalar
	normalizePlaneFn = normalizePlaneScalar
	compositeAlphaFn = compositeAlphaScalar
)
