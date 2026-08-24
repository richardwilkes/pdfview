// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package render

import (
	"fmt"
	"testing"

	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/testrand"
)

// glyphBoxes are the coverage plane sizes body text actually produces: roughly 9 pt at 96, 200, and 300 dpi.
var glyphBoxes = [][2]int{{12, 16}, {24, 32}, {40, 50}}

// BenchmarkCompositeMaskSIMD measures the glyph blit through compositeMask — the row loop, the clipping, and the
// dispatch variable included — under the two coverage patterns the scalar switch has arms for and a ramp that mixes
// every value.
func BenchmarkCompositeMaskSIMD(b *testing.B) {
	for _, box := range glyphBoxes {
		w, h := box[0], box[1]
		for _, cov := range []string{"zero", "full", "ramp"} {
			b.Run(fmt.Sprintf("%dx%d/%s", w, h, cov), func(b *testing.B) {
				plane := make([]byte, w*h)
				for i := range plane {
					switch cov {
					case "full":
						plane[i] = 255
					case "ramp":
						plane[i] = byte(i)
					}
				}
				mask := &glyphMask{plane: plane, w: int32(w), h: int32(h)}
				d, err := New(256, 256)
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(w * h))
				b.ResetTimer()
				for b.Loop() {
					d.compositeMask(mask, 8, 8, 12, 210, 90)
				}
			})
		}
	}
}

// BenchmarkMaskLumaPlaneSIMD measures the luminosity soft-mask reduction through its dispatch variable, at the mask
// bbox sizes a page-sized /SMask produces.
func BenchmarkMaskLumaPlaneSIMD(b *testing.B) {
	for _, side := range []int{512, 1024} {
		b.Run(fmt.Sprintf("%dx%d", side, side), func(b *testing.B) {
			n := side * side
			rng := testrand.Rand(0xb1a5e)
			pix := make([]byte, n*4)
			rng.Fill(pix)
			for i := 3; i < len(pix); i += 4 {
				pix[i] = 255 // The luminosity surface is opaque.
			}
			plane := make([]byte, n)
			b.SetBytes(int64(n * 4))
			b.ResetTimer()
			for b.Loop() {
				lumaPlaneFn(plane, pix)
			}
		})
	}
}

// BenchmarkAllFiniteSIMD measures the path finiteness scan through its dispatch variable at three path sizes: a small
// vector graphic, a typical illustration subpath run, and a heavy map or chart path.
func BenchmarkAllFiniteSIMD(b *testing.B) {
	for _, n := range []int{32, 200, 4096} {
		b.Run(fmt.Sprintf("points=%d", n), func(b *testing.B) {
			rng := testrand.Rand(0xb1a5f)
			pts := make([]gfx.Point, n)
			for i := range pts {
				pts[i] = gfx.Point{X: float32(rng.Next()%2001) - 1000, Y: float32(rng.Next()%2001) - 1000}
			}
			b.SetBytes(int64(n * 8))
			b.ResetTimer()
			for b.Loop() {
				if !allFiniteFn(pts) {
					b.Fatal("the run is finite")
				}
			}
		})
	}
}
