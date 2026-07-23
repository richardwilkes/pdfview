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

// softMaskStream returns the image's /SMask stream, if any. Soft masks exist only on image XObjects (inline images
// cannot carry one), and when present they override the /Mask entry entirely (ISO 32000-2 8.9.6.6).
func (dec *decoder) softMaskStream() (*cos.Stream, bool) {
	sm, ok := dec.entry("SMask", "").(*cos.Stream)
	return sm, ok
}

// applyMasks applies the image's alpha source — /SMask first, else a stencil /Mask stream — to img in place. colorKeyed
// reports that the /Mask entry was a color-key array already applied during sample conversion. Broken or oversized
// masks are ignored, leaving the image opaque, the lenient viewer behavior.
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
	// Cap each dimension against maxImagePixels first, the way run() does: without it a Width/Height near 2^40 wraps
	// mw*mh mod 2^64 to a small value that passes the budget check, then panics or attempts an exabyte allocation.
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
// distort coverage. Unsupported codecs (JBIG2, JPX) and malformed masks report an error and the mask is ignored.
func alphaPlane(d *cos.Document, sm *cos.Stream) (plane []byte, w, h int, err error) {
	data, codec, parms, err := d.ImageFilterSplit(sm.Dict, sm.Raw)
	if err != nil {
		return nil, 0, 0, ErrBadImage
	}
	sub := &decoder{d: d, dict: sm.Dict, data: data, codec: codec, parms: parms}
	w64, wOK := sub.intEntry("Width", "W")
	h64, hOK := sub.intEntry("Height", "H")
	// Cap each dimension against maxImagePixels first, the way run() does: without it a Width/Height near 2^40 wraps
	// w64*h64 mod 2^64 to a small value that passes the budget check, then panics or attempts an exabyte allocation.
	if !wOK || !hOK || w64 <= 0 || h64 <= 0 || w64 > maxImagePixels || h64 > maxImagePixels || w64*h64 > maxPixelsFor(len(data)) {
		return nil, 0, 0, ErrBadImage
	}
	w, h = int(w64), int(h64)
	switch {
	case codec == codecJBIG2Names || codec == codecJPXNames:
		return nil, 0, 0, ErrUnsupportedCodec
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
	bpc, err := sub.bitsPerComponent()
	if err != nil {
		return nil, 0, 0, err
	}
	rowStride := (w*bpc + 7) / 8
	if isCCITT(codec) {
		var cols int
		data, cols, err = sub.decodeCCITT(h)
		if err != nil {
			return nil, 0, 0, err
		}
		bpc = 1
		rowStride = (cols + 7) / 8
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
			plane[y*w+x] = lut[reader.next()]
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

// compositeAlpha multiplies img's alpha by the mask plane, sampling nearest when the dimensions differ (the mask and
// the image both span the same unit square).
func compositeAlpha(img *Image, plane []byte, mw, mh int) {
	if mw <= 0 || mh <= 0 || len(plane) < mw*mh {
		return
	}
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
