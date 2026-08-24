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
	"image/color"
	"math"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
)

// decodeSamples handles the raw-sample path (no image codec, or CCITT whose output is repacked 1-bpc rows): unpack per
// BitsPerComponent, map through /Decode, convert through the color space, then apply /SMask or /Mask alpha.
func (dec *decoder) decodeSamples(w, h int, interpolate bool) (*Image, error) {
	data := dec.data
	rowStride := 0
	bpc := 1
	validCols := w
	bilevel := isCCITT(dec.codec) || isJBIG2(dec.codec)
	switch {
	case bilevel:
		// CCITT and JBIG2 output is one bit per sample in rows byte-aligned at the decoder's column count, which may
		// differ from /Width (extra columns are dropped, missing ones read as zero samples). Neither consults
		// /BitsPerComponent, so a stream that omits it still decodes, as in deployed viewers.
		var cols int
		var err error
		if isCCITT(dec.codec) {
			data, cols, err = dec.decodeCCITT(h)
		} else {
			data, cols, err = dec.decodeJBIG2(h)
		}
		if err != nil {
			return nil, err
		}
		rowStride = rowStrideFor(cols, 1, 1)
		if cols < validCols {
			validCols = cols
		}
	default:
		var err error
		bpc, err = dec.bitsPerComponent()
		if err != nil {
			return nil, err
		}
	}
	space, err := dec.colorSpace()
	if err != nil {
		return nil, err
	}
	ncomp := space.NComponents()
	if ncomp <= 0 || ncomp > 32 {
		return nil, ErrBadImage
	}
	if bilevel && ncomp != 1 {
		// A bilevel row holds one single-component sample per pixel, so a multi-component space would read ncomp
		// samples per pixel against it and produce garbage.
		return nil, ErrBadImage
	}
	if rowStride == 0 {
		rowStride = rowStrideFor(w, ncomp, bpc)
	}
	mapping := dec.decodeMapping(space, bpc)
	lut := mapping.lut(space, bpc)
	colorKey := dec.colorKeyRanges(ncomp, bpc)
	pix := make([]byte, w*h*4)
	comps := make([]float32, ncomp)
	samples := make([]uint32, ncomp)
	reader := sampleReader{data: data, bpc: bpc}
	hasAlpha := false
	for y := range h {
		reader.seek(y * rowStride)
		for x := range w {
			for c := range ncomp {
				// Columns past the decoder's count are not in the byte-aligned row; reading them would consume padding
				// and then the next row's bits, so they read as zero samples.
				if x < validCols {
					samples[c] = reader.next()
				} else {
					samples[c] = 0
				}
			}
			var out color.NRGBA
			if lut != nil {
				out = lut[samples[0]]
			} else {
				for c := range ncomp {
					comps[c] = mapping.apply(samples[c], c)
				}
				out = space.ToNRGBA(comps)
			}
			if colorKey != nil && inColorKey(samples, colorKey) {
				out.A = 0
			}
			// A color space can produce a transparent color on its own (/Separation /None returns the zero NRGBA), so
			// HasAlpha tracks the emitted alpha rather than only the color-key path.
			if out.A != 255 {
				hasAlpha = true
			}
			off := (y*w + x) * 4
			pix[off], pix[off+1], pix[off+2], pix[off+3] = out.R, out.G, out.B, out.A
		}
	}
	img := &Image{Pix: pix, Width: w, Height: h, Interpolate: interpolate, HasAlpha: hasAlpha}
	dec.applyMasks(img, colorKey != nil)
	return img, nil
}

// decodeMapping is the sample→component-value mapping the /Decode array defines: value = min[c] + s×scale[c].
type decodeMapping struct {
	dmin   []float32
	dscale []float32
}

func (m *decodeMapping) apply(s uint32, c int) float32 {
	return m.dmin[c] + float32(s)*m.dscale[c]
}

// decodeMapping computes the mapping for every component. The defaults are [0 1] per component, except Indexed, whose
// default [0 2^bpc−1] passes the sample through as the palette index (ISO 32000-2 Table 87).
func (dec *decoder) decodeMapping(space pdfcolor.Space, bpc int) decodeMapping {
	ncomp := space.NComponents()
	maxVal := float32(uint32(1)<<bpc - 1)
	m := decodeMapping{dmin: make([]float32, ncomp), dscale: make([]float32, ncomp)}
	_, indexed := space.(*pdfcolor.Indexed)
	arr := dec.decodeArray(ncomp)
	for c := range ncomp {
		lo, hi := float32(0), float32(1)
		if indexed {
			hi = maxVal
		}
		if arr != nil {
			lo, hi = arr[2*c], arr[2*c+1]
		}
		m.dmin[c] = lo
		m.dscale[c] = (hi - lo) / maxVal
	}
	return m
}

// decodeArray returns the /Decode array's entries when it is present, well-formed, and long enough, else nil (the
// lenient fallback to the defaults).
func (dec *decoder) decodeArray(ncomp int) []float32 {
	arr, ok := dec.entry("Decode", "D").(cos.Array)
	if !ok || len(arr) < 2*ncomp {
		return nil
	}
	out := make([]float32, 2*ncomp)
	for i := range out {
		v, numOK := cos.AsReal(dec.d.Resolve(arr[i]))
		// Test finiteness after narrowing: a legal PDF number beyond float32's range is a finite float64 but ±Inf here,
		// which would make dmin/dscale non-finite and map every sample to ±Inf/NaN in dctByteMapping and the LUTs.
		f := float32(v)
		if !numOK || math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return nil
		}
		out[i] = f
	}
	return out
}

// lut precomputes the sample→NRGBA table for single-component spaces at 8 bits or fewer, covering the common
// grayscale, indexed, and single-tint images without a per-pixel interface call. Multi-component and 16-bit images
// convert per pixel.
func (m *decodeMapping) lut(space pdfcolor.Space, bpc int) []color.NRGBA {
	if space.NComponents() != 1 || bpc > 8 {
		return nil
	}
	n := uint32(1)<<bpc - 1
	table := make([]color.NRGBA, n+1)
	comps := make([]float32, 1)
	for s := range table {
		comps[0] = m.apply(uint32(s), 0)
		table[s] = space.ToNRGBA(comps)
	}
	return table
}

// colorKeyRanges returns the /Mask color-key ranges as flat [min, max] pairs in raw sample space (ISO 32000-2
// 8.9.6.5), or nil when /Mask is absent or not an array, or when an /SMask is present (it overrides /Mask; see
// applyMasks). Out-of-range and reversed entries are clamped rather than rejected.
func (dec *decoder) colorKeyRanges(ncomp, bpc int) []uint32 {
	if _, hasSMask := dec.softMaskStream(); hasSMask {
		return nil
	}
	arr, ok := dec.entry("Mask", "").(cos.Array)
	if !ok || len(arr) < 2*ncomp {
		return nil
	}
	maxVal := int64(1)<<bpc - 1
	out := make([]uint32, 2*ncomp)
	for i := range out {
		v, numOK := cos.AsInt(dec.d.Resolve(arr[i]))
		if !numOK {
			return nil
		}
		if v < 0 {
			v = 0
		}
		if v > maxVal {
			v = maxVal
		}
		out[i] = uint32(v)
	}
	return out
}

// inColorKey reports whether every component sample falls inside its color-key range.
func inColorKey(samples, ranges []uint32) bool {
	for c, s := range samples {
		if s < ranges[2*c] || s > ranges[2*c+1] {
			return false
		}
	}
	return true
}

// stencilPlane decodes an ImageMask's bits to a coverage plane: 255 where the page is marked. A sample of 0 marks under
// the default Decode [0 1]; Decode [1 0] flips (ISO 32000-2 8.9.6.2). CCITT and JBIG2 payloads decode to bits first;
// DCT (degenerate but tolerated) thresholds the gray plane at one half. JPX is declined: unpacking a still-compressed
// payload as 1-bpc samples would punch pseudo-random holes in a correct base image, and the oracle evidence for the
// codec covers only the /ImageMask XObject, which run diverts to decodeJPX. What still arrives is applyStencilMask's
// /Mask stencil stream, which no golden pins.
func (dec *decoder) stencilPlane(w, h int) ([]byte, error) {
	if isJPX(dec.codec) {
		return nil, ErrUnsupportedCodec
	}
	invert := false
	if arr, ok := dec.entry("Decode", "D").(cos.Array); ok && len(arr) >= 2 {
		if v, numOK := cos.AsReal(dec.d.Resolve(arr[0])); numOK && v == 1 {
			invert = true
		}
	}
	data := dec.data
	rowStride := rowStrideFor(w, 1, 1)
	validCols := w
	switch {
	case isCCITT(dec.codec), isJBIG2(dec.codec):
		var cols int
		var err error
		if isCCITT(dec.codec) {
			data, cols, err = dec.decodeCCITT(h)
		} else {
			data, cols, err = dec.decodeJBIG2(h)
		}
		if err != nil {
			return nil, err
		}
		rowStride = rowStrideFor(cols, 1, 1)
		if cols < validCols {
			validCols = cols
		}
	case isDCT(dec.codec):
		gray, gw, gh, err := dec.dctGrayPlane()
		if err != nil || gw != w || gh != h {
			return nil, ErrBadImage
		}
		alpha := make([]byte, w*h)
		thresholdFn(alpha, gray, invert)
		return alpha, nil
	}
	alpha := make([]byte, w*h)
	reader := sampleReader{data: data, bpc: 1}
	for y := range h {
		reader.seek(y * rowStride)
		for x := range w {
			// Columns past the decoder's count read as zero samples (see decodeSamples).
			sample := uint32(0)
			if x < validCols {
				sample = reader.next()
			}
			if (sample == 0) != invert {
				alpha[y*w+x] = 255
			}
		}
	}
	return alpha, nil
}

// thresholdScalar fills dst with the stencil coverage of a gray plane: 255 where the sample is below 128, else 0, or
// the reverse when invert is set. dst and gray must be the same length, and dst must start zero-filled: only the 255s
// are written. thresholdFn is the vector form.
func thresholdScalar(dst, gray []byte, invert bool) {
	for i, v := range gray[:len(dst)] {
		if (v < 128) != invert {
			dst[i] = 255
		}
	}
}

// invertBytesScalar writes the ones' complement of src into dst, which must be the same length. The JBIG2 row copy uses
// it to flip polarity: that codec stores ink as set bits, the opposite of this package's packed convention. dctCMYK's
// 255−v is the same operation but stays inline: a separate inversion pass measured ~10% slower, since the conversion
// after it dominates. invertBytesFn is the vector form.
func invertBytesScalar(dst, src []byte) {
	src = src[:len(dst)]
	for i := range dst {
		dst[i] = ^src[i]
	}
}

// rowStrideFor returns the byte length of one row of w pixels at ncomp components of bpc bits each. The product tops
// out at 2^35 bits (2^26 columns × 32 components × 16 bits, the caps this package enforces), inside a 64-bit int.
func rowStrideFor(w, ncomp, bpc int) int {
	return (w*ncomp*bpc + 7) / 8
}

// sampleReader unpacks big-endian packed samples of 1, 2, 4, 8, or 16 bits. Reads past the end of data return zero
// samples, the lenient completion for truncated payloads.
type sampleReader struct {
	data []byte
	pos  int // bit position
	bpc  int
}

// seek positions the reader at a byte offset (rows are byte-aligned).
func (r *sampleReader) seek(byteOff int) {
	r.pos = byteOff * 8
}

func (r *sampleReader) next() uint32 {
	switch r.bpc {
	case 8:
		i := r.pos >> 3
		r.pos += 8
		if i < len(r.data) {
			return uint32(r.data[i])
		}
		return 0
	case 16:
		i := r.pos >> 3
		r.pos += 16
		var hi, lo uint32
		if i < len(r.data) {
			hi = uint32(r.data[i])
		}
		if i+1 < len(r.data) {
			lo = uint32(r.data[i+1])
		}
		return hi<<8 | lo
	default: // 1, 2, 4
		i := r.pos >> 3
		shift := 8 - r.bpc - (r.pos & 7)
		r.pos += r.bpc
		if i < len(r.data) {
			return uint32(r.data[i]>>shift) & (1<<r.bpc - 1)
		}
		return 0
	}
}
