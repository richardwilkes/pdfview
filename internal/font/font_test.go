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
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/font/data"
)

// minimalPDF wraps object bodies (starting at object 1) into a parseable document.
func minimalPDF(bodies ...string) string {
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, body := range bodies {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Catalog >>\nendobj\n", len(bodies)+1)
	fmt.Fprintf(&b, "trailer\n<< /Root %d 0 R /Size %d >>\nstartxref\n0\n%%%%EOF\n", len(bodies)+1, len(bodies)+2)
	return b.String()
}

func loadFromDict(t *testing.T, bodies ...string) (*Font, error) {
	t.Helper()
	d, err := cos.Open([]byte(minimalPDF(bodies...)))
	if err != nil {
		t.Fatal(err)
	}
	dict, ok := cos.AsDict(d.LoadObject(1))
	if !ok {
		t.Fatal("object 1 is not a dict")
	}
	return Load(d, dict)
}

const (
	glyphAlpha = "alpha"
	glyphSpace = "space"
)

func TestBundledData(t *testing.T) {
	for name, spot := range map[string]struct {
		glyph string
		width uint16
	}{
		stdHelvetica:     {"T", 611},
		stdHelveticaBold: {"T", 611},
		stdTimesRoman:    {"w", 722},
		stdCourier:       {"anything-monospaced", 0}, // checked separately below
		stdSymbol:        {glyphAlpha, 631},
		stdZapfDingbats:  {"a9", 577},
	} {
		widths := data.AFMWidths(name)
		if widths == nil {
			t.Fatalf("AFMWidths(%q) = nil", name)
		}
		if spot.width != 0 && widths[spot.glyph] != spot.width {
			t.Errorf("%s %s width = %d, want %d", name, spot.glyph, widths[spot.glyph], spot.width)
		}
	}
	for _, name := range []string{
		stdCourier, stdCourierBold, stdCourierBoldOblique, stdCourierOblique,
		stdHelveticaBoldOblique, stdHelveticaOblique,
		stdTimesBold, stdTimesBoldItalic, stdTimesItalic,
	} {
		if data.AFMWidths(name) == nil {
			t.Errorf("AFMWidths(%q) = nil", name)
		}
	}
	if w := data.AFMWidths(stdCourier)["m"]; w != 600 {
		t.Errorf("Courier m = %d, want 600", w)
	}
	if data.AFMWidths("NoSuchFont") != nil {
		t.Error("AFMWidths for unknown font should be nil")
	}
	symEnc := data.BuiltinEncoding(stdSymbol)
	if symEnc == nil || symEnc[97] != glyphAlpha || symEnc[32] != glyphSpace {
		t.Errorf("Symbol builtin encoding wrong: %v", symEnc != nil)
	}
	zdEnc := data.BuiltinEncoding(stdZapfDingbats)
	if zdEnc == nil || zdEnc[33] != "a1" || zdEnc[97] != "a60" {
		t.Error("ZapfDingbats builtin encoding wrong")
	}
	if data.BuiltinEncoding(stdHelvetica) != nil {
		t.Error("text fonts must not have builtin encodings in the bundle")
	}
	agl := data.AGL()
	if agl[glyphAlpha] != "α" || agl[glyphSpace] != " " || agl["ffi"] != "ﬃ" {
		t.Errorf("AGL spot checks failed: %q %q %q", agl[glyphAlpha], agl[glyphSpace], agl["ffi"])
	}
	for _, name := range []string{
		"LiberationMono-Bold", "LiberationMono-BoldItalic", "LiberationMono-Italic", "LiberationMono-Regular",
		"LiberationSans-Bold", "LiberationSans-BoldItalic", "LiberationSans-Italic", "LiberationSans-Regular",
		"LiberationSerif-Bold", "LiberationSerif-BoldItalic", "LiberationSerif-Italic", "LiberationSerif-Regular",
	} {
		ttf := data.Liberation(name)
		if len(ttf) < 1000 {
			t.Fatalf("Liberation(%q) too small: %d", name, len(ttf))
		}
		if info := parseSFNT(ttf); info == nil || info.ascender <= 0 || info.descender >= 0 {
			t.Errorf("Liberation(%q) does not parse as sfnt with sane metrics", name)
		}
	}
	if data.Liberation("NoSuchFace") != nil {
		t.Error("unknown Liberation face should be nil")
	}
}

func TestGlyphNameToUnicode(t *testing.T) {
	for name, want := range map[string]string{
		"A":             "A",
		"alpha":         "α",
		"uni0041":       "A",
		"uni00410042":   "AB",
		"u0041":         "A",
		"u1D400":        "\U0001D400",
		"f_f_i":         "ffi", // Components resolve individually.
		"A.sc":          "A",   // Suffix stripped.
		"uniD800":       "",    // Surrogates rejected.
		"bogusname":     "",
		"":              "",
		"A_uniD800":     "A",  // A bad component discards itself, not the components already resolved.
		"uniD800_A":     "A",  // Leading bad component skipped; later valid one kept.
		"A_bogus_B":     "AB", // Unrecognized middle component contributes nothing.
		"u0041_uABCDEF": "A",  // Out-of-range (> 0x10FFFF) component skipped, valid one kept.
	} {
		if got := GlyphNameToUnicode(name); got != want {
			t.Errorf("GlyphNameToUnicode(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestStandard14Name(t *testing.T) {
	const someRandom = "SomeRandomFont"
	for _, tc := range []struct {
		base  string
		want  string
		flags int
	}{
		{"Helvetica", stdHelvetica, 0},
		{"ArialMT", stdHelvetica, 0},
		{"Arial,BoldItalic", stdHelveticaBoldOblique, 0},
		{"Arial-BoldMT", stdHelveticaBold, 0},
		{"TimesNewRomanPS-ItalicMT", stdTimesItalic, 0},
		{"CourierNewPSMT", stdCourier, 0},
		{"Symbol", stdSymbol, 0},
		{"HelveticaLTStd-Bold", stdHelveticaBold, 32},
		{someRandom, stdHelvetica, 0},
		{someRandom, stdTimesRoman, FlagSerif},
		{someRandom, stdCourierOblique, FlagFixedPitch | FlagItalic},
		{"Garamond-BoldItalic", stdTimesBoldItalic, 0},
	} {
		if got := standard14Name(tc.base, tc.flags); got != tc.want {
			t.Errorf("standard14Name(%q, %d) = %q, want %q", tc.base, tc.flags, got, tc.want)
		}
	}
	for in, want := range map[string]string{
		"ABCDEF+Real-Name": "Real-Name",
		"ABCDE+NoStrip":    "ABCDE+NoStrip", // Prefix must be exactly six uppercase letters.
		"abcdef+NoStrip":   "abcdef+NoStrip",
		"Plain":            "Plain",
	} {
		if got := stripSubsetPrefix(in); got != want {
			t.Errorf("stripSubsetPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadSimpleFont(t *testing.T) {
	// A Type1 with Differences, explicit widths, and a descriptor: /Widths wins, MissingWidth covers the rest, the
	// Differences name resolves through the AGL, and the descriptor supplies quad metrics.
	f, err := loadFromDict(t,
		`<< /Type /Font /Subtype /Type1 /BaseFont /GHIJKL+Whatever-Bold /FirstChar 65 /LastChar 66
		    /Widths [123 456] /FontDescriptor 2 0 R
		    /Encoding << /BaseEncoding /WinAnsiEncoding /Differences [65 /alpha] >> >>`,
		`<< /Type /FontDescriptor /FontName /Whatever-Bold /Flags 32 /MissingWidth 321
		    /Ascent 700 /Descent -150 >>`)
	if err != nil {
		t.Fatal(err)
	}
	if f.BaseFont != "Whatever-Bold" {
		t.Errorf("BaseFont = %q", f.BaseFont)
	}
	if got := f.Width(65); got != 0.123 {
		t.Errorf("Width(65) = %v, want 0.123", got)
	}
	if got := f.Width(66); got != 0.456 {
		t.Errorf("Width(66) = %v", got)
	}
	if got := f.Width(67); got != 0.321 {
		t.Errorf("Width(67) = %v, want MissingWidth 0.321", got)
	}
	if got := f.Unicode(65); got != 'α' {
		t.Errorf("Unicode(65) = %q, want alpha via Differences", got)
	}
	if got := f.Unicode(66); got != 'B' {
		t.Errorf("Unicode(66) = %q, want B via WinAnsi", got)
	}
	if f.Ascender() != 0.7 || f.Descender() != -0.15 {
		t.Errorf("metrics = %v/%v, want descriptor 0.7/-0.15", f.Ascender(), f.Descender())
	}

	// A bare standard-14 font: AFM widths through the encoding, pinned substitute metrics.
	f, err = loadFromDict(t, `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Width('T'); got != 0.611 {
		t.Errorf("Helvetica T width = %v, want 0.611 (AFM)", got)
	}
	if got := f.Width(' '); got != 0.278 {
		t.Errorf("Helvetica space width = %v, want 0.278", got)
	}
	if f.Ascender() != 1.075 || f.Descender() != -0.299 {
		t.Errorf("Helvetica substitute metrics = %v/%v, want 1.075/-0.299", f.Ascender(), f.Descender())
	}

	// Symbol's built-in encoding maps 'a' to alpha.
	f, err = loadFromDict(t, `<< /Type /Font /Subtype /Type1 /BaseFont /Symbol >>`)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Unicode('a'); got != 'α' {
		t.Errorf("Symbol Unicode('a') = %q, want alpha", got)
	}
	if got := f.Width('a'); got != 0.631 {
		t.Errorf("Symbol width('a') = %v, want 0.631", got)
	}

	// Unsupported subtypes degrade, malformed dictionaries error.
	if _, err = loadFromDict(t, `<< /Type /Font /Subtype /Type0 /BaseFont /X >>`); err == nil {
		t.Error("Type0 should not load yet")
	}
	if _, err = loadFromDict(t, `<< /Type /Font /Subtype /Nonsense >>`); err == nil {
		t.Error("subtype-less, basefont-less dict should not load")
	}
}

func TestForEachCodeStops(t *testing.T) {
	f, err := loadFromDict(t, `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	f.ForEachCode([]byte("abcdef"), func(_ uint32, oneByte bool) bool {
		if !oneByte {
			t.Error("simple fonts are one byte per code")
		}
		count++
		return count < 3
	})
	if count != 3 {
		t.Errorf("ForEachCode visited %d codes, want 3", count)
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// buildIndex encodes a CFF INDEX: count, offSize, count+1 one-byte offsets, then the data. The synthetic fonts here all
// stay well under the 255 bytes a one-byte offset can address.
func buildIndex(entries ...[]byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(byte(len(entries) >> 8))
	buf.WriteByte(byte(len(entries)))
	if len(entries) == 0 {
		return buf.Bytes()
	}
	buf.WriteByte(1) // offSize
	off := 1
	buf.WriteByte(byte(off))
	for _, e := range entries {
		off += len(e)
		buf.WriteByte(byte(off))
	}
	for _, e := range entries {
		buf.Write(e)
	}
	return buf.Bytes()
}

// buildCFF assembles a minimal CFF: header, Name INDEX (one name), Top DICT INDEX with the given dict bytes. It is
// enough for the Top DICT parser, but not for a glyph-loading parse — see buildGlyphCFF for that.
func buildCFF(dict []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{1, 0, 4, 1}) // major, minor, hdrSize, offSize
	buf.Write(buildIndex([]byte("Test")))
	buf.Write(buildIndex(dict))
	return buf.Bytes()
}

// buildGlyphCFF assembles a CFF complete enough for go-text's cff.Parse to yield loadable glyphs: the Top DICT holds
// the caller's entries plus a CharStrings offset, and the CharStrings INDEX follows the empty String and Global Subr
// INDEXes. The charset defaults to the predefined ISOAdobe one and the Private DICT is absent, both of which cff.Parse
// accepts.
func buildGlyphCFF(dict []byte, charstrings ...[]byte) []byte {
	dict = append(append([]byte{}, dict...), 29, 0, 0, 0, 0, 17) // 32-bit CharStrings offset (patched below), then op 17
	head := []byte{1, 0, 4, 1}
	name := buildIndex([]byte("Test"))
	empty := buildIndex() // The String INDEX and the Global Subr INDEX.
	binary.BigEndian.PutUint32(dict[len(dict)-5:], uint32(len(head)+len(name)+len(buildIndex(dict))+2*len(empty)))
	var buf bytes.Buffer
	buf.Write(head)
	buf.Write(name)
	buf.Write(buildIndex(dict))
	buf.Write(empty)
	buf.Write(empty)
	buf.Write(buildIndex(charstrings...))
	return buf.Bytes()
}

func TestCFFTopDict(t *testing.T) {
	// FontBBox [-166 -214 1076 952] via 16-bit (28) and small-int operands, then op 5.
	dict := []byte{
		28, 0xFF, 0x5A, // -166
		28, 0xFF, 0x2A, // -214
		28, 0x04, 0x34, // 1076
		28, 0x03, 0xB8, // 952
		5,
	}
	top, err := parseCFFTopDict(buildCFF(dict))
	if err != nil {
		t.Fatal(err)
	}
	asc, desc, ok := top.metrics()
	if !ok || asc != 0.952 || desc != -0.214 {
		t.Errorf("metrics = %v/%v/%v, want 0.952/-0.214", asc, desc, ok)
	}

	// A FontMatrix halving the em (0.002 => upem 500) scales the metrics; reals use packed BCD (30).
	dict = append([]byte{
		30, 0x0a, 0x00, 0x2f, // .002 -> 0.002
		139 + 0, // 0
		139 + 0,
		30, 0x0a, 0x00, 0x2f,
		139 + 0,
		139 + 0,
		12, 7, // FontMatrix
	}, dict...)
	top, err = parseCFFTopDict(buildCFF(dict))
	if err != nil {
		t.Fatal(err)
	}
	asc, desc, ok = top.metrics()
	if !ok || abs32(asc-952.0/500) > 1e-5 || abs32(desc+214.0/500) > 1e-5 {
		t.Errorf("scaled metrics = %v/%v/%v", asc, desc, ok)
	}

	// Malformed data errors rather than panicking.
	for _, bad := range [][]byte{
		nil,
		{1, 0},
		{1, 0, 99, 1},
		buildCFF([]byte{29, 0, 0}), // truncated 32-bit operand
		buildCFF([]byte{30, 0xdd}), // reserved BCD nibble
		buildCFF([]byte{22}),       // reserved operator
		make([]byte, 64),           // zeroed soup
	} {
		if _, err = parseCFFTopDict(bad); err == nil {
			t.Errorf("parseCFFTopDict(%v) should fail", bad)
		}
	}
}

// TestType0DispatchByFontFile guards against trusting /Subtype over the actually-present FontFile: a descendant
// mislabeled "CIDFontType2" whose program really lives in FontFile3 (a CFF) must be parsed from FontFile3 and embedded,
// not routed into the SFNT arm with a nil stream and then substituted away.
func TestType0DispatchByFontFile(t *testing.T) {
	// A CFF carrying FontBBox [-166 -214 1076 952] so top.metrics() resolves and the font counts as embedded.
	cff := buildCFF([]byte{
		28, 0xFF, 0x5A, // -166
		28, 0xFF, 0x2A, // -214
		28, 0x04, 0x34, // 1076
		28, 0x03, 0xB8, // 952
		5, // FontBBox
	})
	f, err := loadFromDict(
		t,
		"<< /Type /Font /Subtype /Type0 /BaseFont /TestCID /Encoding /Identity-H /DescendantFonts [2 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /TestCID /FontDescriptor 3 0 R >>", // mislabeled subtype
		"<< /Type /FontDescriptor /FontName /TestCID /Flags 4 /FontFile3 4 0 R >>",
		fmt.Sprintf("<< /Length %d /Subtype /Type1C >>\nstream\n%s\nendstream", len(cff), cff),
	)
	if err != nil {
		t.Fatal(err)
	}
	if f.sub != nil {
		t.Fatal("FontFile3 CFF was discarded and the font substituted")
	}
	if f.ascender < 0.951 || f.ascender > 0.953 {
		t.Errorf("ascender = %v, want ≈0.952 from the embedded CFF FontBBox", f.ascender)
	}
}

// TestType0CFFWithoutFontBBoxKeepsItsGlyphs guards the CIDFontType0 substitute gate. A subset CFF whose Top DICT has no
// usable /FontBBox yields no metrics, but its charstrings are still the font's glyph source. Loading the Liberation
// substitute alongside them would leave Font.GID mapping codes through the substitute's cmap while Font.GlyphPath
// pulled those indices out of the embedded program — arbitrary glyph shapes, silently. Only the metrics fall back.
func TestType0CFFWithoutFontBBoxKeepsItsGlyphs(t *testing.T) {
	endchar := []byte{139, 14} // "0 endchar": a valid, empty charstring.
	cff := buildGlyphCFF(nil, endchar, endchar, endchar)
	f, err := loadFromDict(
		t,
		"<< /Type /Font /Subtype /Type0 /BaseFont /TestCID /Encoding /Identity-H /DescendantFonts [2 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /TestCID /FontDescriptor 3 0 R >>",
		"<< /Type /FontDescriptor /FontName /TestCID /Flags 4 /FontFile3 4 0 R >>",
		fmt.Sprintf("<< /Length %d /Subtype /CIDFontType0C >>\nstream\n%s\nendstream", len(cff), cff),
	)
	if err != nil {
		t.Fatal(err)
	}
	if f.cff == nil {
		t.Fatal("the embedded CFF supplied no glyph program")
	}
	if f.sub != nil {
		t.Fatal("a substitute was loaded alongside the embedded CFF, so GID and GlyphPath disagree")
	}
	if got := f.GID(2); got != 2 {
		t.Errorf("GID(2) = %d, want 2 through the embedded program (identity CID→GID)", got)
	}
	if f.ascender <= 0 || f.descender >= 0 {
		t.Errorf("metrics = %v/%v, want the substitute metrics fallback", f.ascender, f.descender)
	}
}

func TestType3NonStandardFontMatrixWidths(t *testing.T) {
	// A Type 3 font with a non-standard FontMatrix (0.01 => 10x the default 0.001) exercises the glyph-space→text-space
	// transform for both /Widths and /MissingWidth. /alpha (code 65) has an explicit width; code 66 falls through to
	// /MissingWidth. Both must scale by matrix[0], not by the default 1/1000.
	f, err := loadFromDict(t,
		`<< /Type /Font /Subtype /Type3 /FontBBox [0 0 750 750] /FontMatrix [0.01 0 0 0.01 0 0]
		    /CharProcs << /alpha 3 0 R >> /Encoding << /Differences [65 /alpha] >>
		    /FirstChar 65 /LastChar 65 /Widths [500] /FontDescriptor 2 0 R >>`,
		`<< /Type /FontDescriptor /FontName /T3 /Flags 4 /MissingWidth 321 >>`,
		"<< /Length 2 >>\nstream\nd0\nendstream")
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsType3() {
		t.Fatal("font did not load as Type 3")
	}
	// A raw glyph-space width of 500 scaled by matrix[0]=0.01 yields a text-space advance of 5.
	if got := f.Width(65); got != 5 {
		t.Errorf("Width(65) = %v, want 5 (/Widths through the FontMatrix)", got)
	}
	// A raw /MissingWidth of 321 scaled by matrix[0]=0.01 yields 3.21; before the fix this returned the raw 0.321.
	if got := f.Width(66); got < 3.209 || got > 3.211 {
		t.Errorf("Width(66) = %v, want ≈3.21 (/MissingWidth through the FontMatrix)", got)
	}
}
