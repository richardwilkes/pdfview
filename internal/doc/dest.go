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
	"strconv"
	"strings"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// Traversal guards: name-tree recursion is depth-capped and node-capped, a visited set skips reference cycles, and a
// destination chain that keeps indirecting (name → dictionary → name ...) is cut after maxDestChain steps.
const (
	maxNameTreeDepth = 64
	maxDestChain     = 8
	// maxNameTreeNodes and maxNamedDests bound the flattened /Names → /Dests index: tree nodes visited per build and name
	// → destination pairs retained. Both sit far above any real document; past them the remaining names do not resolve, as
	// an unknown name already does not.
	maxNameTreeNodes = 1 << 16
	maxNamedDests    = 1 << 18
)

// Dest is a resolved internal destination: a 0-based target page plus the explicit point on it, already mapped into the
// page's top-left/y-down space. X and Y are NaN when the destination carries no explicit coordinate on that axis (a
// /Fit destination has no point at all; /FitH has only Y; a /XYZ slot may be null). Page is -1 when the destination
// cannot be resolved to a page in this document.
type Dest struct {
	X, Y float32
	Page int
}

// nan32 is the float32 quiet NaN used for absent coordinates.
func nan32() float32 {
	return float32(math.NaN())
}

// unresolvedDest is the Dest reported when resolution fails entirely.
func unresolvedDest() Dest {
	return Dest{X: nan32(), Y: nan32(), Page: -1}
}

// resolveDest resolves a destination object — an explicit array, a name or byte string naming one, or a dictionary
// wrapping one in /D (old-style /Dests values and /GoTo actions share that shape) — to a page and point. Failures come
// back as unresolvedDest (page -1), which the public API drops.
func (d *Document) resolveDest(obj cos.Object) Dest {
	for range maxDestChain {
		obj = d.cos.Resolve(obj)
		switch v := obj.(type) {
		case cos.Name:
			obj = d.lookupNamedDest([]byte(v))
		case cos.String:
			obj = d.lookupNamedDest([]byte(v))
		case cos.Dict:
			obj = v["D"]
		case cos.Array:
			return d.destFromArray(v)
		default:
			return unresolvedDest()
		}
	}
	return unresolvedDest()
}

// destFromArray interprets an explicit destination array (ISO 32000-2 12.3.2.2): the target page (an indirect reference
// to a page object or, as some writers produce, a 0-based page index), the fit kind, and the kind's coordinate
// operands. Coordinates the kind does not define, null/absent slots, and non-numeric operands are NaN. The PDF-space
// point is mapped into the target page's top-left space, as MuPDF reports destination points (probe-pinned for /XYZ,
// /FitH, /FitV, /FitR, and null slots).
func (d *Document) destFromArray(arr cos.Array) Dest {
	if len(arr) == 0 {
		return unresolvedDest()
	}
	page := -1
	switch v := arr[0].(type) {
	case cos.Ref:
		if n, ok := d.pageIndex[v.Key()]; ok {
			page = n
		}
	case cos.Integer:
		if v >= 0 && int64(v) < int64(len(d.pages)) {
			page = int(v)
		}
	}
	if page < 0 {
		return unresolvedDest()
	}
	x := nan32()
	y := nan32()
	if kind, ok := cos.AsName(d.cos.Resolve(destElem(arr, 1))); ok {
		switch kind {
		case "XYZ":
			x = d.destCoord(arr, 2)
			y = d.destCoord(arr, 3)
		case "FitH", "FitBH":
			y = d.destCoord(arr, 2)
		case "FitV", "FitBV":
			x = d.destCoord(arr, 2)
		case "FitR":
			x = d.destCoord(arr, 2)
			y = d.destCoord(arr, 5) // /FitR left bottom right top: the point is (left, top).
		}
		// /Fit, /FitB, and unknown kinds carry no coordinate.
	}
	u, v := d.geoms[page].toTopLeft(x, y)
	return Dest{X: u, Y: v, Page: page}
}

// destElem returns arr[index], or nil (which resolves as null) when the array is too short.
func destElem(arr cos.Array, index int) cos.Object {
	if index < len(arr) {
		return arr[index]
	}
	return nil
}

// destCoord extracts one numeric destination operand as float32, or NaN when the slot is absent, null, or not a number.
func (d *Document) destCoord(arr cos.Array, index int) float32 {
	if f, ok := cos.AsReal(d.cos.Resolve(destElem(arr, index))); ok {
		return float32(f)
	}
	return nan32()
}

// lookupNamedDest finds the destination a name or byte string refers to, trying the catalog's old-style /Dests
// dictionary (PDF 1.1) first and the /Names → /Dests name tree (PDF 1.2+) second. Both stores accept both key flavors,
// since real files mix them. It returns nil (null) for an unknown name.
//
// The name tree is flattened into a map once per document rather than searched per lookup: the callers are per-node
// (walkOutline resolves up to maxOutlineNodes destinations, Links up to maxPageLinks), and a per-lookup search made
// every miss a full tree scan, so a file pairing a few hundred thousand leaf pairs with 65536 outline items naming
// absent destinations cost ~10^10 compare steps from one TableOfContents call. The public OverallMaxTOCEntries and
// OverallMaxLinks caps do not help: the engine-side walk finishes before they truncate.
func (d *Document) lookupNamedDest(key []byte) cos.Object {
	root, ok := d.cos.GetDict(d.cos.Trailer(), "Root")
	if !ok {
		return nil
	}
	if dests, dictOK := d.cos.GetDict(root, "Dests"); dictOK {
		if obj := dests[cos.Name(key)]; obj != nil {
			return obj
		}
	}
	if d.destIndex == nil {
		d.destIndex = d.buildDestIndex(root)
	}
	return d.destIndex[string(key)]
}

// buildDestIndex flattens the catalog's /Names → /Dests name tree into name → destination pairs. It always returns a
// non-nil map, so a document without a name tree builds the (empty) index once and never walks again.
func (d *Document) buildDestIndex(root cos.Dict) map[string]cos.Object {
	index := make(map[string]cos.Object)
	names, ok := d.cos.GetDict(root, "Names")
	if !ok {
		return index
	}
	tree, ok := d.cos.GetDict(names, "Dests")
	if !ok {
		return index
	}
	nodes := maxNameTreeNodes
	d.indexNameTree(tree, index, 0, &nodes, make(map[cos.RefKey]bool))
	return index
}

// indexNameTree adds one name-tree node's entries (ISO 32000-2 7.9.6) to index and descends into its /Kids. Leaf
// /Names arrays are taken in order and the first entry for a key wins. /Limits are not consulted, since collecting
// every kid's names finds exactly what a limit-pruned search would plus the entries a mis-stated limit would have
// hidden — the same leniency that already descends into a kid whose /Limits are broken. Depth, node count, and entry
// count are capped, and reference cycles are skipped.
func (d *Document) indexNameTree(node cos.Dict, index map[string]cos.Object, depth int, nodes *int,
	visited map[cos.RefKey]bool,
) {
	if depth > maxNameTreeDepth || *nodes <= 0 {
		return
	}
	*nodes--
	if names, ok := d.cos.GetArray(node, "Names"); ok {
		for i := 0; i+1 < len(names); i += 2 {
			if len(index) >= maxNamedDests {
				break
			}
			if k, kOK := cos.AsString(d.cos.Resolve(names[i])); kOK {
				if _, dup := index[string(k)]; !dup {
					index[string(k)] = names[i+1]
				}
			}
		}
	}
	kids, ok := d.cos.GetArray(node, "Kids")
	if !ok {
		return
	}
	for _, kid := range kids {
		if ref, isRef := kid.(cos.Ref); isRef {
			if visited[ref.Key()] {
				continue
			}
			visited[ref.Key()] = true
		}
		if kidDict, kidOK := cos.AsDict(d.cos.Resolve(kid)); kidOK {
			d.indexNameTree(kidDict, index, depth+1, nodes, visited)
		}
	}
}

// hasURIScheme reports whether uri begins with a URI scheme (RFC 3986: a letter followed by letters, digits, "+", "-",
// or ".", terminated by ":"). This is the classification fz_is_external_link applies: a scheme makes a link external;
// anything else is treated as an intra-document reference.
func hasURIScheme(uri string) bool {
	for i := range len(uri) {
		switch ch := uri[i]; {
		case ch == ':':
			return i > 0
		case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z'):
		case i > 0 && ((ch >= '0' && ch <= '9') || ch == '+' || ch == '-' || ch == '.'):
		default:
			return false
		}
	}
	return false
}

// resolveURIFragment resolves the intra-document URI forms MuPDF itself synthesizes and accepts for links without an
// external scheme: "#page=N&zoom=z,x,y" (N is 1-based; x and y are already top-left page-space values and are applied
// without further mapping; absent or unparseable values are NaN) and "#nameddest=NAME" (percent-decoded, then resolved
// like any named destination). Anything else is unresolvable.
func (d *Document) resolveURIFragment(uri string) Dest {
	frag, ok := strings.CutPrefix(uri, "#")
	if !ok {
		return unresolvedDest()
	}
	if name, isNamed := strings.CutPrefix(frag, "nameddest="); isNamed {
		return d.resolveDest(cos.String(percentDecode(name)))
	}
	dest := unresolvedDest()
	for part := range strings.SplitSeq(frag, "&") {
		if pageStr, isPage := strings.CutPrefix(part, "page="); isPage {
			if n, err := strconv.Atoi(pageStr); err == nil && n >= 1 && n <= len(d.pages) {
				dest.Page = n - 1
			}
		} else if zoomStr, isZoom := strings.CutPrefix(part, "zoom="); isZoom {
			// zoom=z,x,y; the zoom factor is ignored.
			comps := strings.Split(zoomStr, ",")
			if len(comps) >= 2 {
				dest.X = parseFloat32(comps[1])
			}
			if len(comps) >= 3 {
				dest.Y = parseFloat32(comps[2])
			}
		}
	}
	if dest.Page < 0 {
		return unresolvedDest()
	}
	return dest
}

// parseFloat32 parses s as a float32, returning NaN when it does not parse ("nan" itself parses to NaN, which MuPDF
// emits for absent coordinates in the URIs it synthesizes).
func parseFloat32(s string) float32 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 32)
	if err != nil {
		return nan32()
	}
	return float32(f)
}

// percentDecode applies URI percent-decoding, leaving malformed escapes as-is.
func percentDecode(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, hiOK := unhex(s[i+1])
			lo, loOK := unhex(s[i+2])
			if hiOK && loOK {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return out
}

func unhex(ch byte) (byte, bool) {
	switch {
	case ch >= '0' && ch <= '9':
		return ch - '0', true
	case ch >= 'a' && ch <= 'f':
		return ch - 'a' + 10, true
	case ch >= 'A' && ch <= 'F':
		return ch - 'A' + 10, true
	default:
		return 0, false
	}
}
