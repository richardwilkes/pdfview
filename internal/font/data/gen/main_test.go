// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package main

import (
	"bytes"
	"go/format"
	"testing"
)

// TestBuildEncodingsTermination guards the shape of the generated encodings_gen.go. Each table appends a blank line
// after its closing brace, which has to come off the end, but trimming every trailing newline instead of all-but-one
// leaves the file without a terminating newline: not gofmt/gofumpt-clean, and a spurious diff against the committed
// file on every regeneration.
func TestBuildEncodingsTermination(t *testing.T) {
	tables := map[string][256]string{}
	for _, name := range []string{encStandard, encMacRoman, encWinAnsi, encMacExpert} {
		var table [256]string
		table[65] = "A"
		table[97] = "a"
		tables[name] = table
	}
	got := buildEncodings(tables)
	if !bytes.HasSuffix(got, []byte("}\n")) {
		t.Fatalf("generated file must end in a terminating newline, got %q", lastBytes(got))
	}
	if bytes.HasSuffix(got, []byte("\n\n")) {
		t.Errorf("generated file must end in exactly one newline, got %q", lastBytes(got))
	}
	formatted, err := format.Source(got)
	if err != nil {
		t.Fatalf("generated file does not parse: %v", err)
	}
	if !bytes.Equal(got, formatted) {
		t.Errorf("generated file is not gofmt-clean; tail = %q, want %q", lastBytes(got), lastBytes(formatted))
	}
	for _, want := range []string{"standardEncoding", "macRomanEncoding", "winAnsiEncoding", "macExpertEncoding"} {
		if !bytes.Contains(got, []byte("var "+want+" = [256]string{")) {
			t.Errorf("generated file is missing the %s table", want)
		}
	}
}

func lastBytes(data []byte) []byte {
	const n = 8
	if len(data) <= n {
		return data
	}
	return data[len(data)-n:]
}
