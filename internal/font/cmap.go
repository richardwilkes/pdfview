package font

import (
	"github.com/go-text/typesetting/font/opentype/tables"
)

// Character-to-GID lookup over specific sfnt 'cmap' subtables. go-text's ProcessCmap selects one "best"
// subtable, but simple-font code→GID mapping (ISO 32000-2 9.6.5.4) needs specific subtables tried in a pinned
// order — (3,1) Windows Unicode by AGL value, (3,0) Windows Symbol with the 0xF000 fold, (1,0) Macintosh Roman
// by code — so the engine keeps the parsed records and consults them directly. Formats 0, 4, 6, and 12 cover
// every subtable in practice for these platform/encoding pairs (format 2 is legacy CJK and is not consulted;
// 13 is last-resort fonts; 14 is variation selectors).

// cmapTable is one cmap subtable the engine can query.
type cmapTable struct {
	sub tables.CmapSubtable
}

// lookup returns the GID for a character code, 0 (.notdef) when unmapped.
func (c *cmapTable) lookup(code uint32) uint32 {
	switch sub := c.sub.(type) {
	case tables.CmapSubtable0:
		if code < 256 {
			return uint32(sub.GlyphIdArray[code])
		}
	case tables.CmapSubtable4:
		return cmap4Lookup(&sub, code)
	case tables.CmapSubtable6:
		if code >= uint32(sub.FirstCode) {
			if i := code - uint32(sub.FirstCode); i < uint32(len(sub.GlyphIdArray)) {
				return uint32(sub.GlyphIdArray[i])
			}
		}
	case tables.CmapSubtable12:
		return cmapGroupLookup(sub.Groups, code)
	}
	return 0
}

// cmap4Lookup implements format 4 segment lookup (the ubiquitous Windows format).
func cmap4Lookup(sub *tables.CmapSubtable4, code uint32) uint32 {
	if code > 0xFFFF {
		return 0
	}
	c := uint16(code)
	segs := len(sub.EndCode)
	if segs == 0 || len(sub.StartCode) != segs || len(sub.IdDelta) != segs || len(sub.IdRangeOffsets) != segs {
		return 0
	}
	// Binary search for the first segment whose EndCode >= c.
	lo, hi := 0, segs-1
	for lo < hi {
		mid := (lo + hi) / 2
		if sub.EndCode[mid] < c {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if sub.EndCode[lo] < c || sub.StartCode[lo] > c {
		return 0
	}
	if sub.IdRangeOffsets[lo] == 0 {
		return uint32(c + sub.IdDelta[lo]) // Wrapping uint16 addition, per the spec.
	}
	// The range offset addresses into GlyphIDArray relative to this segment's IdRangeOffsets slot: the spec
	// defines it as a byte offset from the slot's own position in the file. Slot lo sits (segs-lo) uint16s
	// before GlyphIDArray's start.
	idx := int(sub.IdRangeOffsets[lo])/2 - (segs - lo) + int(c-sub.StartCode[lo])
	if idx < 0 || 2*idx+1 >= len(sub.GlyphIDArray) {
		return 0
	}
	gid := uint32(sub.GlyphIDArray[2*idx])<<8 | uint32(sub.GlyphIDArray[2*idx+1])
	if gid == 0 {
		return 0
	}
	return (gid + uint32(sub.IdDelta[lo])) & 0xFFFF
}

// cmapGroupLookup implements format 12 sequential-group lookup.
func cmapGroupLookup(groups []tables.SequentialMapGroup, code uint32) uint32 {
	lo, hi := 0, len(groups)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch g := groups[mid]; {
		case code < g.StartCharCode:
			hi = mid - 1
		case code > g.EndCharCode:
			lo = mid + 1
		default:
			return g.StartGlyphID + (code - g.StartCharCode)
		}
	}
	return 0
}

// pickCmaps selects the subtables the code→GID chains consult from a parsed cmap table: the Unicode table
// ((3,1) preferred, then (3,10), then any platform 0, matching FreeType's Unicode charmap selection), the
// (3,0) Windows Symbol table, and the (1,0) Macintosh Roman table.
func pickCmaps(cm tables.Cmap) (unicode, symbol, macRoman *cmapTable) {
	var uniScore int
	for _, rec := range cm.Records {
		if !usableCmapSubtable(rec.Subtable) {
			continue
		}
		switch {
		case rec.PlatformID == 3 && rec.EncodingID == 1:
			if uniScore < 3 {
				unicode, uniScore = &cmapTable{sub: rec.Subtable}, 3
			}
		case rec.PlatformID == 3 && rec.EncodingID == 10:
			if uniScore < 2 {
				unicode, uniScore = &cmapTable{sub: rec.Subtable}, 2
			}
		case rec.PlatformID == 0:
			if uniScore < 1 {
				unicode, uniScore = &cmapTable{sub: rec.Subtable}, 1
			}
		case rec.PlatformID == 3 && rec.EncodingID == 0:
			if symbol == nil {
				symbol = &cmapTable{sub: rec.Subtable}
			}
		case rec.PlatformID == 1 && rec.EncodingID == 0:
			if macRoman == nil {
				macRoman = &cmapTable{sub: rec.Subtable}
			}
		}
	}
	return unicode, symbol, macRoman
}

// usableCmapSubtable reports whether lookup understands the subtable's format.
func usableCmapSubtable(sub tables.CmapSubtable) bool {
	switch sub.(type) {
	case tables.CmapSubtable0, tables.CmapSubtable4, tables.CmapSubtable6, tables.CmapSubtable12:
		return true
	default:
		return false
	}
}
