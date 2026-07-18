// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package doc

import (
	"math"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// pageGeom is a page's effective display geometry: the normalized CropBox ∩ MediaBox in PDF space (y-up,
// arbitrary origin) plus the normalized /Rotate value. It defines the top-left/y-down coordinate space —
// matching MuPDF's fz_bound_page semantics — that every coordinate crossing the engine seam (page bounds,
// link rectangles, destination points, outline positions) is expressed in. All arithmetic is float32: the
// exact-value tests were baselined against the C float precision of the MuPDF-based implementation, and wider
// intermediate math would produce off-by-one pixel differences after scaling.
type pageGeom struct {
	x0, y0, x1, y1 float32
	rotate         int
}

// inheritedAttrs carries the inheritable page attributes (ISO 32000-2 7.7.3.4) down the page-tree walk. Values
// are kept unresolved until a leaf is reached; a node's own entry overrides whatever an ancestor supplied.
type inheritedAttrs struct {
	mediaBox  cos.Object
	cropBox   cos.Object
	rotate    cos.Object
	resources cos.Object
}

// override replaces each attribute the node itself defines.
func (a inheritedAttrs) override(node cos.Dict) inheritedAttrs {
	if v := node["MediaBox"]; v != nil {
		a.mediaBox = v
	}
	if v := node["CropBox"]; v != nil {
		a.cropBox = v
	}
	if v := node["Rotate"]; v != nil {
		a.rotate = v
	}
	if v := node["Resources"]; v != nil {
		a.resources = v
	}
	return a
}

// defaultMediaBox is US Letter, the conventional fallback when a page has no usable /MediaBox.
var defaultMediaBox = [4]float32{0, 0, 612, 792}

// resolveGeom converts the inherited attributes of one page into its effective geometry: /MediaBox (falling
// back to US Letter when absent or degenerate), intersected with /CropBox when one is present (an empty
// intersection falls back to the MediaBox), plus the normalized rotation. Verified against MuPDF across box
// origins and all four rotations.
func (d *Document) resolveGeom(attrs inheritedAttrs) pageGeom {
	media, ok := d.rectFromObj(attrs.mediaBox)
	if !ok {
		media = defaultMediaBox
	}
	box := media
	if crop, cropOK := d.rectFromObj(attrs.cropBox); cropOK {
		box = intersectRect(crop, media)
		if box[0] >= box[2] || box[1] >= box[3] {
			box = media
		}
	}
	geom := pageGeom{x0: box[0], y0: box[1], x1: box[2], y1: box[3]}
	if r, rotOK := cos.AsInt(d.cos.Resolve(attrs.rotate)); rotOK {
		geom.rotate = normalizeRotate(r)
	}
	return geom
}

// rectFromObj resolves obj as a rectangle: an array of four finite numbers, normalized so x0 <= x1 and
// y0 <= y1. ok is false when the value is malformed; validity beyond shape (such as a non-empty extent) is the
// caller's concern. A degenerate rectangle (zero width or height) reports false, since every use here treats
// such a box as unusable.
func (d *Document) rectFromObj(obj cos.Object) (rect [4]float32, ok bool) {
	arr, ok := cos.AsArray(d.cos.Resolve(obj))
	if !ok || len(arr) < 4 {
		return rect, false
	}
	var vals [4]float32
	for i := range vals {
		f, numOK := cos.AsReal(d.cos.Resolve(arr[i]))
		if !numOK || math.IsNaN(f) || math.IsInf(f, 0) {
			return rect, false
		}
		vals[i] = float32(f)
	}
	rect[0] = min(vals[0], vals[2])
	rect[1] = min(vals[1], vals[3])
	rect[2] = max(vals[0], vals[2])
	rect[3] = max(vals[1], vals[3])
	return rect, rect[0] < rect[2] && rect[1] < rect[3]
}

func intersectRect(a, b [4]float32) [4]float32 {
	return [4]float32{max(a[0], b[0]), max(a[1], b[1]), min(a[2], b[2]), min(a[3], b[3])}
}

// normalizeRotate maps an arbitrary /Rotate value to 0, 90, 180, or 270. The value is first normalized into
// [0, 360), then rounded to the nearest multiple of 90 (ties round up), matching MuPDF's observed handling of
// out-of-spec values (probed: 45, 100, 450, -90, and -450 all display like 90/270).
func normalizeRotate(r int64) int {
	r %= 360
	if r < 0 {
		r += 360
	}
	return int((r + 45) / 90 * 90 % 360)
}

// displaySize returns the page's displayed width and height in PDF points: the effective box extent, with the
// axes swapped when the rotation is 90 or 270.
func (g pageGeom) displaySize() (width, height float32) {
	width = g.x1 - g.x0
	height = g.y1 - g.y0
	if g.rotate == 90 || g.rotate == 270 {
		return height, width
	}
	return width, height
}

// toTopLeft maps a point from PDF page space (y-up, box origin wherever the document put it) into the
// displayed page's top-left/y-down space, applying the rotation. NaN coordinates (destinations with no
// explicit position) propagate through the arithmetic exactly as they do through MuPDF's float matrix
// transform — note that for 90/270 rotations a NaN switches axes with its source coordinate. The four cases
// were pinned against MuPDF with an offset box origin.
func (g pageGeom) toTopLeft(x, y float32) (u, v float32) {
	switch g.rotate {
	case 90:
		return y - g.y0, x - g.x0
	case 180:
		return g.x1 - x, y - g.y0
	case 270:
		return g.y1 - y, g.x1 - x
	default:
		return x - g.x0, g.y1 - y
	}
}
