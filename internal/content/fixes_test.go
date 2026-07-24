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

// TestLineWidthGuarded verifies the w operator rejects a non-finite line width (a real that narrows to +Inf in float32),
// keeping the default width of 1 so no infinite stroke width flows to StrokePath.
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
	// Valid: finite phase installs both the array and the phase.
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
// them. Content-stream operands are always direct, so the d operator needs no resolution, but a /D array lives in the
// object graph where `[[3 0 R 2] 0]` is legal; an unresolved entry fails cos.AsReal and would leave the previous dash
// pattern in effect instead of the one the ExtGState asked for.
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

// TestImageCacheRetainsAfterCapReached verifies the no-store fallback image cache keeps a resource cached once decoded
// even after the cap is reached, so a repeatedly drawn image is not re-decoded on every Do. Before the LRU, the
// (maxCachedImages+1)-th distinct ref was never cached and each Do handed the device a freshly decoded image.
func TestImageCacheRetainsAfterCapReached(t *testing.T) {
	const extra = maxCachedImages + 1
	bodies := make([]string, extra)
	xobjects := cos.Dict{}
	var content strings.Builder
	for i := range extra {
		// A distinct 1x1 DeviceGray image per slot; the sample byte varies so the streams differ.
		bodies[i] = fmt.Sprintf(
			"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray /Length 1 >>\nstream\n%c\nendstream",
			byte(i+1))
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

// TestLoadFontCachedFailureReportsMiss verifies that a font whose load fails, when cached as a negative entry in the
// budgeted store, still reports a miss on subsequent lookups (like the no-store LRU path). Before the fix, the store
// path returned the typed-nil *font.Font boxed in the cache as a success, so a repeated Tf would clear the current font
// instead of aborting the operator and preserving the previous font.
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
// the shared operand list. Reslicing advanced the list's base pointer permanently — exec's `operands[:0]` reset keeps
// the shifted base — so every " shed two slots of capacity, eventually forcing the operand buffer to reallocate and
// leaving the maxOperands sliding window working against an ever-shrinking buffer.
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

// TestInlineDictValuelessKeyBeforeID verifies parseInlineDict terminates the dictionary when a name key has no value and
// runs straight into the ID marker. Before the fix, the ID keyword was handed to parseOperand, which failed and silently
// consumed it; the loop then scanned into the binary payload, hit EOF, drew nothing, and left the lexer at end-of-stream
// so all trailing page content was discarded. The image must still decode and the trailing fill must still paint.
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

// TestInlineLengthOverflowGuarded verifies isolatePayload does not trust a /L whose value, added to pos, would overflow
// int on a 32-bit build and slip past the pos+length <= len(data) bound into an out-of-range slice. A length beyond the
// available data is rejected and the payload is delimited by scanning for EI instead. (On 64-bit the sum cannot
// overflow, but the guard's fall-back-to-scan behavior is identical and testable on any platform.)
func TestInlineLengthOverflowGuarded(t *testing.T) {
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
