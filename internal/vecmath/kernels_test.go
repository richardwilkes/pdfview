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

import (
	"simd"
	"testing"
)

// TestKernelsSupportedRefusesEmulation pins the half of KernelsSupported's contract every architecture shares: an
// emulated simd package never gets the kernels, whatever the architecture's own floor says. GODEBUG=simd=0 forces
// emulation on any machine.
func TestKernelsSupportedRefusesEmulation(t *testing.T) {
	t.Logf("simd.Emulated=%v simd.VectorBitSize=%d KernelsSupported=%v", simd.Emulated(), simd.VectorBitSize(),
		KernelsSupported())
	if simd.Emulated() && KernelsSupported() {
		t.Fatal("KernelsSupported is true while the simd package is emulating vectors")
	}
}
