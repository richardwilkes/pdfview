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
// /Decode mapping, then conversion through the image's own color space (dctSpace), which for the device spaces is the
// captured behavior in internal/color (a CMYK JPEG pixel converts exactly like a k operator's operands). The JPEG's own
// dimensions are authoritative for the raster (the dictionary's /Width and /Height only position the unit square, which
// the CTM maps regardless of resolution).
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
	case *image.RGBA:
		// image/jpeg returns this form for every 3-component JPEG whose components it decides are already RGB: an Adobe
		// APP14 marker with transform 0, or component IDs 'R','G','B' with no JFIF marker. Neither is exotic in PDF
		// payloads, so it carries the same /Decode and color-key support as the YCbCr form.
		dec.dctRGBA(pix, src, w, h, &hasAlpha)
	case *image.CMYK:
		dec.dctCMYK(pix, src, w, h, &hasAlpha)
	default:
		// image/jpeg returns no other form; any future one converts generically, without /Decode or color-key support.
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

// dctSpace returns the color space the JPEG's ncomp decoded component bytes convert through: the image's own
// /ColorSpace, falling back to the device space for ncomp components when the dictionary names none, names one this
// package cannot resolve, or names one whose component count disagrees with what the payload actually carries (the
// payload wins — a space of a different arity would consume the bytes at the wrong rate). Inferring the space from the
// decoded Go type alone renders every DCT image in a non-device space with wrong colors: an all-zero 1-component JPEG
// under /Separation with a subtractive tint transform paints black where tint 0 must paint white, a fully inverted
// image, and /Indexed, /DeviceN, and /ICCBased whose /N disagrees with the component count fare no better.
func (dec *decoder) dctSpace(ncomp int) pdfcolor.Space {
	if space, err := dec.colorSpace(); err == nil && space.NComponents() == ncomp {
		return space
	}
	switch ncomp {
	case 1:
		return pdfcolor.DeviceGray
	case 4:
		return pdfcolor.DeviceCMYK
	default:
		return pdfcolor.DeviceRGB
	}
}

// dctByteMapping precomputes the /Decode interpolation for every 8-bit sample value of space's components: out[c][s] is
// the mapped component value for sample byte s. The mapping is the same one the raw-sample path builds, so an /Indexed
// space's default of [0 2^bpc−1] passes the sample through as a palette index rather than scaling it into [0 1].
func (dec *decoder) dctByteMapping(space pdfcolor.Space) [][]float32 {
	m := dec.decodeMapping(space, 8)
	ncomp := space.NComponents()
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
	space := dec.dctSpace(1)
	mapping := dec.dctByteMapping(space)
	var lut [256]stdcolor.NRGBA
	comps := make([]float32, 1)
	for s := range 256 {
		comps[0] = mapping[0][s]
		lut[s] = space.ToNRGBA(comps)
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
				}
			}
			// As on the raw-sample path, HasAlpha tracks the emitted alpha rather than only the color-key path: a
			// 1-component space can produce a transparent color on its own (/Separation /None's ToNRGBA does).
			if out.A != 255 {
				*hasAlpha = true
			}
			off := (y*w + x) * 4
			pix[off], pix[off+1], pix[off+2], pix[off+3] = out.R, out.G, out.B, out.A
		}
	}
}

func (dec *decoder) dctYCbCr(pix []byte, src *image.YCbCr, w, h int, hasAlpha *bool) {
	conv := dec.newDCT3()
	for y := range h {
		for x := range w {
			r, g, b := stdcolor.YCbCrToRGB(src.Y[src.YOffset(x, y)], src.Cb[src.COffset(x, y)], src.Cr[src.COffset(x, y)])
			conv.put(pix, (y*w+x)*4, r, g, b, hasAlpha)
		}
	}
}

// dctRGBA converts a 3-component JPEG image/jpeg handed back untransformed. Its alpha is 255 for every pixel (the
// decoder fills the channel), so only the mask paths can make a pixel non-opaque.
func (dec *decoder) dctRGBA(pix []byte, src *image.RGBA, w, h int, hasAlpha *bool) {
	conv := dec.newDCT3()
	for y := range h {
		row := src.Pix[y*src.Stride:]
		for x := range w {
			off4 := x * 4
			conv.put(pix, (y*w+x)*4, row[off4], row[off4+1], row[off4+2], hasAlpha)
		}
	}
}

// dct3 is the shared per-pixel conversion for a 3-component DCT image: the /Decode mapping, the color-key ranges, and
// the space the component bytes convert through. DeviceRGB — the overwhelmingly common case — reduces to one byte LUT
// per channel; any other 3-component space (a /DeviceN with three colorants) converts through its ToNRGBA per pixel, as
// the raw-sample path does for multi-component spaces.
type dct3 struct {
	space    pdfcolor.Space
	mapping  [][]float32
	colorKey []uint32
	comps    []float32
	samples  []uint32
	lut      [3][256]uint8
	device   bool
}

func (dec *decoder) newDCT3() *dct3 {
	c := &dct3{space: dec.dctSpace(3), colorKey: dec.colorKeyRanges(3, 8)}
	mapping := dec.dctByteMapping(c.space)
	if c.device = c.space == pdfcolor.DeviceRGB; c.device {
		// With the default (or absent) /Decode, DeviceRGB conversion is byte-identity — trunc(float32(s)/255×255) equals
		// s for every byte — so the mapped bytes pass through exactly like the oracle's untransformed copy.
		for ch := range 3 {
			for s := range 256 {
				c.lut[ch][s] = rgbByteFor(mapping[ch][s])
			}
		}
	} else {
		c.mapping = mapping
		c.comps = make([]float32, 3)
	}
	if c.colorKey != nil {
		c.samples = make([]uint32, 3)
	}
	return c
}

// put converts one pixel's three component bytes into pix at off.
func (c *dct3) put(pix []byte, off int, b0, b1, b2 byte, hasAlpha *bool) {
	a := uint8(255)
	if c.device {
		pix[off], pix[off+1], pix[off+2] = c.lut[0][b0], c.lut[1][b1], c.lut[2][b2]
	} else {
		c.comps[0], c.comps[1], c.comps[2] = c.mapping[0][b0], c.mapping[1][b1], c.mapping[2][b2]
		out := c.space.ToNRGBA(c.comps)
		pix[off], pix[off+1], pix[off+2], a = out.R, out.G, out.B, out.A
	}
	if c.colorKey != nil {
		c.samples[0], c.samples[1], c.samples[2] = uint32(b0), uint32(b1), uint32(b2)
		if inColorKey(c.samples, c.colorKey) {
			a = 0
		}
	}
	pix[off+3] = a
	if a != 255 {
		*hasAlpha = true
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
	space := dec.dctSpace(4)
	mapping := dec.dctByteMapping(space)
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
			out := space.ToNRGBA(comps)
			if colorKey != nil && inColorKey(samples, colorKey) {
				out.A = 0
			}
			if out.A != 255 {
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
