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

import "simd"

// KernelsSupported reports whether this module's vector kernels may run on the machine the program is on. It is the
// one question every package's init asks before it points a dispatch variable at a kernel, and two things have to
// hold for a yes.
//
// The simd package must be driving real vector hardware rather than emulating lanes in scalar Go, because the
// emulation is slower than the scalar code the kernels replace. simd.Emulated answers that.
//
// The hardware must also carry every instruction the kernels compile to, which simd.Emulated alone does not promise.
// On amd64 the simd package hands its 128-bit hardware tier to any CPU with AVX, but the broadcasts it emits for
// Broadcast<Type>s — VBROADCASTSS, VPBROADCASTD and their siblings — and its masked partial loads and stores
// (VPMASKMOVD) are AVX2, so a CPU with AVX and no AVX2 (Sandy Bridge, Ivy Bridge, AMD's Bulldozer family) passes the
// emulation check and then dies with an illegal instruction on a kernel's first vector op. archKernelsSupported, in
// the per-architecture files beside this one, states each port's instruction-set floor.
func KernelsSupported() bool {
	return !simd.Emulated() && archKernelsSupported()
}
