// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package jbig2

import (
	"bytes"
	"fmt"
	"testing"
)

// This file carries no build tag on purpose: every benchmark here runs the same code in the default build and under
// GOEXPERIMENT=simd, so one body measures both arms and benchstat can difference them. It also holds what the tagged
// equivalence tests share with the benchmarks — the PRNG and the image builders — because a tagged file cannot be
// seen from an untagged one, and the fill test, which runs in both builds because fillBytes is scalar code compiled
// into both.

// composeOpNames pairs each composition operator with a name for benchmark and failure output. Replace is included
// because the unaligned kernel handles it too: SubImage composes with it at whatever bit offset the caller asked for.
var composeOpNames = []struct {
	name string
	op   ComposeOp
}{
	{name: "Or", op: ComposeOr},
	{name: "And", op: ComposeAnd},
	{name: "Xor", op: ComposeXor},
	{name: "Xnor", op: ComposeXnor},
	{name: "Replace", op: ComposeReplace},
}

// prng is a splitmix64 generator: reproducible bytes without math/rand, which gosec G404 forbids in this repo.
type prng uint64

func (p *prng) next() uint64 {
	*p += 0x9e3779b97f4a7c15
	z := uint64(*p)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (p *prng) fill(b []byte) {
	for i := range b {
		b[i] = byte(p.next() >> 24)
	}
}

// randomImage returns a w by h image of generated bits, padding bits past the right edge included, so a kernel that
// touches padding shows up as a difference.
func randomImage(p *prng, w, h int32) *Image {
	img := NewImage(w, h)
	p.fill(img.data)
	return img
}

// TestFillBytesMatchesBytewise proves the doubling copy fills exactly what a byte-at-a-time loop fills, on both sides
// of its length gate and at every length through several doublings. It is untagged because fillBytes is scalar code
// on the default decode path (Image.Fill, Image.Expand, and the MMR black-run fill).
func TestFillBytesMatchesBytewise(t *testing.T) {
	original := fillBytesMin
	t.Cleanup(func() { fillBytesMin = original })
	for _, gate := range []int{1, 2, 32, 1 << 30} {
		fillBytesMin = gate
		for n := range 200 {
			want := make([]byte, n+8)
			got := make([]byte, n+8)
			for i := range want {
				want[i] = 0x5A
				got[i] = 0x5A
			}
			for i := 4; i < n+4; i++ {
				want[i] = 0xFF
			}
			fillBytes(got[4:n+4], 0xFF)
			if !bytes.Equal(got, want) {
				t.Fatalf("fillBytes(len=%d) with gate %d: got %x, want %x", n, gate, got, want)
			}
		}
	}
}

// BenchmarkComposeBytesSIMD measures the byte-aligned composition path at the row lengths JBIG2 actually produces:
// 8 and 16 bytes are symbol rows, 320 is a full-width row of a 2550 pixel page, and 1024 is a wide region.
func BenchmarkComposeBytesSIMD(b *testing.B) {
	var rng prng = 1
	for _, size := range []int{8, 16, 64, 320, 1024} {
		src := make([]byte, size)
		dst := make([]byte, size)
		rng.fill(src)
		rng.fill(dst)
		for _, entry := range composeOpNames {
			if entry.op == ComposeReplace {
				continue // Replace is a copy in both arms; there is nothing to compare.
			}
			b.Run(fmt.Sprintf("%s/n%d", entry.name, size), func(b *testing.B) {
				for b.Loop() {
					composeBytesFn(dst, src, entry.op)
				}
			})
		}
	}
}

// BenchmarkComposeShiftedSIMD measures the unaligned placement path at every bit offset. Widths 16 and 64 are symbol
// rows, placed thousands of times per page, and both sit below the length gate once the head byte is taken, so they
// measure what the dispatch point costs a row the kernel never takes; 96 is the first width above it; 2550 is a full
// page width, what a region composed onto a page at an odd offset moves.
//
// Each iteration composes the whole 32-row image, so ComposeTo's clipping math is amortized as a real region
// composition amortizes it, while the per-row costs — head byte, dispatch call, trailing bytes — are counted 32
// times.
func BenchmarkComposeShiftedSIMD(b *testing.B) {
	const rows = 32
	var rng prng = 2
	for _, width := range []int32{16, 64, 96, 2550} {
		src := randomImage(&rng, width, rows)
		dst := randomImage(&rng, width+16, rows)
		for shift := int32(1); shift <= 7; shift++ {
			for _, entry := range composeOpNames {
				if entry.op != ComposeOr && entry.op != ComposeXnor {
					continue // Or is what text regions use; Xnor is the costliest arm. The rest track them.
				}
				b.Run(fmt.Sprintf("%s/w%d/shift%d", entry.name, width, shift), func(b *testing.B) {
					for b.Loop() {
						src.ComposeTo(dst, shift, 0, entry.op)
					}
				})
			}
		}
	}
}

// BenchmarkFillSIMD measures Image.Fill on a full page. The fill has no vector kernel, so both arms run the same code,
// and a difference between them means something other than the fill moved.
func BenchmarkFillSIMD(b *testing.B) {
	img := NewImage(2550, 3300)
	for b.Loop() {
		img.Fill(true)
	}
}

// BenchmarkFillBytewiseSIMD is the byte-at-a-time baseline fillBytes replaced at Image.Fill, Image.Expand, and the MMR
// black-run fill, so the doubling copy has a number to compare against.
func BenchmarkFillBytewiseSIMD(b *testing.B) {
	img := NewImage(2550, 3300)
	for b.Loop() {
		for idx := range img.data {
			img.data[idx] = 0xFF
		}
	}
}
