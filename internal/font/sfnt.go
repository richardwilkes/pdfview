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
	"bytes"
	"encoding/binary"

	otfont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// sfntInfo is a parsed embedded TrueType/OpenType program: the quad metrics, the cmap subtables the code→GID chains
// consult, and the go-text face that supplies glyph outlines and fallback advances.
type sfntInfo struct {
	// face is the go-text view of the program, used for glyph outlines (GlyphDataOutline) and hmtx advances. It is nil
	// when go-text rejects the program (a subset with no cmap table, which go-text requires); metrics and cmap lookups
	// still work then, and TrueType-flavored programs fall back to the direct glyf walker for outlines.
	face *otfont.Face
	// glyf is the direct glyf outline walker (glyf.go): the only outline source for CIDFontType2 programs (whose
	// subsets routinely omit cmap) and the fallback for cmap-less simple TrueType programs.
	glyf *glyfInfo
	// cff is the wrapped 'CFF ' table of a CFF-flavored OpenType program, prepared for the budgeted Type 2 interpreter
	// (cff_charstring.go). Font.GlyphPath prefers it over face, whose GlyphDataOutline reaches the same charstrings
	// through go-text's unbudgeted loader. simpleGlyphs/cidGlyphs ignore it: a CFF-flavored program go-text refused a
	// face for is still substituted away.
	cff *cffInfo
	// cmapUnicode/cmapSymbol/cmapMacRoman are the subtables of the pinned lookup chains (nil when absent).
	cmapUnicode  *cmapTable
	cmapSymbol   *cmapTable
	cmapMacRoman *cmapTable
	data         []byte
	// hmtx holds the program's horizontal advances in font units, one per longHorMetric entry; a GID past the end takes
	// the last one (the monospace tail the format defines). It is the advance source when face is nil.
	hmtx      []uint16
	ascender  float32 // em units
	descender float32 // em units
	upem      float32
	nGlyphs   int
}

// simpleGlyphs reports whether a parsed program can supply outlines for a simple font: go-text accepted it (GlyphPath's
// face path) or its glyf/loca walker was built (the cmap-less fallback). A program that yields metrics but has neither
// draws nothing, so the loaders treat it as no program at all and the substitute owns both shapes and metrics. Safe on
// a nil receiver: a stream that did not parse answers the same "no".
func (s *sfntInfo) simpleGlyphs() bool { return s != nil && (s.face != nil || s.glyf != nil) }

// cidGlyphs reports the same for a CIDFontType2 program, where GlyphPath uses only the direct glyf walker (CID subsets
// routinely omit the cmap table go-text requires), so a go-text face alone is not a usable outline source.
func (s *sfntInfo) cidGlyphs() bool { return s != nil && s.glyf != nil }

// hasAdvances reports whether the program can supply the /Widths-absent advance fallback (programAdvance). Safe on a
// nil receiver: a font with no sfnt answers "no", which routes it to the standard-14 AFM widths.
func (s *sfntInfo) hasAdvances() bool {
	return s != nil && s.upem > 0 && (s.face != nil || len(s.hmtx) != 0)
}

// advance returns the hmtx advance for a GID in em units. The last hmtx entry applies to every glyph past
// numberOfHMetrics (the OpenType monospace tail), but only up to the program's glyph count, where go-text's own reader
// stops too.
func (s *sfntInfo) advance(gid uint32) (float32, bool) {
	if len(s.hmtx) == 0 || s.upem <= 0 || uint64(gid) >= uint64(s.nGlyphs) {
		return 0, false
	}
	i := uint64(gid)
	if i >= uint64(len(s.hmtx)) {
		i = uint64(len(s.hmtx)) - 1
	}
	return float32(s.hmtx[i]) / s.upem, true
}

// parseSFNTStream decodes and parses a FontFile2/FontFile3(OpenType) stream. Any failure — undecodable stream,
// unparseable font, hostile bytes that panic the parser — yields nil, and the caller substitutes.
func parseSFNTStream(d *cos.Document, s *cos.Stream) (info *sfntInfo) {
	defer func() {
		if recover() != nil {
			info = nil
		}
	}()
	raw, err := d.StreamData(s)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return parseSFNT(raw)
}

// parseSFNT reads the metrics tables of an sfnt font by FreeType's rules, which the oracle's MuPDF build inherits:
// ascender/descender come from hhea; when both are zero, from OS/2 sTypoAscender/sTypoDescender; when those are zero
// too, from usWinAscent and -usWinDescent. All are divided by head's unitsPerEm. Hostile bytes that panic the parser
// yield nil (the guard lives here as well as in parseSFNTStream so the fuzzer exercises the same contract).
func parseSFNT(raw []byte) (info *sfntInfo) {
	defer func() {
		if recover() != nil {
			info = nil
		}
	}()
	ld, err := opentype.NewLoader(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	headRaw, err := ld.RawTable(opentype.MustNewTag("head"))
	if err != nil {
		return nil
	}
	head, _, err := tables.ParseHead(headRaw)
	if err != nil {
		return nil
	}
	upem := float32(head.Upem())
	info = &sfntInfo{data: raw, upem: upem}
	var asc, desc float32
	nHMetrics := 0
	if hheaRaw, hheaErr := ld.RawTable(opentype.MustNewTag("hhea")); hheaErr == nil {
		if hhea, _, parseErr := tables.ParseHhea(hheaRaw); parseErr == nil {
			asc, desc = float32(hhea.Ascender), float32(hhea.Descender)
			nHMetrics = int(hhea.NumOfLongMetrics)
		}
	}
	if asc == 0 && desc == 0 {
		if os2Raw, os2Err := ld.RawTable(opentype.MustNewTag("OS/2")); os2Err == nil {
			if os2, _, parseErr := tables.ParseOs2(os2Raw); parseErr == nil {
				asc, desc = float32(os2.STypoAscender), float32(os2.STypoDescender)
				if asc == 0 && desc == 0 && len(os2Raw) >= 78 {
					// usWinAscent/usWinDescent sit at fixed offsets 74/76; both are unsigned, with the descent measured
					// downward.
					asc = float32(binary.BigEndian.Uint16(os2Raw[74:]))
					desc = -float32(binary.BigEndian.Uint16(os2Raw[76:]))
				}
			}
		}
	}
	info.ascender, info.descender = asc/upem, desc/upem
	if maxpRaw, maxpErr := ld.RawTable(opentype.MustNewTag("maxp")); maxpErr == nil {
		if maxp, _, parseErr := tables.ParseMaxp(maxpRaw); parseErr == nil {
			info.nGlyphs = int(maxp.NumGlyphs)
		}
	}
	if cmapRaw, cmapErr := ld.RawTable(opentype.MustNewTag("cmap")); cmapErr == nil {
		if cm, _, parseErr := tables.ParseCmap(cmapRaw); parseErr == nil {
			info.cmapUnicode, info.cmapSymbol, info.cmapMacRoman = pickCmaps(cm)
		}
	}
	info.hmtx = parseHMetrics(ld, nHMetrics)
	// The go-text font/face for outlines. NewFont re-reads from the same loader; its failure (it requires
	// cmap/head/maxp) leaves face nil without invalidating the metrics above.
	if ft, ftErr := otfont.NewFont(ld); ftErr == nil {
		info.face = otfont.NewFace(ft)
	}
	info.glyf = newGlyfInfo(ld, upem, info.nGlyphs)
	if cffRaw, cffErr := ld.RawTable(opentype.MustNewTag("CFF ")); cffErr == nil {
		if prepared := parseCFFGlyphBytes(cffRaw, nil); prepared != nil &&
			len(prepared.font.Charstrings) == info.nGlyphs {
			// The charstring count must agree with maxp, the same gate go-text applies before its face draws from a
			// 'CFF ' table, so a program with a mismatched CFF alongside a usable glyf table keeps rendering from the
			// glyf.
			info.cff = prepared
		}
	}
	return info
}

// parseHMetrics reads the advance of each longHorMetric record from hmtx (a 4-byte record whose leading uint16 is the
// advance width). A table shorter than hhea's count is read as far as it goes, like every other truncated table here.
func parseHMetrics(ld *opentype.Loader, nHMetrics int) []uint16 {
	if nHMetrics <= 0 {
		return nil
	}
	raw, err := ld.RawTable(opentype.MustNewTag("hmtx"))
	if err != nil {
		return nil
	}
	n := min(nHMetrics, len(raw)/4)
	if n <= 0 {
		return nil
	}
	out := make([]uint16, n)
	for i := range out {
		out[i] = binary.BigEndian.Uint16(raw[4*i:])
	}
	return out
}

// gid runs the pinned code→GID chain for an embedded sfnt program (verified against the glaive golden pixels):
//
//   - non-symbolic fonts: the encoding's glyph name, first through the AGL to Unicode into the Unicode cmap, then
//     through the reverse Mac Roman encoding into the (1,0) cmap (standard viewer practice — glaive's macOS subsets
//     carry only a (1,0) table);
//   - then, or for symbolic fonts directly: the raw code into (3,0) — bare, then folded into the 0xF000 symbol page —
//     and the raw code into (1,0);
//   - last resort: the code as the GID (subset fonts with no usable cmap).
//
// Returns 0 (.notdef) when nothing maps.
func (s *sfntInfo) gid(code uint32, name string, symbolic bool) uint32 {
	if !symbolic && name != "" {
		if r := firstRune(GlyphNameToUnicode(name)); r != 0 && s.cmapUnicode != nil {
			if g := s.cmapUnicode.lookup(uint32(r)); g != 0 {
				return g
			}
		}
		if s.cmapMacRoman != nil {
			if mac, ok := macRomanCode(name); ok {
				if g := s.cmapMacRoman.lookup(mac); g != 0 {
					return g
				}
			}
		}
	}
	if s.cmapSymbol != nil {
		if g := s.cmapSymbol.lookup(code); g != 0 {
			return g
		}
		if code <= 0xFF {
			if g := s.cmapSymbol.lookup(0xF000 | code); g != 0 {
				return g
			}
		}
	}
	if s.cmapMacRoman != nil {
		if g := s.cmapMacRoman.lookup(code); g != 0 {
			return g
		}
	}
	if symbolic && s.cmapUnicode != nil { // Symbolic fonts sometimes carry only a Unicode table.
		if g := s.cmapUnicode.lookup(code); g != 0 {
			return g
		}
	}
	if int(code) < s.nGlyphs {
		return code
	}
	return 0
}
