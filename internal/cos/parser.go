package cos

import (
	"bytes"
	"errors"
	"fmt"
)

// maxNestingDepth caps how deeply arrays and dictionaries may nest, guarding against stack exhaustion from
// hostile input (see plan.md "Resource limits & robustness").
const maxNestingDepth = 512

// maxObjectNumber bounds accepted object numbers. ISO 32000-2 Annex C suggests 8388607 as an implementation
// limit; this is slightly more generous while still bounding lookup structures.
const maxObjectNumber = 1 << 24

var (
	errTooDeep          = errors.New("objects nested too deeply")
	errUnexpectedEOF    = errors.New("unexpected end of input")
	errBadDictKey       = errors.New("dictionary key is not a name")
	errUnexpectedToken  = errors.New("unexpected token")
	errNotIndirect      = errors.New("not an indirect object header")
	errWrongObject      = errors.New("indirect object header has the wrong object number")
	errNoEndstream      = errors.New("missing endstream keyword")
	errStreamOutOfRange = errors.New("stream extends past end of input")
)

// parser builds objects from a token stream, with a small pushback stack for the lookahead that indirect
// references ("N G R") require.
type parser struct {
	stack []token
	lex   lexer
	depth int
}

func newParser(data []byte, pos int) *parser {
	return &parser{lex: lexer{data: data, pos: pos}}
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
	if second.kind != tkInt || second.i < 0 || second.i > 1<<31 {
		p.push(second)
		return Integer(tok.i), nil
	}
	third, err := p.next()
	if err != nil {
		return nil, err
	}
	if third.kind == tkKeyword && bytes.Equal(third.s, []byte("R")) {
		return Ref{Num: int(tok.i), Gen: int(second.i)}, nil
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

// parseIndirectAt parses the indirect object "num gen obj ... [stream ... endstream]" at offset off within data.
// When wantNum is non-negative, the header's object number must match it (detecting stale or wrong xref
// offsets). It returns the object, the object's generation number (which the standard security handler folds
// into the per-object decryption key), and the offset just past it (past endstream for streams), which the
// repair scanner uses to skip stream payloads.
func parseIndirectAt(data []byte, off int64, wantNum int) (obj Object, gen int, end int64, err error) {
	if off < 0 || off >= int64(len(data)) {
		return nil, 0, 0, errStreamOutOfRange
	}
	p := newParser(data, int(off))
	num, err := p.expectInt()
	if err != nil {
		return nil, 0, 0, errNotIndirect
	}
	genNum, err := p.expectInt()
	if err != nil {
		return nil, 0, 0, errNotIndirect
	}
	if err = p.expectKeyword("obj"); err != nil {
		return nil, 0, 0, errNotIndirect
	}
	if wantNum >= 0 && num != int64(wantNum) {
		return nil, 0, 0, errWrongObject
	}
	if genNum < 0 || genNum > 0xffff {
		genNum = 0 // A nonsensical generation cannot be a real one; the encryption key uses its low two bytes.
	}
	if obj, err = p.parseObject(); err != nil {
		return nil, 0, 0, err
	}
	// A stream keyword after the object turns a dictionary into a stream. The pushback stack is empty here for
	// any dictionary object (parseDict consumes through its closing >>), so the lexer position is authoritative
	// for the stream payload; for other object types, the next token's recorded start position yields the object
	// extent even when lookahead tokens were pushed back.
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
		return nil, 0, 0, fmt.Errorf("%w: stream keyword after non-dictionary", errUnexpectedToken)
	}
	raw, rawEnd, err := captureRawStream(data, p.lex.pos, dict)
	if err != nil {
		return nil, 0, 0, err
	}
	return &Stream{Dict: dict, Raw: raw}, int(genNum), rawEnd, nil
}

// captureRawStream slices the raw stream payload that begins after the stream keyword at pos. When the
// dictionary carries a direct, plausible /Length — the payload fits and is followed by "endstream" — that length
// is used; otherwise (indirect, missing, or wrong /Length) the data is scanned for the next "endstream" keyword
// and any final end-of-line marker before it is trimmed, mirroring the recovery behavior of deployed readers.
// The returned end offset is just past the endstream keyword.
func captureRawStream(data []byte, pos int, dict Dict) (raw []byte, end int64, err error) {
	// Per ISO 32000-2 7.3.8.1 the stream keyword is followed by CRLF or LF; a lone CR and a missing break are
	// tolerated.
	if pos < len(data) && data[pos] == '\r' {
		pos++
	}
	if pos < len(data) && data[pos] == '\n' {
		pos++
	}
	if length, ok := AsInt(dict["Length"]); ok && length >= 0 && int64(pos)+length <= int64(len(data)) {
		dataEnd := pos + int(length)
		if at, found := endstreamAt(data, dataEnd); found {
			return data[pos:dataEnd], at, nil
		}
	}
	idx := bytes.Index(data[pos:], []byte("endstream"))
	if idx < 0 {
		return nil, 0, errNoEndstream
	}
	dataEnd := pos + idx
	end = int64(dataEnd + len("endstream"))
	// Trim the end-of-line marker that precedes endstream (it is not part of the payload). The pos-relative
	// bounds keep a zero-length payload from double-counting the EOL already consumed after the stream keyword.
	switch {
	case dataEnd >= pos+2 && data[dataEnd-2] == '\r' && data[dataEnd-1] == '\n':
		dataEnd -= 2
	case dataEnd > pos && (data[dataEnd-1] == '\n' || data[dataEnd-1] == '\r'):
		dataEnd--
	}
	return data[pos:dataEnd], end, nil
}

// endstreamAt reports whether the "endstream" keyword follows pos after optional whitespace, returning the
// offset just past it.
func endstreamAt(data []byte, pos int) (int64, bool) {
	for pos < len(data) && isWhitespace(data[pos]) {
		pos++
	}
	if bytes.HasPrefix(data[pos:], []byte("endstream")) {
		return int64(pos + len("endstream")), true
	}
	return 0, false
}
