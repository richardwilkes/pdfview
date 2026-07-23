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
	"image/color"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// maskFormBody is a luminosity-mask form: white over the left half of its BBox.
const maskFormBody = `<< /Type /XObject /Subtype /Form /BBox [0 0 50 50]
   /Group << /S /Transparency /CS /DeviceGray >> /Length 20 >>
stream
1 g 0 0 25 50 re f
endstream`

// TestSoftMaskEmission pins the per-op soft-mask protocol: mask replay (BeginMask, BBox clip, mask content, PopClip via
// unwind, EndMask), the op with its alpha/blend lifted, PopMask — and the composite group only when the op's constant
// alpha or blend is non-trivial. /SMask /None clears the mask.
func TestSoftMaskEmission(t *testing.T) {
	pdf := minimalPDF(
		maskFormBody,
		`<< /Type /ExtGState /SMask << /S /Luminosity /G 1 0 R /BC [1] >> >>`,
		`<< /Type /ExtGState /SMask /None >>`,
		`<< /Type /ExtGState /ca 0.5 /SMask << /S /Alpha /G 1 0 R >> >>`,
	)
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catExtGState: cos.Dict{
		"GL": cos.Ref{Num: 2}, "GN": cos.Ref{Num: 3}, "GA": cos.Ref{Num: 4},
	}}
	rec := run(t, d, res, "/GL gs 1 0 0 rg 0 0 10 10 re f /GN gs 0 0 10 10 re f")
	wantOps(t, rec, "beginmask", opClip, opFill, opPopClip, "endmask", opFill, "popmask", opFill)
	bm := rec.calls[0]
	if !bm.evenOdd { // luminosity flag
		t.Error("mask not flagged luminosity")
	}
	if bm.paint.Color != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("BC backdrop = %v, want white", bm.paint.Color)
	}
	masked := rec.calls[5]
	if masked.paint.Alpha != 1 || masked.paint.Blend != device.BlendNormal {
		t.Errorf("masked op alpha/blend not reset: %+v", masked.paint)
	}
	if masked.paint.Color != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("masked op color = %v", masked.paint.Color)
	}

	// An op with ca < 1 lifts its alpha into a composite group around the mask span.
	rec = run(t, d, res, "/GA gs 0 0 10 10 re f")
	wantOps(t, rec, "begingroup", "beginmask", opClip, opFill, opPopClip, "endmask", opFill, "popmask", "endgroup")
	bg := rec.calls[0]
	if bg.alpha != 0.5 || bg.paint.Blend != device.BlendNormal {
		t.Errorf("composite group alpha/blend = %v/%v", bg.alpha, bg.paint.Blend)
	}
	if rec.calls[1].evenOdd { // alpha subtype
		t.Error("alpha mask flagged luminosity")
	}
	if rec.calls[6].paint.Alpha != 1 {
		t.Errorf("masked op alpha not reset: %v", rec.calls[6].paint.Alpha)
	}
}

// TestSoftMaskAnchor pins the gs-time CTM anchoring: a cm issued after gs moves the paint but not the mask (the mask
// replay's BBox clip runs under the CTM captured at gs).
func TestSoftMaskAnchor(t *testing.T) {
	pdf := minimalPDF(maskFormBody, `<< /Type /ExtGState /SMask << /S /Luminosity /G 1 0 R >> >>`)
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catExtGState: cos.Dict{resGSName: cos.Ref{Num: 2}}}
	rec := run(t, d, res, "q /GS0 gs 1 0 0 1 7 3 cm 0 0 10 10 re f Q")
	wantOps(t, rec, "beginmask", opClip, opFill, opPopClip, "endmask", opFill, "popmask")
	if got := rec.calls[1].ctm; got != gfx.Identity() {
		t.Errorf("mask clip ctm = %v, want the gs-time CTM (identity)", got)
	}
	if got := rec.calls[5].ctm; got != (gfx.Matrix{A: 1, D: 1, E: 7, F: 3}) {
		t.Errorf("masked op ctm = %v, want the cm-translated CTM", got)
	}
}

// TestSoftMaskBBoxFiniteness pins the bbox handed to BeginMask: the mapped mask /BBox when the composition is usable,
// and the empty rect when it is not — neither the mask form's /Matrix times the gs-time CTM overflowing to non-finite
// nor a finite CTM overflowing the mapped corners may hand the device NaN/Inf geometry.
func TestSoftMaskBBoxFiniteness(t *testing.T) {
	const huge = "100000000000000000000" // 1e20 — finite in float32, but its square is not.
	maskForm := func(bbox, matrix string) string {
		return `<< /Type /XObject /Subtype /Form /BBox ` + bbox + ` /Matrix ` + matrix + `
   /Group << /S /Transparency /CS /DeviceGray >> /Length 20 >>
stream
1 g 0 0 25 50 re f
endstream`
	}
	pdf := minimalPDF(
		maskFormBody,
		`<< /Type /ExtGState /SMask << /S /Luminosity /G 1 0 R >> >>`,
		maskForm("[0 0 50 50]", "["+huge+" 0 0 "+huge+" 0 0]"),
		`<< /Type /ExtGState /SMask << /S /Luminosity /G 3 0 R >> >>`,
		maskForm("[0 0 "+huge+" "+huge+"]", "[1 0 0 1 0 0]"),
		`<< /Type /ExtGState /SMask << /S /Luminosity /G 5 0 R >> >>`,
	)
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catExtGState: cos.Dict{
		"GOK": cos.Ref{Num: 2}, "GCTM": cos.Ref{Num: 4}, "GBOX": cos.Ref{Num: 6},
	}}

	// Usable composition: the mask /BBox mapped through the anchoring CTM.
	rec := run(t, d, res, "/GOK gs 0 0 10 10 re f")
	wantOps(t, rec, "beginmask", opClip, opFill, opPopClip, "endmask", opFill, "popmask")
	if got := rec.calls[0].rect; got != (gfx.Rect{X1: 50, Y1: 50}) {
		t.Errorf("mask bbox = %+v, want the mapped /BBox (0 0 50 50)", got)
	}

	// /Matrix · anchor overflows: the CTM is unusable, so the content replay is skipped AND the bbox degrades.
	rec = run(t, d, res, "q "+huge+" 0 0 "+huge+" 0 0 cm /GCTM gs 0 0 10 10 re f Q")
	wantOps(t, rec, "beginmask", "endmask", opFill, "popmask")
	if got := rec.calls[0].rect; got != (gfx.Rect{}) {
		t.Errorf("mask bbox = %+v, want the empty rect for a non-finite CTM", got)
	}

	// Finite CTM, but the mapped corners overflow: the content still replays, only the bbox degrades.
	rec = run(t, d, res, "q "+huge+" 0 0 "+huge+" 0 0 cm /GBOX gs 0 0 10 10 re f Q")
	wantOps(t, rec, "beginmask", opClip, opFill, opPopClip, "endmask", opFill, "popmask")
	if got := rec.calls[0].rect; got != (gfx.Rect{}) {
		t.Errorf("mask bbox = %+v, want the empty rect for non-finite mapped corners", got)
	}
}

// TestTransparencyGroupEmission pins the Do protocol for /Group /S /Transparency forms: BeginGroup carries the
// isolation/knockout attributes plus the caller's blend and FILL alpha, and the interior runs with alpha/blend/mask
// reset (ISO 32000-2 11.6.6).
func TestTransparencyGroupEmission(t *testing.T) {
	pdf := minimalPDF(
		`<< /Type /XObject /Subtype /Form /BBox [0 0 10 10]
   /Group << /S /Transparency /CS /DeviceRGB /I true /K true >> /Length 22 >>
stream
1 0 0 rg 0 0 5 5 re f
endstream`,
		`<< /Type /ExtGState /ca 0.25 /CA 0.75 /BM /Multiply >>`,
	)
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{
		catXObject:   cos.Dict{resFormName: cos.Ref{Num: 1}},
		catExtGState: cos.Dict{resGSName: cos.Ref{Num: 2}},
	}
	rec := run(t, d, res, "/GS0 gs /Fm0 Do 0 0 2 2 re f")
	wantOps(t, rec, "begingroup", opClip, opFill, opPopClip, "endgroup", opFill)
	bg := rec.calls[0]
	if !bg.evenOdd || !bg.knockout {
		t.Errorf("isolated/knockout = %v/%v, want true/true", bg.evenOdd, bg.knockout)
	}
	if bg.alpha != 0.25 || bg.paint.Blend != device.BlendMultiply {
		t.Errorf("group alpha/blend = %v/%v", bg.alpha, bg.paint.Blend)
	}
	inner := rec.calls[2]
	if inner.paint.Alpha != 1 || inner.paint.Blend != device.BlendNormal {
		t.Errorf("interior alpha/blend not reset: %+v", inner.paint)
	}
	after := rec.calls[5]
	if after.paint.Alpha != 0.25 || after.paint.Blend != device.BlendMultiply {
		t.Errorf("caller state disturbed after group: %+v", after.paint)
	}
}
