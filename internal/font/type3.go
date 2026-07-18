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
	"math"
	"slices"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Type 3 fonts (ISO 32000-2 9.6.4): glyphs are content streams (/CharProcs) executed by the interpreter under
// FontMatrix ∘ Trm — there are no outlines, so GlyphPath reports none and internal/content recurses instead.
// /Widths values are in glyph space and map through the FontMatrix; /Encoding Differences are the required
// code→name source.

// type3Info carries a Type 3 font's execution material.
type type3Info struct {
	procs     map[string]*cos.Stream
	procRefs  map[string]cos.Ref // the /CharProcs entry's reference when it was indirect (cycle guarding)
	resources cos.Dict
	matrix    [6]float32
}

// loadType3 loads a /Subtype /Type3 dictionary.
func loadType3(d *cos.Document, dict cos.Dict) (*Font, error) {
	procsDict, ok := d.GetDict(dict, "CharProcs")
	if !ok || len(procsDict) == 0 {
		return nil, ErrBadFont
	}
	f := &Font{}
	if base, ok2 := d.GetName(dict, "BaseFont"); ok2 {
		f.BaseFont = stripSubsetPrefix(string(base))
	}
	info := &type3Info{
		procs:    make(map[string]*cos.Stream, len(procsDict)),
		procRefs: make(map[string]cos.Ref, len(procsDict)),
		matrix:   [6]float32{0.001, 0, 0, 0.001, 0, 0},
	}
	for name, raw := range procsDict {
		if stream, isStream := cos.AsStream(d.Resolve(raw)); isStream {
			info.procs[string(name)] = stream
			if ref, isRef := raw.(cos.Ref); isRef {
				info.procRefs[string(name)] = ref
			}
		}
	}
	if len(info.procs) == 0 {
		return nil, ErrBadFont
	}
	if arr, has := d.GetArray(dict, "FontMatrix"); has && len(arr) >= 6 {
		var m [6]float32
		valid := true
		for i := range 6 {
			v, numOK := cos.AsReal(d.Resolve(arr[i]))
			if !numOK || !isFiniteF(float32(v)) {
				valid = false
				break
			}
			m[i] = float32(v)
		}
		if valid {
			info.matrix = m
		}
	}
	if res, has := d.GetDict(dict, "Resources"); has {
		info.resources = res
	}
	f.type3 = info
	desc := loadDescriptor(d, dict)
	f.Flags = desc.flags
	f.missingWidth = desc.missingWidth

	// Quad metrics: the FontBBox y extent through the FontMatrix when usable, else the generic defaults.
	// (No search needles pin Type 3 quads; the corpus probe pins pixels.)
	f.ascender, f.descender = 0.8, -0.2
	if arr, has := d.GetArray(dict, "FontBBox"); has && len(arr) >= 4 {
		if y0, okY0 := cos.AsReal(d.Resolve(arr[1])); okY0 {
			if y1, okY1 := cos.AsReal(d.Resolve(arr[3])); okY1 && (y0 != 0 || y1 != 0) {
				lo, hi := float32(y0), float32(y1)
				if lo > hi {
					lo, hi = hi, lo
				}
				asc := hi * info.matrix[3]
				dsc := lo * info.matrix[3]
				if asc > dsc && isFiniteF(asc) && isFiniteF(dsc) {
					f.ascender, f.descender = asc, dsc
				}
			}
		}
	}

	// Encoding: the /Differences are the operative mapping (required for Type 3); the standard base applies
	// beneath them, leniently.
	f.enc = resolveEncoding(d, dict, "", nil)
	f.toUni = loadToUnicode(d, dict)
	buildUnicode(f)

	// Widths are in glyph space: transform to text space through the FontMatrix's x column.
	f.hasWidths = loadWidths(d, dict, f)
	for code, w := range f.widths {
		f.widths[code] = w * 1000 * info.matrix[0] // loadWidths divided by 1000; undo, then apply the matrix.
	}

	// Synthetic GIDs from the proc names (stext/cache identity; there are no program glyph indices).
	names := make([]string, 0, len(info.procs))
	for name := range info.procs {
		names = append(names, name)
	}
	slices.Sort(names)
	nameGID := make(map[string]uint32, len(names))
	for i, name := range names {
		nameGID[name] = uint32(i) + 1 // 0 stays "unmapped".
	}
	for code := range uint32(256) {
		if name := f.GlyphName(code); name != "" {
			f.gids[code] = nameGID[name]
		}
	}
	return f, nil
}

// IsType3 reports whether the font is a Type 3 font (glyphs execute as content streams).
func (f *Font) IsType3() bool { return f.type3 != nil }

// Type3Proc returns the charproc stream for a code, with the /CharProcs entry's reference (zero when the
// stream was direct) for recursion cycle guarding.
func (f *Font) Type3Proc(code uint32) (stream *cos.Stream, ref cos.Ref, ok bool) {
	if f.type3 == nil {
		return nil, cos.Ref{}, false
	}
	name := f.GlyphName(code)
	if name == "" {
		return nil, cos.Ref{}, false
	}
	stream, ok = f.type3.procs[name]
	return stream, f.type3.procRefs[name], ok
}

// Type3Resources returns the font's /Resources dictionary (nil when absent — the caller's frame applies).
func (f *Font) Type3Resources() cos.Dict {
	if f.type3 == nil {
		return nil
	}
	return f.type3.resources
}

// Type3Matrix returns the FontMatrix mapping glyph space to text space.
func (f *Font) Type3Matrix() gfx.Matrix {
	m := [6]float32{0.001, 0, 0, 0.001, 0, 0}
	if f.type3 != nil {
		m = f.type3.matrix
	}
	return gfx.Matrix{A: m[0], B: m[1], C: m[2], D: m[3], E: m[4], F: m[5]}
}

// isFiniteF reports whether v is finite.
func isFiniteF(v float32) bool {
	f64 := float64(v)
	return !math.IsNaN(f64) && !math.IsInf(f64, 0)
}
