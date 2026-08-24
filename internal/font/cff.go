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
	"errors"
	"math"
	"strconv"

	"github.com/go-text/typesetting/font/cff"
	"github.com/go-text/typesetting/font/opentype"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// A minimal CFF (Compact Font Format) container reader, written against Adobe TN5176. go-text's cff package interprets
// charstrings and loads glyphs but does not expose the Top DICT's FontBBox or FontMatrix, which the engine needs
// because FreeType, and so the oracle's MuPDF build, takes a bare CFF font's ascender/descender from its FontBBox (see
// the package comment). The INDEX and DICT walkers are also the base for the CID-keyed charset/FDSelect reader Type0
// support uses.

var errBadCFF = errors.New("malformed CFF data")

// cffTop is the Top DICT subset the engine consumes.
type cffTop struct {
	bbox           [4]float32 // FontBBox: x0, y0, x1, y1 in font units (0 0 0 0 when absent)
	matrix         [6]float32 // FontMatrix (0.001 0 0 0.001 0 0 default)
	charsetOff     int        // charset offset (0/1/2 are the predefined charsets)
	charStringsOff int        // CharStrings INDEX offset (0 when absent)
	privOff        int        // Private DICT offset (0 when absent); the local Subrs offset is relative to it
	privSize       int        // Private DICT length in bytes (0 when absent)
	fdArrayOff     int        // FDArray INDEX offset, CID-keyed programs only (0 when absent)
	fdSelectOff    int        // FDSelect offset, CID-keyed programs only (0 when absent)
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
			var bbox [4]float32
			if cffNarrow(operands, bbox[:]) {
				top.bbox, top.hasBBox = bbox, true
			}
		case op == 0x0c07 && len(operands) >= 6: // FontMatrix (escaped operator 12 7)
			var matrix [6]float32
			if cffNarrow(operands, matrix[:]) {
				top.matrix, top.hasMatrix = matrix, true
			}
		case op == 15 && len(operands) >= 1: // charset offset
			top.charsetOff = clampDictOffset(operands[len(operands)-1])
		case op == 17 && len(operands) >= 1: // CharStrings offset
			top.charStringsOff = clampDictOffset(operands[len(operands)-1])
		case op == 18 && len(operands) >= 2: // Private: size then offset
			top.privSize = clampDictOffset(operands[len(operands)-2])
			top.privOff = clampDictOffset(operands[len(operands)-1])
		case op == 0x0c1e: // ROS (escaped operator 12 30): CID-keyed
			top.isCID = true
		case op == 0x0c24 && len(operands) >= 1: // FDArray (escaped operator 12 36)
			top.fdArrayOff = clampDictOffset(operands[len(operands)-1])
		case op == 0x0c25 && len(operands) >= 1: // FDSelect (escaped operator 12 37)
			top.fdSelectOff = clampDictOffset(operands[len(operands)-1])
		}
	}); err != nil {
		return nil, err
	}
	return top, nil
}

// cffNarrow narrows the leading DICT operands into out, all or nothing (like type1's numbers), so a partial /FontMatrix
// never mixes narrowed and default entries. parseCFFFloat rejects only a non-finite float64: a packed-BCD real such as
// 1e300 is finite there but ±Inf as float32, and while cffTop.metrics guards its own use of the bbox and matrix[3],
// the same matrix reaches cffInfo.matrix, where Font.GlyphPath builds a gfx.Matrix from it and every outline point
// comes back non-finite. Rejecting here closes both paths at their source, as type1.toFloat32 does for these keys.
func cffNarrow(operands []float64, out []float32) bool {
	for i := range out {
		f := float32(operands[i])
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return false
		}
		out[i] = f
	}
	return true
}

// clampDictOffset converts a DICT operand to a non-negative int offset (junk collapses to 0 = unset).
func clampDictOffset(v float64) int {
	if v < 0 || v > float64(math.MaxInt32) || math.IsNaN(v) {
		return 0
	}
	return int(v)
}

// cffCID is the CID→GID view of a CID-keyed CFF program, the inverse of the GID→CID map its charset stores (Adobe
// TN5176 section 13), which go-text's cff package does not expose.
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
	asc, desc = yMax/upem, yMin/upem
	// Both operands are finite (cffNarrow and type1's scanner reject anything else), but a /FontMatrix implying a tiny
	// upem still divides a large yMax past float32's range, and a non-finite ascender/descender reaching stext would
	// place every character quad at the page origin.
	if !isFiniteF(asc) || !isFiniteF(desc) {
		return 0, 0, false
	}
	return asc, desc, true
}

// cffIndex reads an INDEX at pos, returning up to maxEntries entry slices and the offset just past the INDEX. An INDEX
// is: count (Card16), offSize (Card8, 1-4), count+1 offsets (1-based), then the data. pos arrives from clampDictOffset
// (up to MaxInt32) and an offSize-4 entry offset spans the full uint32 range; every sum of them below stays inside the
// 64-bit int this engine requires, so no bound here can be slipped past by a wrap.
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
	if offEnd > len(data) {
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
	if last < 1 || end > len(data) {
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

// cffInfo is a bare CFF (Type1C) program prepared for glyph work: go-text's parsed font for the charstrings and the
// charset, the name→GID map swept from that charset, the subroutine arrays the budgeted Type 2 interpreter needs
// (cff_charstring.go), and the FontMatrix that carries charstring space to em space.
type cffInfo struct {
	font *cff.CFF
	// names maps charset glyph names to GIDs (go-text exposes name-per-GID; the sweep inverts it once).
	names map[string]uint32
	// subrs holds the global and local subroutine arrays a charstring may call, read from the same bytes go-text
	// parsed; nil when the container walk could not recover them, which leaves subroutine calls failing (and so the
	// glyph blank) rather than running unbudgeted.
	subrs *cffSubrs
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

// parseCFFGlyphBytes is the bytes-level half of parseCFFGlyphs (split out so the fuzzer can drive it directly). top may
// be nil, in which case the Top DICT is read here: the subroutine walk needs its Private/FDArray/FDSelect offsets.
func parseCFFGlyphBytes(raw []byte, top *cffTop) (info *cffInfo) {
	defer func() {
		if recover() != nil {
			info = nil
		}
	}()
	f, err := cff.Parse(raw)
	if err != nil || f == nil {
		// A rejected program may only be carrying a deprecated Private DICT operator, and the rewrite costs a copy of the
		// whole program, hence the retry rather than sanitizing every font up front.
		sanitized := sanitizeCFFPrivateDicts(raw, top)
		if sanitized == nil {
			return nil
		}
		if f, err = cff.Parse(sanitized); err != nil || f == nil {
			return nil
		}
		// The rewrite is byte-for-byte in place, so every offset still holds; the walks below read the sanitized copy so
		// they and go-text never disagree about what the container says.
		raw = sanitized
	}
	dict := top
	if dict == nil {
		if parsed, dictErr := parseCFFTopDict(raw); dictErr == nil {
			dict = parsed
		}
	}
	info = &cffInfo{font: f, matrix: [6]float32{0.001, 0, 0, 0.001, 0, 0}}
	if dict != nil {
		info.subrs = parseCFFSubrs(raw, dict, len(f.Charstrings))
	}
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

// Deprecated Private DICT operators, dropped between CFF specification 1.0 and 1.1 but still emitted by Adobe
// Distiller-era producers. go-text's Private DICT parser accepts only the 1.1 operator set and fails the entire font on
// anything else, where FreeType, and so MuPDF, skips what it does not recognize. Both take a single operand, as does
// initialRandomSeed, which go-text accepts and ignores, so overwriting the escaped operator's second byte removes the
// obstruction without moving a byte or changing any value the renderer consumes.
const (
	cffOpForceBoldThreshold = 15 // Escaped operator 12 15.
	cffOpLenIV              = 16 // Escaped operator 12 16.
	cffOpInitialRandomSeed  = 19 // Escaped operator 12 19.
)

// sanitizeCFFPrivateDicts returns a copy of a CFF program with the deprecated operators above rewritten in every
// Private DICT it declares, or nil when there was nothing to rewrite (so the caller has no reason to parse again). raw
// is never written to: it aliases cached stream data the rest of the engine still reads. top may be nil, in which case
// it is re-read here. The copy is taken before the walk rather than after a detection pass because this runs only
// once a parse has already failed.
func sanitizeCFFPrivateDicts(raw []byte, top *cffTop) []byte {
	if top == nil {
		parsed, err := parseCFFTopDict(raw)
		if err != nil {
			return nil
		}
		top = parsed
	}
	out := bytes.Clone(raw)
	changed := rewriteCFFPrivateDict(out, top.privOff, top.privSize)
	// A CID-keyed program keeps a Private DICT per FDArray entry (TN5176 section 18), built by the same tooling and
	// carrying the same deprecated operators; go-text parses all of them, so any single one still fails the font.
	if top.fdArrayOff > 0 {
		if fontDicts, _, err := cffIndex(out, top.fdArrayOff, maxCFFFontDicts); err == nil {
			for _, dict := range fontDicts {
				privOff, privSize := cffPrivateRange(dict)
				if rewriteCFFPrivateDict(out, privOff, privSize) {
					changed = true
				}
			}
		}
	}
	if !changed {
		return nil
	}
	return out
}

// rewriteCFFPrivateDict rewrites the deprecated operators of the Private DICT spanning [off, off+size) of data,
// reporting whether anything changed. A range that does not lie inside the program names no DICT, which is the same
// verdict cffLocalSubrs reaches for it.
func rewriteCFFPrivateDict(data []byte, off, size int) bool {
	if off <= 0 || size <= 0 || off+size > len(data) {
		return false
	}
	dict := data[off : off+size]
	changed := false
	// A positional walk, not a search: an operand byte can hold any value, so only stepping the encodings of TN5176
	// table 3 tells an operator from the middle of a number. Every step is forward and every index is guarded, so
	// hostile bytes end the walk rather than spinning or reaching outside the DICT.
	for i := 0; i < len(dict); {
		switch b := dict[i]; {
		case b <= 21: // Operator, where 12 escapes to a two-byte one.
			if b != 12 {
				i++
				continue
			}
			if i+1 >= len(dict) {
				return changed
			}
			if dict[i+1] == cffOpForceBoldThreshold || dict[i+1] == cffOpLenIV {
				dict[i+1] = cffOpInitialRandomSeed
				changed = true
			}
			i += 2
		case b == 28: // 16-bit integer.
			i += 3
		case b == 29: // 32-bit integer.
			i += 5
		case b == 30: // Real: packed BCD nibbles, ending at the first 0xf in either half of a byte.
			for i++; i < len(dict); {
				nibbles := dict[i]
				i++
				if nibbles&0x0f == 0x0f || nibbles&0xf0 == 0xf0 {
					break
				}
			}
		case b >= 32 && b <= 246: // 1-byte integer.
			i++
		case b >= 247 && b <= 254: // 2-byte integer.
			i += 2
		default: // 22..27, 31 and 255 are reserved, and occupy one byte apiece.
			i++
		}
	}
	return changed
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
