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
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Annotation-appearance selection and placement, matching MuPDF's display path (fz_run_page), pinned entirely by
// oracle probes:
//
//   - Only the normal appearance (/AP /N) ever renders — /D and /R never do. A stream-valued /N is used directly
//     (a stray /AS is ignored); a dictionary-valued /N requires /AS to name one of its stream entries, else the
//     annotation draws nothing.
//   - /F bits Invisible (1), Hidden (2), and NoView (32) each suppress rendering; Print (4) is irrelevant on
//     screen. MuPDF hides Invisible-flagged annotations of every subtype, not just unrecognized ones.
//   - /Link and /Popup annotations never render, even with an /AP. /Widget annotations render only when a field
//     type (/FT) is resolvable on the annotation or through its /Parent chain; membership in the AcroForm
//     /Fields array (or the existence of /AcroForm at all) is irrelevant. Every other subtype — known or unknown
//     — renders its appearance.
//   - Placement follows ISO 32000-2 12.5.5: the form's /BBox mapped through its /Matrix yields an axis-aligned
//     bounding box, and the appearance is translated/scaled so that box coincides with the annotation's
//     normalized /Rect. Degenerate boxes on either side skip the annotation entirely (probe-pinned), and a
//     reversed /Rect normalizes.
//   - Annotation opacity (/CA) is ignored — MuPDF's display path draws appearances opaque.
//
// MuPDF additionally synthesizes appearance streams for some /AP-less markup annotations (Square and Circle
// observed, drawing /C borders and /IC interiors). That synthesis is deliberately out of scope: annotations
// without a usable /AP draw nothing here, and the corpus pins only the /AP path.

// maxParentDepth caps the /Parent chain walk for the widget /FT lookup so reference cycles terminate.
const maxParentDepth = 32

// annotHiddenFlags are the /F bits that suppress rendering: Invisible (1), Hidden (2), NoView (32).
const annotHiddenFlags = 1 | 2 | 32

// Annot is one renderable annotation appearance: the /AP /N form stream (state-selected for dictionary-valued
// /N) and the ISO 32000-2 12.5.5 placement matrix mapping appearance space onto the page's PDF space. Raw is
// the stream object as stored in the file (a cos.Ref when indirect), which the interpreter's form-cycle set
// keys on.
type Annot struct {
	Raw       cos.Object
	Stream    *cos.Stream
	Transform gfx.Matrix
}

// Annotations returns the renderable annotation appearances of the given 0-based page, in /Annots order,
// filtered and placed per the rules above. The maxPageLinks cap bounds the walk like the links walk.
func (d *Document) Annotations(pageNumber int) []Annot {
	if pageNumber < 0 || pageNumber >= len(d.pages) {
		return nil
	}
	annots, ok := d.cos.GetArray(d.pages[pageNumber], "Annots")
	if !ok {
		return nil
	}
	var out []Annot
	for _, annotObj := range annots {
		annot, annotOK := cos.AsDict(d.cos.Resolve(annotObj))
		if !annotOK {
			continue
		}
		if a, usable := d.annotAppearance(annot); usable {
			if out = append(out, a); len(out) >= maxPageLinks {
				break
			}
		}
	}
	return out
}

// annotAppearance applies the subtype/flag gates and computes the 12.5.5 placement for one annotation.
func (d *Document) annotAppearance(annot cos.Dict) (a Annot, ok bool) {
	if flags, hasF := cos.AsInt(d.cos.Resolve(annot["F"])); hasF && flags&annotHiddenFlags != 0 {
		return a, false
	}
	subtype, _ := d.cos.GetName(annot, "Subtype")
	switch subtype {
	case "Link", "Popup":
		return a, false
	case "Widget":
		if !d.widgetHasFieldType(annot) {
			return a, false
		}
	}
	raw, stream, streamOK := d.appearanceStream(annot)
	if !streamOK {
		return a, false
	}
	rect, rectOK := d.rectFromObj(annot["Rect"])
	if !rectOK {
		return a, false
	}
	bbox, bboxOK := d.rectFromObj(stream.Dict["BBox"])
	if !bboxOK {
		return a, false
	}
	m := gfx.Identity()
	if arr, hasM := d.cos.GetArray(stream.Dict, "Matrix"); hasM && len(arr) == 6 {
		var v [6]float32
		valid := true
		for i, entry := range arr {
			f, numOK := cos.AsReal(d.cos.Resolve(entry))
			if !numOK {
				valid = false
				break
			}
			v[i] = float32(f)
		}
		if valid {
			m = gfx.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}
			if !m.IsFinite() {
				m = gfx.Identity()
			}
		}
	}
	// Map the four /BBox corners through /Matrix and take the axis-aligned bounding box, then derive the
	// translate+scale that carries it onto /Rect (ISO 32000-2 12.5.5).
	x00, y00 := m.ApplyXY(bbox[0], bbox[1])
	x10, y10 := m.ApplyXY(bbox[2], bbox[1])
	x01, y01 := m.ApplyXY(bbox[0], bbox[3])
	x11, y11 := m.ApplyXY(bbox[2], bbox[3])
	tx0 := min(min(x00, x10), min(x01, x11))
	ty0 := min(min(y00, y10), min(y01, y11))
	tx1 := max(max(x00, x10), max(x01, x11))
	ty1 := max(max(y00, y10), max(y01, y11))
	if !(tx0 < tx1 && ty0 < ty1) { // Degenerate or non-finite transformed box: nothing to place.
		return a, false
	}
	sx := (rect[2] - rect[0]) / (tx1 - tx0)
	sy := (rect[3] - rect[1]) / (ty1 - ty0)
	transform := gfx.Translate(-tx0, -ty0).Mul(gfx.Scale(sx, sy)).Mul(gfx.Translate(rect[0], rect[1]))
	if !transform.IsFinite() {
		return a, false
	}
	return Annot{Raw: raw, Stream: stream, Transform: transform}, true
}

// widgetHasFieldType reports whether a /Widget annotation resolves a field type (/FT) on itself or through its
// /Parent chain — MuPDF renders only such widgets (probe-pinned; orphan fields render, /FT-less widgets do not).
func (d *Document) widgetHasFieldType(annot cos.Dict) bool {
	node := annot
	for range maxParentDepth {
		if _, has := d.cos.GetName(node, "FT"); has {
			return true
		}
		parent, hasParent := d.cos.GetDict(node, "Parent")
		if !hasParent {
			return false
		}
		node = parent
	}
	return false
}

// appearanceStream selects the normal appearance: /AP /N directly when stream-valued, else the /AS-named entry
// of a dictionary-valued /N. Raw is the object as stored (before resolution) for the /N stream case, or the
// dictionary entry for the state case.
func (d *Document) appearanceStream(annot cos.Dict) (raw cos.Object, stream *cos.Stream, ok bool) {
	ap, hasAP := d.cos.GetDict(annot, "AP")
	if !hasAP {
		return nil, nil, false
	}
	raw = ap["N"]
	switch n := d.cos.Resolve(raw).(type) {
	case *cos.Stream:
		return raw, n, true
	case cos.Dict:
		as, hasAS := d.cos.GetName(annot, "AS")
		if !hasAS {
			return nil, nil, false
		}
		raw = n[as]
		if s, isStream := cos.AsStream(d.cos.Resolve(raw)); isStream {
			return raw, s, true
		}
	}
	return nil, nil, false
}
