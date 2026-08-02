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

	"github.com/mububoki/jpeg2000/j2k"
	"github.com/mububoki/jpeg2000/jp2"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
)

// isJPX reports whether the codec is JPXDecode. The filter has no abbreviated inline-image spelling, so unlike isDCT
// and isCCITT one name is the whole test.
func isJPX(codec cos.Name) bool { return codec == codecJPXNames }

// jpxRaster is a decoded JPEG 2000 payload reduced to what the PDF color pipeline consumes: ncomp color samples per
// pixel normalized to 8 bits, plus the codestream's own opacity channel as straight alpha. The codec's dimensions are
// authoritative — the dictionary's /Width and /Height only position the unit square, which the CTM maps regardless of
// resolution — so a dictionary disagreeing with the codestream never distorts the decode.
type jpxRaster struct {
	samples []byte // ncomp bytes per pixel, row-major.
	alpha   []byte // One byte per pixel, nil when the payload carries no opacity channel.
	w, h    int
	ncomp   int // Color components: 1 (gray or palette index) or 3 (RGB after the payload's own color transform).
}

// decodeJPX handles JPXDecode payloads through the adopted JPEG 2000 decoder, which performs the wavelet, color, and
// palette work internally and hands back an image whose color channels are already in the payload's rendering space.
// The PDF semantics layered on top are pinned to the oracle rather than to ISO 32000-2 7.4.9, which MuPDF does not
// follow: /SMaskInData is ignored entirely, so an embedded opacity channel always applies as straight alpha (never
// un-premultiplied, whatever the entry says), and an explicit /SMask multiplies onto that alpha through the shared
// applyMasks path instead of replacing or being ignored. /Decode is likewise ignored, including under the /Indexed
// override where the specification says it applies. A /ColorSpace entry overrides the payload's own space only when
// its component count matches what the codestream carries; otherwise the payload wins, the dctSpace rule.
//
// The emitted image is always marked Interpolate, whatever the dictionary says. That is not the /Interpolate entry
// being honored: the oracle smooths JPX samples at every scale, including magnifications where it samples every other
// codec's images unfiltered (measured against a raw-sample probe of identical geometry — MuPDF blends a JPX image's
// sample boundaries at 1.5x, 2.1x, and 3.1x, and the same probe's boundaries only below 2x). Reproducing that is what
// the flag buys here; a renderer-level model of the oracle's own filter choice would subsume it.
func (dec *decoder) decodeJPX() (*Image, error) {
	raster, err := dec.jpxRaster()
	if err != nil {
		return nil, err
	}
	space := dec.jpxSpace(raster.ncomp)
	mapping := jpxMapping(space)
	pix := make([]byte, raster.w*raster.h*4)
	hasAlpha := false
	if raster.ncomp == 1 {
		lut := mapping.lut(space, 8)
		for i, s := range raster.samples {
			raster.put(pix, i, lut[s], &hasAlpha)
		}
	} else {
		conv := newJPX3(space, mapping)
		for i := range raster.w * raster.h {
			raster.put(pix, i, conv.at(raster.samples[i*3], raster.samples[i*3+1], raster.samples[i*3+2]), &hasAlpha)
		}
	}
	img := &Image{Pix: pix, Width: raster.w, Height: raster.h, Interpolate: true, HasAlpha: hasAlpha}
	dec.applyMasks(img, false)
	return img, nil
}

// put writes one converted pixel, folding in the payload's own opacity. A color space can also produce a transparent
// color on its own (/Separation /None), so hasAlpha tracks the emitted alpha rather than only the embedded channel.
func (r *jpxRaster) put(pix []byte, i int, out stdcolor.NRGBA, hasAlpha *bool) {
	if r.alpha != nil {
		out.A = uint8(uint32(out.A) * uint32(r.alpha[i]) / 255)
	}
	if out.A != 255 {
		*hasAlpha = true
	}
	off := i * 4
	pix[off], pix[off+1], pix[off+2], pix[off+3] = out.R, out.G, out.B, out.A
}

// jpxGrayPlane returns the payload's gray bytes for a JPX /SMask, the analog of dctGrayPlane. A soft mask should carry
// one component; a color payload in that role reduces through the standard luminance weights.
func (dec *decoder) jpxGrayPlane() (gray []byte, w, h int, err error) {
	raster, err := dec.jpxRaster()
	if err != nil {
		return nil, 0, 0, err
	}
	if raster.ncomp == 1 {
		return raster.samples, raster.w, raster.h, nil
	}
	out := make([]byte, raster.w*raster.h)
	for i := range out {
		c := stdcolor.NRGBA{R: raster.samples[i*3], G: raster.samples[i*3+1], B: raster.samples[i*3+2], A: 255}
		if g, ok := stdcolor.GrayModel.Convert(c).(stdcolor.Gray); ok {
			out[i] = g.Y
		}
	}
	return out, raster.w, raster.h, nil
}

// jpxRaster decodes the payload, enforcing the pixel budget from the header parse before the decoder allocates — the
// library documents that it sizes its buffers from the declared dimensions with no cap of its own.
func (dec *decoder) jpxRaster() (*jpxRaster, error) { return jpxRasterFor(dec.data) }

// jpxRasterFor is jpxRaster with the payload passed explicitly, so the fuzz target can drive the same budget checks
// and the same recovery the production path applies.
func jpxRasterFor(payload []byte) (*jpxRaster, error) {
	budget := maxPixelsFor(len(payload))
	bare := jpxIsCodestream(payload)
	cfg, err := jpxConfig(payload, bare)
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, ErrBadImage
	}
	if int64(cfg.Width)*int64(cfg.Height) > budget {
		return nil, ErrTooLarge
	}
	if bare && (cfg.ColorModel == stdcolor.GrayModel || cfg.ColorModel == stdcolor.Gray16Model) {
		// A single-component bare codestream is the one shape whose raw sample values matter rather than their
		// rendered gray: under an /Indexed override each sample is a palette index. Only bare codestreams expose the
		// native planes — the JP2 container path decodes to an image.Image and nothing else — so a palettized JP2
		// falls through below and renders through its own pclr/cmap boxes instead.
		if raster, compErr := jpxComponentRaster(payload, budget); compErr == nil {
			return raster, nil
		}
	}
	decoded, err := jpxImage(payload, bare)
	if err != nil {
		return nil, err
	}
	return jpxRasterFrom(decoded, budget)
}

// jpxIsCodestream reports whether the payload is a bare codestream (the SOC marker) rather than a JP2 container. PDF
// permits either (ISO 32000-2 7.4.9) and the two live in different packages upstream.
func jpxIsCodestream(data []byte) bool {
	return len(data) >= 2 && data[0] == 0xff && data[1] == 0x4f
}

// jpxConfig parses just enough of the payload's headers to learn its dimensions and component shape. Panics escaping
// the decoder become errors here and in every other call below; see recoverCodec.
func jpxConfig(data []byte, bare bool) (cfg image.Config, err error) {
	defer recoverCodec(codecJPXNames, &err)
	if bare {
		cfg, err = j2k.DecodeConfig(bytes.NewReader(data))
	} else {
		cfg, err = jp2.DecodeConfig(bytes.NewReader(data))
	}
	if err != nil {
		return image.Config{}, ErrBadImage
	}
	return cfg, nil
}

// jpxImage decodes the payload to an image. A truncated or otherwise damaged codestream is a hard failure, which is
// what the oracle does with one: MuPDF runs its JPEG 2000 decoder in strict mode and drops the image entirely rather
// than painting the part that arrived.
func jpxImage(data []byte, bare bool) (img image.Image, err error) {
	defer recoverCodec(codecJPXNames, &err)
	if bare {
		img, err = j2k.Decode(bytes.NewReader(data))
	} else {
		img, err = jp2.Decode(bytes.NewReader(data))
	}
	if err != nil || img == nil || img.Bounds().Empty() {
		return nil, ErrBadImage
	}
	return img, nil
}

// jpxComponentRaster decodes a bare codestream to its native single component, keeping the raw sample values.
func jpxComponentRaster(data []byte, budget int64) (raster *jpxRaster, err error) {
	defer recoverCodec(codecJPXNames, &err)
	comps, err := j2k.DecodeComponents(bytes.NewReader(data), j2k.Options{})
	if err != nil || len(comps) != 1 {
		return nil, ErrBadImage
	}
	c := comps[0]
	if c.W <= 0 || c.H <= 0 || c.Precision < 1 || c.Precision > 32 || len(c.Samples) < c.W*c.H {
		return nil, ErrBadImage
	}
	if int64(c.W)*int64(c.H) > budget {
		return nil, ErrTooLarge
	}
	// Samples arrive in the signed domain: the display level shift the encoder subtracted goes back on, the result is
	// clamped to the component's own range, and depths past 8 bits drop their low bits (this engine renders through
	// 8-bit pixels, as MuPDF does).
	offset := int64(1) << (c.Precision - 1)
	maxVal := int64(1)<<c.Precision - 1
	shift := 0
	if c.Precision > 8 {
		shift = c.Precision - 8
	}
	samples := make([]byte, c.W*c.H)
	for i := range samples {
		v := int64(c.Samples[i]) + offset
		if v < 0 {
			v = 0
		} else if v > maxVal {
			v = maxVal
		}
		samples[i] = byte(v >> shift)
	}
	return &jpxRaster{samples: samples, w: c.W, h: c.H, ncomp: 1}, nil
}

// jpxRasterFrom flattens a decoded image into color samples plus straight alpha. The decoder emits image.Gray(16) for
// one component and image.NRGBA(64) for every color form (RGB, sYCC, CIELab, ICC, CMYK, palette-expanded, and
// gray+alpha), with alpha 255 where the payload defines no opacity. Deep forms keep only their high byte: the 16-bit
// channels hold each sample's raw 0..2^P−1 value, and the jp2 package exposes no per-component precision to rescale
// by, so anything but a true 16-bit component renders dark. No corpus payload exercises that yet.
func jpxRasterFrom(img image.Image, budget int64) (*jpxRaster, error) {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, ErrBadImage
	}
	// The decoded image's own bounds are what everything below sizes from, and they need not match the header the
	// budget was checked against (a reduced-resolution or subsampled reconstruction differs), so re-check them.
	if int64(w)*int64(h) > budget {
		return nil, ErrTooLarge
	}
	switch src := img.(type) {
	case *image.Gray:
		if !jpxPlaneOK(src.Pix, src.Stride, w, h, 1) {
			return nil, ErrBadImage
		}
		samples := make([]byte, w*h)
		for y := range h {
			copy(samples[y*w:(y+1)*w], src.Pix[y*src.Stride:])
		}
		return &jpxRaster{samples: samples, w: w, h: h, ncomp: 1}, nil
	case *image.Gray16:
		if !jpxPlaneOK(src.Pix, src.Stride, w, h, 2) {
			return nil, ErrBadImage
		}
		samples := make([]byte, w*h)
		for y := range h {
			row := src.Pix[y*src.Stride:]
			for x := range w {
				samples[y*w+x] = row[x*2]
			}
		}
		return &jpxRaster{samples: samples, w: w, h: h, ncomp: 1}, nil
	case *image.NRGBA:
		if !jpxPlaneOK(src.Pix, src.Stride, w, h, 4) {
			return nil, ErrBadImage
		}
		samples := make([]byte, w*h*3)
		alpha := make([]byte, w*h)
		for y := range h {
			row := src.Pix[y*src.Stride:]
			for x := range w {
				i := y*w + x
				samples[i*3], samples[i*3+1], samples[i*3+2] = row[x*4], row[x*4+1], row[x*4+2]
				alpha[i] = row[x*4+3]
			}
		}
		return &jpxRaster{samples: samples, alpha: alpha, w: w, h: h, ncomp: 3}, nil
	case *image.NRGBA64:
		if !jpxPlaneOK(src.Pix, src.Stride, w, h, 8) {
			return nil, ErrBadImage
		}
		samples := make([]byte, w*h*3)
		alpha := make([]byte, w*h)
		for y := range h {
			row := src.Pix[y*src.Stride:]
			for x := range w {
				i := y*w + x
				samples[i*3], samples[i*3+1], samples[i*3+2] = row[x*8], row[x*8+2], row[x*8+4]
				alpha[i] = row[x*8+6]
			}
		}
		return &jpxRaster{samples: samples, alpha: alpha, w: w, h: h, ncomp: 3}, nil
	default:
		samples := make([]byte, w*h*3)
		alpha := make([]byte, w*h)
		for y := range h {
			for x := range w {
				if c, ok := stdcolor.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(stdcolor.NRGBA); ok {
					i := y*w + x
					samples[i*3], samples[i*3+1], samples[i*3+2] = c.R, c.G, c.B
					alpha[i] = c.A
				}
			}
		}
		return &jpxRaster{samples: samples, alpha: alpha, w: w, h: h, ncomp: 3}, nil
	}
}

// jpxPlaneOK reports whether a decoded image's pixel buffer really holds w by h pixels of n bytes each at stride. The
// decoder always sizes its buffers that way; checking anyway keeps a future one from turning a mis-sized buffer into a
// panic escaping this package, which no recover covers here.
func jpxPlaneOK(pix []byte, stride, w, h, n int) bool {
	return stride >= w*n && len(pix) >= (h-1)*stride+w*n
}

// jpxSpace returns the color space the payload's ncomp color samples convert through: the image's own /ColorSpace
// when its component count matches what the codestream carries, else the device space for that count. The mismatch
// fallback is dctSpace's rule — a space of a different arity would consume the samples at the wrong rate — and the
// oracle applies it here too, rendering a three-component payload declared /DeviceGray as RGB.
func (dec *decoder) jpxSpace(ncomp int) pdfcolor.Space {
	if space, err := dec.colorSpace(); err == nil && space.NComponents() == ncomp {
		return space
	}
	if ncomp == 1 {
		return pdfcolor.DeviceGray
	}
	return pdfcolor.DeviceRGB
}

// jpxMapping is the sample→component mapping for JPX samples: the default [0 1] per component, or the sample-as-index
// passthrough an /Indexed space needs (ISO 32000-2 Table 87's default for that space). The /Decode array is
// deliberately not consulted; the oracle ignores it for JPXDecode payloads, including under an /Indexed override,
// which is the one place 8.9.5.3 says it still applies.
func jpxMapping(space pdfcolor.Space) decodeMapping {
	ncomp := space.NComponents()
	scale := float32(1) / 255
	if _, indexed := space.(*pdfcolor.Indexed); indexed {
		scale = 1
	}
	m := decodeMapping{dmin: make([]float32, ncomp), dscale: make([]float32, ncomp)}
	for c := range ncomp {
		m.dscale[c] = scale
	}
	return m
}

// jpx3 is the per-pixel conversion for a three-component JPX image, the JPX analog of dct3 without the /Decode and
// color-key machinery this codec does not use. DeviceRGB — the overwhelmingly common case — reduces to one byte LUT
// per channel; any other three-component space converts through its ToNRGBA per pixel.
type jpx3 struct {
	space   pdfcolor.Space
	mapping decodeMapping
	comps   []float32
	lut     [3][256]uint8
	device  bool
}

func newJPX3(space pdfcolor.Space, mapping decodeMapping) *jpx3 {
	c := &jpx3{space: space, mapping: mapping}
	if c.device = space == pdfcolor.DeviceRGB; c.device {
		for ch := range 3 {
			for s := range 256 {
				c.lut[ch][s] = rgbByteFor(mapping.apply(uint32(s), ch))
			}
		}
	} else {
		c.comps = make([]float32, 3)
	}
	return c
}

// at converts one pixel's three color bytes.
func (c *jpx3) at(b0, b1, b2 byte) stdcolor.NRGBA {
	if c.device {
		return stdcolor.NRGBA{R: c.lut[0][b0], G: c.lut[1][b1], B: c.lut[2][b2], A: 255}
	}
	c.comps[0] = c.mapping.apply(uint32(b0), 0)
	c.comps[1] = c.mapping.apply(uint32(b1), 1)
	c.comps[2] = c.mapping.apply(uint32(b2), 2)
	return c.space.ToNRGBA(c.comps)
}
