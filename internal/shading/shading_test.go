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

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Repeated dictionary keys, typed for map literals (and to satisfy goconst).
const (
	keyShadingType cos.Name = "ShadingType"
	keyColorSpace  cos.Name = "ColorSpace"
	keyDomain      cos.Name = "Domain"
	keyFunction    cos.Name = "Function"
	keyBitsPerFlag cos.Name = "BitsPerFlag"
	keyBitsPerCoor cos.Name = "BitsPerCoordinate"
	keyBitsPerComp cos.Name = "BitsPerComponent"
	keyCoords      cos.Name = "Coords"
	keyFuncType    cos.Name = "FunctionType"
	keyVertsPerRow cos.Name = "VerticesPerRow"
)

// testDoc opens a minimal document; the shading objects under test are built directly as cos values, so the document
// only supplies Resolve/StreamData plumbing.
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
		keyFuncType: cos.Integer(2),
		keyDomain:   cos.Array{cos.Real(0), cos.Real(1)},
		"C0":        c0,
		"C1":        c1,
		"N":         cos.Integer(1),
	}
}

func TestParseAxial(t *testing.T) {
	d := testDoc(t)
	sh, err := Parse(d, cos.Dict{
		keyShadingType: cos.Integer(2),
		keyColorSpace:  cos.Name("DeviceRGB"),
		keyCoords:      cos.Array{cos.Real(0), cos.Real(0), cos.Real(100), cos.Real(0)},
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
			keyCoords:   cos.Array{cos.Real(0), cos.Real(0), cos.Real(-1), cos.Real(0), cos.Real(0), cos.Real(5)},
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
			keyFuncType: cos.Integer(4),
			keyDomain:   cos.Array{cos.Real(0), cos.Real(1), cos.Real(0), cos.Real(1)},
			"Range":     cos.Array{cos.Real(0), cos.Real(1)},
			"Length":    cos.Integer(int64(len(calc))),
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
		keyShadingType: cos.Integer(int64(kind)),
		keyColorSpace:  cos.Name("DeviceRGB"),
		keyBitsPerCoor: cos.Integer(16),
		keyBitsPerComp: cos.Integer(8),
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
	sh, err := Parse(d, meshStream(5, cos.Dict{keyVertsPerRow: cos.Integer(2)}, data))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) != 2 {
		t.Fatalf("2x2 uniform lattice should yield 2 flat triangles, got %d", len(sh.Triangles))
	}
	// A truncated final row keeps the complete rows' triangles.
	sh, err = Parse(d, meshStream(5, cos.Dict{keyVertsPerRow: cos.Integer(2)}, append(data, 0x01, 0x02)))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) != 2 {
		t.Fatalf("truncated row should not add triangles, got %d", len(sh.Triangles))
	}
}

func TestParseMeshPatches(t *testing.T) {
	d := testDoc(t)
	// A flat uniform Coons patch (flag 0) plus a flag-1 continuation with the same color: both should tessellate
	// (geometry floor) with every triangle the same color.
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

// TestOverRangeGeometryRejected pins isFinite's check on the narrowed float32: 1 followed by 39 zeros is a legal PDF
// number and a finite float64 but ±Inf as a float32, and /Coords, /Domain, /Matrix, /BBox, and a mesh's /Decode all
// store float32. internal/render's withShadingBBox relies on rectFrom rejecting such /BBox entries as content.rectFrom
// does for a form's box.
func TestOverRangeGeometryRejected(t *testing.T) {
	d := testDoc(t)
	huge := cos.Real(1e39) // Finite as a float64; +Inf once narrowed to float32.
	gray := expFn(cos.Array{cos.Real(0)}, cos.Array{cos.Real(1)})
	axial := func(extra cos.Dict) cos.Dict {
		dict := cos.Dict{
			keyShadingType: cos.Integer(2),
			keyColorSpace:  cos.Name("DeviceGray"),
			keyCoords:      cos.Array{cos.Real(0), cos.Real(0), cos.Real(100), cos.Real(0)},
			keyFunction:    gray,
		}
		for k, v := range extra {
			dict[k] = v
		}
		return dict
	}

	// An over-range coordinate is fatal: the gradient's geometry is the shading.
	t.Run("Coords", func(t *testing.T) {
		bad := axial(cos.Dict{keyCoords: cos.Array{cos.Real(0), cos.Real(0), huge, cos.Real(0)}})
		if _, err := Parse(d, bad); err == nil {
			t.Fatal("a /Coords entry of 1e39 was accepted; it narrows to +Inf as a float32")
		}
	})
	// The optional entries degrade instead, but none of them may store a non-finite value.
	t.Run("BBox", func(t *testing.T) {
		sh, err := Parse(d, axial(cos.Dict{"BBox": cos.Array{cos.Real(0), cos.Real(0), huge, huge}}))
		if err != nil {
			t.Fatal(err)
		}
		if sh.BBox != nil {
			t.Fatalf("BBox = %+v, want nil: an unusable /BBox is no clip at all", *sh.BBox)
		}
	})
	t.Run("Domain", func(t *testing.T) {
		fnBased := cos.Dict{
			keyShadingType: cos.Integer(1),
			keyColorSpace:  cos.Name("DeviceGray"),
			keyDomain:      cos.Array{cos.Real(0), huge, cos.Real(0), cos.Real(1)},
			keyFunction:    cos.Dict{keyFuncType: cos.Integer(2), keyDomain: cos.Array{cos.Real(0), cos.Real(1)}, "C0": cos.Array{cos.Real(0)}, "C1": cos.Array{cos.Real(1)}, "N": cos.Integer(1)},
		}
		sh, err := Parse(d, fnBased)
		if err != nil {
			t.Fatal(err)
		}
		if sh.Domain != [4]float32{0, 1, 0, 1} {
			t.Fatalf("Domain = %v, want the default [0 1 0 1]", sh.Domain)
		}
	})
	t.Run("Matrix", func(t *testing.T) {
		fnBased := cos.Dict{
			keyShadingType: cos.Integer(1),
			keyColorSpace:  cos.Name("DeviceGray"),
			"Matrix":       cos.Array{huge, cos.Real(0), cos.Real(0), cos.Real(1), cos.Real(0), cos.Real(0)},
			keyFunction:    cos.Dict{keyFuncType: cos.Integer(2), keyDomain: cos.Array{cos.Real(0), cos.Real(1)}, "C0": cos.Array{cos.Real(0)}, "C1": cos.Array{cos.Real(1)}, "N": cos.Integer(1)},
		}
		sh, err := Parse(d, fnBased)
		if err != nil {
			t.Fatal(err)
		}
		if !sh.Matrix.IsFinite() {
			t.Fatalf("Matrix = %+v, want a finite fallback", sh.Matrix)
		}
	})
	// A mesh's /Decode drives every vertex coordinate, so an over-range entry must fail the parse rather than yield a
	// mesh whose triangles are all silently dropped as non-finite.
	t.Run("mesh Decode", func(t *testing.T) {
		data := make([]byte, 0, 24)
		data = append(data, v4(0, 0, 0, 10, 20, 30)...)
		data = append(data, v4(0, 100, 0, 10, 20, 30)...)
		data = append(data, v4(0, 0, 100, 10, 20, 30)...)
		stream := meshStream(4, cos.Dict{keyBitsPerFlag: cos.Integer(8)}, data)
		stream.Dict["Decode"] = cos.Array{
			cos.Real(0), huge, cos.Real(0), cos.Real(400),
			cos.Real(0), cos.Real(1), cos.Real(0), cos.Real(1), cos.Real(0), cos.Real(1),
		}
		if _, err := Parse(d, stream); err == nil {
			t.Fatal("a mesh /Decode entry of 1e39 was accepted")
		}
	})
	// The largest in-range values still parse, so the check has not simply rejected big numbers.
	t.Run("in range", func(t *testing.T) {
		big := cos.Real(3e38)
		sh, err := Parse(d, axial(cos.Dict{keyCoords: cos.Array{cos.Real(0), cos.Real(0), big, cos.Real(0)}}))
		if err != nil {
			t.Fatal(err)
		}
		if sh.Coords[2] != float32(3e38) {
			t.Fatalf("Coords[2] = %v, want 3e38", sh.Coords[2])
		}
	})
}

// TestGridSize pins the evaluation grid a device realizes a function-based shading on; the interpreter's work budget is
// charged for the same grid, so the charge and the realization must agree on how many evaluations one operation
// implies.
func TestGridSize(t *testing.T) {
	unit := func(m gfx.Matrix) *Shading {
		return &Shading{Kind: KindFunction, Domain: [4]float32{0, 100, 0, 50}, Matrix: m}
	}
	for _, tc := range []struct {
		sh   *Shading
		name string
		m    gfx.Matrix
		w, h int
		ok   bool
	}{
		{sh: unit(gfx.Identity()), name: "one cell per unit plus the edge", m: gfx.Identity(), w: 101, h: 51, ok: true},
		{sh: unit(gfx.Identity()), name: "the target matrix scales it", m: gfx.Matrix{A: 2, D: 2}, w: 201, h: 101, ok: true},
		{sh: unit(gfx.Matrix{A: 2, D: 2}), name: "the shading's own matrix too", m: gfx.Identity(), w: 201, h: 101, ok: true},
		{
			sh: unit(gfx.Matrix{A: 1e-6, D: 1e-6}), name: "a sub-pixel extent is one cell", m: gfx.Identity(),
			w: 1, h: 1, ok: true,
		},
		// Over-range extents clamp in float space (see clampGridDim).
		{
			sh: unit(gfx.Matrix{A: 1e30, D: 1e30}), name: "an over-range extent clamps", m: gfx.Identity(),
			w: MaxGridDim, h: MaxGridDim, ok: true,
		},
		{
			sh: &Shading{Kind: KindFunction, Matrix: gfx.Identity()}, name: "an empty domain realizes nothing",
			m: gfx.Identity(),
		},
		{
			sh: unit(gfx.Matrix{A: 3e38, D: 3e38}), name: "a domain that overflows the mapping realizes nothing",
			m: gfx.Matrix{A: 3e38, D: 3e38},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, h, ok := tc.sh.GridSize(tc.m)
			if w != tc.w || h != tc.h || ok != tc.ok {
				t.Fatalf("GridSize = %d x %d (ok=%v), want %d x %d (ok=%v)", w, h, ok, tc.w, tc.h, tc.ok)
			}
			if w*h > MaxGridArea {
				t.Fatalf("the %d x %d grid exceeds the %d-cell area cap", w, h, MaxGridArea)
			}
		})
	}
	// The area cap halves both dimensions until the product fits, so a grid at the dimension cap in one axis and wide in
	// the other still lands inside it.
	wide := &Shading{Kind: KindFunction, Domain: [4]float32{0, 4000, 0, 4000}, Matrix: gfx.Identity()}
	w, h, ok := wide.GridSize(gfx.Matrix{A: 1, D: 1})
	if !ok || w*h > MaxGridArea || w < 1 || h < 1 {
		t.Fatalf("GridSize = %d x %d (ok=%v), want a positive grid inside the %d-cell cap", w, h, ok, MaxGridArea)
	}
}

// TestLatticeRowWidthBound pins /VerticesPerRow at maxMeshVertices/2: parseLattice reads at least two rows before any
// triangle forms, so a wider row would let it read and allocate up to twice the vertex budget.
func TestLatticeRowWidthBound(t *testing.T) {
	d := testDoc(t)
	for _, tc := range []struct {
		name   string
		perRow int64
		want   bool // parses
	}{
		{name: "at the cap", perRow: maxMeshVertices / 2, want: true},
		{name: "one past the cap", perRow: maxMeshVertices/2 + 1},
		{name: "the old cap", perRow: maxMeshVertices},
		{name: "below the two-row minimum", perRow: 1},
	} {
		_, err := Parse(d, meshStream(5, cos.Dict{keyVertsPerRow: cos.Integer(tc.perRow)}, nil))
		if (err == nil) != tc.want {
			t.Errorf("%s (/VerticesPerRow %d): err = %v", tc.name, tc.perRow, err)
		}
	}

	// At the cap the reader stops after the two rows the floor allows, consuming exactly maxMeshVertices vertices even
	// when the payload holds more. Three bits per vertex (1-bit coordinates, one 1-bit color value) keeps the payload
	// small; the bit position after the read pins the consumption.
	const perRow = maxMeshVertices / 2
	m := meshDecode{
		space:  pdfcolor.DeviceGray,
		nComps: 1,
		nColor: 1,
		bpc:    1,
		bpcomp: 1,
		decode: []float32{0, 1, 0, 1, 0, 1},
	}
	const bitsPerVertex = 3
	r := &bitReader{data: make([]byte, 3*perRow*bitsPerVertex/8)} // Three rows' worth of payload.
	parseLattice(r, &m, &meshBuilder{}, perRow)
	if want := 2 * perRow * bitsPerVertex; r.pos != want {
		t.Errorf("lattice consumed %d bits (%d vertices), want %d (%d vertices, the whole budget and no more)",
			r.pos, r.pos/bitsPerVertex, want, 2*perRow)
	}
}

// be16 appends one big-endian 16-bit raw field.
func be16(v int) []byte { return []byte{byte(v >> 8), byte(v)} }

// uniformTriMesh builds a type 4 mesh of n independent single-component triangles with 16-bit coordinates and color
// values. Every vertex carries edge flag 0 and all three vertices of a triangle share one color value, so no triangle
// subdivides and the output count reports exactly how many the parse read. With distinct set, triangle k carries raw
// color k (n distinct tuples for the memo to miss on); otherwise every triangle carries the same one.
func uniformTriMesh(n int, distinct bool, extra cos.Dict) *cos.Stream {
	dict := cos.Dict{
		keyShadingType: cos.Integer(4),
		keyColorSpace:  cos.Name("DeviceGray"),
		keyBitsPerCoor: cos.Integer(16),
		keyBitsPerComp: cos.Integer(16),
		keyBitsPerFlag: cos.Integer(8),
		"Decode": cos.Array{
			cos.Real(0), cos.Real(400), cos.Real(0), cos.Real(400), cos.Real(0), cos.Real(1),
		},
	}
	for k, v := range extra {
		dict[k] = v
	}
	data := make([]byte, 0, n*3*7)
	for k := range n {
		c := 0
		if distinct {
			c = k
		}
		for j := range 3 {
			data = append(data, 0) // Edge flag 0: every triple starts a fresh triangle.
			data = append(data, be16((k*13+j*137)&0xFFFF)...)
			data = append(data, be16((k*29+j*211)&0xFFFF)...)
			data = append(data, be16(c)...)
		}
	}
	dict["Length"] = cos.Integer(int64(len(data)))
	return &cos.Stream{Dict: dict, Raw: data}
}

// TestMeshColorEvalBudget pins maxMeshColorEvals. A mesh declares its own vertex count through its payload, so colors
// that run a /Function or a /Separation or /DeviceN tint transform could force one evaluation per vertex against a
// parse priced at the flat shadingParseCost; pointed at an expensive type 4 function, a few hundred kilobytes bought
// minutes of CPU.
func TestMeshColorEvalBudget(t *testing.T) {
	d := testDoc(t)
	const n = maxMeshColorEvals + 100
	gray := expFn(cos.Array{cos.Real(0)}, cos.Array{cos.Real(1)})
	separation := cos.Array{cos.Name("Separation"), cos.Name("Spot"), cos.Name("DeviceGray"), gray}

	// The budget is shared across the parse and stops it like a truncated stream would: the triangles read before it
	// ran out are kept, the rest are not.
	for _, tc := range []struct {
		extra cos.Dict
		name  string
	}{
		{name: "/Function", extra: cos.Dict{keyFunction: gray}},
		{name: "/Separation tint transform", extra: cos.Dict{keyColorSpace: separation}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh, err := Parse(d, uniformTriMesh(n, true, tc.extra))
			if err != nil {
				t.Fatal(err)
			}
			if len(sh.Triangles) != maxMeshColorEvals {
				t.Fatalf("the parse resolved %d colors, want the %d-evaluation budget to stop it",
					len(sh.Triangles), maxMeshColorEvals)
			}
		})
	}

	// A space that converts without a function is not charged at all, so the same stream parses whole: the budget must
	// not truncate the ordinary device-space mesh it was never aimed at.
	sh, err := Parse(d, uniformTriMesh(n, true, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) != n {
		t.Fatalf("a DeviceGray mesh yielded %d of its %d triangles; only function-driven colors are budgeted",
			len(sh.Triangles), n)
	}

	// Repeated raw tuples are memoized rather than charged, so a function-driven mesh that reuses one color — the shape
	// real content takes — parses whole even though it holds far more vertices than the budget allows evaluations.
	sh, err = Parse(d, uniformTriMesh(n, false, cos.Dict{keyFunction: gray}))
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Triangles) != n {
		t.Fatalf("a single-color mesh yielded %d of its %d triangles; repeated tuples must come from the memo",
			len(sh.Triangles), n)
	}
}

// TestSingleFunctionArray pins the one-element /Function array: non-conforming per Table 78, but common in ordinary
// DeviceRGB gradients and accepted by MuPDF and pdf.js.
func TestSingleFunctionArray(t *testing.T) {
	d := testDoc(t)
	rgb := expFn(cos.Array{cos.Real(1), cos.Real(0), cos.Real(0)}, cos.Array{cos.Real(0), cos.Real(0), cos.Real(1)})
	axial := func(fn cos.Object) cos.Dict {
		return cos.Dict{
			keyShadingType: cos.Integer(2),
			keyColorSpace:  cos.Name("DeviceRGB"),
			keyCoords:      cos.Array{cos.Real(0), cos.Real(0), cos.Real(100), cos.Real(0)},
			keyFunction:    fn,
		}
	}

	// [ f ] with one 3-output function is the same red-to-blue ramp as the bare f.
	sh, err := Parse(d, axial(cos.Array{rgb}))
	if err != nil {
		t.Fatalf("/Function [ f ] with a 3-output f: %v", err)
	}
	first, last := sh.Stops[0], sh.Stops[len(sh.Stops)-1]
	if first.Color.R != 255 || first.Color.B != 0 || last.Color.R != 0 || last.Color.B != 255 {
		t.Fatalf("end colors %v %v, want the same ramp the bare function yields", first.Color, last.Color)
	}

	// A one-element array is still held to the single-function contract: one output cannot fill DeviceRGB.
	gray := expFn(cos.Array{cos.Real(0)}, cos.Array{cos.Real(1)})
	if _, err = Parse(d, axial(cos.Array{gray})); err == nil {
		t.Fatal("/Function [ f ] with a 1-output f fills only one of DeviceRGB's three components; expected an error")
	}

	// The conforming form — one 1-output function per component — still takes the per-component path.
	sh, err = Parse(d, axial(cos.Array{gray, gray, gray}))
	if err != nil {
		t.Fatal(err)
	}
	if sh.Stops[len(sh.Stops)-1].Color.R != 255 {
		t.Fatalf("three 1-output functions should ramp every component to white, got %v", sh.Stops[len(sh.Stops)-1].Color)
	}
}

// TestMeshBitWidthsCheckedAsInt64 pins /BitsPerCoordinate, /BitsPerComponent and /BitsPerFlag to the int64 the file
// declared: a width that is legal only in its low 32 bits must be rejected before it is narrowed.
func TestMeshBitWidthsCheckedAsInt64(t *testing.T) {
	// validBits sees the declared int64: a value whose low 32 bits are legal is not.
	for _, v := range []int64{1<<32 + 16, 1<<32 + 8, 1<<32 + 2} {
		if validBits(v, 1, 2, 4, 8, 12, 16, 24, 32) {
			t.Errorf("validBits(%d) accepted a width that is legal only in its low 32 bits", v)
		}
	}

	d := testDoc(t)
	data := make([]byte, 0, 24)
	data = append(data, v4(0, 0, 0, 10, 20, 30)...)
	data = append(data, v4(0, 100, 0, 10, 20, 30)...)
	data = append(data, v4(0, 0, 100, 10, 20, 30)...)
	for _, tc := range []struct {
		key cos.Name
		bad int64
	}{
		{key: keyBitsPerCoor, bad: 1<<32 + 16},
		{key: keyBitsPerComp, bad: 1<<32 + 8},
		{key: keyBitsPerFlag, bad: 1<<32 + 8},
	} {
		stream := meshStream(4, cos.Dict{keyBitsPerFlag: cos.Integer(8), tc.key: cos.Integer(tc.bad)}, data)
		if _, err := Parse(d, stream); err == nil {
			t.Errorf("/%s %d was accepted; it is a legal width only in its low 32 bits", tc.key, tc.bad)
		}
	}
}

// TestLatticeKeepsEveryTriangle pins maxMeshInputTris against the shape a lattice produces: a rows x perRow lattice
// within the vertex budget forms 2*(rows-1)*(perRow-1) triangles, nearly two per vertex, so a 256x256 lattice must
// record all 130050 of them.
func TestLatticeKeepsEveryTriangle(t *testing.T) {
	const perRow = 256
	const rows = maxMeshVertices / perRow
	m := meshDecode{
		space:  pdfcolor.DeviceGray,
		nComps: 1,
		nColor: 1,
		bpc:    1,
		bpcomp: 1,
		decode: []float32{0, 1, 0, 1, 0, 1},
	}
	const bitsPerVertex = 3
	b := &meshBuilder{}
	parseLattice(&bitReader{data: make([]byte, rows*perRow*bitsPerVertex/8)}, &m, b, perRow)
	if want := 2 * (rows - 1) * (perRow - 1); len(b.input) != want {
		t.Fatalf("the lattice recorded %d input triangles, want all %d of them", len(b.input), want)
	}
	if len(b.input) > maxMeshInputTris {
		t.Fatalf("a lattice inside the vertex budget formed %d triangles, past the %d-triangle input cap",
			len(b.input), maxMeshInputTris)
	}
	// Tessellation still honors the output budget.
	b.finish()
	if len(b.tris) > maxTriangles {
		t.Fatalf("tessellated to %d triangles, past the %d cap", len(b.tris), maxTriangles)
	}
}
