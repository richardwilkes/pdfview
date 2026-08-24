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
	"cmp"
	"math"
	"slices"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// PDF CMaps (ISO 32000-2 9.7.5, 9.10.3): the code→CID maps of Type0 font /Encoding entries and, through the bf
// operators, ToUnicode maps. CMap content is lexically PDF surface syntax, so cos.Lexer tokenizes it as it does content
// streams; the operators consulted are begincodespacerange/endcodespacerange, begincidrange/begincidchar,
// beginbfrange/beginbfchar, usecmap, and /WMode. Everything else (the CIDSystemInfo boilerplate, dict/proc syntax) is
// skipped by the same sliding-operand-window convention the content interpreter uses.

// CMap resource caps. maxCodespaces is far tighter than maxCMapRanges because codespaces are consulted per decoded
// character code: nextCode probes the list at each of the four code lengths and, for a code in none of them, walks it
// again for the longest matching prefix. Real CMaps declare a handful (the predefined Adobe ones stay under ten, and
// the PostScript convention caps one begincodespacerange block at 100 entries); 65536 of them would make a
// multi-megabyte show operator cost minutes of CPU. Entries past the cap are dropped.
const (
	maxCMapRanges  = 65536
	maxCodespaces  = 128
	maxCMapDepth   = 4       // usecmap chains
	maxCMapOps     = 1 << 20 // token budget per CMap stream
	maxCMapOperand = 64      // sliding operand window
)

// codespaceRange is one codespace entry: codes of nBytes length whose value lies in [lo, hi].
type codespaceRange struct {
	lo, hi uint32
	nBytes uint8
}

// cidRangeEntry maps the code range [lo, hi] to CIDs starting at cid. The length of the codes it applies to is the
// bucket it sits in (cmapPDF.cids), not a field.
type cidRangeEntry struct {
	lo, hi, cid uint32
}

// bfEntry maps the code range [lo, hi] to target strings: dst for a contiguous mapping (the last UTF-16 code unit
// increments across the range), dstArray for an explicit per-code list. trimmed carries the increment the codes clipped
// off a contiguous entry's front by sortRanges already consumed, so dst stays the string the CMap wrote for lo. Like
// cidRangeEntry, the code length it applies to is its bucket in cmapPDF.bf.
type bfEntry struct {
	dst      []byte
	dstArray [][]byte
	lo, hi   uint32
	trimmed  uint16
}

// cmapPDF is one parsed CMap. codespaces is the declaration order the longest-prefix rule in nextCode walks; byLen is
// the same set bucketed by code length and merged into sorted, disjoint ranges, which the per-code membership test
// binary searches.
//
// The cid and bf lists are bucketed by code length because ISO 32000-2 9.7.6.2 scopes a cidrange to codes of its own
// length: a CMap may declare both a 1-byte and a 2-byte codespace, and entries of the two lengths address unrelated
// codes even where their values coincide, so one value-keyed list would let the wider entry shadow the narrower one
// and resolve a 1-byte code to the wrong glyph (and the wrong ToUnicode string).
type cmapPDF struct {
	base       *cmapPDF // usecmap target, consulted when this map has no entry
	codespaces []codespaceRange
	byLen      [4][]codespaceRange // index n holds the merged ranges of (n+1)-byte codes
	cids       [4][]cidRangeEntry  // index n holds the ranges mapping (n+1)-byte codes
	bf         [4][]bfEntry        // index n holds the entries mapping (n+1)-byte codes
	nCIDs      int                 // total cid entries accepted, across the buckets, against maxCMapRanges
	nBF        int                 // total bf entries accepted, across the buckets, against maxCMapRanges
	wmode      uint8
	hasWMode   bool
	identity   bool // Identity mapping: CID = code (Identity-H/V)
}

// indexCodespaces builds byLen from codespaces: bucketed by code length, sorted by starting code, and with overlapping
// or adjacent ranges merged. Merging preserves the covered set exactly, which is all the membership test asks; the
// longest-prefix fallback in nextCode still walks codespaces, where each entry's own byte-position ranges matter.
func (cm *cmapPDF) indexCodespaces() {
	for i := range cm.byLen {
		cm.byLen[i] = nil
	}
	for _, cs := range cm.codespaces {
		if cs.nBytes >= 1 && cs.nBytes <= 4 {
			cm.byLen[cs.nBytes-1] = append(cm.byLen[cs.nBytes-1], cs)
		}
	}
	for i := range cm.byLen {
		ranges := cm.byLen[i]
		if len(ranges) < 2 {
			continue
		}
		slices.SortFunc(ranges, func(a, b codespaceRange) int { return cmp.Compare(a.lo, b.lo) })
		merged := ranges[:1]
		for _, cs := range ranges[1:] {
			last := &merged[len(merged)-1]
			// last.hi+1 would wrap for a range ending at 2^32-1, which can only be the last range of a 4-byte bucket.
			if cs.lo <= last.hi || (last.hi != math.MaxUint32 && cs.lo == last.hi+1) {
				last.hi = max(last.hi, cs.hi)
				continue
			}
			merged = append(merged, cs)
		}
		cm.byLen[i] = merged
	}
}

// predefinedCMap returns the built-in CMaps: Identity-H and Identity-V (ISO 32000-2 9.7.5.2). Every other predefined
// name returns nil (bundling the Adobe cmap-resources corpus is deferred until real files need it).
func predefinedCMap(name cos.Name) *cmapPDF {
	var cm *cmapPDF
	switch name {
	case "Identity-H":
		cm = &cmapPDF{identity: true, codespaces: []codespaceRange{{lo: 0, hi: 0xFFFF, nBytes: 2}}}
	case "Identity-V":
		cm = &cmapPDF{
			identity:   true,
			codespaces: []codespaceRange{{lo: 0, hi: 0xFFFF, nBytes: 2}},
			wmode:      1,
			hasWMode:   true,
		}
	default:
		return nil
	}
	cm.indexCodespaces() // These are built rather than parsed, so nothing else populates the membership index.
	return cm
}

// parseCMap parses CMap content. resolveUse maps a usecmap name to its CMap (predefined or, for embedded /UseCMap
// streams, loaded by the caller); depth caps usecmap chains.
func parseCMap(data []byte, depth int, resolveUse func(cos.Name) *cmapPDF) *cmapPDF {
	if depth > maxCMapDepth {
		return nil
	}
	cm := &cmapPDF{}
	lex := cos.NewLexer(data, 0)
	var operands []cos.Token
	budget := maxCMapOps
	push := func(tok cos.Token) {
		if len(operands) >= maxCMapOperand {
			copy(operands, operands[1:])
			operands = operands[:len(operands)-1]
		}
		operands = append(operands, tok)
	}
	for budget > 0 {
		budget--
		tok, ok := lex.Next()
		if !ok {
			continue
		}
		if tok.Kind == cos.TokenEOF {
			break
		}
		if tok.Kind != cos.TokenKeyword {
			// Bytes may alias the lexer's input, which is stable here (data is fully materialized), so tokens can be
			// retained without copying.
			push(tok)
			continue
		}
		switch string(tok.Bytes) {
		case "begincodespacerange":
			cm.parseCodespaces(lex, &budget)
		case "begincidrange":
			cm.parseCIDRanges(lex, &budget, false)
		case "begincidchar":
			cm.parseCIDRanges(lex, &budget, true)
		case "beginbfrange":
			cm.parseBFRanges(lex, &budget, false)
		case "beginbfchar":
			cm.parseBFRanges(lex, &budget, true)
		case "usecmap":
			if len(operands) > 0 && operands[len(operands)-1].Kind == cos.TokenName && resolveUse != nil {
				if base := resolveUse(cos.Name(operands[len(operands)-1].Bytes)); base != nil {
					cm.base = base
				}
			}
		case "def":
			// /WMode <n> def
			if len(operands) >= 2 && operands[len(operands)-2].Kind == cos.TokenName &&
				string(operands[len(operands)-2].Bytes) == "WMode" && operands[len(operands)-1].Kind == cos.TokenInt {
				cm.wmode = uint8(operands[len(operands)-1].Int & 1)
				cm.hasWMode = true
			}
		}
		operands = operands[:0]
	}
	cm.sortRanges()
	return cm
}

// sortRanges leaves the code→CID and bf lists sorted by starting code and non-overlapping, so cid and bfRune can
// binary search them. Both run once per glyph shown (Font.Width and Font.GID consult cid, Font.Unicode consults
// bfRune) and each list may hold maxCMapRanges entries, so a linear scan costs O(glyphs × ranges) on a text-heavy page
// with a large embedded CMap or /ToUnicode. The /W and /W2 lists get the same treatment (see disjointCIDRanges).
//
// Overlap is malformed: ISO 32000-2 9.7.5.3 maps a code through one entry. It resolves as it does for /W: the
// contested span goes to the entry with the lower starting code, the earlier entry in the CMap breaking a tie. Entries
// of different code lengths address different codes, so each bucket is made disjoint alone.
func (cm *cmapPDF) sortRanges() {
	cm.indexCodespaces()
	for n := range cm.cids {
		cm.cids[n] = disjointCIDRanges(cm.cids[n], func(r *cidRangeEntry) (uint32, uint32) { return r.lo, r.hi },
			func(r *cidRangeEntry, lo uint32) {
				r.cid += lo - r.lo
				r.lo = lo
			})
	}
	for n := range cm.bf {
		cm.bf[n] = disjointCIDRanges(cm.bf[n], func(e *bfEntry) (uint32, uint32) { return e.lo, e.hi },
			func(e *bfEntry, lo uint32) {
				off := lo - e.lo
				if e.dstArray != nil {
					e.dstArray = e.dstArray[off:] // hi is bounded by the array's length, so the clipped codes are its front.
				} else {
					e.trimmed += uint16(off)
				}
				e.lo = lo
			})
	}
}

// hasBF reports whether any bf entry survived parsing (of any code length).
func (cm *cmapPDF) hasBF() bool {
	for _, entries := range cm.bf {
		if len(entries) > 0 {
			return true
		}
	}
	return false
}

// codeToken converts a hex-string token to (value, byte length); code strings longer than 4 bytes are invalid.
func codeToken(tok cos.Token) (val uint32, n uint8, ok bool) {
	if tok.Kind != cos.TokenString || len(tok.Bytes) == 0 || len(tok.Bytes) > 4 {
		return 0, 0, false
	}
	for _, b := range tok.Bytes {
		val = val<<8 | uint32(b)
	}
	return val, uint8(len(tok.Bytes)), true
}

// parseCodespaces reads <lo> <hi> pairs until endcodespacerange.
func (cm *cmapPDF) parseCodespaces(lex *cos.Lexer, budget *int) {
	var pending []cos.Token
	for *budget > 0 {
		*budget--
		tok, ok := lex.Next()
		if !ok {
			continue
		}
		if tok.Kind == cos.TokenEOF || (tok.Kind == cos.TokenKeyword && string(tok.Bytes) == "endcodespacerange") {
			return
		}
		if tok.Kind != cos.TokenString {
			continue
		}
		pending = append(pending, tok)
		if len(pending) == 2 {
			lo, nLo, okLo := codeToken(pending[0])
			hi, nHi, okHi := codeToken(pending[1])
			if okLo && okHi && nLo == nHi && lo <= hi && len(cm.codespaces) < maxCodespaces {
				cm.codespaces = append(cm.codespaces, codespaceRange{lo: lo, hi: hi, nBytes: nLo})
			}
			pending = pending[:0]
		}
	}
}

// parseCIDRanges reads <lo> <hi> cid triples (or <code> cid pairs when char is set) until the end operator.
func (cm *cmapPDF) parseCIDRanges(lex *cos.Lexer, budget *int, char bool) {
	var pending []cos.Token
	need := 3
	if char {
		need = 2
	}
	for *budget > 0 {
		*budget--
		tok, ok := lex.Next()
		if !ok {
			continue
		}
		if tok.Kind == cos.TokenEOF {
			return
		}
		if tok.Kind == cos.TokenKeyword {
			word := string(tok.Bytes)
			if word == "endcidrange" || word == "endcidchar" {
				return
			}
			pending = pending[:0]
			continue
		}
		if tok.Kind != cos.TokenString && tok.Kind != cos.TokenInt {
			continue
		}
		pending = append(pending, tok)
		if len(pending) < need {
			continue
		}
		last := pending[need-1]
		// CIDs are 16-bit (ISO 32000-2 9.7.4), so an entry naming a larger starting CID is malformed and is dropped
		// rather than narrowed — the same guard the /W and /W2 parsers apply. Without the upper bound the uint32
		// conversion below wraps a value at or above 2^32 into an unrelated CID, silently selecting an arbitrary glyph.
		if last.Kind == cos.TokenInt && last.Int >= 0 && last.Int <= maxCID && cm.nCIDs < maxCMapRanges {
			lo, nLo, okLo := codeToken(pending[0])
			hi, nHi, okHi := lo, nLo, okLo
			if !char {
				hi, nHi, okHi = codeToken(pending[1])
			}
			// The entry lands in the bucket of its own code length (codeToken guarantees 1-4): a cidrange applies only
			// to codes of the length it was written with (ISO 32000-2 9.7.6.2).
			if okLo && okHi && nLo == nHi && lo <= hi {
				cm.cids[nLo-1] = append(cm.cids[nLo-1], cidRangeEntry{lo: lo, hi: hi, cid: uint32(last.Int)})
				cm.nCIDs++
			}
		}
		pending = pending[:0]
	}
}

// parseBFRanges reads bfrange triples (<lo> <hi> <dst>, or <lo> <hi> [<dst>...]) or bfchar pairs.
func (cm *cmapPDF) parseBFRanges(lex *cos.Lexer, budget *int, char bool) {
	var pending []cos.Token
	var arrayDst [][]byte
	inArray := false
	need := 3
	if char {
		need = 2
	}
	flush := func() {
		defer func() { pending = pending[:0]; arrayDst = nil }()
		if len(pending) < need-1 || cm.nBF >= maxCMapRanges {
			return
		}
		lo, nLo, okLo := codeToken(pending[0])
		hi, nHi, okHi := lo, nLo, okLo
		if !char {
			hi, nHi, okHi = codeToken(pending[1])
		}
		if !okLo || !okHi || nLo != nHi || lo > hi || hi-lo >= maxCMapRanges {
			return
		}
		e := bfEntry{lo: lo, hi: hi}
		switch {
		case arrayDst != nil:
			// The array supplies one target per code, so its length — not hi — bounds what the entry can map. Clamping
			// here keeps the codes past the array's end available to a later overlapping entry, and lets bfRune index
			// the array without a bounds check.
			if len(arrayDst) == 0 {
				return
			}
			if uint32(len(arrayDst))-1 < hi-lo {
				e.hi = lo + uint32(len(arrayDst)) - 1
			}
			e.dstArray = arrayDst
		case len(pending) == need && pending[need-1].Kind == cos.TokenString:
			e.dst = append([]byte(nil), pending[need-1].Bytes...)
		default:
			return
		}
		cm.bf[nLo-1] = append(cm.bf[nLo-1], e) // Bucketed by the length of the codes the entry maps, like the cid ranges.
		cm.nBF++
	}
	for *budget > 0 {
		*budget--
		tok, ok := lex.Next()
		if !ok {
			continue
		}
		switch tok.Kind {
		case cos.TokenEOF:
			return
		case cos.TokenKeyword:
			word := string(tok.Bytes)
			if word == "endbfrange" || word == "endbfchar" {
				flush()
				return
			}
			pending = pending[:0]
		case cos.TokenArrayOpen:
			inArray = true
			arrayDst = [][]byte{}
		case cos.TokenArrayClose:
			inArray = false
			flush()
		case cos.TokenString:
			if inArray {
				if len(arrayDst) < maxCMapRanges {
					arrayDst = append(arrayDst, append([]byte(nil), tok.Bytes...))
				}
				continue
			}
			pending = append(pending, tok)
			if len(pending) == need {
				flush()
			}
		default:
		}
	}
}

// nextCode decodes the next character code from b (ISO 32000-2 9.7.6.3): the codespace ranges determine how many bytes
// one code spans. Codes outside every codespace consume bytes per the partial-match rule: the length of the codespace
// whose leading bytes match the longest prefix of the input (each byte within that byte position's range), ties broken
// by the shortest codespace, defaulting to one byte.
func (cm *cmapPDF) nextCode(b []byte) (code uint32, n int) {
	for length := 1; length <= 4 && length <= len(b); length++ {
		var v uint32
		for _, by := range b[:length] {
			v = v<<8 | uint32(by)
		}
		if cm.inCodespace(v, uint8(length)) {
			return v, length
		}
	}
	// Invalid code: consume per the codespace matching the longest input prefix, mapping to CID 0. Prefix length is how
	// many leading bytes lie within the codespace's per-position byte ranges; the winner's full length is consumed.
	n = 1
	bestPrefix := 0
	bestLen := 8
	for c := cm; c != nil; c = c.base {
		for _, cs := range c.codespaces {
			nb := int(cs.nBytes)
			prefix := 0
			for i := 0; i < nb && i < len(b); i++ {
				shift := (nb - 1 - i) * 8
				if uint32(b[i]) < (cs.lo>>shift)&0xFF || uint32(b[i]) > (cs.hi>>shift)&0xFF {
					break
				}
				prefix++
			}
			if prefix > 0 && (prefix > bestPrefix || (prefix == bestPrefix && nb < bestLen)) {
				bestPrefix = prefix
				bestLen = nb
			}
		}
	}
	if bestPrefix > 0 && bestLen <= 4 {
		n = min(bestLen, len(b))
	}
	return 0, n
}

// inCodespace reports whether an nBytes-length code value lies in any codespace (own or base). It binary searches the
// merged per-length index rather than walking the declaration order: nextCode calls this up to four times for every
// character code a show operator decodes.
func (cm *cmapPDF) inCodespace(v uint32, nBytes uint8) bool {
	if nBytes < 1 || nBytes > 4 {
		return false
	}
	for c := cm; c != nil; c = c.base {
		ranges := c.byLen[nBytes-1]
		i, exact := slices.BinarySearchFunc(ranges, v, func(cs codespaceRange, v uint32) int {
			return cmp.Compare(cs.lo, v)
		})
		// exact means a range starts at v; otherwise the only range that can hold v is the one before the insertion
		// point, since the merged ranges are sorted and disjoint.
		if exact || (i > 0 && v <= ranges[i-1].hi) {
			return true
		}
	}
	return false
}

// lengthOrder returns the bucket indexes a code decoded at nBytes bytes consults, in order: its own length first, then
// the remaining lengths shortest-first (nBytes outside 1-4, an unknown length, consults them all shortest-first).
//
// The trailing lengths are the lenient half of the rule ISO 32000-2 9.7.6.2 states strictly. An entry written with the
// code's own length always wins, as the specification requires, but a code no entry of its length maps still falls
// back to another length rather than resolving to .notdef (or to no Unicode at all): producers routinely write a simple
// font's /ToUnicode with 2-byte codes, or a 2-byte CMap's begincidchar with 1-byte ones, and every deployed viewer,
// keying its tables on the value alone, maps those.
func lengthOrder(nBytes uint8) [4]int {
	var out [4]int
	n := 0
	if nBytes >= 1 && nBytes <= 4 {
		out[0] = int(nBytes) - 1
		n = 1
	}
	for i := range 4 {
		if n > 0 && i == out[0] {
			continue
		}
		out[n] = i
		n++
	}
	return out
}

// cid maps a code decoded at nBytes bytes to a CID (0 when unmapped).
func (cm *cmapPDF) cid(code uint32, nBytes uint8) uint32 {
	order := lengthOrder(nBytes)
	for c := cm; c != nil; c = c.base {
		if c.identity {
			return code & 0xFFFF
		}
		for _, n := range order {
			ranges := c.cids[n]
			if i := findCIDRange(ranges, func(r *cidRangeEntry) uint32 { return r.lo }, code); i >= 0 {
				if r := &ranges[i]; code <= r.hi {
					return r.cid + (code - r.lo)
				}
			}
		}
	}
	return 0
}

// bfRune maps a code decoded at nBytes bytes to the first rune of its bf target (ToUnicode), reporting false when the
// code maps nowhere.
//
// Only the leading rune is decoded, never the whole target: this is the per-glyph lookup, while parseBFRanges puts no
// cap on a target's length (a hex string token decodes up to ~512 KB before the lexer's maxHexStringScan stops it), so
// decoding the whole target on every call would allocate proportional to it and turn extraction over a text-heavy page
// into gigabytes of churn for one rune. Codes whose target carries more than one rune (ligatures, and the one-to-many
// mappings of ISO 32000-2 9.10.3 generally) report multi, and the caller asks bfRunesAfterFirst for the rest.
func (cm *cmapPDF) bfRune(code uint32, nBytes uint8) (r rune, multi, ok bool) {
	dst, inc, found := cm.bfTarget(code, nBytes)
	if !found {
		return 0, false, false
	}
	r, ok = utf16BEFirstRune(dst, inc)
	// A target is one-to-many when code units remain past the leading rune's own: one unit, or the two a surrogate
	// pair spans. Odd trailing bytes are the dropped half-unit utf16BEFirstRune ignores, so they are not a rune.
	return r, ok && len(dst)/2 > utf16RuneUnits(dst), ok
}

// bfRunesAfterFirst returns the runes of a code's bf target past its leading one, decoded UTF-16BE with surrogate pairs
// combined: the "filler" characters MuPDF's pdf_show_char emits for a one-to-many mapping. It is called only for the
// codes bfRune reported multi for, so the whole-target decode costs nothing on the ordinary one-rune path.
//
// The result is capped at maxBFRunes runes, as MuPDF caps a mapping at PDF_MRANGE_CAP code units: without it a single
// glyph in a hostile file expands into a quarter-million recorded characters.
func (cm *cmapPDF) bfRunesAfterFirst(code uint32, nBytes uint8) []rune {
	dst, inc, ok := cm.bfTarget(code, nBytes)
	if !ok {
		return nil
	}
	// The cap is on code units, not on runes, and the increment lands on the last unit that survives it — both as
	// MuPDF's bfrange parser does, which truncates the target string before incrementing its final unit.
	units := min(len(dst)/2, maxBFRunes)
	unit := func(i int) uint16 {
		u := uint16(dst[i*2])<<8 | uint16(dst[i*2+1])
		if i == units-1 {
			u += inc
		}
		return u
	}
	out := make([]rune, 0, units)
	for i := utf16RuneUnits(dst); i < units; i++ {
		first := unit(i)
		switch {
		case first >= 0xD800 && first < 0xDC00 && i+1 < units:
			if second := unit(i + 1); second >= 0xDC00 && second < 0xE000 {
				out = append(out, utf16.DecodeRune(rune(first), rune(second)))
				i++
				continue
			}
			out = append(out, utf8.RuneError) // A high surrogate the next unit does not complete.
		case first >= 0xD800 && first < 0xE000:
			out = append(out, utf8.RuneError) // A lone low surrogate, as utf16.Decode renders it.
		default:
			out = append(out, rune(first))
		}
	}
	return out
}

// maxBFRunes caps how many runes one code's /ToUnicode target contributes, mirroring MuPDF's PDF_MRANGE_CAP: its
// pdf_show_char decodes a mapping into an int[PDF_MRANGE_CAP] buffer and shows nothing past it.
const maxBFRunes = 256

// utf16RuneUnits returns how many UTF-16BE code units of b the leading rune spans: 2 for a well-formed surrogate pair,
// otherwise 1 (0 for a target too short to hold a unit, which utf16BEFirstRune reports as no mapping at all).
func utf16RuneUnits(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	if first := uint16(b[0])<<8 | uint16(b[1]); first >= 0xD800 && first < 0xDC00 && len(b) >= 4 {
		if second := uint16(b[2])<<8 | uint16(b[3]); second >= 0xDC00 && second < 0xE000 {
			return 2
		}
	}
	return 1
}

// bfTarget finds the bf target string a code maps to, along with the increment its position within a contiguous entry
// adds to the target's final code unit (the bfrange rule; 0 for the explicit per-code array form).
func (cm *cmapPDF) bfTarget(code uint32, nBytes uint8) (dst []byte, inc uint16, ok bool) {
	order := lengthOrder(nBytes)
	for c := cm; c != nil; c = c.base {
		for _, n := range order {
			entries := c.bf[n]
			i := findCIDRange(entries, func(e *bfEntry) uint32 { return e.lo }, code)
			if i < 0 {
				continue
			}
			e := &entries[i]
			if code > e.hi {
				continue
			}
			idx := code - e.lo
			if e.dstArray != nil {
				return e.dstArray[idx], 0, true
			}
			return e.dst, uint16(idx) + e.trimmed, true
		}
	}
	return nil, 0, false
}

// utf16BEFirstRune decodes the first rune of UTF-16BE bytes, adding inc to the final code unit (the bfrange rule "the
// last byte of the string shall be incremented", which for UTF-16 targets is the final code unit). Odd lengths drop the
// trailing byte, matching lenient viewers, and an empty target reports false so the caller falls through to its other
// Unicode sources, as for an absent entry.
//
// The increment reaches the leading rune only when the target is one code unit long, or two of which the first is a
// high surrogate; longer targets increment a unit the leading rune does not span. Lone or mispaired surrogates decode
// to U+FFFD, as utf16.Decode yields for the same input.
func utf16BEFirstRune(b []byte, inc uint16) (rune, bool) {
	if len(b) < 2 {
		if len(b) == 1 { // A single byte: treat as one 8-bit unit (some producers write <41>).
			return rune(uint16(b[0]) + inc), true
		}
		return 0, false
	}
	units := len(b) / 2
	first := uint16(b[0])<<8 | uint16(b[1])
	if units == 1 {
		first += inc
	}
	switch {
	case first >= 0xD800 && first < 0xDC00: // High surrogate: pairs with the next unit, if there is one.
		if units < 2 {
			return utf8.RuneError, true
		}
		second := uint16(b[2])<<8 | uint16(b[3])
		if units == 2 {
			second += inc
		}
		return utf16.DecodeRune(rune(first), rune(second)), true // U+FFFD when second is not a low surrogate.
	case first >= 0xDC00 && first < 0xE000: // Low surrogate with no high one before it.
		return utf8.RuneError, true
	default:
		return rune(first), true
	}
}

// wModeResolved returns the CMap's writing mode, consulting the usecmap chain.
func (cm *cmapPDF) wModeResolved() uint8 {
	for c := cm; c != nil; c = c.base {
		if c.hasWMode {
			return c.wmode
		}
	}
	return 0
}
