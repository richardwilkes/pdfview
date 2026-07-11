package font

import (
	"fmt"
	"testing"

	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/richardwilkes/pdfview/internal/font/data"
)

func TestSubstitutedGlyphMapping(t *testing.T) {
	f, err := loadFromDict(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	if err != nil {
		t.Fatal(err)
	}
	if f.sub == nil {
		t.Fatal("standard-14 font did not load a substitute face")
	}
	gidA := f.GID('A')
	if gidA == 0 {
		t.Fatal("code 'A' mapped to .notdef in the substitute")
	}
	if gidB := f.GID('B'); gidB == 0 || gidB == gidA {
		t.Errorf("code 'B' → %d, want nonzero and distinct from 'A' → %d", gidB, gidA)
	}
	p := f.GlyphPath(gidA)
	if p == nil || p.IsEmpty() {
		t.Fatal("no outline for 'A'")
	}
	// The outline must be em-normalized: every point of Liberation's 'A' sits inside the em box.
	for _, pt := range p.Points {
		if pt.X < -0.5 || pt.X > 1.5 || pt.Y < -0.5 || pt.Y > 1.5 {
			t.Fatalf("outline point %v far outside the em box; not em-normalized?", pt)
		}
	}
	if w := f.Width('A'); w != 0.667 {
		t.Errorf("Width('A') = %v, want the AFM 667/1000", w)
	}
	if f.GID(300) != 0 {
		t.Error("out-of-range code mapped")
	}
}

func TestZapfDingbatsSubstituteDrawsNothing(t *testing.T) {
	f, err := loadFromDict(t, "<< /Type /Font /Subtype /Type1 /BaseFont /ZapfDingbats >>")
	if err != nil {
		t.Fatal(err)
	}
	if f.sub == nil {
		t.Fatal("no substitute loaded")
	}
	// Code 97 is /a9 in the built-in encoding: the AGL cannot resolve it, so it must map to .notdef and
	// render nothing (drawing a Latin 'a' — the extraction fallback — would be the wrong glyph, and the
	// substitute's .notdef box would be ink the oracle never shows).
	if gid := f.GID(97); gid != 0 {
		t.Fatalf("ZapfDingbats code 97 mapped to %d, want .notdef", gid)
	}
	if p := f.GlyphPath(0); p != nil {
		t.Error("substituted .notdef produced an outline; must render nothing")
	}
	// The widths still come from the ZapfDingbats AFM (through its built-in encoding) so layout stays
	// oracle-exact even though nothing renders.
	name := data.BuiltinEncoding(stdZapfDingbats)[97]
	want := float32(data.AFMWidths(stdZapfDingbats)[name]) / 1000
	if name == "" || want == 0 {
		t.Fatalf("built-in encoding/AFM missing code 97 (name %q)", name)
	}
	if w := f.Width(97); w != want {
		t.Errorf("Width(97) = %v, want the AFM %v for %q", w, want, name)
	}
}

// embeddedTTFDict builds font-dictionary bodies embedding the bundled Liberation Sans as a FontFile2 stream:
// a real TrueType program with (3,1)/(1,0)/(0,x) cmaps, exercising the embedded sfnt paths end to end.
func embeddedTTFDict(t *testing.T, extra string) []string {
	t.Helper()
	ttf := data.Liberation("LiberationSans-Regular")
	if ttf == nil {
		t.Fatal("bundled LiberationSans-Regular missing")
	}
	return []string{
		"<< /Type /Font /Subtype /TrueType /BaseFont /TestSans " + extra +
			" /FontDescriptor 2 0 R >>",
		"<< /Type /FontDescriptor /FontName /TestSans /Flags 32 /FontFile2 3 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(ttf), ttf),
	}
}

func TestEmbeddedSFNTGlyphs(t *testing.T) {
	f, err := loadFromDict(t, embeddedTTFDict(t, "/Encoding /WinAnsiEncoding")...)
	if err != nil {
		t.Fatal(err)
	}
	if f.sfnt == nil || f.sfnt.face == nil {
		t.Fatal("embedded TrueType did not parse")
	}
	if f.sub != nil {
		t.Fatal("embedded font must not carry a substitute")
	}
	gidA := f.GID('A')
	if gidA == 0 {
		t.Fatal("'A' unmapped through the (3,1) cmap chain")
	}
	if p := f.GlyphPath(gidA); p == nil || p.IsEmpty() {
		t.Fatal("no outline for embedded 'A'")
	}
	// No /Widths: the advance must come from the program's hmtx (Liberation Sans 'A' is 1366/2048 em units),
	// not from the AFM table (which is reserved for substituted fonts).
	if w := f.Width('A'); w < 0.666 || w > 0.668 {
		t.Errorf("hmtx fallback width = %v, want ≈0.667", w)
	}
	if f.afm != nil {
		t.Error("embedded font loaded AFM widths")
	}
}

func TestEmbeddedJunkFallsBackToSubstitute(t *testing.T) {
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /TrueType /BaseFont /Broken /FontDescriptor 2 0 R >>",
		"<< /Type /FontDescriptor /FontName /Broken /Flags 32 /FontFile2 3 0 R >>",
		"<< /Length 24 >>\nstream\nnot a truetype font at all\nendstream",
	)
	if err != nil {
		t.Fatal(err)
	}
	if f.sfnt != nil {
		t.Fatal("junk program parsed?")
	}
	if f.sub == nil {
		t.Fatal("junk embedded program did not fall back to the Liberation substitute")
	}
	gid := f.GID('A')
	if gid == 0 {
		t.Fatal("'A' unmapped through the substitute")
	}
	if p := f.GlyphPath(gid); p == nil || p.IsEmpty() {
		t.Fatal("no outline through the substitute")
	}
}

// TestSFNTGIDChainOrder pins the pinned code→GID fallback order on hand-built subtables, independent of any
// real font file (plan.md font-pipeline table: non-symbolic name→AGL→(3,1) then name→MacRoman→(1,0);
// symbolic (3,0) bare then 0xF000-folded, then (1,0) raw; last-resort code→GID).
func TestSFNTGIDChainOrder(t *testing.T) {
	uni := &cmapTable{sub: tables.CmapSubtable12{Groups: []tables.SequentialMapGroup{
		{StartCharCode: 'A', EndCharCode: 'Z', StartGlyphID: 100},
	}}}
	sym := &cmapTable{sub: tables.CmapSubtable12{Groups: []tables.SequentialMapGroup{
		// Format 12 groups are sorted by start code, as the spec requires (the binary search relies on it).
		{StartCharCode: 0x30, EndCharCode: 0x39, StartGlyphID: 400},
		{StartCharCode: 0xF041, EndCharCode: 0xF05A, StartGlyphID: 300},
	}}}
	mac := &cmapTable{sub: tables.CmapSubtable12{Groups: []tables.SequentialMapGroup{
		{StartCharCode: 0xA5, EndCharCode: 0xA5, StartGlyphID: 500}, // Mac Roman bullet
		{StartCharCode: 'A', EndCharCode: 'Z', StartGlyphID: 200},
	}}}
	full := &sfntInfo{cmapUnicode: uni, cmapSymbol: sym, cmapMacRoman: mac, nGlyphs: 1000}

	// Non-symbolic: the AGL name resolves through the Unicode table first.
	if g := full.gid('A', "A", false); g != 100 {
		t.Errorf("nonsymbolic 'A' = %d, want 100 (Unicode table)", g)
	}
	// Without a Unicode table, the name's Mac Roman code drives the (1,0) lookup: /bullet is 0xA5 even
	// though the PDF code is arbitrary.
	noUni := &sfntInfo{cmapMacRoman: mac, nGlyphs: 1000}
	if g := noUni.gid(1, "bullet", false); g != 500 {
		t.Errorf("nonsymbolic /bullet = %d, want 500 (MacRoman (1,0))", g)
	}
	// Symbolic: (3,0) bare code first (digits live at 0x30 here), F000-folded next (letters), the name path
	// never consulted.
	if g := full.gid(0x31, "one", true); g != 401 {
		t.Errorf("symbolic 0x31 = %d, want 401 ((3,0) bare)", g)
	}
	if g := full.gid('B', "B", true); g != 301 {
		t.Errorf("symbolic 'B' = %d, want 301 ((3,0) 0xF000 fold)", g)
	}
	// Nothing maps: the code itself is the GID when in range.
	bare := &sfntInfo{nGlyphs: 50}
	if g := bare.gid(7, "unmappable", false); g != 7 {
		t.Errorf("fallback = %d, want code 7", g)
	}
	if g := bare.gid(77, "", false); g != 0 {
		t.Errorf("out-of-range fallback = %d, want 0", g)
	}
}

func TestCmapFormatLookups(t *testing.T) {
	f0 := tables.CmapSubtable0{}
	f0.GlyphIdArray[65] = 9
	c0 := &cmapTable{sub: f0}
	if g := c0.lookup(65); g != 9 {
		t.Errorf("format 0: %d, want 9", g)
	}
	if g := c0.lookup(300); g != 0 {
		t.Errorf("format 0 out of range: %d", g)
	}

	// Format 4, three segments: [0x20..0x22] via idDelta (gid = code+5), [0x41..0x42] via idRangeOffset into
	// GlyphIDArray, and the mandatory 0xFFFF terminator. The offset counts bytes from its own slot: slot 1
	// is followed by slot 2 (2 bytes) and then the array, so offset 4 addresses array index (c - 0x41).
	f4 := tables.CmapSubtable4{
		EndCode:        []uint16{0x22, 0x42, 0xFFFF},
		StartCode:      []uint16{0x20, 0x41, 0xFFFF},
		IdDelta:        []uint16{5, 0, 1},
		IdRangeOffsets: []uint16{0, 4, 0},
		GlyphIDArray:   []byte{0x00, 0x21, 0x00, 0x22}, // gids 33, 34
	}
	c4 := &cmapTable{sub: f4}
	for code, want := range map[uint32]uint32{
		0x20: 0x25, 0x22: 0x27, // delta segment
		0x41: 33, 0x42: 34, // range-offset segment
		0x23: 0, 0x40: 0, 0x43: 0, 0xFFFF: 0, 0x10000: 0, // misses
	} {
		if g := c4.lookup(code); g != want {
			t.Errorf("format 4 code %#x: %d, want %d", code, g, want)
		}
	}

	f6 := tables.CmapSubtable6{FirstCode: 0xF000, GlyphIdArray: []tables.GlyphID{11, 12, 13}}
	c6 := &cmapTable{sub: f6}
	if g := c6.lookup(0xF001); g != 12 {
		t.Errorf("format 6: %d, want 12", g)
	}
	if g := c6.lookup(0xEFFF); g != 0 {
		t.Errorf("format 6 below range: %d", g)
	}
	if g := c6.lookup(0xF003); g != 0 {
		t.Errorf("format 6 above range: %d", g)
	}
}

func TestMacRomanReverse(t *testing.T) {
	const bulletName = "bullet" // Named to keep goconst quiet about the generated tables' occurrences.
	for name, want := range map[string]uint32{"A": 65, bulletName: 0xA5, glyphSpace: 32} {
		got, ok := macRomanCode(name)
		if !ok || got != want {
			t.Errorf("macRomanCode(%q) = %d, %v; want %d", name, got, ok, want)
		}
	}
	if _, ok := macRomanCode("noSuchGlyphName"); ok {
		t.Error("unknown name resolved")
	}
}
