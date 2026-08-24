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
	"github.com/richardwilkes/pdfview/internal/cos"
)

// softMaskStream returns the image's /SMask stream, if any. The key has no inline abbreviation: inline images cannot
// carry a soft mask.
func (dec *decoder) softMaskStream() (*cos.Stream, bool) {
	sm, ok := dec.entry("SMask", "").(*cos.Stream)
	return sm, ok
}

// applyMasks applies the image's alpha source to img in place: an /SMask, which overrides the /Mask entry entirely
// (ISO 32000-2 8.9.6.6), else a stencil /Mask stream. colorKeyed reports that the /Mask entry was a color-key array
// already applied during sample conversion. Broken or oversized masks are ignored, leaving the image opaque, the
// lenient viewer behavior.
func (dec *decoder) applyMasks(img *Image, colorKeyed bool) {
	if sm, ok := dec.softMaskStream(); ok {
		if plane, mw, mh, err := alphaPlane(dec.d, sm); err == nil {
			compositeAlpha(img, plane, mw, mh)
		}
		return
	}
	if colorKeyed {
		return
	}
	if ms, ok := dec.entry("Mask", "").(*cos.Stream); ok {
		dec.applyStencilMask(img, ms)
	}
}

// applyStencilMask applies a stencil /Mask image: where the mask's decoded sample is 1 the base image is masked out,
// where it is 0 the base is painted (ISO 32000-2 8.9.6.5); the mask's own /Decode array may flip its bits first.
// stencilPlane returns 255 exactly where the decoded sample is 0, so its plane is the base's visibility directly.
func (dec *decoder) applyStencilMask(img *Image, ms *cos.Stream) {
	data, codec, parms, err := dec.d.ImageFilterSplit(ms.Dict, ms.Raw)
	if err != nil {
		return
	}
	sub := &decoder{d: dec.d, dict: ms.Dict, data: data, codec: codec, parms: parms}
	mw, wOK := sub.intEntry("Width", "W")
	mh, hOK := sub.intEntry("Height", "H")
	// Cap each dimension against maxImagePixels before multiplying, as run does: a Width/Height near 2^40 would wrap
	// mw*mh past int64 to a small value that passes the budget check.
	if !wOK || !hOK || mw <= 0 || mh <= 0 || mw > maxImagePixels || mh > maxImagePixels || mw*mh > maxPixelsFor(len(data)) {
		return
	}
	plane, err := sub.stencilPlane(int(mw), int(mh))
	if err != nil {
		return
	}
	compositeAlpha(img, plane, int(mw), int(mh))
}

// alphaPlane decodes an /SMask stream to one alpha byte per pixel. The mask is DeviceGray by definition, so the samples
// map through the mask's /Decode array straight to alpha — never through the painting gray→RGB curve, which would
// distort coverage. Malformed masks report an error and the mask is ignored.
func alphaPlane(d *cos.Document, sm *cos.Stream) (plane []byte, w, h int, err error) {
	data, codec, parms, err := d.ImageFilterSplit(sm.Dict, sm.Raw)
	if err != nil {
		return nil, 0, 0, ErrBadImage
	}
	sub := &decoder{d: d, dict: sm.Dict, data: data, codec: codec, parms: parms}
	w64, wOK := sub.intEntry("Width", "W")
	h64, hOK := sub.intEntry("Height", "H")
	// Same per-dimension cap as applyStencilMask.
	if !wOK || !hOK || w64 <= 0 || h64 <= 0 || w64 > maxImagePixels || h64 > maxImagePixels || w64*h64 > maxPixelsFor(len(data)) {
		return nil, 0, 0, ErrBadImage
	}
	w, h = int(w64), int(h64)
	switch {
	case isJPX(codec):
		// JPX ignores /Decode (see decodeJPX), so its samples are the coverage directly rather than through alphaLUT.
		return sub.jpxGrayPlane()
	case isDCT(codec):
		gray, gw, gh, dctErr := sub.dctGrayPlane()
		if dctErr != nil {
			return nil, 0, 0, dctErr
		}
		lut := sub.alphaLUT(8)
		for i, s := range gray {
			gray[i] = lut[s]
		}
		return gray, gw, gh, nil
	}
	var bpc, rowStride int
	validCols := w
	if isCCITT(codec) || isJBIG2(codec) {
		// CCITT and JBIG2 fix bpc at 1 and supply the column count themselves (see decodeSamples), so a soft mask that
		// omits /BitsPerComponent still decodes; columns past the decoder's count read as zero samples.
		var cols int
		if isCCITT(codec) {
			data, cols, err = sub.decodeCCITT(h)
		} else {
			data, cols, err = sub.decodeJBIG2(h)
		}
		if err != nil {
			return nil, 0, 0, err
		}
		bpc = 1
		rowStride = rowStrideFor(cols, 1, 1)
		if cols < validCols {
			validCols = cols
		}
	} else {
		bpc, err = sub.bitsPerComponent()
		if err != nil {
			return nil, 0, 0, err
		}
		rowStride = rowStrideFor(w, 1, bpc)
	}
	plane = make([]byte, w*h)
	reader := sampleReader{data: data, bpc: bpc}
	if bpc == 16 {
		// 16-bit masks map per sample (no LUT): the high byte carries all the precision alpha keeps.
		mapping := sub.grayMapping(16)
		for y := range h {
			reader.seek(y * rowStride)
			for x := range w {
				plane[y*w+x] = alphaByte(mapping.apply(reader.next(), 0))
			}
		}
		return plane, w, h, nil
	}
	lut := sub.alphaLUT(bpc)
	for y := range h {
		reader.seek(y * rowStride)
		for x := range w {
			// Columns past the decoder's count read as zero samples; the 16-bit path never applies to CCITT or JBIG2.
			sample := uint32(0)
			if x < validCols {
				sample = reader.next()
			}
			plane[y*w+x] = lut[sample]
		}
	}
	return plane, w, h, nil
}

// grayMapping builds the single-component /Decode mapping for a mask decoder.
func (dec *decoder) grayMapping(bpc int) decodeMapping {
	m := decodeMapping{dmin: []float32{0}, dscale: []float32{1 / float32(uint32(1)<<bpc-1)}}
	if arr := dec.decodeArray(1); arr != nil {
		m.dmin[0] = arr[0]
		m.dscale[0] = (arr[1] - arr[0]) / float32(uint32(1)<<bpc-1)
	}
	return m
}

// alphaLUT precomputes sample→alpha for bpc of 8 or fewer bits.
func (dec *decoder) alphaLUT(bpc int) []byte {
	mapping := dec.grayMapping(bpc)
	lut := make([]byte, uint32(1)<<bpc)
	for s := range lut {
		lut[s] = alphaByte(mapping.apply(uint32(s), 0))
	}
	return lut
}

// alphaByte converts a mapped coverage value to an alpha byte, clamped and rounded half-up.
func alphaByte(v float32) byte {
	if !(v > 0) { // Catches NaN too.
		return 0
	}
	if v >= 1 {
		return 255
	}
	return byte(v*255 + 0.5)
}

// compositeAlpha multiplies img's alpha by the mask plane, sampling nearest when the dimensions differ (both span the
// same unit square). The nearest-sample index products top out at 2^52, both factors being under the 2^26 cap.
//
// The oracle paints an image and its mask each at its own resolution, so a mask finer than the image on either axis
// must not be decimated onto the image's grid: the composite expands onto the finer count per axis first, replicating
// the image's samples. A mask no finer on either axis composites in place, the arrangement the /SMask goldens pin.
func compositeAlpha(img *Image, plane []byte, mw, mh int) {
	if mw <= 0 || mh <= 0 || len(plane) < mw*mh {
		return
	}
	if w, h := max(img.Width, mw), max(img.Height, mh); w != img.Width || h != img.Height {
		// The finer grid can exceed the pixel cap even though the image and the mask are each within it (a 1 x 2^26
		// mask over a 2^26 x 1 image). An over-cap pairing keeps the image's grid, decimating the mask but rendering.
		if int64(w)*int64(h) <= maxImagePixels {
			expandForMask(img, w, h)
		}
	}
	compositeAlphaFn(img, plane, mw, mh)
}

// compositeAlphaScalar is the composite itself: one nearest sample of the plane per pixel, multiplied into that
// pixel's alpha. compositeAlphaFn is the vector form, which specializes the equal-dimension case.
func compositeAlphaScalar(img *Image, plane []byte, mw, mh int) {
	for y := range img.Height {
		my := y * mh / img.Height
		for x := range img.Width {
			mx := x * mw / img.Width
			a := plane[my*mw+mx]
			if a == 255 {
				continue
			}
			off := (y*img.Width+x)*4 + 3
			img.Pix[off] = uint8(uint32(img.Pix[off]) * uint32(a) / 255)
			img.HasAlpha = true
		}
	}
}

// expandForMask replaces img's pixels with the same picture nearest-sampled onto a w x h grid. The caller has bounded
// w*h against maxImagePixels, so the allocation stays within the documented worst case and the row-index products
// under 2^52.
func expandForMask(img *Image, w, h int) {
	pix := make([]byte, w*h*4)
	for y := range h {
		src := (y * img.Height / h) * img.Width * 4
		dst := y * w * 4
		for x := range w {
			copy(pix[dst+x*4:dst+x*4+4], img.Pix[src+(x*img.Width/w)*4:])
		}
	}
	img.Pix, img.Width, img.Height = pix, w, h
}
