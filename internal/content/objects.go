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

// maxNesting caps array/dictionary nesting inside content-stream operands, mirroring the COS parser's own
// container cap.
const maxNesting = 512

// parseOperand assembles one operand object whose first token has already been read. Content streams carry
// only direct objects — "R" is not an operator there, so integers are never reference lookahead candidates —
// which is why this small assembler exists alongside the COS object parser. Malformed containers are returned
// as parsed so far (leniency; the enclosing operator will typically be skipped anyway), with ok reporting
// whether the value is usable at all.
func parseOperand(lex *cos.Lexer, tok cos.Token, depth int) (obj cos.Object, ok bool) {
	switch tok.Kind {
	case cos.TokenInt:
		return cos.Integer(tok.Int), true
	case cos.TokenReal:
		return cos.Real(tok.Real), true
	case cos.TokenString:
		return cos.String(tok.Bytes), true
	case cos.TokenName:
		return cos.Name(tok.Bytes), true
	case cos.TokenArrayOpen:
		if depth >= maxNesting {
			return nil, false
		}
		return parseArray(lex, depth+1), true
	case cos.TokenDictOpen:
		if depth >= maxNesting {
			return nil, false
		}
		return parseDict(lex, depth+1), true
	case cos.TokenKeyword:
		switch string(tok.Bytes) {
		case "true":
			return cos.Boolean(true), true
		case "false":
			return cos.Boolean(false), true
		case "null":
			return cos.Null{}, true
		default:
			return nil, false
		}
	default:
		return nil, false
	}
}

// parseArray parses to the closing bracket (or end of input).
func parseArray(lex *cos.Lexer, depth int) cos.Array {
	arr := cos.Array{}
	for {
		tok, ok := lex.Next()
		if !ok {
			continue // Lexical error: the lexer advanced; skip the fragment.
		}
		switch tok.Kind {
		case cos.TokenArrayClose, cos.TokenEOF:
			return arr
		default:
			if obj, objOK := parseOperand(lex, tok, depth); objOK {
				arr = append(arr, obj)
			}
		}
	}
}

// parseDict parses to the closing >> (or end of input). Non-name keys are skipped along with their values'
// tokens as encountered (leniency).
func parseDict(lex *cos.Lexer, depth int) cos.Dict {
	dict := cos.Dict{}
	for {
		tok, ok := lex.Next()
		if !ok {
			continue
		}
		switch tok.Kind {
		case cos.TokenDictClose, cos.TokenEOF:
			return dict
		case cos.TokenName:
			key := cos.Name(tok.Bytes)
			valTok, valOK := lex.Next()
			if !valOK {
				continue
			}
			if valTok.Kind == cos.TokenDictClose || valTok.Kind == cos.TokenEOF {
				return dict
			}
			if obj, objOK := parseOperand(lex, valTok, depth); objOK {
				dict[key] = obj
			}
		default:
			// Skip stray non-name keys.
		}
	}
}
