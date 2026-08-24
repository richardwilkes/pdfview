// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Diagnostic program: dump the font resources of one page, including how each font would load, or with the three
// extra arguments dump one glyph's outline as JSON.
//
// Usage: go run ./testprog document.pdf pageNumber [fontName code outFile]
package main

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/go-text/typesetting/font/cff"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/font"
)

func main() {
	if len(os.Args) != 3 && len(os.Args) != 6 {
		log.Fatal("usage: fontdump document.pdf pageNumber [fontName code outFile]")
	}
	data, err := os.ReadFile(os.Args[1]) //nolint:gosec // Diagnostic tool; the path is the user's own argument
	if err != nil {
		log.Fatal(err)
	}
	pageNumber, err := strconv.Atoi(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	d, err := doc.Open(data)
	if err != nil {
		log.Fatal(err)
	}
	res := d.PageResources(pageNumber)
	if res == nil {
		log.Fatal("no resources")
	}
	c := d.COS()
	if len(os.Args) == 6 {
		fonts, ok := c.GetDict(res, "Font")
		if !ok {
			log.Fatal("no /Font dict")
		}
		dumpGlyphJSON(c, fonts, cos.Name(os.Args[3]), uint32(os.Args[4][0]), os.Args[5])
		return
	}
	dumpFonts(c, res, "page")
	// Form XObjects carry their own resources; body text sometimes lives there.
	if xobjs, ok := c.GetDict(res, "XObject"); ok {
		for name := range xobjs {
			if st, isStream := c.GetStream(xobjs, name); isStream {
				if sub, _ := c.GetName(st.Dict, "Subtype"); sub == "Form" {
					if formRes, hasRes := c.GetDict(st.Dict, "Resources"); hasRes {
						dumpFonts(c, formRes, "form "+string(name))
					}
				}
			}
		}
	}
}

func dumpFonts(c *cos.Document, res cos.Dict, label string) {
	fonts, ok := c.GetDict(res, "Font")
	if !ok {
		fmt.Printf("%s: no /Font dict\n", label)
		return
	}
	for name := range fonts {
		fd, isDict := c.GetDict(fonts, name)
		if !isDict {
			fmt.Printf("%s %s: not a dict\n", label, name)
			continue
		}
		subtype, _ := c.GetName(fd, "Subtype")
		base, _ := c.GetName(fd, "BaseFont")
		desc := describeDescriptor(c, fd)
		if subtype == "Type0" {
			if kids, hasKids := c.GetArray(fd, "DescendantFonts"); hasKids && len(kids) > 0 {
				if kid, kidOK := cos.AsDict(c.Resolve(kids[0])); kidOK {
					desc += " descendant:" + describeDescriptor(c, kid)
				}
			}
		}
		loaded, err := font.Load(c, fd)
		status := "load ERROR: " + fmt.Sprint(err)
		if err == nil {
			status = fmt.Sprintf("loaded BaseFont=%q flags=%d", loaded.BaseFont, loaded.Flags)
		}
		fmt.Printf("%s %-10s subtype=%-8s base=%-32s %s | %s\n", label, name, subtype, base, desc, status)
	}
}

func safeParseCFF(raw []byte) (parsed *cff.CFF, err error) {
	defer func() {
		if r := recover(); r != nil {
			parsed, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()
	return cff.Parse(raw)
}

func describeDescriptor(c *cos.Document, fd cos.Dict) string {
	descDict, ok := c.GetDict(fd, "FontDescriptor")
	if !ok {
		return "NO-DESCRIPTOR"
	}
	out := ""
	if _, has := c.GetStream(descDict, "FontFile"); has {
		out += "FontFile(T1)"
	}
	if _, has := c.GetStream(descDict, "FontFile2"); has {
		out += "FontFile2(TT)"
	}
	if st, has := c.GetStream(descDict, "FontFile3"); has {
		sub, _ := c.GetName(st.Dict, "Subtype")
		out += "FontFile3(" + string(sub) + ")"
		if raw, err := c.StreamData(st); err != nil {
			out += fmt.Sprintf(" stream ERR:%v", err)
		} else {
			out += fmt.Sprintf(" %db", len(raw))
			parsed, perr := safeParseCFF(raw)
			if perr != nil {
				out += fmt.Sprintf(" cff.Parse ERR:%v", perr)
				fmt.Println("dissecting failing CFF:")
				dumpCFFPrivate(raw)
				if fixed, changed := sanitizeCFFPrivate(raw); changed {
					if reparsed, rerr := safeParseCFF(fixed); rerr != nil {
						fmt.Println("  sanitize: re-parse still fails:", rerr)
					} else {
						fmt.Printf("  sanitize: re-parse OK, %d charstrings\n", len(reparsed.Charstrings))
					}
				} else {
					fmt.Println("  sanitize: nothing to change")
				}
			} else {
				out += fmt.Sprintf(" cff ok, %d charstrings", len(parsed.Charstrings))
			}
		}
	}
	if out == "" {
		out = "NOT-EMBEDDED"
	}
	return out
}
