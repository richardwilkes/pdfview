// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package shading

import (
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// Repeated dictionary keys, typed for map literals (and to satisfy goconst).
const (
	keyShadingType cos.Name = "ShadingType"
	keyColorSpace  cos.Name = "ColorSpace"
	keyDomain      cos.Name = "Domain"
	keyFunction    cos.Name = "Function"
	keyBitsPerFlag cos.Name = "BitsPerFlag"
)

// testDoc opens a minimal document; the shading objects under test are built directly as cos values, so the
// document only supplies Resolve/StreamData plumbing.
func testDoc(t *testing.T) *cos.Document {
	t.Helper()
	pdf := "%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\ntrailer\n<< /Size 3 /Root 1 0 R >>\nstartxref\n0\n%%EOF\n"
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func expFn(c0, c1 cos.Array) cos.Dict {
	return cos.Dict{
		"FunctionType": cos.Integer(2),
		keyDomain:      cos.Array{cos.Real(0), cos.Real(1)},
		"C0":           c0,
		"C1":           c1,
		"N":            cos.Integer(1),
	}
}

func TestParseAxial(t *testing.T) {
	d := testDoc(t)
	sh, err := Parse(d, cos.Dict{
		keyShadingType: cos.Integer(2),
		keyColorSpace:  cos.Name("DeviceRGB"),
		"Coords":       cos.Array{cos.Real(0), cos.Real(0), cos.Real(100), cos.Real(0)},
		keyFunction:    expFn(cos.Array{cos.Real(1), cos.Real(0), cos.Real(0)}, cos.Array{cos.Real(0), cos.Real(0), cos.Real(1)}),
		"Extend":       cos.Array{cos.Boolean(true), cos.Boolean(false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sh.Kind != KindAxial || len(sh.Stops) != maxStops {
		t.Fatalf("kind %d stops %d", sh.Kind, len(sh.Stops))
	}
	if !sh.Extend[0] || sh.Extend[1] {
		t.Fatalf("extend %v", sh.Extend)
	}
	first, last := sh.Stops[0], sh.Stops[len(sh.Stops)-1]
	if first.Offset != 0 || last.Offset != 1 {
		t.Fatalf("offsets %v %v", first.Offset, last.Offset)
	}
	// DeviceRGB conversion is trunc(v*255): pure red and pure blue at the ends.
	if first.Color.R != 255 || first.Color.B != 0 || last.Color.R != 0 || last.Color.B != 255 {
		t.Fatalf("end colors %v %v", first.Color, last.Color)
	}
}

func TestParseRejects(t *testing.T) {
	d := testDoc(t)
	cases := []cos.Dict{
		{keyShadingType: cos.Integer(9), keyColorSpace: cos.Name("DeviceRGB")},
		{keyShadingType: cos.Integer(2), keyColorSpace: cos.Name("Pattern")},
		{keyShadingType: cos.Integer(2), keyColorSpace: cos.Name("DeviceRGB")}, // no Coords/Function
		{
			keyShadingType: cos.Integer(3), keyColorSpace: cos.Name("DeviceRGB"), // negative radius
			"Coords":    cos.Array{cos.Real(0), cos.Real(0), cos.Real(-1), cos.Real(0), cos.Real(0), cos.Real(5)},
			keyFunction: expFn(cos.Array{cos.Real(0)}, cos.Array{cos.Real(1)}),
		},
		{keyShadingType: cos.Integer(4), keyColorSpace: cos.Name("DeviceRGB")}, // mesh without stream
	}
	for i, dict := range cases {
		if _, err := Parse(d, dict); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestParseFunctionBased(t *testing.T) {
	d := testDoc(t)
	calc := []byte("{ pop }") // 2 in -> 1 out (x stays)
	sh, err := Parse(d, cos.Dict{
		keyShadingType: cos.Integer(1),
		keyColorSpace:  cos.Name("DeviceGray"),
		keyDomain:      cos.Array{cos.Real(0), cos.Real(2), cos.Real(0), cos.Real(4)},
		"Matrix":       cos.Array{cos.Real(2), cos.Real(0), cos.Real(0), cos.Real(2), cos.Real(10), cos.Real(20)},
		keyFunction: &cos.Stream{Dict: cos.Dict{
			"FunctionType": cos.Integer(4),
			keyDomain:      cos.Array{cos.Real(0), cos.Real(1), cos.Real(0), cos.Real(1)},
			"Range":        cos.Array{cos.Real(0), cos.Real(1)},
			"Length":       cos.Integer(int64(len(calc))),
		}, Raw: calc},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sh.Kind != KindFunction || sh.ColorAt == nil {
		t.Fatal("not a usable function shading")
	}
	if sh.Domain != [4]float32{0, 2, 0, 4} {
		t.Fatalf("domain %v", sh.Domain)
	}
	if sh.Matrix.A != 2 || sh.Matrix.E != 10 {
		t.Fatalf("matrix %+v", sh.Matrix)
	}
	black := sh.ColorAt(0, 0)
	white := sh.ColorAt(1, 0) // gray 1.0
	if black.R >= white.R {
		t.Fatalf("expected ramp: %v %v", black, white)
	}
}

// meshStream builds a mesh shading stream with 16-bit coordinates over [0 400] and 8-bit RGB.
func meshStream(kind int, extra cos.Dict, data []byte) *cos.Stream {
	dict := cos.Dict{
		keyShadingType:      cos.Integer(int64(kind)),
		keyColorSpace:       cos.Name("DeviceRGB"),
		"BitsPerCoordinate": cos.Integer(16),
		"BitsPerComponent":  cos.Integer(8),
		"Decode": cos.Array{
			cos.Real(0), cos.Real(400), cos.Real(0), cos.Real(400),
			cos.Real(0), cos.Real(1), cos.Real(0), cos.Real(1), cos.Real(0), cos.Real(1),
		},
		"Length": cos.Integer(int64(len(data))),
	}
	for k, v := range extra {
		dict[k] = v
	}
	return &cos.Stream{Dict: dict, Raw: data}
}

func coord16(v float64) []byte {
	u := int(v / 400 * 65535)
	return []byte{byte(u >> 8), byte(u)}
}

func v4(flag byte, x, y float64, r, g, b byte) []byte {
	out := []byte{flag}
	out = append(out, coord16(x)...)
	out = append(out, coord16(y)...)
	return append(out, r, g, b)
}

func TestParseMeshType4(t *testing.T) {
	d := testDoc(t)
	// One uniform triangle: no subdivision needed, exactly one flat triangle.
	uni := make([]byte, 0, 24)
	uni = append(uni, v4(0, 0, 0, 10, 20, 30)...)
	uni = append(uni, v4(0, 100, 0, 10, 20, 30)...)
	uni = append(uni, v4(0, 0, 100, 10, 20, 30)...)
	sh, err := Parse(d, meshStream(4, cos.Dict{keyBitsPerFlag: cos.Integer(8)}, uni))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) != 1 {
		t.Fatalf("uniform triangle should stay flat, got %d", len(sh.Triangles))
	}
	tri := sh.Triangles[0]
	if tri.Color.R != 10 || tri.Color.G != 20 || tri.Color.B != 30 {
		t.Fatalf("color %v", tri.Color)
	}
	// Contrasting corners force subdivision; flags 1 and 2 chain more triangles on.
	data := make([]byte, 0, 40)
	data = append(data, v4(0, 0, 0, 255, 0, 0)...)
	data = append(data, v4(0, 100, 0, 0, 255, 0)...)
	data = append(data, v4(0, 0, 100, 0, 0, 255)...)
	data = append(data, v4(1, 100, 100, 255, 255, 0)...)
	data = append(data, v4(2, 200, 100, 0, 255, 255)...)
	sh, err = Parse(d, meshStream(4, cos.Dict{keyBitsPerFlag: cos.Integer(8)}, data))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) < 100 {
		t.Fatalf("contrasting mesh should subdivide, got %d triangles", len(sh.Triangles))
	}
	if len(sh.Triangles) > maxTriangles {
		t.Fatalf("budget exceeded: %d", len(sh.Triangles))
	}
}

func TestParseMeshType5(t *testing.T) {
	d := testDoc(t)
	data := make([]byte, 0, 28)
	for _, xy := range [][2]float64{{0, 0}, {100, 0}, {0, 100}, {100, 100}} {
		data = append(data, coord16(xy[0])...)
		data = append(data, coord16(xy[1])...)
		data = append(data, 50, 60, 70)
	}
	sh, err := Parse(d, meshStream(5, cos.Dict{"VerticesPerRow": cos.Integer(2)}, data))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) != 2 {
		t.Fatalf("2x2 uniform lattice should yield 2 flat triangles, got %d", len(sh.Triangles))
	}
	// A truncated final row keeps the complete rows' triangles.
	sh, err = Parse(d, meshStream(5, cos.Dict{"VerticesPerRow": cos.Integer(2)}, append(data, 0x01, 0x02)))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) != 2 {
		t.Fatalf("truncated row should not add triangles, got %d", len(sh.Triangles))
	}
}

func TestParseMeshPatches(t *testing.T) {
	d := testDoc(t)
	// A flat uniform Coons patch (flag 0) plus a flag-1 continuation with the same color: both should
	// tessellate (geometry floor) with every triangle the same color.
	boundary := [][2]float64{
		{0, 0},
		{0, 33},
		{0, 66},
		{0, 100},
		{33, 100},
		{66, 100},
		{100, 100},
		{100, 66},
		{100, 33},
		{100, 0},
		{66, 0},
		{33, 0},
	}
	data := make([]byte, 0, 128)
	data = append(data, 0)
	for _, p := range boundary {
		data = append(data, coord16(p[0])...)
		data = append(data, coord16(p[1])...)
	}
	for range 4 {
		data = append(data, 80, 90, 100)
	}
	cont := [][2]float64{
		{0, 133},
		{0, 166},
		{0, 200},
		{33, 200},
		{66, 200},
		{100, 200},
		{100, 166},
		{100, 133},
	}
	data = append(data, 1)
	for _, p := range cont {
		data = append(data, coord16(p[0])...)
		data = append(data, coord16(p[1])...)
	}
	for range 2 {
		data = append(data, 80, 90, 100)
	}
	sh, err := Parse(d, meshStream(6, cos.Dict{keyBitsPerFlag: cos.Integer(8)}, data))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) == 0 {
		t.Fatal("no triangles from patches")
	}
	var minX, maxY float32
	for _, tri := range sh.Triangles {
		if tri.Color.R != 80 || tri.Color.G != 90 || tri.Color.B != 100 {
			t.Fatalf("color %v", tri.Color)
		}
		for _, pt := range tri.P {
			minX = min(minX, pt.X)
			maxY = max(maxY, pt.Y)
		}
	}
	// The continuation extends the surface to y=200 (edge sharing worked); nothing strays far negative.
	if maxY < 190 {
		t.Fatalf("continuation missing: maxY %v", maxY)
	}
	if minX < -5 {
		t.Fatalf("geometry escaped: minX %v", minX)
	}
}

func TestBitReader(t *testing.T) {
	r := &bitReader{data: []byte{0xAB, 0xCD, 0xEF}}
	if v, ok := r.read(12); !ok || v != 0xABC {
		t.Fatalf("12-bit read: %x", v)
	}
	r.align()
	if v, ok := r.read(8); !ok || v != 0xEF {
		t.Fatalf("post-align read: %x", v)
	}
	if _, ok := r.read(8); ok {
		t.Fatal("read past end should fail")
	}
	if _, ok := r.read(0); ok {
		t.Fatal("zero-bit read should fail")
	}
}
