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
	"bytes"
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/store"
)

// Shared sub-test case names for the graphics-state guard tables.
const (
	caseValid    = "valid"
	caseNegative = "negative"
	caseOverflow = "overflow"
)

// TestCMNonFiniteRejected verifies the cm operator rejects a CTM concat that overflows to a non-finite matrix (finite
// operands can still multiply to Inf), leaving the prior CTM in effect so the device never sees NaN/Inf coordinates.
func TestCMNonFiniteRejected(t *testing.T) {
	// The first cm scales by 1e20 (finite in float32); the second squares it to ~1e40, which overflows float32 to +Inf
	// and must be dropped. The paint then carries the first, still-finite, CTM. (PDF reals have no exponent syntax, so
	// the magnitude is written out as a plain decimal.)
	big := "1" + strings.Repeat("0", 20) // 1e20
	rec := run(t, nil, nil, fmt.Sprintf("%[1]s 0 0 %[1]s 0 0 cm %[1]s 0 0 %[1]s 0 0 cm 0 0 m 1 1 l S", big))
	wantOps(t, rec, opStroke)
	ctm := rec.calls[0].ctm
	if !ctm.IsFinite() {
		t.Fatalf("non-finite CTM reached the device: %+v", ctm)
	}
	if ctm.A != 1e20 {
		t.Fatalf("second cm was not rejected: ctm.A = %v, want 1e20", ctm.A)
	}
}

// TestMiterLimitGuarded verifies the M operator rejects non-positive and non-finite miter limits (like the line-width
// guard), keeping the default of 10.
func TestMiterLimitGuarded(t *testing.T) {
	huge := "1" + strings.Repeat("0", 39) // 1e39, which overflows float32 to +Inf
	for _, tc := range []struct {
		name    string
		content string
		want    float32
	}{
		{caseValid, "3.5 M 0 0 m 1 1 l S", 3.5},
		{"zero", "0 M 0 0 m 1 1 l S", 10},
		{caseNegative, "-4 M 0 0 m 1 1 l S", 10},
		{caseOverflow, huge + " M 0 0 m 1 1 l S", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := run(t, nil, nil, tc.content)
			wantOps(t, rec, opStroke)
			if got := rec.calls[0].sp.MiterLimit; got != tc.want {
				t.Fatalf("MiterLimit = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExtGStateMiterLimitGuarded verifies the ExtGState /ML entry applies the same finiteness/positivity guard.
func TestExtGStateMiterLimitGuarded(t *testing.T) {
	for _, tc := range []struct {
		name string
		ml   string
		want float32
	}{
		{caseValid, "2.5", 2.5},
		{caseNegative, "-1", 10},
		{caseOverflow, "1" + strings.Repeat("0", 39), 10}, // 1e39 -> +Inf in float32
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := minimalPDF(fmt.Sprintf("<< /Type /ExtGState /ML %s >>", tc.ml))
			d, err := cos.Open([]byte(pdf))
			if err != nil {
				t.Fatal(err)
			}
			res := cos.Dict{catExtGState: cos.Dict{resGSName: cos.Ref{Num: 1}}}
			rec := run(t, d, res, "/GS0 gs 0 0 m 1 1 l S")
			wantOps(t, rec, opStroke)
			if got := rec.calls[0].sp.MiterLimit; got != tc.want {
				t.Fatalf("MiterLimit = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLineWidthGuarded verifies the w operator rejects a negative or non-finite line width (a real that narrows to +Inf
// in float32), keeping the default width of 1 so no bad stroke width flows to StrokePath.
func TestLineWidthGuarded(t *testing.T) {
	huge := "1" + strings.Repeat("0", 39) // 1e39, which overflows float32 to +Inf
	for _, tc := range []struct {
		name    string
		content string
		want    float32
	}{
		{caseValid, "4 w 0 0 m 1 1 l S", 4},
		{caseNegative, "-4 w 0 0 m 1 1 l S", 1},
		{caseOverflow, huge + " w 0 0 m 1 1 l S", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := run(t, nil, nil, tc.content)
			wantOps(t, rec, opStroke)
			if got := rec.calls[0].sp.Width; got != tc.want {
				t.Fatalf("Width = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExtGStateLineWidthGuarded verifies the ExtGState /LW entry applies the same finiteness guard as the w operator.
func TestExtGStateLineWidthGuarded(t *testing.T) {
	for _, tc := range []struct {
		name string
		lw   string
		want float32
	}{
		{caseValid, "5", 5},
		{caseNegative, "-1", 1},
		{caseOverflow, "1" + strings.Repeat("0", 39), 1}, // 1e39 -> +Inf in float32
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := minimalPDF(fmt.Sprintf("<< /Type /ExtGState /LW %s >>", tc.lw))
			d, err := cos.Open([]byte(pdf))
			if err != nil {
				t.Fatal(err)
			}
			res := cos.Dict{catExtGState: cos.Dict{resGSName: cos.Ref{Num: 1}}}
			rec := run(t, d, res, "/GS0 gs 0 0 m 1 1 l S")
			wantOps(t, rec, opStroke)
			if got := rec.calls[0].sp.Width; got != tc.want {
				t.Fatalf("Width = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDashPhaseGuarded verifies the d operator rejects a non-finite dash phase, leaving the previous dash (empty by
// default) in effect so no NaN/Inf offset reaches the stroker.
func TestDashPhaseGuarded(t *testing.T) {
	huge := "1" + strings.Repeat("0", 39) // 1e39, which overflows float32 to +Inf
	rec := run(t, nil, nil, "[6 3] 1.5 d 0 0 m 1 1 l S")
	wantOps(t, rec, opStroke)
	if sp := rec.calls[0].sp; len(sp.Dash) != 2 || sp.DashPhase != 1.5 {
		t.Fatalf("valid dash rejected: %v phase %v", sp.Dash, sp.DashPhase)
	}
	// Non-finite phase: the whole operator is skipped, so the default (empty) dash and zero phase remain.
	rec = run(t, nil, nil, "[6 3] "+huge+" d 0 0 m 1 1 l S")
	wantOps(t, rec, opStroke)
	if sp := rec.calls[0].sp; len(sp.Dash) != 0 || sp.DashPhase != 0 {
		t.Fatalf("non-finite dash phase accepted: %v phase %v", sp.Dash, sp.DashPhase)
	}
}

// TestExtGStateDashEntriesResolved verifies the individual ExtGState /D dash lengths are resolved before opDash reads
// them. Content-stream operands are always direct, but a /D array lives in the object graph where `[[3 0 R 2] 0]` is
// legal; an unresolved entry fails cos.AsReal and would leave the previous dash pattern in effect.
func TestExtGStateDashEntriesResolved(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gs    string
		extra []string
		dash  []float32
		phase float32
	}{
		{
			name:  "indirect entries",
			gs:    `<< /Type /ExtGState /D [[2 0 R 3 0 R] 4 0 R] >>`,
			extra: []string{"6", "3", "1.5"},
			dash:  []float32{6, 3},
			phase: 1.5,
		},
		{
			name:  "indirect array",
			gs:    `<< /Type /ExtGState /D [2 0 R 0] >>`,
			extra: []string{"[4 2]"},
			dash:  []float32{4, 2},
		},
		{ // Resolution does not make an invalid entry valid: a negative length still skips the operator.
			name:  caseNegative,
			gs:    `<< /Type /ExtGState /D [[6 2 0 R] 0] >>`,
			extra: []string{"-3"},
			dash:  []float32{1, 5},
		},
		{ // An absent object resolves to Null, which is not a number, so the previous dash survives.
			name: "missing entry",
			gs:   `<< /Type /ExtGState /D [[6 9 0 R] 0] >>`,
			dash: []float32{1, 5},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := cos.Open([]byte(minimalPDF(append([]string{tc.gs}, tc.extra...)...)))
			if err != nil {
				t.Fatal(err)
			}
			res := cos.Dict{catExtGState: cos.Dict{resGSName: cos.Ref{Num: 1}}}
			// The content stream installs a dash of its own first, so a skipped /D is visible as that leftover pattern.
			rec := run(t, d, res, "[1 5] 0 d /GS0 gs 0 0 m 1 1 l S")
			wantOps(t, rec, opStroke)
			sp := rec.calls[0].sp
			if len(sp.Dash) != len(tc.dash) {
				t.Fatalf("dash = %v, want %v", sp.Dash, tc.dash)
			}
			for i, want := range tc.dash {
				if sp.Dash[i] != want {
					t.Fatalf("dash = %v, want %v", sp.Dash, tc.dash)
				}
			}
			if sp.DashPhase != tc.phase {
				t.Fatalf("dash phase = %v, want %v", sp.DashPhase, tc.phase)
			}
		})
	}
}

// TestFormMatrixNonFiniteRejected verifies the form XObject /Matrix concatenation is dropped when it overflows to a
// non-finite CTM (like cm), so transformAABB/ClipPath and the form body's paints never see NaN/Inf.
func TestFormMatrixNonFiniteRejected(t *testing.T) {
	big := "1" + strings.Repeat("0", 20) // 1e20; the square (~1e40) overflows float32 to +Inf
	form := fmt.Sprintf(`<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Matrix [%[1]s 0 0 %[1]s 0 0] /Length 24 >>
stream
1 0 0 rg 0 0 5 5 re f
endstream`, big)
	d, err := cos.Open([]byte(minimalPDF(form)))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catXObject: cos.Dict{resFormName: cos.Ref{Num: 1}}}
	// The outer cm scales by 1e20 (finite); multiplying by the form's 1e20 Matrix overflows and must be dropped, so the
	// form body paints under the still-finite outer CTM.
	rec := run(t, d, res, fmt.Sprintf("%[1]s 0 0 %[1]s 0 0 cm /Fm0 Do", big))
	wantOps(t, rec, opClip, opFill, opPopClip)
	fill := rec.calls[1]
	if !fill.ctm.IsFinite() {
		t.Fatalf("non-finite CTM reached the device: %+v", fill.ctm)
	}
	if fill.ctm.A != 1e20 {
		t.Fatalf("form Matrix was not rejected: ctm.A = %v, want 1e20", fill.ctm.A)
	}
}

// TestPatternMatrixNonFiniteRejected verifies an scn-selected pattern is dropped when its /Matrix composed with the
// stream's default-space CTM overflows to a non-finite matrix. numbers6 validates the six /Matrix entries individually,
// so finite entries can still compose to a NaN/Inf Paint.PatternCTM — which the device passes on unchecked as the
// /BBox clip transform and the shader's local matrix. Like cm/Do/Tm, the composition itself must be checked.
func TestPatternMatrixNonFiniteRejected(t *testing.T) {
	big := "1" + strings.Repeat("0", 20) // 1e20; composed with a 1e20 stream CTM the product overflows float32 to +Inf
	cell := "1 0 0 rg 0 0 2 2 re f"
	pdf := minimalPDF(
		`<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 10 0] /Function 2 0 R >>`,
		`<< /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >>`,
		fmt.Sprintf(`<< /PatternType 2 /Shading 1 0 R /Matrix [%[1]s 0 0 %[1]s 0 0] >>`, big),
		fmt.Sprintf("<< /PatternType 1 /PaintType 1 /BBox [0 0 4 4] /XStep 4 /YStep 4 /Resources << >> /Matrix [%[1]s 0 0 %[1]s 0 0] /Length %[2]d >>\nstream\n%[3]s\nendstream",
			big, len(cell), cell),
	)
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catPattern: cos.Dict{"PS": cos.Ref{Num: 3}, "PT": cos.Ref{Num: 4}}}
	for _, name := range []cos.Name{"PS", "PT"} {
		t.Run(string(name), func(t *testing.T) {
			content := []byte(fmt.Sprintf("/Pattern cs /%s scn 0 0 5 5 re f", name))
			// A stream CTM of 1e20 (as a page scale would produce) squares the pattern's own 1e20 scale.
			rec := &recorder{t: t}
			Run(d, res, content, gfx.Matrix{A: 1e20, D: 1e20}, rec, nil)
			wantOps(t, rec) // The pattern is unusable, so the paint must not mark at all.
			// The same pattern under a stream CTM that keeps the composition finite still paints.
			rec = &recorder{t: t}
			Run(d, res, content, gfx.Matrix{A: 2, D: 2}, rec, nil)
			wantOps(t, rec, opFill)
			patCTM := rec.calls[0].paint.PatternCTM
			if !patCTM.IsFinite() {
				t.Fatalf("non-finite PatternCTM reached the device: %+v", patCTM)
			}
			if want := (gfx.Matrix{A: 2e20, D: 2e20}); patCTM != want {
				t.Fatalf("PatternCTM = %+v, want %+v: the guard must not block a finite composition", patCTM, want)
			}
		})
	}
}

// TestImageCacheRetainsAfterCapReached verifies the no-store fallback image cache still caches a newly decoded resource
// once the cap is reached, so the (maxCachedImages+1)-th distinct image is not re-decoded on every Do.
func TestImageCacheRetainsAfterCapReached(t *testing.T) {
	const extra = maxCachedImages + 1
	bodies := make([]string, extra)
	xobjects := cos.Dict{}
	var content strings.Builder
	for i := range extra {
		// A distinct 1x1 DeviceGray image per slot; the sample byte varies so the streams differ.
		bodies[i] = fmt.Sprintf(
			"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray /Length 1 >>\nstream\n%c\nendstream",
			byte(i+1),
		)
		name := cos.Name(fmt.Sprintf("Im%d", i))
		xobjects[name] = cos.Ref{Num: i + 1}
		fmt.Fprintf(&content, "/%s Do ", name)
	}
	// Draw the last (cap-overflowing) image a second time; the LRU must return the same decoded image both times.
	last := cos.Name(fmt.Sprintf("Im%d", extra-1))
	fmt.Fprintf(&content, "/%s Do", last)

	d, err := cos.Open([]byte(minimalPDF(bodies...)))
	if err != nil {
		t.Fatal(err)
	}
	rec := run(t, d, cos.Dict{catXObject: xobjects}, content.String())
	if len(rec.calls) != extra+1 {
		t.Fatalf("recorded %d draws, want %d", len(rec.calls), extra+1)
	}
	first, second := rec.calls[extra-1], rec.calls[extra]
	if first.img == nil || second.img == nil {
		t.Fatalf("image decode failed: %+v / %+v", first.img, second.img)
	}
	if first.img != second.img {
		t.Fatal("repeated draw of a cap-overflowing image re-decoded instead of reusing the cached image")
	}
}

// TestLoadFontCachedFailureReportsMiss verifies a font whose load fails, cached as a negative entry in the budgeted
// store, still reports a miss on later lookups: a typed-nil *font.Font boxed in the cache must not read as a success,
// or a repeated Tf would clear the current font instead of keeping the previous one.
func TestLoadFontCachedFailureReportsMiss(t *testing.T) {
	// Object 1 is a plain integer, so it is not a font dictionary and font.Load never runs — loadFont fails.
	d, err := cos.Open([]byte(minimalPDF("42")))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{cos.Name("Font"): cos.Dict{cos.Name("F1"): cos.Ref{Num: 1}}}
	in := newInterp(d, res, gfx.Identity(), device.Device(nil), store.New(1<<20))

	if f, ok := in.loadFont(cos.Name("F1")); ok || f != nil {
		t.Fatalf("first load of an unloadable font: got (%v, %v), want (nil, false)", f, ok)
	}
	// The second lookup hits the cached negative entry; it must also report a miss.
	if f, ok := in.loadFont(cos.Name("F1")); ok || f != nil {
		t.Fatalf("cached-failure load of an unloadable font: got (%v, %v), want (nil, false)", f, ok)
	}
}

// TestLRUCache exercises the count-bounded LRU directly: recency-ordered eviction, MRU retention across a full cycle,
// and negative (nil-value) entries surviving as cache hits.
func TestLRUCache(t *testing.T) {
	c := newLRUCache[int, *int](2)
	one, two, three := 1, 2, 3
	c.put(1, &one)
	c.put(2, &two)
	// Touch 1 so 2 becomes least-recently-used, then insert 3: 2 must be the one evicted.
	if _, ok := c.get(1); !ok {
		t.Fatal("key 1 missing before eviction")
	}
	c.put(3, &three)
	if _, ok := c.get(2); ok {
		t.Fatal("key 2 should have been evicted as least-recently-used")
	}
	if v, ok := c.get(1); !ok || v != &one {
		t.Fatal("key 1 should have survived as recently used")
	}
	if v, ok := c.get(3); !ok || v != &three {
		t.Fatal("key 3 missing after insertion")
	}
	// A negative entry (nil value) is a hit, not a miss — a cached failure must not be re-attempted.
	c.put(3, nil)
	if v, ok := c.get(3); !ok || v != nil {
		t.Fatalf("negative entry not retained: v=%v ok=%v", v, ok)
	}
}

// TestSpacedShowKeepsOperandBacking verifies the " operator reads its string operand positionally instead of reslicing
// the shared operand list: exec's `operands[:0]` reset keeps a shifted base, so each reslice would shed two slots of
// capacity for the rest of the stream.
func TestSpacedShowKeepsOperandBacking(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF("<< >>")))
	if err != nil {
		t.Fatal(err)
	}
	in := newInterp(d, nil, gfx.Identity(), &recorder{t: t}, nil)
	in.operands = make([]cos.Object, 0, maxOperands)
	want := cap(in.operands)
	const spacedShows = maxOperands // Enough to consume the whole backing array two slots at a time.
	in.exec([]byte("BT " + strings.Repeat(`1 2 (a) " `, spacedShows) + "ET"))
	if got := cap(in.operands); got != want {
		t.Fatalf("operand capacity = %d after %d %q operators, want %d: the backing array was shifted",
			got, spacedShows, `"`, want)
	}
}

// TestInlineDictValuelessKeyBeforeID verifies parseInlineDict terminates the dictionary when a name key has no value
// and runs straight into the ID marker, rather than consuming ID as the value and scanning into the payload: the image
// must still decode and the trailing fill must still paint.
func TestInlineDictValuelessKeyBeforeID(t *testing.T) {
	// /Junk carries no value and is immediately followed by ID; the four gray samples decode as a 2x2 image.
	rec := run(t, nil, nil, "BI /W 2 /H 2 /BPC 8 /CS /G /Junk ID \x00\x01\x02\x03 EI 0 0 1 1 re f")
	wantOps(t, rec, opFillImage, opFill)
	if img := rec.calls[0].img; img.Width != 2 || img.Height != 2 || len(img.Pix) != 16 {
		t.Fatalf("inline image decoded wrong: %+v", img)
	}
	if rec.calls[1].path.Points[2] != (gfx.Point{X: 1, Y: 1}) {
		t.Error("trailing content after a valueless-key inline image was discarded")
	}
}

// TestInlineLengthBeyondDataGuarded verifies isolatePayload does not trust a /L that reaches past the available data:
// the claimed extent is rejected and the payload is delimited by scanning for EI instead, rather than slicing out of
// range.
func TestInlineLengthBeyondDataGuarded(t *testing.T) {
	data := []byte("\x00\x01\x02 EI trailing")
	dict := cos.Dict{"L": cos.Integer(1<<31 - 1)} // A length far past len(data); the guard must reject it.
	payload, end := isolatePayload(dict, data, 0)
	if string(payload) != "\x00\x01\x02" {
		t.Fatalf("payload = %q, want the EI-delimited bytes", payload)
	}
	if want := len("\x00\x01\x02 EI"); end != want {
		t.Fatalf("end = %d, want %d (just past EI)", end, want)
	}
}

// TestSpacedShowOperands verifies the " operator still takes aw and ac from the first two operands and the shown string
// from the third, moving to the next line before showing.
func TestSpacedShowOperands(t *testing.T) {
	d := type3PDF(t)
	// 14 TL sets the leading, so " drops the baseline to y = -14 before showing. Each "A" is 600 glyph units wide
	// (600 × 0.001 × 10pt = 6 text units) and ac adds 3 more, so the second glyph starts 9 to the right.
	rec := run(t, d, resourcesOf(t, d), `BT /T3 10 Tf 14 TL 5 3 (AA) " ET`)
	fills := rec.byOp(opFill)
	if len(fills) != 2 {
		t.Fatalf("fills = %d, want 2 (the third operand is the string to show)", len(fills))
	}
	p0 := fills[0].ctm.Apply(fills[0].path.Points[0])
	p1 := fills[1].ctm.Apply(fills[1].path.Points[0])
	if p0.X != 0 || p0.Y != -14 {
		t.Errorf("first glyph at (%v, %v), want (0, -14): the leading move must precede the show", p0.X, p0.Y)
	}
	if dx := p1.X - p0.X; dx != 9 {
		t.Errorf("second glyph advanced %v, want 9 (6 width + 3 char spacing from the second operand)", dx)
	}
	if p1.Y != p0.Y {
		t.Errorf("second glyph baseline %v, want %v", p1.Y, p0.Y)
	}
}

// TestShortColorOperandsIgnored verifies sc/scn/SC/SCN leave the current color alone when the operand list carries
// fewer numbers than the selected space needs, as g/rg/k already do: installing a short list would let ToNRGBA pad it
// with zeroes, so a bare scn under DeviceCMYK would repaint in white.
func TestShortColorOperandsIgnored(t *testing.T) {
	// (0, 0, 0.8, 0) is the CMYK anchor TestColorOperators pins: 255, 243, 79. All-zero CMYK would be white.
	yellow := color.NRGBA{R: 255, G: 243, B: 79, A: 255}
	// (0, 1, 1, 0) is the CMYK red the same conversion produces.
	red := color.NRGBA{R: 237, G: 28, B: 36, A: 255}
	rec := run(t, nil, nil, `0 0 0.8 0 k 0 0 1 1 re f
scn 0 0 1 1 re f
0.5 0.25 scn 0 0 1 1 re f
0 1 1 0 sc 0 0 1 1 re f`)
	wantOps(t, rec, opFill, opFill, opFill, opFill)
	for i, what := range []string{"k", "a bare scn", "a two-operand scn"} {
		if got := rec.calls[i].paint.Color; got != yellow {
			t.Errorf("fill %d after %s = %v, want %v", i, what, got, yellow)
		}
	}
	if got := rec.calls[3].paint.Color; got != red {
		t.Errorf("a complete four-operand sc = %v, want %v: the guard must not block valid operands", got, red)
	}
	// The stroke operators take the same path.
	rec = run(t, nil, nil, `0 0 0.8 0 K 0 0 m 1 1 l S
SCN 0 0 m 1 1 l S
0 1 1 0 SC 0 0 m 1 1 l S`)
	wantOps(t, rec, opStroke, opStroke, opStroke)
	if got := rec.calls[1].paint.Color; got != yellow {
		t.Errorf("stroke after a bare SCN = %v, want %v", got, yellow)
	}
	if got := rec.calls[2].paint.Color; got != red {
		t.Errorf("stroke after a complete SC = %v, want %v", got, red)
	}
}

// TestFormGroupBBoxNonFiniteGuarded verifies execForm drops a transparency-group bbox whose device-space mapping
// overflows, degrading to the empty rect the way replayMask does. A finite /BBox against a finite CTM can still
// produce ±Inf corner products, and device.BeginGroup documents its bbox as geometry a device may size its work to.
func TestFormGroupBBoxNonFiniteGuarded(t *testing.T) {
	const body = "0 0 1 1 re f"
	d, err := cos.Open([]byte(minimalPDF(fmt.Sprintf(
		`<< /Type /XObject /Subtype /Form /BBox [0 0 50 50] /Group << /S /Transparency >> /Length %d >>
stream
%s
endstream`, len(body), body,
	))))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catXObject: cos.Dict{resFormName: cos.Ref{Num: 1}}}
	// 1e37 is finite in float32, so the cm guard accepts it; the BBox's 50-unit corner then maps to 5e38, which is not.
	huge := "1" + strings.Repeat("0", 37)
	for _, tc := range []struct {
		name    string
		content string
		want    gfx.Rect
	}{
		{caseValid, "2 0 0 2 10 10 cm " + formDo, gfx.Rect{X0: 10, Y0: 10, X1: 110, Y1: 110}},
		{caseOverflow, fmt.Sprintf("%[1]s 0 0 %[1]s 0 0 cm %s", huge, formDo), gfx.Rect{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := run(t, d, res, tc.content)
			groups := rec.byOp("begingroup")
			if len(groups) != 1 {
				t.Fatalf("begingroup calls = %d, want 1 (ops: %v)", len(groups), ops(rec))
			}
			if got := groups[0].rect; got != tc.want {
				t.Fatalf("group bbox = %+v, want %+v", got, tc.want)
			}
			if !groups[0].rect.IsFinite() {
				t.Fatalf("non-finite group bbox reached the device: %+v", groups[0].rect)
			}
		})
	}
}

// formDo paints the shared test form.
const formDo = "/" + string(resFormName) + " Do"

// TestTextMatrixSurvivesNonFiniteAdvance verifies appendGlyphs leaves the text matrix alone when folding in a glyph's
// advance would make it non-finite. Two finite factors (a huge /Widths entry and a huge Tf size) can still multiply to
// ±Inf, and a poisoned in.tm makes newRun return nil for every later show operator until the next BT.
func TestTextMatrixSurvivesNonFiniteAdvance(t *testing.T) {
	// 1e30 glyph units is 1e27 in text space, and 1e27 * a 1e12 point size overflows float32's ~3.4e38 ceiling.
	width := "1" + strings.Repeat("0", 30)
	size := "1" + strings.Repeat("0", 12)
	d, err := cos.Open([]byte(minimalPDF(
		"<< /Font << /F1 2 0 R >> >>",
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 65 /LastChar 65 /Widths [`+width+`] >>`,
	)))
	if err != nil {
		t.Fatal(err)
	}
	// Four glyphs in the first show operator and two more in a second one, all within a single BT/ET.
	rec := run(t, d, resourcesOf(t, d), "BT /F1 "+size+" Tf (AAAA) Tj (AA) Tj ET")
	if len(rec.texts) != 2 {
		t.Fatalf("text calls = %d, want 2 (ops: %+v)", len(rec.texts), rec.texts)
	}
	if rec.texts[0].glyphs != 4 {
		t.Errorf("first show emitted %d glyphs, want 4: the text matrix was poisoned by the first advance",
			rec.texts[0].glyphs)
	}
	if rec.texts[1].glyphs != 2 {
		t.Errorf("second show emitted %d glyphs, want 2: the poisoned matrix outlived the show operator",
			rec.texts[1].glyphs)
	}
}

// overRangeBox is a /BBox whose corners are individually finite but whose extent is not: 3e38 - -1e38 is 4e38, past
// float32's ~3.4e38 ceiling. Every clip built from it must be spelled corner by corner.
var overRangeBox = fmt.Sprintf("[%[1]s %[1]s %[2]s %[2]s]",
	"-1"+strings.Repeat("0", 38), "3"+strings.Repeat("0", 38))

// wantFiniteClipCorners checks that a clip path is the closed rectangle spanning box's corners, with nothing
// non-finite in it.
func wantFiniteClipCorners(t *testing.T, c *call, box gfx.Rect) {
	t.Helper()
	if c.op != opClip {
		t.Fatalf("call is %q, not a clip", c.op)
	}
	for i, pt := range c.path.Points {
		if !isFinitePt(pt.X, pt.Y) {
			t.Fatalf("clip point %d = %v is not finite: the box's extent overflowed", i, pt)
		}
	}
	want := []gfx.Point{
		{X: box.X0, Y: box.Y0}, {X: box.X1, Y: box.Y0}, {X: box.X1, Y: box.Y1}, {X: box.X0, Y: box.Y1},
	}
	if len(c.path.Points) != len(want) {
		t.Fatalf("clip has %d points, want %d", len(c.path.Points), len(want))
	}
	for i, w := range want {
		if c.path.Points[i] != w {
			t.Errorf("clip point %d = %v, want %v", i, c.path.Points[i], w)
		}
	}
}

// TestFormBBoxClipSurvivesOverRangeBox verifies execForm builds the form's /BBox clip from the box's corners rather
// than from an origin plus a computed extent: rectFrom validates the four entries individually, but X1-X0 overflows to
// +Inf for a box spanning more than float32's range, and a clip built from x+w would then degenerate and paint nothing
// where a box that large should clip nothing at all.
func TestFormBBoxClipSurvivesOverRangeBox(t *testing.T) {
	body := "1 0 0 rg 0 0 100 100 re f"
	for _, tc := range []struct {
		name string
		bbox string
		want gfx.Rect
	}{
		{caseValid, "[0 0 200 200]", gfx.Rect{X1: 200, Y1: 200}},
		{caseOverflow, overRangeBox, gfx.Rect{X0: -1e38, Y0: -1e38, X1: 3e38, Y1: 3e38}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := cos.Open([]byte(minimalPDF(fmt.Sprintf(
				"<< /Type /XObject /Subtype /Form /BBox %s /Length %d >>\nstream\n%s\nendstream",
				tc.bbox, len(body), body,
			))))
			if err != nil {
				t.Fatal(err)
			}
			res := cos.Dict{catXObject: cos.Dict{resFormName: cos.Ref{Num: 1}}}
			rec := run(t, d, res, formDo)
			wantOps(t, rec, opClip, opFill, opPopClip)
			wantFiniteClipCorners(t, &rec.calls[0], tc.want)
		})
	}
}

// TestSoftMaskBBoxClipSurvivesOverRangeBox verifies replayMask builds the mask body's clip the same way. Its failure
// mode is the opposite of the form's and worse: the non-finite corners lose the clip rather than emptying it, so mask
// content that belongs inside the box would paint everywhere.
func TestSoftMaskBBoxClipSurvivesOverRangeBox(t *testing.T) {
	const maskBody = "1 g 0 0 25 50 re f"
	for _, tc := range []struct {
		name string
		bbox string
		want gfx.Rect
	}{
		{caseValid, "[0 0 50 50]", gfx.Rect{X1: 50, Y1: 50}},
		{caseOverflow, overRangeBox, gfx.Rect{X0: -1e38, Y0: -1e38, X1: 3e38, Y1: 3e38}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := cos.Open([]byte(minimalPDF(
				fmt.Sprintf(`<< /Type /XObject /Subtype /Form /BBox %s
   /Group << /S /Transparency /CS /DeviceGray >> /Length %d >>
stream
%s
endstream`, tc.bbox, len(maskBody), maskBody),
				`<< /Type /ExtGState /SMask << /S /Luminosity /G 1 0 R >> >>`,
			)))
			if err != nil {
				t.Fatal(err)
			}
			res := cos.Dict{catExtGState: cos.Dict{"GL": cos.Ref{Num: 2}}}
			rec := run(t, d, res, "/GL gs 1 0 0 rg 0 0 10 10 re f")
			wantOps(t, rec, "beginmask", opClip, opFill, opPopClip, "endmask", opFill, "popmask")
			wantFiniteClipCorners(t, &rec.calls[1], tc.want)
		})
	}
}

// TestTilingStepFallbackFinite verifies parseTiling's missing-/XStep fallback gets the same finiteness test the
// file-supplied step gets. The fallback is the cell extent — a subtraction of two validated /BBox entries — so an
// over-range box makes it +Inf, which every step consumer rejects: the pattern would silently paint nothing.
func TestTilingStepFallbackFinite(t *testing.T) {
	const cell = "1 0 0 rg 0 0 2 2 re f"
	for _, tc := range []struct {
		name string
		bbox string
		want float32
	}{
		{caseValid, "[0 0 4 6]", 4},
		{caseOverflow, overRangeBox, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := cos.Open([]byte(minimalPDF(fmt.Sprintf(
				"<< /PatternType 1 /PaintType 1 /BBox %s /Resources << >> /Length %d >>\nstream\n%s\nendstream",
				tc.bbox, len(cell), cell,
			))))
			if err != nil {
				t.Fatal(err)
			}
			res := cos.Dict{catPattern: cos.Dict{"P": cos.Ref{Num: 1}}}
			rec := run(t, d, res, "/Pattern cs /P scn 0 0 5 5 re f")
			wantOps(t, rec, opFill)
			tiling := rec.calls[0].paint.Tiling
			if tiling == nil {
				t.Fatal("expected a tiling payload")
			}
			if !isFinitePt(tiling.XStep, tiling.YStep) || tiling.XStep <= 0 || tiling.YStep <= 0 {
				t.Fatalf("steps (%v, %v) are not usable: the renderer drops the pattern entirely",
					tiling.XStep, tiling.YStep)
			}
			if tiling.XStep != tc.want {
				t.Errorf("XStep = %v, want %v", tiling.XStep, tc.want)
			}
		})
	}
}

// TestTilingCellChildSharesParentState verifies the cell-replay interpreter shares the parent's cycle set, parse
// caches, and image/font LRUs instead of allocating its own: a single fill replays up to 4096 cells, so a fresh set per
// cell is pure garbage, and an unshared LRU would let a cell's decoded images escape the parent's own lookups.
func TestTilingCellChildSharesParentState(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF("<< >>")))
	if err != nil {
		t.Fatal(err)
	}
	parent := newInterp(d, nil, gfx.Identity(), &recorder{t: t}, nil)
	parent.budget = 1234
	child := parent.newChild(nil, gfx.Identity(), &recorder{t: t})
	if child.active == nil || len(parent.active) != 0 {
		t.Fatal("the parent's cycle set is not usable")
	}
	parent.active[cos.Ref{Num: 9}.Key()] = true
	if !child.active[cos.Ref{Num: 9}.Key()] {
		t.Error("the child does not share the parent's cycle set: a cyclic pattern would not terminate")
	}
	if child.caches != parent.caches {
		t.Error("the child does not share the parent's parse caches")
	}
	if child.images != parent.images {
		t.Error("the child does not share the parent's image LRU")
	}
	if child.fonts != parent.fonts {
		t.Error("the child does not share the parent's font LRU")
	}
	if child.budget != parent.budget {
		t.Errorf("child budget = %d, want the parent's %d", child.budget, parent.budget)
	}
	if child.formDepth != parent.formDepth+1 {
		t.Errorf("child formDepth = %d, want %d", child.formDepth, parent.formDepth+1)
	}
	if child.doc != parent.doc || child.st != parent.st {
		t.Error("the child does not share the parent's document or store")
	}
}

// TestStoreBackedInterpSkipsFallbackCaches verifies the per-Run image/font LRUs are built only without a store: with a
// budgeted store wired every image and font lookup goes to the store, so the LRUs would never be consulted.
func TestStoreBackedInterpSkipsFallbackCaches(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF("<< >>")))
	if err != nil {
		t.Fatal(err)
	}
	withStore := newInterp(d, nil, gfx.Identity(), &recorder{t: t}, store.New(1<<20))
	if withStore.images != nil || withStore.fonts != nil {
		t.Error("the no-store fallback caches were built alongside a store")
	}
	// The nil caches must be inert rather than a panic waiting to happen.
	if _, hit := withStore.images.get(cos.Ref{Num: 1}.Key()); hit {
		t.Error("a nil image cache reported a hit")
	}
	withStore.fonts.put(cos.Ref{Num: 1}.Key(), nil)
	// Without a store they are the only cache there is, so they must exist.
	noStore := newInterp(d, nil, gfx.Identity(), &recorder{t: t}, nil)
	if noStore.images == nil || noStore.fonts == nil {
		t.Error("the fallback caches are missing without a store")
	}
}

// countElements totals the array elements and dictionary entries in an operand's whole object tree — the quantity the
// shared element budget bounds.
func countElements(obj cos.Object) int {
	switch v := obj.(type) {
	case cos.Array:
		n := len(v)
		for _, e := range v {
			n += countElements(e)
		}
		return n
	case cos.Dict:
		n := len(v)
		for _, e := range v {
			n += countElements(e)
		}
		return n
	default:
		return 0
	}
}

// parseOneOperand assembles the single operand the source spells out.
func parseOneOperand(t *testing.T, src string) cos.Object {
	t.Helper()
	lex := cos.NewLexer([]byte(src), 0)
	tok, ok := lex.Next()
	if !ok {
		t.Fatal("the first token did not lex")
	}
	obj, objOK := parseTopOperand(lex, tok)
	if !objOK {
		t.Fatal("parseTopOperand rejected the operand outright")
	}
	return obj
}

// TestOperandElementBudget covers the per-operand element cap. An array is a single operand consumed by a single
// operator, so neither the work budget (one unit for the whole TJ) nor maxOperands (a bound on how many operands are
// kept, not on how large one is) charges for its elements.
func TestOperandElementBudget(t *testing.T) {
	t.Run("flat", func(t *testing.T) {
		obj := parseOneOperand(t, "["+strings.Repeat("1 ", maxOperandElements+64)+"]")
		if got := countElements(obj); got != maxOperandElements {
			t.Fatalf("kept %d elements, want the cap of %d", got, maxOperandElements)
		}
	})
	t.Run("nesting shares the budget", func(t *testing.T) {
		// Two sibling arrays, each on its own large enough to fit, together exceed the cap. A per-container cap would
		// keep both in full; the shared budget must hold the total down.
		const per = maxOperandElements/2 + 32
		inner := "[" + strings.Repeat("1 ", per) + "]"
		obj := parseOneOperand(t, "["+inner+inner+"]")
		if got := countElements(obj); got > maxOperandElements {
			t.Fatalf("kept %d elements across the nesting, want at most %d", got, maxOperandElements)
		}
	})
	t.Run("dictionary entries count", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("<<")
		for i := range maxOperandElements + 64 {
			fmt.Fprintf(&sb, " /K%d %d", i, i)
		}
		sb.WriteString(" >>")
		obj := parseOneOperand(t, sb.String())
		if got := countElements(obj); got != maxOperandElements {
			t.Fatalf("kept %d entries, want the cap of %d", got, maxOperandElements)
		}
	})
	t.Run("within the cap is untouched", func(t *testing.T) {
		obj := parseOneOperand(t, "[1 2 [3 4] << /A 5 >>]")
		if got := countElements(obj); got != 7 {
			t.Fatalf("kept %d elements, want all 7", got)
		}
	})
}

// TestOversizedOperandKeepsTokenizerInSync verifies that dropping the elements past the cap does not abandon the array:
// the assembler must still consume through the closing bracket, or every operator after it would be misread.
func TestOversizedOperandKeepsTokenizerInSync(t *testing.T) {
	content := "BT [" + strings.Repeat("1 ", maxOperandElements+64) + "] TJ ET 0 0 m 1 1 l S"
	rec := run(t, nil, nil, content)
	wantOps(t, rec, opStroke)
}

// TestOperandFloodKeepsTheOperatorsOwnOperands verifies the operand cap drops the newest operands past it, keeping the
// front of the list: operators read positionally from the list's start, so keeping the newest maxOperands instead would
// shift every operator's operands by the flood's length.
func TestOperandFloodKeepsTheOperatorsOwnOperands(t *testing.T) {
	for _, flood := range []int{0, 1, maxOperands, 4 * maxOperands} {
		t.Run(fmt.Sprintf("flood %d", flood), func(t *testing.T) {
			// The matrix operands come first, then the flood, then cm: whatever the flood's length, cm must read the six
			// operands the content wrote for it.
			content := "2 0 0 3 5 7 " + strings.Repeat("9 ", flood) + "cm 0 0 m 1 1 l S"
			rec := run(t, nil, nil, content)
			wantOps(t, rec, opStroke)
			want := gfx.Matrix{A: 2, B: 0, C: 0, D: 3, E: 5, F: 7}
			if got := rec.calls[0].ctm; got != want {
				t.Fatalf("ctm = %+v, want %+v: the operand window moved cm's operands", got, want)
			}
		})
	}
}

// TestSoftMaskBackdropNarrowingGuarded verifies a /BC entry is validated after the narrowing to float32: a legal PDF
// number past float32's range is ±Inf once narrowed, and the backdrop it forms is the mask coverage outside the mask's
// bbox, so an unguarded ±Inf would turn a DeviceGray mask group's black backdrop into full white there.
func TestSoftMaskBackdropNarrowingGuarded(t *testing.T) {
	huge := "1" + strings.Repeat("0", 39) // 1e39: finite as a float64, +Inf as a float32
	for _, tc := range []struct {
		name string
		bc   string
		want color.NRGBA
	}{
		{caseValid, "[1]", color.NRGBA{R: 255, G: 255, B: 255, A: 255}},
		{caseOverflow, "[" + huge + "]", color.NRGBA{A: 255}},
		{"lexer infinity", "[" + strings.Repeat("9", 400) + "]", color.NRGBA{A: 255}},
		{caseNegative + " overflow", "[-" + huge + "]", color.NRGBA{A: 255}},
		{"absent", "", color.NRGBA{A: 255}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bc := ""
			if tc.bc != "" {
				bc = "/BC " + tc.bc
			}
			body := "0 0 1 1 re f"
			d, err := cos.Open([]byte(minimalPDF(
				fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Group << /S /Transparency "+
					"/CS /DeviceGray >> /Length %d >>\nstream\n%s\nendstream", len(body), body),
				"<< /Type /ExtGState /SMask << /S /Luminosity /G 1 0 R "+bc+" >> >>",
			)))
			if err != nil {
				t.Fatal(err)
			}
			res := cos.Dict{catExtGState: cos.Dict{resGSName: cos.Ref{Num: 2}}}
			rec := run(t, d, res, "/GS0 gs 0 0 1 1 re f")
			masks := rec.byOp("beginmask")
			if len(masks) != 1 {
				t.Fatalf("recorded %d BeginMask calls, want 1", len(masks))
			}
			if got := masks[0].paint.Color; got != tc.want {
				t.Fatalf("mask backdrop = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestFormCycleGuardIgnoresGeneration verifies the cycle set catches a form re-entering itself under a different
// generation number: object lookup keys on the object number alone, so "1 0 R" and "1 1 R" are the same form, and a
// whole-reference key would leave only maxFormDepth to stop the recursion.
func TestFormCycleGuardIgnoresGeneration(t *testing.T) {
	body := "0 0 1 1 re f /Fm1 Do"
	d, err := cos.Open([]byte(minimalPDF(streamObj(
		"/Type /XObject /Subtype /Form /BBox [0 0 10 10] /Resources << /XObject << /Fm1 1 1 R >> >>", body,
	))))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catXObject: cos.Dict{resFormName: cos.Ref{Num: 1}}}
	rec := run(t, d, res, formDo)
	if got := len(rec.byOp(opFill)); got != 1 {
		t.Errorf("the form ran %d times, want 1: re-entry under generation 1 slipped past the cycle set", got)
	}
}

// TestFormCachesIgnoreGeneration verifies the reference-keyed per-Run caches treat two generations of one object as one
// entry rather than decoding and storing the same body twice.
func TestFormCachesIgnoreGeneration(t *testing.T) {
	body := "0 0 1 1 re f"
	d, err := cos.Open([]byte(minimalPDF(streamObj("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", body))))
	if err != nil {
		t.Fatal(err)
	}
	stream, ok := cos.AsStream(d.Resolve(cos.Ref{Num: 1}))
	if !ok {
		t.Fatal("object 1 is not a stream")
	}
	in := newInterp(d, nil, gfx.Identity(), device.Device(nil), nil)
	first, ok := in.streamBody(cos.Ref{Num: 1}, stream)
	if !ok || len(first) == 0 {
		t.Fatal("the first decode failed")
	}
	second, ok := in.streamBody(cos.Ref{Num: 1, Gen: 7}, stream)
	if !ok {
		t.Fatal("the second decode failed")
	}
	if &first[0] != &second[0] {
		t.Error("generation 7 missed the cache entry generation 0 stored; the same body decoded twice")
	}
	if got := len(in.caches.bodies.entries); got != 1 {
		t.Errorf("the body cache holds %d entries, want 1", got)
	}
}

// TestAnnotRunSharesBudgetAcrossAppearances verifies a page's annotation appearances are bounded as a group: with a
// fresh budget per appearance, a page naming tens of thousands of annotations that all point at one appearance stream
// would re-execute it that many times over.
func TestAnnotRunSharesBudgetAcrossAppearances(t *testing.T) {
	const appearances = 1200
	body := paddedBody("0 0 1 1 re f")
	d, err := cos.Open([]byte(minimalPDF(streamObj("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", body))))
	if err != nil {
		t.Fatal(err)
	}
	ref := cos.Ref{Num: 1}
	stream, ok := cos.AsStream(d.Resolve(ref))
	if !ok {
		t.Fatal("object 1 is not a stream")
	}
	rec := &recorder{t: t}
	annots := NewAnnotRun(nil)
	for range appearances {
		annots.Annot(d, nil, ref, stream, gfx.Identity(), rec)
	}
	wantBounded(t, "the appearance stream", len(rec.byOp(opFill)), appearances, len(body))
	if rec.depth != 0 {
		t.Fatalf("device clip depth %d after the appearances; the auto-unwind failed", rec.depth)
	}
}

// TestAnnotRunSharesBodyCacheAcrossAppearances verifies the decoded-body cache spans the page's appearances too: the
// second annotation naming a stream reuses the first's bytes rather than inflating it again.
func TestAnnotRunSharesBodyCacheAcrossAppearances(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF(streamObj("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", "0 0 1 1 re f"))))
	if err != nil {
		t.Fatal(err)
	}
	ref := cos.Ref{Num: 1}
	stream, ok := cos.AsStream(d.Resolve(ref))
	if !ok {
		t.Fatal("object 1 is not a stream")
	}
	rec := &recorder{t: t}
	annots := NewAnnotRun(nil)
	annots.Annot(d, nil, ref, stream, gfx.Identity(), rec)
	first, hit := annots.caches.bodies.get(ref.Key())
	if !hit {
		t.Fatal("the first appearance cached no body")
	}
	annots.Annot(d, nil, ref, stream, gfx.Identity(), rec)
	second, hit := annots.caches.bodies.get(ref.Key())
	if !hit || &first.data[0] != &second.data[0] {
		t.Error("the second appearance re-decoded the stream instead of hitting the shared body cache")
	}
	if got := len(rec.byOp(opFill)); got != 2 {
		t.Errorf("the appearances drew %d fills, want 2", got)
	}
}

// TestInlineDictElementBudget verifies the inline-image dictionary shares the maxOperandElements allowance every other
// container the interpreter parses gets: one BI followed by millions of "/aN <</b 0>>" pairs costs a single budget
// unit. Entries past the allowance are dropped, but parsing must still reach ID so the payload and the operators after
// it stay in sync.
func TestInlineDictElementBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("BI /W 2 /H 2 /BPC 8 /CS /G ")
	for i := range maxOperandElements {
		fmt.Fprintf(&sb, "/k%d << /a %d >> ", i, i)
	}
	sb.WriteString("ID \x00\x01\x02\x03 EI 0 0 1 1 re f")
	lex := cos.NewLexer([]byte(sb.String()), 0)
	if tok, ok := lex.Next(); !ok || string(tok.Bytes) != "BI" {
		t.Fatal("the BI keyword did not lex")
	}
	dict, ok := parseInlineDict(lex)
	if !ok {
		t.Fatal("the dictionary did not terminate at ID")
	}
	if got := countElements(dict); got > maxOperandElements {
		t.Fatalf("kept %d elements, want at most the shared allowance of %d", got, maxOperandElements)
	}
	// The tokenizer stayed in sync: the whole page still executes, image and trailing fill included.
	rec := run(t, nil, nil, sb.String())
	wantOps(t, rec, opFillImage, opFill)
}

// TestImageCarriesBlendMode verifies an ordinary (non-stencil) image composites under the current blend mode: drawImage
// hands the device a paint carrying /BM and the constant fill alpha, as a path or an image mask does. With a soft mask
// in scope the enclosing composite group carries the blend instead, so the image's own paint must stay Normal/1 there —
// the blend must not apply twice.
func TestImageCarriesBlendMode(t *testing.T) {
	pdf := minimalPDF(
		"<< /Type /XObject /Subtype /Image /Width 2 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceRGB /Length 6 >>\nstream\n\x10\x20\x30\x40\x50\x60\nendstream",
		"<< /Type /XObject /Subtype /Image /Width 4 /Height 1 /ImageMask true /Length 2 >>\nstream\n\x50\x00\nendstream",
		"<< /Type /ExtGState /ca 0.5 /BM /Multiply >>",
		maskFormBody,
		"<< /Type /ExtGState /ca 0.5 /BM /Multiply /SMask << /S /Luminosity /G 4 0 R /BC [1] >> >>",
	)
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{
		catXObject:   cos.Dict{"ImC": cos.Ref{Num: 1}, "ImM": cos.Ref{Num: 2}},
		catExtGState: cos.Dict{resGSName: cos.Ref{Num: 3}, "GSM": cos.Ref{Num: 5}},
	}

	// The image, the stencil and the path fill all pick up /BM /Multiply and ca 0.5 from the same graphics state.
	rec := run(t, d, res, "/GS0 gs /ImC Do /ImM Do 0 0 1 1 re f")
	wantOps(t, rec, opFillImage, opFillMask, opFill)
	for _, c := range rec.calls {
		if c.paint.Blend != device.BlendMultiply {
			t.Errorf("%s blend = %v, want %v", c.op, c.paint.Blend, device.BlendMultiply)
		}
		if c.paint.Alpha != 0.5 {
			t.Errorf("%s alpha = %v, want 0.5", c.op, c.paint.Alpha)
		}
	}

	// Under a soft mask the composite group carries the blend and alpha; the image inside it draws Normal at alpha 1.
	rec = run(t, d, res, "/GSM gs /ImC Do")
	wantOps(t, rec, "begingroup", "beginmask", opClip, opFill, opPopClip, "endmask", opFillImage, "popmask", "endgroup")
	bg := rec.calls[0]
	if bg.alpha != 0.5 || bg.paint.Blend != device.BlendMultiply {
		t.Errorf("composite group alpha/blend = %v/%v, want 0.5/%v", bg.alpha, bg.paint.Blend, device.BlendMultiply)
	}
	img := rec.calls[6]
	if img.paint.Alpha != 1 || img.paint.Blend != device.BlendNormal {
		t.Errorf("masked image paint not reset: %+v", img.paint)
	}
}

// simpleFontPDF builds a document whose object 1 is a resource dictionary carrying a simple Type 1 font /F1 whose
// single-byte codes 'A'-'C' are 500 glyph units wide apiece.
func simpleFontPDF(t *testing.T) *cos.Document {
	t.Helper()
	d, err := cos.Open([]byte(minimalPDF(
		"<< /Font << /F1 2 0 R >> >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 65 /LastChar 67 /Widths [500 500 500] >>",
	)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRunGlyphCountBounded verifies one show operator's run is capped at maxRunGlyphs. The per-glyph budget charge
// bounds a Run's total glyph work but not one run's peak heap: a glyph costs one payload byte in the string operand and
// a whole device.Glyph in the run. A TJ array's strings compose one run, so they share the allowance.
func TestRunGlyphCountBounded(t *testing.T) {
	d := simpleFontPDF(t)
	res := resourcesOf(t, d)
	over := strings.Repeat("A", maxRunGlyphs+64)
	for _, tc := range []struct {
		name    string
		content string
		want    int
	}{
		{"Tj", "BT /F1 1 Tf (" + over + ") Tj ET", maxRunGlyphs},
		{"TJ", "BT /F1 1 Tf [(" + over + ") -100 (" + over + ")] TJ ET", maxRunGlyphs},
		{"under the cap", "BT /F1 1 Tf (ABC) Tj ET", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := run(t, d, res, tc.content)
			if len(rec.texts) != 1 {
				t.Fatalf("text calls = %d, want 1 (%+v)", len(rec.texts), rec.texts)
			}
			if rec.texts[0].glyphs != tc.want {
				t.Fatalf("run holds %d glyphs, want %d", rec.texts[0].glyphs, tc.want)
			}
		})
	}
}

// TestFrameResourceCacheBounded verifies the per-frame name-keyed parse caches stop growing at maxFrameCacheEntries.
// They cache negative results, and content may name any number of undefined resources at one budget unit per operator.
// The cap drops the memo, never the lookup: a defined name must still resolve once the cache is full.
func TestFrameResourceCacheBounded(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF(
		"<< /Shading << /Sh0 2 0 R >> /ColorSpace << /Cs0 3 0 R >> /Pattern << /P0 4 0 R >> >>",
		`<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 1 0]
  /Function << /FunctionType 2 /Domain [0 1] /C0 [0 0 0] /C1 [1 1 1] /N 1 >> >>`,
		"/DeviceRGB",
		"<< /PatternType 2 /Shading 2 0 R >>",
	)))
	if err != nil {
		t.Fatal(err)
	}
	in := newInterp(d, resourcesOf(t, d), gfx.Identity(), &recorder{t: t}, nil)
	for i := range maxFrameCacheEntries + 64 {
		name := cos.Name(fmt.Sprintf("z%d", i))
		if sh := in.shadingFor(name); sh != nil {
			t.Fatalf("undefined shading %s resolved to %+v", name, sh)
		}
		if space, ok := in.colorSpace(name); ok {
			t.Fatalf("undefined color space %s resolved to %+v", name, space)
		}
		if pat := in.resolvePattern(name); pat != nil {
			t.Fatalf("undefined pattern %s resolved to %+v", name, pat)
		}
	}
	frame := &in.frames[len(in.frames)-1]
	for _, tc := range []struct {
		name string
		got  int
	}{
		{"shadings", len(frame.shadings)},
		{"spaces", len(frame.spaces)},
		{"patterns", len(frame.patterns)},
	} {
		if tc.got > maxFrameCacheEntries {
			t.Errorf("the %s cache holds %d entries, want at most %d", tc.name, tc.got, maxFrameCacheEntries)
		}
	}
	if sh := in.shadingFor("Sh0"); sh == nil {
		t.Error("a defined shading stopped resolving once the frame cache filled")
	}
	if _, ok := in.colorSpace("Cs0"); !ok {
		t.Error("a defined color space stopped resolving once the frame cache filled")
	}
	if pat := in.resolvePattern("P0"); pat == nil {
		t.Error("a defined pattern stopped resolving once the frame cache filled")
	}
}

// TestTextMatrixSurvivesNonFiniteTJKick verifies opTJ leaves the text matrix alone when a TJ number's kick would make
// it non-finite: the operand is range-checked, but float32(-n)/1000 still overflows against a large Tf size or Tz, and
// a stored ±Inf would make newRun return nil for every later show operator until the next BT/Tm/Td.
func TestTextMatrixSurvivesNonFiniteTJKick(t *testing.T) {
	size := "1" + strings.Repeat("0", 20)  // 1e20
	kick := "-1" + strings.Repeat("0", 38) // -1e38, finite on its own; /1000 against the size overflows float32
	d := simpleFontPDF(t)
	rec := run(t, d, resourcesOf(t, d), "BT /F1 "+size+" Tf [(AA) "+kick+" (BB)] TJ (CC) Tj ET")
	if len(rec.texts) != 2 {
		t.Fatalf("text calls = %d, want 2 (%+v): the kick poisoned the text matrix", len(rec.texts), rec.texts)
	}
	if rec.texts[0].glyphs != 4 {
		t.Errorf("the TJ run holds %d glyphs, want 4: the strings after the kick were dropped", rec.texts[0].glyphs)
	}
	if rec.texts[1].glyphs != 2 {
		t.Errorf("the following Tj holds %d glyphs, want 2: the poisoned matrix outlived the operator",
			rec.texts[1].glyphs)
	}
}

// TestTextLineMatrixSurvivesNonFiniteMove verifies textMove leaves the line matrix alone when the composed translation
// would be non-finite: Td validates its operands, but the composition still overflows once the operand and Tlm's own
// translation are both near float32's ceiling, and since Td/TD/T*/'/" all recompose from Tlm a stored ±Inf would
// persist until the next Tm or BT.
func TestTextLineMatrixSurvivesNonFiniteMove(t *testing.T) {
	big := "3" + strings.Repeat("0", 38) // 3e38 is finite in float32; twice over is past the ~3.4e38 ceiling
	d := simpleFontPDF(t)
	rec := run(t, d, resourcesOf(t, d),
		"BT /F1 1 Tf "+big+" 0 Td "+big+" 0 Td (AA) Tj 0 -12 Td (BB) Tj ET")
	if len(rec.texts) != 2 {
		t.Fatalf("text calls = %d, want 2 (%+v): the overflowing Td poisoned the line matrix", len(rec.texts),
			rec.texts)
	}
	for i, want := range []int{2, 2} {
		if rec.texts[i].glyphs != want {
			t.Errorf("run %d holds %d glyphs, want %d", i, rec.texts[i].glyphs, want)
		}
	}
}

// TestImageColorSpaceFromResourcesNotCached verifies an image whose /ColorSpace is a bare name resolved through the
// resource frame is not served from the reference-keyed decode cache: imaging falls back to resources /ColorSpace[name]
// for such an image, so the same XObject drawn under two forms that map /CS0 to different spaces decodes to different
// colors.
func TestImageColorSpaceFromResourcesNotCached(t *testing.T) {
	// One 1x1 8-bpc image whose three payload bytes read as red under DeviceRGB and as white under DeviceGray.
	d, err := cos.Open([]byte(minimalPDF(
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /CS0 /Length 3 >>\nstream\n\xff\x00\x00\nendstream",
		"<< /Type /XObject /Subtype /Form /Resources << /XObject << /Im0 1 0 R >> /ColorSpace << /CS0 /DeviceRGB >> >> /Length 8 >>\nstream\n/Im0 Do\nendstream",
		"<< /Type /XObject /Subtype /Form /Resources << /XObject << /Im0 1 0 R >> /ColorSpace << /CS0 /DeviceGray >> >> /Length 8 >>\nstream\n/Im0 Do\nendstream",
	)))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catXObject: cos.Dict{"FmRGB": cos.Ref{Num: 2}, "FmGray": cos.Ref{Num: 3}}}
	rec := run(t, d, res, "/FmRGB Do /FmGray Do")
	draws := rec.byOp(opFillImage)
	if len(draws) != 2 {
		t.Fatalf("recorded %d image draws, want 2 (ops: %v)", len(draws), ops(rec))
	}
	for i, want := range [][]byte{{255, 0, 0, 255}, {255, 255, 255, 255}} {
		img := draws[i].img
		if img == nil {
			t.Fatalf("draw %d decoded nothing", i)
		}
		if !bytes.Equal(img.Pix, want) {
			t.Errorf("draw %d decoded %v, want %v: the resource frame's /CS0 was not consulted", i, img.Pix, want)
		}
	}
}
