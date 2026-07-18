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
)

// maxPageLinks caps the links collected from one page so a hostile /Annots array cannot balloon memory. The public API
// applies its own, configurable OverallMaxLinks budget on top.
const maxPageLinks = 65536

// Link is one link annotation on a page. The rectangle and — for internal links — the destination point are in the
// top-left/y-down space of their respective pages (the rectangle on the page the link appears on, the destination point
// on the target page). URI is the raw link target for external links and empty for internal ones; External reports
// which. Internal links that cannot be resolved carry Page -1 (the public API drops them). DestX/DestY are NaN when the
// destination has no explicit coordinate, and for external links.
type Link struct {
	URI            string
	X0, Y0, X1, Y1 float32
	DestX, DestY   float32
	Page           int
	External       bool
}

// Links returns the link annotations of the given 0-based page, in /Annots order. Annotations that are not links, and
// link annotations with neither a destination nor a supported action, are skipped entirely — matching MuPDF, which
// reports no link for them (as opposed to unresolvable destinations, which are reported with page -1).
func (d *Document) Links(pageNumber int) []Link {
	if pageNumber < 0 || pageNumber >= len(d.pages) {
		return nil
	}
	annots, ok := d.cos.GetArray(d.pages[pageNumber], "Annots")
	if !ok {
		return nil
	}
	geom := d.geoms[pageNumber]
	var links []Link
	for _, annotObj := range annots {
		annot, annotOK := cos.AsDict(d.cos.Resolve(annotObj))
		if !annotOK {
			continue
		}
		if subtype, subOK := d.cos.GetName(annot, "Subtype"); !subOK || subtype != "Link" {
			continue
		}
		link, linkOK := d.linkFromAnnot(annot)
		if !linkOK {
			continue
		}
		link.X0, link.Y0, link.X1, link.Y1 = mapRect(geom, d.linkRect(annot))
		if links = append(links, link); len(links) >= maxPageLinks {
			break
		}
	}
	return links
}

// linkRect extracts the annotation's /Rect normalized in PDF space. A missing or malformed rectangle degrades to the
// empty rectangle at the origin rather than dropping the link, mirroring MuPDF's lenient pdf_to_rect.
func (d *Document) linkRect(annot cos.Dict) [4]float32 {
	rect, _ := d.rectFromObj(annot["Rect"])
	return rect
}

// mapRect maps a PDF-space rectangle into the page's top-left space. The corners are mapped individually and re-sorted,
// since rotation can swap and reverse the axes.
func mapRect(geom pageGeom, rect [4]float32) (x0, y0, x1, y1 float32) {
	u0, v0 := geom.toTopLeft(rect[0], rect[1])
	u1, v1 := geom.toTopLeft(rect[2], rect[3])
	return min(u0, u1), min(v0, v1), max(u0, u1), max(v0, v1)
}

// linkFromAnnot classifies one /Subtype /Link annotation: a /Dest (or /A /GoTo /D) destination makes an internal link;
// a /A /URI action is external when its URI carries a scheme (fz_is_external_link semantics) and otherwise resolves
// like the intra-document fragments MuPDF synthesizes; /GoToR and /Launch surface the target file specification as the
// URI. ok is false when the annotation carries nothing usable and is not a link at all.
func (d *Document) linkFromAnnot(annot cos.Dict) (link Link, ok bool) {
	link = Link{Page: -1, DestX: nan32(), DestY: nan32()}
	if destObj := annot["Dest"]; destObj != nil {
		return internalLink(d.resolveDest(destObj)), true
	}
	action, actionOK := d.cos.GetDict(annot, "A")
	if !actionOK {
		return link, false
	}
	kind, _ := d.cos.GetName(action, "S")
	switch kind {
	case "GoTo":
		destObj := action["D"]
		if destObj == nil {
			return link, false
		}
		return internalLink(d.resolveDest(destObj)), true
	case "URI":
		uri, uriOK := d.cos.GetString(action, "URI")
		if !uriOK {
			return link, false
		}
		text := cos.DecodeTextString(uri)
		if hasURIScheme(text) {
			link.URI = text
			link.External = true
			return link, true
		}
		return internalLink(d.resolveURIFragment(text)), true
	case "GoToR", "Launch":
		// Remote and launch targets degrade to their file specification; without an embedded scheme the public API will
		// drop them (best effort — the corpus has none to pin exact behavior against).
		file := d.fileSpecString(action["F"])
		if file == "" {
			return link, false
		}
		link.URI = file
		link.External = hasURIScheme(file)
		return link, true
	default:
		return link, false
	}
}

// internalLink builds the Link for a resolved internal destination.
func internalLink(dest Dest) Link {
	return Link{Page: dest.Page, DestX: dest.X, DestY: dest.Y}
}

// fileSpecString extracts a file specification (ISO 32000-2 7.11): either a bare string or a dictionary, whose /UF
// (Unicode) entry is preferred over /F.
func (d *Document) fileSpecString(obj cos.Object) string {
	switch v := d.cos.Resolve(obj).(type) {
	case cos.String:
		return cos.DecodeTextString(v)
	case cos.Dict:
		if s, ok := d.cos.GetString(v, "UF"); ok {
			return cos.DecodeTextString(s)
		}
		if s, ok := d.cos.GetString(v, "F"); ok {
			return cos.DecodeTextString(s)
		}
	}
	return ""
}
