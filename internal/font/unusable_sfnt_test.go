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
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"testing"
)

// buildSFNT assembles a TrueType-flavored sfnt from whole tables, keyed by four-character tag. The records are written
// in tag order, as the format requires.
func buildSFNT(tbls map[string][]byte) []byte {
	tags := slices.Sorted(maps.Keys(tbls))
	headerLen := 12 + 16*len(tags)
	total := headerLen
	for _, data := range tbls {
		total += (len(data) + 3) &^ 3 // Tables begin on a four-byte boundary.
	}
	out := make([]byte, headerLen, total)
	binary.BigEndian.PutUint32(out, 0x00010000) // sfntVersion: TrueType outlines
	binary.BigEndian.PutUint16(out[4:], uint16(len(tags)))
	// searchRange/entrySelector/rangeShift stay zero: no parser here reads them.
	for i, tag := range tags {
		rec := out[12+16*i:]
		copy(rec, tag)
		// rec[4:8] is the checksum, which goes unverified.
		binary.BigEndian.PutUint32(rec[8:], uint32(len(out)))
		binary.BigEndian.PutUint32(rec[12:], uint32(len(tbls[tag])))
		out = append(out, tbls[tag]...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	return out
}

// headTable builds a valid head table with the given unitsPerEm.
func headTable(upem uint16) []byte {
	t := make([]byte, 54)
	binary.BigEndian.PutUint32(t, 0x00010000)      // version
	binary.BigEndian.PutUint32(t[12:], 0x5F0F3CF5) // magicNumber
	binary.BigEndian.PutUint16(t[18:], upem)       // unitsPerEm
	binary.BigEndian.PutUint16(t[50:], 0)          // indexToLocFormat: short
	binary.BigEndian.PutUint16(t[52:], 0)          // glyphDataFormat
	return t
}

// hheaTable builds a valid hhea table carrying the given ascender/descender in font units.
func hheaTable(ascender, descender int16) []byte {
	t := make([]byte, 36)
	binary.BigEndian.PutUint32(t, 0x00010000) // version
	binary.BigEndian.PutUint16(t[4:], uint16(ascender))
	binary.BigEndian.PutUint16(t[6:], uint16(descender))
	binary.BigEndian.PutUint16(t[34:], 1) // numberOfHMetrics
	return t
}

// metricsOnlySFNT is an sfnt that parses far enough to yield quad metrics (head + hhea) but supplies no outlines at
// all: go-text rejects it (no cmap/maxp) and the direct glyf walker cannot be built (no glyf/loca). A truncated
// FontFile2 lands here, as does the mislabeled-but-real-world case of a CFF-flavored OpenType program dropped into
// FontFile2.
func metricsOnlySFNT() []byte {
	return buildSFNT(map[string][]byte{
		"head": headTable(1000),
		"hhea": hheaTable(900, -300),
	})
}

// TestMetricsOnlySFNTIsNotAnEmbeddedProgram pins the simple-font rule that the shapes and the quad metrics come from
// the same font. A FontFile2 that parses but renders nothing is not an embedded program: its glyphs come from the
// Liberation substitute, so its metrics must come from the substitute pins too (the descriptor's 0.7/-0.15 here, not
// the discarded program's 0.9/-0.3).
func TestMetricsOnlySFNTIsNotAnEmbeddedProgram(t *testing.T) {
	sfnt := metricsOnlySFNT()
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /TrueType /BaseFont /TestSimple /FontDescriptor 2 0 R >>",
		"<< /Type /FontDescriptor /FontName /TestSimple /Flags 32 /Ascent 700 /Descent -150 /FontFile2 3 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(sfnt), sfnt))
	if err != nil {
		t.Fatal(err)
	}
	if f.sfnt != nil {
		t.Fatal("an sfnt with neither a face nor a glyf walker was kept as the embedded program")
	}
	if f.sub == nil {
		t.Fatal("no substitute was loaded for a font whose only program draws nothing")
	}
	if f.ascender != 0.7 || f.descender != -0.15 {
		t.Errorf("ascender/descender = %v/%v, want 0.7/-0.15 (the substitute's pins, not the discarded program's)",
			f.ascender, f.descender)
	}
}

// TestMetricsOnlySFNTFallsThroughToNextFontFile is the same rule from the dispatch side: a FontFile2 that yields no
// outlines must not end the search, so a descriptor carrying a usable FontFile3 alongside it still renders its real
// glyphs.
func TestMetricsOnlySFNTFallsThroughToNextFontFile(t *testing.T) {
	sfnt := metricsOnlySFNT()
	endchar := []byte{139, 14} // "0 endchar": a valid, empty charstring.
	cff := buildGlyphCFF([]byte{
		28, 0xFF, 0x5A, // -166
		28, 0xFF, 0x2A, // -214
		28, 0x04, 0x34, // 1076
		28, 0x03, 0xB8, // 952
		5, // FontBBox
	}, endchar, endchar, endchar)
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /TrueType /BaseFont /TestSimple /FontDescriptor 2 0 R >>",
		"<< /Type /FontDescriptor /FontName /TestSimple /Flags 4 /FontFile2 3 0 R /FontFile3 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(sfnt), sfnt),
		fmt.Sprintf("<< /Length %d /Subtype /Type1C >>\nstream\n%s\nendstream", len(cff), cff))
	if err != nil {
		t.Fatal(err)
	}
	if f.cff == nil {
		t.Error("the outline-less FontFile2 ended the dispatch and the FontFile3 program was never read")
	}
	if f.sub != nil {
		t.Error("a substitute was loaded alongside the embedded CFF")
	}
	if f.ascender < 0.951 || f.ascender > 0.953 {
		t.Errorf("ascender = %v, want ≈0.952 from the FontFile3 CFF FontBBox", f.ascender)
	}
}

// TestType0MetricsOnlySFNTSubstitutes pins the composite-font counterpart. Font.GlyphPath draws a CIDFontType2 only
// through the direct glyf walker, so an sfnt without one renders nothing; such a font must substitute instead of
// vanishing.
func TestType0MetricsOnlySFNTSubstitutes(t *testing.T) {
	sfnt := metricsOnlySFNT()
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /Type0 /BaseFont /TestCID /Encoding /Identity-H /DescendantFonts [2 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /TestCID /FontDescriptor 3 0 R /CIDToGIDMap /Identity >>",
		"<< /Type /FontDescriptor /FontName /TestCID /Flags 4 /Ascent 700 /Descent -150 /FontFile2 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(sfnt), sfnt))
	if err != nil {
		t.Fatal(err)
	}
	if f.type0.sfnt != nil {
		t.Fatal("a CIDFontType2 program with no glyf walker was kept as the embedded program")
	}
	if f.sub == nil {
		t.Fatal("no substitute was loaded for a composite font whose program draws nothing")
	}
	if f.ascender != 0.7 || f.descender != -0.15 {
		t.Errorf("ascender/descender = %v/%v, want 0.7/-0.15 (the substitute's pins)", f.ascender, f.descender)
	}
}

// TestType0GlyfOnlySFNTStaysEmbedded is the negative control: a CIDFontType2 program that does carry a glyf walker is
// still the glyph source, substitute-free, even though go-text rejects the cmap-less subset.
func TestType0GlyfOnlySFNTStaysEmbedded(t *testing.T) {
	loca := make([]byte, 6) // Three short entries, all zero: two empty glyphs.
	sfnt := buildSFNT(map[string][]byte{
		"head": headTable(1000),
		"hhea": hheaTable(900, -300),
		"maxp": {0, 0, 0x50, 0, 0, 2}, // version 0.5, numGlyphs 2
		"loca": loca,
		"glyf": {},
	})
	f, err := loadFromDict(t,
		"<< /Type /Font /Subtype /Type0 /BaseFont /TestCID /Encoding /Identity-H /DescendantFonts [2 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /TestCID /FontDescriptor 3 0 R /CIDToGIDMap /Identity >>",
		"<< /Type /FontDescriptor /FontName /TestCID /Flags 4 /Ascent 700 /Descent -150 /FontFile2 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(sfnt), sfnt))
	if err != nil {
		t.Fatal(err)
	}
	if f.type0.sfnt == nil {
		t.Fatal("a CIDFontType2 program with a usable glyf walker was discarded")
	}
	if f.sub != nil {
		t.Error("a substitute was loaded alongside the embedded program")
	}
	if f.ascender != 0.9 || f.descender != -0.3 {
		t.Errorf("ascender/descender = %v/%v, want 0.9/-0.3 from the embedded hhea", f.ascender, f.descender)
	}
}
