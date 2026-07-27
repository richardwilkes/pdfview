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
	"testing"

	"github.com/go-text/typesetting/font/opentype"

	"github.com/richardwilkes/pdfview/internal/font/data"
)

// TestType0OpenTypeFontFile3IsEmbedded covers ISO 32000-2 9.9 Table 126, which permits /FontFile3 with
// /Subtype /OpenType for CIDFontType0 and CIDFontType2 alike. Reading such a stream only as bare CFF failed on the
// sfnt header and substituted the font away: its metrics fell back to the standard-14 pin and its glyphs came from
// Liberation through /ToUnicode — or, with no /ToUnicode, nothing rendered at all.
func TestType0OpenTypeFontFile3IsEmbedded(t *testing.T) {
	ttf := data.Liberation("LiberationSans-Regular")
	if ttf == nil {
		t.Fatal("bundled LiberationSans-Regular missing")
	}
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /Type0 /BaseFont /TestCID /Encoding /Identity-H /DescendantFonts [2 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /TestCID /FontDescriptor 3 0 R /CIDToGIDMap /Identity >>",
		"<< /Type /FontDescriptor /FontName /TestCID /Flags 4 /FontFile3 4 0 R >>",
		fmt.Sprintf("<< /Length %d /Subtype /OpenType >>\nstream\n%s\nendstream", len(ttf), ttf))
	if err != nil {
		t.Fatal(err)
	}
	if f.type0.sfnt == nil {
		t.Fatal("an OpenType program in /FontFile3 was not read as the composite font's embedded program")
	}
	if f.sub != nil {
		t.Error("a substitute was loaded alongside the embedded program, so GID and GlyphPath disagree")
	}
	// LiberationSans' hhea ascender is 1854 over a 2048 upem; the substitute pin it used to fall back to is 0.8.
	if f.ascender < 0.904 || f.ascender > 0.906 {
		t.Errorf("ascender = %v, want ≈0.905 from the embedded program's hhea", f.ascender)
	}
	gid := f.GID(0x24, 2) // Identity-H: CID 0x24 is GID 0x24, an outline-bearing glyph in Liberation.
	if gid != 0x24 {
		t.Fatalf("GID(0x24) = %d, want the identity mapping through the embedded program", gid)
	}
	if p := f.GlyphPath(gid); p == nil || p.IsEmpty() {
		t.Error("the embedded OpenType program drew no outline")
	}
}

// cmaplessLiberation rebuilds the bundled program without its cmap table — the exact shape a TrueType subset takes when
// its producer drops the table the PDF encoding makes redundant. go-text refuses such a program, so the direct glyf
// walker draws it and the go-text face is nil.
func cmaplessLiberation(t *testing.T, tags ...string) []byte {
	t.Helper()
	return buildSFNT(liberationTables(t, tags...))
}

// TestCmaplessTrueTypeWidthsComeFromHmtx pins the /Widths-absent fallback for a program go-text rejected. The loader
// had already suppressed the standard-14 AFM table (an embedded program was present), so with no advance source the
// font reported /MissingWidth — 0 by default — for every code while its outlines rendered correctly, piling the whole
// string onto one point.
func TestCmaplessTrueTypeWidthsComeFromHmtx(t *testing.T) {
	program := cmaplessLiberation(t, "head", "hhea", "maxp", "loca", "glyf", "hmtx")
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /TrueType /BaseFont /TestSans /FontDescriptor 2 0 R >>",
		"<< /Type /FontDescriptor /FontName /TestSans /Flags 32 /FontFile2 3 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(program), program))
	if err != nil {
		t.Fatal(err)
	}
	if f.sfnt == nil || f.sfnt.face != nil {
		t.Fatal("the fixture is meant to be a program go-text rejects, drawn by the glyf walker")
	}
	if f.sfnt.glyf == nil {
		t.Fatal("the glyf walker was not built, so the fixture draws nothing at all")
	}
	gid := f.GID('A', 1) // No cmap: the chain's last resort maps the code straight to the GID.
	if p := f.GlyphPath(gid); p == nil || p.IsEmpty() {
		t.Fatal("no outline for the cmap-less program's glyph")
	}
	// The advance must be the program's own hmtx entry for that glyph, which is what a go-text face would have
	// reported had it accepted the program.
	intact := parseSFNT(data.Liberation("LiberationSans-Regular"))
	if intact == nil || intact.face == nil {
		t.Fatal("the intact program did not parse")
	}
	want := intact.face.HorizontalAdvance(opentype.GID(gid)) / intact.upem
	if want <= 0 {
		t.Fatalf("the fixture glyph has no advance to compare against (%v)", want)
	}
	if got := f.Width('A', 1); got < want-0.001 || got > want+0.001 {
		t.Errorf("Width('A') = %v, want %v from the program's hmtx", got, want)
	}
}

// TestHmtxMatchesFaceAdvances pins the hmtx reader itself against go-text's, across the whole table including the
// monospace tail every glyph past numberOfHMetrics shares.
func TestHmtxMatchesFaceAdvances(t *testing.T) {
	info := parseSFNT(data.Liberation("LiberationSans-Regular"))
	if info == nil || info.face == nil || len(info.hmtx) == 0 {
		t.Fatal("the bundled program did not yield both a face and an hmtx table")
	}
	for _, gid := range []uint32{0, 1, 36, 65, uint32(len(info.hmtx)) - 1, uint32(info.nGlyphs) - 1} {
		want := info.face.HorizontalAdvance(opentype.GID(gid)) / info.upem
		got, ok := info.advance(gid)
		if !ok {
			t.Fatalf("advance(%d) reported no source", gid)
		}
		if got != want {
			t.Errorf("advance(%d) = %v, want %v", gid, got, want)
		}
	}
	// Past the program's glyph count there is no glyph, so there is no advance to report either.
	if _, ok := info.advance(uint32(info.nGlyphs)); ok {
		t.Error("advance past the last glyph reported a width")
	}
}

// TestNoAdvanceSourceFallsBackToAFM covers the other half of the same rule: a program with neither a go-text face nor
// an hmtx table supplies no advance at all, so a /Widths-less font must fall back to the standard-14 widths rather
// than to /MissingWidth.
func TestNoAdvanceSourceFallsBackToAFM(t *testing.T) {
	program := cmaplessLiberation(t, "head", "hhea", "maxp", "loca", "glyf")
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /TrueType /BaseFont /Helvetica /FontDescriptor 2 0 R >>",
		"<< /Type /FontDescriptor /FontName /Helvetica /Flags 32 /FontFile2 3 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(program), program))
	if err != nil {
		t.Fatal(err)
	}
	if f.sfnt == nil || f.sfnt.hasAdvances() {
		t.Fatal("the fixture is meant to carry an embedded program with no advance source")
	}
	if f.afm == nil {
		t.Fatal("no AFM fallback, so every code would take /MissingWidth")
	}
	if got := f.Width('A', 1); got < 0.666 || got > 0.668 {
		t.Errorf("Width('A') = %v, want ≈0.667 from the standard-14 AFM widths", got)
	}
}
