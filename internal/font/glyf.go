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
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Direct 'glyf' outline extraction, independent of go-text's Font/Face layer. CIDFontType2 programs are
// TrueType subsets that frequently carry no 'cmap' table at all — their code→glyph mapping lives in the PDF
// (CMap + CIDToGIDMap) — and go-text's otfont.NewFont refuses cmap-less programs (the M6 decision-log
// warning). This walker reads head/maxp/loca lazily and converts one glyph at a time: TrueType quadratic
// contours (on/off-curve points with implied midpoints) plus composite glyphs (transformed component
// recursion). go-text's tables package supplies the low-level record parsing; the outline semantics here
// follow the OpenType specification's glyf description.

// glyfInfo is a lazily indexed glyf outline source.
type glyfInfo struct {
	glyfData []byte
	loca     []uint32
	upem     float32
}

// glyfCompositeDepth caps composite-glyph recursion (matching go-text's own cap).
const glyfCompositeDepth = 8

// newGlyfInfo builds the walker from an sfnt loader; nil when the program has no usable glyf/loca pair.
func newGlyfInfo(ld *opentype.Loader, upem float32, numGlyphs int) *glyfInfo {
	if upem <= 0 || numGlyphs <= 0 {
		return nil
	}
	headRaw, err := ld.RawTable(opentype.MustNewTag("head"))
	if err != nil {
		return nil
	}
	head, _, err := tables.ParseHead(headRaw)
	if err != nil {
		return nil
	}
	locaRaw, err := ld.RawTable(opentype.MustNewTag("loca"))
	if err != nil {
		return nil
	}
	loca, err := tables.ParseLoca(locaRaw, numGlyphs, head.IndexToLocFormat == 1)
	if err != nil {
		return nil
	}
	glyfData, err := ld.RawTable(opentype.MustNewTag("glyf"))
	if err != nil {
		return nil
	}
	return &glyfInfo{glyfData: glyfData, loca: loca, upem: upem}
}

// glyphData returns the raw glyf record for a GID (nil for empty glyphs — a valid, blank outcome).
func (g *glyfInfo) glyphData(gid uint32) []byte {
	if int(gid)+1 >= len(g.loca) {
		return nil
	}
	start, end := g.loca[gid], g.loca[gid+1]
	if start >= end || int(end) > len(g.glyfData) {
		return nil
	}
	return g.glyfData[start:end]
}

// path converts one glyph to an em-normalized gfx.Path (nil only when gid is out of range; empty glyphs
// yield an empty path).
func (g *glyfInfo) path(gid uint32) *gfx.Path {
	if int(gid)+1 >= len(g.loca) {
		return nil
	}
	p := &gfx.Path{}
	scale := 1 / g.upem
	g.appendGlyph(p, gid, gfx.Scale(scale, scale), 0)
	return p
}

// appendGlyph appends one glyph's contours under m, recursing into composite components.
func (g *glyfInfo) appendGlyph(p *gfx.Path, gid uint32, m gfx.Matrix, depth int) {
	if depth > glyfCompositeDepth {
		return
	}
	data := g.glyphData(gid)
	if data == nil {
		return
	}
	glyph, _, err := tables.ParseGlyph(data)
	if err != nil {
		return
	}
	switch d := glyph.Data.(type) {
	case tables.SimpleGlyph:
		appendSimpleContours(p, d, m)
	case tables.CompositeGlyph:
		for i := range d.Glyphs {
			part := &d.Glyphs[i]
			if uint32(part.GlyphIndex) == gid {
				continue // Self-reference: hostile.
			}
			g.appendGlyph(p, uint32(part.GlyphIndex), componentMatrix(part).Mul(m), depth+1)
		}
	}
}

// componentMatrix builds a composite component's transform: the 2x2 scale matrix plus the args translation.
// Anchored (point-matching) placement is not supported — the component lands untranslated, the degradation
// deployed rasterizers apply when point indices are unusable; no real corpus file has exercised it.
func componentMatrix(part *tables.CompositeGlyphPart) gfx.Matrix {
	var tx, ty float32
	if !part.IsAnchored() {
		a1, a2 := part.ArgsAsTranslation()
		tx, ty = float32(a1), float32(a2)
	}
	s := part.Scale
	// contourPoint convention (glyf spec): X' = X*s[0] + Y*s[2] + tx, Y' = X*s[1] + Y*s[3] + ty. When
	// scaledComponentOffset is set, the translation is transformed by the scale first.
	if part.IsScaledOffsets() && (tx != 0 || ty != 0) {
		tx, ty = tx*s[0]+ty*s[2], tx*s[1]+ty*s[3]
	}
	return gfx.Matrix{A: s[0], B: s[1], C: s[2], D: s[3], E: tx, F: ty}
}

// appendSimpleContours converts a simple glyph's quadratic contours: runs of off-curve points imply on-curve
// midpoints between them; a contour with no on-curve point starts at the midpoint of its first two points.
func appendSimpleContours(p *gfx.Path, sg tables.SimpleGlyph, m gfx.Matrix) {
	pts := sg.Points
	start := 0
	for _, endIdx := range sg.EndPtsOfContours {
		end := int(endIdx)
		if end < start || end >= len(pts) {
			return // Malformed contour indices: stop appending, keep what is valid so far.
		}
		appendContour(p, pts[start:end+1], m)
		start = end + 1
	}
}

// appendContour emits one closed quadratic contour. Start-point selection follows the convention every
// TrueType rasterizer shares: the first point when it is on-curve, else the last point when that is on-curve,
// else the midpoint of the two (a fully off-curve contour) — with every unconsumed point then processed once
// in order and the contour closed back to the start.
func appendContour(p *gfx.Path, pts []tables.GlyphContourPoint, m gfx.Matrix) {
	const flagOnCurve = 1
	n := len(pts)
	if n == 0 {
		return
	}
	at := func(i int) (gfx.Point, bool) {
		pt := pts[i]
		return m.Apply(gfx.Point{X: float32(pt.X), Y: float32(pt.Y)}), pt.Flag&flagOnCurve != 0
	}
	first, firstOn := at(0)
	last, lastOn := at(n - 1)
	var start gfx.Point
	var lo, hi int // The index range of points still to process, in order.
	switch {
	case firstOn:
		start, lo, hi = first, 1, n-1
	case lastOn:
		start, lo, hi = last, 0, n-2
	default:
		start = gfx.Point{X: (first.X + last.X) / 2, Y: (first.Y + last.Y) / 2}
		lo, hi = 0, n-1
	}
	p.MoveTo(start.X, start.Y)
	var ctrl gfx.Point
	haveCtrl := false
	for i := lo; i <= hi; i++ {
		pt, on := at(i)
		switch {
		case on && haveCtrl:
			p.QuadTo(ctrl.X, ctrl.Y, pt.X, pt.Y)
			haveCtrl = false
		case on:
			p.LineTo(pt.X, pt.Y)
		case haveCtrl: // Two consecutive off-curve points: an implied on-curve midpoint between them.
			mid := gfx.Point{X: (ctrl.X + pt.X) / 2, Y: (ctrl.Y + pt.Y) / 2}
			p.QuadTo(ctrl.X, ctrl.Y, mid.X, mid.Y)
			ctrl = pt
		default:
			ctrl = pt
			haveCtrl = true
		}
	}
	// Close back to the start (through a trailing control point when one is pending).
	if haveCtrl {
		p.QuadTo(ctrl.X, ctrl.Y, start.X, start.Y)
	} else {
		p.LineTo(start.X, start.Y)
	}
	p.Close()
}
