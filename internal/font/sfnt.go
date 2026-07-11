package font

import (
	"bytes"
	"encoding/binary"

	otfont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// sfntInfo is a parsed embedded TrueType/OpenType program: the quad metrics, the cmap subtables the code→GID
// chains consult, and the go-text face that supplies glyph outlines and fallback advances.
type sfntInfo struct {
	// face is the go-text view of the program, used for glyph outlines (GlyphDataOutline) and hmtx advances.
	// It is nil when go-text rejects the program (such as a subset with no cmap table at all — go-text
	// requires one); metrics and cmap lookups still work then, but there are no outlines, so the font renders
	// through its substitute.
	face *otfont.Face
	// cmapUnicode/cmapSymbol/cmapMacRoman are the subtables of the pinned lookup chains (nil when absent).
	cmapUnicode  *cmapTable
	cmapSymbol   *cmapTable
	cmapMacRoman *cmapTable
	data         []byte
	ascender     float32 // em units
	descender    float32 // em units
	upem         float32
	nGlyphs      int
}

// parseSFNTStream decodes and parses a FontFile2/FontFile3(OpenType) stream. Any failure — undecodable
// stream, unparseable font, hostile bytes that panic the parser — yields nil, and the caller substitutes.
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

// parseSFNT reads the metrics tables of an sfnt font, following FreeType's rules (which the oracle's MuPDF
// build inherits — FreeType is BSD-licensed and fine to consult): ascender/descender come from hhea; when
// both are zero, from OS/2 sTypoAscender/sTypoDescender; when those are zero too, from usWinAscent and
// -usWinDescent. All divided by head's unitsPerEm. Hostile bytes that panic the parser yield nil (the guard
// lives here, not only in parseSFNTStream, so the fuzzer exercises the same contract).
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
	if hheaRaw, hheaErr := ld.RawTable(opentype.MustNewTag("hhea")); hheaErr == nil {
		if hhea, _, parseErr := tables.ParseHhea(hheaRaw); parseErr == nil {
			asc, desc = float32(hhea.Ascender), float32(hhea.Descender)
		}
	}
	if asc == 0 && desc == 0 {
		if os2Raw, os2Err := ld.RawTable(opentype.MustNewTag("OS/2")); os2Err == nil {
			if os2, _, parseErr := tables.ParseOs2(os2Raw); parseErr == nil {
				asc, desc = float32(os2.STypoAscender), float32(os2.STypoDescender)
				if asc == 0 && desc == 0 && len(os2Raw) >= 78 {
					// usWinAscent/usWinDescent sit at fixed offsets 74/76; both are unsigned, with the
					// descent measured downward.
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
	// The go-text font/face for outlines. NewFont re-reads from the same loader; its failure (it requires
	// cmap/head/maxp) leaves face nil without invalidating the metrics above.
	if ft, ftErr := otfont.NewFont(ld); ftErr == nil {
		info.face = otfont.NewFace(ft)
	}
	return info
}

// gid runs the pinned code→GID chain for an embedded sfnt program (plan.md font-pipeline table, verified
// against the glaive golden pixels):
//
//   - non-symbolic fonts: the encoding's glyph name, first through the AGL to Unicode into the Unicode cmap,
//     then through the reverse Mac Roman encoding into the (1,0) cmap (standard viewer practice — glaive's
//     macOS subsets carry only a (1,0) table);
//   - then, or for symbolic fonts directly: the raw code into (3,0) — bare, then folded into the 0xF000
//     symbol page — and the raw code into (1,0);
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
