// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package data holds the embedded font bundle: width tables and built-in encodings compiled from the Adobe Core 14
// AFMs, the Adobe Glyph List, and the Liberation fonts used as metric-compatible substitutes for non-embedded fonts.
// Everything is embedded compressed and decompressed lazily on first use; README.md in this directory documents the
// upstream sources, and the gen subdirectory holds the generator that produced the committed files. All accessors are
// safe for concurrent use and treat the embedded data as trusted (it is generated and covered by unit tests): a blob
// that fails to load simply reports "not present".
package data

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"strconv"
	"strings"
	"sync"
)

//go:embed afm.txt.gz agl.txt.gz fonts
var files embed.FS

// gunzip decompresses one embedded blob, returning nil when it is absent or unreadable.
func gunzip(path string) []byte {
	blob, err := files.ReadFile(path)
	if err != nil {
		return nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil
	}
	return data
}

var (
	afmOnce    sync.Once
	afmWidths  map[string]map[string]uint16
	afmCodes   map[string]*[256]string
	aglOnce    sync.Once
	aglNames   map[string]string
	fontMu     sync.Mutex
	fontCache  map[string][]byte
	fontLoaded map[string]bool
)

// loadAFM parses afm.txt.gz: "font <name>" starts a width table of "<width> <glyph>" lines, "enc <name>" starts a
// built-in encoding table of "<code> <glyph>" lines.
func loadAFM() {
	afmWidths = map[string]map[string]uint16{}
	afmCodes = map[string]*[256]string{}
	var widths map[string]uint16
	var codes *[256]string
	for line := range strings.Lines(string(gunzip("afm.txt.gz"))) {
		line = strings.TrimSuffix(line, "\n")
		a, b, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch a {
		case "font":
			widths, codes = map[string]uint16{}, nil
			afmWidths[b] = widths
		case "enc":
			widths, codes = nil, &[256]string{}
			afmCodes[b] = codes
		default:
			n, err := strconv.Atoi(a)
			if err != nil {
				continue
			}
			switch {
			case widths != nil && n >= 0 && n <= 65535:
				widths[b] = uint16(n)
			case codes != nil && n >= 0 && n <= 255:
				codes[n] = b
			}
		}
	}
}

// AFMWidths returns the glyph-name→width table (1000-unit em space) for one of the 14 standard fonts, named by its
// canonical PostScript name (such as "Helvetica-Bold"), or nil when the name is not one of the 14.
func AFMWidths(name string) map[string]uint16 {
	afmOnce.Do(loadAFM)
	return afmWidths[name]
}

// BuiltinEncoding returns the built-in encoding (code→glyph name, "" for unused codes) for "Symbol" or "ZapfDingbats",
// the two standard fonts whose encodings are not one of the Annex D tables. Nil otherwise.
func BuiltinEncoding(name string) *[256]string {
	afmOnce.Do(loadAFM)
	return afmCodes[name]
}

// AGL returns the Adobe Glyph List: glyph name → Unicode string (usually one rune; ligature entries carry several). The
// returned map is shared — callers must not modify it.
func AGL() map[string]string {
	aglOnce.Do(func() {
		aglNames = map[string]string{}
		for line := range strings.Lines(string(gunzip("agl.txt.gz"))) {
			name, codes, ok := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
			if !ok {
				continue
			}
			var sb strings.Builder
			for _, hex := range strings.Fields(codes) {
				v, err := strconv.ParseUint(hex, 16, 32)
				if err != nil || v > 0x10FFFF {
					sb.Reset()
					break
				}
				sb.WriteRune(rune(v))
			}
			if sb.Len() > 0 {
				aglNames[name] = sb.String()
			}
		}
	})
	return aglNames
}

// Liberation returns the named Liberation font (such as "LiberationSans-Bold"), decompressed, or nil when no such font
// is bundled. The decompressed bytes are cached — callers must not modify them.
func Liberation(name string) []byte {
	fontMu.Lock()
	defer fontMu.Unlock()
	if fontLoaded == nil {
		fontCache = map[string][]byte{}
		fontLoaded = map[string]bool{}
	}
	if fontLoaded[name] {
		return fontCache[name]
	}
	fontLoaded[name] = true
	if strings.ContainsAny(name, "/\\.") {
		return nil
	}
	data := gunzip("fonts/" + name + ".ttf.gz")
	fontCache[name] = data
	return data
}
