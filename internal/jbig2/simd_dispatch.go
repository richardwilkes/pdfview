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
// Dispatch works through the function variables below. Each one starts out holding the scalar behavior that shipped
// before any of this existed, and the init in simd_on.go — which only that build has, which only runs on real vector
// hardware, and which consults the architecture's preference constants — repoints it at a kernel. So the composition
// sites in the vendored code call one variable unconditionally, with no build tags and no mode test of their own,
// and a build without the experiment is the scalar code it always was.
//
// This file also holds fillBytes, which has no vector form: the doubling copy it uses rides runtime.memmove, and no
// portable-SIMD store loop measured faster than that.

// composeBytesFn composes whole bytes of two byte-aligned rows. It is the scalar composeBytes until a vector build
// points it at composeBytesSIMD.
var composeBytesFn = composeBytes

// composeShiftedRunFn composes the interior whole destination bytes of an unaligned run, returning the source column
// to continue from. The default consumes nothing, which leaves the caller's scalar loop doing all of the work, as it
// did before there were kernels; a vector build points this at composeShiftedRunSIMD.
var composeShiftedRunFn = composeShiftedRunScalar

// composeShiftedRunMin is the smallest unaligned run, in source columns, that composeShiftedRunSIMD handles; below
// it the scalar loop composes the whole run. Eight destination bytes is where the kernel starts winning, and the
// gate is tested at the call site as well as inside the kernel: a dispatch variable costs a call whatever it holds,
// and on the very short rows symbol placement produces that call is a measurable part of the row. Testing it in both
// places keeps one value in charge, so a test that moves it moves both.
var composeShiftedRunMin = int32(64)

// fillBytesMin is the smallest run, in bytes, that fillBytes fills with the doubling copy; below it the plain loop
// wins. It must be at least one: at and above it, the doubling copy assumes a byte to seed. It is a var, not a
// const, so the test can force either side of it. composeBytesMin, which only the kernel that reads it can see,
// lives beside that kernel in simd_on.go.
var fillBytesMin = 32

// composeShiftedRunScalar is the default composeShiftedRunFn: it consumes nothing and reports the source column it
// was given, so the caller's byte-at-a-time loop composes the entire run exactly as it always has.
func composeShiftedRunScalar(_ *Image, _ int32, _ *Image, _, _, srcX, _ int32, _ ComposeOp) int32 {
	return srcX
}

// fillBytes sets every byte of b to v.
//
// Go recognizes a range loop that stores zero and turns it into a memclr, but it has no such rewrite for any other
// value, so the byte-at-a-time loops this replaced ran one store per byte. Seeding one byte and doubling the filled
// prefix with copy hands the work to runtime.memmove instead, which is vectorized on every port. Short runs keep the
// plain loop: a doubling copy costs a call per step, and for a handful of bytes that is more than the stores it saves.
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
