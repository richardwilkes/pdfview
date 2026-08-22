// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package jbig2

import (
	"bytes"
	"reflect"
	"simd"
	"testing"
)

// The kernels have to be bit-identical to the scalar code, not merely close, because a JBIG2 page is a bitmap: one
// wrong bit is a visible speck, and the corpus pins in pdfview_test.go compare whole pages byte for byte.
//
// Rather than reimplement the scalar path as a reference — where a shared misreading would agree with itself — every
// test here runs the same entry point twice on identical input, once with the dispatch variables holding their
// scalar defaults and once with them holding the kernels, and requires the same bytes out. Each comparison is run at
// three gate settings: open, so the kernels take every run they can legally take, including lengths the shipping
// gates would never hand them; the shipping values; and closed, which proves the fallback each kernel takes below
// its own gate.
//
// These tests call the kernels whether or not init installed them, so a machine with emulated lanes still proves
// them — that is where a lane-order or spill-mask mistake shows up.

// gateSettings are the three length-gate configurations every equivalence sweep runs under. The middle one reads
// the package's own gate values rather than repeating them, so it follows any tuning; it is initialized before any
// test can move them.
var gateSettings = []struct {
	name     string
	bytesMin int
	runMin   int32
}{
	{name: "open", bytesMin: 0, runMin: 0},
	{name: "shipped", bytesMin: composeBytesMin, runMin: composeShiftedRunMin},
	{name: "closed", bytesMin: 1 << 30, runMin: 1 << 30},
}

// TestKernelsWiredWithExperiment pins that init installed exactly the kernels this architecture prefers, which is
// what makes the benchmarks and the corpus pins measure and cover the vector path in this build. A kernel its
// architecture declines must leave the scalar function in place, untouched.
func TestKernelsWiredWithExperiment(t *testing.T) {
	if simd.Emulated() {
		t.Skip("vector units are emulated here, so init deliberately leaves the scalar dispatch in place")
	}
	for _, entry := range []struct {
		name      string
		got       uintptr
		kernel    uintptr
		scalar    uintptr
		preferred bool
	}{
		{
			name:      "composeBytesFn",
			got:       reflect.ValueOf(composeBytesFn).Pointer(),
			kernel:    reflect.ValueOf(composeBytesSIMD).Pointer(),
			scalar:    reflect.ValueOf(composeBytes).Pointer(),
			preferred: preferComposeBytes,
		},
		{
			name:      "composeShiftedRunFn",
			got:       reflect.ValueOf(composeShiftedRunFn).Pointer(),
			kernel:    reflect.ValueOf(composeShiftedRunSIMD).Pointer(),
			scalar:    reflect.ValueOf(composeShiftedRunScalar).Pointer(),
			preferred: preferComposeShiftedRun,
		},
	} {
		want, which := entry.scalar, "its scalar default, which this architecture prefers"
		if entry.preferred {
			want, which = entry.kernel, "its kernel"
		}
		if entry.got != want {
			t.Errorf("%s does not hold %s", entry.name, which)
		}
	}
}

// TestComposeBytesKernelMatchesScalar sweeps the byte-aligned kernel against the scalar function it replaces, across
// every length from empty to past four vectors — which covers every tail remainder the machine's vector width can
// produce — and at every gate setting.
func TestComposeBytesKernelMatchesScalar(t *testing.T) {
	restoreDispatch(t)
	var rng prng = 11
	lengths := make([]int, 0, 80)
	for n := range 71 {
		lengths = append(lengths, n)
	}
	lengths = append(lengths, 127, 128, 129, 319, 320, 1024)
	for _, gate := range gateSettings {
		composeBytesMin = gate.bytesMin
		for _, entry := range composeOpNames {
			for _, n := range lengths {
				src := make([]byte, n)
				base := make([]byte, n)
				rng.fill(src)
				rng.fill(base)
				want := bytes.Clone(base)
				composeBytes(want, src, entry.op)
				got := bytes.Clone(base)
				composeBytesSIMD(got, src, entry.op)
				if !bytes.Equal(got, want) {
					t.Fatalf("composeBytesSIMD %s len %d gate %s: got %x, want %x", entry.name, n, gate.name,
						got, want)
				}
			}
		}
	}
}

// TestComposeToMatchesScalar is the whole-placement sweep: every operator, every horizontal offset through two full
// bytes in each direction — so every combination of destination bit offset and source bit offset, including the
// negative offsets that clip the source's left edge and the ones that push it off the right — and every source width
// from one pixel to past four vectors. Three rows catch a kernel that walked into the next row's bytes, and the
// vertical offset makes the destination row differ from the source row.
func TestComposeToMatchesScalar(t *testing.T) {
	restoreDispatch(t)
	var rng prng = 12
	for _, gate := range gateSettings {
		composeBytesMin, composeShiftedRunMin = gate.bytesMin, gate.runMin
		for _, entry := range composeOpNames {
			for x := int32(-17); x <= 17; x++ {
				for w := int32(1); w <= 70; w++ {
					src := randomImage(&rng, w, 3)
					dst := randomImage(&rng, w+9, 5)
					want := composeWithDispatch(dst, src, x, 1, entry.op, false)
					got := composeWithDispatch(dst, src, x, 1, entry.op, true)
					if !bytes.Equal(got, want) {
						t.Fatalf("ComposeTo %s x=%d w=%d gate %s: got %x, want %x", entry.name, x, w, gate.name,
							got, want)
					}
				}
			}
		}
	}
}

// TestComposeToPageWidthMatchesScalar runs the same comparison at a full page width, where the shifted kernel takes
// whole vectors instead of only its masked tail, and at every bit offset. The destination is narrower than the
// source plus the offset for some of these, so the right-edge clip is exercised at page scale too.
func TestComposeToPageWidthMatchesScalar(t *testing.T) {
	restoreDispatch(t)
	var rng prng = 13
	src := randomImage(&rng, 2550, 4)
	dst := randomImage(&rng, 2554, 6)
	for _, gate := range gateSettings {
		composeBytesMin, composeShiftedRunMin = gate.bytesMin, gate.runMin
		for _, entry := range composeOpNames {
			for x := int32(-8); x <= 8; x++ {
				want := composeWithDispatch(dst, src, x, 1, entry.op, false)
				got := composeWithDispatch(dst, src, x, 1, entry.op, true)
				if !bytes.Equal(got, want) {
					t.Fatalf("ComposeTo %s x=%d gate %s at page width: first difference at byte %d", entry.name, x,
						gate.name, firstDifference(got, want))
				}
			}
		}
	}
}

// TestSubImageMatchesScalar covers the path SubImage takes: a Replace composition at a negative offset into a fresh
// bitmap, which is how a symbol is cut out of a collective bitmap, at every bit offset and every width.
func TestSubImageMatchesScalar(t *testing.T) {
	restoreDispatch(t)
	var rng prng = 14
	src := randomImage(&rng, 300, 4)
	for _, gate := range gateSettings {
		composeBytesMin, composeShiftedRunMin = gate.bytesMin, gate.runMin
		for x := int32(0); x < 16; x++ {
			for w := int32(1); w <= 70; w++ {
				setDispatch(false)
				want := src.SubImage(x, 1, w, 2)
				setDispatch(true)
				got := src.SubImage(x, 1, w, 2)
				if !bytes.Equal(got.data, want.data) {
					t.Fatalf("SubImage x=%d w=%d gate %s: got %x, want %x", x, w, gate.name, got.data, want.data)
				}
			}
		}
	}
}

// TestRealignBytesMatchesScalarShift pins the realign step itself against the per-byte shift it stands in for. The
// whole-placement tests would catch a mistake here, but only as a wrong page; this says which lane and which shift.
//
// The interesting inputs are the ones that light up the bits the spill masks are supposed to clear, so alongside
// generated bytes it runs the patterns whose top and bottom bits are set.
func TestRealignBytesMatchesScalarShift(t *testing.T) {
	var probe simd.Uint8s
	lanes := probe.Len()
	var rng prng = 15
	a := make([]byte, lanes)
	b := make([]byte, lanes)
	out := make([]byte, lanes)
	patterns := [][2]byte{
		{0x00, 0xFF}, {0xFF, 0x00}, {0x01, 0x80}, {0x80, 0x01},
		{0x0F, 0xF0}, {0xF0, 0x0F}, {0x55, 0xAA}, {0xAA, 0x55},
	}
	for shift := uint(1); shift <= 7; shift++ {
		for _, pair := range patterns {
			for i := range a {
				a[i], b[i] = pair[0], pair[1]
			}
			checkRealign(t, shift, a, b, out)
		}
		// A three-clause loop, not "for range 32". This function names a vector type, so the compiler clones it per
		// vector width, and go1.27's cloner dereferences a nil left-hand side when it deep-copies a range clause
		// that declares no variable — an internal compiler error, not a diagnostic.
		for trial := 0; trial < 32; trial++ {
			rng.fill(a)
			rng.fill(b)
			checkRealign(t, shift, a, b, out)
		}
	}
}

// checkRealign runs one realign over a and b and compares every lane against the per-byte shift it stands for.
func checkRealign(t *testing.T, shift uint, a, b, out []byte) {
	t.Helper()
	mul, keep, lowS, hghS := newRealignConstants(shift)
	realignBytes(simd.LoadUint8s(a), simd.LoadUint8s(b), mul, keep, lowS, hghS).Store(out)
	for i, got := range out {
		if want := a[i]<<shift | b[i]>>(8-shift); got != want {
			t.Fatalf("realignBytes shift %d lane %d: a=%08b b=%08b, got %08b, want %08b",
				shift, i, a[i], b[i], got, want)
		}
	}
}

// clone returns a deep copy of an image, so a compose can be replayed onto identical starting bits.
func (i *Image) clone() *Image {
	return &Image{width: i.width, height: i.height, stride: i.stride, data: bytes.Clone(i.data)}
}

// restoreDispatch puts the package's real dispatch and gates back after a test has moved them.
func restoreDispatch(t *testing.T) {
	t.Helper()
	bytesFn, runFn := composeBytesFn, composeShiftedRunFn
	bytesMin, runMin := composeBytesMin, composeShiftedRunMin
	t.Cleanup(func() {
		composeBytesFn, composeShiftedRunFn = bytesFn, runFn
		composeBytesMin, composeShiftedRunMin = bytesMin, runMin
	})
}

// setDispatch points the dispatch variables at the kernels or at their scalar defaults.
func setDispatch(kernels bool) {
	if kernels {
		composeBytesFn, composeShiftedRunFn = composeBytesSIMD, composeShiftedRunSIMD
		return
	}
	composeBytesFn, composeShiftedRunFn = composeBytes, composeShiftedRunScalar
}

// composeWithDispatch composes src onto a copy of dst with the chosen dispatch, and returns the resulting bits.
func composeWithDispatch(dst, src *Image, x, y int32, op ComposeOp, kernels bool) []byte {
	work := dst.clone()
	setDispatch(kernels)
	src.ComposeTo(work, x, y, op)
	return work.data
}

// firstDifference returns the index of the first byte that differs, or -1. Page-width bitmaps are too large to dump.
func firstDifference(a, b []byte) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	if len(b) > len(a) {
		return len(a)
	}
	return -1
}
