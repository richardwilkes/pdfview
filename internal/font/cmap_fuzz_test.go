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
)

// FuzzCMap drives the PDF CMap parser and its lookup surface with arbitrary bytes. Nothing may panic or spin: the lexer
// guarantees forward progress, every range count is capped, and decoding must always consume at least one byte per
// code.
func FuzzCMap(f *testing.F) {
	f.Add([]byte(testCMapContent))
	f.Add([]byte(testToUnicodeContent))
	f.Add([]byte("/Identity-H usecmap 1 begincidrange <00> <ff> 7 endcidrange"))
	f.Add([]byte("2 begincodespacerange <00> <80> <8140> <9ffc> endcodespacerange"))
	f.Add([]byte("1 beginbfrange <0000> <00ff> [<0041>] endbfrange"))
	f.Add([]byte("/WMode 1 def"))
	f.Fuzz(func(t *testing.T, data []byte) {
		cm := parseCMap(data, 0, predefinedCMap)
		if cm == nil {
			return
		}
		cm.wModeResolved()
		// Decode a fixed probe through whatever codespaces were parsed; consumption must always advance.
		probe := []byte{0x00, 0x41, 0x81, 0x40, 0xff, 0x20, 0x7f, 0xa0, 0xa0, 0xa0}
		for len(probe) > 0 {
			code, n := cm.nextCode(probe)
			if n <= 0 {
				t.Fatalf("nextCode consumed %d bytes", n)
			}
			cm.cid(code)
			cm.bfString(code)
			probe = probe[min(n, len(probe)):]
		}
	})
}
