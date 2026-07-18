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
	bpc, err := dec.bitsPerComponent()
	if err != nil {
		return nil, err
	}
	if isCCITT(dec.codec) {
		// CCITT output is always one bit per sample; rows are byte-aligned at the decoder's column count, which may
		// differ from /Width (extra columns are dropped, missing ones read as zero samples).
		var cols int
		data, cols, err = dec.decodeCCITT(h)
		if err != nil {
			return nil, err
		}
		bpc = 1
		rowStride = (cols + 7) / 8
	}
	space, err := dec.colorSpace()
	if err != nil {
		return nil, err
	}
	ncomp := space.NComponents()
	if ncomp <= 0 || ncomp > 32 {
		return nil, ErrBadImage
	}
	if rowStride == 0 {
		rowStride = (w*ncomp*bpc + 7) / 8
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
				samples[c] = reader.next()
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
		if !numOK || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		out[i] = float32(v)
	}
	return out
}

// lut precomputes the sample→NRGBA table for single-component spaces at 8 bits or fewer, covering the common grayscale,
// indexed, and single-tint images without a per-pixel interface call. Multi-component and 16-bit images convert per
// pixel.
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

// colorKeyRanges returns the /Mask color-key ranges as flat [min, max] pairs in raw sample space (ISO 32000-2 8.9.6.5),
// or nil when /Mask is absent or not an array — or when an /SMask is present, which overrides the /Mask entry entirely
// (8.9.6.6). Out-of-range and reversed entries are clamped rather than rejected.
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

// stencilPlane decodes an ImageMask's bits to a coverage plane: 255 where the page is marked with the current paint. A
// decoded sample of 0 marks under the default Decode [0 1]; Decode [1 0] flips (ISO 32000-2 8.9.6.2). CCITT payloads
// decode to bits first; DCT (degenerate but tolerated) thresholds the gray plane at one half.
func (dec *decoder) stencilPlane(w, h int) ([]byte, error) {
	invert := false
	if arr, ok := dec.entry("Decode", "D").(cos.Array); ok && len(arr) >= 2 {
		if v, numOK := cos.AsReal(dec.d.Resolve(arr[0])); numOK && v == 1 {
			invert = true
		}
	}
	data := dec.data
	rowStride := (w + 7) / 8
	switch {
	case isCCITT(dec.codec):
		var cols int
		var err error
		data, cols, err = dec.decodeCCITT(h)
		if err != nil {
			return nil, err
		}
		rowStride = (cols + 7) / 8
	case isDCT(dec.codec):
		gray, gw, gh, err := dec.dctGrayPlane()
		if err != nil || gw != w || gh != h {
			return nil, ErrBadImage
		}
		alpha := make([]byte, w*h)
		for i, v := range gray {
			if (v < 128) != invert {
				alpha[i] = 255
			}
		}
		return alpha, nil
	}
	alpha := make([]byte, w*h)
	reader := sampleReader{data: data, bpc: 1}
	for y := range h {
		reader.seek(y * rowStride)
		for x := range w {
			if (reader.next() == 0) != invert {
				alpha[y*w+x] = 255
			}
		}
	}
	return alpha, nil
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

// next returns the next sample.
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
