// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package content

import "github.com/richardwilkes/pdfview/internal/cos"

// opInlineImage consumes a BI … ID … EI inline image (ISO 32000-2 8.9.7), decodes it, draws it, and leaves the lexer
// positioned just past the EI keyword. The dictionary entries between BI and ID are parsed with the ordinary operand
// machinery; the binary payload after ID is isolated by length when the dictionary supplies a usable /L (or /Length),
// and otherwise by scanning for the EI keyword delimited the way real encoders emit it. Malformed constructs degrade to
// drawing nothing; the only hard obligation is not to desynchronize the tokenizer.
func (in *interp) opInlineImage(lex *cos.Lexer, data []byte) {
	dict, ok := parseInlineDict(lex)
	if !ok {
		return
	}
	pos := lex.Pos()
	// Exactly one whitespace byte separates ID from the payload (the spec's rule; its absence is tolerated).
	if pos < len(data) && cos.IsWhitespaceByte(data[pos]) {
		pos++
	}
	payload, end := isolatePayload(dict, data, pos)
	lex.SetPos(end)
	img, err := in.decodeInline(dict, payload)
	if err != nil {
		return
	}
	in.drawImage(img)
}

// parseInlineDict parses the key/value entries between BI and ID. It reports false when the stream ends before ID
// arrives (nothing to draw, nothing left to position past).
func parseInlineDict(lex *cos.Lexer) (cos.Dict, bool) {
	dict := cos.Dict{}
	for {
		tok, ok := lex.Next()
		if !ok {
			continue
		}
		if tok.Kind == cos.TokenEOF {
			return nil, false
		}
		if tok.Kind == cos.TokenKeyword && string(tok.Bytes) == "ID" {
			return dict, true
		}
		if tok.Kind == cos.TokenName {
			key := cos.Name(tok.Bytes)
			valTok, valOK := lex.Next()
			if !valOK {
				continue
			}
			if valTok.Kind == cos.TokenEOF {
				return nil, false
			}
			if obj, objOK := parseOperand(lex, valTok, 0); objOK {
				dict[key] = obj
			}
		}
	}
}

// isolatePayload returns the payload bytes starting at pos and the offset just past the terminating EI. A direct /L (or
// /Length) delimits the payload without inspection when the claimed extent lands at an EI; otherwise the payload ends
// at the first plausible EI keyword, with the single separating whitespace byte before it excluded.
func isolatePayload(dict cos.Dict, data []byte, pos int) (payload []byte, end int) {
	if length := inlineLength(dict); length >= 0 && pos+length <= len(data) {
		if eiEnd, ok := eiAt(data, pos+length); ok {
			return data[pos : pos+length], eiEnd
		}
	}
	payloadEnd, end := scanForEI(data, pos)
	return data[pos:payloadEnd], end
}

// inlineLength returns the payload length claimed by /L or /Length, or -1.
func inlineLength(dict cos.Dict) int {
	for _, key := range []cos.Name{"L", "Length"} {
		if v, ok := cos.AsInt(dict[key]); ok && v >= 0 && v < 1<<31 {
			return int(v)
		}
	}
	return -1
}

// eiAt reports whether the EI keyword sits at pos (after optional whitespace), returning the offset just past it.
func eiAt(data []byte, pos int) (end int, ok bool) {
	for pos < len(data) && cos.IsWhitespaceByte(data[pos]) {
		pos++
	}
	if pos+2 <= len(data) && data[pos] == 'E' && data[pos+1] == 'I' &&
		(pos+2 == len(data) || cos.IsWhitespaceByte(data[pos+2]) || cos.IsDelimiterByte(data[pos+2])) {
		return pos + 2, true
	}
	return 0, false
}

// scanForEI finds the first plausible EI keyword at or after pos: preceded by whitespace (or the payload start) and
// followed by whitespace, a delimiter, or end of input. Binary payloads can contain the letters "EI", so the delimiting
// requirements matter; a payload byte pair that still satisfies them ends the payload early, which is the standard
// failure mode every reader shares for undeclared-length inline images. With no EI at all, everything to the end of
// input is consumed. The returned payloadEnd excludes the single whitespace byte separating the payload from the EI
// keyword; end is just past the keyword.
func scanForEI(data []byte, pos int) (payloadEnd, end int) {
	for i := pos; i+2 <= len(data); i++ {
		if data[i] != 'E' || data[i+1] != 'I' {
			continue
		}
		if i > pos && !cos.IsWhitespaceByte(data[i-1]) {
			continue
		}
		if i+2 == len(data) || cos.IsWhitespaceByte(data[i+2]) || cos.IsDelimiterByte(data[i+2]) {
			payloadEnd = i
			if i > pos {
				payloadEnd = i - 1 // Exclude the separator byte the match required.
			}
			return payloadEnd, i + 2
		}
	}
	return len(data), len(data)
}
