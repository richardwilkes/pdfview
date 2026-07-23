// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

import (
	"image"
	"math"
	"testing"
)

// TestScaleRectDeterministic pins that scaleRect maps non-finite and out-of-range coordinates to 0 rather than emitting
// architecture-defined garbage. A hostile or damaged PDF can supply an extreme link /Rect or a degenerate search-quad
// coordinate whose float→int conversion is otherwise 0 on arm64 but math.MinInt64 on amd64.
func TestScaleRectDeterministic(t *testing.T) {
	inf := math.Inf(1)
	nan := math.NaN()
	huge := math.MaxFloat64
	for _, tc := range []struct {
		name                  string
		x0, y0, x1, y1, scale float64
		want                  image.Rectangle
	}{
		{name: "finite", x0: 1.2, y0: 2.7, x1: 3.4, y1: 4.1, scale: 1, want: image.Rect(1, 2, 4, 5)},
		{name: "posInf min corner", x0: inf, y0: inf, x1: 3, y1: 4, scale: 1, want: image.Rect(0, 0, 3, 4)},
		{name: "negInf min corner", x0: -inf, y0: -inf, x1: 3, y1: 4, scale: 1, want: image.Rect(0, 0, 3, 4)},
		{name: "posInf max corner", x0: 1, y0: 2, x1: inf, y1: inf, scale: 1, want: image.Rect(1, 2, 0, 0)},
		{name: "nan corners", x0: nan, y0: nan, x1: nan, y1: nan, scale: 1, want: image.Rect(0, 0, 0, 0)},
		{name: "out of range via scale", x0: huge, y0: huge, x1: huge, y1: huge, scale: huge, want: image.Rect(0, 0, 0, 0)},
		{name: "nan scale", x0: 1, y0: 2, x1: 3, y1: 4, scale: nan, want: image.Rect(0, 0, 0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scaleRect(tc.x0, tc.y0, tc.x1, tc.y1, tc.scale); got != tc.want {
				t.Errorf("scaleRect(%v,%v,%v,%v,%v) = %v, want %v",
					tc.x0, tc.y0, tc.x1, tc.y1, tc.scale, got, tc.want)
			}
		})
	}
}

// TestQuadToRectExtremeCoords covers the search-hit path: an extreme quad coordinate must not leak an
// architecture-defined int into the returned rectangle.
func TestQuadToRectExtremeCoords(t *testing.T) {
	q := quad{
		ulX: float32(math.Inf(1)), ulY: float32(math.Inf(1)),
		urX: float32(math.Inf(1)), urY: float32(math.Inf(1)),
		llX: 0, llY: 0,
		lrX: 0, lrY: 0,
	}
	got := quadToRect(q, 1)
	// minX/minY are 0; maxX/maxY are +Inf, which must clamp to 0.
	if got != image.Rect(0, 0, 0, 0) {
		t.Errorf("quadToRect with +Inf corners = %v, want %v", got, image.Rect(0, 0, 0, 0))
	}
}

// TestClampFloatToInt pins the shared clamp helper directly.
func TestClampFloatToInt(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
		want int
	}{
		{name: "zero", in: 0, want: 0},
		{name: "small positive", in: 42, want: 42},
		{name: "small negative", in: -42, want: -42},
		{name: "posInf", in: math.Inf(1), want: 0},
		{name: "negInf", in: math.Inf(-1), want: 0},
		{name: "nan", in: math.NaN(), want: 0},
		{name: "overflow", in: math.MaxFloat64, want: 0},
		{name: "underflow", in: -math.MaxFloat64, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampFloatToInt(tc.in); got != tc.want {
				t.Errorf("clampFloatToInt(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
