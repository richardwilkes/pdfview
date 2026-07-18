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
	"image/color"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/function"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Mesh shadings (ISO 32000-2 8.7.4.5.5-8) are tessellated at parse time into flat triangles: vertex colors are
// converted to the rendered RGB space first (MuPDF likewise converts mesh vertex colors to the destination space and
// interpolates RGB), then triangles are subdivided until the color difference across each is below one 8-bit step, so
// drawing them flat is visually equivalent to Gouraud interpolation. Truncated or malformed stream data degrades to
// however many complete primitives were read — never an error — matching the leniency both MuPDF and the filter layer
// apply.

// vert is one mesh vertex: a shading-space position plus its resolved RGB color (0-255 range, kept in float for exact
// midpoint interpolation).
type vert struct {
	pt gfx.Point
	c  [3]float32
}

// bitReader reads big-endian bit fields from a mesh stream's decoded payload.
type bitReader struct {
	data []byte
	pos  int // bit position
}

func (r *bitReader) read(bits int) (uint32, bool) {
	if bits <= 0 || bits > 32 || r.pos+bits > len(r.data)*8 {
		return 0, false
	}
	var out uint32
	for range bits {
		byteIdx := r.pos >> 3
		bit := (r.data[byteIdx] >> (7 - uint(r.pos&7))) & 1
		out = out<<1 | uint32(bit)
		r.pos++
	}
	return out, true
}

// align advances to the next byte boundary (each type 4 vertex and each type 6/7 patch begins on one).
func (r *bitReader) align() {
	r.pos = (r.pos + 7) &^ 7
}

// meshDecode holds the per-value decode parameters of a mesh stream.
type meshDecode struct {
	space  pdfcolor.Space
	fns    []function.Func
	decode []float32 // pairs: x, y, then one pair per color value
	bpc    int       // BitsPerCoordinate
	bpcomp int       // BitsPerComponent
	bpf    int       // BitsPerFlag
	nColor int       // color values per vertex (1 when fns is set)
	nComps int       // color-space component count
}

// decodeVal maps a raw bit-field value into the decode range for slot i.
func (m *meshDecode) decodeVal(raw uint32, bits, slot int) float32 {
	dmin, dmax := m.decode[2*slot], m.decode[2*slot+1]
	maxRaw := float64(uint64(1)<<uint(bits)) - 1
	return dmin + float32(float64(raw)/maxRaw)*(dmax-dmin)
}

// readVertex reads one x, y, color tuple.
func (m *meshDecode) readVertex(r *bitReader) (vert, bool) {
	var v vert
	rx, ok := r.read(m.bpc)
	if !ok {
		return v, false
	}
	ry, ok := r.read(m.bpc)
	if !ok {
		return v, false
	}
	v.pt = gfx.Point{X: m.decodeVal(rx, m.bpc, 0), Y: m.decodeVal(ry, m.bpc, 1)}
	c, ok := m.readColor(r)
	if !ok {
		return v, false
	}
	v.c = c
	return v, true
}

// readColor reads one color tuple and resolves it to RGB.
func (m *meshDecode) readColor(r *bitReader) ([3]float32, bool) {
	comps := make([]float32, m.nColor)
	for i := range comps {
		raw, ok := r.read(m.bpcomp)
		if !ok {
			return [3]float32{}, false
		}
		comps[i] = m.decodeVal(raw, m.bpcomp, 2+i)
	}
	if m.fns != nil {
		comps = evalComps(m.fns, comps, m.nComps)
	}
	rgba := m.space.ToNRGBA(comps)
	return [3]float32{float32(rgba.R), float32(rgba.G), float32(rgba.B)}, true
}

// parseMesh parses and tessellates one mesh shading stream (kinds 4-7) into sh.Triangles.
func parseMesh(d *cos.Document, stream *cos.Stream, sh *Shading, space pdfcolor.Space, fns []function.Func) error {
	dict := stream.Dict
	m := meshDecode{space: space, fns: fns, nComps: space.NComponents()}
	bpc, ok := d.GetInt(dict, "BitsPerCoordinate")
	if !ok || !validBits(int(bpc), 1, 2, 4, 8, 12, 16, 24, 32) {
		return errBadShading
	}
	m.bpc = int(bpc)
	bpcomp, ok := d.GetInt(dict, "BitsPerComponent")
	if !ok || !validBits(int(bpcomp), 1, 2, 4, 8, 12, 16) {
		return errBadShading
	}
	m.bpcomp = int(bpcomp)
	if sh.Kind != KindLatticeTriangle {
		bpf, hasFlag := d.GetInt(dict, "BitsPerFlag")
		if !hasFlag || !validBits(int(bpf), 2, 4, 8) {
			return errBadShading
		}
		m.bpf = int(bpf)
	}
	m.nColor = m.nComps
	if fns != nil {
		m.nColor = 1
	}
	decode, ok := d.GetArray(dict, "Decode")
	if !ok || len(decode) < 2*(2+m.nColor) {
		return errBadShading
	}
	m.decode = make([]float32, 2*(2+m.nColor))
	for i := range m.decode {
		v, numOK := cos.AsReal(d.Resolve(decode[i]))
		if !numOK || !isFinite(v) {
			return errBadShading
		}
		m.decode[i] = float32(v)
	}
	data, err := d.StreamData(stream)
	if err != nil {
		return errBadShading
	}
	r := &bitReader{data: data}
	b := &meshBuilder{}
	switch sh.Kind {
	case KindFreeTriangle:
		parseFreeTriangles(r, &m, b)
	case KindLatticeTriangle:
		rows, hasRows := d.GetInt(dict, "VerticesPerRow")
		if !hasRows || rows < 2 || rows > maxMeshVertices {
			return errBadShading
		}
		parseLattice(r, &m, b, int(rows))
	default:
		parsePatches(r, &m, b, sh.Kind == KindTensor)
	}
	b.finish()
	sh.Triangles = b.tris
	return nil
}

func validBits(v int, allowed ...int) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// parseFreeTriangles reads a type 4 free-form triangle stream: each vertex carries an edge flag — 0 starts a fresh
// triangle (two more vertices follow), 1 continues sharing the previous triangle's second and third vertices, 2 shares
// its first and third.
func parseFreeTriangles(r *bitReader, m *meshDecode, b *meshBuilder) {
	var va, vb, vc vert
	have := false
	for count := 0; count < maxMeshVertices; count++ {
		flag, ok := r.read(m.bpf)
		if !ok {
			return
		}
		v, ok := m.readVertex(r)
		if !ok {
			return
		}
		r.align() // Each type 4 vertex begins on a byte boundary.
		switch flag {
		case 0:
			va = v
			for i := range 2 {
				if _, ok = r.read(m.bpf); !ok {
					return
				}
				if v, ok = m.readVertex(r); !ok {
					return
				}
				r.align()
				if i == 0 {
					vb = v
				} else {
					vc = v
				}
				count++
			}
			have = true
		case 1:
			if !have {
				return
			}
			va, vb, vc = vb, vc, v
		case 2:
			if !have {
				return
			}
			vb, vc = vc, v
		default:
			return // Reserved flag values: stop at the malformed record.
		}
		b.triangle(va, vb, vc)
	}
}

// parseLattice reads a type 5 lattice stream: rows of perRow vertices, triangulated between adjacent rows.
func parseLattice(r *bitReader, m *meshDecode, b *meshBuilder, perRow int) {
	maxRows := maxMeshVertices / perRow
	var prev, row []vert
	for range max(maxRows, 2) {
		row = make([]vert, 0, perRow)
		for range perRow {
			v, ok := m.readVertex(r)
			if !ok {
				// A partial final row is dropped; everything before it is kept.
				return
			}
			row = append(row, v)
		}
		if prev != nil {
			for i := 0; i+1 < perRow; i++ {
				b.triangle(prev[i], prev[i+1], row[i])
				b.triangle(prev[i+1], row[i+1], row[i])
			}
		}
		prev = row
	}
}

// patch is one Coons/tensor patch: a 4x4 control-point grid (Coons patches get their four interior points computed from
// the boundary) plus the four corner colors c[0]..c[3] at grid corners (0,0), (0,3), (3,3), (3,0).
type patch struct {
	p [4][4]gfx.Point
	c [4][3]float32
}

// Stream point orders (grid row, col), per the spec's tensor figure: a fresh patch's 16 (tensor) points are the
// boundary counterclockwise from (0,0) then the four interior points; Coons patches carry only the 12 boundary points.
// Continuation patches reuse row 0 from the previous patch's shared edge and read the rest.
var (
	tensorOrderNew  = [][2]int{{0, 0}, {0, 1}, {0, 2}, {0, 3}, {1, 3}, {2, 3}, {3, 3}, {3, 2}, {3, 1}, {3, 0}, {2, 0}, {1, 0}, {1, 1}, {1, 2}, {2, 2}, {2, 1}}
	tensorOrderCont = tensorOrderNew[4:]
	coonsOrderNew   = tensorOrderNew[:12]
	coonsOrderCont  = tensorOrderNew[4:12]
)

// parsePatches reads a type 6/7 patch stream and tessellates each patch.
func parsePatches(r *bitReader, m *meshDecode, b *meshBuilder, tensor bool) {
	var prev patch
	have := false
	for count := 0; count < maxMeshVertices; count++ {
		flag, ok := r.read(m.bpf)
		if !ok {
			return
		}
		var cur patch
		var order [][2]int
		nColors := 4
		if flag == 0 {
			if tensor {
				order = tensorOrderNew
			} else {
				order = coonsOrderNew
			}
		} else {
			if flag > 3 || !have {
				return
			}
			nColors = 2
			if tensor {
				order = tensorOrderCont
			} else {
				order = coonsOrderCont
			}
			// Row 0 and its two corner colors come from the shared edge of the previous patch.
			switch flag {
			case 1:
				cur.p[0] = [4]gfx.Point{prev.p[0][3], prev.p[1][3], prev.p[2][3], prev.p[3][3]}
				cur.c[0], cur.c[1] = prev.c[1], prev.c[2]
			case 2:
				cur.p[0] = [4]gfx.Point{prev.p[3][3], prev.p[3][2], prev.p[3][1], prev.p[3][0]}
				cur.c[0], cur.c[1] = prev.c[2], prev.c[3]
			case 3:
				cur.p[0] = [4]gfx.Point{prev.p[3][0], prev.p[2][0], prev.p[1][0], prev.p[0][0]}
				cur.c[0], cur.c[1] = prev.c[3], prev.c[0]
			}
		}
		for _, rc := range order {
			rx, okx := r.read(m.bpc)
			ry, oky := r.read(m.bpc)
			if !okx || !oky {
				return
			}
			cur.p[rc[0]][rc[1]] = gfx.Point{X: m.decodeVal(rx, m.bpc, 0), Y: m.decodeVal(ry, m.bpc, 1)}
		}
		for i := 4 - nColors; i < 4; i++ {
			c, okc := m.readColor(r)
			if !okc {
				return
			}
			cur.c[i] = c
		}
		r.align() // Each patch begins on a byte boundary.
		if !tensor {
			fillCoonsInterior(&cur)
		}
		if len(b.patches) < maxMeshVertices {
			b.patches = append(b.patches, cur)
		}
		prev = cur
		have = true
	}
}

// fillCoonsInterior computes the four interior control points of a Coons patch from its boundary, the standard
// Coons-to-tensor promotion (ISO 32000-2 8.7.4.5.7).
func fillCoonsInterior(pa *patch) {
	p := &pa.p
	p[1][1] = coonsInner(p[0][0], p[0][1], p[1][0], p[0][3], p[3][0], p[3][1], p[1][3], p[3][3])
	p[1][2] = coonsInner(p[0][3], p[0][2], p[1][3], p[0][0], p[3][3], p[3][2], p[1][0], p[3][0])
	p[2][1] = coonsInner(p[3][0], p[3][1], p[2][0], p[3][3], p[0][0], p[0][1], p[2][3], p[0][3])
	p[2][2] = coonsInner(p[3][3], p[3][2], p[2][3], p[3][0], p[0][3], p[0][2], p[2][0], p[0][0])
}

// coonsInner computes one interior point: (-4a + 6(b+c) - 2(d+e) + 3(f+g) - h) / 9, the spec's formula with the
// operands ordered so all four interior points share it under symmetry.
func coonsInner(a, b, c, d, e, f, g, h gfx.Point) gfx.Point {
	return gfx.Point{
		X: (-4*a.X + 6*(b.X+c.X) - 2*(d.X+e.X) + 3*(f.X+g.X) - h.X) / 9,
		Y: (-4*a.Y + 6*(b.Y+c.Y) - 2*(d.Y+e.Y) + 3*(f.Y+g.Y) - h.Y) / 9,
	}
}

// tessellatePatch evaluates the patch's Bézier surface over a grid sized by the corner-color deltas (so each cell's
// color spread stays below one 8-bit step, up to the grid cap) with a geometric floor that keeps curved patch interiors
// smooth, then emits two flat triangles per cell. share is this patch's slice of the triangle budget; the grid coarsens
// rather than dropping the patch.
func tessellatePatch(pa *patch, b *meshBuilder, share int) {
	const geomFloor = 12
	du := max(colorDelta(pa.c[0], pa.c[3]), colorDelta(pa.c[1], pa.c[2]))
	dv := max(colorDelta(pa.c[0], pa.c[1]), colorDelta(pa.c[3], pa.c[2]))
	nu := clampGrid(du, geomFloor)
	nv := clampGrid(dv, geomFloor)
	for nu*nv*2 > min(share, maxTriangles-len(b.tris)) && (nu > 1 || nv > 1) {
		nu = max(nu/2, 1)
		nv = max(nv/2, 1)
	}
	pts := make([]gfx.Point, (nu+1)*(nv+1))
	for i := 0; i <= nu; i++ {
		u := float32(i) / float32(nu)
		for j := 0; j <= nv; j++ {
			v := float32(j) / float32(nv)
			pts[i*(nv+1)+j] = patchPoint(pa, u, v)
		}
	}
	for i := range nu {
		for j := range nv {
			cc := patchColor(pa, (float32(i)+0.5)/float32(nu), (float32(j)+0.5)/float32(nv))
			p00 := pts[i*(nv+1)+j]
			p01 := pts[i*(nv+1)+j+1]
			p10 := pts[(i+1)*(nv+1)+j]
			p11 := pts[(i+1)*(nv+1)+j+1]
			b.emit(Triangle{P: [3]gfx.Point{p00, p01, p11}, Color: cc})
			b.emit(Triangle{P: [3]gfx.Point{p00, p11, p10}, Color: cc})
		}
	}
}

func clampGrid(delta float32, floor int) int {
	n := int(delta) + 1
	if n < floor {
		n = floor
	}
	if n > maxPatchGrid {
		n = maxPatchGrid
	}
	return n
}

// patchPoint evaluates the tensor surface S(u,v) = ΣΣ B_i(u) B_j(v) p[i][j].
func patchPoint(pa *patch, u, v float32) gfx.Point {
	bu := bezierBasis(u)
	bv := bezierBasis(v)
	var x, y float32
	for i := range 4 {
		for j := range 4 {
			w := bu[i] * bv[j]
			x += w * pa.p[i][j].X
			y += w * pa.p[i][j].Y
		}
	}
	return gfx.Point{X: x, Y: y}
}

func bezierBasis(t float32) [4]float32 {
	mt := 1 - t
	return [4]float32{mt * mt * mt, 3 * mt * mt * t, 3 * mt * t * t, t * t * t}
}

// patchColor interpolates the corner colors bilinearly: c0 at (0,0), c1 at (0,1), c2 at (1,1), c3 at (1,0).
func patchColor(pa *patch, u, v float32) color.NRGBA {
	var out [3]float32
	for k := range 3 {
		out[k] = (1-u)*(1-v)*pa.c[0][k] + (1-u)*v*pa.c[1][k] + u*v*pa.c[2][k] + u*(1-v)*pa.c[3][k]
	}
	return quantize(out)
}

// meshBuilder accumulates the input primitives during stream parsing, then tessellates them under the global triangle
// budget in finish(). Collecting first keeps the budget FAIR: every input triangle and patch gets an equal share, so a
// color-contrasting first primitive cannot exhaust the budget and drop everything after it.
type meshBuilder struct {
	input   []gouraudTri
	patches []patch
	tris    []Triangle
}

// gouraudTri is one input triangle with per-vertex colors (types 4 and 5).
type gouraudTri struct {
	v [3]vert
}

// triangle records one input Gouraud triangle for tessellation.
func (b *meshBuilder) triangle(v0, v1, v2 vert) {
	if len(b.input) < maxMeshVertices {
		b.input = append(b.input, gouraudTri{v: [3]vert{v0, v1, v2}})
	}
}

// finish tessellates the collected primitives. Each input's share of the budget bounds its subdivision depth; within
// that depth, splitting stops as soon as the triangle's color spread is below one 8-bit step.
func (b *meshBuilder) finish() {
	n := len(b.input) + len(b.patches)
	if n == 0 {
		return
	}
	share := maxTriangles / n
	depth := 0
	for cap4 := 1; cap4*4 <= share && depth < maxSubdivDepth; depth++ {
		cap4 *= 4
	}
	for i := range b.input {
		t := &b.input[i]
		b.subdivide(t.v[0], t.v[1], t.v[2], depth)
	}
	for i := range b.patches {
		tessellatePatch(&b.patches[i], b, share)
	}
	b.input, b.patches = nil, nil
}

// subdivide splits a Gouraud triangle at edge midpoints until its color spread is below one 8-bit step or the depth
// budget runs out, then emits it flat with the average color.
func (b *meshBuilder) subdivide(v0, v1, v2 vert, depth int) {
	if len(b.tris) >= maxTriangles {
		return
	}
	spread := max(colorDelta(v0.c, v1.c), colorDelta(v1.c, v2.c), colorDelta(v0.c, v2.c))
	if spread > 1 && depth > 0 && len(b.tris)+4 <= maxTriangles {
		m01 := midVert(v0, v1)
		m12 := midVert(v1, v2)
		m20 := midVert(v2, v0)
		b.subdivide(v0, m01, m20, depth-1)
		b.subdivide(m01, v1, m12, depth-1)
		b.subdivide(m20, m12, v2, depth-1)
		b.subdivide(m01, m12, m20, depth-1)
		return
	}
	var avg [3]float32
	for k := range 3 {
		avg[k] = (v0.c[k] + v1.c[k] + v2.c[k]) / 3
	}
	b.emit(Triangle{P: [3]gfx.Point{v0.pt, v1.pt, v2.pt}, Color: quantize(avg)})
}

func (b *meshBuilder) emit(t Triangle) {
	if len(b.tris) >= maxTriangles {
		return
	}
	for _, p := range t.P {
		if !isFinite(float64(p.X)) || !isFinite(float64(p.Y)) {
			return
		}
	}
	b.tris = append(b.tris, t)
}

func midVert(a, b vert) vert {
	return vert{
		pt: gfx.Point{X: (a.pt.X + b.pt.X) / 2, Y: (a.pt.Y + b.pt.Y) / 2},
		c:  [3]float32{(a.c[0] + b.c[0]) / 2, (a.c[1] + b.c[1]) / 2, (a.c[2] + b.c[2]) / 2},
	}
}

func colorDelta(a, b [3]float32) float32 {
	var out float32
	for k := range 3 {
		d := a[k] - b[k]
		if d < 0 {
			d = -d
		}
		if d > out {
			out = d
		}
	}
	return out
}

func quantize(c [3]float32) color.NRGBA {
	return color.NRGBA{R: clamp255(c[0]), G: clamp255(c[1]), B: clamp255(c[2]), A: 255}
}

func clamp255(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}
