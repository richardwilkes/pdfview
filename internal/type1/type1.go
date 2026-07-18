// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package type1 parses Adobe Type 1 font programs (the FontFile stream of a PDF font dictionary): the PFA/PFB
// container, eexec decryption, the built-in /Encoding, /FontMatrix and /FontBBox from the clear-text portion, and the
// /Subrs and /CharStrings charstrings (decrypted with r=4330 and their lenIV bytes stripped) from the private portion.
// Charstrings execute through go-text's psinterpreter (whose Type1Charstring context supplies the number parsing and
// subroutine machinery); this package contributes only the operator handler — see charstring.go. The format authority
// is Adobe's published "Adobe Type 1 Font Format" specification; the eexec and charstring encryption algorithm (r=55665
// / r=4330, c1=52845, c2=22719) is printed there in full.
//
// Hostile input never panics Parse or Glyph: both recover into errors, and all counts and lengths are capped.
package type1

import (
	"errors"
	"slices"
	"strconv"
)

// Errors reported by this package. Callers degrade a failed parse to font substitution and a failed glyph to a missing
// outline; nothing here escalates to the page level.
var (
	// ErrBadType1 marks a font program too malformed to use.
	ErrBadType1 = errors.New("malformed Type 1 font program")
	// ErrBadCharstring marks a charstring whose execution failed.
	ErrBadCharstring = errors.New("malformed Type 1 charstring")
)

// Caps against hostile input: counts are bounded so a small file cannot claim huge tables, and every byte read is
// bounded by the input length.
const (
	maxCharstrings = 65536
	maxSubrs       = 65536
	maxEncodingOps = 4096 // dup/put entries scanned in an /Encoding array (256 are meaningful)
)

// Token spellings the parser matches repeatedly.
const (
	notdefName = ".notdef"
	kwDef      = "def"
)

// Font is a parsed Type 1 font program. It is immutable after Parse except for StdEnc, which the caller may set once
// before glyph interpretation; concurrent Glyph calls are safe (each builds its own interpreter).
type Font struct {
	// CharStrings maps glyph names to decrypted charstrings (lenIV bytes already stripped).
	CharStrings map[string][]byte
	// Encoding is the program's explicit built-in encoding array, nil when it declares none (see StdEncoding).
	Encoding *[256]string
	// StdEnc supplies the StandardEncoding code→name table consulted by the seac operator (the composite
	// accented-character operator addresses its components by standard-encoding code). The caller sets it after Parse —
	// internal/font owns the generated Annex D tables — and seac degrades to the base glyph when nil.
	StdEnc *[256]string
	// Names lists the charstring names with notdefName forced to index 0, giving each glyph a stable synthetic GID
	// (Type 1 programs address glyphs by name; the engine's device seam addresses them by index).
	Names []string
	// Subrs holds the decrypted local subroutines.
	Subrs [][]byte
	// FontMatrix is the glyph-space→text-space matrix ({0.001 0 0 0.001 0 0} when the program declares none).
	FontMatrix [6]float32
	// FontBBox is the declared bounding box (x0 y0 x1 y1 in glyph units), valid when HasBBox.
	FontBBox [4]float32
	// StdEncoding reports that the program declared "/Encoding StandardEncoding def".
	StdEncoding bool
	// HasBBox/HasMatrix report whether the clear text carried the respective entries.
	HasBBox   bool
	HasMatrix bool
}

// Parse reads a Type 1 font program in PFB, PFA, or raw (PDF FontFile) form.
func Parse(data []byte) (f *Font, err error) {
	defer func() {
		if recover() != nil { // Hostile input must degrade, never panic.
			f = nil
			err = ErrBadType1
		}
	}()
	clearPart, enc, err := splitProgram(data)
	if err != nil {
		return nil, err
	}
	f = &Font{FontMatrix: [6]float32{0.001, 0, 0, 0.001, 0, 0}}
	f.parseClear(clearPart)
	if isHexEexec(enc) {
		enc = decodeHex(enc)
	}
	private := decrypt(enc, eexecR, 4)
	f.parsePrivate(private)
	if len(f.CharStrings) == 0 {
		return nil, ErrBadType1
	}
	f.buildNames()
	return f, nil
}

// splitProgram divides the program into its clear-text and encrypted portions, handling PFB segmentation and locating
// the eexec boundary. The PDF stream's /Length1//Length2 are deliberately not trusted (real files get them wrong); the
// eexec keyword is authoritative, as in deployed parsers.
func splitProgram(data []byte) (clearPart, encPart []byte, err error) {
	if len(data) >= 2 && data[0] == 0x80 {
		data, err = joinPFB(data)
		if err != nil {
			return nil, nil, err
		}
	}
	idx := indexToken(data, "eexec")
	if idx < 0 {
		return nil, nil, ErrBadType1
	}
	clearPart = data[:idx]
	pos := idx + len("eexec")
	// The encrypted portion begins after the whitespace following the keyword (FreeType-compatible: skip all
	// immediately following whitespace bytes; the first ciphertext byte is effectively random, so a ciphertext byte is
	// mistaken for whitespace with negligible probability — and hex form tolerates it regardless).
	for pos < len(data) && isWhite(data[pos]) {
		pos++
	}
	return clearPart, data[pos:], nil
}

// joinPFB reassembles the segments of a PFB container: 0x80 0x01 ASCII and 0x80 0x02 binary segments, each with a
// 4-byte little-endian length, terminated by 0x80 0x03. The concatenation in file order reproduces the raw program
// (clear text, encrypted portion, trailer).
func joinPFB(data []byte) ([]byte, error) {
	var out []byte
	pos := 0
	for pos+2 <= len(data) {
		if data[pos] != 0x80 {
			return nil, ErrBadType1
		}
		kind := data[pos+1]
		if kind == 0x03 {
			break
		}
		if kind != 0x01 && kind != 0x02 {
			return nil, ErrBadType1
		}
		if pos+6 > len(data) {
			return nil, ErrBadType1
		}
		n := int(data[pos+2]) | int(data[pos+3])<<8 | int(data[pos+4])<<16 | int(data[pos+5])<<24
		pos += 6
		if n < 0 || pos+n > len(data) {
			return nil, ErrBadType1
		}
		out = append(out, data[pos:pos+n]...)
		pos += n
	}
	if len(out) == 0 {
		return nil, ErrBadType1
	}
	return out, nil
}

// indexToken finds a keyword at a token boundary (not inside a longer name), returning its byte offset or -1.
func indexToken(data []byte, word string) int {
	for i := 0; i+len(word) <= len(data); i++ {
		if data[i] != word[0] || string(data[i:i+len(word)]) != word {
			continue
		}
		if i > 0 && !isDelim(data[i-1]) && !isWhite(data[i-1]) {
			continue
		}
		end := i + len(word)
		if end < len(data) && !isDelim(data[end]) && !isWhite(data[end]) {
			continue
		}
		return i
	}
	return -1
}

// isHexEexec reports whether the encrypted portion is hex-encoded (PFA form): per the Type 1 spec, the first four
// non-whitespace bytes all being hex digits marks ASCII form.
func isHexEexec(data []byte) bool {
	seen := 0
	for _, b := range data {
		if isWhite(b) {
			continue
		}
		if !isHexDigit(b) {
			return false
		}
		seen++
		if seen == 4 {
			return true
		}
	}
	return false
}

func isHexDigit(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func hexVal(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	default:
		return b - 'A' + 10
	}
}

// decodeHex decodes hex data, ignoring whitespace and stopping at the first non-hex byte.
func decodeHex(data []byte) []byte {
	out := make([]byte, 0, len(data)/2)
	var hi byte
	haveHi := false
	for _, b := range data {
		if isWhite(b) {
			continue
		}
		if !isHexDigit(b) {
			break
		}
		if haveHi {
			out = append(out, hi<<4|hexVal(b))
			haveHi = false
		} else {
			hi = hexVal(b)
			haveHi = true
		}
	}
	if haveHi { // An odd trailing digit pairs with 0, matching lenient hex decoders.
		out = append(out, hi<<4)
	}
	return out
}

// Encryption constants from the Type 1 spec (chapter 7, "Encryption").
const (
	eexecR       = 55665
	charstringR  = 4330
	encryptC1    = 52845
	encryptC2    = 22719
	defaultLenIV = 4
)

// decrypt runs the Type 1 decryption algorithm with initial key r, dropping the first skip plaintext bytes (4 for
// eexec, lenIV for charstrings). A negative skip means the data is not encrypted at all (the lenIV -1 convention some
// generators use) and returns the input unchanged.
func decrypt(data []byte, r uint16, skip int) []byte {
	if skip < 0 {
		return data
	}
	if skip > len(data) {
		skip = len(data)
	}
	out := make([]byte, 0, len(data)-skip)
	for i, c := range data {
		p := c ^ byte(r>>8)
		r = (uint16(c)+r)*encryptC1 + encryptC2
		if i >= skip {
			out = append(out, p)
		}
	}
	return out
}

// parseClear extracts /FontMatrix, /FontBBox, and the built-in /Encoding from the clear-text portion.
func (f *Font) parseClear(data []byte) {
	s := scanner{data: data}
	for {
		tok, ok := s.next()
		if !ok {
			return
		}
		if tok.kind != tokName {
			continue
		}
		switch tok.text {
		case "FontMatrix":
			if v, ok2 := s.numbers(6); ok2 {
				for i := range 6 {
					f.FontMatrix[i] = float32(v[i])
				}
				f.HasMatrix = true
			}
		case "FontBBox":
			if v, ok2 := s.numbers(4); ok2 {
				for i := range 4 {
					f.FontBBox[i] = float32(v[i])
				}
				f.HasBBox = true
			}
		case "Encoding":
			f.parseEncoding(&s)
		}
	}
}

// parseEncoding handles the token stream after /Encoding: either the StandardEncoding keyword or an array built with
// "dup <code> /<name> put" entries, terminated by def (or readonly def).
func (f *Font) parseEncoding(s *scanner) {
	tok, ok := s.next()
	if !ok {
		return
	}
	if tok.kind == tokKeyword && tok.text == "StandardEncoding" {
		f.StdEncoding = true
		return
	}
	var table [256]string
	filled := false
	for range maxEncodingOps {
		tok, ok = s.next()
		if !ok {
			break
		}
		if tok.kind == tokKeyword && (tok.text == kwDef || tok.text == "readonly") {
			if tok.text == kwDef {
				break
			}
			continue
		}
		if tok.kind != tokKeyword || tok.text != "dup" {
			continue
		}
		code, okC := s.integer()
		if !okC {
			continue
		}
		nameTok, okN := s.next()
		if !okN {
			break
		}
		if nameTok.kind != tokName {
			continue
		}
		putTok, okP := s.next()
		if !okP {
			break
		}
		if putTok.kind == tokKeyword && putTok.text == "put" && code >= 0 && code <= 255 {
			table[code] = nameTok.text
			filled = true
		}
	}
	if filled {
		f.Encoding = &table
	}
}

// parsePrivate extracts /lenIV, /Subrs, and /CharStrings from the decrypted private portion.
func (f *Font) parsePrivate(data []byte) {
	s := scanner{data: data}
	lenIV := defaultLenIV
	for {
		tok, ok := s.next()
		if !ok {
			return
		}
		if tok.kind != tokName {
			continue
		}
		switch tok.text {
		case "lenIV":
			if v, ok2 := s.integer(); ok2 && v >= -1 && v <= 64 {
				lenIV = int(v)
			}
		case "Subrs":
			f.parseSubrs(&s, lenIV)
		case "CharStrings":
			f.parseCharStrings(&s, lenIV)
		}
	}
}

// parseSubrs reads "dup <index> <length> RD <binary> NP" entries. The binary-read operator's name is defined by the
// font itself (conventionally RD or -|), so any keyword token in that position is accepted.
func (f *Font) parseSubrs(s *scanner, lenIV int) {
	count, ok := s.integer()
	if !ok || count < 0 || count > maxSubrs {
		return
	}
	subrs := make([][]byte, count)
	for range count + 8 { // A few non-dup tokens (array, noaccess, ...) may precede or interleave.
		save := s.pos
		tok, ok2 := s.next()
		if !ok2 {
			break
		}
		if tok.kind == tokKeyword && (tok.text == "ND" || tok.text == "|-" || tok.text == kwDef || tok.text == "end") {
			break
		}
		if tok.kind != tokKeyword || tok.text != "dup" {
			if tok.kind == tokName { // A following dictionary entry (/CharStrings ...) — rewind and stop.
				s.pos = save
				break
			}
			continue
		}
		idx, okI := s.integer()
		length, okL := s.integer()
		if !okI || !okL {
			continue
		}
		raw, okR := s.binary(length)
		if !okR {
			break
		}
		if idx >= 0 && idx < count {
			subrs[idx] = decrypt(raw, charstringR, lenIV)
		}
		s.skipKeyword() // The NP token (or whatever the font calls it).
	}
	f.Subrs = subrs
}

// parseCharStrings reads "/<name> <length> RD <binary> ND" entries between begin and end.
func (f *Font) parseCharStrings(s *scanner, lenIV int) {
	count, ok := s.integer()
	if !ok || count < 0 || count > maxCharstrings {
		return
	}
	f.CharStrings = make(map[string][]byte, min(count, 1024))
	// Skip forward to the begin keyword (the "dict dup" boilerplate varies).
	for range 16 {
		tok, ok2 := s.next()
		if !ok2 {
			return
		}
		if tok.kind == tokKeyword && tok.text == "begin" {
			break
		}
	}
	for len(f.CharStrings) <= maxCharstrings {
		tok, ok2 := s.next()
		if !ok2 {
			return
		}
		if tok.kind == tokKeyword && tok.text == "end" {
			return
		}
		if tok.kind != tokName {
			continue
		}
		length, okL := s.integer()
		if !okL {
			continue
		}
		raw, okR := s.binary(length)
		if !okR {
			return
		}
		f.CharStrings[tok.text] = decrypt(raw, charstringR, lenIV)
		s.skipKeyword() // The ND token.
	}
}

// buildNames assigns stable synthetic glyph indices: notdefName at 0 (present or not), then every other charstring name
// in sorted order (map iteration is randomized; sorting keeps GIDs deterministic per program).
func (f *Font) buildNames() {
	names := make([]string, 1, len(f.CharStrings)+1)
	names[0] = notdefName
	for name := range f.CharStrings {
		if name != notdefName {
			names = append(names, name)
		}
	}
	slices.Sort(names[1:])
	f.Names = names
}

// ---- token scanner -------------------------------------------------------------------------------------------

type tokKind uint8

const (
	tokName    tokKind = iota // /name (text carries the name without the slash)
	tokNumber                 // integer or real
	tokKeyword                // any executable token, including punctuation-named operators like -| and |-
	tokOther                  // structural delimiters: [ ] { }
)

type token struct {
	text  string
	value float64
	kind  tokKind
}

// scanner is a minimal PostScript-shaped tokenizer: whitespace and %-comments separate tokens; names begin with /;
// numbers parse leniently; anything else is a keyword or delimiter. It never backtracks (pos always advances), so
// scanning terminates on any input.
type scanner struct {
	data []byte
	pos  int
}

func isWhite(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f' || b == 0
}

func isDelim(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	default:
		return false
	}
}

func (s *scanner) skipSpace() {
	for s.pos < len(s.data) {
		b := s.data[s.pos]
		if isWhite(b) {
			s.pos++
			continue
		}
		if b == '%' {
			for s.pos < len(s.data) && s.data[s.pos] != '\n' && s.data[s.pos] != '\r' {
				s.pos++
			}
			continue
		}
		return
	}
}

// next returns the next token, or ok=false at end of input.
func (s *scanner) next() (token, bool) {
	s.skipSpace()
	if s.pos >= len(s.data) {
		return token{}, false
	}
	b := s.data[s.pos]
	switch b {
	case '[', ']', '{', '}':
		s.pos++
		return token{kind: tokOther, text: string(b)}, true
	case '(':
		// Skip a parenthesized string (balanced, with \-escapes); nothing the engine needs lives in one.
		s.pos++
		depth := 1
		for s.pos < len(s.data) && depth > 0 {
			switch s.data[s.pos] {
			case '\\':
				s.pos++
			case '(':
				depth++
			case ')':
				depth--
			}
			s.pos++
		}
		return token{kind: tokOther, text: "("}, true
	case '<', '>':
		s.pos++
		return token{kind: tokOther, text: string(b)}, true
	case '/':
		s.pos++
		start := s.pos
		for s.pos < len(s.data) && !isWhite(s.data[s.pos]) && !isDelim(s.data[s.pos]) {
			s.pos++
		}
		return token{kind: tokName, text: string(s.data[start:s.pos])}, true
	}
	start := s.pos
	for s.pos < len(s.data) && !isWhite(s.data[s.pos]) && !isDelim(s.data[s.pos]) {
		s.pos++
	}
	if s.pos == start {
		// An unhandled delimiter byte (a stray ')' in decrypted junk, say): consume it so the scan always advances —
		// the termination guarantee everything above relies on.
		s.pos++
		return token{kind: tokOther, text: string(b)}, true
	}
	word := string(s.data[start:s.pos])
	if v, err := strconv.ParseFloat(word, 64); err == nil {
		return token{kind: tokNumber, text: word, value: v}, true
	}
	return token{kind: tokKeyword, text: word}, true
}

// integer returns the next token as an integer, failing (without consuming further) when it is not a number.
func (s *scanner) integer() (int64, bool) {
	tok, ok := s.next()
	if !ok || tok.kind != tokNumber {
		return 0, false
	}
	return int64(tok.value), true
}

// numbers reads a bracketed (or braced) list of n numbers: [ n1 ... nN ] — tolerating a missing opener.
func (s *scanner) numbers(n int) ([]float64, bool) {
	out := make([]float64, 0, n)
	for range n * 2 {
		tok, ok := s.next()
		if !ok {
			return nil, false
		}
		switch tok.kind {
		case tokNumber:
			out = append(out, tok.value)
			if len(out) == n {
				return out, true
			}
		case tokOther:
			if tok.text == "[" || tok.text == "{" {
				continue
			}
			return nil, false
		default:
			return nil, false
		}
	}
	return nil, false
}

// binary consumes the RD-style binary-read token (whatever the font named it — conventionally RD or -|), exactly one
// separator byte after it, and then length raw bytes. This mirrors the PostScript definition the fonts carry ("{string
// currentfile exch readstring pop}"): readstring begins after a single whitespace byte.
func (s *scanner) binary(length int64) ([]byte, bool) {
	if length < 0 {
		return nil, false
	}
	tok, ok := s.next()
	if !ok || tok.kind != tokKeyword {
		return nil, false
	}
	if s.pos < len(s.data) { // The single separator byte (a space in every real file).
		s.pos++
	}
	if int64(len(s.data)-s.pos) < length {
		return nil, false
	}
	out := s.data[s.pos : s.pos+int(length)]
	s.pos += int(length)
	return out, true
}

// skipKeyword consumes one trailing keyword token (NP/ND after a binary read), leaving anything else in place.
func (s *scanner) skipKeyword() {
	save := s.pos
	tok, ok := s.next()
	if !ok {
		return
	}
	if tok.kind != tokKeyword {
		s.pos = save
	}
}
