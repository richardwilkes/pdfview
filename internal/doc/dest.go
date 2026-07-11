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
	"bytes"
	"math"
	"strconv"
	"strings"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// Traversal guards (see plan.md "Resource limits & robustness"): hostile documents cannot force unbounded work
// because name-tree recursion is depth-capped and reference cycles are skipped via a visited set, and a chain of
// destinations that keeps indirecting (name → dictionary → name ...) is cut off after maxDestChain steps.
const (
	maxNameTreeDepth = 64
	maxDestChain     = 8
)

// Dest is a resolved internal destination: a 0-based target page plus the explicit point on it, already mapped
// into the page's top-left/y-down space. X and Y are NaN when the destination carries no explicit coordinate on
// that axis (a /Fit destination has no point at all; /FitH has only Y; a /XYZ slot may be null). Page is -1 when
// the destination cannot be resolved to a page in this document.
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

// resolveDest resolves a destination object — an explicit array, a name or byte string naming a destination, or
// a dictionary wrapping one in /D (both the old-style /Dests values and /GoTo actions use that shape) — to a
// page and point. It always returns a usable Dest; failures come back as unresolvedDest (page -1), which the
// public API drops.
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

// destFromArray interprets an explicit destination array (ISO 32000-2 12.3.2.2): the target page (an indirect
// reference to a page object, or — as some writers produce — a 0-based page index), the fit kind, and the kind's
// coordinate operands. Coordinates the kind does not define, null/absent slots, and non-numeric operands are
// NaN. The extracted PDF-space point is mapped into the target page's top-left space, exactly as MuPDF reports
// destination points (pinned by probes for /XYZ, /FitH, /FitV, /FitR, and null slots — see the M3 decision log).
func (d *Document) destFromArray(arr cos.Array) Dest {
	if len(arr) == 0 {
		return unresolvedDest()
	}
	page := -1
	switch v := arr[0].(type) {
	case cos.Ref:
		if n, ok := d.pageIndex[v]; ok {
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
		// /Fit and /FitB carry no coordinate; unknown kinds are treated the same way.
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

// destCoord extracts one numeric destination operand as float32, or NaN when the slot is absent, null, or not a
// number.
func (d *Document) destCoord(arr cos.Array, index int) float32 {
	if f, ok := cos.AsReal(d.cos.Resolve(destElem(arr, index))); ok {
		return float32(f)
	}
	return nan32()
}

// lookupNamedDest finds the destination a name or byte string refers to, trying the old-style /Dests dictionary
// in the catalog (PDF 1.1) first and the /Names → /Dests name tree (PDF 1.2+) second. Both stores accept both
// key flavors — a name's text and a byte string's bytes compare identically — since real files mix them. It
// returns nil (null) when the name is unknown.
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
	if names, namesOK := d.cos.GetDict(root, "Names"); namesOK {
		if tree, treeOK := d.cos.GetDict(names, "Dests"); treeOK {
			if obj, found := d.lookupNameTree(tree, key, 0, make(map[cos.Ref]bool)); found {
				return obj
			}
		}
	}
	return nil
}

// lookupNameTree searches a name tree (ISO 32000-2 7.9.6) for key. Leaf /Names arrays are scanned linearly —
// robust against the unsorted arrays repaired files exhibit — and /Kids are pruned by their /Limits only when
// the limits are well-formed, so a node with broken limits is still descended into rather than silently
// skipped. Depth is capped and reference cycles are skipped.
func (d *Document) lookupNameTree(node cos.Dict, key []byte, depth int, visited map[cos.Ref]bool) (cos.Object, bool) {
	if depth > maxNameTreeDepth {
		return nil, false
	}
	if names, ok := d.cos.GetArray(node, "Names"); ok {
		for i := 0; i+1 < len(names); i += 2 {
			if k, kOK := cos.AsString(d.cos.Resolve(names[i])); kOK && bytes.Equal(k, key) {
				return names[i+1], true
			}
		}
	}
	kids, ok := d.cos.GetArray(node, "Kids")
	if !ok {
		return nil, false
	}
	for _, kid := range kids {
		if ref, isRef := kid.(cos.Ref); isRef {
			if visited[ref] {
				continue
			}
			visited[ref] = true
		}
		kidDict, kidOK := cos.AsDict(d.cos.Resolve(kid))
		if !kidOK {
			continue
		}
		if limits, limOK := d.cos.GetArray(kidDict, "Limits"); limOK && len(limits) >= 2 {
			lo, loOK := cos.AsString(d.cos.Resolve(limits[0]))
			hi, hiOK := cos.AsString(d.cos.Resolve(limits[1]))
			if loOK && hiOK && (bytes.Compare(key, lo) < 0 || bytes.Compare(key, hi) > 0) {
				continue
			}
		}
		if obj, found := d.lookupNameTree(kidDict, key, depth+1, visited); found {
			return obj, true
		}
	}
	return nil, false
}

// hasURIScheme reports whether uri begins with a URI scheme (RFC 3986: a letter followed by letters, digits,
// "+", "-", or ".", terminated by ":"). This is the classification fz_is_external_link applies: a scheme makes
// a link external; anything else is treated as an intra-document reference.
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

// resolveURIFragment resolves the intra-document URI forms MuPDF itself synthesizes and accepts for links
// without an external scheme: "#page=N&zoom=z,x,y" (N is 1-based; x and y are already top-left page-space
// values and are applied without further mapping; absent or unparseable values are NaN) and "#nameddest=NAME"
// (percent-decoded, then resolved like any named destination). Anything else is unresolvable.
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
			// zoom=z,x,y — the zoom factor itself is not part of the public contract and is ignored.
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

// parseFloat32 parses s as a float32, returning NaN when it does not parse ("nan" itself parses to NaN, which
// MuPDF emits for absent coordinates in the URIs it synthesizes).
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
