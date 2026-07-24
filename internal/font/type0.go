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
)

// Type0 (composite) fonts, ISO 32000-2 9.7: a CMap decodes the multi-byte string codes to CIDs, and one descendant
// CIDFont supplies the glyphs — CIDFontType2 (TrueType: CID→GID via /CIDToGIDMap, outlines through the direct glyf
// walker, never gated on go-text's cmap requirement) or CIDFontType0 (CFF: CID→GID via the program's own charset,
// outlines through go-text's CFF loader, which handles FDSelect internally). Widths come from /W with /DW as the
// default (1000 when absent); vertical mode adds /W2 and /DW2. The PDF /Widths rules for simple fonts do not apply
// here.

// type0Info carries the composite-font state hanging off Font.
type type0Info struct {
	cmap *cmapPDF
	// cidToGID is the decoded /CIDToGIDMap stream (CIDFontType2), nil for /Identity.
	cidToGID []uint16
	// cffCID maps CIDs to GIDs for CID-keyed CFF programs; nil for CIDFontType2 (and for non-CID-keyed CFF descendants,
	// where CID = GID per ISO 32000-2 9.7.4.2).
	cffCID *cffCID
	// glyf is the outline source for CIDFontType2 (the descendant's sfnt info holds the parsed program).
	sfnt *sfntInfo
	w    []wRange
	w2   []w2Range
	// dw is the default glyph width in text space (/DW / 1000, default 1.0).
	dw float32
	// dw2 is the default vertical origin y and displacement (/DW2 / 1000, default 0.88, -1.0).
	dw2      [2]float32
	vertical bool
}

// wRange is one /W entry: CIDs [lo, hi] with per-CID widths (ws indexed from lo) or one uniform width (len(ws) == 1),
// in text space.
type wRange struct {
	ws     []float32
	lo, hi uint32
}

// w2Range is one /W2 entry: per-CID (w1y, vx, vy) triples, or one uniform triple, in text space.
type w2Range struct {
	entries [][3]float32
	lo, hi  uint32
}

// maxCIDToGIDBytes bounds the /CIDToGIDMap stream read: CIDs and GIDs are 16-bit, so 2×65536 covers every addressable
// entry.
const maxCIDToGIDBytes = 2 * 65536

// maxCID is the largest addressable CID: CIDs are 16-bit per ISO 32000-2 9.7.4, so a /W or /W2 entry naming a larger
// starting CID is malformed. Rejecting it also keeps the later uint32(c1) narrowing from wrapping on a hostile value.
const maxCID = 0xFFFF

// loadType0 loads a Type0 font dictionary.
func loadType0(d *cos.Document, dict cos.Dict) (*Font, error) {
	f := &Font{}
	if base, ok := d.GetName(dict, "BaseFont"); ok {
		f.BaseFont = stripSubsetPrefix(string(base))
	}
	cm := loadEncodingCMap(d, dict["Encoding"], 0)
	if cm == nil {
		return nil, ErrUnsupportedFont // Predefined non-Identity CMaps are corpus-driven future work.
	}
	f.toUni = loadToUnicode(d, dict)
	descendant := type0Descendant(d, dict)
	if descendant == nil {
		return nil, ErrBadFont
	}
	desc := loadDescriptor(d, descendant)
	f.Flags = desc.flags
	info := &type0Info{cmap: cm, dw: 1, dw2: [2]float32{0.88, -1}, vertical: cm.wModeResolved() == 1}
	f.type0 = info

	// Dispatch on which FontFile is actually present rather than trusting /Subtype: a font mislabeled "CIDFontType2"
	// whose program really sits in FontFile3 (bare/Type1C CFF) must still be parsed from FontFile3, not routed into the
	// SFNT arm with a nil stream and then substituted away (ISO 32000-2 9.7.4.2).
	embedded := false
	switch {
	case desc.fontFile2 != nil:
		if sfnt := parseSFNTStream(d, desc.fontFile2); sfnt != nil {
			info.sfnt = sfnt
			f.ascender, f.descender = sfnt.ascender, sfnt.descender
			embedded = true
		}
		info.cidToGID = loadCIDToGID(d, descendant["CIDToGIDMap"])
	case desc.fontFile3 != nil:
		// CIDFontType0: a CFF program (bare CID-keyed CFF or Type1C). Metrics follow the bare-CFF FontBBox rule;
		// CID→GID comes from the program's charset when it is CID-keyed, else CID = GID.
		if top := parseCFFTopFromStream(d, desc.fontFile3); top != nil {
			if asc, dsc, ok := top.metrics(); ok {
				f.ascender, f.descender = asc, dsc
				embedded = true
			}
			f.cff = parseCFFGlyphs(d, desc.fontFile3, top)
			if raw, err := d.StreamData(desc.fontFile3); err == nil {
				info.cffCID = parseCFFCharsetCID(raw, top)
			}
		}
	}
	if !embedded {
		// Non-embedded CID fonts substitute like simple fonts: pinned substitute metrics and Liberation shapes through
		// Unicode (which needs ToUnicode; without one, nothing renders — accepted until a corpus file demands better).
		std14 := standard14Name(f.BaseFont, desc.flags)
		f.ascender, f.descender = substituteMetrics(&desc, std14)
		// Shapes, like loadSimple: the substitute owns the glyphs only when no embedded program does. A CFF whose Top
		// DICT carries no usable /FontBBox lands here with outlines but without metrics — substituting its shapes too
		// would have Font.GID resolve codes through the substitute's cmap while Font.GlyphPath pulled those indices out
		// of the embedded charstrings, drawing arbitrary glyphs. Only the metrics fall back in that case.
		if info.sfnt == nil && f.cff == nil {
			f.sub = loadSubstitute(std14)
		}
	}

	if v, ok := cos.AsReal(d.Resolve(descendant["DW"])); ok && v > 0 {
		info.dw = float32(v) / 1000
	}
	if arr, ok := d.GetArray(descendant, "W"); ok {
		info.w = parseWArray(d, arr)
	}
	if arr, ok := d.GetArray(descendant, "DW2"); ok && len(arr) >= 2 {
		if vy, okV := cos.AsReal(d.Resolve(arr[0])); okV {
			info.dw2[0] = float32(vy) / 1000
		}
		if w1, okW := cos.AsReal(d.Resolve(arr[1])); okW {
			info.dw2[1] = float32(w1) / 1000
		}
	}
	if arr, ok := d.GetArray(descendant, "W2"); ok {
		info.w2 = parseW2Array(d, arr)
	}
	return f, nil
}

// loadToUnicode parses the /ToUnicode CMap stream of any font dictionary (ISO 32000-2 9.10.3), nil when absent or
// unusable. Its bf entries map character codes (not CIDs) to Unicode strings and take precedence over every other
// Unicode source.
func loadToUnicode(d *cos.Document, dict cos.Dict) *cmapPDF {
	stream, ok := cos.AsStream(d.Resolve(dict["ToUnicode"]))
	if !ok {
		return nil
	}
	data, err := d.StreamData(stream)
	if err != nil || len(data) == 0 {
		return nil
	}
	cm := parseCMap(data, 0, nil)
	if cm == nil || len(cm.bf) == 0 {
		return nil
	}
	return cm
}

// loadEncodingCMap resolves the /Encoding entry: a predefined CMap name (Identity-H/V) or an embedded CMap stream,
// whose /UseCMap chains resolve recursively.
func loadEncodingCMap(d *cos.Document, obj cos.Object, depth int) *cmapPDF {
	if depth > maxCMapDepth {
		return nil
	}
	resolved := d.Resolve(obj)
	if name, ok := cos.AsName(resolved); ok {
		return predefinedCMap(name)
	}
	stream, ok := cos.AsStream(resolved)
	if !ok {
		return nil
	}
	data, err := d.StreamData(stream)
	if err != nil || len(data) == 0 {
		return nil
	}
	cm := parseCMap(data, depth, predefinedCMap)
	if cm == nil {
		return nil
	}
	// The stream dictionary's /UseCMap wins over (and typically duplicates) the content's usecmap operator.
	if use, has := stream.Dict["UseCMap"]; has && cm.base == nil {
		cm.base = loadEncodingCMap(d, use, depth+1)
	}
	if !cm.hasWMode {
		if v, has := d.GetInt(stream.Dict, "WMode"); has {
			cm.wmode = uint8(v & 1)
			cm.hasWMode = true
		}
	}
	return cm
}

// type0Descendant returns the single descendant CIDFont dictionary.
func type0Descendant(d *cos.Document, dict cos.Dict) cos.Dict {
	arr, ok := d.GetArray(dict, "DescendantFonts")
	if !ok || len(arr) == 0 {
		return nil
	}
	descendant, ok := cos.AsDict(d.Resolve(arr[0]))
	if !ok {
		return nil
	}
	return descendant
}

// loadCIDToGID decodes /CIDToGIDMap: nil for /Identity (or anything unusable — identity is the lenient reading), else
// the stream's big-endian 16-bit GID per CID.
func loadCIDToGID(d *cos.Document, obj cos.Object) []uint16 {
	stream, ok := cos.AsStream(d.Resolve(obj))
	if !ok {
		return nil
	}
	data, err := d.StreamData(stream)
	if err != nil {
		return nil
	}
	if len(data) > maxCIDToGIDBytes {
		data = data[:maxCIDToGIDBytes]
	}
	out := make([]uint16, len(data)/2)
	for i := range out {
		out[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
	}
	return out
}

// parseWArray decodes /W (ISO 32000-2 9.7.4.3): "c [w1 w2 ...]" gives consecutive per-CID widths from c; "c1 c2 w"
// gives one width for the whole range.
func parseWArray(d *cos.Document, arr cos.Array) []wRange {
	var out []wRange
	for i := 0; i+1 < len(arr) && len(out) < maxCMapRanges; {
		c1, ok := cos.AsInt(d.Resolve(arr[i]))
		if !ok || c1 < 0 || c1 > maxCID {
			i++
			continue
		}
		next := d.Resolve(arr[i+1])
		if list, isArr := cos.AsArray(next); isArr {
			ws := make([]float32, 0, min(len(list), 65536))
			for _, entry := range list {
				if len(ws) >= 65536 {
					break
				}
				if v, okV := cos.AsReal(d.Resolve(entry)); okV {
					ws = append(ws, float32(v)/1000)
				} else {
					ws = append(ws, 0)
				}
			}
			if len(ws) > 0 {
				out = append(out, wRange{lo: uint32(c1), hi: uint32(c1) + uint32(len(ws)) - 1, ws: ws})
			}
			i += 2
			continue
		}
		if i+2 < len(arr) {
			c2, ok2 := cos.AsInt(d.Resolve(next))
			w, okW := cos.AsReal(d.Resolve(arr[i+2]))
			if ok2 && okW && c2 >= c1 && c2-c1 < 1<<20 {
				out = append(out, wRange{lo: uint32(c1), hi: uint32(c2), ws: []float32{float32(w) / 1000}})
			}
			i += 3
			continue
		}
		i++
	}
	return out
}

// parseW2Array decodes /W2: "c [w11y v1x v1y w12y v2x v2y ...]" or "c1 c2 w1y vx vy".
func parseW2Array(d *cos.Document, arr cos.Array) []w2Range {
	var out []w2Range
	real3 := func(objs []cos.Object) ([3]float32, bool) {
		var t [3]float32
		for j, o := range objs {
			v, ok := cos.AsReal(d.Resolve(o))
			if !ok {
				return t, false
			}
			t[j] = float32(v) / 1000
		}
		return t, true
	}
	for i := 0; i+1 < len(arr) && len(out) < maxCMapRanges; {
		c1, ok := cos.AsInt(d.Resolve(arr[i]))
		if !ok || c1 < 0 || c1 > maxCID {
			i++
			continue
		}
		next := d.Resolve(arr[i+1])
		if list, isArr := cos.AsArray(next); isArr {
			entries := make([][3]float32, 0, min(len(list)/3, 65536))
			for j := 0; j+2 < len(list) && len(entries) < 65536; j += 3 {
				if t, okT := real3(list[j : j+3]); okT {
					entries = append(entries, t)
				} else {
					break
				}
			}
			if len(entries) > 0 {
				out = append(out, w2Range{lo: uint32(c1), hi: uint32(c1) + uint32(len(entries)) - 1, entries: entries})
			}
			i += 2
			continue
		}
		if i+4 < len(arr) {
			c2, ok2 := cos.AsInt(d.Resolve(next))
			t, okT := real3([]cos.Object{arr[i+2], arr[i+3], arr[i+4]})
			if ok2 && okT && c2 >= c1 && c2 <= maxCID {
				out = append(out, w2Range{lo: uint32(c1), hi: uint32(c2), entries: [][3]float32{t}})
			}
			i += 5
			continue
		}
		i++
	}
	return out
}

// cidWidth returns the horizontal width for a CID in text space: /W, else /DW.
func (t *type0Info) cidWidth(cid uint32) float32 {
	for i := range t.w {
		r := &t.w[i]
		if cid >= r.lo && cid <= r.hi {
			if len(r.ws) == 1 {
				return r.ws[0]
			}
			return r.ws[cid-r.lo]
		}
	}
	return t.dw
}

// cidVMetrics returns the vertical displacement w1y and origin vector (vx, vy) for a CID in text space: /W2, else vx =
// w0/2 with /DW2's (vy, w1y).
func (t *type0Info) cidVMetrics(cid uint32, w0 float32) (w1y, vx, vy float32) {
	for i := range t.w2 {
		r := &t.w2[i]
		if cid >= r.lo && cid <= r.hi {
			e := r.entries[0]
			if len(r.entries) > 1 {
				e = r.entries[cid-r.lo]
			}
			return e[0], e[1], e[2]
		}
	}
	return t.dw2[1], w0 / 2, t.dw2[0]
}

// gid maps a CID to the descendant program's GID.
func (t *type0Info) gid(cid uint32) uint32 {
	switch {
	case t.cffCID != nil:
		return t.cffCID.gid(cid)
	case t.cidToGID != nil:
		if int(cid) < len(t.cidToGID) {
			return uint32(t.cidToGID[cid])
		}
		return 0
	default:
		return cid // Identity /CIDToGIDMap, and non-CID-keyed CFF descendants (CID = GID).
	}
}
