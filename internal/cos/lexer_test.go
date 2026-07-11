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
	"math"
	"strings"
	"testing"
)

func lexAll(t *testing.T, src string) []token {
	t.Helper()
	l := lexer{data: []byte(src)}
	var tokens []token
	for {
		tok, err := l.next()
		if err != nil {
			t.Fatalf("lex error in %q: %v", src, err)
		}
		if tok.kind == tkEOF {
			return tokens
		}
		tokens = append(tokens, tok)
	}
}

func TestLexNumbers(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want token
	}{
		{"0", token{kind: tkInt, i: 0}},
		{"42", token{kind: tkInt, i: 42}},
		{"+17", token{kind: tkInt, i: 17}},
		{"-98", token{kind: tkInt, i: -98}},
		{"34.5", token{kind: tkReal, f: 34.5}},
		{"-3.62", token{kind: tkReal, f: -3.62}},
		{"+123.6", token{kind: tkReal, f: 123.6}},
		{"4.", token{kind: tkReal, f: 4}},
		{"-.002", token{kind: tkReal, f: -0.002}},
		{".5", token{kind: tkReal, f: 0.5}},
		{"--5", token{kind: tkInt, i: 5}}, // Lenient: doubled signs cancel.
	} {
		tokens := lexAll(t, tc.src)
		if len(tokens) != 1 {
			t.Errorf("%q lexed to %d tokens", tc.src, len(tokens))
			continue
		}
		got := tokens[0]
		if got.kind != tc.want.kind || got.i != tc.want.i || math.Abs(got.f-tc.want.f) > 1e-9 {
			t.Errorf("%q = kind %d i %d f %g, want kind %d i %d f %g",
				tc.src, got.kind, got.i, got.f, tc.want.kind, tc.want.i, tc.want.f)
		}
	}
}

func TestLexNames(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{"/Name1", "Name1"},
		{"/A;Name_With-Various***Characters?", "A;Name_With-Various***Characters?"},
		{"/paired#28#29parentheses", "paired()parentheses"},
		{"/A#42", "AB"},
		{"/", ""},
		{"/lime#20Green", "lime Green"},
		{"/bad#escape", "bad#escape"}, // Invalid escape keeps the # literally.
	} {
		tokens := lexAll(t, tc.src)
		if len(tokens) != 1 || tokens[0].kind != tkName || string(tokens[0].s) != tc.want {
			t.Errorf("%q: got %q, want %q", tc.src, tokens[0].s, tc.want)
		}
	}
}

func TestLexLiteralStrings(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{"(simple)", "simple"},
		{"(balanced (nested) parens)", "balanced (nested) parens"},
		{`(escapes: \n\r\t\b\f\(\)\\)`, "escapes: \n\r\t\b\f()\\"},
		{`(octal \053 and \53)`, "octal + and +"},
		{`(three digit \101\102)`, "three digit AB"},
		{"(line\r\nbreaks\rnormalize\n)", "line\nbreaks\nnormalize\n"},
		{"(continuation \\\r\nline)", "continuation line"},
		{`(unknown \q escape)`, "unknown q escape"},
		{"()", ""},
	} {
		tokens := lexAll(t, tc.src)
		if len(tokens) != 1 || tokens[0].kind != tkString || string(tokens[0].s) != tc.want {
			t.Errorf("%q: got %q, want %q", tc.src, tokens[0].s, tc.want)
		}
	}
}

func TestLexHexStrings(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{"<48656C6C6F>", "Hello"},
		{"<48 65 6c 6c 6f>", "Hello"},
		{"<7>", "p"}, // Odd digit count implies a trailing 0.
		{"<>", ""},
	} {
		tokens := lexAll(t, tc.src)
		if len(tokens) != 1 || tokens[0].kind != tkString || string(tokens[0].s) != tc.want {
			t.Errorf("%q: got %q, want %q", tc.src, tokens[0].s, tc.want)
		}
	}
}

func TestLexUnterminatedStringErrors(t *testing.T) {
	for _, src := range []string{"(never ends", "(unbalanced (paren)", "<48656C"} {
		l := lexer{data: []byte(src)}
		if _, err := l.next(); err == nil {
			t.Errorf("%q: expected an error", src)
		}
	}
}

func TestLexComments(t *testing.T) {
	tokens := lexAll(t, "abc% comment ( /% blah blah blah\n123")
	if len(tokens) != 2 || tokens[0].kind != tkKeyword || string(tokens[0].s) != "abc" ||
		tokens[1].kind != tkInt || tokens[1].i != 123 {
		t.Errorf("comment handling produced %+v", tokens)
	}
}

func parseOne(t *testing.T, src string) Object {
	t.Helper()
	p := newParser([]byte(src), 0)
	obj, err := p.parseObject()
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return obj
}

func TestParseObjects(t *testing.T) {
	if obj := parseOne(t, "<< /Type /Page /Count 3 /Parent 2 0 R >>"); obj != nil {
		dict, ok := obj.(Dict)
		if !ok {
			t.Fatalf("expected dict, got %T", obj)
		}
		if typ, _ := AsName(dict["Type"]); typ != "Page" {
			t.Errorf("Type = %q", typ)
		}
		if count, _ := AsInt(dict["Count"]); count != 3 {
			t.Errorf("Count = %d", count)
		}
		if ref, ok2 := dict["Parent"].(Ref); !ok2 || ref.Num != 2 || ref.Gen != 0 {
			t.Errorf("Parent = %#v", dict["Parent"])
		}
	}
	arr, ok := parseOne(t, "[1 2.5 (str) /Nm true false null 6 0 R [0 0]]").(Array)
	if !ok || len(arr) != 9 {
		t.Fatalf("array parse failed: %#v", arr)
	}
	if _, isNull := arr[6].(Null); !isNull {
		t.Errorf("expected null at index 6, got %#v", arr[6])
	}
	if ref, ok2 := arr[7].(Ref); !ok2 || ref.Num != 6 {
		t.Errorf("expected ref at index 7, got %#v", arr[7])
	}
	// Two integers NOT followed by R stay separate integers.
	arr, ok = parseOne(t, "[1 2 3]").(Array)
	if !ok || len(arr) != 3 {
		t.Fatalf("integer array misparsed: %#v", arr)
	}
}

func TestParseNestingLimit(t *testing.T) {
	deep := strings.Repeat("[", maxNestingDepth+1) + strings.Repeat("]", maxNestingDepth+1)
	p := newParser([]byte(deep), 0)
	if _, err := p.parseObject(); err == nil {
		t.Error("expected a nesting-depth error")
	}
}

func TestParseIndirectWithStream(t *testing.T) {
	src := []byte("7 0 obj\n<< /Length 5 >>\nstream\nabcde\nendstream\nendobj\n")
	obj, _, end, err := parseIndirectAt(src, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	stream, ok := obj.(*Stream)
	if !ok {
		t.Fatalf("expected stream, got %T", obj)
	}
	if !bytes.Equal(stream.Raw, []byte("abcde")) {
		t.Errorf("Raw = %q", stream.Raw)
	}
	if !bytes.HasPrefix(src[end:], []byte("\nendobj")) {
		t.Errorf("end offset %d is wrong", end)
	}
	// A wrong /Length falls back to scanning for endstream.
	src = []byte("7 0 obj\n<< /Length 9999 >>\nstream\nabcde\nendstream\nendobj\n")
	obj, _, _, err = parseIndirectAt(src, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if stream, ok = obj.(*Stream); !ok || !bytes.Equal(stream.Raw, []byte("abcde")) {
		t.Errorf("wrong-length stream Raw = %q", stream.Raw)
	}
	// An indirect /Length uses the same scan.
	src = []byte("7 0 obj << /Length 8 0 R >> stream\r\nxyz\nendstream endobj")
	obj, _, _, err = parseIndirectAt(src, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if stream, ok = obj.(*Stream); !ok || !bytes.Equal(stream.Raw, []byte("xyz")) {
		t.Errorf("indirect-length stream Raw = %q", stream.Raw)
	}
	// The wrong object number must be detected.
	if _, _, _, err = parseIndirectAt([]byte("3 0 obj null endobj"), 0, 7); err == nil {
		t.Error("expected a wrong-object-number error")
	}
}

func TestParseIndirectEmptyStream(t *testing.T) {
	// A zero-length payload whose post-stream EOL directly abuts endstream must not underflow the EOL trim
	// (found by FuzzOpen; the input is preserved under testdata/fuzz).
	for _, src := range []string{
		"0 0obj<<>>stream\nendstream0",
		"1 0 obj << >> stream\nendstream endobj",
		"1 0 obj << >> stream\r\nendstream endobj",
	} {
		obj, _, _, err := parseIndirectAt([]byte(src), 0, -1)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		stream, ok := obj.(*Stream)
		if !ok {
			t.Fatalf("%q: expected a stream, got %T", src, obj)
		}
		if len(stream.Raw) != 0 {
			t.Errorf("%q: Raw = %q, want empty", src, stream.Raw)
		}
	}
}

func TestParseIndirectEndPosition(t *testing.T) {
	// The value 42 requires lookahead that pushes tokens back; the reported end must still point before the
	// next object's header.
	src := []byte("5 0 obj 42\n6 0 obj null endobj")
	obj, _, end, err := parseIndirectAt(src, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := AsInt(obj); v != 42 {
		t.Fatalf("object = %#v", obj)
	}
	if !bytes.HasPrefix(src[end:], []byte("6 0 obj")) {
		t.Errorf("end offset %d lands at %q", end, src[end:])
	}
}

func TestDecodeTextString(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		in   []byte
	}{
		{"utf16be", "Hi€", []byte{0xfe, 0xff, 0x00, 'H', 0x00, 'i', 0x20, 0xac}},
		{"utf16be surrogate", "\U0001f600", []byte{0xfe, 0xff, 0xd8, 0x3d, 0xde, 0x00}},
		{"utf8 bom", "ok", []byte{0xef, 0xbb, 0xbf, 'o', 'k'}},
		{"pdfdoc ascii", "plain", []byte("plain")},
		{"pdfdoc specials", "ﬁ€–", []byte{0x93, 0xa0, 0x85}},
		{"pdfdoc latin1", "é", []byte{0xe9}},
		{"pdfdoc undefined", "�", []byte{0x9f}},
	} {
		if got := DecodeTextString(String(tc.in)); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
