package font

import (
	"bytes"
	"encoding/binary"

	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// sfntInfo is a parsed embedded TrueType/OpenType program: the quad metrics now, and the raw bytes retained
// for the glyph work (cmap/outlines) that lands next in M6.
type sfntInfo struct {
	data      []byte
	ascender  float32 // em units
	descender float32 // em units
	upem      float32
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
// -usWinDescent. All divided by head's unitsPerEm.
func parseSFNT(raw []byte) *sfntInfo {
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
	info := &sfntInfo{data: raw, upem: upem}
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
	return info
}
