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
	"math"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/type1"
)

// The COS lexer keeps a numeric literal too large for float64 as the correctly-signed infinity, on the documented
// understanding that the downstream finiteness guards reject it (internal/cos/lexer.go). lexInf is such a literal;
// f32Inf is finite as a float64 but overflows the float32 the font metrics narrow to. Both must be rejected, since a
// non-finite advance poisons the interpreter's text matrix (dropping the rest of the text object) and a non-finite
// ascender/descender collapses every stext quad to the page origin.
var (
	lexInf = strings.Repeat("9", 400)
	f32Inf = "1" + strings.Repeat("0", 40)
)

// Shared sub-test case names for the two ways a font-metric number can arrive non-finite.
const (
	caseLexInf = "lexer infinity"
	caseF32Inf = "float32 overflow"
)

// wantFiniteMetrics fails when any of a font's externally visible metrics is non-finite.
func wantFiniteMetrics(t *testing.T, f *Font, codes ...uint32) {
	t.Helper()
	if !isFiniteF(f.Ascender()) || !isFiniteF(f.Descender()) {
		t.Errorf("metrics = %v/%v, want finite", f.Ascender(), f.Descender())
	}
	for _, code := range codes {
		if w := f.Width(code); !isFiniteF(w) {
			t.Errorf("Width(%d) = %v, want finite", code, w)
		}
		if w1, vx, vy := f.VMetrics(code); !isFiniteF(w1) || !isFiniteF(vx) || !isFiniteF(vy) {
			t.Errorf("VMetrics(%d) = %v/%v/%v, want finite", code, w1, vx, vy)
		}
	}
}

// TestDescriptorNonFiniteMetricsIgnored verifies loadDescriptor treats a /MissingWidth, /Ascent, or /Descent that does
// not survive the narrowing to float32 as absent, so the substituted font keeps its defaults rather than reporting an
// infinite advance or quad extent.
func TestDescriptorNonFiniteMetricsIgnored(t *testing.T) {
	for _, tc := range []struct {
		name string
		num  string
	}{
		{caseLexInf, lexInf},
		{caseF32Inf, f32Inf},
		{"negative", "-" + lexInf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := loadFromDict(t,
				`<< /Type /Font /Subtype /Type1 /BaseFont /Whatever /FirstChar 65 /LastChar 65
				    /Widths [500] /FontDescriptor 2 0 R >>`,
				`<< /Type /FontDescriptor /FontName /Whatever /Flags 32 /MissingWidth `+tc.num+
					` /Ascent `+tc.num+` /Descent -`+tc.num+` >>`)
			if err != nil {
				t.Fatal(err)
			}
			wantFiniteMetrics(t, f, 65, 66)
			// The unusable descriptor slots fall back to substituteMetrics' documented defaults.
			if f.Ascender() != 0.8 || f.Descender() != -0.2 {
				t.Errorf("metrics = %v/%v, want the 0.8/-0.2 defaults", f.Ascender(), f.Descender())
			}
			// Code 66 has no /Widths entry, so it lands on the (now zero) /MissingWidth.
			if got := f.Width(66); got != 0 {
				t.Errorf("Width(66) = %v, want 0 (the unusable /MissingWidth was dropped)", got)
			}
			// The well-formed entry is untouched.
			if got := f.Width(65); got != 0.5 {
				t.Errorf("Width(65) = %v, want 0.5", got)
			}
		})
	}
}

// TestSimpleWidthsNonFiniteEntryIgnored verifies a /Widths entry that narrows to ±Inf is left as a gap, so the code
// falls through to /MissingWidth instead of handing the interpreter an infinite advance.
func TestSimpleWidthsNonFiniteEntryIgnored(t *testing.T) {
	for _, tc := range []struct{ name, num string }{
		{caseLexInf, lexInf},
		{caseF32Inf, f32Inf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := loadFromDict(t,
				`<< /Type /Font /Subtype /Type1 /BaseFont /Whatever /FirstChar 65 /LastChar 66
				    /Widths [`+tc.num+` 456] /FontDescriptor 2 0 R >>`,
				`<< /Type /FontDescriptor /FontName /Whatever /Flags 32 /MissingWidth 321 >>`)
			if err != nil {
				t.Fatal(err)
			}
			wantFiniteMetrics(t, f, 65, 66, 67)
			if got := f.Width(65); got != 0.321 {
				t.Errorf("Width(65) = %v, want MissingWidth 0.321 (the unusable entry must be a gap)", got)
			}
			if got := f.Width(66); got != 0.456 {
				t.Errorf("Width(66) = %v, want 0.456: the guard must not drop the valid neighbor", got)
			}
		})
	}
}

// TestCFFMetricsNonFiniteBBoxRejected verifies cffTop.metrics validates the FontBBox it divides, not just the
// FontMatrix-derived upem. This is the shared metrics implementation for bare CFF and embedded Type 1, and a
// non-finite ascender/descender reaching stext puts every search hit's quad at the page origin.
func TestCFFMetricsNonFiniteBBoxRejected(t *testing.T) {
	inf32 := float32(math.Inf(1))
	for _, tc := range []struct {
		name string
		bbox [4]float32
		ok   bool
	}{
		{"valid", [4]float32{0, -200, 1000, 800}, true},
		{"infinite extents", [4]float32{0, -inf32, 0, inf32}, false},
		{"infinite yMax", [4]float32{0, -200, 1000, inf32}, false},
		{"infinite yMin", [4]float32{0, -inf32, 1000, 800}, false},
		{"nan yMax", [4]float32{0, -200, 1000, float32(math.NaN())}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top := cffTop{bbox: tc.bbox, hasBBox: true}
			asc, dsc, ok := top.metrics()
			if ok != tc.ok {
				t.Fatalf("metrics() ok = %v (asc %v, dsc %v), want %v", ok, asc, dsc, tc.ok)
			}
			if ok && (!isFiniteF(asc) || !isFiniteF(dsc)) {
				t.Fatalf("metrics() reported ok with asc %v, dsc %v", asc, dsc)
			}
		})
	}
}

// TestType1NonFiniteFontMatrixOutline verifies glyphPath refuses a non-finite FontMatrix rather than emitting outline
// points the rasterizer would have to cope with. The type1 parser now rejects such a matrix at its source, so this
// pins the adapter's own guard directly.
func TestType1NonFiniteFontMatrixOutline(t *testing.T) {
	prog := &type1.Font{
		Names:       []string{glyphNotdef},
		CharStrings: map[string][]byte{glyphNotdef: {139, 14}}, // "0 endchar"
	}
	info := &t1Info{font: prog, nameGID: map[string]uint32{glyphNotdef: 0}}
	info.matrix = [6]float32{0.001, 0, 0, 0.001, 0, 0}
	if info.glyphPath(0) == nil {
		t.Fatal("a finite FontMatrix produced no path")
	}
	info.matrix[0] = float32(math.Inf(1))
	if p := info.glyphPath(0); p != nil {
		t.Fatalf("a non-finite FontMatrix produced a path: %+v", p)
	}
}

// TestType0NonFiniteMetricsIgnored verifies the composite-font metrics (/DW, /DW2, both /W forms, and /W2) reject
// values that do not narrow to a finite float32, leaving the defaults in place.
func TestType0NonFiniteMetricsIgnored(t *testing.T) {
	for _, tc := range []struct{ name, num string }{
		{caseLexInf, lexInf},
		{caseF32Inf, f32Inf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := tc.num
			f, err := loadFromDict(t,
				"<< /Type /Font /Subtype /Type0 /BaseFont /TestCID /Encoding /Identity-V /DescendantFonts [2 0 R] >>",
				`<< /Type /Font /Subtype /CIDFontType2 /BaseFont /TestCID /FontDescriptor 3 0 R
				    /DW `+n+` /DW2 [`+n+` -`+n+`]
				    /W [1 [`+n+` 500] 10 12 `+n+` 20 [300]]
				    /W2 [1 [`+n+` 0 `+n+`] 10 12 `+n+` 0 0] >>`,
				"<< /Type /FontDescriptor /FontName /TestCID /Flags 4 >>")
			if err != nil {
				t.Fatal(err)
			}
			if f.type0 == nil {
				t.Fatal("Type0 info not populated")
			}
			// Identity-V maps a two-byte code straight to the CID, so the codes below are the CIDs.
			wantFiniteMetrics(t, f, 1, 2, 11, 20, 42)
			if got := f.type0.dw; got != 1 {
				t.Errorf("dw = %v, want the 1.0 default (the unusable /DW must be dropped)", got)
			}
			if got := f.type0.cidWidth(1); got != 0 {
				t.Errorf("cidWidth(1) = %v, want 0 (an unusable /W entry lands as zero)", got)
			}
			if got := f.type0.cidWidth(2); got != 0.5 {
				t.Errorf("cidWidth(2) = %v, want 0.5: the guard must not drop the valid neighbor", got)
			}
			if got := f.type0.cidWidth(11); got != 1 {
				t.Errorf("cidWidth(11) = %v, want the 1.0 default (the unusable c1 c2 w range is skipped)", got)
			}
			if got := f.type0.cidWidth(20); got != 0.3 {
				t.Errorf("cidWidth(20) = %v, want 0.3: the guard must not drop the valid range", got)
			}
		})
	}
}

// TestType3NonFiniteWidthProductIgnored verifies the Type 3 glyph-space→text-space rescale checks its product the way
// its ascender/descender neighbor does: two finite factors (a large FontMatrix x scale and a large width) can still
// multiply to ±Inf.
func TestType3NonFiniteWidthProductIgnored(t *testing.T) {
	big := "1" + strings.Repeat("0", 30) // Finite in float32, but 1e30 * 1e30 is not.
	f, err := loadFromDict(t,
		`<< /Type /Font /Subtype /Type3 /FontBBox [0 0 750 750] /FontMatrix [`+big+` 0 0 0.001 0 0]
		    /CharProcs << /alpha 3 0 R >> /Encoding << /Differences [65 /alpha 66 /alpha] >>
		    /FirstChar 65 /LastChar 66 /Widths [`+big+` 500] /FontDescriptor 2 0 R >>`,
		`<< /Type /FontDescriptor /FontName /T3 /Flags 4 /MissingWidth `+big+` >>`,
		"<< /Length 2 >>\nstream\nd0\nendstream")
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsType3() {
		t.Fatal("font did not load as Type 3")
	}
	wantFiniteMetrics(t, f, 65, 66, 67)
	// Code 65's own product overflows, so it falls through to /MissingWidth — whose product overflows too, leaving 0.
	if got := f.Width(65); got != 0 {
		t.Errorf("Width(65) = %v, want 0 (both the entry and /MissingWidth overflowed)", got)
	}
	// Code 66's 500 glyph units scale by 1e30 to 5e32, which is finite: the guard must not drop it.
	if got := f.Width(66); got != 5e32 {
		t.Errorf("Width(66) = %v, want 5e32", got)
	}
}

// TestSubstituteMetricsNonFiniteDescriptor pins substituteMetrics' own guard. loadDescriptor now rejects a non-finite
// /Ascent or /Descent before it gets here, so this exercises the function directly: a non-finite slot must fall back to
// its default rather than becoming the substituted font's Ascender/Descender for every quad.
func TestSubstituteMetricsNonFiniteDescriptor(t *testing.T) {
	inf32 := float32(math.Inf(1))
	for _, tc := range []struct {
		name     string
		desc     descriptor
		asc, dsc float32
	}{
		{"valid", descriptor{present: true, ascent: 700, descent: -150}, 0.7, -0.15},
		{"infinite ascent", descriptor{present: true, ascent: inf32, descent: -150}, 0.8, -0.15},
		{"infinite descent", descriptor{present: true, ascent: 700, descent: -inf32}, 0.7, -0.2},
		{"nan both", descriptor{present: true, ascent: float32(math.NaN()), descent: float32(math.NaN())}, 0.8, -0.2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asc, dsc := substituteMetrics(&tc.desc, stdHelvetica)
			if asc != tc.asc || dsc != tc.dsc {
				t.Fatalf("substituteMetrics = %v/%v, want %v/%v", asc, dsc, tc.asc, tc.dsc)
			}
		})
	}
}
