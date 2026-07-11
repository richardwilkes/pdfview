// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package font

import (
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/type1"
)

// Embedded Type 1 programs (FontFile). internal/type1 owns the container and charstring work; this file
// adapts it to the Font seam: synthetic GIDs from the program's name list, glyph-space outlines through the
// FontMatrix, quad metrics from the FontBBox (FreeType's rule for Type 1, like bare CFF — see the package
// comment), and hsbw advances as the /Widths-absent fallback.

// t1Info is a parsed embedded Type 1 program prepared for glyph work.
type t1Info struct {
	font *type1.Font
	// nameGID inverts font.Names once (Names[gid] = name).
	nameGID map[string]uint32
	// advances lazily built at Load only for /Widths-less fonts (codes 0-255 through the encoding).
	advances map[uint32]float32
	// matrix is the FontMatrix mapping glyph units to em space at size 1.
	matrix [6]float32
}

// parseType1Stream decodes and parses a FontFile stream. Failures yield nil and the caller substitutes.
func parseType1Stream(d *cos.Document, s *cos.Stream, stdEnc *[256]string) *t1Info {
	raw, err := d.StreamData(s)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return parseType1Bytes(raw, stdEnc)
}

// parseType1Bytes is the bytes-level half (split out for the fuzzer).
func parseType1Bytes(raw []byte, stdEnc *[256]string) *t1Info {
	f, err := type1.Parse(raw)
	if err != nil {
		return nil
	}
	f.StdEnc = stdEnc
	info := &t1Info{font: f, matrix: [6]float32{0.001, 0, 0, 0.001, 0, 0}}
	if f.HasMatrix {
		info.matrix = f.FontMatrix
	}
	info.nameGID = make(map[string]uint32, len(f.Names))
	for gid, name := range f.Names {
		if _, exists := info.nameGID[name]; !exists {
			info.nameGID[name] = uint32(gid)
		}
	}
	return info
}

// metrics returns the em-normalized ascender/descender the FreeType way (FontBBox yMax/yMin over the
// FontMatrix-implied upem), reusing the bare-CFF rule.
func (t *t1Info) metrics() (asc, desc float32, ok bool) {
	top := cffTop{bbox: t.font.FontBBox, matrix: t.matrix, hasBBox: t.font.HasBBox, hasMatrix: t.font.HasMatrix}
	return top.metrics()
}

// builtinEncoding returns the program's built-in encoding table for use as the encoding base, or nil.
func (t *t1Info) builtinEncoding() *[256]string {
	if t.font.Encoding != nil {
		return t.font.Encoding
	}
	return nil // StdEncoding (or nothing) both resolve to StandardEncoding at the caller.
}

// gid maps a code to a synthetic GID through the encoding's glyph name.
func (t *t1Info) gid(name string) uint32 {
	if name == "" {
		return 0
	}
	if g, ok := t.nameGID[name]; ok {
		return g
	}
	return 0
}

// glyphPath interprets one glyph and maps it through the FontMatrix into em-normalized glyph space.
func (t *t1Info) glyphPath(gid uint32) *gfx.Path {
	if gid >= uint32(len(t.font.Names)) {
		return nil
	}
	segs, _, err := t.font.Glyph(t.font.Names[gid])
	if err != nil {
		return nil
	}
	m := t.matrix
	return segmentsToPath(segs, gfx.Matrix{A: m[0], B: m[1], C: m[2], D: m[3], E: m[4], F: m[5]})
}

// buildAdvances precomputes the hsbw advances of every encoded glyph, keyed by GID and in text space (glyph
// units through the FontMatrix's x scale — advances are x-directional). Only called at Load, and only for
// fonts without /Widths, so Font stays immutable afterwards.
func (t *t1Info) buildAdvances(enc *[256]string) {
	t.advances = make(map[uint32]float32, 64)
	for code := range 256 {
		name := enc[code]
		if name == "" {
			continue
		}
		gid, ok := t.nameGID[name]
		if !ok {
			continue
		}
		if _, done := t.advances[gid]; done {
			continue
		}
		if adv, advOK := t.font.Advance(name); advOK {
			t.advances[gid] = adv * t.matrix[0]
		}
	}
}

// advance returns the precomputed hsbw advance for a GID.
func (t *t1Info) advance(gid uint32) (float32, bool) {
	adv, ok := t.advances[gid]
	return adv, ok
}
