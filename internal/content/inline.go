package content

import "github.com/richardwilkes/pdfview/internal/cos"

// skipInlineImage consumes a BI … ID … EI inline image (ISO 32000-2 8.9.7), leaving the lexer positioned just
// past the EI keyword. The dictionary entries between BI and ID are parsed with the ordinary operand
// machinery; the binary payload after ID is skipped by length when the dictionary supplies a usable /L (or
// /Length), and otherwise by scanning for the EI keyword delimited the way real encoders emit it. Decoding
// the image arrives with M5; at M4 the whole construct is a safe no-op whose only obligation is not to
// desynchronize the tokenizer.
func skipInlineImage(lex *cos.Lexer, data []byte) {
	dict := cos.Dict{}
	for {
		tok, ok := lex.Next()
		if !ok {
			continue
		}
		if tok.Kind == cos.TokenEOF {
			return
		}
		if tok.Kind == cos.TokenKeyword && string(tok.Bytes) == "ID" {
			break
		}
		if tok.Kind == cos.TokenName {
			key := cos.Name(tok.Bytes)
			valTok, valOK := lex.Next()
			if !valOK {
				continue
			}
			if valTok.Kind == cos.TokenEOF {
				return
			}
			if obj, objOK := parseOperand(lex, valTok, 0); objOK {
				dict[key] = obj
			}
		}
	}
	pos := lex.Pos()
	// Exactly one whitespace byte separates ID from the payload (the spec's rule; its absence is tolerated).
	if pos < len(data) && cos.IsWhitespaceByte(data[pos]) {
		pos++
	}
	// A direct /L (or /Length) skips the payload without inspection when the claimed extent lands at an EI.
	if length := inlineLength(dict); length >= 0 && pos+length <= len(data) {
		if end, ok := eiAt(data, pos+length); ok {
			lex.SetPos(end)
			return
		}
	}
	lex.SetPos(scanForEI(data, pos))
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

// eiAt reports whether the EI keyword sits at pos (after optional whitespace), returning the offset just past
// it.
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

// scanForEI finds the first plausible EI keyword at or after pos: preceded by whitespace (or the payload
// start) and followed by whitespace, a delimiter, or end of input. Binary payloads can contain the letters
// "EI", so the delimiting requirements matter; a payload byte pair that still satisfies them ends the skip
// early, which is the standard failure mode every reader shares for undeclared-length inline images. With no
// EI at all, everything to the end of input is consumed.
func scanForEI(data []byte, pos int) int {
	for i := pos; i+2 <= len(data); i++ {
		if data[i] != 'E' || data[i+1] != 'I' {
			continue
		}
		if i > pos && !cos.IsWhitespaceByte(data[i-1]) {
			continue
		}
		if i+2 == len(data) || cos.IsWhitespaceByte(data[i+2]) || cos.IsDelimiterByte(data[i+2]) {
			return i + 2
		}
	}
	return len(data)
}
