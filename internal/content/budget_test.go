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
	"strings"
	"testing"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
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

// imageCounter is a Device that counts the images it is handed without retaining them: the image-decode budget test
// decodes dozens of multi-megapixel images, which the recorder would hold alive all at once.
type imageCounter struct {
	device.Null
	images int
}

func (c *imageCounter) FillImage(*imaging.Image, gfx.Matrix, float64)          { c.images++ }
func (c *imageCounter) FillImageMask(*imaging.Image, gfx.Matrix, device.Paint) { c.images++ }

// TestInlineImageDecodeChargedPerSample verifies a stream of inline images drains the work budget in proportion to the
// samples the decodes produce. The dictionary alone dictates that count: maxPixelsFor's 2^22-pixel floor is independent
// of how small the payload is, so before the charge a BI cost one budget unit out of maxTotalOps while triggering four
// million samples of work — a kilobyte of content stream bought minutes of decoding with the budget still untouched.
func TestInlineImageDecodeChargedPerSample(t *testing.T) {
	const images = 400
	// One payload byte claiming 2048x2048. /IM keeps the decoded plane one byte per sample rather than four, which
	// bounds the test's transient allocation; the charge counts samples either way.
	const one = "BI /IM true /W 2048 /H 2048 /L 1 ID \x00 EI "
	d, err := cos.Open([]byte(minimalPDF("<< >>")))
	if err != nil {
		t.Fatal(err)
	}
	var dev imageCounter
	Run(d, nil, []byte(strings.Repeat(one, images)), gfx.Identity(), &dev, nil)
	// One more than the budget divides by: the decode that exhausts the budget still completes before exec stops.
	limit := 1 + maxTotalOps/imageDecodeCost(&imaging.Image{Width: 2048, Height: 2048}, 1)
	switch {
	case dev.images == 0:
		t.Fatal("no inline image decoded at all: the decode charge is too aggressive")
	case dev.images >= images:
		t.Fatalf("all %d inline images decoded: a 4-megapixel decode is not charged to the work budget", images)
	case dev.images > limit:
		t.Fatalf("%d inline images decoded, want at most %d (one decode charge each)", dev.images, limit)
	}
}

// TestImageXObjectDecodeChargedOncePerDecode verifies an image XObject's decode charges the budget for the samples it
// produces — so a page naming many distinct images pays for each — while the cache hit that follows charges nothing,
// which is what keeps one image drawn repeatedly cheap.
func TestImageXObjectDecodeChargedOncePerDecode(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF(streamObj(
		"/Type /XObject /Subtype /Image /Width 64 /Height 64 /BitsPerComponent 8 /ColorSpace /DeviceGray",
		"\x00\x01\x02\x03",
	))))
	if err != nil {
		t.Fatal(err)
	}
	ref := cos.Ref{Num: 1}
	stream, ok := cos.AsStream(d.Resolve(ref))
	if !ok {
		t.Fatal("object 1 is not a stream")
	}
	in := newInterp(d, nil, gfx.Identity(), device.Null{}, nil)
	before := in.budget
	img := in.cachedImage(ref, stream, nil)
	if img == nil {
		t.Fatal("the image did not decode")
	}
	afterFirst := in.budget
	want := imageDecodeCost(img, len(stream.Raw))
	if got := before - afterFirst; got != want {
		t.Fatalf("the decode charged %d, want %d", got, want)
	}
	if want <= bodyCost(len(stream.Raw)) {
		t.Fatalf("the charge of %d ignores the %d samples the decode produced", want, img.Width*img.Height)
	}
	if again := in.cachedImage(ref, stream, nil); again != img {
		t.Fatal("the repeat re-decoded the image instead of hitting the per-Run cache")
	}
	if in.budget != afterFirst {
		t.Fatalf("the cache hit charged %d, want 0", afterFirst-in.budget)
	}
}

// TestFontLoadChargedPerDistinctFont verifies Tf charges the work budget for the font it loads. The per-reference cache
// makes a repeated Tf on the same reference free, but the cost is per DISTINCT reference: a resource dictionary may
// name up to maxContainerElements font entries, and an object stream supplies a million 30-byte font dictionaries that
// all point at one descriptor. Before the charge, Tf was the one operator that could force an arbitrarily expensive
// resource parse for one budget unit — a font load decodes the whole embedded program (up to internal/filter's
// allowance) and then parses it, the most expensive parse in the engine.
func TestFontLoadChargedPerDistinctFont(t *testing.T) {
	const fonts = 3000
	bodies := make([]string, fonts)
	entries := cos.Dict{}
	for i := range fonts {
		bodies[i] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
		entries[cos.Name(fmt.Sprintf("F%d", i))] = cos.Ref{Num: i + 1}
	}
	d, err := cos.Open([]byte(minimalPDF(bodies...)))
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	for i := range fonts {
		fmt.Fprintf(&content, "BT /F%d 12 Tf (A) Tj ET ", i)
	}
	rec := run(t, d, cos.Dict{cos.Name("Font"): entries}, content.String())
	shown := 0
	for _, tc := range rec.texts {
		if tc.op == opFillText {
			shown++
		}
	}
	// Every load here is a cheap non-embedded substitute, so the flat charge alone is what bounds the count.
	limit := 1 + maxTotalOps/fontParseCost
	switch {
	case shown == 0:
		t.Fatal("no text was shown at all: the font-load charge is too aggressive")
	case shown >= fonts:
		t.Fatalf("all %d distinct fonts loaded: a font load is not charged to the work budget", fonts)
	case shown > limit:
		t.Fatalf("%d distinct fonts loaded, want at most %d (one load charge each)", shown, limit)
	}
}

// TestFontLoadChargeCountsTheProgramAndCachesFree verifies the shape of the charge: a load that parses charges the flat
// parse cost plus the program it decoded, and the cache hit that follows charges nothing — which is what keeps one font
// used across a whole page cheap while a page naming many distinct fonts pays for each.
func TestFontLoadChargeCountsTheProgramAndCachesFree(t *testing.T) {
	d, err := cos.Open([]byte(minimalPDF("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{cos.Name("Font"): cos.Dict{cos.Name("F0"): cos.Ref{Num: 1}}}
	in := newInterp(d, res, gfx.Identity(), device.Null{}, nil)
	before := in.budget
	f, ok := in.loadFont("F0")
	if !ok || f == nil {
		t.Fatal("the font did not load")
	}
	afterFirst := in.budget
	want := fontParseCost + int(f.MemoryEstimate()>>fontProgramCostShift)
	if got := before - afterFirst; got != want {
		t.Fatalf("the load charged %d, want %d", got, want)
	}
	if want <= fontParseCost {
		t.Fatalf("the charge of %d ignores the %d-byte program the load decoded", want, f.MemoryEstimate())
	}
	if again, okAgain := in.loadFont("F0"); !okAgain || again != f {
		t.Fatal("the repeat re-loaded the font instead of hitting the per-Run cache")
	}
	if in.budget != afterFirst {
		t.Fatalf("the cache hit charged %d, want 0", afterFirst-in.budget)
	}
}

// TestShadingPaintCost pins what each shading kind charges for its DEVICE-side realization, which happens per painting
// operation rather than per parse: a type 1 shading's grid is evaluated cell by cell, and a mesh's triangles are
// re-rasterized every time. Axial and radial shadings realize as a gradient built from the ramp parseShading already
// sampled, so they cost only their operator unit.
func TestShadingPaintCost(t *testing.T) {
	if got := shadingPaintCost(nil, gfx.Identity()); got != 0 {
		t.Errorf("a nil shading charged %d, want 0", got)
	}
	for _, kind := range []int{shading.KindAxial, shading.KindRadial} {
		sh := &shading.Shading{Kind: kind, Stops: make([]shading.Stop, 256)}
		if got := shadingPaintCost(sh, gfx.Identity()); got != 1 {
			t.Errorf("kind %d charged %d, want 1", kind, got)
		}
	}
	mesh := &shading.Shading{Kind: shading.KindCoons, Triangles: make([]shading.Triangle, 1<<12)}
	if got, want := shadingPaintCost(mesh, gfx.Identity()), 1+(1<<12)>>shadingSampleCostShift; got != want {
		t.Errorf("a %d-triangle mesh charged %d, want %d", len(mesh.Triangles), got, want)
	}
	// A function-based shading charges for the grid the device evaluates, which the domain's extent under the target
	// matrix sizes: 100 units square here, and 1 cell per unit plus the inclusive edge.
	fn := &shading.Shading{
		Kind:    shading.KindFunction,
		Domain:  [4]float32{0, 100, 0, 100},
		Matrix:  gfx.Identity(),
		ColorAt: func(float32, float32) color.NRGBA { return color.NRGBA{A: 255} },
	}
	w, h, ok := fn.GridSize(gfx.Identity())
	if !ok || w != 101 || h != 101 {
		t.Fatalf("GridSize = %d x %d (ok=%v), want 101 x 101", w, h, ok)
	}
	if got, want := shadingPaintCost(fn, gfx.Identity()), 1+w*h>>shadingSampleCostShift; got != want {
		t.Errorf("a %dx%d grid charged %d, want %d", w, h, got, want)
	}
	// Geometry the device realizes nothing from evaluates nothing, so it charges nothing.
	empty := &shading.Shading{Kind: shading.KindFunction, Domain: [4]float32{0, 0, 0, 0}, Matrix: gfx.Identity()}
	if got := shadingPaintCost(empty, gfx.Identity()); got != 0 {
		t.Errorf("an empty domain charged %d, want 0", got)
	}
}

// TestShadingRealizationChargedPerPaint verifies a flood of sh operators on one shading drains the budget in proportion
// to the device realization each one forces. The parse is cached and charged once, so before this charge a
// flate-compressed run of sh operators — a few tens of kilobytes of file — bought one full grid evaluation and a 1 MB
// image allocation per operator for one budget unit apiece: measured at 10 ms per sh with the cheapest possible
// /Function and 304 ms with a 200-instruction type 4 program, both exactly linear in the repeat count.
func TestShadingRealizationChargedPerPaint(t *testing.T) {
	const paints = 400
	d, err := cos.Open([]byte(minimalPDF(
		`<< /ShadingType 1 /ColorSpace /DeviceGray /Domain [0 1 0 1] /Matrix [500 0 0 500 0 0]
  /Function << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >> >>`,
	)))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{cos.Name("Shading"): cos.Dict{cos.Name("Sh0"): cos.Ref{Num: 1}}}
	rec := run(t, d, res, strings.Repeat("/Sh0 sh ", paints))
	painted := len(rec.byOp(opFillShading))
	sh, err := shading.Parse(d, cos.Ref{Num: 1})
	if err != nil {
		t.Fatal(err)
	}
	limit := 1 + maxTotalOps/shadingPaintCost(sh, gfx.Identity())
	switch {
	case painted == 0:
		t.Fatal("no sh operator painted at all: the realization charge is too aggressive")
	case painted >= paints:
		t.Fatalf("all %d sh operators painted: the device realization is not charged to the work budget", paints)
	case painted > limit:
		t.Fatalf("%d sh operators painted, want at most %d (one realization charge each)", painted, limit)
	}
}

// TestLexicalGarbageChargedToBudget pins the work budget over the one path that used to escape it. A token that fails
// to lex advances the read position, so the scan always terminates — but without a charge its bound was the stream
// length rather than maxTotalOps, and a stream of pure garbage was scanned end to end no matter how little budget
// remained. Each stray ')' here is a one-byte lexical error; past the budget nothing more may run.
func TestLexicalGarbageChargedToBudget(t *testing.T) {
	tail := " 0 0 1 1 re f"
	if rec := run(t, nil, nil, strings.Repeat(")", 16)+tail); len(rec.byOp(opFill)) != 1 {
		t.Fatalf("a little garbage stopped the scan: %d fills, want 1", len(rec.byOp(opFill)))
	}
	if rec := run(t, nil, nil, strings.Repeat(")", maxTotalOps+1)+tail); len(rec.byOp(opFill)) != 0 {
		t.Errorf("the operator past %d bytes of lexical garbage still ran: the scan is not charged to the budget",
			maxTotalOps+1)
	}
}
