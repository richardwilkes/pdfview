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
	"encoding/binary"
	"testing"

	"github.com/richardwilkes/pdfview/internal/gfx"
)

// glyf simple-glyph point flags (OpenType glyf specification).
const (
	glyfOnCurve   = 0x01
	glyfXShort    = 0x02
	glyfYShort    = 0x04
	glyfRepeat    = 0x08
	glyfXSamePos  = 0x10
	glyfYSamePos  = 0x20
	glyfShortPosX = glyfXShort | glyfXSamePos // A single-byte, non-negative x delta.
	glyfShortPosY = glyfYShort | glyfYSamePos // A single-byte, non-negative y delta.
	glyfSamePos   = glyfXSamePos | glyfYSamePos
)

// glyf composite-component flags (OpenType glyf specification).
const (
	glyfArgsAreWords   = 0x0001
	glyfArgsAreXY      = 0x0002
	glyfMoreComponents = 0x0020
)

// triangleGlyph is a minimal three-on-curve-point simple glyph; walking it emits exactly one contour (MoveTo, three
// LineTos, Close — five verbs).
func triangleGlyph() []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, uint16(int16(1))) // numberOfContours
	b = append(b, 0, 0, 0, 0, 0, 0, 0, 0)                  // XMin/YMin/XMax/YMax (unused by the walker)
	b = binary.BigEndian.AppendUint16(b, 2)                // endPtsOfContours[0]: last point index (three points)
	b = binary.BigEndian.AppendUint16(b, 0)                // instructionLength
	pf := byte(glyfOnCurve | glyfShortPosX | glyfShortPosY)
	b = append(
		b,
		pf, pf, pf, // flags for the three points
		10, 10, 10, // x deltas
		0, 10, 10, // y deltas
	)
	return b
}

// fatGlyph is a one-contour simple glyph declaring n on-curve points in a handful of bytes. Every point reuses one
// flag, and that flag pairs X_SAME/Y_SAME with the short bits clear — the encoding for a zero delta, which costs no
// coordinate bytes — so the whole record is the header plus one repeat run per 256 points. This is the shape that turns
// a per-glyph/per-contour budget into an amplifier: 65536 points fit in about 520 bytes.
func fatGlyph(n int) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, uint16(int16(1))) // numberOfContours
	b = append(b, 0, 0, 0, 0, 0, 0, 0, 0)                  // XMin/YMin/XMax/YMax (unused by the walker)
	b = binary.BigEndian.AppendUint16(b, uint16(n-1))      // endPtsOfContours[0]: last point index
	b = binary.BigEndian.AppendUint16(b, 0)                // instructionLength
	flag := byte(glyfOnCurve | glyfSamePos)                // On-curve, zero x delta, zero y delta.
	for left := n; left > 0; {
		run := min(left, 256) // A repeat byte carries at most 255 additional points.
		if run == 1 {
			b = append(b, flag)
		} else {
			b = append(b, flag|glyfRepeat, byte(run-1))
		}
		left -= run
	}
	return b
}

// compositeGlyph builds a composite glyph record whose components reference each GID in children (as XY-offset
// components with a zero translation).
func compositeGlyph(children ...uint16) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 0xFFFF) // numberOfContours = -1 (composite)
	b = append(b, 0, 0, 0, 0, 0, 0, 0, 0)        // bounding box
	for i, child := range children {
		flags := uint16(glyfArgsAreWords | glyfArgsAreXY)
		if i < len(children)-1 {
			flags |= glyfMoreComponents
		}
		b = binary.BigEndian.AppendUint16(b, flags)
		b = binary.BigEndian.AppendUint16(b, child)
		b = binary.BigEndian.AppendUint16(b, 0) // arg1 (x offset)
		b = binary.BigEndian.AppendUint16(b, 0) // arg2 (y offset)
	}
	return b
}

// buildGlyf assembles the per-GID records into a glyfInfo, deriving loca from the record lengths.
func buildGlyf(records [][]byte) *glyfInfo {
	var glyfData []byte
	loca := make([]uint32, 1, len(records)+1)
	for _, r := range records {
		glyfData = append(glyfData, r...)
		loca = append(loca, uint32(len(glyfData)))
	}
	return &glyfInfo{glyfData: glyfData, loca: loca, upem: 1000}
}

// maxGlyfVerbs is the verb ceiling the work budget implies for one path() call, whatever the glyphs look like: every
// emitted verb is either one of the three a contour costs on its own (MoveTo, the closing segment, ClosePath) or the
// lone verb one converted point can emit, and contours and points are each charged a budget unit — so no path exceeds
// three verbs per unit. The bound must not assume anything about points per contour; that assumption is exactly what a
// fat leaf glyph breaks.
const maxGlyfVerbs = 3 * glyfWorkBudget

// TestGlyfCompositeFanoutBudget builds a hostile chain where each composite glyph is `branch` copies of the next glyph.
// Depth is bounded but branching is not, so an unbudgeted walk would make branch^levels appendGlyph calls (and emit a
// path of the same size) before returning — for these parameters, tens of millions. The work budget must cap both the
// walk and the emitted path.
func TestGlyfCompositeFanoutBudget(t *testing.T) {
	const branch = 8
	const levels = 8 // Composite GIDs 0..levels-1, each pointing at the next; GID levels is the simple leaf.
	records := make([][]byte, levels+1)
	kids := make([]uint16, branch)
	for i := 0; i < levels; i++ {
		for k := range kids {
			kids[k] = uint16(i + 1)
		}
		records[i] = compositeGlyph(kids...)
	}
	records[levels] = triangleGlyph()

	p := buildGlyf(records).path(0)
	if p == nil {
		t.Fatal("path(0) returned nil for an in-range GID")
	}
	// The unbudgeted walk would emit branch^levels (16.7M) contours.
	if got := len(p.Verbs); got > maxGlyfVerbs {
		t.Fatalf("emitted %d verbs; work budget should hold it to <= %d", got, maxGlyfVerbs)
	}
}

// TestGlyfFatLeafPointBudget is the same composite fan-out aimed at a leaf that declares thousands of points instead of
// three. A budget charging only glyphs and contours barely notices this shape — 341 glyph visits plus 256 contours is
// 3.6% of it — while every one of those 256 leaf visits re-emits the whole point run, so a glyf table of a few hundred
// bytes yields a path of a million verbs. Scaled up (65536 points, eight levels of eight-way fan-out) the same file
// reaches ~500M verbs and several GB, an allocation failure Font.GlyphPath's recover() cannot catch. Points must be
// charged.
func TestGlyfFatLeafPointBudget(t *testing.T) {
	const branch = 4
	const levels = 4    // Composite GIDs 0..levels-1, each pointing at the next; GID levels is the fat leaf.
	const points = 4096 // branch^levels * points = 1,048,576 verbs if the walk is not charged per point.
	records := make([][]byte, levels+1)
	kids := make([]uint16, branch)
	for i := range levels {
		for k := range kids {
			kids[k] = uint16(i + 1)
		}
		records[i] = compositeGlyph(kids...)
	}
	records[levels] = fatGlyph(points)

	g := buildGlyf(records)
	if len(g.glyfData) > 512 {
		t.Fatalf("glyf table is %d bytes; this test is about amplification from a tiny table", len(g.glyfData))
	}
	p := g.path(0)
	if p == nil {
		t.Fatal("path(0) returned nil for an in-range GID")
	}
	if got := len(p.Verbs); got > maxGlyfVerbs {
		t.Fatalf("emitted %d verbs from a %d byte glyf table; work budget should hold it to <= %d",
			got, len(g.glyfData), maxGlyfVerbs)
	}
}

// TestGlyfFatContourTruncated covers the single-glyph half of the same amplification: one contour whose point count
// exceeds the whole budget. The walk must stop mid-contour rather than converting every point, and must still leave a
// well-formed path — the contour it opened has to be closed.
func TestGlyfFatContourTruncated(t *testing.T) {
	const points = 4 * glyfWorkBudget
	p := buildGlyf([][]byte{fatGlyph(points)}).path(0)
	if p == nil {
		t.Fatal("path(0) returned nil")
	}
	if got := len(p.Verbs); got > maxGlyfVerbs {
		t.Fatalf("emitted %d verbs for a %d point contour; work budget should hold it to <= %d",
			got, points, maxGlyfVerbs)
	}
	if got := len(p.Verbs); got < 3 {
		t.Fatalf("emitted %d verbs; the contour should be truncated, not dropped", got)
	}
	if p.Verbs[0] != gfx.MoveTo {
		t.Errorf("first verb = %v, want %v", p.Verbs[0], gfx.MoveTo)
	}
	if last := p.Verbs[len(p.Verbs)-1]; last != gfx.ClosePath {
		t.Errorf("last verb = %v, want %v: a truncated contour must still be closed", last, gfx.ClosePath)
	}
}

// TestGlyfFatGlyphUnderBudgetIsWhole is the other side of TestGlyfFatContourTruncated: a glyph with far more points
// than any real one, but still inside the budget, must come through complete. The per-point charge has to bound hostile
// input without clipping legitimate outlines.
func TestGlyfFatGlyphUnderBudgetIsWhole(t *testing.T) {
	const points = 2048 // Well above any real glyph's point count, well below glyfWorkBudget.
	p := buildGlyf([][]byte{fatGlyph(points)}).path(0)
	if p == nil {
		t.Fatal("path(0) returned nil")
	}
	// MoveTo (the first, on-curve point), a LineTo for each of the remaining points, the closing LineTo, and ClosePath.
	if got, want := len(p.Verbs), points+2; got != want {
		t.Fatalf("got %d verbs for a %d point contour, want %d", got, points, want)
	}
}

// TestGlyfCompositeCycleSkipped verifies the recursion-path set skips a component that references an ancestor, not only
// a direct self-reference: GID 0 -> GID 1 -> {GID 0 (cycle), GID 2 (leaf)}. The cycle edge must be dropped, leaving
// exactly the one leaf contour.
func TestGlyfCompositeCycleSkipped(t *testing.T) {
	records := [][]byte{
		compositeGlyph(1),    // GID 0 -> GID 1
		compositeGlyph(0, 2), // GID 1 -> GID 0 (back-edge) and GID 2
		triangleGlyph(),      // GID 2: leaf
	}
	p := buildGlyf(records).path(0)
	if p == nil {
		t.Fatal("path(0) returned nil")
	}
	// One leaf contour: MoveTo, three LineTos, Close.
	want := []gfx.PathVerb{gfx.MoveTo, gfx.LineTo, gfx.LineTo, gfx.LineTo, gfx.ClosePath}
	if len(p.Verbs) != len(want) {
		t.Fatalf("got %d verbs, want %d (cycle edge should be skipped, leaf emitted once)", len(p.Verbs), len(want))
	}
	for i, v := range want {
		if p.Verbs[i] != v {
			t.Fatalf("verb %d = %v, want %v", i, p.Verbs[i], v)
		}
	}
}

// TestGlyfLocaBoundRejectsOutOfRangeGIDs covers the loca range guard glyphData and path share: every gid past the table
// must be turned away rather than indexed (this package's contract is that a hostile program never panics), and the
// last real glyph must still be accepted — loca carries one more entry than there are glyphs.
func TestGlyfLocaBoundRejectsOutOfRangeGIDs(t *testing.T) {
	g := buildGlyf([][]byte{triangleGlyph(), triangleGlyph()}) // GIDs 0 and 1; loca has three entries.
	for _, gid := range []uint32{0, 1} {
		if !g.inRange(gid) {
			t.Errorf("inRange(%d) = false for a glyph the loca table covers", gid)
		}
		if g.glyphData(gid) == nil {
			t.Errorf("glyphData(%d) = nil for a glyph the loca table covers", gid)
		}
		if g.path(gid) == nil {
			t.Errorf("path(%d) = nil for a glyph the loca table covers", gid)
		}
	}
	// 2 indexes loca's terminating entry (no glyph follows it); the rest run out to the top of the uint32 GID range.
	for _, gid := range []uint32{2, 3, 1 << 31, 1<<31 + 7, 0xFFFFFFFF} {
		if g.inRange(gid) {
			t.Errorf("inRange(%d) = true for a gid past the loca table", gid)
		}
		if g.glyphData(gid) != nil {
			t.Errorf("glyphData(%d) returned a record for a gid past the loca table", gid)
		}
		if g.path(gid) != nil {
			t.Errorf("path(%d) returned a path for a gid past the loca table", gid)
		}
	}
}

// TestGlyfSimpleGlyphRenders confirms the budgeted walk still emits an ordinary glyph's single contour unchanged.
func TestGlyfSimpleGlyphRenders(t *testing.T) {
	p := buildGlyf([][]byte{triangleGlyph()}).path(0)
	if p == nil {
		t.Fatal("path(0) returned nil")
	}
	want := []gfx.PathVerb{gfx.MoveTo, gfx.LineTo, gfx.LineTo, gfx.LineTo, gfx.ClosePath}
	if len(p.Verbs) != len(want) {
		t.Fatalf("got %d verbs, want %d", len(p.Verbs), len(want))
	}
	for i, v := range want {
		if p.Verbs[i] != v {
			t.Fatalf("verb %d = %v, want %v", i, p.Verbs[i], v)
		}
	}
}

// TestGlyfRecordBoundRejectsOutOfRangeEnds covers the other half of the loca guard: the record slice bound. A record
// whose end lies past the glyf table must yield no glyph rather than panicking the slice expression, which would cost
// the glyph its outline behind Font.GlyphPath's recover.
func TestGlyfRecordBoundRejectsOutOfRangeEnds(t *testing.T) {
	record := triangleGlyph()
	g := buildGlyf([][]byte{record})
	if g.glyphData(0) == nil {
		t.Fatal("glyphData(0) = nil for the one real glyph")
	}
	// A long-format loca whose terminator lies past the glyf table, out to the top of the uint32 offset range.
	for _, end := range []uint32{1 << 31, 1<<31 + uint32(len(record)), 0xFFFFFFFF} {
		wide := &glyfInfo{glyfData: g.glyfData, loca: []uint32{0, end}, upem: 1000}
		if wide.glyphData(0) != nil {
			t.Errorf("glyphData(0) returned a record for a loca end of %d, past the %d byte glyf table",
				end, len(wide.glyfData))
		}
		if p := wide.path(0); p == nil || len(p.Verbs) != 0 {
			t.Errorf("path(0) = %v for an out-of-range record; want an empty path", p)
		}
	}
}
