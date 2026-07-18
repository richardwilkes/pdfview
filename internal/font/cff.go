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
	"errors"
	"math"
	"strconv"

	"github.com/go-text/typesetting/font/cff"
	"github.com/go-text/typesetting/font/opentype"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// A minimal CFF (Compact Font Format) container reader, written against Adobe TN5176. go-text's cff package provides
// charstring interpretation and glyph loading but does not expose the Top DICT's FontBBox or FontMatrix, which the
// engine needs because FreeType — and therefore the oracle's MuPDF build — takes a bare CFF font's ascender/descender
// from its FontBBox (see internal/font's package comment). The INDEX and DICT walkers here are also the base for the
// CID-keyed charset/FDSelect reader Type0 support uses.

var errBadCFF = errors.New("malformed CFF data")

// cffTop is the Top DICT subset the engine consumes.
type cffTop struct {
	bbox           [4]float32 // FontBBox: x0, y0, x1, y1 in font units (0 0 0 0 when absent)
	matrix         [6]float32 // FontMatrix (0.001 0 0 0.001 0 0 default)
	charsetOff     int        // charset offset (0/1/2 are the predefined charsets)
	charStringsOff int        // CharStrings INDEX offset (0 when absent)
	hasBBox        bool
	hasMatrix      bool
	isCID          bool // ROS present: a CID-keyed program (charset maps GIDs to CIDs, not name SIDs)
}

// parseCFFTopDict reads the header, skips the Name INDEX, and decodes the first Top DICT.
func parseCFFTopDict(data []byte) (*cffTop, error) {
	if len(data) < 4 {
		return nil, errBadCFF
	}
	hdrSize := int(data[2])
	if hdrSize < 4 || hdrSize > len(data) {
		return nil, errBadCFF
	}
	pos, err := cffSkipIndex(data, hdrSize) // Name INDEX
	if err != nil {
		return nil, err
	}
	entries, _, err := cffIndex(data, pos, 1)
	if err != nil || len(entries) == 0 {
		return nil, errBadCFF
	}
	top := &cffTop{matrix: [6]float32{0.001, 0, 0, 0.001, 0, 0}}
	if err = cffWalkDict(entries[0], func(op int, operands []float64) {
		switch {
		case op == 5 && len(operands) >= 4: // FontBBox
			for i := range 4 {
				top.bbox[i] = float32(operands[i])
			}
			top.hasBBox = true
		case op == 0x0c07 && len(operands) >= 6: // FontMatrix (escaped operator 12 7)
			for i := range 6 {
				top.matrix[i] = float32(operands[i])
			}
			top.hasMatrix = true
		case op == 15 && len(operands) >= 1: // charset offset
			top.charsetOff = clampDictOffset(operands[len(operands)-1])
		case op == 17 && len(operands) >= 1: // CharStrings offset
			top.charStringsOff = clampDictOffset(operands[len(operands)-1])
		case op == 0x0c1e: // ROS (escaped operator 12 30): CID-keyed
			top.isCID = true
		}
	}); err != nil {
		return nil, err
	}
	return top, nil
}

// clampDictOffset converts a DICT operand to a non-negative int offset (junk collapses to 0 = unset).
func clampDictOffset(v float64) int {
	if v < 0 || v > float64(math.MaxInt32) || math.IsNaN(v) {
		return 0
	}
	return int(v)
}

// cffCID is the CID→GID view of a CID-keyed CFF program, read from its charset per Adobe TN5176 section 13 (go-text's
// cff package parses CID programs for glyph loading but does not expose the charset). For CID fonts the charset maps
// each GID to its CID; the engine needs the inverse.
type cffCID struct {
	cidToGID map[uint32]uint32
	nGlyphs  int
	identity bool // Predefined charsets (offsets 0-2) degrade to CID = GID for CID-keyed programs.
}

// gid maps a CID to a GID (0 when unmapped).
func (c *cffCID) gid(cid uint32) uint32 {
	if c.identity {
		if int(cid) < c.nGlyphs {
			return cid
		}
		return 0
	}
	return c.cidToGID[cid]
}

// parseCFFCharsetCID reads a CID-keyed program's charset. nGlyphs comes from the CharStrings INDEX count.
func parseCFFCharsetCID(data []byte, top *cffTop) *cffCID {
	if top == nil || !top.isCID {
		return nil
	}
	nGlyphs := cffIndexCount(data, top.charStringsOff)
	if nGlyphs <= 0 || nGlyphs > 65536 {
		return nil
	}
	out := &cffCID{nGlyphs: nGlyphs}
	if top.charsetOff <= 2 { // Predefined charsets are meaningless for CID keying; degrade to identity.
		out.identity = true
		return out
	}
	pos := top.charsetOff
	if pos >= len(data) {
		return nil
	}
	format := data[pos]
	pos++
	out.cidToGID = make(map[uint32]uint32, min(nGlyphs, 4096))
	put := func(cid, gid uint32) {
		if _, exists := out.cidToGID[cid]; !exists { // First wins on duplicates.
			out.cidToGID[cid] = gid
		}
	}
	put(0, 0) // GID 0 is always CID 0 (.notdef); the charset lists GIDs from 1.
	switch format {
	case 0:
		for gid := 1; gid < nGlyphs; gid++ {
			if pos+2 > len(data) {
				break
			}
			put(uint32(data[pos])<<8|uint32(data[pos+1]), uint32(gid))
			pos += 2
		}
	case 1, 2:
		nLeftSize := 1
		if format == 2 {
			nLeftSize = 2
		}
		for gid := 1; gid < nGlyphs; {
			if pos+2+nLeftSize > len(data) {
				break
			}
			first := uint32(data[pos])<<8 | uint32(data[pos+1])
			pos += 2
			nLeft := int(data[pos])
			if nLeftSize == 2 {
				nLeft = nLeft<<8 | int(data[pos+1])
			}
			pos += nLeftSize
			for i := 0; i <= nLeft && gid < nGlyphs; i++ {
				put(first+uint32(i), uint32(gid))
				gid++
			}
		}
	default:
		return nil
	}
	return out
}

// cffIndexCount returns the entry count of the INDEX at pos, or -1 when unreadable.
func cffIndexCount(data []byte, pos int) int {
	if pos <= 0 || pos+2 > len(data) {
		return -1
	}
	return int(data[pos])<<8 | int(data[pos+1])
}

// metrics converts the Top DICT to em-normalized ascender/descender the FreeType way: the FontBBox's yMax/yMin divided
// by the units-per-em implied by the FontMatrix (1/|yy|, 1000 for the standard matrix).
func (t *cffTop) metrics() (asc, desc float32, ok bool) {
	if !t.hasBBox || (t.bbox[1] == 0 && t.bbox[3] == 0) {
		return 0, 0, false
	}
	upem := float32(1000)
	if t.hasMatrix && t.matrix[3] != 0 {
		yy := t.matrix[3]
		if yy < 0 {
			yy = -yy
		}
		upem = 1 / yy
	}
	if upem <= 0 || math.IsNaN(float64(upem)) || math.IsInf(float64(upem), 0) {
		return 0, 0, false
	}
	yMin, yMax := t.bbox[1], t.bbox[3]
	if yMin > yMax {
		yMin, yMax = yMax, yMin
	}
	return yMax / upem, yMin / upem, true
}

// cffIndex reads an INDEX at pos, returning up to maxEntries entry slices and the offset just past the INDEX. An INDEX
// is: count (Card16), offSize (Card8, 1-4), count+1 offsets (1-based), then the data.
func cffIndex(data []byte, pos, maxEntries int) (entries [][]byte, next int, err error) {
	if pos < 0 || pos+2 > len(data) {
		return nil, 0, errBadCFF
	}
	count := int(data[pos])<<8 | int(data[pos+1])
	pos += 2
	if count == 0 {
		return nil, pos, nil
	}
	if pos >= len(data) {
		return nil, 0, errBadCFF
	}
	offSize := int(data[pos])
	pos++
	if offSize < 1 || offSize > 4 {
		return nil, 0, errBadCFF
	}
	offEnd := pos + (count+1)*offSize
	if offEnd < pos || offEnd > len(data) {
		return nil, 0, errBadCFF
	}
	offset := func(i int) int {
		v := 0
		for b := range offSize {
			v = v<<8 | int(data[pos+i*offSize+b])
		}
		return v
	}
	dataStart := offEnd - 1 // Offsets are 1-based from the byte before the data.
	last := offset(count)
	end := dataStart + last
	if last < 1 || end < dataStart || end > len(data) {
		return nil, 0, errBadCFF
	}
	n := min(count, maxEntries)
	entries = make([][]byte, 0, n)
	for i := range n {
		lo, hi := offset(i), offset(i+1)
		if lo < 1 || hi < lo || dataStart+hi > len(data) {
			return nil, 0, errBadCFF
		}
		entries = append(entries, data[dataStart+lo:dataStart+hi])
	}
	return entries, end, nil
}

// cffSkipIndex advances past an INDEX without materializing entries.
func cffSkipIndex(data []byte, pos int) (int, error) {
	_, next, err := cffIndex(data, pos, 0)
	return next, err
}

// cffWalkDict decodes DICT tokens (TN5176 table 3/4), invoking fn for each operator with its operands.
func cffWalkDict(dict []byte, fn func(op int, operands []float64)) error {
	var operands []float64
	const maxDictOperands = 48 // The largest legal operand count is small; floods are hostile.
	for i := 0; i < len(dict); {
		b0 := int(dict[i])
		switch {
		case b0 <= 21: // Operator.
			op := b0
			i++
			if b0 == 12 {
				if i >= len(dict) {
					return errBadCFF
				}
				op = 0x0c00 | int(dict[i])
				i++
			}
			fn(op, operands)
			operands = operands[:0]
			continue
		case b0 == 28:
			if i+3 > len(dict) {
				return errBadCFF
			}
			operands = append(operands, float64(int16(uint16(dict[i+1])<<8|uint16(dict[i+2]))))
			i += 3
		case b0 == 29:
			if i+5 > len(dict) {
				return errBadCFF
			}
			v := uint32(dict[i+1])<<24 | uint32(dict[i+2])<<16 | uint32(dict[i+3])<<8 | uint32(dict[i+4])
			operands = append(operands, float64(int32(v)))
			i += 5
		case b0 == 30: // Real: packed BCD nibbles until 0xf.
			v, n, err := cffReal(dict[i+1:])
			if err != nil {
				return err
			}
			operands = append(operands, v)
			i += 1 + n
		case b0 >= 32 && b0 <= 246:
			operands = append(operands, float64(b0-139))
			i++
		case b0 >= 247 && b0 <= 250:
			if i+2 > len(dict) {
				return errBadCFF
			}
			operands = append(operands, float64((b0-247)*256+int(dict[i+1])+108))
			i += 2
		case b0 >= 251 && b0 <= 254:
			if i+2 > len(dict) {
				return errBadCFF
			}
			operands = append(operands, float64(-(b0-251)*256-int(dict[i+1])-108))
			i += 2
		default: // 22..27 and 31 are reserved.
			return errBadCFF
		}
		if len(operands) > maxDictOperands {
			return errBadCFF
		}
	}
	return nil
}

// cffReal decodes a packed-BCD real, returning its value and the bytes consumed.
func cffReal(data []byte) (value float64, consumed int, err error) {
	var sb []byte
	for i := range data {
		for _, nib := range [2]byte{data[i] >> 4, data[i] & 0xf} {
			switch {
			case nib <= 9:
				sb = append(sb, '0'+nib)
			case nib == 0xa:
				sb = append(sb, '.')
			case nib == 0xb:
				sb = append(sb, 'E')
			case nib == 0xc:
				sb = append(sb, 'E', '-')
			case nib == 0xe:
				sb = append(sb, '-')
			case nib == 0xf:
				v, parseErr := parseCFFFloat(string(sb))
				if parseErr != nil {
					return 0, 0, errBadCFF
				}
				return v, i + 1, nil
			default: // 0xd is reserved.
				return 0, 0, errBadCFF
			}
		}
		if len(sb) > 64 {
			return 0, 0, errBadCFF
		}
	}
	return 0, 0, errBadCFF
}

// parseCFFFloat parses the ASCII form assembled from packed BCD, rejecting non-finite results.
func parseCFFFloat(s string) (float64, error) {
	if s == "" {
		return 0, errBadCFF
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errBadCFF
	}
	return v, nil
}

// parseCFFTopFromStream decodes a FontFile3 stream and extracts its Top DICT, tolerating hostile bytes.
func parseCFFTopFromStream(d *cos.Document, s *cos.Stream) *cffTop {
	raw, err := d.StreamData(s)
	if err != nil || len(raw) == 0 {
		return nil
	}
	top, err := parseCFFTopDict(raw)
	if err != nil {
		return nil
	}
	return top
}

// cffInfo is a bare CFF (Type1C) program prepared for glyph work: go-text's parsed font for charstring interpretation,
// the name→GID map swept from its charset, and the FontMatrix that carries charstring space to em space.
type cffInfo struct {
	font *cff.CFF
	// names maps charset glyph names to GIDs (go-text exposes name-per-GID; the sweep inverts it once).
	names map[string]uint32
	// matrix is the Top DICT FontMatrix (charstring units → em space at size 1).
	matrix [6]float32
}

// parseCFFGlyphs prepares a FontFile3/Type1C stream for glyph loading, tolerating hostile bytes (panics and parse
// errors yield nil, and the caller renders through the substitute). top supplies the FontMatrix already read by
// parseCFFTopFromStream.
func parseCFFGlyphs(d *cos.Document, s *cos.Stream, top *cffTop) *cffInfo {
	raw, err := d.StreamData(s)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return parseCFFGlyphBytes(raw, top)
}

// parseCFFGlyphBytes is the bytes-level half of parseCFFGlyphs (split out so the fuzzer can drive it directly).
func parseCFFGlyphBytes(raw []byte, top *cffTop) (info *cffInfo) {
	defer func() {
		if recover() != nil {
			info = nil
		}
	}()
	f, err := cff.Parse(raw)
	if err != nil || f == nil {
		return nil
	}
	info = &cffInfo{font: f, matrix: [6]float32{0.001, 0, 0, 0.001, 0, 0}}
	if top != nil && top.hasMatrix {
		info.matrix = top.matrix
	}
	info.names = make(map[string]uint32, len(f.Charstrings))
	for gid := range len(f.Charstrings) {
		if name := f.GlyphName(opentype.GID(gid)); name != "" {
			if _, exists := info.names[name]; !exists { // First wins on duplicate names.
				info.names[name] = uint32(gid)
			}
		}
	}
	return info
}

// gid maps a code to a GID for a bare CFF program: the encoding's glyph name against the charset sweep, with the code
// itself as the last resort (subset programs with junk charsets).
func (c *cffInfo) gid(code uint32, name string) uint32 {
	if name != "" {
		if g, ok := c.names[name]; ok {
			return g
		}
	}
	if int(code) < len(c.font.Charstrings) {
		return code
	}
	return 0
}
