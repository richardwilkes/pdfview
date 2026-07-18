// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package color

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"image/color"
	"io"
	"math"
	"sync"

	_ "embed"
)

// The device-colorspace conversions below reproduce, byte for byte at the observation points, what the MuPDF build
// behind the oracle produces when painting solid colors into its RGB pixmap (that build routes device colorspaces
// through ICC profiles). They were captured behaviorally — probe PDFs of flat patches rendered through the oracle,
// never from MuPDF source — by oracle/colorprobe, which regenerates the two data files:
//
//   - data/gray1021.bin: DeviceGray sampled at v = i/1020 (every quarter-byte step) → 3 RGB bytes each. The ICC
//     gray→RGB transform is not perfectly neutral (some grays land with B one below R/G), so all three channels are
//     recorded.
//   - data/cmyk17.bin.gz: DeviceCMYK sampled on the 17^4 grid (step 1/16) → 3 RGB bytes per node. Multilinear
//     interpolation between nodes matched 2516 off-grid oracle observations with mean error 0.25 and max 1.7,
//     comfortably inside the pixel-diff thresholds.
//
// DeviceRGB needs no table: observation shows the component bytes are trunc(v×255) computed in float32 (510 of 1021
// ramp points contradict round-to-nearest; all match truncation).

//go:embed data/gray1021.bin
var grayTable []byte

//go:embed data/cmyk17.bin.gz
var cmykTableGz []byte

const (
	graySamples = 1021
	cmykNodes   = 17
)

var cmykOnce = sync.OnceValues(func() ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(cmykTableGz))
	if err != nil {
		return nil, err
	}
	table, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(table) != cmykNodes*cmykNodes*cmykNodes*cmykNodes*3 {
		return nil, fmt.Errorf("cmyk table has %d bytes", len(table))
	}
	return table, nil
})

// clamp01 clamps v to [0, 1], mapping NaN to 0.
func clamp01(v float32) float32 {
	if math.IsNaN(float64(v)) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// rgbByte converts one DeviceRGB component to its rendered byte: truncation of the float32 product, the observed MuPDF
// behavior.
func rgbByte(v float32) uint8 {
	return uint8(clamp01(v) * 255)
}

// grayToNRGBA converts a DeviceGray value through the observed gray curve, interpolating linearly between the
// quarter-byte observation points.
func grayToNRGBA(v float32) color.NRGBA {
	t := float64(clamp01(v)) * (graySamples - 1)
	lo := int(t)
	if lo > graySamples-2 {
		lo = graySamples - 2
	}
	fr := t - float64(lo)
	var out [3]uint8
	for ch := range 3 {
		a := float64(grayTable[lo*3+ch])
		b := float64(grayTable[(lo+1)*3+ch])
		out[ch] = uint8(math.Floor(a + (b-a)*fr + 0.5))
	}
	return color.NRGBA{R: out[0], G: out[1], B: out[2], A: 255}
}

// cmykToNRGBA converts a DeviceCMYK color through the observed 17^4 grid with multilinear interpolation.
func cmykToNRGBA(c, m, y, k float32) color.NRGBA {
	table, err := cmykOnce()
	if err != nil {
		// The embedded table is validated at build time by the package tests; this fallback (the classical additive
		// formula) only runs if the binary's data is somehow corrupt.
		cc, mm, yy, kk := float64(clamp01(c)), float64(clamp01(m)), float64(clamp01(y)), float64(clamp01(k))
		return color.NRGBA{
			R: uint8(255 * (1 - math.Min(1, cc+kk))),
			G: uint8(255 * (1 - math.Min(1, mm+kk))),
			B: uint8(255 * (1 - math.Min(1, yy+kk))),
			A: 255,
		}
	}
	var lo [4]int
	var fr [4]float64
	for i, v := range [4]float32{c, m, y, k} {
		t := float64(clamp01(v)) * (cmykNodes - 1)
		lo[i] = int(t)
		if lo[i] > cmykNodes-2 {
			lo[i] = cmykNodes - 2
		}
		fr[i] = t - float64(lo[i])
	}
	var acc [3]float64
	for corner := range 16 {
		w := 1.0
		var ix [4]int
		for d := range 4 {
			if corner>>d&1 == 1 {
				w *= fr[d]
				ix[d] = lo[d] + 1
			} else {
				w *= 1 - fr[d]
				ix[d] = lo[d]
			}
		}
		if w == 0 {
			continue
		}
		base := (((ix[0]*cmykNodes+ix[1])*cmykNodes+ix[2])*cmykNodes + ix[3]) * 3
		for ch := range 3 {
			acc[ch] += w * float64(table[base+ch])
		}
	}
	return color.NRGBA{
		R: uint8(math.Floor(acc[0] + 0.5)),
		G: uint8(math.Floor(acc[1] + 0.5)),
		B: uint8(math.Floor(acc[2] + 0.5)),
		A: 255,
	}
}
