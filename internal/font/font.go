// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package font implements PDF font semantics (ISO 32000-2 9.5–9.10): font-dictionary and descriptor parsing,
// embedded font programs, encodings, width resolution, and the deterministic substitution of non-embedded
// fonts from the bundled data (internal/font/data). It supplies the content interpreter with everything a
// show-text operator needs — per-code advances in text space, ascent/descent for text quads, and Unicode for
// extraction/search — and glyph outlines for rendering.
//
// Metrics contract (pinned behaviorally against the oracle goldens): the widths that position glyphs come from
// the PDF /Widths (or /W) entries whenever present, never from the font program, so layout and search parity
// hold even when glyph shapes are substituted. The ascender/descender that size text quads follow FreeType's
// rules, because that is what the oracle's MuPDF build exposes: hhea values for sfnt fonts (falling back to
// OS/2 typo, then win metrics, when hhea has none), and the FontBBox for bare CFF and Type 1 programs.
// Substituted fonts use the pinned metrics of MuPDF's bundled replacements, not the metrics of our Liberation
// stand-ins, for the same reason.
package font

import (
	"errors"
	"strings"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/font/data"
)

// Errors reported by Load. They flow no further than the interpreter, which degrades a failed font load by
// keeping the previous font (matching the oracle's operator-level error recovery).
var (
	// ErrUnsupportedFont marks font configurations without engine support (Type0 fonts encoded with a
	// predefined non-Identity CMap); the interpreter skips text shown with them, never erroring the page.
	ErrUnsupportedFont = errors.New("unsupported font type")
	// ErrBadFont marks a font dictionary too malformed to use.
	ErrBadFont = errors.New("unusable font dictionary")
)

// Flag bits of the font descriptor /Flags entry (ISO 32000-2 9.8.2, table 121).
const (
	FlagFixedPitch  = 1 << 0
	FlagSerif       = 1 << 1
	FlagSymbolic    = 1 << 2
	FlagScript      = 1 << 3
	FlagNonsymbolic = 1 << 5
	FlagItalic      = 1 << 6
	FlagAllCap      = 1 << 16
	FlagSmallCap    = 1 << 17
	FlagForceBold   = 1 << 18
)

// Font is one loaded PDF font resource. It is immutable after Load and safe for concurrent reads.
type Font struct {
	// enc maps codes to glyph names for simple fonts ("" when the code has none).
	enc *[256]string
	// widths maps codes to advances in text space (the PDF 1000-unit values already divided by 1000).
	widths map[uint32]float32
	// afm is the standard-14 fallback width table (glyph name → 1000-unit width) consulted only when the PDF
	// supplies no /Widths array at all and the font is substituted (embedded programs fall back to their own
	// advances instead). When /Widths exists, codes it does not cover take /MissingWidth, per the descriptor.
	afm map[string]uint16
	// sfnt carries the parsed embedded TrueType/OpenType program, nil otherwise.
	sfnt *sfntInfo
	// cff carries the parsed embedded bare-CFF (Type1C) program, nil otherwise.
	cff *cffInfo
	// t1 carries the parsed embedded Type 1 program (FontFile), nil otherwise.
	t1 *t1Info
	// type0 carries composite-font state (CMap, CID widths, CID→GID); nil for simple fonts. The simple-font
	// [256] tables below are not consulted when it is set.
	type0 *type0Info
	// type3 carries Type 3 CharProcs state; the interpreter recurses into the procs instead of drawing
	// outlines (GlyphPath reports none).
	type3 *type3Info
	// toUni is the parsed /ToUnicode CMap, nil when absent; it takes precedence over every Unicode source.
	toUni *cmapPDF
	// sub carries the bundled substitute face when no embedded program renders (nil for embedded fonts).
	sub *subInfo
	// BaseFont is the /BaseFont name with any subset prefix stripped.
	BaseFont string
	// Flags is the descriptor /Flags value (0 when absent).
	Flags int
	// uni maps codes to Unicode runes for simple fonts (0 when unknown).
	uni [256]rune
	// gids is the precomputed code→GID table (see buildGIDs).
	gids [256]uint32
	// ascender/descender are in text space (em units): the values MuPDF's stext device would use for quads.
	ascender  float32
	descender float32
	// missingWidth is the descriptor /MissingWidth in text space.
	missingWidth float32
	// hasWidths records whether the dictionary carried a /Widths array (its gaps then mean /MissingWidth,
	// never a fallback source).
	hasWidths bool
}

// Load builds a Font from a font resource dictionary. The document's resolver is used for every indirect
// value. Unsupported subtypes return ErrUnsupportedFont; malformed dictionaries return ErrBadFont. The load
// never panics on hostile input (embedded font parsing is guarded).
func Load(d *cos.Document, dict cos.Dict) (*Font, error) {
	subtype, _ := d.GetName(dict, "Subtype")
	switch subtype {
	case "Type1", "MMType1", "TrueType":
		return loadSimple(d, dict)
	case "Type0":
		return loadType0(d, dict)
	case "Type3":
		return loadType3(d, dict)
	default:
		// A missing or unknown subtype gets the simple-font treatment when the dictionary looks like one —
		// lenient, like deployed viewers.
		if _, ok := dict["BaseFont"]; ok {
			return loadSimple(d, dict)
		}
		return nil, ErrBadFont
	}
}

// descriptor is the parsed subset of a font descriptor the engine uses.
type descriptor struct {
	fontFile     *cos.Stream // FontFile (Type 1)
	fontFile2    *cos.Stream // FontFile2 (TrueType)
	fontFile3    *cos.Stream // FontFile3 (CFF or OpenType, per its own /Subtype)
	fontFile3Sub cos.Name
	flags        int
	missingWidth float32
	ascent       float32 // /Ascent, 1000-unit space (0 when absent)
	descent      float32 // /Descent, 1000-unit space (0 when absent)
	present      bool    // whether the font dictionary carried a descriptor at all
}

func loadDescriptor(d *cos.Document, dict cos.Dict) descriptor {
	var out descriptor
	fd, ok := d.GetDict(dict, "FontDescriptor")
	if !ok {
		return out
	}
	out.present = true
	if v, has := d.GetInt(fd, "Flags"); has {
		out.flags = int(v)
	}
	if v, has := cos.AsReal(d.Resolve(fd["MissingWidth"])); has {
		out.missingWidth = float32(v) / 1000
	}
	if v, has := cos.AsReal(d.Resolve(fd["Ascent"])); has {
		out.ascent = float32(v)
	}
	if v, has := cos.AsReal(d.Resolve(fd["Descent"])); has {
		out.descent = float32(v)
	}
	if s, has := cos.AsStream(d.Resolve(fd["FontFile"])); has {
		out.fontFile = s
	}
	if s, has := cos.AsStream(d.Resolve(fd["FontFile2"])); has {
		out.fontFile2 = s
	}
	if s, has := cos.AsStream(d.Resolve(fd["FontFile3"])); has {
		out.fontFile3 = s
		out.fontFile3Sub, _ = d.GetName(s.Dict, "Subtype")
	}
	return out
}

// loadSimple loads Type1/MMType1/TrueType fonts: one-byte codes, at most 256 glyphs (ISO 32000-2 9.6).
func loadSimple(d *cos.Document, dict cos.Dict) (*Font, error) {
	f := &Font{}
	if base, ok := d.GetName(dict, "BaseFont"); ok {
		f.BaseFont = stripSubsetPrefix(string(base))
	}
	desc := loadDescriptor(d, dict)
	f.Flags = desc.flags
	f.missingWidth = desc.missingWidth

	// The embedded program supplies the quad metrics; substituted fonts use the standard-14 pins.
	embedded := false
	switch {
	case desc.fontFile2 != nil:
		if info := parseSFNTStream(d, desc.fontFile2); info != nil {
			f.sfnt, embedded = info, true
			f.ascender, f.descender = info.ascender, info.descender
		}
	case desc.fontFile3 != nil && desc.fontFile3Sub == "OpenType":
		if info := parseSFNTStream(d, desc.fontFile3); info != nil {
			f.sfnt, embedded = info, true
			f.ascender, f.descender = info.ascender, info.descender
		}
	case desc.fontFile3 != nil:
		// Bare CFF (Type1C): FreeType — and so the oracle — takes ascender/descender from the FontBBox.
		if top := parseCFFTopFromStream(d, desc.fontFile3); top != nil {
			if asc, dsc, ok := top.metrics(); ok {
				f.ascender, f.descender = asc, dsc
				embedded = true
			}
			f.cff = parseCFFGlyphs(d, desc.fontFile3, top)
		}
	case desc.fontFile != nil:
		// Type 1 programs: quad metrics come from the FontBBox over the FontMatrix-implied upem, like bare
		// CFF (FreeType's rule; see the package comment). seac needs StandardEncoding regardless of the
		// font's own encoding, so the generated table is injected here.
		if info := parseType1Stream(d, desc.fontFile, &standardEncoding); info != nil {
			f.t1 = info
			if asc, dsc, ok := info.metrics(); ok {
				f.ascender, f.descender = asc, dsc
				embedded = true
			}
		}
	}
	std14 := standard14Name(f.BaseFont, desc.flags)
	if !embedded {
		f.ascender, f.descender = substituteMetrics(&desc, std14)
	}

	// Encoding: explicit /Encoding wins; otherwise the embedded program's built-in encoding (Type 1),
	// Symbol/ZapfDingbats' built-in tables, or StandardEncoding, in that order.
	var builtin *[256]string
	if f.t1 != nil {
		builtin = f.t1.builtinEncoding()
	}
	f.enc = resolveEncoding(d, dict, std14, builtin)
	f.toUni = loadToUnicode(d, dict)
	buildUnicode(f)

	// Widths: /Widths always wins. Without one, substituted fonts take the standard-14 AFM widths and
	// embedded programs their own advances (in Width).
	f.hasWidths = loadWidths(d, dict, f)

	// Shapes: an embedded program renders itself; anything else — including embedded programs whose bytes
	// yield no outline source at all — renders through the deterministic Liberation substitute (never an
	// error, never a system font). An sfnt go-text rejects (no cmap table) but whose glyf/loca tables read
	// keeps rendering its own shapes through the direct glyf walker. The substitute is the glyph source only
	// when no embedded source exists, so GID/GlyphPath/Width stay mutually consistent.
	if f.sfnt != nil && f.sfnt.face == nil && f.sfnt.glyf == nil {
		f.sfnt = nil
	}
	if f.sfnt == nil && f.cff == nil && f.t1 == nil {
		f.sub = loadSubstitute(std14)
	}
	// Width fallback for /Widths-less fonts: sfnt programs supply hmtx advances and Type 1 programs their
	// hsbw advances (programAdvance); everything else — substituted fonts per the std14-styles pin, and bare
	// CFF until its charstring advances land — takes the AFM widths of the standard-14 stand-in.
	if !f.hasWidths && f.sfnt == nil && f.t1 == nil {
		f.afm = data.AFMWidths(std14)
	}
	f.buildGIDs()
	if !f.hasWidths && f.t1 != nil {
		f.t1.buildAdvances(f.enc)
	}
	return f, nil
}

// stripSubsetPrefix removes the "ABCDEF+" subset tag from a BaseFont name.
func stripSubsetPrefix(name string) string {
	if len(name) > 7 && name[6] == '+' {
		for i := range 6 {
			if name[i] < 'A' || name[i] > 'Z' {
				return name
			}
		}
		return name[7:]
	}
	return name
}

// loadWidths parses /FirstChar + /Widths into text-space advances, reporting whether a /Widths array was
// present (even an empty or junk-filled one counts: its gaps mean /MissingWidth, not substitute advances).
func loadWidths(d *cos.Document, dict cos.Dict, f *Font) bool {
	f.widths = map[uint32]float32{}
	first, _ := d.GetInt(dict, "FirstChar")
	arr, ok := d.GetArray(dict, "Widths")
	if !ok {
		return false
	}
	const maxSimpleWidths = 256 // A simple font has at most 256 codes; longer arrays are hostile or junk.
	for i, entry := range arr {
		if i >= maxSimpleWidths {
			break
		}
		code := first + int64(i)
		if code < 0 || code > 255 {
			continue
		}
		if v, numOK := cos.AsReal(d.Resolve(entry)); numOK {
			f.widths[uint32(code)] = float32(v) / 1000
		}
	}
	return true
}

// Width returns the advance for a code in text space (em units at size 1). A present /Widths array is
// authoritative: its value when the code resolves, /MissingWidth otherwise. Without one, substituted fonts
// use the standard-14 AFM width for the code's glyph name and embedded sfnt programs their own hmtx advance,
// then /MissingWidth. Composite fonts use /W with /DW as the default instead.
func (f *Font) Width(code uint32) float32 {
	if f.type0 != nil {
		return f.type0.cidWidth(f.type0.cmap.cid(code))
	}
	if w, ok := f.widths[code]; ok {
		return w
	}
	if f.hasWidths {
		return f.missingWidth
	}
	if f.afm != nil && code < 256 && f.enc != nil {
		if name := f.enc[code]; name != "" {
			if w, ok := f.afm[name]; ok {
				return float32(w) / 1000
			}
		}
	}
	if w, ok := f.programAdvance(f.GID(code)); ok {
		return w
	}
	return f.missingWidth
}

// Unicode returns the Unicode rune for a code, or 0 when none is known. A /ToUnicode CMap takes precedence
// over every other source (ISO 32000-2 9.10.2); multi-rune targets (ligatures) surface their first rune, the
// one rune per code the search/extraction seam carries.
func (f *Font) Unicode(code uint32) rune {
	if f.toUni != nil {
		if s := f.toUni.bfString(code); s != "" {
			for _, r := range s {
				return r
			}
		}
	}
	if f.type0 == nil && code < 256 {
		return f.uni[code]
	}
	return 0
}

// GlyphName returns the glyph name a simple font's encoding assigns to code ("" when none).
func (f *Font) GlyphName(code uint32) string {
	if f.enc != nil && code < 256 {
		return f.enc[code]
	}
	return ""
}

// MemoryEstimate returns a rough byte footprint for cache budgeting (internal/store): the embedded program's
// data plus a fixed allowance for the per-font tables. It never needs to be exact — the store's budget is a
// working-set bound, not an accounting ledger.
func (f *Font) MemoryEstimate() uint64 {
	const base = 8 << 10 // gids/uni/enc tables, widths map, struct overhead.
	n := uint64(base)
	if f.type0 != nil {
		if f.type0.sfnt != nil {
			n += uint64(len(f.type0.sfnt.data)) * 2
		}
		n += uint64(len(f.type0.cidToGID))*2 + uint64(len(f.type0.w))*24 + uint64(len(f.type0.w2))*32
		for i := range f.type0.w {
			n += uint64(len(f.type0.w[i].ws)) * 4
		}
	}
	switch {
	case f.sfnt != nil:
		n += uint64(len(f.sfnt.data)) * 2 // Raw table data plus go-text's parsed view of it.
	case f.cff != nil:
		for _, cs := range f.cff.font.Charstrings {
			n += uint64(len(cs))
		}
		n += uint64(len(f.cff.names)) * 32
	case f.t1 != nil:
		for _, cs := range f.t1.font.CharStrings {
			n += uint64(len(cs)) + 32
		}
		for _, sub := range f.t1.font.Subrs {
			n += uint64(len(sub))
		}
	}
	return n
}

// Ascender returns the quad-top metric in text space (em units): positive above the baseline.
func (f *Font) Ascender() float32 { return f.ascender }

// Descender returns the quad-bottom metric in text space (em units): negative below the baseline.
func (f *Font) Descender() float32 { return f.descender }

// WMode reports the writing mode: 0 horizontal (all simple fonts), 1 vertical (Type0 with a vertical CMap).
func (f *Font) WMode() uint8 {
	if f.type0 != nil && f.type0.vertical {
		return 1
	}
	return 0
}

// VMetrics returns the vertical-mode metrics for a code in text space: the vertical displacement w1 (usually
// negative — downward) and the position vector (vx, vy) displacing the glyph from its horizontal origin
// (ISO 32000-2 9.7.4.3). Only meaningful when WMode() is 1.
func (f *Font) VMetrics(code uint32) (w1, vx, vy float32) {
	if f.type0 == nil {
		return -1, f.Width(code) / 2, 0.88
	}
	cid := f.type0.cmap.cid(code)
	return f.type0.cidVMetrics(cid, f.type0.cidWidth(cid))
}

// ForEachCode decodes a PDF string operand into character codes: one byte per code for simple fonts, the
// CMap's codespace ranges for composite fonts. oneByte reports whether the code came from a single byte (the
// word-spacing rule applies only to single-byte code 32, ISO 32000-2 9.3.3).
func (f *Font) ForEachCode(s []byte, fn func(code uint32, oneByte bool) bool) {
	if f.type0 != nil {
		for len(s) > 0 {
			code, n := f.type0.cmap.nextCode(s)
			if n <= 0 { // Defensive: nextCode always consumes, but never risk a spin here.
				n = 1
			}
			if !fn(code, n == 1) {
				return
			}
			s = s[n:]
		}
		return
	}
	for _, b := range s {
		if !fn(uint32(b), true) {
			return
		}
	}
}

// buildUnicode fills the code→rune table: glyph name through the Adobe Glyph List (including its uniXXXX and
// uXXXXXX conventions), else the code itself for ASCII, else unknown. A /ToUnicode CMap, when present, takes
// precedence over this table at lookup time (see Unicode).
func buildUnicode(f *Font) {
	for code := range 256 {
		if name := f.enc[code]; name != "" {
			if s := GlyphNameToUnicode(name); s != "" {
				runes := []rune(s)
				f.uni[code] = runes[0]
				continue
			}
		}
		if code >= 32 && code < 127 {
			f.uni[code] = rune(code)
		}
	}
}

// GlyphNameToUnicode implements the AGL algorithm for one glyph name: strip any suffix after the first
// period, split ligature components on underscores, then resolve each component via the AGL, the uniXXXX
// (one or more 4-hex-digit UTF-16 values) form, or the uXXXX[XX] form. Returns "" when nothing resolves.
func GlyphNameToUnicode(name string) string {
	if name == "" {
		return ""
	}
	if dot := strings.IndexByte(name, '.'); dot > 0 {
		name = name[:dot]
	}
	agl := data.AGL()
	var sb strings.Builder
	for _, part := range strings.Split(name, "_") {
		switch {
		case agl[part] != "":
			sb.WriteString(agl[part])
		case strings.HasPrefix(part, "uni") && len(part) >= 7 && (len(part)-3)%4 == 0:
			for i := 3; i+4 <= len(part); i += 4 {
				v, ok := parseHex(part[i : i+4])
				if !ok || (v >= 0xD800 && v <= 0xDFFF) {
					return ""
				}
				sb.WriteRune(rune(v))
			}
		case strings.HasPrefix(part, "u") && len(part) >= 5 && len(part) <= 7:
			v, ok := parseHex(part[1:])
			if !ok || v > 0x10FFFF || (v >= 0xD800 && v <= 0xDFFF) {
				return ""
			}
			sb.WriteRune(rune(v))
		}
	}
	return sb.String()
}

// parseHex parses an uppercase-or-lowercase hex string (the AGL specifies uppercase; be lenient).
func parseHex(s string) (uint32, bool) {
	var v uint32
	for i := range len(s) {
		c := s[i]
		var d uint32
		switch {
		case c >= '0' && c <= '9':
			d = uint32(c - '0')
		case c >= 'A' && c <= 'F':
			d = uint32(c-'A') + 10
		case c >= 'a' && c <= 'f':
			d = uint32(c-'a') + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	return v, true
}
