// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd && !amd64 && !arm64

package vecmath

// archKernelsSupported reports whether this CPU has every instruction the module's kernels compile to. On the ports
// without a file of their own the simd package either emulates every operation or drives a vector unit whose baseline
// covers everything it emits (wasm's simd128 is all-or-nothing at instantiation), so its emulation verdict is the
// whole answer.
func archKernelsSupported() bool {
	return true
}
