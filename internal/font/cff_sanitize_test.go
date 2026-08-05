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
	"testing"
)

// dictInt encodes a DICT operand in the single-byte form, which covers -107..107 — every value these fixtures need.
func dictInt(v int) byte { return byte(v + 139) }

// deprecatedPrivateOps names the CFF 1.0 Private DICT operators go-text rejects outright, keyed by the name a font
// producer's dump would show.
var deprecatedPrivateOps = map[string]byte{
	"ForceBoldThreshold": cffOpForceBoldThreshold,
	"lenIV":              cffOpLenIV,
}

// privDictWithDeprecatedOp builds a Private DICT that carries one deprecated escaped operator between entries go-text
// accepts, and declares no local Subrs: these fixtures keep their outlines in the charstrings, so nothing depends on an
// offset relative to the Private DICT.
func privDictWithDeprecatedOp(sub byte) []byte {
	return []byte{
		dictInt(0), 20, // defaultWidthX
		dictInt(50), 12, sub, // The operator under test.
		dictInt(0), 21, // nominalWidthX
	}
}

// privDictWithSubrsAndDeprecatedOp is the same DICT with the local Subrs offset (operator 19) appended, which must land
// exactly past the DICT's own last byte for cidCFFLayout's block order.
func privDictWithSubrsAndDeprecatedOp(sub byte) []byte {
	d := []byte{dictInt(50), 12, sub, 29, 0, 0, 0, 0, 19}
	binary.BigEndian.PutUint32(d[4:], uint32(len(d)))
	return d
}

// TestCFFDeprecatedPrivateDictOpParses is the whole point of the sanitizer: a CFF whose Private DICT carries an
// operator dropped after CFF 1.0 has entirely sound charstrings, and FreeType (so MuPDF, so every other viewer) renders
// it by skipping what it does not know. go-text instead fails the font, which would send a real body text face — the
// Distiller-era Type1C programs these operators come from — through a Liberation substitute for every glyph on the
// page.
func TestCFFDeprecatedPrivateDictOpParses(t *testing.T) {
	for name, sub := range deprecatedPrivateOps {
		t.Run(name, func(t *testing.T) {
			raw := cffLayout(nil, nil, [][]byte{boxCharstring(0), boxCharstring(100)},
				privDictWithDeprecatedOp(sub), nil)
			info := loadCFFInfo(t, raw)
			assertMatchesGoText(t, info, 1)
		})
	}
}

// TestCFFDeprecatedPrivateDictOpNeedsSanitizing pins the premise the retry rests on. If go-text ever starts skipping
// unknown Private DICT operators the way FreeType does, the fixtures above would pass with the sanitizer removed and
// stop testing anything; this fails when that day comes, so the retry can be dropped deliberately rather than kept as
// dead weight.
func TestCFFDeprecatedPrivateDictOpNeedsSanitizing(t *testing.T) {
	for name, sub := range deprecatedPrivateOps {
		t.Run(name, func(t *testing.T) {
			raw := cffLayout(nil, nil, [][]byte{boxCharstring(0), boxCharstring(100)},
				privDictWithDeprecatedOp(sub), nil)
			if sanitizeCFFPrivateDicts(raw, nil) == nil {
				t.Fatal("the fixture's Private DICT was not recognized as carrying the deprecated operator")
			}
		})
	}
}

// TestCFFCIDDeprecatedPrivateDictOpParses covers the FDArray arm. A CID-keyed program keeps a Private DICT per font
// DICT and go-text parses every one of them, so a single deprecated operator anywhere in the array fails the font just
// as one in the Top DICT's Private does.
func TestCFFCIDDeprecatedPrivateDictOpParses(t *testing.T) {
	for name, sub := range deprecatedPrivateOps {
		t.Run(name, func(t *testing.T) {
			callSubr := []byte{csInt(-107), csCallSub, csEndchar}
			raw := cidCFFLayout([][]byte{{csEndchar}, callSubr}, [][][]byte{{boxSubr(100)}},
				[]byte{0, 0, 0}, privDictWithSubrsAndDeprecatedOp(sub))
			info := loadCFFInfo(t, raw)
			// The local subroutines live behind the very Private DICT that was rewritten, so recovering them also pins
			// that the rewrite left the Subrs offset — and every other operand in the DICT — where it was.
			if info.subrs == nil || len(info.subrs.localFor(1)) != 1 {
				t.Fatalf("font DICT subroutines not recovered: %+v", info.subrs)
			}
			assertMatchesGoText(t, info, 1)
		})
	}
}

// TestCFFSanitizeDoesNotMutateInput pins the contract the retry depends on: parseCFFGlyphBytes is handed bytes that
// alias cached stream data the rest of the engine still reads, so the rewrite has to happen in a copy.
func TestCFFSanitizeDoesNotMutateInput(t *testing.T) {
	raw := cffLayout(nil, nil, [][]byte{boxCharstring(0), boxCharstring(100)},
		privDictWithDeprecatedOp(cffOpForceBoldThreshold), nil)
	before := bytes.Clone(raw)
	if parseCFFGlyphBytes(raw, nil) == nil {
		t.Fatal("parseCFFGlyphBytes returned nil")
	}
	if !bytes.Equal(raw, before) {
		t.Error("parseCFFGlyphBytes rewrote the program it was given instead of a copy")
	}
	top, err := parseCFFTopDict(raw)
	if err != nil {
		t.Fatalf("parseCFFTopDict: %v", err)
	}
	if sanitizeCFFPrivateDicts(raw, top) == nil {
		t.Fatal("sanitizeCFFPrivateDicts reported no change")
	}
	if !bytes.Equal(raw, before) {
		t.Error("sanitizeCFFPrivateDicts wrote through to its input")
	}
}

// TestCFFSanitizeLeavesUnrelatedFailuresNil holds the rest of the rejection behavior still. The retry only ever adds a
// second chance for a program the sanitizer actually changed; anything else — junk, a truncated container, a Private
// DICT with nothing deprecated in it — must fail exactly as before, so the caller still falls back to a substitute.
func TestCFFSanitizeLeavesUnrelatedFailuresNil(t *testing.T) {
	// Two truncations of the same layout: one whose Private DICT the sanitizer rewrites (and the retry still fails),
	// one it leaves alone (so there is no retry at all). Both must land on nil.
	truncate := func(raw []byte) []byte { return raw[:len(raw)/2] }
	for name, raw := range map[string][]byte{
		"empty":       {},
		"junk":        []byte("not a font at all"),
		"header only": {1, 0, 4, 4, 0, 0},
		"truncated with a deprecated operator": truncate(cffLayout(nil, nil,
			[][]byte{boxCharstring(0), boxCharstring(100)}, privDictWithDeprecatedOp(cffOpLenIV), nil)),
		"truncated without one": truncate(cffLayout(nil, nil,
			[][]byte{boxCharstring(0), boxCharstring(100)}, privDictWithSubrs(), nil)),
	} {
		t.Run(name, func(t *testing.T) {
			before := bytes.Clone(raw)
			if info := parseCFFGlyphBytes(raw, nil); info != nil {
				t.Errorf("parseCFFGlyphBytes accepted %q", name)
			}
			if !bytes.Equal(raw, before) {
				t.Errorf("parseCFFGlyphBytes rewrote %q in place", name)
			}
		})
	}
}
