// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package content

import (
	"fmt"
	"image/color"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// type3PDF builds a document whose object 1 is a resource dictionary carrying a Type 3 font /T3 with two
// glyphs: "boxy" (d1 shape: a 500×700 unit rectangle, with an attempted color change that d1 must suppress)
// and "dot" (d0 colored: paints its own green rectangle). The glyph coordinate space is 1000 units per em
// (FontMatrix 0.001), and widths are 600 and 400 glyph units.
func type3PDF(t *testing.T) *cos.Document {
	t.Helper()
	boxy := "600 0 0 0 500 700 d1\n0 0 1 rg\n0 0 500 700 re f"
	dot := "400 0 d0\n0 1 0 rg\n100 100 200 200 re f"
	recursive := "1000 0 0 0 100 100 d1\nBT /T3 10 Tf (R) Tj ET" // Shows its own code: must terminate.
	d, err := cos.Open([]byte(minimalPDF(
		"<< /Font << /T3 2 0 R >> >>",
		`<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1000 800] /FontMatrix [0.001 0 0 0.001 0 0]
  /CharProcs << /boxy 3 0 R /dot 4 0 R /recur 5 0 R >>
  /Encoding << /Type /Encoding /Differences [65 /boxy 66 /dot 82 /recur] >>
  /FirstChar 65 /LastChar 66 /Widths [600 400] >>`,
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(boxy), boxy),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(dot), dot),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(recursive), recursive),
	)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func resourcesOf(t *testing.T, d *cos.Document) cos.Dict {
	t.Helper()
	res, ok := cos.AsDict(d.LoadObject(1))
	if !ok {
		t.Fatal("object 1 is not the resource dict")
	}
	return res
}

// byOp returns the recorded calls with the given op.
func (r *recorder) byOp(op string) []call {
	var out []call
	for i := range r.calls {
		if r.calls[i].op == op {
			out = append(out, r.calls[i])
		}
	}
	return out
}

func TestType3ShapeGlyph(t *testing.T) {
	d := type3PDF(t)
	// Red fill; the d1 glyph's blue rg must be ignored, so the proc's rectangle fills red.
	rec := run(t, d, resourcesOf(t, d), "BT 1 0 0 rg /T3 10 Tf 20 30 Td (A) Tj ET")
	fills := rec.byOp(opFill)
	if len(fills) != 1 {
		t.Fatalf("fills = %d, want 1 (the charproc rectangle)", len(fills))
	}
	f := fills[0]
	if f.paint.Color != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("d1 glyph painted %+v, want the caller's red (its own color op must be suppressed)", f.paint.Color)
	}
	// CTM = FontMatrix(0.001) · Trm(10pt at 20,30): glyph units scale by 0.01. The rect spans 500×700 glyph
	// units → 5×7 text units from (20, 30).
	if len(f.path.Points) < 3 {
		t.Fatalf("charproc path too short: %+v", f.path)
	}
	pt := f.ctm.Apply(f.path.Points[2]) // The rect's (500, 700) corner.
	if pt.X != 25 || pt.Y != 37 {
		t.Errorf("charproc corner mapped to (%v, %v), want (25, 37)", pt.X, pt.Y)
	}
}

func TestType3ColoredGlyph(t *testing.T) {
	d := type3PDF(t)
	rec := run(t, d, resourcesOf(t, d), "BT 1 0 0 rg /T3 10 Tf (B) Tj ET")
	fills := rec.byOp(opFill)
	if len(fills) != 1 {
		t.Fatalf("fills = %d, want 1", len(fills))
	}
	if fills[0].paint.Color != (color.NRGBA{G: 255, A: 255}) {
		t.Errorf("d0 glyph painted %+v, want its own green", fills[0].paint.Color)
	}
}

func TestType3AdvanceUsesWidthsAndMatrix(t *testing.T) {
	d := type3PDF(t)
	// Two "A" glyphs: the second must start 600 glyph units × 0.001 × 10pt = 6 units right of the first.
	rec := run(t, d, resourcesOf(t, d), "BT /T3 10 Tf (AA) Tj ET")
	fills := rec.byOp(opFill)
	if len(fills) != 2 {
		t.Fatalf("fills = %d, want 2", len(fills))
	}
	p0 := fills[0].ctm.Apply(fills[0].path.Points[0])
	p1 := fills[1].ctm.Apply(fills[1].path.Points[0])
	if dx := p1.X - p0.X; dx != 6 {
		t.Errorf("second glyph advanced %v, want 6", dx)
	}
}

func TestType3RecursionTerminates(t *testing.T) {
	d := type3PDF(t)
	rec := run(t, d, resourcesOf(t, d), "BT /T3 10 Tf (R) Tj ET") // The proc shows itself.
	// Termination and balance are the assertions (run checks depth); the self-referential proc is cut by the
	// cycle guard, so nothing fills.
	if n := len(rec.byOp(opFill)); n != 0 {
		t.Errorf("fills = %d, want 0 (cycle must be cut)", n)
	}
}

func TestType3ClipModeDegrades(t *testing.T) {
	d := type3PDF(t)
	// Tr 7 (clip-only) with a Type 3 font: no ClipText accumulation, no EndTextClip, and later content still
	// draws (the degrade documented in emitType3Run).
	rec := run(t, d, resourcesOf(t, d), "BT 7 Tr /T3 10 Tf (A) Tj ET 0 0 5 5 re f")
	for _, c := range rec.calls {
		if c.op == "endtextclip" {
			t.Fatalf("Type 3 clip run produced EndTextClip")
		}
	}
	if n := len(rec.byOp(opFill)); n != 1 { // Only the trailing rectangle.
		t.Errorf("fills = %d, want 1", n)
	}
}
