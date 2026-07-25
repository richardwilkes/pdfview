// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package type1

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"testing"

	ot "github.com/go-text/typesetting/font/opentype"
)

// ---- test font builder ----------------------------------------------------------------------------------------
//
// buildTestFont assembles a complete, spec-conformant Type 1 program from scratch (clear text, eexec-encrypted private
// dict, encrypted charstrings) so the parser and interpreter are tested against known geometry. The corpus generator
// used the same construction; only its output is committed (testfiles/corpus README pattern).

// csNum encodes one charstring number (spec 6.2).
func csNum(v int) []byte {
	switch {
	case v >= -107 && v <= 107:
		return []byte{byte(v + 139)}
	case v >= 108 && v <= 1131:
		v -= 108
		return []byte{byte(247 + v>>8), byte(v)}
	case v <= -108 && v >= -1131:
		v = -v - 108
		return []byte{byte(251 + v>>8), byte(v)}
	default:
		return []byte{255, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
}

// cs assembles a charstring from numbers (int) and operator bytes ([]byte).
func cs(parts ...any) []byte {
	var out []byte
	for _, p := range parts {
		switch v := p.(type) {
		case int:
			out = append(out, csNum(v)...)
		case []byte:
			out = append(out, v...)
		default:
			panic(fmt.Sprintf("bad part %T", p))
		}
	}
	return out
}

// Operator byte sequences.
var (
	oHstem     = []byte{1}
	oVstem     = []byte{3}
	oVmoveto   = []byte{4}
	oRlineto   = []byte{5}
	oHlineto   = []byte{6}
	oVlineto   = []byte{7}
	oRrcurveto = []byte{8}
	oClosepath = []byte{9}
	oCallsubr  = []byte{10}
	oReturn    = []byte{11}
	oHsbw      = []byte{13}
	oEndchar   = []byte{14}
	oRmoveto   = []byte{21}
	oHmoveto   = []byte{22}
	oVhcurveto = []byte{30}
	oHvcurveto = []byte{31}
	oDotsect   = []byte{12, 0}
	oSeac      = []byte{12, 6}
	oSbw       = []byte{12, 7}
	oDiv       = []byte{12, 12}
	oOthersubr = []byte{12, 16}
	oPop       = []byte{12, 17}
	oSetCurPt  = []byte{12, 33}
)

// encryptT1 runs the Type 1 encryption with lead prepended plaintext bytes (4 for eexec, lenIV for charstrings).
func encryptT1(plain []byte, r uint16, lead int) []byte {
	full := append(bytes.Repeat([]byte{0x55}, lead), plain...)
	out := make([]byte, len(full))
	for i, p := range full {
		c := p ^ byte(r>>8)
		r = (uint16(c)+r)*encryptC1 + encryptC2
		out[i] = c
	}
	return out
}

// standardFlexSubrs returns the four standard Subrs entries fonts using flex/hint replacement must carry.
func standardFlexSubrs() [][]byte {
	return [][]byte{
		cs(3, 0, oOthersubr, oPop, oPop, oSetCurPt, oReturn),
		cs(0, 1, oOthersubr, oReturn),
		cs(0, 2, oOthersubr, oReturn),
		cs(0, oReturn), // Dummy: hint replacement calls it when othersubr 3 is unimplemented.
	}
}

// testGlyphs returns the charstrings of the test font, keyed by glyph name.
func testGlyphs() map[string][]byte {
	flexBody := cs(
		0, 500, oHsbw,
		0, 0, oRmoveto,
		100, oHlineto,
		1, oCallsubr,
		50, 25, oRmoveto, 2, oCallsubr,
		-17, -5, oRmoveto, 2, oCallsubr,
		33, 20, oRmoveto, 2, oCallsubr,
		34, 10, oRmoveto, 2, oCallsubr,
		33, 10, oRmoveto, 2, oCallsubr,
		33, 20, oRmoveto, 2, oCallsubr,
		34, 20, oRmoveto, 2, oCallsubr,
		50, 300, 100, 0, oCallsubr,
		0, -100, oRlineto,
		oClosepath, oEndchar,
	)
	return map[string][]byte{
		notdefName: cs(0, 500, oHsbw, oEndchar),
		"A": cs(0, 600, oHsbw, 100, 0, oRmoveto, 400, oHlineto, 0, 700, oRlineto,
			-400, 0, oRlineto, oClosepath, oEndchar),
		"B": cs(20, 1400, 2, oDiv, oHsbw, 0, 100, oHstem, 20, 80, oVstem, oDotsect,
			0, 0, oRmoveto, 4, oCallsubr,
			0, 0, 100, 100, 0, 100, oRrcurveto,
			100, 50, 50, 100, oVhcurveto,
			50, 50, 50, -50, oHvcurveto,
			oClosepath, oEndchar),
		"C": flexBody,
		"D": cs(0, 0, 400, 0, oSbw, 10, 20, oRmoveto, 100, oVlineto, -50, oHmoveto, 30, oVmoveto, oEndchar),
		"e": cs(50, 500, oHsbw, 0, 0, oRmoveto, 300, oHlineto, 0, 400, oRlineto,
			-300, 0, oRlineto, oClosepath, oEndchar),
		glyphAcute:  cs(30, 300, oHsbw, 0, 0, oRmoveto, 50, 100, oRlineto, oClosepath, oEndchar),
		glyphEacute: cs(30, 550, oHsbw, 30, 250, 350, 101, 194, oSeac),
	}
}

// buildTestFont assembles the full program. hexForm selects PFA-style hex eexec data; pfb wraps the result in PFB
// segments.
func buildTestFont(hexForm, pfb, stdEncoding bool) []byte {
	var clearBuf bytes.Buffer
	clearBuf.WriteString("%!PS-AdobeFont-1.0: TestT1 001.000\n")
	clearBuf.WriteString("/FontName /TestT1 def\n")
	clearBuf.WriteString("/FontMatrix [0.001 0 0 0.001 0 0] readonly def\n")
	clearBuf.WriteString("/FontBBox {0 -200 1000 800} readonly def\n")
	if stdEncoding {
		clearBuf.WriteString("/Encoding StandardEncoding def\n")
	} else {
		clearBuf.WriteString("/Encoding 256 array\n0 1 255 {1 index exch /.notdef put} for\n")
		clearBuf.WriteString("dup 65 /A put\ndup 66 /B put\ndup 67 /C put\ndup 68 /D put\n")
		clearBuf.WriteString("dup 101 /e put\ndup 194 /acute put\ndup 233 /eacute put\nreadonly def\n")
	}
	clearBuf.WriteString("currentdict end\ncurrentfile eexec\n")

	var priv bytes.Buffer
	priv.WriteString("  dup /Private 15 dict dup begin\n") // Leading bytes exercise the 4-byte skip.
	priv.WriteString("/lenIV 4 def\n")
	subrs := standardFlexSubrs()
	subrs = append(subrs, cs(200, 0, oRlineto, oReturn)) // Subr 4: shared drawing fragment.
	fmt.Fprintf(&priv, "/Subrs %d array\n", len(subrs))
	for i, sub := range subrs {
		enc := encryptT1(sub, charstringR, 4)
		fmt.Fprintf(&priv, "dup %d %d RD ", i, len(enc))
		priv.Write(enc)
		priv.WriteString(" NP\n")
	}
	priv.WriteString("ND\n")
	glyphs := testGlyphs()
	names := []string{notdefName, "A", "B", "C", "D", "e", glyphAcute, glyphEacute}
	fmt.Fprintf(&priv, "/CharStrings %d dict dup begin\n", len(glyphs))
	for _, name := range names {
		enc := encryptT1(glyphs[name], charstringR, 4)
		fmt.Fprintf(&priv, "/%s %d RD ", name, len(enc))
		priv.Write(enc)
		priv.WriteString(" ND\n")
	}
	priv.WriteString("end\nend\n")

	encPart := encryptT1(priv.Bytes(), eexecR, 4)
	if hexForm {
		var hexed bytes.Buffer
		for i, b := range encPart {
			fmt.Fprintf(&hexed, "%02x", b)
			if i%32 == 31 {
				hexed.WriteByte('\n')
			}
		}
		encPart = hexed.Bytes()
	}

	var out bytes.Buffer
	if pfb {
		writeSeg := func(kind byte, data []byte) {
			out.WriteByte(0x80)
			out.WriteByte(kind)
			n := len(data)
			out.Write([]byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)})
			out.Write(data)
		}
		writeSeg(0x01, clearBuf.Bytes())
		writeSeg(0x02, encPart)
		writeSeg(0x01, []byte(trailer()))
		out.Write([]byte{0x80, 0x03})
	} else {
		out.Write(clearBuf.Bytes())
		out.Write(encPart)
		out.WriteString(trailer())
	}
	return out.Bytes()
}

func trailer() string {
	var sb bytes.Buffer
	sb.WriteByte('\n')
	for range 8 {
		for range 64 {
			sb.WriteByte('0')
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("cleartomark\n")
	return sb.String()
}

// ---- tests ----------------------------------------------------------------------------------------------------

// stdEncForSeac supplies the two codes the test font's seac uses.
func stdEncForSeac() *[256]string {
	var t [256]string
	t[101] = "e"
	t[194] = glyphAcute
	return &t
}

func parseTestFont(t *testing.T, hexForm, pfb, stdEncoding bool) *Font {
	t.Helper()
	f, err := Parse(buildTestFont(hexForm, pfb, stdEncoding))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f.StdEnc = stdEncForSeac()
	return f
}

func TestParseContainerForms(t *testing.T) {
	for _, tc := range []struct {
		name         string
		hexForm, pfb bool
	}{{"raw-binary", false, false}, {"pfa-hex", true, false}, {"pfb", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := parseTestFont(t, tc.hexForm, tc.pfb, false)
			if len(f.CharStrings) != 8 {
				t.Fatalf("CharStrings = %d, want 8", len(f.CharStrings))
			}
			if len(f.Subrs) != 5 {
				t.Fatalf("Subrs = %d, want 5", len(f.Subrs))
			}
			if !f.HasMatrix || f.FontMatrix != [6]float32{0.001, 0, 0, 0.001, 0, 0} {
				t.Errorf("FontMatrix = %v", f.FontMatrix)
			}
			if !f.HasBBox || f.FontBBox != [4]float32{0, -200, 1000, 800} {
				t.Errorf("FontBBox = %v", f.FontBBox)
			}
			if f.Encoding == nil || f.Encoding[65] != "A" || f.Encoding[233] != glyphEacute || f.Encoding[32] != "" {
				t.Errorf("built-in encoding not extracted")
			}
			if f.StdEncoding {
				t.Errorf("StdEncoding true for custom-encoded font")
			}
			if f.Names[0] != notdefName || len(f.Names) != 8 {
				t.Errorf("Names = %v", f.Names)
			}
		})
	}
}

func TestParseStandardEncoding(t *testing.T) {
	f := parseTestFont(t, false, false, true)
	if !f.StdEncoding || f.Encoding != nil {
		t.Errorf("StdEncoding = %v, Encoding = %v; want true, nil", f.StdEncoding, f.Encoding)
	}
}

// Glyph names used repeatedly across the fixtures.
const (
	glyphAcute  = "acute"
	glyphEacute = "eacute"
)

// seg is a decoded outline segment for compact comparison.
type seg struct {
	args []float32
	op   ot.SegmentOp
}

func flatten(segs []ot.Segment) []seg {
	out := make([]seg, 0, len(segs))
	for _, s := range segs {
		n := 1
		switch s.Op {
		case ot.SegmentOpQuadTo:
			n = 2
		case ot.SegmentOpCubeTo:
			n = 3
		}
		args := make([]float32, 0, n*2)
		for i := range n {
			args = append(args, s.Args[i].X, s.Args[i].Y)
		}
		out = append(out, seg{op: s.Op, args: args})
	}
	return out
}

func wantSegs(t *testing.T, got []ot.Segment, want []seg) {
	t.Helper()
	flat := flatten(got)
	if len(flat) != len(want) {
		t.Fatalf("segments = %d, want %d: %v", len(flat), len(want), flat)
	}
	for i, w := range want {
		g := flat[i]
		if g.op != w.op || len(g.args) != len(w.args) {
			t.Fatalf("segment %d = %v, want %v", i, g, w)
		}
		for j := range w.args {
			if math.Abs(float64(g.args[j]-w.args[j])) > 1e-4 {
				t.Fatalf("segment %d arg %d = %v, want %v", i, j, g.args, w.args)
			}
		}
	}
}

func TestGlyphBox(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	segs, adv, err := f.Glyph("A")
	if err != nil {
		t.Fatalf("Glyph(A): %v", err)
	}
	if adv != 600 {
		t.Errorf("advance = %v, want 600", adv)
	}
	wantSegs(t, segs, []seg{
		{op: ot.SegmentOpMoveTo, args: []float32{100, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{500, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{500, 700}},
		{op: ot.SegmentOpLineTo, args: []float32{100, 700}},
		{op: ot.SegmentOpLineTo, args: []float32{100, 0}},
	})
}

func TestGlyphCurvesDivAndSubr(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	segs, adv, err := f.Glyph("B")
	if err != nil {
		t.Fatalf("Glyph(B): %v", err)
	}
	if adv != 700 { // 1400 2 div
		t.Errorf("advance = %v, want 700", adv)
	}
	wantSegs(t, segs, []seg{
		{op: ot.SegmentOpMoveTo, args: []float32{20, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{220, 0}}, // Subr 4.
		{op: ot.SegmentOpCubeTo, args: []float32{220, 0, 320, 100, 320, 200}},
		{op: ot.SegmentOpCubeTo, args: []float32{320, 300, 370, 350, 470, 350}},
		{op: ot.SegmentOpCubeTo, args: []float32{520, 350, 570, 400, 570, 350}},
		{op: ot.SegmentOpLineTo, args: []float32{20, 0}},
	})
}

func TestGlyphFlex(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	segs, _, err := f.Glyph("C")
	if err != nil {
		t.Fatalf("Glyph(C): %v", err)
	}
	wantSegs(t, segs, []seg{
		{op: ot.SegmentOpMoveTo, args: []float32{0, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{100, 0}},
		{op: ot.SegmentOpCubeTo, args: []float32{133, 20, 166, 40, 200, 50}},
		{op: ot.SegmentOpCubeTo, args: []float32{233, 60, 266, 80, 300, 100}},
		{op: ot.SegmentOpLineTo, args: []float32{300, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{0, 0}},
	})
}

func TestGlyphSeac(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	segs, adv, err := f.Glyph(glyphEacute)
	if err != nil {
		t.Fatalf("Glyph(eacute): %v", err)
	}
	if adv != 550 { // The composite's own hsbw wins over both components'.
		t.Errorf("advance = %v, want 550", adv)
	}
	// Base "e" at its natural position, accent translated by (adx-asb, ady) = (220, 350): its own sbx 30 lands its
	// sidebearing point at x = 250.
	wantSegs(t, segs, []seg{
		{op: ot.SegmentOpMoveTo, args: []float32{50, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{350, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{350, 400}},
		{op: ot.SegmentOpLineTo, args: []float32{50, 400}},
		{op: ot.SegmentOpLineTo, args: []float32{50, 0}},
		{op: ot.SegmentOpMoveTo, args: []float32{250, 350}},
		{op: ot.SegmentOpLineTo, args: []float32{300, 450}},
		{op: ot.SegmentOpLineTo, args: []float32{250, 350}},
	})
}

func TestGlyphSbwAndMovetos(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	segs, adv, err := f.Glyph("D")
	if err != nil {
		t.Fatalf("Glyph(D): %v", err)
	}
	if adv != 400 {
		t.Errorf("advance = %v, want 400", adv)
	}
	wantSegs(t, segs, []seg{
		{op: ot.SegmentOpMoveTo, args: []float32{10, 20}},
		{op: ot.SegmentOpLineTo, args: []float32{10, 120}},
		{op: ot.SegmentOpLineTo, args: []float32{10, 20}}, // ClosePath before the hmoveto contour.
		{op: ot.SegmentOpMoveTo, args: []float32{-40, 120}},
		{op: ot.SegmentOpMoveTo, args: []float32{-40, 150}},
	})
}

func TestAdvanceOnly(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	for name, want := range map[string]float32{"A": 600, "B": 700, notdefName: 500, glyphEacute: 550} {
		if adv, ok := f.Advance(name); !ok || adv != want {
			t.Errorf("Advance(%s) = %v, %v; want %v", name, adv, ok, want)
		}
	}
	if _, ok := f.Advance("nosuchglyph"); ok {
		t.Errorf("Advance(nosuchglyph) succeeded")
	}
	// A charstring that never runs hsbw/sbw must report ok=false, not a spurious width of 0 — the caller relies on
	// this to fall back to another width source rather than record a bogus zero advance.
	f.CharStrings["nowidth"] = cs(0, 0, oRmoveto, 100, oHlineto, oEndchar)
	if adv, ok := f.Advance("nowidth"); ok {
		t.Errorf("Advance(nowidth) = %v, %v; want ok=false", adv, ok)
	}
	// A charstring whose hsbw genuinely sets width 0 must still report ok=true, distinguishing it from the above.
	f.CharStrings["zerowidth"] = cs(0, 0, oHsbw, oEndchar)
	if adv, ok := f.Advance("zerowidth"); !ok || adv != 0 {
		t.Errorf("Advance(zerowidth) = %v, %v; want 0, true", adv, ok)
	}
}

func TestGlyphErrors(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	if _, _, err := f.Glyph("nosuchglyph"); err == nil {
		t.Errorf("Glyph(nosuchglyph) succeeded")
	}
	// A charstring that divides by zero must degrade to an error, not a panic or non-finite geometry.
	f.CharStrings["bad"] = cs(0, 500, oHsbw, 1, 0, oDiv, 0, oRlineto, oEndchar)
	if _, _, err := f.Glyph("bad"); err == nil {
		t.Errorf("division by zero did not error")
	}
	// Truncated and empty charstrings degrade too.
	f.CharStrings["trunc"] = []byte{255, 0}
	if _, _, err := f.Glyph("trunc"); err == nil {
		t.Errorf("truncated number did not error")
	}
	f.CharStrings["empty"] = nil
	if segs, _, err := f.Glyph("empty"); err != nil || len(segs) != 0 {
		t.Errorf("empty charstring: segs=%v err=%v", segs, err)
	}
	// Hostile amplification: a huge run of drawing operators must trip the segment cap, not chew memory.
	huge := cs(0, 500, oHsbw, 0, 0, oRmoveto)
	step := cs(1, 1, oRlineto)
	for range maxSegments + 64 {
		huge = append(huge, step...)
	}
	f.CharStrings["huge"] = append(huge, oEndchar...)
	if _, _, err := f.Glyph("huge"); err == nil {
		t.Errorf("segment flood did not error")
	}
}

// ---- non-finite and out-of-range operands ---------------------------------------------------------------------
//
// Type 1's charstring number encoding carries int32s only, but div composes them into arbitrary float64s — including
// ±Inf and NaN — and the scanner's numbers come from strconv.ParseFloat, which accepts "1e300", "inf" and "nan". Every
// place either kind of value is converted to an int must reject it in float space, because Go leaves an out-of-range
// float→int conversion implementation-defined and amd64 and arm64 disagree (amd64 wraps to the minimum integer, arm64
// saturates and maps NaN to 0). These tests pin the converted results and the behavior that depends on them.

// csScaleUp returns a charstring that pushes a seed of 2^30 and then multiplies the top of the stack by 2^30 k times:
// each repetition of "1 2^30 div div" leaves 2^-30 on top and then divides the value beneath it by that. It is the
// only way a charstring reaches a value the number encoding cannot express.
func csScaleUp(k int) []byte {
	out := cs(1 << 30)
	for range k {
		out = append(out, cs(1, 1<<30, oDiv, oDiv)...)
	}
	return out
}

// csInf leaves +Inf on the argument stack (2^30 scaled up past the float64 range).
func csInf() []byte { return csScaleUp(35) }

// csNaN leaves NaN on the argument stack (Inf divided by Inf).
func csNaN() []byte { return append(append(csInf(), csInf()...), oDiv...) }

func TestCsIndex(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    float64
		maxV int
		want int
	}{
		{name: "zero", v: 0, maxV: 255, want: 0},
		{name: "in range", v: 200, maxV: 255, want: 200},
		{name: "at max", v: 255, maxV: 255, want: 255},
		{name: "truncates", v: 200.9, maxV: 255, want: 200},
		{name: "over max", v: 256, maxV: 255, want: -1},
		{name: "negative", v: -1, maxV: 255, want: -1},
		{name: "NaN", v: math.NaN(), maxV: 255, want: -1},
		{name: "+Inf", v: math.Inf(1), maxV: 255, want: -1},
		{name: "-Inf", v: math.Inf(-1), maxV: 255, want: -1},
		{name: "huge finite", v: 1e300, maxV: math.MaxInt32, want: -1},
		{name: "+Inf at int32 max", v: math.Inf(1), maxV: math.MaxInt32, want: -1},
		{name: "int32 max", v: math.MaxInt32, maxV: math.MaxInt32, want: math.MaxInt32},
	} {
		if got := csIndex(tc.v, tc.maxV); got != tc.want {
			t.Errorf("csIndex(%v, %d) = %d, want %d", tc.v, tc.maxV, got, tc.want)
		}
	}
}

// TestCharstringInfIsReachable is the premise the tests below rest on: the div chain really does produce +Inf on the
// argument stack, so operators that consume it see a non-finite operand.
func TestCharstringInfIsReachable(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	f.CharStrings["inf"] = append(cs(0, 500, oHsbw, 0, 0, oRmoveto), append(csInf(), cs(0, oRlineto, oEndchar)...)...)
	segs, _, err := f.Glyph("inf")
	if err != nil {
		t.Fatalf("Glyph(inf): %v", err)
	}
	got := flatten(segs)
	if len(got) < 2 || !math.IsInf(float64(got[1].args[0]), 1) {
		t.Fatalf("div chain did not reach +Inf: %v", got)
	}
}

func TestSeacRejectsNonFiniteComponentCodes(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	// bchar and achar are non-finite: seac must degrade like a missing encoding (draw nothing, no error), the same way
	// on every architecture, rather than converting NaN to a code that happens to be in range.
	for _, tc := range []struct {
		name string
		code []byte
	}{
		{name: "seacNaN", code: csNaN()},
		{name: "seacInf", code: csInf()},
		{name: "seacHuge", code: csScaleUp(2)}, // 2^90: finite, but far outside both int32 and the 0-255 code range.
	} {
		body := cs(0, 500, oHsbw, 0, 0, 0)
		body = append(body, tc.code...)
		body = append(body, tc.code...)
		f.CharStrings[tc.name] = append(body, oSeac...)
		segs, adv, err := f.Glyph(tc.name)
		if err != nil {
			t.Errorf("Glyph(%s): %v", tc.name, err)
			continue
		}
		if len(segs) != 0 {
			t.Errorf("Glyph(%s) drew %d segments, want none", tc.name, len(segs))
		}
		if adv != 500 {
			t.Errorf("Glyph(%s) advance = %v, want 500", tc.name, adv)
		}
	}
}

func TestCallOtherSubrRejectsNonFiniteArgCount(t *testing.T) {
	f := parseTestFont(t, false, false, false)
	// The argument count selects the slice of arguments handed to the othersubr, so one that does not convert is fatal
	// — deterministically, rather than depending on whether the CPU maps NaN to 0.
	for _, tc := range []struct {
		name  string
		count []byte
	}{
		{name: "countNaN", count: csNaN()},
		{name: "countInf", count: csInf()},
		{name: "countHuge", count: csScaleUp(2)},
	} {
		f.CharStrings[tc.name] = append(append(cs(0, 500, oHsbw), tc.count...), cs(3, oOthersubr, oEndchar)...)
		if _, _, err := f.Glyph(tc.name); err == nil {
			t.Errorf("Glyph(%s) succeeded, want an error", tc.name)
		}
	}
	// An othersubr *number* that does not convert is merely an othersubr this interpreter does not implement: its
	// arguments pass through to the PostScript stack and the glyph keeps drawing.
	body := append(cs(0, 500, oHsbw, 0), csNaN()...)
	f.CharStrings["whichNaN"] = append(body, cs(oOthersubr, 0, 0, oRmoveto, 100, oHlineto, oEndchar)...)
	segs, _, err := f.Glyph("whichNaN")
	if err != nil {
		t.Fatalf("Glyph(whichNaN): %v", err)
	}
	wantSegs(t, segs, []seg{
		{op: ot.SegmentOpMoveTo, args: []float32{0, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{100, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{0, 0}}, // endchar closes the contour.
	})
}

func TestToInt64(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    float64
		want int64
		ok   bool
	}{
		{name: "zero", v: 0, want: 0, ok: true},
		{name: "positive", v: 42, want: 42, ok: true},
		{name: "negative", v: -7, want: -7, ok: true},
		{name: "truncates toward zero", v: 3.9, want: 3, ok: true},
		{name: "truncates negative toward zero", v: -3.9, want: -3, ok: true},
		{name: "min int64", v: math.MinInt64, want: math.MinInt64, ok: true},
		// The nearest float64 to math.MaxInt64 is 2^63, one past the representable range.
		{name: "max int64 rounds up out of range", v: math.MaxInt64},
		{name: "below min int64", v: -1e300},
		{name: "above max int64", v: 1e300},
		{name: "NaN", v: math.NaN()},
		{name: "+Inf", v: math.Inf(1)},
		{name: "-Inf", v: math.Inf(-1)},
	} {
		got, ok := toInt64(tc.v)
		if got != tc.want || ok != tc.ok {
			t.Errorf("toInt64(%v) = %d, %v; want %d, %v", tc.v, got, ok, tc.want, tc.ok)
		}
	}
}

func TestScannerIntegerRejectsUnconvertibleNumbers(t *testing.T) {
	s := scanner{data: []byte("42 -7 3.9 1e300 -1e300 inf -inf nan 9223372036854775807 notanumber")}
	for _, tc := range []struct {
		want int64
		ok   bool
	}{
		{want: 42, ok: true},
		{want: -7, ok: true},
		{want: 3, ok: true},
		{}, // 1e300
		{}, // -1e300
		{}, // inf
		{}, // -inf
		{}, // nan
		{}, // 9223372036854775807 parses to 2^63, which no int64 holds.
		{}, // notanumber is a keyword, not a number.
	} {
		got, ok := s.integer()
		if got != tc.want || ok != tc.ok {
			t.Errorf("integer() = %d, %v; want %d, %v", got, ok, tc.want, tc.ok)
		}
	}
}

// buildHostileNumberFont assembles a minimal program whose /Encoding codes, /lenIV, /Subrs index, and one charstring
// length are written as numbers no integer can represent, alongside well-formed entries the parser must still find.
func buildHostileNumberFont() []byte {
	var clearBuf bytes.Buffer
	clearBuf.WriteString("%!PS-AdobeFont-1.0: HostileNumbers 001.000\n")
	clearBuf.WriteString("/FontName /HostileNumbers def\n")
	clearBuf.WriteString("/Encoding 256 array\n")
	clearBuf.WriteString("dup 1e300 /B put\ndup nan /C put\ndup -inf /D put\ndup 65 /A put\nreadonly def\n")
	clearBuf.WriteString("currentdict end\ncurrentfile eexec\n")

	sub := encryptT1(cs(200, 0, oRlineto, oReturn), charstringR, 4)
	var priv bytes.Buffer
	priv.WriteString("  dup /Private 15 dict dup begin\n")
	priv.WriteString("/lenIV inf def\n") // Ignored: lenIV stays at the default 4, so the charstrings below decrypt.
	priv.WriteString("/Subrs 2 array\n")
	for _, idx := range []string{"1e300", "0"} { // The unconvertible index is discarded; the scan resyncs after it.
		fmt.Fprintf(&priv, "dup %s %d RD ", idx, len(sub))
		priv.Write(sub)
		priv.WriteString(" NP\n")
	}
	priv.WriteString("ND\n")
	glyphs := map[string][]byte{
		notdefName: cs(0, 500, oHsbw, oEndchar),
		"A":        cs(0, 600, oHsbw, 0, 0, oRmoveto, 0, oCallsubr, oClosepath, oEndchar),
	}
	priv.WriteString("/CharStrings 3 dict dup begin\n")
	for _, name := range []string{notdefName, "A"} {
		enc := encryptT1(glyphs[name], charstringR, 4)
		fmt.Fprintf(&priv, "/%s %d RD ", name, len(enc))
		priv.Write(enc)
		priv.WriteString(" ND\n")
	}
	priv.WriteString("/Z 1e300 ND\n") // An unconvertible length: the entry is skipped, the scan continues.
	priv.WriteString("end\nend\n")

	var out bytes.Buffer
	out.Write(clearBuf.Bytes())
	out.Write(encryptT1(priv.Bytes(), eexecR, 4))
	out.WriteString(trailer())
	return out.Bytes()
}

func TestParseIgnoresUnconvertibleNumbers(t *testing.T) {
	f, err := Parse(buildHostileNumberFont())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Encoding == nil {
		t.Fatalf("Encoding = nil, want the built-in array")
	}
	for code, want := range map[int]string{65: "A"} {
		if f.Encoding[code] != want {
			t.Errorf("Encoding[%d] = %q, want %q", code, f.Encoding[code], want)
		}
	}
	for _, name := range []string{"B", "C", "D"} {
		if slices.Contains(f.Encoding[:], name) {
			t.Errorf("Encoding holds %q, which only an unconvertible code assigned", name)
		}
	}
	if _, ok := f.CharStrings["Z"]; ok {
		t.Errorf("CharStrings holds Z, whose length was unconvertible")
	}
	// lenIV survived as the default, so the well-formed glyph decrypts and its subr call (stored at the well-formed
	// index, after the unconvertible one was discarded without desynchronizing the scan) draws.
	segs, adv, err := f.Glyph("A")
	if err != nil {
		t.Fatalf("Glyph(A): %v", err)
	}
	if adv != 600 {
		t.Errorf("advance = %v, want 600", adv)
	}
	wantSegs(t, segs, []seg{
		{op: ot.SegmentOpMoveTo, args: []float32{0, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{200, 0}},
		{op: ot.SegmentOpLineTo, args: []float32{0, 0}},
	})
}

func TestParseRejectsJunk(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		[]byte("not a font at all"),
		[]byte("%!PS-AdobeFont-1.0\n/FontName /X def\n"),           // No eexec.
		{0x80, 0x01, 0xff, 0xff, 0xff, 0x7f, 'x'},                  // PFB length overrun.
		append([]byte("eexec\n"), bytes.Repeat([]byte{7}, 100)...), // Decrypts to junk: no charstrings.
	} {
		if f, err := Parse(data); err == nil {
			t.Errorf("Parse(%q...) succeeded: %v", data[:min(len(data), 12)], f)
		}
	}
}
