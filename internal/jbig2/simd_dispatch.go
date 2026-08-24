// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package jbig2

// This file and the simd_* files beside it are pdfview-authored (MPL-2.0), not upstream code.
//
// Dispatch runs through the function variables below. Each defaults to the upstream scalar function; the init in
// simd_on.go, built only under goexperiment.simd, repoints it at a kernel on hardware that can run one and where the
// architecture's preference constant selects it. The composition sites in the vendored code call one variable
// unconditionally, with no build tags or mode test of their own, so a build without the experiment is the scalar code
// it always was.
//
// fillBytes has no vector form: its doubling copy rides runtime.memmove, and no portable-SIMD store loop measured
// faster than that.

// composeBytesFn composes whole bytes of two byte-aligned rows. It is the scalar composeBytes until a vector build
// points it at composeBytesSIMD.
var composeBytesFn = composeBytes

// composeShiftedRunFn composes the interior whole destination bytes of an unaligned run and returns the source column
// to continue from. The default consumes nothing, leaving the caller's scalar loop the whole run; a vector build
// points this at composeShiftedRunSIMD.
var composeShiftedRunFn = composeShiftedRunScalar

// composeShiftedRunMin is the smallest unaligned run, in source columns, that composeShiftedRunSIMD handles; below it
// the scalar loop composes the whole run. Eight destination bytes is where the kernel starts winning. The gate is
// tested at the call site as well as inside the kernel: a dispatch variable costs a call whatever it holds, and on the
// very short rows symbol placement produces that call is a measurable part of the row.
var composeShiftedRunMin = int32(64)

// fillBytesMin is the smallest run, in bytes, that fillBytes fills with the doubling copy; below it the plain loop
// wins. It must be at least one, because the doubling copy seeds b[0]. It is a var so the test can force either side.
// composeBytesMin lives beside its kernel in simd_on.go, the only non-test code that reads it.
var fillBytesMin = 32

// composeShiftedRunScalar is the default composeShiftedRunFn: it consumes nothing and returns srcX, so the caller's
// byte-at-a-time loop composes the entire run.
func composeShiftedRunScalar(_ *Image, _ int32, _ *Image, _, _, srcX, _ int32, _ ComposeOp) int32 {
	return srcX
}

// fillBytes sets every byte of b to v.
//
// Go rewrites a range loop that stores zero into a memclr but has no such rewrite for any other value, so a plain
// loop is one store per byte. Seeding one byte and doubling the filled prefix with copy hands the work to
// runtime.memmove, which is vectorized on every port. Short runs keep the plain loop: each doubling step is a call,
// which a handful of bytes cannot pay for.
func fillBytes(b []byte, v byte) {
	if len(b) < fillBytesMin {
		for i := range b {
			b[i] = v
		}
		return
	}
	b[0] = v
	for i := 1; i < len(b); i <<= 1 {
		copy(b[i:], b[:i])
	}
}
