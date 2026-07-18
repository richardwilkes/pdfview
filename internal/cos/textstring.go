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
	"unicode/utf16"
	"unicode/utf8"
)

// DecodeTextString converts a PDF text string (outline titles, document information, and similar human-readable values)
// to a Go string per ISO 32000-2 7.9.2.2: a UTF-16BE byte-order mark selects UTF-16BE, a UTF-8 byte-order mark (PDF
// 2.0) selects UTF-8, and everything else is PDFDocEncoding. Undecodable content maps to U+FFFD, which the public API's
// sanitizer strips.
func DecodeTextString(s String) string {
	b := []byte(s)
	switch {
	case len(b) >= 2 && b[0] == 0xfe && b[1] == 0xff:
		return decodeUTF16BE(b[2:])
	case bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}):
		return string(b[3:])
	default:
		return decodePDFDoc(b)
	}
}

func decodeUTF16BE(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
	}
	// A trailing odd byte is dropped.
	return string(utf16.Decode(units))
}

func decodePDFDoc(b []byte) string {
	out := make([]rune, len(b))
	for i, c := range b {
		out[i] = pdfDocEncoding[c]
	}
	return string(out)
}

// pdfDocEncoding maps PDFDocEncoding bytes to Unicode per ISO 32000-2 Table D.2. Positions the table leaves undefined
// map to U+FFFD.
var pdfDocEncoding = buildPDFDocEncoding()

func buildPDFDocEncoding() [256]rune {
	var table [256]rune
	for i := range 256 {
		switch {
		case i == '\t' || i == '\n' || i == '\r':
			table[i] = rune(i)
		case i < 0x18:
			table[i] = utf8.RuneError // Other C0 control positions are undefined.
		case i >= 0x20 && i <= 0x7e:
			table[i] = rune(i) // ASCII printable range.
		case i == 0x7f || i == 0x9f || i == 0xad:
			table[i] = utf8.RuneError // Undefined positions.
		case i >= 0xa1:
			table[i] = rune(i) // Latin-1 range.
		default:
			table[i] = utf8.RuneError // Overwritten below for the defined 0x18-0x1f, 0x80-0x9e, and 0xa0 slots.
		}
	}
	for i, r := range [8]rune{
		0x02d8, // BREVE
		0x02c7, // CARON
		0x02c6, // MODIFIER LETTER CIRCUMFLEX ACCENT
		0x02d9, // DOT ABOVE
		0x02dd, // DOUBLE ACUTE ACCENT
		0x02db, // OGONEK
		0x02da, // RING ABOVE
		0x02dc, // SMALL TILDE
	} {
		table[0x18+i] = r
	}
	for i, r := range [31]rune{
		0x2022, // BULLET
		0x2020, // DAGGER
		0x2021, // DOUBLE DAGGER
		0x2026, // HORIZONTAL ELLIPSIS
		0x2014, // EM DASH
		0x2013, // EN DASH
		0x0192, // LATIN SMALL LETTER F WITH HOOK
		0x2044, // FRACTION SLASH
		0x2039, // SINGLE LEFT-POINTING ANGLE QUOTATION MARK
		0x203a, // SINGLE RIGHT-POINTING ANGLE QUOTATION MARK
		0x2212, // MINUS SIGN
		0x2030, // PER MILLE SIGN
		0x201e, // DOUBLE LOW-9 QUOTATION MARK
		0x201c, // LEFT DOUBLE QUOTATION MARK
		0x201d, // RIGHT DOUBLE QUOTATION MARK
		0x2018, // LEFT SINGLE QUOTATION MARK
		0x2019, // RIGHT SINGLE QUOTATION MARK
		0x201a, // SINGLE LOW-9 QUOTATION MARK
		0x2122, // TRADE MARK SIGN
		0xfb01, // LATIN SMALL LIGATURE FI
		0xfb02, // LATIN SMALL LIGATURE FL
		0x0141, // LATIN CAPITAL LETTER L WITH STROKE
		0x0152, // LATIN CAPITAL LIGATURE OE
		0x0160, // LATIN CAPITAL LETTER S WITH CARON
		0x0178, // LATIN CAPITAL LETTER Y WITH DIAERESIS
		0x017d, // LATIN CAPITAL LETTER Z WITH CARON
		0x0131, // LATIN SMALL LETTER DOTLESS I
		0x0142, // LATIN SMALL LETTER L WITH STROKE
		0x0153, // LATIN SMALL LIGATURE OE
		0x0161, // LATIN SMALL LETTER S WITH CARON
		0x017e, // LATIN SMALL LETTER Z WITH CARON
	} {
		table[0x80+i] = r
	}
	table[0xa0] = 0x20ac // EURO SIGN
	return table
}
