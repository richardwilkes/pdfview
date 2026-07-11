// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Command gen regenerates the committed contents of internal/font/data (and the generated base-encoding tables
// in internal/font/encodings_gen.go) from locally downloaded upstream inputs. It is a dev-time tool: CI never
// runs it, only its committed outputs are used by the library, and it performs no network access itself. The
// exact upstream sources, versions, and checksums are documented in internal/font/data/README.md; fetch them
// into an input directory laid out as:
//
//	<in>/afm/*.afm            Adobe Core 14 AFM files (Core14_AFMs.zip, extracted, incl. MustRead.html)
//	<in>/glyphlist.txt        Adobe Glyph List (agl-aglfn repository)
//	<in>/encodings.js         pdf.js src/core/encodings.js (Apache-2.0; base-encoding reference tables)
//	<in>/liberation/*.ttf     Liberation fonts release (incl. LICENSE)
//
// Usage: go run ./internal/font/data/gen -in <inputs> -data internal/font/data -enc internal/font/encodings_gen.go
//
// The generator cross-checks the derived StandardEncoding table against the AFM character codes (the Core 14
// text AFMs are coded in AdobeStandardEncoding), so drift between the two upstream sources fails generation
// rather than producing silently wrong tables.
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	in := flag.String("in", "", "input directory (see package comment for layout)")
	dataDir := flag.String("data", "internal/font/data", "output data directory")
	encFile := flag.String("enc", "internal/font/encodings_gen.go", "output Go file for base-encoding tables")
	flag.Parse()
	if *in == "" {
		fatal("missing -in")
	}
	afms := parseAFMs(filepath.Join(*in, "afm"))
	agl := readFile(filepath.Join(*in, "glyphlist.txt"))
	encodings := parseEncodingsJS(readFile(filepath.Join(*in, "encodings.js")))
	std := encodings[encStandard]
	crossCheckStandard(&std, afms)

	writeGzip(filepath.Join(*dataDir, "afm.txt.gz"), buildAFMBlob(afms))
	writeGzip(filepath.Join(*dataDir, "agl.txt.gz"), buildAGLBlob(agl))
	writeEncodings(*encFile, encodings)

	libDir := filepath.Join(*in, "liberation")
	entries, err := os.ReadDir(libDir)
	if err != nil {
		fatal("liberation dir: %v", err)
	}
	fontsOut := filepath.Join(*dataDir, "fonts")
	if err = os.MkdirAll(fontsOut, 0o755); err != nil {
		fatal("%v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".ttf") {
			continue
		}
		writeGzip(filepath.Join(fontsOut, e.Name()+".gz"), readFile(filepath.Join(libDir, e.Name())))
	}
	copyFile(filepath.Join(libDir, "LICENSE"), filepath.Join(*dataDir, "LICENSE-liberation.txt"))
	copyFile(filepath.Join(*in, "afm", "MustRead.html"), filepath.Join(*dataDir, "LICENSE-afm.html"))
	writeAGLLicense(filepath.Join(*dataDir, "LICENSE-agl.txt"), agl)
	fmt.Println("generated", *dataDir, "and", *encFile)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func readFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		fatal("%v", err)
	}
	return data
}

func copyFile(src, dst string) {
	if err := os.WriteFile(dst, readFile(src), 0o644); err != nil {
		fatal("%v", err)
	}
}

func writeGzip(path string, data []byte) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		fatal("%v", err)
	}
	if _, err = zw.Write(data); err != nil {
		fatal("%v", err)
	}
	if err = zw.Close(); err != nil {
		fatal("%v", err)
	}
	if err = os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("  %s: %d -> %d bytes\n", path, len(data), buf.Len())
}

// afmFont is one parsed AFM: glyph-name widths plus the font's built-in encoding (C codes).
type afmFont struct {
	widths map[string]int
	codes  map[int]string
	name   string
}

var afmCharRE = regexp.MustCompile(`^C\s+(-?\d+)\s*;\s*WX\s+(-?\d+)\s*;\s*N\s+(\S+)\s*;`)

func parseAFMs(dir string) []afmFont {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fatal("afm dir: %v", err)
	}
	var fonts []afmFont
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".afm") {
			continue
		}
		font := afmFont{
			name:   strings.TrimSuffix(e.Name(), ".afm"),
			widths: map[string]int{},
			codes:  map[int]string{},
		}
		for _, line := range strings.Split(string(readFile(filepath.Join(dir, e.Name()))), "\n") {
			m := afmCharRE.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			code, codeErr := strconv.Atoi(m[1])
			if codeErr != nil {
				fatal("%s: bad code in %q", e.Name(), line)
			}
			width, widthErr := strconv.Atoi(m[2])
			if widthErr != nil {
				fatal("%s: bad width in %q", e.Name(), line)
			}
			font.widths[m[3]] = width
			if code >= 0 && code <= 255 {
				font.codes[code] = m[3]
			}
		}
		if len(font.widths) == 0 {
			fatal("%s: no character metrics parsed", e.Name())
		}
		fonts = append(fonts, font)
	}
	if len(fonts) != 14 {
		fatal("expected 14 AFMs, found %d", len(fonts))
	}
	sort.Slice(fonts, func(i, j int) bool { return fonts[i].name < fonts[j].name })
	return fonts
}

// buildAFMBlob serializes the widths (all fonts) and built-in encodings (Symbol and ZapfDingbats only — the
// text fonts use the standard tables from encodings_gen.go). Format: "font <name>" starts a width table with
// "<width> <glyph>" lines; "enc <name>" starts an encoding table with "<code> <glyph>" lines.
func buildAFMBlob(fonts []afmFont) []byte {
	var buf bytes.Buffer
	for _, font := range fonts {
		fmt.Fprintf(&buf, "font %s\n", font.name)
		names := make([]string, 0, len(font.widths))
		for name := range font.widths {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&buf, "%d %s\n", font.widths[name], name)
		}
	}
	for _, font := range fonts {
		if font.name != "Symbol" && font.name != "ZapfDingbats" {
			continue
		}
		fmt.Fprintf(&buf, "enc %s\n", font.name)
		codes := make([]int, 0, len(font.codes))
		for code := range font.codes {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		for _, code := range codes {
			fmt.Fprintf(&buf, "%d %s\n", code, font.codes[code])
		}
	}
	return buf.Bytes()
}

// buildAGLBlob converts glyphlist.txt ("name;XXXX YYYY" with # comments) to "name XXXX[ YYYY...]" lines.
func buildAGLBlob(glyphlist []byte) []byte {
	var buf bytes.Buffer
	count := 0
	for _, line := range strings.Split(string(glyphlist), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, codes, ok := strings.Cut(line, ";")
		if !ok {
			fatal("glyphlist: bad line %q", line)
		}
		fmt.Fprintf(&buf, "%s %s\n", name, strings.TrimSpace(codes))
		count++
	}
	if count < 4000 {
		fatal("glyphlist: only %d entries parsed", count)
	}
	return buf.Bytes()
}

// writeAGLLicense extracts the license header comment from glyphlist.txt.
func writeAGLLicense(path string, glyphlist []byte) {
	var buf bytes.Buffer
	for _, line := range strings.Split(string(glyphlist), "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		buf.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "#")))
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		fatal("%v", err)
	}
}

// Encoding table keys, matching the pdf.js array-name prefixes.
const (
	encStandard  = "Standard"
	encMacRoman  = "MacRoman"
	encWinAnsi   = "WinAnsi"
	encMacExpert = "MacExpert"
)

// parseEncodingsJS extracts the four base-encoding arrays from pdf.js's encodings.js: each is
// "const <Name>Encoding = [ "glyph", "", ... ];" with 256 string entries ("" = unused code).
func parseEncodingsJS(src []byte) map[string][256]string {
	out := map[string][256]string{}
	for _, want := range []string{encMacExpert, encMacRoman, encStandard, encWinAnsi} {
		re := regexp.MustCompile(`const ` + want + `Encoding = \[([^\]]*)\]`)
		m := re.FindSubmatch(src)
		if m == nil {
			fatal("encodings.js: %sEncoding not found", want)
		}
		entryRE := regexp.MustCompile(`"([^"]*)"`)
		entries := entryRE.FindAllStringSubmatch(string(m[1]), -1)
		if len(entries) != 256 {
			fatal("encodings.js: %sEncoding has %d entries, want 256", want, len(entries))
		}
		var table [256]string
		for i, e := range entries {
			table[i] = e[1]
		}
		out[want] = table
	}
	return out
}

// crossCheckStandard verifies the StandardEncoding table from encodings.js against the AFM character codes:
// the Core 14 text AFMs are coded in AdobeStandardEncoding, so every C >= 0 entry must agree.
func crossCheckStandard(std *[256]string, fonts []afmFont) {
	checked := 0
	for _, font := range fonts {
		if font.name == "Symbol" || font.name == "ZapfDingbats" {
			continue // Coded in their own built-in encodings.
		}
		for code, name := range font.codes {
			if std[code] != name {
				fatal("StandardEncoding drift at %d: encodings.js %q vs %s.afm %q", code, std[code], font.name, name)
			}
			checked++
		}
	}
	if checked == 0 {
		fatal("StandardEncoding cross-check checked nothing")
	}
	fmt.Printf("  StandardEncoding cross-check: %d entries agree\n", checked)
}

func writeEncodings(path string, encodings map[string][256]string) {
	var buf bytes.Buffer
	buf.WriteString(`// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Code generated by internal/font/data/gen. DO NOT EDIT.
// The four base encodings of ISO 32000-2 Annex D, derived from pdf.js's encodings.js (Apache-2.0; see
// internal/font/data/README.md) and cross-checked against the Adobe Core 14 AFM character codes at generation
// time. Empty strings mark codes with no glyph.

package font

`)
	names := []string{encStandard, encMacRoman, encWinAnsi, encMacExpert}
	varNames := map[string]string{
		encStandard: "standardEncoding", encMacRoman: "macRomanEncoding",
		encWinAnsi: "winAnsiEncoding", encMacExpert: "macExpertEncoding",
	}
	for _, name := range names {
		table := encodings[name]
		fmt.Fprintf(&buf, "var %s = [256]string{\n", varNames[name])
		for i := 0; i < 256; i += 4 {
			line := "\t"
			for j := i; j < i+4; j++ {
				line += fmt.Sprintf("%q, ", table[j])
			}
			buf.WriteString(strings.TrimRight(line, " ") + "\n")
		}
		buf.WriteString("}\n\n")
	}
	if err := os.WriteFile(path, bytes.TrimRight(buf.Bytes(), "\n"), 0o644); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("  %s written\n", path)
}
