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
	"encoding/json"
	"fmt"
	"os"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/font"
)

// glyphDump serializes a gfx.Path: verbs (0 move, 1 line, 2 quad, 3 cubic, 4 close) and points in em-normalized glyph
// space.
type glyphDump struct {
	Verbs  []int        `json:"verbs"`
	Points [][2]float32 `json:"points"`
}

// dumpGlyphJSON writes the em-normalized outline of one glyph, found by its code in the named page font, as JSON.
func dumpGlyphJSON(c *cos.Document, fonts cos.Dict, fontName cos.Name, code uint32, out string) {
	fd, ok := c.GetDict(fonts, fontName)
	if !ok {
		fmt.Println("no font", fontName)
		return
	}
	f, err := font.Load(c, fd)
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	gid := f.GID(code, 1)
	gp := f.GlyphPath(gid)
	if gp == nil {
		fmt.Printf("no path for %s code %q gid %d\n", fontName, rune(code), gid)
		return
	}
	var dump glyphDump
	for _, v := range gp.Verbs {
		dump.Verbs = append(dump.Verbs, int(v))
	}
	for _, pt := range gp.Points {
		dump.Points = append(dump.Points, [2]float32{pt.X, pt.Y})
	}
	data, err := json.Marshal(dump)
	if err != nil {
		fmt.Println("marshal:", err)
		return
	}
	if err = os.WriteFile(out, data, 0o600); err != nil { //nolint:gosec // This is a test program, so OK
		fmt.Println("write:", err)
		return
	}
	fmt.Printf("wrote %s: %s code %q gid %d, %d verbs\n", out, fontName, rune(code), gid, len(dump.Verbs))
}
