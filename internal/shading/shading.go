// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package shading parses PDF shading dictionaries (ISO 32000-2 8.7.4, types 1-7) into a normalized form the
// raster device can draw without consulting COS objects or PDF functions again: axial and radial shadings
// carry a sampled color ramp (at most maxStops stops, the resolution MuPDF itself uses), function-based
// shadings carry their domain, matrix, and a color-evaluation closure, and the mesh types (4-7) are
// tessellated at parse time into flat triangles whose vertex-color deltas are below one 8-bit quantization
// step (plan.md "Rendering mapping"). All colors are resolved to the rendered RGB space through
// internal/color, so a Shading is pure geometry + RGB.
package shading

import (
	"errors"
	"image/color"
	"math"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/function"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Shading kinds (the /ShadingType values).
const (
	KindFunction = 1 // function-based
	KindAxial    = 2
	KindRadial   = 3
	// Kinds 4-7 (the mesh types) all normalize to Triangles; Kind records the original type.
	KindFreeTriangle    = 4
	KindLatticeTriangle = 5
	KindCoons           = 6
	KindTensor          = 7
)

// Limits (plan.md "Resource limits & robustness"). maxStops is the ramp resolution for axial/radial shadings
// (matching the "Function sampled to <=256 stops" plan rule). maxTriangles caps the tessellation output of one
// mesh shading; subdivision stops refining (emitting flat triangles at the reached level) once the budget is
// hit, so hostile meshes degrade to banding, never to unbounded memory. maxMeshVertices caps how many vertices
// or patches a mesh stream may declare through its payload. maxSubdivDepth caps the recursive triangle split
// (4^8 = 65536 triangles from one input triangle is beyond ΔRGB<1 for any color pair). maxPatchGrid caps the
// per-patch tessellation grid.
const (
	maxStops        = 256
	maxTriangles    = 1 << 19
	maxMeshVertices = 1 << 16
	maxSubdivDepth  = 8
	maxPatchGrid    = 96
)

var (
	errNotShading  = errors.New("not a shading")
	errBadShading  = errors.New("malformed shading dictionary")
	errUnsupported = errors.New("unsupported shading type")
)

// Stop is one color stop of a sampled axial/radial ramp. Offset is in [0, 1] over the gradient's parametric
// span (the /Domain mapping is folded into the sampling, so offset 0 is the t0 end and offset 1 the t1 end).
type Stop struct {
	Offset float32
	Color  color.NRGBA
}

// Triangle is one flat-shaded triangle of a tessellated mesh shading, in shading space.
type Triangle struct {
	P     [3]gfx.Point
	Color color.NRGBA
}

// Shading is one parsed shading dictionary in normalized form. Kind selects which of the payload fields are
// meaningful: Coords+Extend+Stops for KindAxial/KindRadial, Domain+Matrix+ColorAt for KindFunction, and
// Triangles for the mesh kinds. BBox, when non-nil, is a clip in the shading's target coordinate space.
type Shading struct {
	// ColorAt evaluates the function-based shading's color at a point in its (pre-/Matrix) domain space.
	// Only set for KindFunction.
	ColorAt func(x, y float32) color.NRGBA
	// BBox is the optional /BBox clip in the shading's target space, already normalized.
	BBox *gfx.Rect
	// Stops is the sampled color ramp for KindAxial/KindRadial (2..maxStops entries, offsets ascending).
	Stops []Stop
	// Triangles is the flat tessellation of a mesh shading (kinds 4-7), in shading space.
	Triangles []Triangle
	// Matrix is KindFunction's /Matrix (domain space -> shading target space).
	Matrix gfx.Matrix
	// Domain is KindFunction's /Domain as [x0, x1, y0, y1]; points outside it are not painted.
	Domain [4]float32
	// Coords holds /Coords: x0,y0,x1,y1 for KindAxial (in the first four slots); x0,y0,r0,x1,y1,r1 for
	// KindRadial.
	Coords [6]float32
	// Kind is the /ShadingType (1-7).
	Kind int
	// Extend holds /Extend for KindAxial/KindRadial.
	Extend [2]bool
}

// Parse resolves and parses one shading dictionary (or stream, for the mesh types). Malformed dictionaries
// return an error — the caller (the interpreter) skips the paint, matching viewer behavior for broken
// shadings.
func Parse(d *cos.Document, obj cos.Object) (*Shading, error) {
	var dict cos.Dict
	var stream *cos.Stream
	switch v := d.Resolve(obj).(type) {
	case cos.Dict:
		dict = v
	case *cos.Stream:
		stream, dict = v, v.Dict
	default:
		return nil, errNotShading
	}
	kind, ok := d.GetInt(dict, "ShadingType")
	if !ok || kind < 1 || kind > 7 {
		return nil, errUnsupported
	}
	spaceObj, ok := dict["ColorSpace"]
	if !ok {
		return nil, errBadShading
	}
	space, err := pdfcolor.Parse(d, spaceObj)
	if err != nil {
		return nil, errBadShading
	}
	if _, isPattern := space.(*pdfcolor.Pattern); isPattern {
		return nil, errBadShading // A pattern space cannot color a shading (ISO 32000-2 8.7.4.3).
	}
	fns := parseFunctions(d, dict["Function"], space.NComponents())
	sh := &Shading{Kind: int(kind), Matrix: gfx.Identity()}
	if bbox, has := rectFrom(d, dict, "BBox"); has {
		sh.BBox = &bbox
	}
	switch kind {
	case KindFunction:
		err = parseFunctionBased(d, dict, sh, space, fns)
	case KindAxial, KindRadial:
		err = parseGradient(d, dict, sh, space, fns)
	default:
		if stream == nil {
			return nil, errBadShading // Mesh shadings are streams by definition.
		}
		err = parseMesh(d, stream, sh, space, fns)
	}
	if err != nil {
		return nil, err
	}
	return sh, nil
}

// parseFunctions resolves the /Function entry: absent yields nil, a single function or an array of 1-output
// functions yield an evaluator set. nComps is the color-space component count the evaluations must cover; a
// function set that cannot produce that many outputs is rejected (nil).
func parseFunctions(d *cos.Document, obj cos.Object, nComps int) []function.Func {
	if obj == nil {
		return nil
	}
	resolved := d.Resolve(obj)
	if arr, ok := resolved.(cos.Array); ok {
		if len(arr) < nComps || nComps <= 0 {
			return nil
		}
		fns := make([]function.Func, 0, nComps)
		for _, entry := range arr[:nComps] {
			fn, err := function.Parse(d, entry)
			if err != nil || fn.NOutputs() < 1 {
				return nil
			}
			fns = append(fns, fn)
		}
		return fns
	}
	fn, err := function.Parse(d, resolved)
	if err != nil || fn.NOutputs() < nComps {
		return nil
	}
	return []function.Func{fn}
}

// evalComps evaluates the function set on in, yielding nComps components.
func evalComps(fns []function.Func, in []float32, nComps int) []float32 {
	if len(fns) == 1 {
		out := fns[0].Eval(in)
		if len(out) >= nComps {
			return out[:nComps]
		}
		return append(out, make([]float32, nComps-len(out))...)
	}
	out := make([]float32, nComps)
	for i, fn := range fns {
		if i >= nComps {
			break
		}
		v := fn.Eval(in)
		if len(v) > 0 {
			out[i] = v[0]
		}
	}
	return out
}

// parseGradient fills sh for the axial/radial kinds: coordinates, extends, and the sampled ramp.
func parseGradient(d *cos.Document, dict cos.Dict, sh *Shading, space pdfcolor.Space, fns []function.Func) error {
	if fns == nil {
		return errBadShading // /Function is required for types 2 and 3.
	}
	want := 4
	if sh.Kind == KindRadial {
		want = 6
	}
	coords, ok := d.GetArray(dict, "Coords")
	if !ok || len(coords) < want {
		return errBadShading
	}
	for i := range want {
		v, numOK := cos.AsReal(d.Resolve(coords[i]))
		if !numOK || !isFinite(v) {
			return errBadShading
		}
		sh.Coords[i] = float32(v)
	}
	if sh.Kind == KindRadial && (sh.Coords[2] < 0 || sh.Coords[5] < 0) {
		return errBadShading // Negative radii are meaningless.
	}
	t0, t1 := float32(0), float32(1)
	if arr, has := d.GetArray(dict, "Domain"); has && len(arr) >= 2 {
		v0, ok0 := cos.AsReal(d.Resolve(arr[0]))
		v1, ok1 := cos.AsReal(d.Resolve(arr[1]))
		if ok0 && ok1 && isFinite(v0) && isFinite(v1) {
			t0, t1 = float32(v0), float32(v1)
		}
	}
	if arr, has := d.GetArray(dict, "Extend"); has && len(arr) >= 2 {
		sh.Extend[0], _ = cos.AsBool(d.Resolve(arr[0]))
		sh.Extend[1], _ = cos.AsBool(d.Resolve(arr[1]))
	}
	// Sample the function over [t0, t1] into a uniform ramp. maxStops samples bound both the work and the
	// ramp's memory; uniform sampling reproduces MuPDF's own 256-sample ramp behavior.
	nComps := space.NComponents()
	sh.Stops = make([]Stop, maxStops)
	in := make([]float32, 1)
	for i := range maxStops {
		f := float32(i) / (maxStops - 1)
		in[0] = t0 + f*(t1-t0)
		sh.Stops[i] = Stop{Offset: f, Color: space.ToNRGBA(evalComps(fns, in, nComps))}
	}
	return nil
}

// parseFunctionBased fills sh for KindFunction: domain, matrix, and the evaluation closure.
func parseFunctionBased(d *cos.Document, dict cos.Dict, sh *Shading, space pdfcolor.Space, fns []function.Func) error {
	if fns == nil {
		return errBadShading // /Function is required for type 1.
	}
	sh.Domain = [4]float32{0, 1, 0, 1}
	if arr, has := d.GetArray(dict, "Domain"); has && len(arr) >= 4 {
		var vals [4]float32
		good := true
		for i := range vals {
			v, numOK := cos.AsReal(d.Resolve(arr[i]))
			if !numOK || !isFinite(v) {
				good = false
				break
			}
			vals[i] = float32(v)
		}
		if good {
			sh.Domain = vals
		}
	}
	if sh.Domain[0] > sh.Domain[1] {
		sh.Domain[0], sh.Domain[1] = sh.Domain[1], sh.Domain[0]
	}
	if sh.Domain[2] > sh.Domain[3] {
		sh.Domain[2], sh.Domain[3] = sh.Domain[3], sh.Domain[2]
	}
	if arr, has := d.GetArray(dict, "Matrix"); has && len(arr) >= 6 {
		var vals [6]float32
		good := true
		for i := range vals {
			v, numOK := cos.AsReal(d.Resolve(arr[i]))
			if !numOK || !isFinite(v) {
				good = false
				break
			}
			vals[i] = float32(v)
		}
		if good {
			sh.Matrix = gfx.Matrix{A: vals[0], B: vals[1], C: vals[2], D: vals[3], E: vals[4], F: vals[5]}
		}
	}
	nComps := space.NComponents()
	sh.ColorAt = func(x, y float32) color.NRGBA {
		return space.ToNRGBA(evalComps(fns, []float32{x, y}, nComps))
	}
	return nil
}

// rectFrom reads dict[key] as a normalized finite rectangle.
func rectFrom(d *cos.Document, dict cos.Dict, key cos.Name) (gfx.Rect, bool) {
	arr, ok := d.GetArray(dict, key)
	if !ok || len(arr) < 4 {
		return gfx.Rect{}, false
	}
	var vals [4]float32
	for i := range vals {
		v, numOK := cos.AsReal(d.Resolve(arr[i]))
		if !numOK || !isFinite(v) {
			return gfx.Rect{}, false
		}
		vals[i] = float32(v)
	}
	return gfx.Rect{X0: vals[0], Y0: vals[1], X1: vals[2], Y1: vals[3]}.Normalize(), true
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
