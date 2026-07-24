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
	"unicode/utf16"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// PDF CMaps (ISO 32000-2 9.7.5, 9.10.3): the code→CID maps of Type0 font /Encoding entries and — through the bf
// operators — ToUnicode maps. CMap content is lexically PDF surface syntax, so the exported cos.Lexer tokenizes it
// (exactly as content streams do); the operators consulted are begincodespacerange/endcodespace- range,
// begincidrange/begincidchar, beginbfrange/beginbfchar, usecmap, and /WMode. Everything else (the CIDSystemInfo
// boilerplate, dict/proc syntax) is skipped by the same sliding-operand-window convention the content interpreter uses.

// CMap resource caps.
const (
	maxCMapRanges  = 65536
	maxCMapDepth   = 4       // usecmap chains
	maxCMapOps     = 1 << 20 // token budget per CMap stream
	maxCMapOperand = 64      // sliding operand window
)

// codespaceRange is one codespace entry: codes of nBytes length whose value lies in [lo, hi].
type codespaceRange struct {
	lo, hi uint32
	nBytes uint8
}

// cidRangeEntry maps the code range [lo, hi] (of nBytes-length codes) to CIDs starting at cid.
type cidRangeEntry struct {
	lo, hi, cid uint32
	nBytes      uint8
}

// bfEntry maps the code range [lo, hi] to target strings: dst for a contiguous mapping (the last UTF-16 code unit
// increments across the range), dstArray for an explicit per-code list. trimmed carries the increment the codes clipped
// off a contiguous entry's front by sortRanges already consumed, so dst stays the string the CMap wrote for lo.
type bfEntry struct {
	dst      []byte
	dstArray [][]byte
	lo, hi   uint32
	trimmed  uint16
	nBytes   uint8
}

// cmapPDF is one parsed CMap.
type cmapPDF struct {
	base       *cmapPDF // usecmap target, consulted when this map has no entry
	codespaces []codespaceRange
	cids       []cidRangeEntry
	bf         []bfEntry
	wmode      uint8
	hasWMode   bool
	identity   bool // Identity mapping: CID = code (Identity-H/V)
}

// predefinedCMap returns the built-in CMaps: Identity-H and Identity-V (ISO 32000-2 9.7.5.2). Every other predefined
// name returns nil (bundling the Adobe cmap-resources corpus is deferred until real files need it).
func predefinedCMap(name cos.Name) *cmapPDF {
	switch name {
	case "Identity-H":
		return &cmapPDF{identity: true, codespaces: []codespaceRange{{lo: 0, hi: 0xFFFF, nBytes: 2}}}
	case "Identity-V":
		return &cmapPDF{
			identity:   true,
			codespaces: []codespaceRange{{lo: 0, hi: 0xFFFF, nBytes: 2}},
			wmode:      1,
			hasWMode:   true,
		}
	default:
		return nil
	}
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

// sortRanges leaves the code→CID and bf lists sorted by starting code and non-overlapping, so cid and bfString can
// binary search them instead of walking from the start. Both run once per glyph shown — Font.Width and Font.GID consult
// cid, Font.Unicode consults bfString — and parsing accepts up to maxCMapRanges entries in each list, so a linear scan
// costs O(glyphs × ranges) on a text-heavy page using a large embedded CMap or /ToUnicode. The /W and /W2 lists already
// get exactly this treatment for the same reason (see disjointCIDRanges).
//
// Overlap is malformed: ISO 32000-2 9.7.5.3 maps a code through one entry. It resolves as it does for /W — the
// contested span goes to the entry with the lower starting code, the earlier entry in the CMap breaking a tie — which
// matches what the former linear scan returned for every case except a later entry that also starts lower.
func (cm *cmapPDF) sortRanges() {
	cm.cids = disjointCIDRanges(cm.cids, func(r *cidRangeEntry) (uint32, uint32) { return r.lo, r.hi },
		func(r *cidRangeEntry, lo uint32) {
			r.cid += lo - r.lo
			r.lo = lo
		})
	cm.bf = disjointCIDRanges(cm.bf, func(e *bfEntry) (uint32, uint32) { return e.lo, e.hi },
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
			if okLo && okHi && nLo == nHi && lo <= hi && len(cm.codespaces) < maxCMapRanges {
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
		if last.Kind == cos.TokenInt && last.Int >= 0 && len(cm.cids) < maxCMapRanges {
			lo, nLo, okLo := codeToken(pending[0])
			hi, nHi, okHi := lo, nLo, okLo
			if !char {
				hi, nHi, okHi = codeToken(pending[1])
			}
			if okLo && okHi && nLo == nHi && lo <= hi {
				cm.cids = append(cm.cids, cidRangeEntry{lo: lo, hi: hi, cid: uint32(last.Int), nBytes: nLo})
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
		if len(pending) < need-1 || len(cm.bf) >= maxCMapRanges {
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
		e := bfEntry{lo: lo, hi: hi, nBytes: nLo}
		switch {
		case arrayDst != nil:
			// The array supplies one target per code, so its length — not hi — bounds what the entry can map. Clamping
			// here keeps the codes past the array's end available to a later overlapping entry, which is what the former
			// linear scan gave them by falling through, and lets bfString index the array without a bounds check.
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
		cm.bf = append(cm.bf, e)
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
// one code spans. Codes outside every codespace consume bytes per the partial-match rule — the length of the codespace
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

// inCodespace reports whether an nBytes-length code value lies in any codespace (own or base).
func (cm *cmapPDF) inCodespace(v uint32, nBytes uint8) bool {
	for c := cm; c != nil; c = c.base {
		for _, cs := range c.codespaces {
			if cs.nBytes == nBytes && v >= cs.lo && v <= cs.hi {
				return true
			}
		}
	}
	return false
}

// cid maps a decoded code to a CID (0 when unmapped).
func (cm *cmapPDF) cid(code uint32) uint32 {
	for c := cm; c != nil; c = c.base {
		if c.identity {
			return code & 0xFFFF
		}
		if i := findCIDRange(c.cids, func(r *cidRangeEntry) uint32 { return r.lo }, code); i >= 0 {
			if r := &c.cids[i]; code <= r.hi {
				return r.cid + (code - r.lo)
			}
		}
	}
	return 0
}

// bfString maps a code to its bf target string (ToUnicode), decoding UTF-16BE; "" when unmapped.
func (cm *cmapPDF) bfString(code uint32) string {
	for c := cm; c != nil; c = c.base {
		i := findCIDRange(c.bf, func(e *bfEntry) uint32 { return e.lo }, code)
		if i < 0 {
			continue
		}
		e := &c.bf[i]
		if code > e.hi {
			continue
		}
		idx := code - e.lo
		if e.dstArray != nil {
			return utf16BEToString(e.dstArray[idx], 0)
		}
		return utf16BEToString(e.dst, uint16(idx)+e.trimmed)
	}
	return ""
}

// utf16BEToString decodes UTF-16BE bytes, adding inc to the final code unit (the bfrange increment rule: "the last byte
// of the string shall be incremented", which for UTF-16 targets is the final code unit). Odd-length input drops the
// trailing byte, matching lenient viewers.
func utf16BEToString(b []byte, inc uint16) string {
	if len(b) < 2 {
		if len(b) == 1 { // A single byte: treat as one 8-bit unit (some producers write <41>).
			return string(rune(uint16(b[0]) + inc))
		}
		return ""
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
	}
	units[len(units)-1] += inc
	return string(utf16.Decode(units))
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
