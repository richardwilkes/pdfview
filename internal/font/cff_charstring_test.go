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
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/richardwilkes/pdfview/internal/font/data"
)

// Type 2 charstring operators used by the fixtures here.
const (
	csRmoveto  = 21
	csRlineto  = 5
	csEndchar  = 14
	csReturn   = 11
	csCallGSub = 29
	csCallSub  = 10
)

// csInt encodes a small charstring integer (the single-byte form covers -107..107, which is every operand these
// fixtures need).
func csInt(v int) byte { return byte(v + 139) }

// csGSubrIndex encodes the operand a callgsubr needs to reach subroutine i. Type 2 biases the index by 107 for any
// array shorter than 1240 entries (TN5177 section 4.7), and the fixtures here stay far below that.
func csGSubrIndex(i int) byte { return csInt(i - 107) }

// cffLayout assembles a CFF container around the caller's pieces: header, Name INDEX, Top DICT INDEX (with the
// CharStrings and, when present, the Private DICT offsets patched in), String INDEX, Global Subr INDEX, then the
// trailing blocks in the order the offsets name them.
//
// dict is the caller's Top DICT prefix; it is followed by "<32-bit offset> 17" for CharStrings and, when privDict is
// non-nil, by "<size> <32-bit offset> 18" for the Private DICT. The two placeholder offsets are patched once the
// preceding INDEX lengths are known, which is why the layout is built twice.
func cffLayout(dict []byte, gsubrs, charstrings [][]byte, privDict []byte, localSubrs [][]byte) []byte {
	head := []byte{1, 0, 4, 1}
	name := buildIndex([]byte("Test"))
	str := buildIndex()
	gsub := buildIndex(gsubrs...)
	csIndex := buildIndex(charstrings...)

	full := append([]byte{}, dict...)
	full = append(full, 29, 0, 0, 0, 0, 17) // CharStrings offset (patched below)
	if privDict != nil {
		full = append(full, 29, 0, 0, 0, 0, 29, 0, 0, 0, 0, 18) // Private: size then offset (both patched below)
	}
	top := buildIndex(full)
	base := len(head) + len(name) + len(top) + len(str) + len(gsub)
	binary.BigEndian.PutUint32(full[len(dict)+1:], uint32(base))
	if privDict != nil {
		privOff := base + len(csIndex)
		binary.BigEndian.PutUint32(full[len(dict)+7:], uint32(len(privDict)))
		binary.BigEndian.PutUint32(full[len(dict)+12:], uint32(privOff))
	}
	// The patched dict is the same length as the one measured above, so the offsets still hold.
	top = buildIndex(full)

	var buf bytes.Buffer
	buf.Write(head)
	buf.Write(name)
	buf.Write(top)
	buf.Write(str)
	buf.Write(gsub)
	buf.Write(csIndex)
	if privDict != nil {
		buf.Write(privDict)
		buf.Write(buildIndex(localSubrs...))
	}
	return buf.Bytes()
}

// privDictWithSubrs builds a Private DICT whose only entry is the local Subrs offset (operator 19), which is relative
// to the start of the Private DICT — here, just past its own bytes.
func privDictWithSubrs() []byte {
	d := []byte{29, 0, 0, 0, 0, 19}
	binary.BigEndian.PutUint32(d[1:], uint32(len(d)))
	return d
}

// boxOutline draws a square of the given side at the origin, without the operator that ends the run.
func boxOutline(side int) []byte {
	return []byte{
		csInt(0), csInt(0), csRmoveto,
		csInt(side), csInt(0), csRlineto,
		csInt(0), csInt(side), csRlineto,
		csInt(-side), csInt(0), csRlineto,
	}
}

// boxCharstring draws the square and ends the glyph; boxSubr draws it and returns to its caller.
func boxCharstring(side int) []byte { return append(boxOutline(side), csEndchar) }
func boxSubr(side int) []byte       { return append(boxOutline(side), csReturn) }

// loadCFFInfo runs the package's own CFF preparation over raw bytes, failing the test when the program did not parse.
func loadCFFInfo(t *testing.T, raw []byte) *cffInfo {
	t.Helper()
	top, err := parseCFFTopDict(raw)
	if err != nil {
		t.Fatalf("parseCFFTopDict: %v", err)
	}
	info := parseCFFGlyphBytes(raw, top)
	if info == nil {
		t.Fatal("parseCFFGlyphBytes returned nil")
	}
	return info
}

// assertMatchesGoText checks that the budgeted interpreter reproduces exactly what go-text's own loader produces. The
// budget exists to stop hostile programs, and the outlines of every valid one must be untouched by it; the handler
// drives the same psi.CharstringReader operators for precisely this reason.
func assertMatchesGoText(t *testing.T, info *cffInfo, gid uint32) {
	t.Helper()
	want, _, err := info.font.LoadGlyph(tables.GlyphID(gid))
	if err != nil {
		t.Fatalf("go-text LoadGlyph(%d): %v", gid, err)
	}
	got, ok := info.glyphSegments(gid)
	if !ok {
		t.Fatalf("glyphSegments(%d) reported failure where go-text succeeded", gid)
	}
	if len(got) == 0 {
		t.Fatalf("glyphSegments(%d) drew nothing", gid)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("glyphSegments(%d) = %v, want %v (go-text's own loader)", gid, got, want)
	}
}

// TestCFFCharstringMatchesGoText covers the plain case: a glyph whose whole outline sits in its charstring.
func TestCFFCharstringMatchesGoText(t *testing.T) {
	raw := cffLayout(nil, nil, [][]byte{boxCharstring(0), boxCharstring(100)}, nil, nil)
	assertMatchesGoText(t, loadCFFInfo(t, raw), 1)
}

// TestCFFLocalSubrOutline drives the outline through a local subroutine, which only resolves when the Private DICT and
// its Subrs INDEX were recovered from the container. A font whose local subroutines went missing would draw nothing —
// real Type1C programs put most of their outline work in exactly these.
func TestCFFLocalSubrOutline(t *testing.T) {
	raw := cffLayout(nil, nil,
		[][]byte{{csEndchar}, {csInt(-107), csCallSub, csEndchar}},
		privDictWithSubrs(), [][]byte{boxSubr(100)})
	info := loadCFFInfo(t, raw)
	if info.subrs == nil || len(info.subrs.localFor(1)) != 1 {
		t.Fatalf("local subroutines not recovered: %+v", info.subrs)
	}
	assertMatchesGoText(t, info, 1)
}

// TestCFFGlobalSubrOutline is the same check for the global subroutine array, which sits at a fixed place in the
// container rather than behind the Private DICT.
func TestCFFGlobalSubrOutline(t *testing.T) {
	raw := cffLayout(nil, [][]byte{boxSubr(100)},
		[][]byte{{csEndchar}, {csGSubrIndex(0), csCallGSub, csEndchar}}, nil, nil)
	info := loadCFFInfo(t, raw)
	if info.subrs == nil || len(info.subrs.global) != 1 {
		t.Fatalf("global subroutines not recovered: %+v", info.subrs)
	}
	assertMatchesGoText(t, info, 1)
}

// TestCFFSubroutineBombIsBudgeted is the whole point of owning the interpreter. psi.Machine caps subroutine NESTING at
// 10 but nothing caps BRANCHING, so nine global subroutines that each call the next eight times cost 8^8 ≈ 16.7M
// operator dispatches for one glyph — measured at ~2.5 s through go-text's unbudgeted loader, and exactly exponential
// in the branch factor, so a slightly wider program never returns at all. The whole bomb is 100-odd bytes and
// compresses to nothing, so a page can name it once per glyph shown.
//
// The budget must stop it promptly and the glyph must degrade to no outline, never to a hung render.
func TestCFFSubroutineBombIsBudgeted(t *testing.T) {
	const (
		levels = 9
		branch = 8
	)
	gsubrs := make([][]byte, levels)
	gsubrs[levels-1] = []byte{csReturn}
	for k := levels - 2; k >= 0; k-- {
		body := make([]byte, 0, 2*branch+1)
		for range branch {
			body = append(body, csGSubrIndex(k+1), csCallGSub)
		}
		gsubrs[k] = append(body, csReturn)
	}
	raw := cffLayout(nil, gsubrs, [][]byte{{csEndchar}, {csGSubrIndex(0), csCallGSub, csEndchar}}, nil, nil)
	if len(raw) > 512 {
		t.Fatalf("fixture grew to %d bytes; the point is that the bomb is tiny", len(raw))
	}
	info := loadCFFInfo(t, raw)
	start := time.Now()
	segs, ok := info.glyphSegments(1)
	elapsed := time.Since(start)
	if ok || segs != nil {
		t.Error("the exponential charstring was interpreted to completion instead of tripping the work budget")
	}
	// Unbudgeted this call takes seconds; budgeted it stops after maxCFFHandlerOps dispatches, which is milliseconds.
	if elapsed > time.Second {
		t.Errorf("budgeted interpretation took %v, so the branch amplification is still running", elapsed)
	}
}

// TestCFFSegmentFloodIsBudgeted covers the other amplification a charstring can reach: rlineto emits one segment per
// operand pair and the argument stack holds 513 of them, so a modest program repeated through subroutine calls can
// pile up outline segments far past anything a real glyph draws.
func TestCFFSegmentFloodIsBudgeted(t *testing.T) {
	// A subroutine that draws 40 line segments and returns, called 200 times by the charstring: 8000 segments, still
	// under the cap, so this also pins that the cap is nowhere near a plausible glyph.
	body := make([]byte, 0, 3*40+4)
	body = append(body, csInt(0), csInt(0), csRmoveto)
	for range 40 {
		body = append(body, csInt(1), csInt(1), csRlineto)
	}
	body = append(body, csReturn)
	cs := make([]byte, 0, 2*200+1)
	for range 200 {
		cs = append(cs, csGSubrIndex(0), csCallGSub)
	}
	cs = append(cs, csEndchar)
	raw := cffLayout(nil, [][]byte{body}, [][]byte{{csEndchar}, cs}, nil, nil)
	info := loadCFFInfo(t, raw)
	segs, ok := info.glyphSegments(1)
	if !ok {
		t.Fatal("a legitimate 8000-segment glyph was rejected: the caps are too tight")
	}
	if len(segs) > maxCFFSegments+8 { // The cap is checked per operator, so a few segments may land past it.
		t.Errorf("emitted %d segments, past the %d cap", len(segs), maxCFFSegments)
	}
}

// cidCFFLayout assembles a CID-keyed CFF: the Top DICT carries ROS, FDArray and FDSelect, and each font DICT in the
// FDArray holds its own Private DICT with its own local subroutines. It is the layout every CJK CIDFontType0 subset
// uses, and the one where picking the wrong local subroutine array silently draws the wrong glyph.
//
// privDict is shared by every font DICT and must name its local Subrs at an offset equal to its own length, since the
// subroutine INDEX is written immediately after it.
func cidCFFLayout(charstrings [][]byte, fdLocalSubrs [][][]byte, fdSelect, privDict []byte) []byte {
	head := []byte{1, 0, 4, 1}
	name := buildIndex([]byte("Test"))
	str := buildIndex()
	gsub := buildIndex()
	csIndex := buildIndex(charstrings...)

	// ROS <sid> <sid> <supplement>, then the three patched offsets: CharStrings, FDArray, FDSelect.
	dict := make([]byte, 0, 25)
	dict = append(dict,
		csInt(0), csInt(0), csInt(0), 12, 30, // ROS
		29, 0, 0, 0, 0, 17, // CharStrings
		29, 0, 0, 0, 0, 12, 36, // FDArray
		29, 0, 0, 0, 0, 12, 37) // FDSelect
	patch := func(at, v int) { binary.BigEndian.PutUint32(dict[at:], uint32(v)) }

	// The blocks after the Top DICT INDEX, in write order: CharStrings, FDSelect, then per-FD Private DICT + Subrs,
	// then the FDArray INDEX naming those Private DICTs.
	top := buildIndex(dict)
	base := len(head) + len(name) + len(top) + len(str) + len(gsub)
	patch(6, base)
	fdSelectOff := base + len(csIndex)
	patch(19, fdSelectOff)

	var tail bytes.Buffer
	fontDicts := make([][]byte, len(fdLocalSubrs))
	off := fdSelectOff + len(fdSelect)
	for i, subrs := range fdLocalSubrs {
		index := buildIndex(subrs...)
		fd := []byte{29, 0, 0, 0, 0, 29, 0, 0, 0, 0, 18} // Private: size then offset
		binary.BigEndian.PutUint32(fd[1:], uint32(len(privDict)))
		binary.BigEndian.PutUint32(fd[6:], uint32(off))
		fontDicts[i] = fd
		tail.Write(privDict)
		tail.Write(index)
		off += len(privDict) + len(index)
	}
	patch(12, off)
	top = buildIndex(dict) // Re-encoded with the patched offsets; the 32-bit operands keep the length the same.

	var buf bytes.Buffer
	buf.Write(head)
	buf.Write(name)
	buf.Write(top)
	buf.Write(str)
	buf.Write(gsub)
	buf.Write(csIndex)
	buf.Write(fdSelect)
	buf.Write(tail.Bytes())
	buf.Write(buildIndex(fontDicts...))
	return buf.Bytes()
}

// TestCFFCIDLocalSubrsFollowFDSelect pins the CID-keyed half of the subroutine walk. Each glyph's local subroutines
// come from the Private DICT of the font DICT its FDSelect entry names, so a walk that read the wrong one — or none —
// would leave the glyph blank or draw another FD's shape.
func TestCFFCIDLocalSubrsFollowFDSelect(t *testing.T) {
	subrA := boxSubr(40)
	subrB := boxSubr(120)
	callSubr := []byte{csInt(-107), csCallSub, csEndchar}
	// Format 0 FDSelect: one byte per glyph. Glyph 0 (.notdef) and glyph 1 use FD 0; glyph 2 uses FD 1.
	for _, fdSelect := range [][]byte{
		{0, 0, 0, 1},                      // format 0
		{3, 0, 2, 0, 0, 0, 0, 2, 1, 0, 3}, // format 3: [0,2) → FD 0, [2,3) → FD 1, sentinel 3
	} {
		raw := cidCFFLayout([][]byte{{csEndchar}, callSubr, callSubr},
			[][][]byte{{subrA}, {subrB}}, fdSelect, privDictWithSubrs())
		info := loadCFFInfo(t, raw)
		if info.subrs == nil || len(info.subrs.local) != 2 {
			t.Fatalf("FDSelect format %d: font DICT subroutines not recovered: %+v", fdSelect[0], info.subrs)
		}
		assertMatchesGoText(t, info, 1)
		assertMatchesGoText(t, info, 2)
		one, _ := info.glyphSegments(1)
		two, _ := info.glyphSegments(2)
		if reflect.DeepEqual(one, two) {
			t.Errorf("FDSelect format %d: both glyphs drew the same outline, so FDSelect was not followed", fdSelect[0])
		}
	}
}

// liberationTables returns the named tables of the bundled LiberationSans program, the raw material for the synthetic
// sfnt wrappers below.
func liberationTables(t *testing.T, tags ...string) map[string][]byte {
	t.Helper()
	raw := data.Liberation("LiberationSans-Regular")
	if raw == nil {
		t.Fatal("bundled LiberationSans-Regular missing")
	}
	ld, err := opentype.NewLoader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("loading LiberationSans-Regular: %v", err)
	}
	out := make(map[string][]byte, len(tags))
	for _, tag := range tags {
		tbl, tblErr := ld.RawTable(opentype.MustNewTag(tag))
		if tblErr != nil {
			t.Fatalf("table %q: %v", tag, tblErr)
		}
		out[tag] = tbl
	}
	return out
}

// buildOTTO assembles a CFF-flavored OpenType program: buildSFNT's layout under the 'OTTO' signature.
func buildOTTO(tbls map[string][]byte) []byte {
	out := buildSFNT(tbls)
	copy(out, "OTTO")
	return out
}

// openTypeCFF wraps a bare CFF in an OpenType program complete enough for go-text to build a face for it (it wants
// cmap, head and maxp), which is what routes the font through the sfnt arm of Font.GlyphPath.
func openTypeCFF(t *testing.T, cffTable []byte, nGlyphs int) []byte {
	t.Helper()
	// Only the cmap is borrowed: go-text refuses a program without one. The remaining tables describe the synthetic
	// CFF rather than Liberation, because go-text will not draw from a 'CFF ' table whose charstring count disagrees
	// with maxp — and its hmtx reader panics outright when hhea claims more metrics than maxp has glyphs.
	tbls := liberationTables(t, "cmap")
	tbls["CFF "] = cffTable
	tbls["maxp"] = []byte{0, 0, 0x50, 0, 0, byte(nGlyphs)}
	tbls["head"] = headTable(1000)
	tbls["hhea"] = hheaTable(900, -300)
	tbls["hmtx"] = []byte{0x01, 0xF4, 0, 0} // One longHorMetric: advance 500, left side bearing 0.
	return buildOTTO(tbls)
}

// simpleOpenTypeFont loads an sfnt program as a simple font's /FontFile3 with /Subtype /OpenType.
func simpleOpenTypeFont(t *testing.T, program []byte) *Font {
	t.Helper()
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /Type1 /BaseFont /TestOTF /FontDescriptor 2 0 R >>",
		"<< /Type /FontDescriptor /FontName /TestOTF /Flags 4 /FontFile3 3 0 R >>",
		fmt.Sprintf("<< /Length %d /Subtype /OpenType >>\nstream\n%s\nendstream", len(program), program))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestOpenTypeCFFOutlineMatchesFace pins the swap on the sfnt arm: a CFF-flavored OpenType program's outlines now come
// from the budgeted interpreter instead of go-text's Face, and the two must agree glyph for glyph — the budget is
// there to stop hostile programs, not to change any valid one.
func TestOpenTypeCFFOutlineMatchesFace(t *testing.T) {
	f := simpleOpenTypeFont(t, openTypeCFF(t, cffLayout(nil, nil,
		[][]byte{boxCharstring(0), boxCharstring(100)}, nil, nil), 2))
	if f.sfnt == nil || f.sfnt.face == nil {
		t.Fatal("the OpenType wrapper did not parse")
	}
	if f.sfnt.cff == nil {
		t.Fatal("the wrapped 'CFF ' table was not prepared, so outlines still run unbudgeted through the face")
	}
	outline, ok := f.sfnt.face.GlyphDataOutline(tables.GlyphID(1))
	if !ok {
		t.Fatal("go-text drew nothing for the wrapped CFF glyph")
	}
	segs, ok := f.sfnt.cff.glyphSegments(1)
	if !ok {
		t.Fatal("the budgeted interpreter drew nothing for the wrapped CFF glyph")
	}
	if !reflect.DeepEqual(segs, outline.Segments) {
		t.Errorf("wrapped CFF segments = %v, want %v (go-text's own loader)", segs, outline.Segments)
	}
	if p := f.GlyphPath(1); p == nil || p.IsEmpty() {
		t.Error("GlyphPath drew nothing for the wrapped CFF glyph")
	}
}

// TestOpenTypeCFFBombIsBudgeted is the same exponential charstring reached through the other call site the unbudgeted
// loader backed: Face.GlyphDataOutline tries the 'CFF ' table first, so a CFF-flavored OpenType program hangs a render
// exactly as a bare CFF one does.
func TestOpenTypeCFFBombIsBudgeted(t *testing.T) {
	const (
		levels = 9
		branch = 8
	)
	gsubrs := make([][]byte, levels)
	gsubrs[levels-1] = []byte{csReturn}
	for k := levels - 2; k >= 0; k-- {
		body := make([]byte, 0, 2*branch+1)
		for range branch {
			body = append(body, csGSubrIndex(k+1), csCallGSub)
		}
		gsubrs[k] = append(body, csReturn)
	}
	f := simpleOpenTypeFont(t, openTypeCFF(t, cffLayout(nil, gsubrs,
		[][]byte{{csEndchar}, {csGSubrIndex(0), csCallGSub, csEndchar}}, nil, nil), 2))
	if f.sfnt == nil || f.sfnt.cff == nil {
		t.Fatal("the OpenType wrapper did not reach the budgeted interpreter")
	}
	start := time.Now()
	p := f.GlyphPath(1)
	elapsed := time.Since(start)
	if p != nil && !p.IsEmpty() {
		t.Error("the exponential charstring drew an outline instead of tripping the work budget")
	}
	if elapsed > time.Second {
		t.Errorf("GlyphPath took %v, so the wrapped CFF still runs unbudgeted", elapsed)
	}
}
