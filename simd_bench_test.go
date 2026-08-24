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

	"github.com/richardwilkes/pdfview/internal/testrand"
)

// benchPix builds a premultiplied w×h buffer in which translucentPct percent of the pixels carry a random alpha in
// 1..254 (components at or below it) and the rest are opaque. banded puts the translucent pixels in one contiguous run,
// the shape a real page has: transparency arrives as a soft-masked image or a group covering an area, not as noise.
func benchPix(w, h, translucentPct int, banded bool) []byte {
	rng := testrand.Rand(0xf00d)
	pix := make([]byte, w*h*4)
	band := len(pix) * translucentPct / 100
	for i := 0; i < len(pix); i += 4 {
		v := rng.Next()
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

// BenchmarkUnpremultiplySIMD measures the page-buffer unpremultiply through its dispatch variable at the two sizes a
// letter page rasterizes to at 150 and 300 dpi, under three alpha mixes: fully opaque (the scalar arm's cheap case), a
// fifth translucent in one band (a soft-masked image or transparency group), and the same fifth scattered pixel by
// pixel, the worst case for the kernel's whole-chunk skip and a floor rather than a page anyone renders.
//
// The buffer is not restored between iterations: the cost of either arm depends only on the alpha channel, which the
// work never touches, so the timing is the same and the reset stays out of a memory-bound loop.
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
