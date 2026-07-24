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
	"fmt"
	"strings"
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

	// The same bound applies to a /W2 range end: 2^32+100 must be rejected rather than narrowed via uint32 to 100,
	// which would otherwise put a bogus [0, 100] range ahead of the real entry that cidVMetrics scans for.
	w2Overflow := cos.Array{
		cos.Integer(0), cos.Integer(1<<32 + 100), cos.Integer(-1100), cos.Integer(300), cos.Integer(900),
		cos.Integer(50),
		cos.Array{cos.Integer(-900), cos.Integer(400), cos.Integer(880)},
	}
	w2Over := &type0Info{dw2: [2]float32{0.88, -1}, w2: parseW2Array(d, w2Overflow)}
	if len(w2Over.w2) != 1 || w2Over.w2[0].lo != 50 || w2Over.w2[0].hi != 50 {
		t.Fatalf("overflow /W2 ranges = %+v, want only the CID 50 entry", w2Over.w2)
	}
	if w1, vx, vy := w2Over.cidVMetrics(50, 0.5); w1 != -0.9 || vx != 0.4 || vy != 0.88 {
		t.Errorf("overflow cidVMetrics(50) = %v %v %v, want -0.9 0.4 0.88", w1, vx, vy)
	}
	if w1, vx, vy := w2Over.cidVMetrics(10, 0.5); w1 != -1 || vx != 0.25 || vy != 0.88 { // Defaults, not the bad range.
		t.Errorf("overflow cidVMetrics(10) = %v %v %v, want defaults", w1, vx, vy)
	}
}

// TestWArrayIsSearchable covers the sorted, non-overlapping form parseWArray / parseW2Array leave behind so that
// cidWidth and cidVMetrics can binary search: entries arriving out of order must still resolve, and overlapping entries
// must be trimmed rather than left to shadow one another.
func TestWArrayIsSearchable(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF("<< >>")))
	if err != nil {
		t.Fatal(err)
	}

	// Deliberately out of CID order, with three kinds of overlap: [10, 13] partially covers the later-listed [12, 15],
	// [5, 8] partially covers the later-listed per-CID [7, 9] (whose surviving width must be re-based, not shifted), and
	// [0, 100] wholly covers [50, 51], which must drop out entirely.
	info := &type0Info{dw: 1, w: parseWArray(d, cos.Array{
		cos.Integer(200), cos.Integer(210), cos.Integer(2000),
		cos.Integer(10),
		cos.Array{cos.Integer(100), cos.Integer(200), cos.Integer(300), cos.Integer(400)},
		cos.Integer(12), cos.Integer(15), cos.Integer(1500),
		cos.Integer(5), cos.Integer(8), cos.Integer(800),
		cos.Integer(7),
		cos.Array{cos.Integer(700), cos.Integer(710), cos.Integer(720)},
		cos.Integer(0), cos.Integer(100), cos.Integer(1000),
		cos.Integer(50),
		cos.Array{cos.Integer(5000), cos.Integer(5100)},
	})}
	for i := 1; i < len(info.w); i++ {
		if prev, cur := info.w[i-1], info.w[i]; cur.lo <= prev.hi {
			t.Fatalf("/W ranges not sorted and disjoint: %+v then %+v", prev, cur)
		}
	}
	for cid, want := range map[uint32]float32{
		0: 1, 4: 1, 9: 1, 50: 1, 51: 1, 100: 1, // [0, 100] wins everything the later entries claimed inside it.
		101: 1, 199: 1, // /DW: past the last kept range but before the next one.
		200: 2, 205: 2, 210: 2,
		211: 1, 65535: 1, // /DW: past every range.
	} {
		if got := info.cidWidth(cid); got != want {
			t.Errorf("cidWidth(%d) = %v, want %v", cid, got, want)
		}
	}

	// The same overlaps without the [0, 100] blanket, so the trimming of the two partial overlaps is observable.
	info = &type0Info{dw: 1, w: parseWArray(d, cos.Array{
		cos.Integer(12), cos.Integer(15), cos.Integer(1500),
		cos.Integer(10),
		cos.Array{cos.Integer(100), cos.Integer(200), cos.Integer(300), cos.Integer(400)},
		cos.Integer(7),
		cos.Array{cos.Integer(700), cos.Integer(710), cos.Integer(720)},
		cos.Integer(5), cos.Integer(8), cos.Integer(800),
	})}
	for cid, want := range map[uint32]float32{
		4:  1,                              // /DW.
		5:  0.8,                            // [5, 8] starts lower than [7, 9], so it keeps the contested 7 and 8.
		8:  0.8,                            //
		9:  0.72,                           // The tail of [7, 9] survives, re-based onto its third width.
		10: 0.1, 11: 0.2, 12: 0.3, 13: 0.4, // [10, 13] starts lower than [12, 15], so it keeps 12 and 13.
		14: 1.5, 15: 1.5, // The tail of [12, 15] survives.
		16: 1, // /DW.
	} {
		if got := info.cidWidth(cid); got != want {
			t.Errorf("trimmed cidWidth(%d) = %v, want %v", cid, got, want)
		}
	}

	// Many entries, listed high CID first, to exercise the search rather than a scan that happens to hit early.
	const n = 500
	var arr cos.Array
	for i := n - 1; i >= 0; i-- {
		arr = append(arr, cos.Integer(4*i), cos.Integer(4*i+1), cos.Integer(i)) // Ranges [4i, 4i+1], gaps between.
	}
	info = &type0Info{dw: 7, w: parseWArray(d, arr)}
	if len(info.w) != n {
		t.Fatalf("kept %d of %d /W ranges", len(info.w), n)
	}
	for i := range uint32(n) {
		want := float32(i) / 1000
		if got := info.cidWidth(4 * i); got != want {
			t.Errorf("cidWidth(%d) = %v, want %v", 4*i, got, want)
		}
		if got := info.cidWidth(4*i + 1); got != want {
			t.Errorf("cidWidth(%d) = %v, want %v", 4*i+1, got, want)
		}
		if got := info.cidWidth(4*i + 2); got != 7 { // In the gap, so /DW.
			t.Errorf("gap cidWidth(%d) = %v, want 7", 4*i+2, got)
		}
	}

	// /W2 gets the same treatment: out of order, with [20, 29] partially shadowing the later-listed per-CID [25, 27].
	v := &type0Info{dw2: [2]float32{0.88, -1}, w2: parseW2Array(d, cos.Array{
		cos.Integer(40),
		cos.Array{cos.Integer(-400), cos.Integer(410), cos.Integer(420)},
		cos.Integer(25),
		cos.Array{
			cos.Integer(-250), cos.Integer(251), cos.Integer(252),
			cos.Integer(-260), cos.Integer(261), cos.Integer(262),
			cos.Integer(-270), cos.Integer(271), cos.Integer(272),
		},
		cos.Integer(20), cos.Integer(26), cos.Integer(-200), cos.Integer(201), cos.Integer(202),
	})}
	for i := 1; i < len(v.w2); i++ {
		if prev, cur := v.w2[i-1], v.w2[i]; cur.lo <= prev.hi {
			t.Fatalf("/W2 ranges not sorted and disjoint: %+v then %+v", prev, cur)
		}
	}
	for _, tc := range []struct {
		cid        uint32
		w1, vx, vy float32
	}{
		{cid: 19, w1: -1, vx: 0.25, vy: 0.88}, // Defaults.
		{cid: 20, w1: -0.2, vx: 0.201, vy: 0.202},
		{cid: 26, w1: -0.2, vx: 0.201, vy: 0.202},  // [20, 26] starts lower, so it keeps 25 and 26.
		{cid: 27, w1: -0.27, vx: 0.271, vy: 0.272}, // The re-based tail of [25, 27].
		{cid: 28, w1: -1, vx: 0.25, vy: 0.88},      // Defaults.
		{cid: 40, w1: -0.4, vx: 0.41, vy: 0.42},
		{cid: 41, w1: -1, vx: 0.25, vy: 0.88}, // Defaults.
	} {
		if w1, vx, vy := v.cidVMetrics(tc.cid, 0.5); w1 != tc.w1 || vx != tc.vx || vy != tc.vy {
			t.Errorf("cidVMetrics(%d) = %v %v %v, want %v %v %v", tc.cid, w1, vx, vy, tc.w1, tc.vx, tc.vy)
		}
	}
}

// TestCMapRangesAreSearchable covers the sorted, non-overlapping form parseCMap leaves its code→CID and bf lists in so
// that cid and bfString can binary search: entries arriving out of code order must still resolve, and overlapping
// entries must be trimmed — with the surviving tail re-based onto the right CID, bfrange increment or array element —
// rather than left to shadow one another.
func TestCMapRangesAreSearchable(t *testing.T) {
	// Deliberately out of code order, with three kinds of overlap: [0010, 0013] partially covers the later-listed
	// [0012, 0015], [0005, 0008] partially covers the later-listed [0007, 0009], and [0100, 0180] wholly covers
	// [0150, 0151], which must drop out entirely.
	cm := parseCMap([]byte(`1 begincodespacerange <0000> <ffff> endcodespacerange
7 begincidrange
<0200> <020a> 2000
<0010> <0013> 100
<0012> <0015> 500
<0005> <0008> 800
<0007> <0009> 700
<0100> <0180> 9000
<0150> <0151> 4000
endcidrange`), 0, nil)
	if cm == nil {
		t.Fatal("parseCMap returned nil")
	}
	for i := 1; i < len(cm.cids); i++ {
		if prev, cur := cm.cids[i-1], cm.cids[i]; cur.lo <= prev.hi {
			t.Fatalf("cid ranges not sorted and disjoint: %+v then %+v", prev, cur)
		}
	}
	for code, want := range map[uint32]uint32{
		0x0004: 0, 0x000a: 0, 0x0016: 0, 0x00ff: 0, 0x0181: 0, 0x020b: 0, // Unmapped, around the kept ranges.
		0x0005: 800, 0x0008: 803, // [0005, 0008] starts lower than [0007, 0009], so it keeps the contested 7 and 8.
		0x0009: 702,              // The tail of [0007, 0009] survives, re-based onto CID 700 + 2.
		0x0010: 100, 0x0013: 103, // [0010, 0013] starts lower than [0012, 0015], so it keeps 12 and 13.
		0x0014: 502, 0x0015: 503, // The re-based tail of [0012, 0015].
		0x0100: 9000, 0x0150: 9080, 0x0151: 9081, 0x0180: 9128, // [0100, 0180] wins everything [0150, 0151] claimed.
		0x0200: 2000, 0x020a: 2010, // The entry listed first, now searched last.
	} {
		if got := cm.cid(code); got != want {
			t.Errorf("cid(%#04x) = %d, want %d", code, got, want)
		}
	}

	// bf entries get the same treatment. [0020, 0031] partially covers the later-listed contiguous [0030, 0033], whose
	// surviving tail must keep incrementing from its own start; [0040, 004f] carries a two-element array, so it maps only
	// [0040, 0041] and the codes past the array's end stay available to the later [0042, 0043]; and [0050, 0055] partially
	// covers the later-listed array entry [0053, 0056], whose surviving element must be the array's fourth, not its first.
	cm = parseCMap([]byte(`1 begincodespacerange <0000> <ffff> endcodespacerange
5 beginbfrange
<0030> <0033> <0041>
<0020> <0031> <0061>
<0053> <0056> [<0041> <0042> <0043> <0044>]
<0040> <004f> [<0058> <0059>]
<0042> <0043> <0030>
<0050> <0055> <0061>
endbfrange`), 0, nil)
	if cm == nil {
		t.Fatal("parseCMap returned nil")
	}
	for i := 1; i < len(cm.bf); i++ {
		if prev, cur := cm.bf[i-1], cm.bf[i]; cur.lo <= prev.hi {
			t.Fatalf("bf ranges not sorted and disjoint: %+v then %+v", prev, cur)
		}
	}
	for code, want := range map[uint32]string{
		0x001f: "", 0x0034: "", 0x0044: "", 0x0057: "", // Unmapped, around the kept ranges.
		0x0020: "a", 0x0030: "q", 0x0031: "r", // [0020, 0031] starts lower, so it keeps the contested 30 and 31.
		0x0032: "C", 0x0033: "D", // The tail of [0030, 0033] survives, still incrementing from its own <0041>.
		0x0040: "X", 0x0041: "Y", // The array bounds the entry: 0042 and beyond fall to the next entry.
		0x0042: "0", 0x0043: "1",
		0x0050: "a", 0x0055: "f", // [0050, 0055] starts lower than the array entry [0053, 0056].
		0x0056: "D", // The tail of [0053, 0056] survives, re-based onto the array's fourth element.
	} {
		if got := cm.bfString(code); got != want {
			t.Errorf("bfString(%#04x) = %q, want %q", code, got, want)
		}
	}

	// Many entries, listed high code first, to exercise the search rather than a scan that happens to hit early.
	const n = 500
	var sb strings.Builder
	sb.WriteString("1 begincodespacerange <0000> <ffff> endcodespacerange\n500 begincidrange\n")
	for i := n - 1; i >= 0; i-- {
		fmt.Fprintf(&sb, "<%04x> <%04x> %d\n", 4*i, 4*i+1, 1000+i) // Ranges [4i, 4i+1], gaps between.
	}
	sb.WriteString("endcidrange")
	if cm = parseCMap([]byte(sb.String()), 0, nil); cm == nil {
		t.Fatal("parseCMap returned nil")
	}
	if len(cm.cids) != n {
		t.Fatalf("kept %d of %d cid ranges", len(cm.cids), n)
	}
	for i := range uint32(n) {
		want := 1000 + i
		if got := cm.cid(4 * i); got != want {
			t.Errorf("cid(%d) = %d, want %d", 4*i, got, want)
		}
		if got := cm.cid(4*i + 1); got != want+1 { // The range's second code takes the next CID.
			t.Errorf("cid(%d) = %d, want %d", 4*i+1, got, want+1)
		}
		if got := cm.cid(4*i + 2); got != 0 { // In the gap, so unmapped.
			t.Errorf("gap cid(%d) = %d, want 0", 4*i+2, got)
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
