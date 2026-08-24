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
	"simd/archsimd"
	"testing"
)

// TestKernelsSupportedNeedsAVX2 pins the amd64 floor: KernelsSupported must say no whenever AVX2 is missing, since
// the simd package drives an AVX-only CPU in hardware and the kernels' broadcasts would fault there, and yes whenever
// AVX2 is present and the simd package is not emulating, so a later edit cannot retreat to a stricter floor (FMA,
// AVX-512) and switch the kernels off on hardware that runs them.
func TestKernelsSupportedNeedsAVX2(t *testing.T) {
	want := !simd.Emulated() && archsimd.X86.AVX2()
	if got := KernelsSupported(); got != want {
		t.Fatalf("KernelsSupported=%v, want %v (Emulated=%v AVX=%v AVX2=%v)", got, want, simd.Emulated(),
			archsimd.X86.AVX(), archsimd.X86.AVX2())
	}
}
