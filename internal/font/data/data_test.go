// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package data

import (
	"strings"
	"testing"
)

// TestParseAGLLine pins which blob lines yield an entry. Surrogates matter as much as out-of-range scalars: strings
// .Builder.WriteRune substitutes U+FFFD for them silently, so an entry mapping a glyph name to a replacement character
// would look like a successful lookup — while font.GlyphNameToUnicode's uniXXXX/uXXXX forms reject the same class
// outright. Both paths must agree.
func TestParseAGLLine(t *testing.T) {
	for _, tc := range []struct {
		name  string
		line  string
		want  string
		valid bool
	}{
		{"single scalar", "alpha 03B1", "α", true},
		{"ligature", "fi 0066 0069", "fi", true},
		{"astral", "u1F600 1F600", "\U0001F600", true},
		{"high surrogate", "bogus D800", "", false},
		{"low surrogate", "bogus DFFF", "", false},
		{"surrogate among valid", "bogus 0066 D83D 0069", "", false},
		{"out of range", "bogus 110000", "", false},
		{"not hex", "bogus ZZZZ", "", false},
		{"no scalars", "bogus ", "", false},
		{"no separator", "bogus", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, value, ok := parseAGLLine(tc.line)
			if ok != tc.valid {
				t.Fatalf("parseAGLLine(%q) ok = %v, want %v (name %q, value %q)", tc.line, ok, tc.valid, got, value)
			}
			if ok && value != tc.want {
				t.Fatalf("parseAGLLine(%q) = %q, want %q", tc.line, value, tc.want)
			}
		})
	}
}

// TestAGLHasNoReplacementChars guards the committed blob itself: no entry may carry U+FFFD, the character an unusable
// scalar would silently become.
func TestAGLHasNoReplacementChars(t *testing.T) {
	agl := AGL()
	if len(agl) < 4000 {
		t.Fatalf("AGL has %d entries, want the full list", len(agl))
	}
	for name, value := range agl {
		if strings.ContainsRune(value, '�') {
			t.Errorf("AGL[%q] = %q carries a replacement character", name, value)
		}
	}
	if got := agl["alpha"]; got != "α" {
		t.Errorf("AGL[alpha] = %q, want α", got)
	}
}
