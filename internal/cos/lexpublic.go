// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos

// This file exports the package's lexer for the other consumers of PDF's surface syntax: content streams
// (internal/content) and PostScript-calculator function programs (internal/function). Both share COS's lexical rules
// exactly (ISO 32000-2 7.2) but assemble tokens differently from the object parser — content streams have operators and
// no indirect references — so they consume tokens, not parsed objects.

// TokenKind identifies the lexical class of a Token.
type TokenKind uint8

// TokenKind values.
const (
	TokenEOF TokenKind = iota
	TokenInt
	TokenReal
	TokenString
	TokenName
	TokenKeyword
	TokenArrayOpen
	TokenArrayClose
	TokenDictOpen
	TokenDictClose
	TokenBraceOpen
	TokenBraceClose
)

// Token is one lexical token. The payload field depends on Kind: Int for TokenInt, Real for TokenReal, and Bytes for
// TokenString (decoded bytes), TokenName (decoded name), and TokenKeyword (keyword text). Bytes may alias the input
// buffer. Pos is the byte offset of the token's first character (end of input for TokenEOF).
type Token struct {
	Bytes []byte
	Int   int64
	Real  float64
	Pos   int
	Kind  TokenKind
}

// Lexer tokenizes PDF surface syntax from a byte slice. The zero value is not usable; construct with NewLexer.
type Lexer struct {
	lex lexer
}

// NewLexer returns a Lexer reading data from offset pos.
func NewLexer(data []byte, pos int) *Lexer {
	return &Lexer{lex: lexer{data: data, pos: pos}}
}

// Next returns the next token. At end of input it returns a TokenEOF token with ok true. Lexical errors — an
// unterminated string, a stray delimiter — report ok false with the position already advanced past the offending input,
// so a lenient caller can simply continue scanning: the position never sticks, guaranteeing forward progress.
func (l *Lexer) Next() (tok Token, ok bool) {
	t, err := l.lex.next()
	if err != nil {
		return Token{Pos: t.pos}, false
	}
	return Token{
		Bytes: t.s,
		Int:   t.i,
		Real:  t.f,
		Pos:   t.pos,
		Kind:  publicKind(t.kind),
	}, true
}

// Pos returns the current read offset.
func (l *Lexer) Pos() int {
	return l.lex.pos
}

// SetPos moves the read offset (used to skip non-lexical spans such as inline-image payloads). Offsets outside the data
// are clamped to its end.
func (l *Lexer) SetPos(pos int) {
	if pos < 0 || pos > len(l.lex.data) {
		pos = len(l.lex.data)
	}
	l.lex.pos = pos
}

// publicKind maps the internal token kinds to the exported ones. The internal set is a superset in ordering only; map
// explicitly so neither enum constrains the other.
func publicKind(k tokenKind) TokenKind {
	switch k {
	case tkInt:
		return TokenInt
	case tkReal:
		return TokenReal
	case tkString:
		return TokenString
	case tkName:
		return TokenName
	case tkKeyword:
		return TokenKeyword
	case tkArrayOpen:
		return TokenArrayOpen
	case tkArrayClose:
		return TokenArrayClose
	case tkDictOpen:
		return TokenDictOpen
	case tkDictClose:
		return TokenDictClose
	case tkBraceOpen:
		return TokenBraceOpen
	case tkBraceClose:
		return TokenBraceClose
	default:
		return TokenEOF
	}
}

// IsWhitespaceByte reports whether c is PDF whitespace (ISO 32000-2 Table 1). Exported for consumers that need
// byte-level scanning around lexical data, such as inline-image payload recovery.
func IsWhitespaceByte(c byte) bool {
	return isWhitespace(c)
}

// IsDelimiterByte reports whether c is a PDF delimiter character (ISO 32000-2 Table 2).
func IsDelimiterByte(c byte) bool {
	return isDelimiter(c)
}
