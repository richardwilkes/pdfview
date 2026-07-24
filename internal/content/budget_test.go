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

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// padding is the whitespace each repeatedly executed body in these tests carries: enough that re-running it is real
// work, while the operators it executes stay countable on one hand. The budget must charge for the scan, not just for
// the two operators the body dispatches.
const padding = 1 << 16

// paddedBody returns a content-stream body of padding bytes of whitespace followed by tail.
func paddedBody(tail string) string {
	return strings.Repeat(" ", padding) + tail
}

// streamObj wraps body in a stream object whose dictionary carries entries plus the matching /Length.
func streamObj(entries, body string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", entries, len(body), body)
}

// wantBounded checks that a repeatedly triggered body ran at least once, stopped before the last invocation, and ran no
// more times than its per-invocation body charge allows.
func wantBounded(t *testing.T, what string, ran, invocations, bodyLen int) {
	t.Helper()
	limit := maxTotalOps / bodyCost(bodyLen)
	switch {
	case ran == 0:
		t.Fatalf("%s never ran: the budget charge is too aggressive", what)
	case ran >= invocations:
		t.Fatalf("%s ran all %d times: re-running a %d byte body is not charged to the work budget",
			what, invocations, bodyLen)
	case ran > limit:
		t.Fatalf("%s ran %d times, want at most %d (one body charge per invocation)", what, ran, limit)
	}
}

// TestFormBodyChargedPerInvocation verifies that repeatedly invoking one form XObject drains the work budget in
// proportion to the body it re-runs. The cycle set only stops recursive re-entry, so before the charge a page of
// sequential Do operators — one budget unit each — could re-decode and re-scan a multi-megabyte body per invocation.
func TestFormBodyChargedPerInvocation(t *testing.T) {
	const invocations = 1200
	body := paddedBody("0 0 1 1 re f")
	d, err := cos.Open([]byte(minimalPDF(streamObj("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", body))))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catXObject: cos.Dict{resFormName: cos.Ref{Num: 1}}}
	rec := run(t, d, res, strings.Repeat("/Fm0 Do ", invocations))
	wantBounded(t, "the form body", len(rec.byOp(opFill)), invocations, len(body))
}

// TestType3CharprocChargedPerGlyph verifies the same for Type 3 charprocs, which re-run once per glyph shown while
// appendGlyphs charges only one unit per glyph.
func TestType3CharprocChargedPerGlyph(t *testing.T) {
	const glyphs = 1200
	proc := "600 0 d0" + paddedBody("0 0 500 700 re f")
	d, err := cos.Open([]byte(minimalPDF(
		"<< /Font << /T3 2 0 R >> >>",
		`<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1000 800] /FontMatrix [0.001 0 0 0.001 0 0]
  /CharProcs << /boxy 3 0 R >> /Encoding << /Type /Encoding /Differences [65 /boxy] >>
  /FirstChar 65 /LastChar 65 /Widths [600] >>`,
		streamObj("", proc),
	)))
	if err != nil {
		t.Fatal(err)
	}
	content := "BT /T3 10 Tf (" + strings.Repeat("A", glyphs) + ") Tj ET"
	rec := run(t, d, resourcesOf(t, d), content)
	wantBounded(t, "the charproc", len(rec.byOp(opFill)), glyphs, len(proc))
}

// TestSoftMaskReplayChargedPerPaint verifies the same for an ExtGState soft mask, whose form body replays once per
// painting operation it gates.
func TestSoftMaskReplayChargedPerPaint(t *testing.T) {
	const paints = 1200
	maskBody := paddedBody("0 0 1 1 re n") // Paints nothing, so only the page's own fills are recorded.
	d, err := cos.Open([]byte(minimalPDF(
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", maskBody),
		"<< /Type /ExtGState /SMask << /S /Alpha /G 1 0 R >> >>",
	)))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{catExtGState: cos.Dict{resGSName: cos.Ref{Num: 2}}}
	rec := run(t, d, res, "/GS0 gs "+strings.Repeat("0 0 1 1 re f ", paints))
	// The mask replay is what the budget must charge: the page's fills keep being emitted, but once the budget is spent
	// the replay executes nothing, so the mask's own clip stops appearing.
	wantBounded(t, "the mask body", len(rec.byOp(opClip)), paints, len(maskBody))
}

// TestStreamBodyCachedAndChargedPerCall verifies that a referenced body decodes once per Run — the repeat is a cache
// hit, returning the same bytes — while every invocation is still charged, because exec re-scans the body each time.
func TestStreamBodyCachedAndChargedPerCall(t *testing.T) {
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
	in := newInterp(d, nil, gfx.Identity(), device.Device(nil), nil)
	before := in.budget
	first, ok := in.streamBody(ref, stream)
	if !ok || len(first) != len(body) {
		t.Fatalf("first decode: ok=%v len=%d, want true/%d", ok, len(first), len(body))
	}
	afterFirst := in.budget
	second, ok := in.streamBody(ref, stream)
	if !ok {
		t.Fatal("second decode reported failure")
	}
	if &first[0] != &second[0] {
		t.Fatal("the repeat re-decoded the stream instead of hitting the per-Run body cache")
	}
	cost := bodyCost(len(body))
	if got := before - afterFirst; got != cost {
		t.Fatalf("first call charged %d, want %d", got, cost)
	}
	if got := afterFirst - in.budget; got != cost {
		t.Fatalf("cache-hit call charged %d, want %d: the body is re-scanned on every invocation", got, cost)
	}
}

// TestShadingParsedOnceAcrossResourceFrames verifies the reference-keyed per-Run cache survives the fresh resource frame
// every form invocation pushes, so a shading named from N frames is parsed — and charged — once rather than N times.
func TestShadingParsedOnceAcrossResourceFrames(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF(
		`<< /ShadingType 2 /ColorSpace /DeviceGray /Coords [0 0 1 0]
  /Function << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >> >>`,
	)))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{cos.Name("Shading"): cos.Dict{cos.Name("Sh0"): cos.Ref{Num: 1}}}
	in := newInterp(d, res, gfx.Identity(), device.Device(nil), nil)
	before := in.budget
	first := in.shadingFor("Sh0")
	if first == nil {
		t.Fatal("the shading did not parse")
	}
	afterFirst := in.budget
	if got := before - afterFirst; got != shadingParseCost {
		t.Fatalf("the parse charged %d, want %d", got, shadingParseCost)
	}
	// Push a resource frame, as a form invocation does: the name-keyed cache is dropped, the reference-keyed one is not.
	in.frames = append(in.frames, resFrame{spaces: map[cos.Name]pdfcolor.Space{}})
	if second := in.shadingFor("Sh0"); second != first {
		t.Fatal("the shading was re-parsed in the new resource frame")
	}
	if in.budget != afterFirst {
		t.Fatalf("the second lookup charged %d, want 0", afterFirst-in.budget)
	}
}

// TestChargeSaturates verifies charge stops at the exhausted state exec tests for rather than wrapping the counter, no
// matter how large or how many the charges are.
func TestChargeSaturates(t *testing.T) {
	in := &interp{budget: 10}
	in.charge(4)
	if in.budget != 6 {
		t.Fatalf("budget = %d, want 6", in.budget)
	}
	in.charge(0)
	if in.budget != 6 {
		t.Fatalf("a zero charge moved the budget to %d", in.budget)
	}
	in.charge(maxTotalOps)
	if in.budget != -1 {
		t.Fatalf("budget = %d after an over-charge, want -1", in.budget)
	}
	in.charge(maxTotalOps)
	if in.budget != -1 {
		t.Fatalf("budget = %d after charging an exhausted budget, want -1", in.budget)
	}
}
