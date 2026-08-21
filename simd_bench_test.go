// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

import (
	"fmt"
	"testing"
)

// benchRand is the same splitmix64 generator the equivalence tests use, repeated here because this file carries no
// build tag and must compile in the default build too.
type benchRand struct {
	state uint64
}

// next returns the next value in the sequence.
func (r *benchRand) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// benchPix builds a premultiplied page buffer of w×h pixels in which the given percentage of pixels are translucent
// (a random alpha in 1..254, with components kept at or below it so the data is real premultiplied output) and the
// rest are opaque. When banded, the translucent pixels are one contiguous run rather than scattered, which is the
// shape a real page has — transparency arrives as a soft-masked image or a group covering an area, not as noise.
func benchPix(w, h, translucentPct int, banded bool) []byte {
	rng := benchRand{state: 0xf00d}
	pix := make([]byte, w*h*4)
	band := len(pix) * translucentPct / 100
	for i := 0; i < len(pix); i += 4 {
		v := rng.next()
		a := uint32(255)
		if banded {
			if i < band {
				a = 1 + uint32((v>>32)%254)
			}
		} else if translucentPct > 0 && int(v%100) < translucentPct {
			a = 1 + uint32((v>>32)%254)
		}
		pix[i] = byte(uint32(v>>8) % (a + 1))
		pix[i+1] = byte(uint32(v>>16) % (a + 1))
		pix[i+2] = byte(uint32(v>>24) % (a + 1))
		pix[i+3] = byte(a)
	}
	return pix
}

// BenchmarkUnpremultiplySIMD measures the page-buffer unpremultiply through its dispatch variable, at the two page
// sizes a letter page rasterizes to at 150 and 300 dpi and with the two alpha mixes that decide how much work the
// scalar arm skips: a fully opaque page (every pixel takes the scalar arm's cheap case), a page with a fifth of its
// pixels translucent in one contiguous band (a soft-masked image or a transparency group covering part of the page),
// and the same fifth scattered pixel by pixel, which is the worst case for the kernel's whole-chunk skip and is here
// as a floor rather than as a page anyone renders.
//
// The buffer is deliberately not restored between iterations. Unpremultiplying twice is not the same as doing it
// once, but the cost of either arm depends only on the alpha channel — which this never touches — so the timing is
// the same and the reset that would otherwise dominate a memory-bound benchmark stays out of the loop.
func BenchmarkUnpremultiplySIMD(b *testing.B) {
	for _, size := range [][2]int{{1275, 1650}, {2550, 3300}} {
		for _, mix := range []struct {
			name   string
			pct    int
			banded bool
		}{
			{name: "opaque"},
			{name: "band20", pct: 20, banded: true},
			{name: "scattered20", pct: 20},
		} {
			b.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], mix.name), func(b *testing.B) {
				pix := benchPix(size[0], size[1], mix.pct, mix.banded)
				b.SetBytes(int64(len(pix)))
				b.ResetTimer()
				for b.Loop() {
					unpremultiplyPixelsFn(pix)
				}
			})
		}
	}
}
