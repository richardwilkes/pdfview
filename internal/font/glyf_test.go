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
	glyfXSamePos  = 0x10
	glyfYSamePos  = 0x20
	glyfShortPosX = glyfXShort | glyfXSamePos // A single-byte, non-negative x delta.
	glyfShortPosY = glyfYShort | glyfYSamePos // A single-byte, non-negative y delta.
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
	// Each contour emits at most five verbs, and the budget charges at least one unit per contour, so the total verb
	// count cannot exceed 5*glyfWorkBudget. The unbudgeted walk would emit branch^levels (16.7M) contours.
	if got, limit := len(p.Verbs), 5*glyfWorkBudget; got > limit {
		t.Fatalf("emitted %d verbs; work budget should hold it to <= %d", got, limit)
	}
}

// TestGlyfCompositeCycleSkipped verifies the recursion-path set skips a component that references an ancestor beyond the
// direct self-reference the older guard caught: GID 0 -> GID 1 -> {GID 0 (cycle), GID 2 (leaf)}. The cycle edge must be
// dropped, leaving exactly the one leaf contour.
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

// TestGlyfLocaBoundIsWidthIndependent covers the loca range guard glyphData and path share. The bound was computed as
// int(gid)+1 >= len(g.loca), which on a 32-bit build (GOARCH=386/arm) narrows every gid at or above 2^31 to a negative
// int, passes the guard, and panics indexing loca. The uint64 form must reject those gids on every architecture, and
// must still accept the last real glyph — loca carries one more entry than there are glyphs.
func TestGlyfLocaBoundIsWidthIndependent(t *testing.T) {
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
	// 2 indexes loca's terminating entry (no glyph follows it); the rest are the values whose int narrowing differs by
	// architecture.
	for _, gid := range []uint32{2, 3, 1 << 31, 1<<31 + 7, 0xFFFFFFFF} {
		if gid >= 1<<31 && int32(gid)+1 >= int32(len(g.loca)) {
			t.Errorf("gid %d no longer exercises the 32-bit narrowing this test guards", gid)
		}
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

// TestGlyfRecordBoundIsWidthIndependent covers the other half of the loca guard: the record slice bound. It was written
// as int(end) > len(g.glyfData), and on a 32-bit build (GOARCH=386/arm, which this package's comments explicitly care
// about) int(end) is negative for any long-format loca offset at or above 2^31 — the guard passes and the slice
// expression panics, costing the glyph its outline behind Font.GlyphPath's recover. Like inRange, the comparison must be
// done in uint64 so it holds on every architecture.
func TestGlyfRecordBoundIsWidthIndependent(t *testing.T) {
	record := triangleGlyph()
	g := buildGlyf([][]byte{record})
	if g.glyphData(0) == nil {
		t.Fatal("glyphData(0) = nil for the one real glyph")
	}
	// A long-format loca whose terminator lies past the glyf table. Every value here narrows to a negative int32, so a
	// 32-bit build would take the out-of-range end for an in-range one.
	for _, end := range []uint32{1 << 31, 1<<31 + uint32(len(record)), 0xFFFFFFFF} {
		if int32(end) > int32(len(g.glyfData)) {
			t.Errorf("end %d no longer exercises the 32-bit narrowing this test guards", end)
		}
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
