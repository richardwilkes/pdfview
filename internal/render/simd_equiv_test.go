// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package render

import (
	"math"
	"reflect"
	"simd"
	"testing"

	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/testrand"
	"github.com/richardwilkes/pdfview/internal/vecmath"
)

// fillWords overwrites w with pseudorandom premultiplied pixels: each channel is at or below the alpha, which is what
// the surface pixmap the glyph blit writes into actually holds.
func fillWords(r *testrand.Rand, w []uint32) {
	for i := range w {
		v := r.Next()
		a := uint32(v>>32) % 256
		c0 := uint32(v) % (a + 1)
		c1 := uint32(v>>8) % (a + 1)
		c2 := uint32(v>>16) % (a + 1)
		w[i] = c0 | c1<<8 | c2<<16 | a<<24
	}
}

// TestRenderSIMDWiring checks that every dispatch variable points where this architecture's preference constants say:
// at the vector kernel where preferred, at the scalar implementation where not. Everything else in this file tests
// the kernels directly.
func TestRenderSIMDWiring(t *testing.T) {
	if !vecmath.KernelsSupported() {
		t.Skip("this machine cannot run the vector kernels, so init deliberately leaves the scalar dispatch in place")
	}
	for _, c := range []struct {
		got    any
		vector any
		scalar any
		name   string
		prefer bool
	}{
		{
			got: compositeMaskSpanFn, vector: compositeMaskSpanSIMD, scalar: compositeMaskSpanScalar,
			name: "compositeMaskSpanFn", prefer: preferCompositeMask,
		},
		{
			got: lumaPlaneFn, vector: maskLumaPlaneSIMD, scalar: lumaPlaneScalar,
			name: "lumaPlaneFn", prefer: preferMaskLuma,
		},
		{
			got: allFiniteFn, vector: allFiniteSIMD, scalar: allFiniteScalar,
			name: "allFiniteFn", prefer: preferAllFinite,
		},
	} {
		want := reflect.ValueOf(c.scalar).Pointer()
		if c.prefer {
			want = reflect.ValueOf(c.vector).Pointer()
		}
		if got := reflect.ValueOf(c.got).Pointer(); got != want {
			t.Errorf("%s is wired to %#x, want %#x (prefer=%v)", c.name, got, want, c.prefer)
		}
	}
}

// TestCompositeMaskSwitchArmsMatchFormula pins the claim the vector blit is built on: the scalar loop's c == 0 and
// c == 255 arms are the general formula's answers, not different math.
func TestCompositeMaskSwitchArmsMatchFormula(t *testing.T) {
	formula := func(src, dst, c uint32) uint32 {
		return (src*c + dst*(255-c) + 127) / 255
	}
	for dst := range uint32(256) {
		for _, src := range []uint32{0, 1, 127, 128, 254, 255} {
			if got := formula(src, dst, 0); got != dst {
				t.Fatalf("c=0 src=%d dst=%d: formula gives %d, the scalar arm leaves %d", src, dst, got, dst)
			}
			if got := formula(src, dst, 255); got != src {
				t.Fatalf("c=255 src=%d dst=%d: formula gives %d, the scalar arm stores %d", src, dst, got, src)
			}
		}
	}
}

// coveragePatterns returns the coverage runs the blit is exercised with: the two values with their own arm in the
// scalar switch, a ramp that walks every value at every lane offset, noise, and a run that changes character from one
// scan group to the next, so a single span drives all three kernel branches and every boundary between them.
func coveragePatterns(n int, rng *testrand.Rand) map[string][]byte {
	zero := make([]byte, n)
	full := make([]byte, n)
	ramp := make([]byte, n)
	noise := make([]byte, n)
	blocks := make([]byte, n)
	for i := range n {
		full[i] = 255
		ramp[i] = byte(i)
		switch (i / compositeMaskGroup) % 3 {
		case 1:
			blocks[i] = 255
		case 2:
			blocks[i] = byte(i * 7)
		}
	}
	rng.Fill(noise)
	return map[string][]byte{"zero": zero, "full": full, "ramp": ramp, "noise": noise, "blocks": blocks}
}

// TestCompositeMaskSpanSIMDMatchesScalar walks every span length from empty through two scan groups plus four vectors,
// covering the full-vector body, every LoadPart/StorePart tail length, the group boundary, and a partial final group,
// each against five coverage patterns and a random destination.
func TestCompositeMaskSpanSIMDMatchesScalar(t *testing.T) {
	wasMin := compositeMaskSpanMin
	t.Cleanup(func() { compositeMaskSpanMin = wasMin })
	compositeMaskSpanMin = 0
	var probe simd.Uint32s
	lanes := probe.Len()
	rng := testrand.Rand(0x71a5)
	for n := range 2*compositeMaskGroup + 4*lanes {
		dst := make([]uint32, n)
		fillWords(&rng, dst)
		for name, cov := range coveragePatterns(n, &rng) {
			for _, src := range [][3]uint32{{0, 0, 0}, {255, 255, 255}, {17, 129, 250}} {
				want := make([]uint32, n)
				copy(want, dst)
				compositeMaskSpanScalar(want, cov, src[0], src[1], src[2])
				got := make([]uint32, n)
				copy(got, dst)
				compositeMaskSpanSIMD(got, cov, src[0], src[1], src[2])
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("%s coverage, span %d, src %v, pixel %d (cov %d, dst %08x): got %08x, want %08x",
							name, n, src, i, cov[i], dst[i], got[i], want[i])
					}
				}
			}
		}
	}
}

// TestCompositeMaskSpanSIMDGate drives the kernel across both sides of its length gate. Below the gate it must hand
// the span to the scalar loop and still produce the same pixels.
func TestCompositeMaskSpanSIMDGate(t *testing.T) {
	wasMin := compositeMaskSpanMin
	t.Cleanup(func() { compositeMaskSpanMin = wasMin })
	rng := testrand.Rand(0x71a6)
	for _, gate := range []int{0, 1, 8, 33} {
		compositeMaskSpanMin = gate
		for _, n := range []int{gate - 1, gate, gate + 1, gate + 16} {
			if n < 0 {
				continue
			}
			dst := make([]uint32, n)
			fillWords(&rng, dst)
			cov := make([]byte, n)
			rng.Fill(cov)
			want := make([]uint32, n)
			copy(want, dst)
			compositeMaskSpanScalar(want, cov, 11, 200, 33)
			got := make([]uint32, n)
			copy(got, dst)
			compositeMaskSpanSIMD(got, cov, 11, 200, 33)
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("gate %d, span %d, pixel %d: got %08x, want %08x", gate, n, i, got[i], want[i])
				}
			}
		}
	}
}

// TestCompositeMaskBlitMatchesScalar runs the whole blit — the row loop, the clipping either side of the surface, and
// the dispatch variable — with the vector kernel wired in and again with the scalar one, and compares the surfaces
// byte for byte.
func TestCompositeMaskBlitMatchesScalar(t *testing.T) {
	was := compositeMaskSpanFn
	t.Cleanup(func() { compositeMaskSpanFn = was })
	rng := testrand.Rand(0x8c0d)
	const w, h = 40, 24
	plane := make([]byte, w*h)
	rng.Fill(plane)
	for i := range 64 { // Sprinkle the two special coverage values through the plane.
		plane[i*3%len(plane)] = 0
		plane[(i*7+1)%len(plane)] = 255
	}
	mask := &glyphMask{plane: plane, w: w, h: h}
	blit := func(fn func([]uint32, []byte, uint32, uint32, uint32)) []uint32 {
		compositeMaskSpanFn = fn
		d, err := New(48, 32)
		if err != nil {
			t.Fatal(err)
		}
		pm := d.surf.Pixmap()
		if pm == nil {
			t.Fatal("no pixmap")
		}
		seed := testrand.Rand(0x8c0e)
		fillWords(&seed, pm.Pix)
		for _, at := range [][2]int{{0, 0}, {-7, -5}, {20, 14}, {45, 30}, {-60, 0}, {3, -30}} {
			d.compositeMask(mask, at[0], at[1], 12, 210, 90)
		}
		out := make([]uint32, len(pm.Pix))
		copy(out, pm.Pix)
		return out
	}
	want := blit(compositeMaskSpanScalar)
	got := blit(compositeMaskSpanSIMD)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pixel %d: got %08x, want %08x", i, got[i], want[i])
		}
	}
}

// TestMaskLumaPlaneSIMDMatchesScalar walks plane lengths across the kernel's chunk boundary — every length from empty
// through a chunk plus a few pixels, and lengths past two chunks — covering the chunked walk, its tail, and the handoff
// to the scalar LUT pass. The neutral ramp is the input the LUT was captured from.
func TestMaskLumaPlaneSIMDMatchesScalar(t *testing.T) {
	wasMin := maskLumaMin
	t.Cleanup(func() { maskLumaMin = wasMin })
	maskLumaMin = 0
	rng := testrand.Rand(0x1d0c)
	lengths := make([]int, 0, maskLumaChunk+8)
	for n := range maskLumaChunk + 5 {
		lengths = append(lengths, n)
	}
	lengths = append(lengths, 2*maskLumaChunk, 2*maskLumaChunk+7, 3*maskLumaChunk+1)
	for _, n := range lengths {
		for _, kind := range []string{"noise", "ramp"} {
			pix := make([]byte, n*4)
			if kind == "noise" {
				rng.Fill(pix)
			} else {
				for i := range n {
					v := byte(i)
					pix[i*4], pix[i*4+1], pix[i*4+2], pix[i*4+3] = v, v, v, 255
				}
			}
			want := make([]byte, n)
			lumaPlaneScalar(want, pix)
			got := make([]byte, n)
			maskLumaPlaneSIMD(got, pix)
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s, plane %d, pixel %d (rgb %d,%d,%d): got %d, want %d",
						kind, n, i, pix[i*4], pix[i*4+1], pix[i*4+2], got[i], want[i])
				}
			}
		}
	}
}

// TestMaskLumaPlaneSIMDExhaustiveChannels pushes every value of every channel through the weighted sum with the other
// two channels held at each end of their range, proving the lane extraction reads the channel it thinks it does: a
// red/blue swap would survive a neutral ramp untouched.
func TestMaskLumaPlaneSIMDExhaustiveChannels(t *testing.T) {
	wasMin := maskLumaMin
	t.Cleanup(func() { maskLumaMin = wasMin })
	maskLumaMin = 0
	for channel := range 3 {
		for _, other := range []byte{0, 255, 128} {
			pix := make([]byte, 256*4)
			for v := range 256 {
				for c := range 3 {
					pix[v*4+c] = other
				}
				pix[v*4+channel] = byte(v)
				pix[v*4+3] = 255
			}
			want := make([]byte, 256)
			lumaPlaneScalar(want, pix)
			got := make([]byte, 256)
			maskLumaPlaneSIMD(got, pix)
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("channel %d at %d (others %d): got %d, want %d", channel, i, other, got[i], want[i])
				}
			}
		}
	}
}

// TestMaskLumaPlaneSIMDGate drives the kernel across both sides of its length gate.
func TestMaskLumaPlaneSIMDGate(t *testing.T) {
	wasMin := maskLumaMin
	t.Cleanup(func() { maskLumaMin = wasMin })
	rng := testrand.Rand(0x1d0d)
	for _, gate := range []int{0, 1, 64, 300} {
		maskLumaMin = gate
		for _, n := range []int{gate - 1, gate, gate + 1, gate + 64} {
			if n < 0 {
				continue
			}
			pix := make([]byte, n*4)
			rng.Fill(pix)
			want := make([]byte, n)
			lumaPlaneScalar(want, pix)
			got := make([]byte, n)
			maskLumaPlaneSIMD(got, pix)
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("gate %d, plane %d, pixel %d: got %d, want %d", gate, n, i, got[i], want[i])
				}
			}
		}
	}
}

// nonFinite is every coordinate value the finiteness scan has to reject.
var nonFinite = []float32{
	float32(math.NaN()),
	float32(math.Inf(1)),
	float32(math.Inf(-1)),
}

// TestAllFiniteSIMDMatchesScalar plants each non-finite value at each coordinate of each point in runs of every
// length, so every lane and tail position is checked, and checks that all-finite runs (including float32's extremes)
// come back true.
func TestAllFiniteSIMDMatchesScalar(t *testing.T) {
	wasMin := allFiniteMin
	t.Cleanup(func() { allFiniteMin = wasMin })
	allFiniteMin = 0
	var probe simd.Float32s
	lanes := probe.Len()
	rng := testrand.Rand(0x2f1e)
	for n := 1; n < 4*lanes+4; n++ {
		base := make([]gfx.Point, n)
		for i := range base {
			base[i] = gfx.Point{X: float32(rng.Next()%2001) - 1000, Y: float32(rng.Next()%2001) - 1000}
		}
		base[0].X = math.MaxFloat32
		base[n-1].Y = -math.MaxFloat32
		if got, want := allFiniteSIMD(base), allFiniteScalar(base); got != want {
			t.Fatalf("finite run of %d: got %v, want %v", n, got, want)
		}
		for _, bad := range nonFinite {
			for i := range n {
				for coord := range 2 {
					pts := make([]gfx.Point, n)
					copy(pts, base)
					if coord == 0 {
						pts[i].X = bad
					} else {
						pts[i].Y = bad
					}
					got := allFiniteSIMD(pts)
					if want := allFiniteScalar(pts); got != want {
						t.Fatalf("%v at point %d coord %d of %d: got %v, want %v", bad, i, coord, n, got, want)
					}
					if got {
						t.Fatalf("%v at point %d coord %d of %d reported finite", bad, i, coord, n)
					}
				}
			}
		}
	}
}

// TestAllFiniteSIMDGate drives the kernel across both sides of its length gate, with and without a non-finite
// coordinate in the run.
func TestAllFiniteSIMDGate(t *testing.T) {
	wasMin := allFiniteMin
	t.Cleanup(func() { allFiniteMin = wasMin })
	for _, gate := range []int{0, 1, 32, 97} {
		allFiniteMin = gate
		for _, n := range []int{gate - 1, gate, gate + 1, gate + 16} {
			if n <= 0 {
				continue
			}
			pts := make([]gfx.Point, n)
			for i := range pts {
				pts[i] = gfx.Point{X: float32(i), Y: float32(-i)}
			}
			if got, want := allFiniteSIMD(pts), allFiniteScalar(pts); got != want {
				t.Fatalf("gate %d, finite run of %d: got %v, want %v", gate, n, got, want)
			}
			pts[n-1].Y = float32(math.Inf(1))
			if got, want := allFiniteSIMD(pts), allFiniteScalar(pts); got != want {
				t.Fatalf("gate %d, run of %d ending in +Inf: got %v, want %v", gate, n, got, want)
			}
		}
	}
}
