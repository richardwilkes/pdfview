// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package color

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// docWith parses a minimal PDF whose numbered objects are the given bodies (object 1 first); the repair scan
// handles the deliberately missing xref.
func docWith(t *testing.T, bodies ...string) *cos.Document {
	t.Helper()
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, body := range bodies {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Catalog >>\nendobj\n", len(bodies)+1)
	fmt.Fprintf(&b, "trailer\n<< /Root %d 0 R /Size %d >>\nstartxref\n0\n%%%%EOF\n", len(bodies)+1, len(bodies)+2)
	d, err := cos.Open([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The conversion anchors below are oracle observations (probe patches rendered through MuPDF; see convert.go).

func TestDeviceRGBTruncation(t *testing.T) {
	// float32(0.9)*255 = 229.4999... — truncation says 229; rounding would say 230. The oracle says 229.
	got := DeviceRGB.ToNRGBA([]float32{0.2, 0.4, 0.9})
	if got != (color.NRGBA{R: 51, G: 102, B: 229, A: 255}) {
		t.Errorf("rgb(0.2, 0.4, 0.9) = %v", got)
	}
	if got = DeviceRGB.ToNRGBA([]float32{1, 0, 0}); got != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("red = %v", got)
	}
	if got = DeviceRGB.ToNRGBA([]float32{-1, 2, 0}); got != (color.NRGBA{R: 0, G: 255, B: 0, A: 255}) {
		t.Errorf("clamped = %v", got)
	}
}

func TestDeviceGrayCurve(t *testing.T) {
	// Oracle anchors: 0.5 -> 127 neutral; 42/255 -> (42, 42, 41), one of the non-neutral points.
	if got := DeviceGray.ToNRGBA([]float32{0.5}); got != (color.NRGBA{R: 127, G: 127, B: 127, A: 255}) {
		t.Errorf("gray 0.5 = %v (oracle says 127)", got)
	}
	if got := DeviceGray.ToNRGBA([]float32{42.0 / 255}); got != (color.NRGBA{R: 42, G: 42, B: 41, A: 255}) {
		t.Errorf("gray 42/255 = %v (oracle says 42,42,41)", got)
	}
	if got := DeviceGray.ToNRGBA([]float32{0}); got != (color.NRGBA{A: 255}) {
		t.Errorf("black = %v", got)
	}
	if got := DeviceGray.ToNRGBA([]float32{1}); got != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("white = %v", got)
	}
}

func TestDeviceCMYKTable(t *testing.T) {
	// Oracle anchors from the probe grid and the vectors golden.
	if got := DeviceCMYK.ToNRGBA([]float32{0, 0, 0.8, 0}); got != (color.NRGBA{R: 255, G: 243, B: 79, A: 255}) {
		t.Errorf("cmyk(0,0,0.8,0) = %v (vectors golden says 255,243,79)", got)
	}
	if got := DeviceCMYK.ToNRGBA([]float32{0, 0, 0, 1}); got != (color.NRGBA{R: 34, G: 31, B: 31, A: 255}) {
		t.Errorf("cmyk k=1 = %v (oracle says 34,31,31)", got)
	}
	if got := DeviceCMYK.ToNRGBA([]float32{0, 0, 0, 0}); got != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("cmyk zero = %v", got)
	}
	if got := DeviceCMYK.ToNRGBA([]float32{1, 0, 0, 0}); got != (color.NRGBA{G: 173, B: 239, A: 255}) {
		t.Errorf("cmyk c=1 = %v (oracle says 0,173,239)", got)
	}
}

func TestInitialColors(t *testing.T) {
	if DeviceCMYK.ToNRGBA(DeviceCMYK.Initial()) != DeviceCMYK.ToNRGBA([]float32{0, 0, 0, 1}) {
		t.Error("CMYK initial color is not black")
	}
	if DeviceGray.ToNRGBA(DeviceGray.Initial()) != (color.NRGBA{A: 255}) {
		t.Error("gray initial color is not black")
	}
}

func TestParseNamesAndICC(t *testing.T) {
	d := docWith(t, "<< /N 3 >>")
	for _, tc := range []struct {
		body string
		n    int
	}{
		{"/DeviceGray", 1}, {"/DeviceRGB", 3}, {"/DeviceCMYK", 4}, {"/CalRGB", 3}, {"/CalGray", 1},
	} {
		space, err := Parse(d, cos.Name(tc.body[1:]))
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if space.NComponents() != tc.n {
			t.Errorf("%s components = %d", tc.body, space.NComponents())
		}
	}
	space, err := Parse(d, cos.Array{cos.Name("ICCBased"), cos.Ref{Num: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if space != DeviceRGB {
		t.Errorf("ICC N=3 = %#v", space)
	}
	if _, err = Parse(d, cos.Name("Lab")); err == nil {
		t.Error("bare /Lab parsed (unsupported for now)")
	}
}

func TestParseIndexed(t *testing.T) {
	d := docWith(t, "[ /Indexed /DeviceRGB 2 <FF0000 00FF00 0000FF> ]")
	space, err := Parse(d, cos.Ref{Num: 1})
	if err != nil {
		t.Fatal(err)
	}
	if space.NComponents() != 1 {
		t.Fatalf("components = %d", space.NComponents())
	}
	if got := space.ToNRGBA([]float32{1}); got != (color.NRGBA{G: 255, A: 255}) {
		t.Errorf("index 1 = %v", got)
	}
	if got := space.ToNRGBA([]float32{99}); got != (color.NRGBA{B: 255, A: 255}) {
		t.Errorf("out-of-range index = %v (must clamp to hival)", got)
	}
	if got := space.ToNRGBA([]float32{-3}); got != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("negative index = %v (must clamp to 0)", got)
	}
}

func TestParseSeparation(t *testing.T) {
	d := docWith(t, "[ /Separation /Spot /DeviceCMYK 2 0 R ]",
		"<< /FunctionType 2 /Domain [0 1] /C0 [0 0 0 0] /C1 [0 0 0 1] /N 1 >>")
	space, err := Parse(d, cos.Ref{Num: 1})
	if err != nil {
		t.Fatal(err)
	}
	if space.NComponents() != 1 {
		t.Fatalf("components = %d", space.NComponents())
	}
	if got, want := space.ToNRGBA([]float32{1}), DeviceCMYK.ToNRGBA([]float32{0, 0, 0, 1}); got != want {
		t.Errorf("full tint = %v, want %v", got, want)
	}
	if got, want := space.ToNRGBA([]float32{0}), DeviceCMYK.ToNRGBA([]float32{0, 0, 0, 0}); got != want {
		t.Errorf("zero tint = %v, want %v", got, want)
	}
	if got, want := space.ToNRGBA(space.Initial()), space.ToNRGBA([]float32{1}); got != want {
		t.Errorf("initial = %v, want full tint %v", got, want)
	}
}

func TestParseSeparationNone(t *testing.T) {
	d := docWith(t, "[ /Separation /None /DeviceGray 2 0 R ]", "<< /Broken true >>")
	space, err := Parse(d, cos.Ref{Num: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := space.ToNRGBA([]float32{1}); got.A != 0 {
		t.Errorf("Separation /None marks: %v", got)
	}
}

func TestParseDeviceN(t *testing.T) {
	d := docWith(t, "[ /DeviceN [/A /B] /DeviceRGB 2 0 R ]",
		"<< /FunctionType 4 /Domain [0 1 0 1] /Range [0 1 0 1 0 1] /Length 22 >>\nstream\n{ exch dup 3 1 roll }\nendstream")
	space, err := Parse(d, cos.Ref{Num: 1})
	if err != nil {
		t.Fatal(err)
	}
	if space.NComponents() != 2 {
		t.Fatalf("components = %d", space.NComponents())
	}
	// { exch dup 3 1 roll } maps (a, b) to (a, b, a): spot-check the calculator wiring.
	got := space.ToNRGBA([]float32{1, 0})
	if got != (color.NRGBA{R: 255, G: 0, B: 255, A: 255}) {
		t.Errorf("deviceN(1,0) = %v", got)
	}
}

func TestParsePattern(t *testing.T) {
	d := docWith(t, "<< >>")
	space, err := Parse(d, cos.Name("Pattern"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := space.(*Pattern); !ok {
		t.Fatalf("got %#v", space)
	}
	space, err = Parse(d, cos.Array{cos.Name("Pattern"), cos.Name("DeviceRGB")})
	if err != nil {
		t.Fatal(err)
	}
	pattern, ok := space.(*Pattern)
	if !ok || pattern.Base != DeviceRGB {
		t.Fatalf("uncolored pattern: %#v", space)
	}
	if space.ToNRGBA([]float32{1, 0, 0}).A != 0 {
		t.Error("pattern space marks")
	}
}

func TestParseRejects(t *testing.T) {
	d := docWith(t, "<< >>")
	for _, obj := range []cos.Object{
		cos.Integer(4),
		cos.Array{},
		cos.Array{cos.Name("Indexed"), cos.Name("Pattern"), cos.Integer(1), cos.String("ab")},
		cos.Array{cos.Name("Separation"), cos.Name("Spot"), cos.Name("DeviceGray")}, // missing tint
		cos.Array{cos.Name("NotASpace")},
	} {
		if _, err := Parse(d, obj); err == nil {
			t.Errorf("%#v parsed", obj)
		}
	}
}
