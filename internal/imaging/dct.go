// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package imaging

import (
	"bytes"
	"image"
	stdcolor "image/color"
	"image/jpeg"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
)

// decodeDCT handles DCTDecode payloads through the standard library's JPEG decoder, which performs the YCbCr→RGB and
// Adobe APP14 CMYK/YCCK handling internally. The decoded component bytes then follow the same path as raw samples:
// /Decode mapping, then conversion through the captured device-colorspace behavior in internal/color (a CMYK JPEG pixel
// converts exactly like a k operator's operands). The JPEG's own dimensions are authoritative for the raster (the
// dictionary's /Width and /Height only position the unit square, which the CTM maps regardless of resolution).
func (dec *decoder) decodeDCT(interpolate bool) (*Image, error) {
	decoded, w, h, err := dec.dctImage()
	if err != nil {
		return nil, err
	}
	pix := make([]byte, w*h*4)
	hasAlpha := false
	switch src := decoded.(type) {
	case *image.Gray:
		dec.dctGray(pix, src, w, h, &hasAlpha)
	case *image.YCbCr:
		dec.dctYCbCr(pix, src, w, h, &hasAlpha)
	case *image.CMYK:
		dec.dctCMYK(pix, src, w, h, &hasAlpha)
	default:
		// Any other decoded form (none today) converts generically, without /Decode or color-key support.
		for y := range h {
			for x := range w {
				if c, ok := stdcolor.NRGBAModel.Convert(decoded.At(x, y)).(stdcolor.NRGBA); ok {
					off := (y*w + x) * 4
					pix[off], pix[off+1], pix[off+2], pix[off+3] = c.R, c.G, c.B, c.A
				}
			}
		}
	}
	img := &Image{Pix: pix, Width: w, Height: h, Interpolate: interpolate, HasAlpha: hasAlpha}
	dec.applyMasks(img, false)
	return img, nil
}

// dctImage decodes the JPEG payload, enforcing the pixel budget from the header before the decoder allocates.
func (dec *decoder) dctImage() (img image.Image, w, h int, err error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(dec.data))
	if err != nil {
		return nil, 0, 0, ErrBadImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, 0, 0, ErrBadImage
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixelsFor(len(dec.data)) {
		return nil, 0, 0, ErrTooLarge
	}
	decoded, err := jpeg.Decode(bytes.NewReader(dec.data))
	if err != nil {
		return nil, 0, 0, ErrBadImage
	}
	bounds := decoded.Bounds()
	return decoded, bounds.Dx(), bounds.Dy(), nil
}

// dctByteMapping precomputes the /Decode interpolation for every 8-bit sample value of ncomp components: out[c][s] is
// the mapped component value for sample byte s.
func (dec *decoder) dctByteMapping(ncomp int) [][]float32 {
	m := decodeMapping{dmin: make([]float32, ncomp), dscale: make([]float32, ncomp)}
	arr := dec.decodeArray(ncomp)
	for c := range ncomp {
		lo, hi := float32(0), float32(1)
		if arr != nil {
			lo, hi = arr[2*c], arr[2*c+1]
		}
		m.dmin[c], m.dscale[c] = lo, (hi-lo)/255
	}
	out := make([][]float32, ncomp)
	for c := range ncomp {
		out[c] = make([]float32, 256)
		for s := range 256 {
			out[c][s] = m.apply(uint32(s), c)
		}
	}
	return out
}

func (dec *decoder) dctGray(pix []byte, src *image.Gray, w, h int, hasAlpha *bool) {
	colorKey := dec.colorKeyRanges(1, 8)
	mapping := dec.dctByteMapping(1)
	var lut [256]stdcolor.NRGBA
	comps := make([]float32, 1)
	for s := range 256 {
		comps[0] = mapping[0][s]
		lut[s] = pdfcolor.DeviceGray.ToNRGBA(comps)
	}
	samples := make([]uint32, 1)
	for y := range h {
		row := src.Pix[y*src.Stride:]
		for x := range w {
			s := row[x]
			out := lut[s]
			if colorKey != nil {
				samples[0] = uint32(s)
				if inColorKey(samples, colorKey) {
					out.A = 0
					*hasAlpha = true
				}
			}
			off := (y*w + x) * 4
			pix[off], pix[off+1], pix[off+2], pix[off+3] = out.R, out.G, out.B, out.A
		}
	}
}

func (dec *decoder) dctYCbCr(pix []byte, src *image.YCbCr, w, h int, hasAlpha *bool) {
	colorKey := dec.colorKeyRanges(3, 8)
	mapping := dec.dctByteMapping(3)
	// With the default (or absent) /Decode, DeviceRGB conversion is byte-identity — trunc(float32(s)/255×255) equals s
	// for every byte — so the mapped bytes pass through exactly like the oracle's untransformed copy.
	var lut [3][256]uint8
	for c := range 3 {
		for s := range 256 {
			lut[c][s] = rgbByteFor(mapping[c][s])
		}
	}
	samples := make([]uint32, 3)
	for y := range h {
		for x := range w {
			r, g, b := stdcolor.YCbCrToRGB(src.Y[src.YOffset(x, y)], src.Cb[src.COffset(x, y)], src.Cr[src.COffset(x, y)])
			off := (y*w + x) * 4
			pix[off], pix[off+1], pix[off+2] = lut[0][r], lut[1][g], lut[2][b]
			pix[off+3] = 255
			if colorKey != nil {
				samples[0], samples[1], samples[2] = uint32(r), uint32(g), uint32(b)
				if inColorKey(samples, colorKey) {
					pix[off+3] = 0
					*hasAlpha = true
				}
			}
		}
	}
}

// dctCMYK converts a 4-component JPEG. The standard library undoes the Adobe inversion (its image.CMYK holds true ink
// values), but the oracle pins the opposite convention for DCT streams inside PDFs: MuPDF consumes libjpeg's raw
// output, which leaves Adobe CMYK/YCCK samples in their stored, inverted form, and applies no inversion of its own — a
// transform-0 probe renders near-black under the identity /Decode. The stored samples are therefore reconstructed here
// (255−v per channel, which also restores libjpeg's YCCK output for transform-2 files), and /Decode plus color-key
// masking see those, like every other codec's raw samples. Producers compensate with /Decode [1 0 1 0 1 0 1 0] when
// they intend true ink values, which then flows through mapping below exactly as in MuPDF.
func (dec *decoder) dctCMYK(pix []byte, src *image.CMYK, w, h int, hasAlpha *bool) {
	colorKey := dec.colorKeyRanges(4, 8)
	mapping := dec.dctByteMapping(4)
	comps := make([]float32, 4)
	samples := make([]uint32, 4)
	for y := range h {
		row := src.Pix[y*src.Stride:]
		for x := range w {
			off4 := x * 4
			for c := range 4 {
				stored := 255 - row[off4+c]
				samples[c] = uint32(stored)
				comps[c] = mapping[c][stored]
			}
			out := pdfcolor.DeviceCMYK.ToNRGBA(comps)
			if colorKey != nil && inColorKey(samples, colorKey) {
				out.A = 0
				*hasAlpha = true
			}
			off := (y*w + x) * 4
			pix[off], pix[off+1], pix[off+2], pix[off+3] = out.R, out.G, out.B, out.A
		}
	}
}

// rgbByteFor converts one mapped DeviceRGB component to its rendered byte through the same conversion as
// internal/color's DeviceRGB (truncation of the float32 product, the captured oracle behavior).
func rgbByteFor(v float32) uint8 {
	comps := [3]float32{v, v, v}
	return pdfcolor.DeviceRGB.ToNRGBA(comps[:]).R
}

// dctGrayPlane returns the JPEG's gray bytes for degenerate DCT stencil masks: the Y plane of a color JPEG, the gray
// plane of a grayscale one.
func (dec *decoder) dctGrayPlane() (gray []byte, w, h int, err error) {
	decoded, w, h, err := dec.dctImage()
	if err != nil {
		return nil, 0, 0, err
	}
	out := make([]byte, w*h)
	switch src := decoded.(type) {
	case *image.Gray:
		for y := range h {
			copy(out[y*w:(y+1)*w], src.Pix[y*src.Stride:])
		}
	case *image.YCbCr:
		for y := range h {
			copy(out[y*w:(y+1)*w], src.Y[y*src.YStride:y*src.YStride+w])
		}
	default:
		for y := range h {
			for x := range w {
				if g, ok := stdcolor.GrayModel.Convert(decoded.At(x, y)).(stdcolor.Gray); ok {
					out[y*w+x] = g.Y
				}
			}
		}
	}
	return out, w, h, nil
}
