// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package font

import (
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

const testCMapContent = `%!PS-Adobe-3.0 Resource-CMap
/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CIDSystemInfo << /Registry (Test) /Ordering (Probe) /Supplement 0 >> def
/CMapName /Test-Probe def
/CMapType 1 def
/WMode 0 def
3 begincodespacerange
<00> <7f>
<8140> <9ffc>
<a0a0a0> <bfbfbf>
endcodespacerange
2 begincidrange
<20> <7e> 1
<8140> <817e> 200
endcidrange
1 begincidchar
<7f> 999
endcidchar
endcmap
CMapName currentdict /CMap defineresource pop
end
end`

func TestParseCMapCore(t *testing.T) {
	cm := parseCMap([]byte(testCMapContent), 0, predefinedCMap)
	if cm == nil {
		t.Fatal("parseCMap returned nil")
	}
	if len(cm.codespaces) != 3 || len(cm.cids) != 3 {
		t.Fatalf("codespaces=%d cids=%d, want 3 and 3", len(cm.codespaces), len(cm.cids))
	}
	if cm.wModeResolved() != 0 {
		t.Errorf("wmode = %d", cm.wModeResolved())
	}
	for _, tc := range []struct {
		in   []byte
		n    int
		code uint32
		cid  uint32
	}{
		{[]byte{0x20}, 1, 0x20, 1},                 // 1-byte codespace start
		{[]byte{0x41, 0x42}, 1, 0x41, 0x22},        // one byte consumed, next left
		{[]byte{0x7f}, 1, 0x7f, 999},               // cidchar
		{[]byte{0x81, 0x50}, 2, 0x8150, 216},       // 2-byte range: 200 + (0x8150-0x8140)
		{[]byte{0xa0, 0xa5, 0xa0}, 3, 0xa0a5a0, 0}, // 3-byte codespace, unmapped → CID 0
		// Codespace matching is integer comparison over the code value (MuPDF-compatible), not Adobe's
		// per-byte-dimension ranges: <8500> sits inside <8140>..<9ffc> as an integer.
		{[]byte{0x85, 0x00}, 2, 0x8500, 0},
		{[]byte{0xff}, 1, 0, 0}, // in no codespace at all: 1 byte consumed
	} {
		code, n := cm.nextCode(tc.in)
		if code != tc.code || n != tc.n {
			t.Errorf("nextCode(% x) = %x, %d; want %x, %d", tc.in, code, n, tc.code, tc.n)
			continue
		}
		if cid := cm.cid(code); cid != tc.cid {
			t.Errorf("cid(%x) = %d, want %d", code, cid, tc.cid)
		}
	}
}

func TestNextCodePartialMatch(t *testing.T) {
	// Two overlapping codespaces whose first-byte ranges both bracket 0x41, but the 3-byte one matches a longer prefix
	// of the input. ISO 32000-2 9.7.6.3 consumes the longest-matching codespace's length, not the shortest bracketing.
	content := `begincmap
2 begincodespacerange
<4180> <41ff>
<414141> <7e7e7e>
endcodespacerange
endcmap`
	cm := parseCMap([]byte(content), 0, nil)
	if cm == nil {
		t.Fatal("parseCMap returned nil")
	}
	for _, tc := range []struct {
		in   []byte
		n    int
		code uint32
	}{
		{[]byte{0x41, 0x41, 0x00}, 3, 0}, // invalid: 3-byte codespace matches 2 leading bytes → consume its 3
		{[]byte{0x41, 0x30}, 2, 0},       // tie on 1-byte prefix → shortest (2-byte) codespace consumed
		{[]byte{0x41, 0x90}, 2, 0x4190},  // full 2-byte match
		{[]byte{0xff}, 1, 0},             // no codespace brackets the first byte → one byte
	} {
		code, n := cm.nextCode(tc.in)
		if code != tc.code || n != tc.n {
			t.Errorf("nextCode(% x) = %x, %d; want %x, %d", tc.in, code, n, tc.code, tc.n)
		}
	}
}

func TestPredefinedIdentity(t *testing.T) {
	h := predefinedCMap("Identity-H")
	v := predefinedCMap("Identity-V")
	if h == nil || v == nil {
		t.Fatal("identity CMaps missing")
	}
	if h.wModeResolved() != 0 || v.wModeResolved() != 1 {
		t.Errorf("wmodes = %d, %d", h.wModeResolved(), v.wModeResolved())
	}
	code, n := h.nextCode([]byte{0x12, 0x34, 0x56})
	if code != 0x1234 || n != 2 {
		t.Errorf("nextCode = %x, %d", code, n)
	}
	if cid := h.cid(0x1234); cid != 0x1234 {
		t.Errorf("cid = %d", cid)
	}
	if predefinedCMap("UniJIS-UCS2-H") != nil {
		t.Error("unexpected predefined CMap")
	}
}

func TestUseCMapChain(t *testing.T) {
	content := `/Identity-H usecmap
1 begincidrange
<0041> <0042> 500
endcidrange`
	cm := parseCMap([]byte(content), 0, predefinedCMap)
	if cm == nil || cm.base == nil {
		t.Fatal("usecmap base not resolved")
	}
	if cid := cm.cid(0x41); cid != 500 { // Own range wins.
		t.Errorf("cid(41) = %d, want 500", cid)
	}
	if cid := cm.cid(0x99); cid != 0x99 { // Falls through to the identity base.
		t.Errorf("cid(99) = %d, want 153", cid)
	}
	if code, n := cm.nextCode([]byte{0x00, 0x99}); code != 0x99 || n != 2 { // Base codespace applies.
		t.Errorf("nextCode = %x, %d", code, n)
	}
}

const testToUnicodeContent = `/CIDInit /ProcSet findresource begin
begincmap
1 begincodespacerange
<0000> <ffff>
endcodespacerange
2 beginbfchar
<0003> <0020>
<000f> <2074>
endbfchar
2 beginbfrange
<0010> <0012> <0041>
<0020> <0022> [<00660066> <D835DC00> <0031>]
endbfrange
endcmap
end`

func TestToUnicodeBF(t *testing.T) {
	cm := parseCMap([]byte(testToUnicodeContent), 0, nil)
	if cm == nil {
		t.Fatal("parseCMap returned nil")
	}
	for code, want := range map[uint32]string{
		0x03: " ",
		0x0f: "⁴",
		0x10: "A", 0x11: "B", 0x12: "C", // bfrange increments the last code unit.
		0x20: "ff",         // multi-unit target
		0x21: "\U0001D400", // surrogate pair
		0x22: "1",
		0x99: "", // unmapped
	} {
		if got := cm.bfString(code); got != want {
			t.Errorf("bfString(%#x) = %q, want %q", code, got, want)
		}
	}
}

func TestWMode1(t *testing.T) {
	cm := parseCMap([]byte("/WMode 1 def\n1 begincodespacerange <00> <ff> endcodespacerange"), 0, nil)
	if cm == nil || cm.wModeResolved() != 1 {
		t.Fatalf("WMode not parsed: %+v", cm)
	}
}

func TestParseWArrays(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF("<< >>")))
	if err != nil {
		t.Fatal(err)
	}
	arr := cos.Array{
		cos.Integer(1),
		cos.Array{cos.Integer(500), cos.Integer(600), cos.Real(750.5)},
		cos.Integer(10), cos.Integer(20), cos.Integer(1000),
	}
	info := &type0Info{dw: 1}
	info.w = parseWArray(d, arr)
	for cid, want := range map[uint32]float32{
		1: 0.5, 2: 0.6, 3: 0.7505,
		10: 1, 15: 1, 20: 1,
		0: 1, 4: 1, 99: 1, // /DW
	} {
		if got := info.cidWidth(cid); got != want {
			t.Errorf("cidWidth(%d) = %v, want %v", cid, got, want)
		}
	}
	w2 := cos.Array{
		cos.Integer(5),
		cos.Array{cos.Integer(-900), cos.Integer(400), cos.Integer(880)},
		cos.Integer(7), cos.Integer(9), cos.Integer(-1100), cos.Integer(300), cos.Integer(900),
	}
	info.w2 = parseW2Array(d, w2)
	info.dw2 = [2]float32{0.88, -1}
	if w1, vx, vy := info.cidVMetrics(5, 0.5); w1 != -0.9 || vx != 0.4 || vy != 0.88 {
		t.Errorf("cidVMetrics(5) = %v %v %v", w1, vx, vy)
	}
	if w1, vx, vy := info.cidVMetrics(8, 0.5); w1 != -1.1 || vx != 0.3 || vy != 0.9 {
		t.Errorf("cidVMetrics(8) = %v %v %v", w1, vx, vy)
	}
	if w1, vx, vy := info.cidVMetrics(99, 0.5); w1 != -1 || vx != 0.25 || vy != 0.88 { // Defaults.
		t.Errorf("cidVMetrics(99) = %v %v %v", w1, vx, vy)
	}

	// A /W whose starting CID exceeds the 16-bit CID space (here 2^33) must be rejected, not narrowed via uint32 to a
	// small wrapped CID that keys the widths to the wrong range.
	overflow := cos.Array{
		cos.Integer(1 << 33),
		cos.Array{cos.Real(500)},
		cos.Integer(3),
		cos.Array{cos.Real(750)}, // A valid entry after the bad one still parses.
	}
	over := &type0Info{dw: 1, w: parseWArray(d, overflow)}
	for cid, want := range map[uint32]float32{
		0: 1,    // uint32(1<<33) == 0: the wrapped low bits must NOT have picked up the width.
		3: 0.75, // The valid entry following the rejected one still applies.
	} {
		if got := over.cidWidth(cid); got != want {
			t.Errorf("overflow cidWidth(%d) = %v, want %v", cid, got, want)
		}
	}
}

func TestCFFCIDCharset(t *testing.T) {
	// A synthetic charset: format 1, GIDs 1.. mapping to CID ranges {100..102, 500}. CharStrings INDEX with 5 entries
	// (nGlyphs = 5), each 1 byte.
	data := make([]byte, 64)
	// CharStrings INDEX at offset 8: count=5, offSize=1, offsets 1..6, data 5 bytes.
	copy(data[8:], []byte{0, 5, 1, 1, 2, 3, 4, 5, 6, 14, 14, 14, 14, 14})
	// charset at offset 32: format 1: {first=100, nLeft=2}, {first=500, nLeft=0}.
	copy(data[32:], []byte{1, 0, 100, 2, 1, 244, 0})
	top := &cffTop{isCID: true, charsetOff: 32, charStringsOff: 8}
	cid := parseCFFCharsetCID(data, top)
	if cid == nil {
		t.Fatal("parseCFFCharsetCID returned nil")
	}
	for cidVal, gid := range map[uint32]uint32{0: 0, 100: 1, 101: 2, 102: 3, 500: 4, 7: 0} {
		if got := cid.gid(cidVal); got != gid {
			t.Errorf("gid(%d) = %d, want %d", cidVal, got, gid)
		}
	}
	// Predefined charset degrades to identity.
	top2 := &cffTop{isCID: true, charsetOff: 0, charStringsOff: 8}
	cid2 := parseCFFCharsetCID(data, top2)
	if cid2 == nil || !cid2.identity || cid2.gid(3) != 3 || cid2.gid(99) != 0 {
		t.Errorf("predefined charset not identity: %+v", cid2)
	}
}
