// Package gfx holds the shared geometry types the engine's graphics pipeline is built on: points, rectangles,
// quads, an affine matrix, paths, and stroke parameters. Everything is float32 — the precision the original
// cgo/MuPDF implementation carried (C floats), which the exact-value tests were baselined against (plan.md
// invariant 4). Matrix composition follows PDF's row-vector convention: a point is transformed as
// [x y 1]·M, and M1.Mul(M2) applies M1 first.
package gfx

import "math"

// Point is a point or vector in some coordinate space.
type Point struct {
	X, Y float32
}

// Rect is an axis-aligned rectangle. A Rect is normalized when X0 <= X1 and Y0 <= Y1; Normalize returns that
// form, and consumers generally expect it.
type Rect struct {
	X0, Y0, X1, Y1 float32
}

// Normalize returns the rectangle with its corners ordered so X0 <= X1 and Y0 <= Y1.
func (r Rect) Normalize() Rect {
	if r.X0 > r.X1 {
		r.X0, r.X1 = r.X1, r.X0
	}
	if r.Y0 > r.Y1 {
		r.Y0, r.Y1 = r.Y1, r.Y0
	}
	return r
}

// IsEmpty reports whether the normalized rectangle has no area.
func (r Rect) IsEmpty() bool {
	r = r.Normalize()
	return r.X0 >= r.X1 || r.Y0 >= r.Y1
}

// Quad is a quadrilateral, such as the bounds of a run of (possibly rotated or skewed) text. The corners are
// upper-left, upper-right, lower-left, and lower-right in the text's own orientation.
type Quad struct {
	UL, UR, LL, LR Point
}

// Matrix is a 2x3 affine transform in PDF's row-vector convention (ISO 32000-2 8.3.3):
//
//	x' = A·x + C·y + E
//	y' = B·x + D·y + F
//
// which is the same [a b c d e f] element order the cm operator and form /Matrix entries use.
type Matrix struct {
	A, B, C, D, E, F float32
}

// Identity returns the identity matrix.
func Identity() Matrix {
	return Matrix{A: 1, D: 1}
}

// Translate returns a translation matrix.
func Translate(dx, dy float32) Matrix {
	return Matrix{A: 1, D: 1, E: dx, F: dy}
}

// Scale returns a scale matrix.
func Scale(sx, sy float32) Matrix {
	return Matrix{A: sx, D: sy}
}

// Mul returns the composition that applies m first and then n ([x y 1]·m·n).
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		A: m.A*n.A + m.B*n.C,
		B: m.A*n.B + m.B*n.D,
		C: m.C*n.A + m.D*n.C,
		D: m.C*n.B + m.D*n.D,
		E: m.E*n.A + m.F*n.C + n.E,
		F: m.E*n.B + m.F*n.D + n.F,
	}
}

// Apply transforms the point by the matrix.
func (m Matrix) Apply(p Point) Point {
	x, y := m.ApplyXY(p.X, p.Y)
	return Point{X: x, Y: y}
}

// ApplyXY transforms the coordinate pair by the matrix.
func (m Matrix) ApplyXY(x, y float32) (tx, ty float32) {
	return m.A*x + m.C*y + m.E, m.B*x + m.D*y + m.F
}

// Invert returns the inverse transform, reporting false when the matrix is degenerate (zero or non-finite
// determinant) and no inverse exists.
func (m Matrix) Invert() (Matrix, bool) {
	det := m.A*m.D - m.B*m.C
	d := float64(det)
	if d == 0 || math.IsNaN(d) || math.IsInf(d, 0) {
		return Matrix{}, false
	}
	inv := Matrix{
		A: m.D / det,
		B: -m.B / det,
		C: -m.C / det,
		D: m.A / det,
	}
	inv.E = -(m.E*inv.A + m.F*inv.C)
	inv.F = -(m.E*inv.B + m.F*inv.D)
	if !inv.IsFinite() {
		return Matrix{}, false
	}
	return inv, true
}

// IsFinite reports whether every element is a finite number.
func (m Matrix) IsFinite() bool {
	for _, v := range [6]float32{m.A, m.B, m.C, m.D, m.E, m.F} {
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
	}
	return true
}

// LineCap selects the shape drawn at the endpoints of stroked open subpaths (PDF line cap style, ISO 32000-2
// 8.4.3.3). The values match the J operator's operand.
type LineCap uint8

// LineCap values.
const (
	ButtCap LineCap = iota
	RoundCap
	SquareCap
)

// LineJoin selects the shape drawn at the corners of stroked paths (PDF line join style, ISO 32000-2 8.4.3.4).
// The values match the j operator's operand.
type LineJoin uint8

// LineJoin values.
const (
	MiterJoin LineJoin = iota
	RoundJoin
	BevelJoin
)

// StrokeParams carries the stroke-related graphics-state parameters, in user-space units, exactly as the
// content stream provided them; consumers (the raster device) adapt them to their stroker's requirements.
// A Width of 0 requests the thinnest renderable line (a hairline). Dash is the dash array as written — possibly
// empty (solid), of odd length (the pattern repeats with on/off roles alternating), or degenerate (all zeros,
// rendered solid).
type StrokeParams struct {
	Dash       []float32
	Width      float32
	MiterLimit float32
	DashPhase  float32
	Cap        LineCap
	Join       LineJoin
}

// Clone returns a copy whose Dash slice is independent of the original.
func (s *StrokeParams) Clone() StrokeParams {
	out := *s
	if len(s.Dash) > 0 {
		out.Dash = append([]float32(nil), s.Dash...)
	}
	return out
}
