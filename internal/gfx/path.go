// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package gfx

// PathVerb is one path-construction step.
type PathVerb uint8

// PathVerb values. The number of points each verb consumes from Path.Points: MoveTo and LineTo one, QuadTo two, CubicTo
// three, ClosePath none.
const (
	MoveTo PathVerb = iota
	LineTo
	QuadTo
	CubicTo
	ClosePath
)

// Path is a sequence of subpaths in some coordinate space: verbs plus the points they consume. It carries no fill rule
// — the painting call that consumes the path supplies one — and does no validation; the interpreter that builds it
// enforces PDF's construction rules (such as a subpath starting with a moveto).
type Path struct {
	Verbs  []PathVerb
	Points []Point
}

// MoveTo starts a new subpath at (x, y).
func (p *Path) MoveTo(x, y float32) {
	p.Verbs = append(p.Verbs, MoveTo)
	p.Points = append(p.Points, Point{X: x, Y: y})
}

// LineTo appends a line segment to (x, y).
func (p *Path) LineTo(x, y float32) {
	p.Verbs = append(p.Verbs, LineTo)
	p.Points = append(p.Points, Point{X: x, Y: y})
}

// QuadTo appends a quadratic Bézier segment with control point (x1, y1) ending at (x2, y2).
func (p *Path) QuadTo(x1, y1, x2, y2 float32) {
	p.Verbs = append(p.Verbs, QuadTo)
	p.Points = append(p.Points, Point{X: x1, Y: y1}, Point{X: x2, Y: y2})
}

// CubicTo appends a cubic Bézier segment with control points (x1, y1) and (x2, y2) ending at (x3, y3).
func (p *Path) CubicTo(x1, y1, x2, y2, x3, y3 float32) {
	p.Verbs = append(p.Verbs, CubicTo)
	p.Points = append(p.Points, Point{X: x1, Y: y1}, Point{X: x2, Y: y2}, Point{X: x3, Y: y3})
}

// Close closes the current subpath.
func (p *Path) Close() {
	p.Verbs = append(p.Verbs, ClosePath)
}

// Rect appends an axis-aligned rectangle as a complete closed subpath, matching the re operator's expansion (ISO
// 32000-2 8.5.2.1: moveto, three linetos, closepath, counterclockwise for positive extents).
func (p *Path) Rect(x, y, w, h float32) {
	p.MoveTo(x, y)
	p.LineTo(x+w, y)
	p.LineTo(x+w, y+h)
	p.LineTo(x, y+h)
	p.Close()
}

// IsEmpty reports whether the path has no verbs at all.
func (p *Path) IsEmpty() bool {
	return len(p.Verbs) == 0
}

// Clone returns a deep copy of the path.
func (p *Path) Clone() *Path {
	return &Path{
		Verbs:  append([]PathVerb(nil), p.Verbs...),
		Points: append([]Point(nil), p.Points...),
	}
}

// Transform applies m to every point of the path in place.
func (p *Path) Transform(m Matrix) {
	for i, pt := range p.Points {
		p.Points[i] = m.Apply(pt)
	}
}
