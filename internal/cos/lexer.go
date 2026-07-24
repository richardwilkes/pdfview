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
	"errors"
	"math"
	"strconv"
)

// tokenKind identifies the lexical class of a token.
type tokenKind uint8

const (
	tkEOF tokenKind = iota
	tkInt
	tkReal
	tkString
	tkName
	tkKeyword
	tkArrayOpen
	tkArrayClose
	tkDictOpen
	tkDictClose
	tkBraceOpen
	tkBraceClose
)

// token is one lexical token. The payload field used depends on kind: i for tkInt, f for tkReal, and s for tkString
// (decoded bytes), tkName (decoded name), and tkKeyword (keyword text). pos is the byte offset of the token's first
// character (or of the end of input for tkEOF), which survives parser pushback so callers can recover accurate
// object-extent positions.
type token struct {
	s    []byte
	i    int64
	f    float64
	pos  int
	kind tokenKind
}

var (
	errUnterminatedString = errors.New("unterminated string")
	errUnterminatedHex    = errors.New("unterminated hex string")
	errStrayDelimiter     = errors.New("stray delimiter")
	errStringTooDeep      = errors.New("string parentheses nested too deeply")
)

// isWhitespace reports whether c is PDF whitespace (ISO 32000-2 Table 1).
func isWhitespace(c byte) bool {
	switch c {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	default:
		return false
	}
}

// isDelimiter reports whether c is a PDF delimiter character (ISO 32000-2 Table 2).
func isDelimiter(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	default:
		return false
	}
}

// isRegular reports whether c is a regular character: neither whitespace nor a delimiter.
func isRegular(c byte) bool {
	return !isWhitespace(c) && !isDelimiter(c)
}

// lexer tokenizes PDF syntax from data starting at pos. It never allocates for tokens that can alias data (names and
// keywords without escapes); decoded strings are built fresh.
type lexer struct {
	data []byte
	pos  int
}

// skipSpace advances past whitespace and comments.
func (l *lexer) skipSpace() {
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		switch {
		case isWhitespace(c):
			l.pos++
		case c == '%':
			for l.pos < len(l.data) && l.data[l.pos] != '\n' && l.data[l.pos] != '\r' {
				l.pos++
			}
		default:
			return
		}
	}
}

// next returns the next token. At end of input it returns a tkEOF token and no error; lexical errors (such as an
// unterminated string) are reported as errors.
func (l *lexer) next() (token, error) {
	l.skipSpace()
	start := l.pos
	if l.pos >= len(l.data) {
		return token{kind: tkEOF, pos: start}, nil
	}
	tok, err := l.lexTokenAt()
	tok.pos = start
	return tok, err
}

func (l *lexer) lexTokenAt() (token, error) {
	c := l.data[l.pos]
	switch {
	case c >= '0' && c <= '9', c == '+', c == '-', c == '.':
		return l.lexNumber(), nil
	case c == '/':
		return l.lexName(), nil
	case c == '(':
		return l.lexLiteralString()
	case c == '<':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '<' {
			l.pos += 2
			return token{kind: tkDictOpen}, nil
		}
		return l.lexHexString()
	case c == '>':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '>' {
			l.pos += 2
			return token{kind: tkDictClose}, nil
		}
		l.pos++
		return token{}, errStrayDelimiter
	case c == '[':
		l.pos++
		return token{kind: tkArrayOpen}, nil
	case c == ']':
		l.pos++
		return token{kind: tkArrayClose}, nil
	case c == '{':
		l.pos++
		return token{kind: tkBraceOpen}, nil
	case c == '}':
		l.pos++
		return token{kind: tkBraceClose}, nil
	case c == ')':
		l.pos++
		return token{}, errStrayDelimiter
	default:
		return l.lexKeyword(), nil
	}
}

// lexNumber scans an integer or real. PDF numbers have no exponent notation. Out-of-range integers and other oddities
// (multiple signs or decimal points) are handled leniently, converting what can be converted.
func (l *lexer) lexNumber() token {
	start := l.pos
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		if (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.' {
			l.pos++
		} else {
			break
		}
	}
	span := l.data[start:l.pos]
	neg := false
	i := 0
	for i < len(span) && (span[i] == '+' || span[i] == '-') {
		if span[i] == '-' {
			neg = !neg
		}
		i++
	}
	var whole int64
	digits := false
	overflow := false
	for i < len(span) && span[i] >= '0' && span[i] <= '9' {
		digits = true
		d := int64(span[i] - '0')
		if whole > (math.MaxInt64-d)/10 {
			overflow = true
		} else {
			whole = whole*10 + d
		}
		i++
	}
	if i < len(span) && span[i] == '.' {
		// Real: parse the mantissa span with strconv for correct rounding, ignoring any junk beyond a second decimal
		// point. A range error is not a parse failure: strconv still hands back the correctly-signed infinity (or zero,
		// on underflow), which is the right answer and one the downstream finiteness guards reject. Falling back to
		// `whole` there would instead yield the arbitrary truncated prefix accumulated before the integer overflow was
		// detected, letting a bogus finite coordinate through.
		end := i + 1
		for end < len(span) && span[end] >= '0' && span[end] <= '9' {
			end++
		}
		f, err := strconv.ParseFloat(string(span[:end]), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			f = float64(whole)
			if neg {
				f = -f
			}
		}
		return token{kind: tkReal, f: f}
	}
	if overflow || !digits {
		f, err := strconv.ParseFloat(string(span), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			f = 0
		}
		if overflow {
			return token{kind: tkReal, f: f}
		}
		return token{kind: tkInt, i: int64(f)}
	}
	if neg {
		whole = -whole
	}
	return token{kind: tkInt, i: whole}
}

// lexName scans a name object, decoding #xx escapes. An invalid escape keeps the '#' literally (leniency; the spec
// calls it an error).
func (l *lexer) lexName() token {
	l.pos++ // consume '/'
	start := l.pos
	hasEscape := false
	for l.pos < len(l.data) && isRegular(l.data[l.pos]) {
		if l.data[l.pos] == '#' {
			hasEscape = true
		}
		l.pos++
	}
	raw := l.data[start:l.pos]
	if !hasEscape {
		return token{kind: tkName, s: raw}
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '#' && i+2 < len(raw) {
			hi := hexVal(raw[i+1])
			lo := hexVal(raw[i+2])
			if hi >= 0 && lo >= 0 {
				out = append(out, byte(hi<<4|lo))
				i += 2
				continue
			}
		}
		out = append(out, raw[i])
	}
	return token{kind: tkName, s: out}
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}

// maxStringNesting caps parenthesis nesting inside literal strings, a cheap guard against pathological input (nesting
// requires one byte per level, so this is generous for real documents).
const maxStringNesting = 4096

// lexLiteralString scans a (...) string, decoding backslash escapes and normalizing unescaped end-of-line markers to a
// single line feed, per ISO 32000-2 7.3.4.2.
func (l *lexer) lexLiteralString() (token, error) {
	l.pos++ // consume '('
	out := make([]byte, 0, 32)
	depth := 1
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		switch c {
		case '(':
			depth++
			if depth > maxStringNesting {
				return token{}, errStringTooDeep
			}
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return token{kind: tkString, s: out}, nil
			}
			out = append(out, c)
		case '\r':
			// CR or CRLF becomes LF.
			if l.pos < len(l.data) && l.data[l.pos] == '\n' {
				l.pos++
			}
			out = append(out, '\n')
		case '\\':
			if l.pos >= len(l.data) {
				return token{}, errUnterminatedString
			}
			e := l.data[l.pos]
			l.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, e)
			case '\r':
				// Line continuation; swallow an optional LF after the CR.
				if l.pos < len(l.data) && l.data[l.pos] == '\n' {
					l.pos++
				}
			case '\n':
				// Line continuation.
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := int(e - '0')
				for range 2 {
					if l.pos < len(l.data) && l.data[l.pos] >= '0' && l.data[l.pos] <= '7' {
						v = v<<3 | int(l.data[l.pos]-'0')
						l.pos++
					}
				}
				out = append(out, byte(v))
			default:
				// Unknown escape: the backslash is dropped and the character kept.
				out = append(out, e)
			}
		default:
			out = append(out, c)
		}
	}
	return token{}, errUnterminatedString
}

// lexHexString scans a <...> string. Whitespace and invalid characters between the digits are skipped (leniency), and a
// missing final digit is treated as 0, per ISO 32000-2 7.3.4.3.
func (l *lexer) lexHexString() (token, error) {
	l.pos++ // consume '<'
	out := make([]byte, 0, 16)
	var hi int
	haveHi := false
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		if c == '>' {
			if haveHi {
				out = append(out, byte(hi<<4))
			}
			return token{kind: tkString, s: out}, nil
		}
		v := hexVal(c)
		if v < 0 {
			continue
		}
		if haveHi {
			out = append(out, byte(hi<<4|v))
			haveHi = false
		} else {
			hi = v
			haveHi = true
		}
	}
	return token{}, errUnterminatedHex
}

// lexKeyword scans a run of regular characters (keywords such as obj, endobj, stream, R, true, false, null).
func (l *lexer) lexKeyword() token {
	start := l.pos
	for l.pos < len(l.data) && isRegular(l.data[l.pos]) {
		l.pos++
	}
	if l.pos == start {
		// Defensive: cannot happen, since next() dispatches only regular characters here.
		l.pos++
	}
	return token{kind: tkKeyword, s: l.data[start:l.pos]}
}
