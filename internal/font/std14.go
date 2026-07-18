// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package font

import "strings"

// Canonical PostScript names of the 14 standard fonts.
const (
	stdCourier              = "Courier"
	stdCourierBold          = "Courier-Bold"
	stdCourierBoldOblique   = "Courier-BoldOblique"
	stdCourierOblique       = "Courier-Oblique"
	stdHelvetica            = "Helvetica"
	stdHelveticaBold        = "Helvetica-Bold"
	stdHelveticaBoldOblique = "Helvetica-BoldOblique"
	stdHelveticaOblique     = "Helvetica-Oblique"
	stdSymbol               = "Symbol"
	stdTimesBold            = "Times-Bold"
	stdTimesBoldItalic      = "Times-BoldItalic"
	stdTimesItalic          = "Times-Italic"
	stdTimesRoman           = "Times-Roman"
	stdZapfDingbats         = "ZapfDingbats"
)

// std14Aliases maps the well-known non-canonical BaseFont names to the 14 standard fonts' canonical PostScript names.
// The set follows the conventional mapping deployed viewers share (compare pdf.js's getStdFontMap, Apache-2.0); names
// not listed fall back to the flag/name heuristics in standard14Name.
var std14Aliases = map[string]string{
	"Arial":                        stdHelvetica,
	"Arial-Bold":                   stdHelveticaBold,
	"Arial-BoldItalic":             stdHelveticaBoldOblique,
	"Arial-Italic":                 stdHelveticaOblique,
	"Arial-BoldItalicMT":           stdHelveticaBoldOblique,
	"Arial-BoldMT":                 stdHelveticaBold,
	"Arial-ItalicMT":               stdHelveticaOblique,
	"ArialMT":                      stdHelvetica,
	"ArialUnicodeMS":               stdHelvetica,
	"Courier":                      stdCourier,
	"Courier-Bold":                 stdCourierBold,
	"Courier-BoldOblique":          stdCourierBoldOblique,
	"Courier-Oblique":              stdCourierOblique,
	"CourierNew":                   stdCourier,
	"CourierNew-Bold":              stdCourierBold,
	"CourierNew-BoldItalic":        stdCourierBoldOblique,
	"CourierNew-Italic":            stdCourierOblique,
	"CourierNewPS-BoldItalicMT":    stdCourierBoldOblique,
	"CourierNewPS-BoldMT":          stdCourierBold,
	"CourierNewPS-ItalicMT":        stdCourierOblique,
	"CourierNewPSMT":               stdCourier,
	"Helvetica":                    stdHelvetica,
	"Helvetica-Bold":               stdHelveticaBold,
	"Helvetica-BoldItalic":         stdHelveticaBoldOblique,
	"Helvetica-BoldOblique":        stdHelveticaBoldOblique,
	"Helvetica-Italic":             stdHelveticaOblique,
	"Helvetica-Oblique":            stdHelveticaOblique,
	"Symbol":                       stdSymbol,
	"Times-Bold":                   stdTimesBold,
	"Times-BoldItalic":             stdTimesBoldItalic,
	"Times-Italic":                 stdTimesItalic,
	"Times-Roman":                  stdTimesRoman,
	"TimesNewRoman":                stdTimesRoman,
	"TimesNewRoman-Bold":           stdTimesBold,
	"TimesNewRoman-BoldItalic":     stdTimesBoldItalic,
	"TimesNewRoman-Italic":         stdTimesItalic,
	"TimesNewRomanPS":              stdTimesRoman,
	"TimesNewRomanPS-Bold":         stdTimesBold,
	"TimesNewRomanPS-BoldItalic":   stdTimesBoldItalic,
	"TimesNewRomanPS-BoldItalicMT": stdTimesBoldItalic,
	"TimesNewRomanPS-BoldMT":       stdTimesBold,
	"TimesNewRomanPS-Italic":       stdTimesItalic,
	"TimesNewRomanPS-ItalicMT":     stdTimesItalic,
	"TimesNewRomanPSMT":            stdTimesRoman,
	"TimesNewRomanPSMT-Bold":       stdTimesBold,
	"ZapfDingbats":                 stdZapfDingbats,
}

// standard14Name maps a (subset-prefix-stripped) BaseFont name plus descriptor flags to the canonical standard-14 font
// that substitutes for it. The mapping is deterministic and never consults system fonts: exact aliases first, then
// style parsing of the name (",Bold", "-Oblique", ...) combined with the fixed-pitch/serif/italic descriptor flags.
func standard14Name(base string, flags int) string {
	normalized := strings.ReplaceAll(base, " ", "")
	normalized = strings.ReplaceAll(normalized, ",", "-")
	if canonical, ok := std14Aliases[normalized]; ok {
		return canonical
	}
	lower := strings.ToLower(normalized)
	bold := strings.Contains(lower, "bold") || flags&FlagForceBold != 0
	italic := strings.Contains(lower, "italic") || strings.Contains(lower, "oblique") || flags&FlagItalic != 0
	switch {
	case strings.Contains(lower, "courier") || strings.Contains(lower, "mono") || flags&FlagFixedPitch != 0:
		switch {
		case bold && italic:
			return stdCourierBoldOblique
		case bold:
			return stdCourierBold
		case italic:
			return stdCourierOblique
		default:
			return stdCourier
		}
	case strings.Contains(lower, "times") || strings.Contains(lower, "georgia") || strings.Contains(lower, "garamond") ||
		strings.Contains(lower, "book") || flags&FlagSerif != 0:
		switch {
		case bold && italic:
			return stdTimesBoldItalic
		case bold:
			return stdTimesBold
		case italic:
			return stdTimesItalic
		default:
			return stdTimesRoman
		}
	default:
		switch {
		case bold && italic:
			return stdHelveticaBoldOblique
		case bold:
			return stdHelveticaBold
		case italic:
			return stdHelveticaOblique
		default:
			return stdHelvetica
		}
	}
}

// substituteMetrics returns the text-space ascender/descender used for quads when a font is substituted, reproducing
// the oracle's rules (pinned by the subst-metrics, std14-styles, and text-std14 corpus probes): when the font
// dictionary carries a descriptor, each slot comes from its /Ascent or /Descent when that value is nonzero, else that
// slot's default (0.8 / -0.2) — even for standard-14 BaseFont names. Only descriptor-less fonts use the substitute font
// program's own metrics, which for the 14 standard fonts are the pinned FontBBox values of MuPDF's bundled
// replacements.
func substituteMetrics(desc *descriptor, std14 string) (asc, dsc float32) {
	if desc.present {
		asc, dsc = 0.8, -0.2
		if desc.ascent != 0 {
			asc = desc.ascent / 1000
		}
		if desc.descent != 0 {
			dsc = desc.descent / 1000
		}
		return asc, dsc
	}
	if m, ok := nimbusMetrics[std14]; ok {
		return float32(m[0]) / 1000, float32(m[1]) / 1000
	}
	return 0.8, -0.2 // MuPDF's fallback when nothing supplies metrics.
}

// nimbusMetrics holds the oracle-pinned quad metrics of MuPDF's substitute fonts in 1000-unit em space, keyed by
// canonical standard-14 name. Every value was recovered exactly (integer font units) from the std14-styles golden's
// 50-pt probe quads, except ZapfDingbats: MuPDF extracts no searchable Unicode for it, so its entry carries the Adobe
// ZapfDingbats AFM FontBBox (yMin -143, yMax 820) as a documented, deterministic stand-in until something pins it.
var nimbusMetrics = map[string][2]int16{
	stdCourier:              {932, -317},
	stdCourierBold:          {1007, -393},
	stdCourierBoldOblique:   {997, -393},
	stdCourierOblique:       {920, -317},
	stdHelvetica:            {1075, -299},
	stdHelveticaBold:        {1070, -307},
	stdHelveticaBoldOblique: {1073, -309},
	stdHelveticaOblique:     {1070, -284},
	stdTimesBold:            {1044, -341},
	stdTimesBoldItalic:      {972, -324},
	stdTimesItalic:          {951, -270},
	stdTimesRoman:           {1053, -281},
	stdSymbol:               {1010, -293},
	stdZapfDingbats:         {820, -143},
}
