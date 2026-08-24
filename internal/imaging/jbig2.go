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
	"encoding/binary"
	"log/slog"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/jbig2"
)

// isJBIG2 reports whether the codec is JBIG2Decode, which has no abbreviated inline-image spelling.
func isJBIG2(codec cos.Name) bool { return codec == codecJBIG2Names }

// jbig2MinSegmentHeader is the shortest legal segment header: a 4-byte segment number, the flags byte, the
// referred-to count byte with no referred-to segments, a one-byte page association, and the 4-byte data length.
const jbig2MinSegmentHeader = 11

// decodeJBIG2 expands a JBIG2Decode payload to packed one-bit rows (byte-aligned, MSB first) at the decoded page's own
// column count, h rows tall, with a 0 bit meaning black. That polarity is PDF's, not JBIG2's (a set page pixel is
// black): MuPDF and pdf.js both invert, so the samples read correctly through /DeviceGray and an ImageMask with the
// default /Decode [0 1] paints where the ink is. /BitsPerComponent is not consulted; the codec fixes it at 1.
//
// The page dimensions are recovered from the segment headers before the library sees the payload, so a hostile stream
// cannot allocate past the caller's budget, and the same budget goes to the library as a cumulative cap on bitmaps no
// header declares. A payload whose page information parsed but whose region data the library rejects (truncated
// mid-region) yields a full-size all-white page, as MuPDF paints it; rows past the decoded page height and columns
// past its width are white too. A payload with no usable page information is declined.
func (dec *decoder) decodeJBIG2(h int) (data []byte, cols int, err error) {
	return decodeJBIG2Plane(dec.data, dec.jbig2Globals(), h)
}

// decodeJBIG2Plane is decodeJBIG2 with the payload and its globals passed explicitly, so the fuzz target can drive the
// same budget checks and the same recovery the production path applies.
func decodeJBIG2Plane(payload, globals []byte, h int) (data []byte, cols int, err error) {
	caps := jbig2CapsFor(payload, globals)
	// The globals stream is scanned against the same caps; only the payload is expected to declare a page.
	if _, _, err = jbig2Scan(globals, caps); err != nil {
		return nil, 0, err
	}
	pageW, _, err := jbig2Scan(payload, caps)
	if err != nil {
		return nil, 0, err
	}
	if pageW <= 0 {
		return nil, 0, ErrBadImage
	}
	budget := caps.pixels
	page, decodeErr := jbig2Page(payload, globals, budget)
	cols = pageW
	if decodeErr == nil {
		cols = int(page.Width())
	}
	// Bound each dimension before multiplying, as run does: an unbounded cols×h could overflow int64 and slip under
	// the budget check.
	if cols <= 0 || h <= 0 || cols > maxImagePixels || h > maxImagePixels || int64(cols)*int64(h) > budget {
		return nil, 0, ErrTooLarge
	}
	rowBytes := (cols + 7) / 8
	out := make([]byte, rowBytes*h)
	for i := range out {
		out[i] = 0xff // White.
	}
	if decodeErr != nil {
		slog.Debug("pdfview: JBIG2 region undecodable; painting the page white", "error", decodeErr,
			"width", cols, "height", h)
		return out, cols, nil
	}
	rows := int(page.Height())
	if rows > h {
		rows = h
	}
	width := int(page.Width())
	if width > cols {
		width = cols
	}
	// The decoded page is packed one bit per pixel, MSB first, with JBIG2's polarity (a set bit is ink). Rows are
	// copied inverted into the white-filled output: whole bytes where a byte holds only kept columns, then bit by bit
	// across the boundary so columns past width stay white.
	stride := int(page.Stride())
	packed := page.Data()
	wholeBytes := width >> 3
	for y := range rows {
		if (y+1)*stride > len(packed) {
			break
		}
		src := packed[y*stride:]
		dst := out[y*rowBytes:]
		invertBytesFn(dst[:wholeBytes], src[:wholeBytes])
		for x := wholeBytes << 3; x < width; x++ {
			if src[x>>3]&(0x80>>(x&7)) != 0 {
				dst[x>>3] &^= 0x80 >> (x & 7)
			}
		}
	}
	return out, cols, nil
}

// jbig2Globals returns the decoded bytes of the /JBIG2Globals stream named by /DecodeParms, or nil when there is none
// or it cannot be decoded (the decode then fails on the missing segments rather than mis-rendering).
func (dec *decoder) jbig2Globals() []byte {
	if dec.d == nil || dec.parms == nil {
		return nil
	}
	stream, ok := dec.d.GetStream(dec.parms, "JBIG2Globals")
	if !ok {
		return nil
	}
	globals, err := dec.d.StreamData(stream)
	if err != nil {
		return nil
	}
	return globals
}

// jbig2Page decodes the payload's page through the embedded-profile entry point, which takes the stream as PDF stores
// it (no file header, sequential segments, everything on page 1) and enforces budget as a cumulative cap on every
// bitmap the decode allocates, the only bound possible on symbol dimensions that arrive as arithmetic-decoded deltas.
// A panic escaping the library becomes an error: the fuzz-enforced "panics never escape" invariant is on this
// package's entry points, not on the dependency.
func jbig2Page(data, globals []byte, budget int64) (page *jbig2.Image, err error) {
	defer recoverCodec(codecJBIG2Names, &err)
	dec, err := jbig2.NewEmbeddedDecoder(data, globals, jbig2.Limits{MaxPixels: budget})
	if err != nil {
		return nil, err
	}
	page, err = dec.DecodePage()
	if err != nil {
		return nil, err
	}
	if page == nil || page.Width() <= 0 || page.Height() <= 0 {
		return nil, ErrBadImage
	}
	return page, nil
}

// jbig2Caps bounds everything a JBIG2 stream can ask the decoder to allocate, derived from the payload's own size the
// way maxPixelsFor derives the pixel budget. The decoder sizes several slices straight from 32-bit segment fields with
// no cap of its own (a four-byte symbol-dictionary body claiming 2^32 new symbols would allocate a 32 GB pointer
// slice), so jbig2Scan rejects such a stream before the library sees it.
type jbig2Caps struct {
	// pixels bounds page, region, and pattern-cell areas.
	pixels int64
	// counts bounds the symbol, pattern, and text-instance counts a stream may declare. Each declared item needs at
	// least one coded decision, and maxJBIG2CountPerByte per byte is far past what any encoder achieves: jbig2enc's
	// densest dictionaries run well under one symbol per byte.
	counts int64
	// segments bounds referred-to segment counts. It is exact rather than heuristic: a referred-to segment must exist,
	// and every segment costs at least a jbig2MinSegmentHeader-byte header.
	segments int64
}

// maxJBIG2CountPerByte is the per-byte allowance jbig2Caps.counts documents.
const maxJBIG2CountPerByte = 8

// jbig2CapsFor derives the caps for a payload and the /JBIG2Globals bytes that accompany it; the globals contribute
// segments to the same decode, so they count toward the same allowances.
func jbig2CapsFor(payload, globals []byte) jbig2Caps {
	total := int64(len(payload)) + int64(len(globals))
	return jbig2Caps{
		pixels:   maxPixelsFor(len(payload)),
		counts:   total * maxJBIG2CountPerByte,
		segments: total/jbig2MinSegmentHeader + 1,
	}
}

// jbig2Scan walks a stream's segment headers, rejecting every field that would size an allocation past caps, and
// returns the page bitmap's dimensions — zero when the stream declares no page, as a /JBIG2Globals stream does not.
// Every dimension and count a segment can claim is a 32-bit field, so without this a few hostile bytes would ask the
// library for a terabyte-scale allocation. The walk is self-bounding: each iteration consumes at least
// jbig2MinSegmentHeader bytes, so it runs at most len(data)/11 times. A header or body that runs off the end ends the
// walk with whatever was learned, the same leniency the region decode gets.
func jbig2Scan(data []byte, caps jbig2Caps) (w, h int, err error) {
	const unknownLength = 0xffffffff
	pageW, pageH, bottom := int64(-1), int64(-1), int64(0)
walk:
	for off := 0; off+jbig2MinSegmentHeader <= len(data); {
		segNum := int64(binary.BigEndian.Uint32(data[off:]))
		flags := data[off+4]
		off += 5
		segType := flags & 0x3f
		refCount := int64(data[off] >> 5)
		if refCount == 7 {
			// Long form (T.88 7.2.4): a 32-bit count whose top three bits are the 7 escape, then one retain bit per
			// referred-to segment plus one for this segment, rounded up to whole bytes.
			refCount = int64(binary.BigEndian.Uint32(data[off:]) & 0x1fffffff)
			off += 4 + int((refCount+8)/8)
		} else {
			off++
		}
		if refCount > caps.segments {
			return 0, 0, ErrTooLarge
		}
		refSize := int64(1)
		switch {
		case segNum > 65536:
			refSize = 4
		case segNum > 256:
			refSize = 2
		}
		// refCount is capped above and refSize tops out at 4, so the product fits; an offset past the payload ends the
		// walk below.
		off += int(refCount * refSize)
		if flags&0x40 != 0 {
			off += 4
		} else {
			off++
		}
		if off+4 > len(data) {
			break
		}
		dataLen := int64(binary.BigEndian.Uint32(data[off:]))
		off += 4
		ok := true
		switch body := data[off:]; segType {
		case 48: // Page information: width, height, then the resolutions, flags, and striping fields.
			if len(body) < 19 {
				break walk
			}
			pageW = int64(binary.BigEndian.Uint32(body))
			pageH = int64(binary.BigEndian.Uint32(body[4:]))
		case 0: // Symbol dictionary.
			ok, err = jbig2ScanSymbolDict(body, caps)
		case 16: // Pattern dictionary.
			ok, err = jbig2ScanPatternDict(body, caps)
		case 4, 6, 7: // Text regions.
			ok, err = jbig2ScanRegion(body, caps, &bottom)
			if ok && err == nil {
				ok, err = jbig2ScanTextRegion(body[17:], caps)
			}
		case 20, 22, 23: // Halftone regions.
			ok, err = jbig2ScanRegion(body, caps, &bottom)
			if ok && err == nil {
				ok, err = jbig2ScanHalftoneRegion(body[17:], caps)
			}
		case 36, 38, 39, 40, 42, 43: // Generic and refinement regions.
			ok, err = jbig2ScanRegion(body, caps, &bottom)
		}
		if err != nil {
			return 0, 0, err
		}
		if !ok {
			break walk
		}
		if dataLen == unknownLength {
			// Only an immediate generic region may declare an unknown length (T.88 7.2.7), and its terminating
			// sequence can only be found by decoding it, so the walk stops here with the dimensions already read.
			break walk
		}
		off += int(dataLen)
		if off > len(data) {
			break
		}
	}
	if pageW <= 0 {
		return 0, 0, nil
	}
	if pageH <= 0 || pageH == unknownLength {
		// A striped page leaves its height for the end-of-stripe segments to resolve; the regions placed on it bound
		// it just as well for the allocation this guards.
		pageH = bottom
	}
	if pageH <= 0 {
		return 0, 0, nil
	}
	if pageW > maxImagePixels || pageH > maxImagePixels || pageW*pageH > caps.pixels {
		return 0, 0, ErrTooLarge
	}
	return int(pageW), int(pageH), nil
}

// jbig2ScanRegion validates a segment body's leading region segment information field (T.88 7.4.1) and tracks the
// bottom edge the regions reach, which resolves a striped page's unknown height. ok reports whether the body was long
// enough to carry the field; a short one ends the walk rather than failing the decode.
func jbig2ScanRegion(body []byte, caps jbig2Caps, bottom *int64) (ok bool, err error) {
	if len(body) < 17 {
		return false, nil
	}
	regionW := int64(binary.BigEndian.Uint32(body))
	regionH := int64(binary.BigEndian.Uint32(body[4:]))
	regionY := int64(binary.BigEndian.Uint32(body[12:]))
	if regionW <= 0 || regionH <= 0 || regionW > maxImagePixels || regionH > maxImagePixels ||
		regionW*regionH > caps.pixels {
		return false, ErrTooLarge
	}
	if regionY+regionH > *bottom {
		*bottom = regionY + regionH
	}
	return true, nil
}

// jbig2ScanSymbolDict bounds a symbol dictionary segment's exported and new symbol counts (T.88 7.4.2), each of which
// the decoder turns straight into a slice of that many entries. The counts sit past a variable run of adaptive-template
// pixels whose length the flags word selects.
func jbig2ScanSymbolDict(body []byte, caps jbig2Caps) (ok bool, err error) {
	if len(body) < 2 {
		return false, nil
	}
	dictFlags := binary.BigEndian.Uint16(body)
	off := 2
	if dictFlags&0x0001 == 0 { // SDHUFF clear: arithmetic coding carries the generic-region AT pixels.
		if (dictFlags>>10)&0x0003 == 0 { // SDTEMPLATE 0 takes four AT pairs, the others one.
			off += 8
		} else {
			off += 2
		}
	}
	if dictFlags&0x0002 != 0 && (dictFlags>>12)&0x0001 == 0 { // SDREFAGG set with SDRTEMPLATE 0: two refinement AT pairs.
		off += 4
	}
	if len(body) < off+8 {
		return false, nil
	}
	exported := int64(binary.BigEndian.Uint32(body[off:]))
	fresh := int64(binary.BigEndian.Uint32(body[off+4:]))
	if exported > caps.counts || fresh > caps.counts || exported+fresh > caps.counts {
		return false, ErrTooLarge
	}
	return true, nil
}

// jbig2ScanPatternDict bounds a pattern dictionary segment (T.88 7.4.4): the decoder allocates one image per gray
// level and decodes them out of a collective bitmap that many cells wide.
func jbig2ScanPatternDict(body []byte, caps jbig2Caps) (ok bool, err error) {
	if len(body) < 7 {
		return false, nil
	}
	cellW := int64(body[1])
	cellH := int64(body[2])
	patterns := int64(binary.BigEndian.Uint32(body[3:])) + 1 // GRAYMAX is the highest index, not the count.
	if patterns > caps.counts || cellW <= 0 || cellH <= 0 || patterns*cellW*cellH > caps.pixels {
		return false, ErrTooLarge
	}
	return true, nil
}

// jbig2ScanTextRegion bounds a text region segment's instance count (T.88 7.4.3.1.4), which sits past the flags words
// and the optional refinement adaptive-template pixels. body starts after the region segment information field.
func jbig2ScanTextRegion(body []byte, caps jbig2Caps) (ok bool, err error) {
	if len(body) < 2 {
		return false, nil
	}
	textFlags := binary.BigEndian.Uint16(body)
	off := 2
	if textFlags&0x0001 != 0 { // SBHUFF: a second flags word selects the standard tables.
		off += 2
	}
	if textFlags&0x0002 != 0 && (textFlags>>15)&0x0001 == 0 { // SBREFINE with SBRTEMPLATE 0: two AT pairs.
		off += 4
	}
	if len(body) < off+4 {
		return false, nil
	}
	if int64(binary.BigEndian.Uint32(body[off:])) > caps.counts {
		return false, ErrTooLarge
	}
	return true, nil
}

// jbig2ScanHalftoneRegion bounds a halftone region's grid (T.88 7.4.5): the decoder builds one gray-code bitplane of
// HGW by HGH samples per bit of the pattern dictionary's depth. body starts after the region segment information field.
func jbig2ScanHalftoneRegion(body []byte, caps jbig2Caps) (ok bool, err error) {
	if len(body) < 9 {
		return false, nil
	}
	gridW := int64(binary.BigEndian.Uint32(body[1:]))
	gridH := int64(binary.BigEndian.Uint32(body[5:]))
	if gridW <= 0 || gridH <= 0 || gridW > maxImagePixels || gridH > maxImagePixels || gridW*gridH > caps.pixels {
		return false, ErrTooLarge
	}
	return true, nil
}

// recoverCodec converts a panic escaping a third-party codec into err, shared by both codec glue files: malformed
// input must degrade to a skipped image, and the fuzz targets enforce that no panic reaches a caller. The pointer is
// how a deferred recover reports through its caller's named return.
//
//nolint:gocritic // ptrToRefParam: the pointer is the point; see above.
func recoverCodec(codec cos.Name, err *error) {
	if r := recover(); r != nil {
		slog.Debug("pdfview: image codec panicked; rendering blank", "codec", string(codec), "panic", r)
		*err = ErrBadImage
	}
}
