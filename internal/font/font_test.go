package font

import (
	"bytes"
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
		"A":           "A",
		"alpha":       "α",
		"uni0041":     "A",
		"uni00410042": "AB",
		"u0041":       "A",
		"u1D400":      "\U0001D400",
		"f_f_i":       "ffi", // Components resolve individually.
		"A.sc":        "A",   // Suffix stripped.
		"uniD800":     "",    // Surrogates rejected.
		"bogusname":   "",
		"":            "",
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
	// A Type1 with Differences, explicit widths, and a descriptor: /Widths wins, MissingWidth covers the
	// rest, the Differences name resolves through the AGL, and the descriptor supplies quad metrics.
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

// buildCFF assembles a minimal CFF: header, Name INDEX (one name), Top DICT INDEX with the given dict bytes.
func buildCFF(dict []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{1, 0, 4, 1}) // major, minor, hdrSize, offSize
	index := func(entries ...[]byte) {
		buf.WriteByte(byte(len(entries) >> 8))
		buf.WriteByte(byte(len(entries)))
		if len(entries) == 0 {
			return
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
	}
	index([]byte("Test"))
	index(dict)
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
