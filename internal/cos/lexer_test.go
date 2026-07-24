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
	"math"
	"strings"
	"testing"
	"unicode/utf8"
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
		// Integers above the old (1<<62)/10 guard but within int64 must stay exact tkInt, not be perturbed as tkReal.
		{"922337203685477580", token{kind: tkInt, i: 922337203685477580}},
		{"5000000000000000000", token{kind: tkInt, i: 5000000000000000000}},
		{"9223372036854775807", token{kind: tkInt, i: 9223372036854775807}}, // math.MaxInt64
		{"-9223372036854775807", token{kind: tkInt, i: -9223372036854775807}},
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
	// A zero-length payload whose post-stream EOL directly abuts endstream must not underflow the EOL trim (found by
	// FuzzOpen; the input is preserved under testdata/fuzz).
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
	// The value 42 requires lookahead that pushes tokens back; the reported end must still point before the next
	// object's header.
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
		{"utf8 bom multibyte", "é€", []byte{0xef, 0xbb, 0xbf, 0xc3, 0xa9, 0xe2, 0x82, 0xac}},
		// A UTF-8 byte-order mark does not make the rest valid UTF-8: undecodable bytes must map to U+FFFD rather than
		// surviving raw in the returned string.
		{"utf8 bom invalid byte", "o�k", []byte{0xef, 0xbb, 0xbf, 'o', 0xff, 'k'}},
		{"utf8 bom truncated sequence", "e�", []byte{0xef, 0xbb, 0xbf, 'e', 0xe2, 0x82}},
		{"utf8 bom bare continuation", "�a", []byte{0xef, 0xbb, 0xbf, 0xa9, 'a'}},
		{"utf8 bom only", "", []byte{0xef, 0xbb, 0xbf}},
		{"pdfdoc ascii", "plain", []byte("plain")},
		{"pdfdoc specials", "ﬁ€–", []byte{0x93, 0xa0, 0x85}},
		{"pdfdoc latin1", "é", []byte{0xe9}},
		{"pdfdoc undefined", "�", []byte{0x9f}},
	} {
		got := DecodeTextString(String(tc.in))
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: result is not valid UTF-8: % x", tc.name, got)
		}
	}
}

func TestLexLargeIntegerOverflow(t *testing.T) {
	// A magnitude beyond int64 overflows and is returned as a real rather than a truncated integer.
	tokens := lexAll(t, "99999999999999999999999")
	if len(tokens) != 1 || tokens[0].kind != tkReal {
		t.Fatalf("overflowing integer lexed to %#v", tokens)
	}
	if tokens[0].f < 9.9e22 || tokens[0].f > 1.1e23 {
		t.Errorf("overflow real = %g, want ~1e23", tokens[0].f)
	}
}

func TestSetPosClamps(t *testing.T) {
	data := []byte("0123456789")
	l := NewLexer(data, 0)
	l.SetPos(-5)
	if l.Pos() != 0 {
		t.Errorf("negative offset clamped to %d, want 0", l.Pos())
	}
	l.SetPos(4)
	if l.Pos() != 4 {
		t.Errorf("in-range offset became %d, want 4", l.Pos())
	}
	l.SetPos(len(data) + 100)
	if l.Pos() != len(data) {
		t.Errorf("past-end offset clamped to %d, want %d", l.Pos(), len(data))
	}
}

func TestLoadObjStmSelfReference(t *testing.T) {
	// Object stream 1's /N is an indirect reference to object 2, whose cross-reference entry points back into stream 1.
	// Resolving /N therefore re-enters loadObjStm(1); without the recursion guard this exhausts the goroutine stack.
	// Reaching an ordinary error return proves the guard broke the cycle.
	data := []byte("1 0 obj\n<< /Type /ObjStm /N 2 0 R /First 4 >>\nstream\n2 0\nendstream\nendobj\n")
	d := &Document{
		data: data,
		xref: map[int]xrefEntry{
			1: {kind: xrefInFile, offset: 0},
			2: {kind: xrefInStream, stmNum: 1, stmIdx: 0},
		},
		objCache:      map[int]Object{},
		objStms:       map[int]*objStm{},
		objStmLoading: map[int]bool{},
		repaired:      true, // Suppress the load-failure repair retry so the test exercises only the recursion guard.
	}
	if _, err := d.loadObjStm(1); err == nil {
		t.Fatal("expected an error from the self-referential object stream")
	}
	if len(d.objStmLoading) != 0 {
		t.Errorf("objStmLoading not cleaned up: %v", d.objStmLoading)
	}
}

func TestLoadObjStmDepthCap(t *testing.T) {
	// The per-number cycle guard only stops a single stream from re-entering itself; a straight chain of distinct
	// object streams recurses without repeating any number, so only the nesting-depth cap keeps it from exhausting the
	// goroutine stack. Simulate maxObjStmDepth streams already under load and confirm the next entry is refused.
	d := &Document{
		xref:          map[int]xrefEntry{},
		objCache:      map[int]Object{},
		objStms:       map[int]*objStm{},
		objStmLoading: map[int]bool{},
	}
	for i := range maxObjStmDepth {
		d.objStmLoading[-(i + 1)] = true
	}
	if _, err := d.loadObjStm(999); !errors.Is(err, errObjStmDepth) {
		t.Fatalf("at max nesting depth: got %v, want errObjStmDepth", err)
	}
	// One level shallower the guard must not fire, so loadObjStm proceeds and fails later for another reason (here the
	// missing xref entry for object 999), never returning errObjStmDepth.
	delete(d.objStmLoading, -1)
	if _, err := d.loadObjStm(999); errors.Is(err, errObjStmDepth) {
		t.Fatal("depth guard fired below the cap")
	}
}

func TestLoadObjStmChainTerminates(t *testing.T) {
	// Build a straight chain of distinct object streams: stream s's /N is an indirect reference to object 100+s, whose
	// cross-reference entry stores it inside stream s+1. Resolving each /N therefore recurses into the next stream via
	// loadObjStm with a fresh number every time, defeating the cycle guard. The chain is longer than maxObjStmDepth, so
	// the depth cap must break the recursion; the load returns an ordinary error and leaves objStmLoading clean.
	const chain = maxObjStmDepth + 10
	var buf bytes.Buffer
	xref := map[int]xrefEntry{}
	for s := 1; s <= chain; s++ {
		xref[s] = xrefEntry{kind: xrefInFile, offset: int64(buf.Len())}
		xref[100+s] = xrefEntry{kind: xrefInStream, stmNum: s + 1, stmIdx: 0}
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Type /ObjStm /N %d 0 R /First 4 >>\nstream\n0 0\nendstream\nendobj\n",
			s, 100+s)
	}
	d := &Document{
		data:          buf.Bytes(),
		xref:          xref,
		objCache:      map[int]Object{},
		objStms:       map[int]*objStm{},
		objStmLoading: map[int]bool{},
		repaired:      true, // Suppress the load-failure repair retry so the test exercises only the recursion path.
	}
	if _, err := d.loadObjStm(1); err == nil {
		t.Fatal("expected an error from the deep object-stream chain")
	}
	if len(d.objStmLoading) != 0 {
		t.Errorf("objStmLoading not cleaned up: %v", d.objStmLoading)
	}
}

func TestCaptureRawStreamEndstreamLimit(t *testing.T) {
	dict := Dict{}
	data := []byte("stream\nabcde\nendstream rest")
	pos := len("stream\n")
	// With the limit at end of buffer, the fallback scan finds endstream.
	raw, _, err := captureRawStream(data, pos, len(data), dict)
	if err != nil || !bytes.Equal(raw, []byte("abcde")) {
		t.Fatalf("full-limit capture: raw=%q err=%v", raw, err)
	}
	// A limit before the endstream keyword hides it, so the scan reports the miss without running past the bound.
	if _, _, err = captureRawStream(data, pos, pos+3, dict); err == nil {
		t.Error("expected errNoEndstream when the keyword is beyond the limit")
	}
	// A start offset at or beyond the limit fails immediately.
	if _, _, err = captureRawStream(data, len(data), 0, dict); err == nil {
		t.Error("expected errNoEndstream when pos is beyond the limit")
	}
}

func TestAsIntRealRange(t *testing.T) {
	// Finite reals within the int64 range truncate toward zero.
	for _, tc := range []struct {
		in   Real
		want int64
	}{
		{Real(3.9), 3},
		{Real(-3.9), -3},
		{Real(0), 0},
		// 2^63-1024 is the largest float64 strictly below 2^63; its truncation still fits in int64.
		{Real(9223372036854774784), 9223372036854774784},
		{Real(math.MinInt64), math.MinInt64},
	} {
		if got, ok := AsInt(tc.in); !ok || got != tc.want {
			t.Errorf("AsInt(%v) = (%d, %v), want (%d, true)", float64(tc.in), got, ok, tc.want)
		}
	}
	// Non-finite reals and reals whose magnitude exceeds the int64 range report false rather than performing the
	// implementation-defined out-of-range float→int conversion that would break cross-architecture determinism.
	for _, in := range []Real{
		Real(math.NaN()),
		Real(math.Inf(1)),
		Real(math.Inf(-1)),
		Real(9223372036854775808), // 2^63, just past math.MaxInt64
		Real(1e300),
		Real(-1e300),
	} {
		if got, ok := AsInt(in); ok {
			t.Errorf("AsInt(%v) = (%d, true), want ok=false", float64(in), got)
		}
	}
}

func TestCaptureRawStreamHugeLength(t *testing.T) {
	// A large-but-valid Integer /Length near math.MaxInt64 used to overflow the pos+length bound to a negative value,
	// pass the guard, and then index the buffer with a negative offset. The overflow-safe bound must reject it and fall
	// back to the endstream scan instead of panicking.
	dict := Dict{"Length": Integer(9223372036854775800)}
	data := []byte("stream\nabcde\nendstream rest")
	pos := len("stream\n")
	raw, _, err := captureRawStream(data, pos, len(data), dict)
	if err != nil || !bytes.Equal(raw, []byte("abcde")) {
		t.Fatalf("huge-length capture: raw=%q err=%v", raw, err)
	}
}

func TestRepairManyBareStreams(t *testing.T) {
	// Many "N 0 obj << >> stream" headers with no endstream anywhere used to cost O(n²): each header scanned to EOF.
	// The repair endstream bound makes each miss constant-time. This checks the pathological input still repairs into a
	// usable document (with a real trailer/catalog appended) rather than hanging.
	var b bytes.Buffer
	for i := 2; i < 4000; i++ {
		fmt.Fprintf(&b, "%d 0 obj\n<< /Length 10 >>\nstream\n", i)
	}
	b.WriteString("1 0 obj << /Type /Catalog /Pages 5 0 R >> endobj\n")
	b.WriteString("5 0 obj << /Type /Pages /Kids [] /Count 0 >> endobj\n")
	b.WriteString("trailer << /Root 1 0 R >>\n")
	d, err := Open(b.Bytes())
	if err != nil {
		t.Fatalf("Open of bare-stream file failed: %v", err)
	}
	if _, ok := d.GetDict(d.Trailer(), "Root"); !ok {
		t.Error("repaired document has no usable root")
	}
}
