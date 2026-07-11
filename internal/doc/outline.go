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

// Outline-walk guards (see plan.md "Resource limits & robustness"): depth is capped, reference cycles are cut
// by a visited set shared across the whole walk, and the total number of nodes is capped so a hostile /Next
// chain cannot balloon memory. The public API applies its own, configurable OverallMaxTOCEntries budget on top.
const (
	maxOutlineDepth = 64
	maxOutlineNodes = 65536
)

// OutlineItem is one node of the document outline (/Outlines tree). Siblings link through Next and children
// hang off Down, mirroring the /First–/Next chains in the file and the shape the engine seam consumes. X and Y
// are the destination point mapped into the target page's top-left/y-down space (NaN when the destination
// carries no explicit coordinate); Page is the 0-based target page, or -1 when the item has no resolvable
// internal destination (including items whose action is an external URI). Title is the decoded text string,
// unsanitized — the public API sanitizes.
type OutlineItem struct {
	Down  *OutlineItem
	Next  *OutlineItem
	Title string
	X, Y  float32
	Page  int
}

// Outline returns the root of the document outline, or nil when the document has none.
func (d *Document) Outline() *OutlineItem {
	root, ok := d.cos.GetDict(d.cos.Trailer(), "Root")
	if !ok {
		return nil
	}
	outlines, ok := d.cos.GetDict(root, "Outlines")
	if !ok {
		return nil
	}
	budget := maxOutlineNodes
	return d.walkOutline(outlines["First"], 0, &budget, make(map[cos.Ref]bool))
}

// walkOutline builds the sibling chain starting at obj (a /First value), recursing into children. Siblings are
// walked iteratively so only the tree depth — capped — consumes Go stack.
func (d *Document) walkOutline(obj cos.Object, depth int, budget *int, visited map[cos.Ref]bool) *OutlineItem {
	if depth > maxOutlineDepth {
		return nil
	}
	var head *OutlineItem
	tail := &head
	for *budget > 0 {
		if ref, isRef := obj.(cos.Ref); isRef {
			if visited[ref] {
				break
			}
			visited[ref] = true
		}
		node, ok := cos.AsDict(d.cos.Resolve(obj))
		if !ok {
			break
		}
		*budget--
		item := &OutlineItem{Page: -1, X: nan32(), Y: nan32()}
		if title, titleOK := d.cos.GetString(node, "Title"); titleOK {
			item.Title = cos.DecodeTextString(title)
		}
		if destObj := d.destForNode(node); destObj != nil {
			dest := d.resolveDest(destObj)
			item.Page = dest.Page
			item.X = dest.X
			item.Y = dest.Y
		}
		if node["First"] != nil {
			item.Down = d.walkOutline(node["First"], depth+1, budget, visited)
		}
		*tail = item
		tail = &item.Next
		obj = node["Next"]
	}
	return head
}

// destForNode extracts the destination an outline item carries: /Dest directly, or a /GoTo action's /D. Items
// whose action is of any other kind (an external URI, JavaScript, ...) have no internal destination and yield
// nil, leaving the item at page -1 — matching MuPDF, which still lists such entries.
func (d *Document) destForNode(node cos.Dict) cos.Object {
	if obj := node["Dest"]; obj != nil {
		return obj
	}
	action, ok := d.cos.GetDict(node, "A")
	if !ok {
		return nil
	}
	if s, sOK := d.cos.GetName(action, "S"); !sOK || s != "GoTo" {
		return nil
	}
	return action["D"]
}
