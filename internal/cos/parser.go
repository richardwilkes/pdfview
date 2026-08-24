// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos

import (
	"bytes"
	"errors"
	"fmt"
)

// maxNestingDepth caps how deeply arrays and dictionaries may nest, guarding against stack exhaustion from hostile
// input.
const maxNestingDepth = 512

// maxObjectNumber bounds accepted object numbers. ISO 32000-2 Annex C suggests 8388607 as an implementation limit; this
// is slightly more generous while still bounding lookup structures.
const maxObjectNumber = 1 << 24

// maxGenerationNumber is the largest generation ISO 32000-2 7.3.10 defines. A "N G R" lookahead whose G exceeds it is
// still taken as a reference (leniency: the generation takes no part in resolution, so rejecting the reference would
// lose the object over a field nothing reads), but the recorded generation is clamped to 0 — the same treatment
// parseIndirectAtBounded gives a nonsensical header generation.
const maxGenerationNumber = 0xffff

// maxContainerElements caps how many array elements and dictionary entries one parsed object may hold across its whole
// tree. Nesting depth does not bound size, and an object's payload is not bounded by the file: an object stream decodes
// through internal/filter's max(64 MB, 256x input) allowance, so a small file can hold an array of tens of millions of
// elements. The result lives in Document.objCache, which has no size budget and is dropped only by clearCaches, so the
// memory stays live for the whole Document. The cap is orders of magnitude above the largest real containers (a CJK
// CIDFont's /W array, a flat page tree's /Kids).
const maxContainerElements = 1 << 20

var (
	errTooDeep          = errors.New("objects nested too deeply")
	errTooLarge         = errors.New("object has too many elements")
	errUnexpectedEOF    = errors.New("unexpected end of input")
	errBadDictKey       = errors.New("dictionary key is not a name")
	errUnexpectedToken  = errors.New("unexpected token")
	errNotIndirect      = errors.New("not an indirect object header")
	errWrongObject      = errors.New("indirect object header has the wrong object number")
	errNoEndstream      = errors.New("missing endstream keyword")
	errStreamOutOfRange = errors.New("stream extends past end of input")
)

// parser builds objects from a token stream, with a small pushback stack for the lookahead that indirect references ("N
// G R") require.
type parser struct {
	stack []token
	lex   lexer
	depth int
	// elems is the remaining element allowance (see maxContainerElements), shared by every container this parser
	// builds so nesting cannot multiply the total. Each parser instance covers one object, so the budget is per
	// object.
	elems int
}

func newParser(data []byte, pos int) *parser {
	return &parser{lex: lexer{data: data, pos: pos}, elems: maxContainerElements}
}

func (p *parser) next() (token, error) {
	if n := len(p.stack); n > 0 {
		t := p.stack[n-1]
		p.stack = p.stack[:n-1]
		return t, nil
	}
	return p.lex.next()
}

func (p *parser) push(t token) {
	p.stack = append(p.stack, t)
}

// resumePos returns the offset of the first byte this parser has not consumed: the start of the next pushed-back token
// when lookahead was returned to the stack, and the raw lexer position otherwise. The repair sweep restarts past a
// failed attempt from it instead of re-lexing the same span, which keeps the sweep linear on hostile input.
func (p *parser) resumePos() int {
	if n := len(p.stack); n > 0 {
		return p.stack[n-1].pos
	}
	return p.lex.pos
}

// parseObject parses one object.
func (p *parser) parseObject() (Object, error) {
	tok, err := p.next()
	if err != nil {
		return nil, err
	}
	return p.parseObjectFrom(tok)
}

// parseObjectFrom parses one object whose first token has already been read.
func (p *parser) parseObjectFrom(tok token) (Object, error) {
	switch tok.kind {
	case tkInt:
		return p.parseIntOrRef(tok)
	case tkReal:
		return Real(tok.f), nil
	case tkString:
		return String(tok.s), nil
	case tkName:
		return Name(tok.s), nil
	case tkArrayOpen:
		return p.parseArray()
	case tkDictOpen:
		return p.parseDict()
	case tkKeyword:
		switch {
		case bytes.Equal(tok.s, []byte("true")):
			return Boolean(true), nil
		case bytes.Equal(tok.s, []byte("false")):
			return Boolean(false), nil
		case bytes.Equal(tok.s, []byte("null")):
			return Null{}, nil
		default:
			return nil, fmt.Errorf("%w: keyword %q", errUnexpectedToken, tok.s)
		}
	case tkEOF:
		return nil, errUnexpectedEOF
	default:
		return nil, fmt.Errorf("%w: kind %d", errUnexpectedToken, tok.kind)
	}
}

// parseIntOrRef disambiguates an integer from an indirect reference by looking ahead for "G R".
func (p *parser) parseIntOrRef(tok token) (Object, error) {
	if tok.i < 0 || tok.i > maxObjectNumber {
		return Integer(tok.i), nil
	}
	second, err := p.next()
	if err != nil {
		return nil, err
	}
	if second.kind != tkInt || second.i < 0 {
		p.push(second)
		return Integer(tok.i), nil
	}
	third, err := p.next()
	if err != nil {
		return nil, err
	}
	if third.kind == tkKeyword && bytes.Equal(third.s, []byte("R")) {
		gen := second.i
		if gen > maxGenerationNumber {
			gen = 0
		}
		return Ref{Num: int(tok.i), Gen: int(gen)}, nil
	}
	p.push(third)
	p.push(second)
	return Integer(tok.i), nil
}

func (p *parser) parseArray() (Object, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxNestingDepth {
		return nil, errTooDeep
	}
	arr := Array{}
	for {
		tok, err := p.next()
		if err != nil {
			return nil, err
		}
		if tok.kind == tkArrayClose {
			return arr, nil
		}
		if tok.kind == tkEOF {
			return nil, errUnexpectedEOF
		}
		obj, err := p.parseObjectFrom(tok)
		if err != nil {
			return nil, err
		}
		p.elems--
		if p.elems < 0 {
			return nil, errTooLarge
		}
		arr = append(arr, obj)
	}
}

func (p *parser) parseDict() (Object, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxNestingDepth {
		return nil, errTooDeep
	}
	dict := Dict{}
	for {
		tok, err := p.next()
		if err != nil {
			return nil, err
		}
		switch tok.kind {
		case tkDictClose:
			return dict, nil
		case tkName:
			value, verr := p.parseObject()
			if verr != nil {
				return nil, verr
			}
			p.elems--
			if p.elems < 0 {
				return nil, errTooLarge
			}
			// Duplicate keys are undefined behavior per the spec; the last occurrence wins here.
			dict[Name(tok.s)] = value
		case tkEOF:
			return nil, errUnexpectedEOF
		default:
			return nil, errBadDictKey
		}
	}
}

// expectKeyword consumes the next token and verifies it is the given keyword.
func (p *parser) expectKeyword(word string) error {
	tok, err := p.next()
	if err != nil {
		return err
	}
	if tok.kind != tkKeyword || !bytes.Equal(tok.s, []byte(word)) {
		return fmt.Errorf("%w: expected %q", errUnexpectedToken, word)
	}
	return nil
}

// expectInt consumes the next token and returns its integer value.
func (p *parser) expectInt() (int64, error) {
	tok, err := p.next()
	if err != nil {
		return 0, err
	}
	if tok.kind != tkInt {
		return 0, fmt.Errorf("%w: expected integer", errUnexpectedToken)
	}
	return tok.i, nil
}

// parseIndirectAt parses the indirect object "num gen obj ... [stream ... endstream]" at offset off within data. When
// wantNum is non-negative the header's object number must match it, detecting stale xref offsets. It returns the
// object, its generation number, and the offset just past it (past endstream for streams). An indirect /Length is not
// resolved; see Document.parseIndirectObjectAt for the path that does.
func parseIndirectAt(data []byte, off int64, wantNum int) (obj Object, gen int, end int64, err error) {
	return parseIndirectAtBounded(data, off, wantNum, len(data), nil)
}

// lengthResolver returns the value of an indirect /Length, or ok == false when it cannot be established cheaply and
// safely. captureRawStream falls back to its "endstream" scan for a false.
type lengthResolver func(Ref) (length int64, ok bool)

// parseIndirectObjectAt parses the indirect object at off, resolving an indirect /Length against this document's
// cross-reference data. Every stream reached through the cross-reference table comes through here rather than
// parseIndirectAt so that a single-pass writer's "/Length 3 0 R" is honored instead of being second-guessed by the
// fallback scan, which truncates any payload that happens to contain the bytes "endstream".
func (d *Document) parseIndirectObjectAt(off int64, wantNum int) (obj Object, gen int, err error) {
	obj, gen, _, err = parseIndirectAtBounded(d.data, off, wantNum, len(d.data), d.resolveStreamLength)
	return obj, gen, err
}

// resolveStreamLength returns the value of a stream's indirect /Length. It reads the referenced object directly — the
// header at its cross-reference offset followed by one integer — rather than through loadObject: this runs mid-parse,
// where the caches and the repair scan loadObject drives would either recurse (a /Length reference to the very stream
// being parsed) or replace state the in-flight parse stands on. Reading one integer at a known offset costs a handful
// of tokens, so no hostile file can turn a page's worth of stream parses into repeated file-sized work.
//
// A reference that does not name a plainly stored integer (free or absent, held in an object stream, a stale offset, a
// non-integer value) yields ok == false and leaves the caller on the fallback scan. Integers are never encrypted, so
// the decryptor (which may not be installed yet) plays no part.
func (d *Document) resolveStreamLength(ref Ref) (int64, bool) {
	entry, ok := d.xref[ref.Num]
	if !ok || entry.kind != xrefInFile || entry.offset < 0 || entry.offset >= int64(len(d.data)) {
		return 0, false
	}
	p := newParser(d.data, int(entry.offset))
	num, err := p.expectInt()
	if err != nil || num != int64(ref.Num) {
		return 0, false
	}
	if _, err = p.expectInt(); err != nil { // The generation number, which takes no part in resolution.
		return 0, false
	}
	if err = p.expectKeyword("obj"); err != nil {
		return 0, false
	}
	length, err := p.expectInt()
	if err != nil {
		return 0, false
	}
	return length, true
}

// parseIndirectAtBounded is parseIndirectAt with an exclusive upper bound on the fallback "endstream" scan. The repair
// sweep passes the offset just past the file's last "endstream" so a swept stream header with no matching endstream
// fails immediately instead of scanning to end of input (O(n²) on input full of bare stream keywords); other callers
// pass len(data).
//
// On failure end is the offset the attempt stopped reading at (see parser.resumePos), so a sweeping caller can continue
// past the work already done. resolveLength, when non-nil, supplies the value of an indirect /Length; the repair sweep
// passes nil, since it is rebuilding the table such a reference would resolve against.
func parseIndirectAtBounded(data []byte, off int64, wantNum, endstreamLimit int, resolveLength lengthResolver,
) (obj Object, gen int, end int64, err error) {
	if off < 0 || off >= int64(len(data)) {
		return nil, 0, 0, errStreamOutOfRange
	}
	p := newParser(data, int(off))
	num, err := p.expectInt()
	if err != nil {
		return nil, 0, int64(p.resumePos()), errNotIndirect
	}
	genNum, err := p.expectInt()
	if err != nil {
		return nil, 0, int64(p.resumePos()), errNotIndirect
	}
	if err = p.expectKeyword("obj"); err != nil {
		return nil, 0, int64(p.resumePos()), errNotIndirect
	}
	if wantNum >= 0 && num != int64(wantNum) {
		return nil, 0, int64(p.resumePos()), errWrongObject
	}
	if genNum < 0 || genNum > 0xffff {
		genNum = 0 // A nonsensical generation cannot be a real one; the encryption key uses its low two bytes.
	}
	if obj, err = p.parseObject(); err != nil {
		return nil, 0, int64(p.resumePos()), err
	}
	// A stream keyword after the object turns a dictionary into a stream. The pushback stack is empty after a dictionary
	// (parseDict consumes through its closing >>), so the lexer position is authoritative for the payload; for other
	// types the next token's recorded position gives the object extent even after lookahead pushback.
	tok, err := p.next()
	if err != nil {
		return obj, int(genNum), int64(p.lex.pos), nil //nolint:nilerr // The object parsed; trailing junk is ignored.
	}
	if tok.kind != tkKeyword || !bytes.Equal(tok.s, []byte("stream")) {
		// Not a stream; the object stands on its own. "endobj" is deliberately not required (leniency).
		return obj, int(genNum), int64(tok.pos), nil
	}
	dict, ok := obj.(Dict)
	if !ok {
		return nil, 0, int64(p.resumePos()), fmt.Errorf("%w: stream keyword after non-dictionary", errUnexpectedToken)
	}
	raw, rawEnd, err := captureRawStream(data, p.lex.pos, endstreamLimit, dict, resolveLength)
	if err != nil {
		return nil, 0, int64(p.lex.pos), err
	}
	return &Stream{Dict: dict, Raw: raw}, int(genNum), rawEnd, nil
}

// captureRawStream slices the raw stream payload that begins after the stream keyword at pos. When the dictionary
// carries a plausible /Length (the payload fits and is followed by "endstream") that length is used; otherwise the data
// is scanned for the next "endstream" keyword and the end-of-line marker before it is trimmed, as deployed readers
// recover. The returned end offset is just past the endstream keyword.
func captureRawStream(data []byte, pos, endstreamLimit int, dict Dict, resolveLength lengthResolver,
) (raw []byte, end int64, err error) {
	// Per ISO 32000-2 7.3.8.1 the stream keyword is followed by CRLF or LF; a lone CR and a missing break are
	// tolerated.
	if pos < len(data) && data[pos] == '\r' {
		pos++
	}
	if pos < len(data) && data[pos] == '\n' {
		pos++
	}
	// The bound is written as length <= len(data)-pos rather than pos+length <= len(data): pos is within [0, len(data)]
	// so len(data)-pos is a non-negative int that cannot overflow, whereas pos+length would wrap negative for a large
	// but valid Integer /Length (near math.MaxInt64) and slip a bogus, negative dataEnd past the guard.
	if length, ok := streamLength(dict, resolveLength); ok && length >= 0 && length <= int64(len(data)-pos) {
		dataEnd := pos + int(length)
		if at, found := endstreamAt(data, dataEnd); found {
			return data[pos:dataEnd], at, nil
		}
	}
	// The fallback scan searches only up to endstreamLimit (never past the buffer). A stream header at or beyond that
	// bound cannot be followed by an "endstream" keyword, so report the miss without scanning.
	searchEnd := min(endstreamLimit, len(data))
	if pos >= searchEnd {
		return nil, 0, errNoEndstream
	}
	idx := bytes.Index(data[pos:searchEnd], []byte("endstream"))
	if idx < 0 {
		return nil, 0, errNoEndstream
	}
	dataEnd := pos + idx
	end = int64(dataEnd + len("endstream"))
	// Trim the end-of-line marker that precedes endstream (it is not part of the payload). The pos-relative bounds keep
	// a zero-length payload from double-counting the EOL already consumed after the stream keyword.
	switch {
	case dataEnd >= pos+2 && data[dataEnd-2] == '\r' && data[dataEnd-1] == '\n':
		dataEnd -= 2
	case dataEnd > pos && (data[dataEnd-1] == '\n' || data[dataEnd-1] == '\r'):
		dataEnd--
	}
	return data[pos:dataEnd], end, nil
}

// streamLength returns the stream's declared /Length. A direct integer is taken as written; an indirect reference —
// permitted by ISO 32000-2 7.3.8.2 and emitted by every single-pass writer, since the payload's size is unknown when
// the dictionary is written — goes to resolveLength when supplied. The result is only a proposal: captureRawStream
// requires "endstream" to follow the payload it describes, so a stale or hostile value costs nothing beyond the scan.
func streamLength(dict Dict, resolveLength lengthResolver) (int64, bool) {
	if length, ok := AsInt(dict["Length"]); ok {
		return length, true
	}
	if ref, ok := dict["Length"].(Ref); ok && resolveLength != nil {
		return resolveLength(ref)
	}
	return 0, false
}

// endstreamAt reports whether the "endstream" keyword follows pos after optional whitespace, returning the offset just
// past it.
func endstreamAt(data []byte, pos int) (int64, bool) {
	for pos < len(data) && isWhitespace(data[pos]) {
		pos++
	}
	if bytes.HasPrefix(data[pos:], []byte("endstream")) {
		return int64(pos + len("endstream")), true
	}
	return 0, false
}
