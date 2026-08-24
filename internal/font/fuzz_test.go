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

	"github.com/richardwilkes/pdfview/internal/font/data"
)

// FuzzFontProgram drives the embedded-font-program surface with arbitrary bytes: sfnt parsing (go-text loader plus
// metrics/cmap table reads), bare-CFF parsing (go-text cff plus the TN5176 Top DICT reader), Type 1 parsing, the
// code→GID chains over every simple-font code, and outline extraction for the mapped GIDs. Nothing may panic: hostile
// programs must degrade to nil, which Load turns into Liberation substitution.
func FuzzFontProgram(f *testing.F) {
	if ttf := data.Liberation("LiberationSans-Regular"); ttf != nil {
		f.Add(ttf)
	}
	f.Add([]byte("\x00\x01\x00\x00\x00\x04head")) // sfnt-ish prefix
	f.Add([]byte{1, 0, 4, 4, 0, 0})               // CFF header prefix
	f.Add([]byte("OTTO"))
	f.Add(buildT1Program()) // Type 1 program (t1_test.go's builder)
	// A bare CFF whose Private DICT carries a deprecated CFF 1.0 operator, so mutations start from a program that
	// reaches the sanitize-and-retry arm of parseCFFGlyphBytes rather than failing at the first parse.
	f.Add(cffLayout(nil, nil, [][]byte{boxCharstring(0), boxCharstring(100)},
		privDictWithDeprecatedOp(cffOpForceBoldThreshold), nil))
	f.Fuzz(func(_ *testing.T, raw []byte) {
		if info := parseSFNT(raw); info != nil {
			fnt := &Font{sfnt: info, enc: &standardEncoding}
			fnt.buildGIDs()
			for _, code := range []uint32{0, 'A', 128, 255} {
				fnt.GlyphPath(fnt.GID(code, 1))
			}
			fnt.programAdvance(fnt.GID('A', 1))
		}
		top, err := parseCFFTopDict(raw)
		if err != nil {
			top = nil
		} else {
			top.metrics()
		}
		if info := parseCFFGlyphBytes(raw, top); info != nil {
			fnt := &Font{cff: info, enc: &standardEncoding}
			fnt.buildGIDs()
			for _, code := range []uint32{0, 'A', 255} {
				fnt.GlyphPath(fnt.GID(code, 1))
			}
		}
		if info := parseType1Bytes(raw, &standardEncoding); info != nil {
			fnt := &Font{t1: info, enc: &standardEncoding}
			fnt.buildGIDs()
			info.buildAdvances(fnt.enc)
			for _, code := range []uint32{0, 'A', 255} {
				fnt.GlyphPath(fnt.GID(code, 1))
				fnt.programAdvance(fnt.GID(code, 1))
			}
		}
	})
}
