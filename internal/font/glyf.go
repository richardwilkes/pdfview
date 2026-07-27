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

// Direct 'glyf' outline extraction, independent of go-text's Font/Face layer. CIDFontType2 programs are TrueType
// subsets that frequently carry no 'cmap' table at all — their code→glyph mapping lives in the PDF (CMap + CIDToGIDMap)
// — and go-text's otfont.NewFont refuses cmap-less programs. This walker reads head/maxp/loca lazily and converts one
// glyph at a time: TrueType quadratic contours (on/off-curve points with implied midpoints) plus composite glyphs
// (transformed component recursion). go-text's tables package supplies the low-level record parsing; the outline
// semantics here follow the OpenType specification's glyf description.

// glyfInfo is a lazily indexed glyf outline source.
type glyfInfo struct {
	glyfData []byte
	loca     []uint32
	upem     float32
}

// glyfCompositeDepth caps composite-glyph recursion (matching go-text's own cap).
const glyfCompositeDepth = 8

// glyfWorkBudget caps the total work one path() call may spend: one unit per component visited, one per contour
// emitted, and one per contour point converted. The depth cap alone bounds recursion depth but not branching, so a
// chain where glyph i is N components of glyph i+1 costs N^depth appendGlyph calls (and a path that grows just as fast)
// without this ceiling. Points must be charged too, or the same amplification runs through a fat leaf instead: a simple
// glyph can declare 65536 points in a few hundred bytes (repeat-flag runs whose X_SAME/Y_SAME deltas cost no coordinate
// bytes at all), so a budget that only counts glyphs and contours still lets thousands of leaf visits emit hundreds of
// millions of verbs — gigabytes of gfx.Path, an allocation failure no recover() can catch. Sized like maxSegments in
// the Type 1 interpreter; real glyphs stay in the low hundreds.
const glyfWorkBudget = 1 << 14

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

// inRange reports whether gid and its successor both index the loca table (a glyph's record runs from loca[gid] to
// loca[gid+1], so the last entry is a terminator, not a glyph). The comparison is done in uint64 rather than by
// converting gid to int: on a 32-bit build int(gid) is negative for any gid at or above 2^31, which would let the guard
// pass and the loca index panic. Font.GlyphPath rejects gids above 0xFFFF before reaching here, but the bound belongs
// with the indexing it protects.
func (g *glyfInfo) inRange(gid uint32) bool {
	return uint64(gid)+1 < uint64(len(g.loca))
}

// glyphData returns the raw glyf record for a GID (nil for empty glyphs — a valid, blank outcome).
func (g *glyfInfo) glyphData(gid uint32) []byte {
	if !g.inRange(gid) {
		return nil
	}
	start, end := g.loca[gid], g.loca[gid+1]
	// The upper bound is compared in uint64 for the reason inRange gives: on a 32-bit build int(end) is negative for
	// any long-format loca offset at or above 2^31, which would let the guard pass and the slice expression below
	// panic (recovered by Font.GlyphPath, but only after the glyph is lost). The bound belongs with the indexing it
	// protects.
	if start >= end || uint64(end) > uint64(len(g.glyfData)) {
		return nil
	}
	return g.glyfData[start:end]
}

// path converts one glyph to an em-normalized gfx.Path (nil only when gid is out of range; empty glyphs yield an empty
// path).
func (g *glyfInfo) path(gid uint32) *gfx.Path {
	if !g.inRange(gid) {
		return nil
	}
	p := &gfx.Path{}
	scale := 1 / g.upem
	budget := glyfWorkBudget
	g.appendGlyph(p, gid, gfx.Scale(scale, scale), 0, &budget, map[uint32]bool{})
	return p
}

// appendGlyph appends one glyph's contours under m, recursing into composite components. budget is decremented once per
// visited glyph, once per emitted contour and once per converted point, and stops the walk when exhausted; onPath holds
// the composite GIDs on the current recursion path, so a component that references any ancestor (a self-reference or a
// longer A->B->A cycle) is skipped rather than followed.
func (g *glyfInfo) appendGlyph(p *gfx.Path, gid uint32, m gfx.Matrix, depth int, budget *int, onPath map[uint32]bool) {
	if depth > glyfCompositeDepth || *budget <= 0 {
		return
	}
	*budget--
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
		appendSimpleContours(p, d, m, budget)
	case tables.CompositeGlyph:
		onPath[gid] = true
		for i := range d.Glyphs {
			part := &d.Glyphs[i]
			child := uint32(part.GlyphIndex)
			if onPath[child] {
				continue // References an ancestor on the recursion path (self or longer cycle): hostile.
			}
			g.appendGlyph(p, child, componentMatrix(part).Mul(m), depth+1, budget, onPath)
			if *budget <= 0 {
				break
			}
		}
		delete(onPath, gid)
	}
}

// componentMatrix builds a composite component's transform: the 2x2 scale matrix plus the args translation. Anchored
// (point-matching) placement is not supported — the component lands untranslated, the degradation deployed rasterizers
// apply when point indices are unusable; no real corpus file has exercised it.
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

// appendSimpleContours converts a simple glyph's quadratic contours: runs of off-curve points imply on-curve midpoints
// between them; a contour with no on-curve point starts at the midpoint of its first two points.
func appendSimpleContours(p *gfx.Path, sg tables.SimpleGlyph, m gfx.Matrix, budget *int) {
	pts := sg.Points
	start := 0
	for _, endIdx := range sg.EndPtsOfContours {
		if *budget <= 0 {
			return
		}
		*budget--
		end := int(endIdx)
		if end < start || end >= len(pts) {
			return // Malformed contour indices: stop appending, keep what is valid so far.
		}
		appendContour(p, pts[start:end+1], m, budget)
		start = end + 1
	}
}

// appendContour emits one closed quadratic contour. Start-point selection follows the convention every TrueType
// rasterizer shares: the first point when it is on-curve, else the last point when that is on-curve, else the midpoint
// of the two (a fully off-curve contour) — with every unconsumed point then processed once in order and the contour
// closed back to the start. Each processed point costs one budget unit and emits at most one verb, so the walk stops
// mid-contour once the budget runs out; the contour is still closed, leaving a well-formed (if truncated) path.
func appendContour(p *gfx.Path, pts []tables.GlyphContourPoint, m gfx.Matrix, budget *int) {
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
		if *budget <= 0 {
			break
		}
		*budget--
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
