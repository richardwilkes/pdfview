// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package imaging decodes PDF image XObjects and inline images (ISO 32000-2 8.9) into the two raster forms the raster
// device (internal/render) consumes: straight-alpha RGBA pixels for ordinary images, and a one-byte-per- pixel coverage
// plane for stencil masks (ImageMask true), which the device tints with the current fill paint.
//
// The pipeline: the stream's leading non-image filters are applied by internal/cos (ImageFilterSplit), then the
// terminal image codec — none (raw samples), DCTDecode (stdlib image/jpeg, including CMYK/YCCK with the Adobe APP14
// transform), or CCITTFaxDecode (x/image/ccitt) — produces component samples. Samples are unpacked per BitsPerComponent
// (1/2/4/8/16), mapped through the /Decode array, and converted to rendered RGB through internal/color (whose device
// conversions reproduce the oracle's observed ICC-backed behavior; DeviceCMYK JPEG pixels flow through the same
// captured tables as the k operator). /SMask soft masks and /Mask entries (stencil stream or color-key array) become
// the alpha channel. JBIG2Decode and JPXDecode are deliberate stubs: they return ErrUnsupportedCodec, the interpreter
// skips the draw, and the page renders blank where the image would be — never an error to the caller.
//
// Robustness: image dimensions are capped before any allocation, both absolutely (maxImagePixels) and in proportion to
// the encoded payload's size (maxPixelsFor), so hostile dictionaries claiming absurd dimensions over a few payload
// bytes cannot force giant allocations. Sample data shorter than the claimed dimensions reads as zero samples (the
// warn-and-continue analog of deployed viewers).
package imaging

import (
	"errors"
	"log/slog"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
)

// Errors reported by this package. The interpreter treats every decode failure the same way — the image is skipped and
// the page keeps rendering — so these exist for tests and logs rather than control flow.
var (
	// ErrBadImage is reported for malformed image dictionaries or undecodable payloads.
	ErrBadImage = errors.New("malformed image")
	// ErrUnsupportedCodec is reported for the JBIG2Decode and JPXDecode stubs: the image renders blank, not an error.
	ErrUnsupportedCodec = errors.New("unsupported image codec")
	// ErrTooLarge is reported when the claimed dimensions exceed the allocation caps.
	ErrTooLarge = errors.New("image too large")
)

// maxImagePixels is the hard cap on decoded image pixels (width × height), chosen so a 600-dpi letter-size bilevel scan
// (~34 Mpx) still decodes while the RGBA expansion stays bounded (~256 MB worst case). Larger images are skipped
// (rendered blank), never partially decoded.
const maxImagePixels = 1 << 26

// maxPixelsFor returns the pixel budget for an image whose encoded payload is dataLen bytes: min(maxImagePixels,
// max(2^22, 8192×dataLen)). The proportional term stops hostile dictionaries from claiming huge dimensions over a
// handful of payload bytes (missing samples read as zeros, so the only effect of such a claim is allocation), while the
// floor and the generous per-byte multiplier accommodate legitimately extreme compression: an all-white fax page
// compresses to a few bytes per row under CCITT G4.
func maxPixelsFor(dataLen int) int64 {
	budget := int64(dataLen) * 8192
	if budget < 1<<22 {
		budget = 1 << 22
	}
	if budget > maxImagePixels {
		budget = maxImagePixels
	}
	return budget
}

// Image is one decoded image resource, ready for the raster device.
type Image struct {
	// Pix holds the decoded pixels, row-major from the image's top-left sample: straight (non-premultiplied) RGBA, 4
	// bytes per pixel, when Stencil is false; one coverage byte per pixel (255 = paint) when Stencil is true.
	Pix    []byte
	Width  int
	Height int
	// Stencil marks an image mask (ImageMask true): Pix is coverage the device tints with the fill paint.
	Stencil bool
	// Interpolate is the dictionary's /Interpolate flag; the raster device maps it to a sampling filter.
	Interpolate bool
	// HasAlpha reports whether any pixel is non-opaque (from an /SMask or /Mask); always true for stencils.
	HasAlpha bool
}

// DecodeXObject decodes an image XObject. resources is the resource dictionary in scope at the Do operator, consulted
// (leniently — the spec reserves this for inline images) when /ColorSpace is a name that is not a device space.
func DecodeXObject(d *cos.Document, stream *cos.Stream, resources cos.Dict) (*Image, error) {
	data, codec, parms, err := d.ImageFilterSplit(stream.Dict, stream.Raw)
	if err != nil {
		return nil, ErrBadImage
	}
	dec := &decoder{d: d, dict: stream.Dict, data: data, codec: codec, parms: parms, res: resources}
	return dec.run()
}

// DecodeInline decodes an inline image (BI … ID … EI) whose dictionary and payload the content interpreter has
// isolated. resources is the resource dictionary in scope, used to resolve named /CS entries.
func DecodeInline(d *cos.Document, dict cos.Dict, payload []byte, resources cos.Dict) (*Image, error) {
	data, codec, parms, err := d.InlineImageFilterSplit(dict, payload)
	if err != nil {
		return nil, ErrBadImage
	}
	dec := &decoder{d: d, dict: dict, data: data, codec: codec, parms: parms, res: resources}
	return dec.run()
}

// Image codec filter names as ImageFilterSplit reports them, including the abbreviated inline forms.
const (
	codecDCT        cos.Name = "DCTDecode"
	codecDCTAbbr    cos.Name = "DCT"
	codecCCITT      cos.Name = "CCITTFaxDecode"
	codecCCITTAbbr  cos.Name = "CCF"
	codecJBIG2Names cos.Name = "JBIG2Decode"
	codecJPXNames   cos.Name = "JPXDecode"
)

// isDCT reports whether the codec is DCTDecode (either spelling); isCCITT likewise for CCITTFaxDecode.
func isDCT(codec cos.Name) bool   { return codec == codecDCT || codec == codecDCTAbbr }
func isCCITT(codec cos.Name) bool { return codec == codecCCITT || codec == codecCCITTAbbr }

// decoder carries one image's decode state: the dictionary, the payload with non-image filters already applied, and the
// terminal image codec (empty for raw samples).
type decoder struct {
	d     *cos.Document
	dict  cos.Dict
	parms cos.Dict
	res   cos.Dict
	codec cos.Name
	data  []byte
}

// entry resolves dict[full], falling back to the abbreviated inline-image key. Both spellings are accepted for both
// image kinds (PDF 2.0 allows full names inline; abbreviations elsewhere are harmless leniency).
func (dec *decoder) entry(full, abbr cos.Name) cos.Object {
	if v, ok := dec.dict[full]; ok {
		return dec.d.Resolve(v)
	}
	if abbr != "" {
		if v, ok := dec.dict[abbr]; ok {
			return dec.d.Resolve(v)
		}
	}
	return nil
}

func (dec *decoder) intEntry(full, abbr cos.Name) (int64, bool) {
	return cos.AsInt(dec.entry(full, abbr))
}

func (dec *decoder) boolEntry(full, abbr cos.Name) bool {
	b, ok := dec.entry(full, abbr).(cos.Boolean)
	return ok && bool(b)
}

// run decodes the image.
func (dec *decoder) run() (*Image, error) {
	w, wOK := dec.intEntry("Width", "W")
	h, hOK := dec.intEntry("Height", "H")
	if !wOK || !hOK || w <= 0 || h <= 0 || w > maxImagePixels || h > maxImagePixels {
		return nil, ErrBadImage
	}
	if w*h > maxPixelsFor(len(dec.data)) {
		return nil, ErrTooLarge
	}
	interpolate := dec.boolEntry("Interpolate", "I")
	if dec.codec == codecJBIG2Names || dec.codec == codecJPXNames {
		// Deliberate stubs: blank, not an error, with a debug-level note for diagnosability.
		slog.Debug("pdfview: unsupported image codec; rendering blank", "codec", string(dec.codec))
		return nil, ErrUnsupportedCodec
	}
	if dec.boolEntry("ImageMask", "IM") {
		// A stencil mask: one bit per sample regardless of any declared BitsPerComponent or ColorSpace (ISO 32000-2
		// 8.9.6.2); Decode [1 0] flips polarity.
		alpha, err := dec.stencilPlane(int(w), int(h))
		if err != nil {
			return nil, err
		}
		return &Image{
			Pix: alpha, Width: int(w), Height: int(h),
			Stencil: true, Interpolate: interpolate, HasAlpha: true,
		}, nil
	}
	if isDCT(dec.codec) {
		return dec.decodeDCT(interpolate)
	}
	return dec.decodeSamples(int(w), int(h), interpolate)
}

// bitsPerComponent returns the validated BitsPerComponent entry.
func (dec *decoder) bitsPerComponent() (int, error) {
	bpc, ok := dec.intEntry("BitsPerComponent", "BPC")
	if !ok {
		return 0, ErrBadImage
	}
	switch bpc {
	case 1, 2, 4, 8, 16:
		return int(bpc), nil
	default:
		return 0, ErrBadImage
	}
}

// colorSpace resolves the image's color space: a direct name or array first, then — for names — the /ColorSpace
// subdictionary of the resource dictionary in scope (the inline-image rule, extended to XObjects as leniency). A
// /Pattern space is invalid for images.
func (dec *decoder) colorSpace() (pdfcolor.Space, error) {
	obj := dec.entry("ColorSpace", "CS")
	if obj == nil {
		return nil, ErrBadImage
	}
	if space, err := pdfcolor.Parse(dec.d, obj); err == nil {
		if _, isPattern := space.(*pdfcolor.Pattern); isPattern {
			return nil, ErrBadImage
		}
		return space, nil
	}
	if name, ok := obj.(cos.Name); ok && dec.res != nil {
		if csDict, hasDict := dec.d.GetDict(dec.res, "ColorSpace"); hasDict {
			if entry, hasEntry := csDict[name]; hasEntry {
				space, err := pdfcolor.Parse(dec.d, entry)
				if err != nil {
					return nil, ErrBadImage
				}
				if _, isPattern := space.(*pdfcolor.Pattern); isPattern {
					return nil, ErrBadImage
				}
				return space, nil
			}
		}
	}
	return nil, ErrBadImage
}
