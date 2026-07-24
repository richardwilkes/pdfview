// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package doc

import (
	"bytes"
	"math"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/filter"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// maxContentStreams caps how many entries of a /Contents array are examined, bounding the decode work a hostile page can
// demand (each stream's own decode is already capped by internal/filter).
const maxContentStreams = 8192

// PageResources returns the given 0-based page's resolved (inheritable) /Resources dictionary, or nil when it has none.
func (d *Document) PageResources(pageNumber int) cos.Dict {
	if pageNumber < 0 || pageNumber >= len(d.resources) {
		return nil
	}
	res, _ := cos.AsDict(d.cos.Resolve(d.resources[pageNumber]))
	return res
}

// PageContents returns the given 0-based page's decoded content: its /Contents stream, or the concatenation of its
// /Contents array with a newline between parts (the array form splits one logical stream between lexical tokens, so a
// separator is required and sufficient). Streams that cannot be decoded contribute what they yielded (internal/filter
// returns partial output for corrupt-but-decodable input) or nothing; a page with no usable content returns an empty
// slice, which renders blank.
//
// The array form's concatenation is truncated at filter.MaxDecodedSize of the array's total raw byte count. Each
// stream's own decode is capped individually, but nothing stops one small compression bomb from being listed hundreds
// of times, so the aggregate needs its own budget; the per-stream allowance applied to the sum leaves legitimate
// multi-part content streams untouched.
func (d *Document) PageContents(pageNumber int) []byte {
	page, err := d.Page(pageNumber)
	if err != nil {
		return nil
	}
	contents := d.cos.Resolve(page["Contents"])
	if stream, ok := cos.AsStream(contents); ok {
		data, streamErr := d.cos.StreamData(stream)
		if streamErr != nil {
			return nil
		}
		return data
	}
	arr, ok := cos.AsArray(contents)
	if !ok {
		return nil
	}
	streams := make([]*cos.Stream, 0, min(len(arr), maxContentStreams))
	var raw int64 // Accumulated as an int64 so at most maxContentStreams lengths cannot overflow the sum.
	for i, entry := range arr {
		if i >= maxContentStreams {
			break
		}
		if stream, streamOK := cos.AsStream(d.cos.Resolve(entry)); streamOK {
			streams = append(streams, stream)
			raw += int64(len(stream.Raw))
		}
	}
	budget := filter.MaxDecodedSize(int(min(raw, math.MaxInt)))
	var buf bytes.Buffer
	for _, stream := range streams {
		if buf.Len() >= budget {
			break
		}
		data, streamErr := d.cos.StreamData(stream)
		if streamErr != nil {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		if remaining := budget - buf.Len(); len(data) > remaining {
			buf.Write(data[:remaining])
			break
		}
		buf.Write(data)
	}
	return buf.Bytes()
}

// PageCTM returns the matrix mapping the given 0-based page's PDF user space to rendered-image space at the given
// scale: the page's effective box maps to [0, w×scale] × [0, h×scale] with the top-left/y-down orientation and /Rotate
// applied — the same mapping toTopLeft pins against MuPDF, expressed as a matrix and composed with the scale.
func (d *Document) PageCTM(pageNumber int, scale float32) (gfx.Matrix, error) {
	if pageNumber < 0 || pageNumber >= len(d.geoms) {
		return gfx.Matrix{}, errNoSuchPage
	}
	g := d.geoms[pageNumber]
	var m gfx.Matrix
	switch g.rotate {
	case 90: // u = y − y0, v = x − x0
		m = gfx.Matrix{B: 1, C: 1, E: -g.y0, F: -g.x0}
	case 180: // u = x1 − x, v = y − y0
		m = gfx.Matrix{A: -1, D: 1, E: g.x1, F: -g.y0}
	case 270: // u = y1 − y, v = x1 − x
		m = gfx.Matrix{B: -1, C: -1, E: g.y1, F: g.x1}
	default: // u = x − x0, v = y1 − y
		m = gfx.Matrix{A: 1, D: -1, E: -g.x0, F: g.y1}
	}
	return m.Mul(gfx.Scale(scale, scale)), nil
}
