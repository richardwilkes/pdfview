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
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// Traversal guards for the /PageLabels number tree, mirroring the name-tree guards in dest.go: hostile documents cannot
// force unbounded work because recursion is depth-capped, node count-capped, and reference cycles are skipped via a
// visited set.
const (
	maxNumberTreeDepth = 64
	maxNumberTreeNodes = 1 << 16
	// maxPageLabelRanges bounds how many key → range-dictionary pairs one build retains. Only one range per page can
	// ever matter and maxPages is 65536, so a document that hits this cap has already described every page it can have;
	// the pairs past it are dropped, which leaves their pages labeled by the last range that did fit.
	maxPageLabelRanges = 1 << 16
	// maxRomanPageLabel and maxAlphaPageLabel bound how long a single label may get before the styled forms give up and
	// print the number in decimal instead. Roman is greedy, so 65536 costs 65 "m" characters plus the remainder — about
	// 80 bytes — while the alpha style repeats one letter, so 26*64 caps it at 64 characters. Both sit far above any
	// page number a real document numbers in these styles, and a document that exceeds them is asking for a label no
	// reader could use; decimal is the honest answer there.
	maxRomanPageLabel = 65536
	maxAlphaPageLabel = 26 * 64
	// maxPageLabelStart clamps /St so that start plus the page's offset within its range cannot overflow int64.
	// Offsets are bounded by maxPages, so 2^62 leaves room for any of them with two bits to spare, and a start that
	// large already prints a 19-digit label.
	maxPageLabelStart = 1 << 62
)

// labelRange is one entry of the /PageLabels number tree (ISO 32000-2 12.4.2): the numbering that takes effect at page
// first and runs until the next range begins.
type labelRange struct {
	prefix string // The decoded (but unsanitized) /P text string; empty when absent or not a string.
	start  int64  // /St, defaulting to 1, clamped to ±maxPageLabelStart.
	first  int    // The 0-based page index the range starts at, which is the tree key.
	style  byte   // 'D', 'R', 'r', 'A', or 'a'; 0 when /S is absent or names an unknown style.
}

// PageLabels returns one display label per page, indexed by 0-based page number, so the result always has PageCount
// entries. Pages that no /PageLabels range covers — every page of a document without the tree, and any page before the
// tree's first key — get their decimal ordinal, matching what a reader shows when a document declines to name its
// pages. Labels are decoded text strings that have not been sanitized; the public API sanitizes them.
//
// The tree is flattened once and cached, for the same reason the /Names → /Dests name tree is (see buildDestIndex): the
// callers are per-page, so a per-page search would re-walk the whole tree for each of up to maxPages pages. The
// returned slice is the cache itself and must not be modified by callers.
func (d *Document) PageLabels() []string {
	if d.pageLabels == nil {
		d.buildPageLabels()
	}
	return d.pageLabels
}

// HasPageLabels reports whether the document defines a usable /PageLabels tree, meaning the flattened tree yielded at
// least one range dictionary. It is how a caller tells real labels from the decimal ordinals PageLabels falls back to,
// since the two are indistinguishable in a document that labels its pages "1", "2", "3". A range whose dictionary is
// empty still counts: the document does declare the range, and the empty labels it produces are its own answer.
func (d *Document) HasPageLabels() bool {
	if d.pageLabels == nil {
		d.buildPageLabels()
	}
	return d.pageLabelsPresent
}

// buildPageLabels flattens the catalog's /PageLabels number tree and expands it to one label per page. It always leaves
// d.pageLabels non-nil — make with a zero length still returns a non-nil slice — so a document without the tree builds
// the labels once and never walks again.
func (d *Document) buildPageLabels() {
	ranges := d.pageLabelRanges()
	d.pageLabelsPresent = len(ranges) > 0
	labels := make([]string, len(d.pages))
	for page := range labels {
		// The ranges are sorted and their keys unique, so the range in effect is the last one whose key is at or before
		// this page: an exact hit is that range, and a miss lands on the following range, whose predecessor is it.
		i, exact := slices.BinarySearchFunc(ranges, page, func(r labelRange, p int) int {
			return cmp.Compare(r.first, p)
		})
		if !exact {
			i--
		}
		if i < 0 {
			labels[page] = strconv.Itoa(page + 1) // Before the first range: the decimal ordinal.
			continue
		}
		labels[page] = formatPageLabel(ranges[i], page)
	}
	d.pageLabels = labels
}

// pageLabelRanges flattens the catalog's /PageLabels number tree into the ranges it defines, sorted by starting page.
// It returns nothing for a document with no tree, an unusable one, or one whose every entry was rejected.
func (d *Document) pageLabelRanges() []labelRange {
	root, ok := d.cos.GetDict(d.cos.Trailer(), "Root")
	if !ok {
		return nil
	}
	tree, ok := d.cos.GetDict(root, "PageLabels")
	if !ok {
		return nil
	}
	entries := make(map[int64]cos.Object)
	nodes := maxNumberTreeNodes
	d.indexNumberTree(tree, entries, 0, &nodes, make(map[cos.RefKey]bool))
	ranges := make([]labelRange, 0, len(entries))
	for key, value := range entries {
		if r, rOK := d.labelRangeFrom(key, value); rOK {
			ranges = append(ranges, r)
		}
	}
	slices.SortFunc(ranges, func(a, b labelRange) int { return cmp.Compare(a.first, b.first) })
	return ranges
}

// indexNumberTree adds one number-tree node's entries (ISO 32000-2 7.9.7) to entries and descends into its /Kids. Leaf
// /Nums arrays alternate key and value and are taken in order, and the first entry for a key wins. /Limits are not
// consulted, for the same reason the name-tree walk ignores them (see indexNameTree): collecting every kid's keys finds
// exactly what a limit-pruned search would plus the entries a mis-stated limit would have hidden. Depth, node count,
// and entry count are capped, and reference cycles are skipped.
func (d *Document) indexNumberTree(node cos.Dict, entries map[int64]cos.Object, depth int, nodes *int,
	visited map[cos.RefKey]bool,
) {
	if depth > maxNumberTreeDepth || *nodes <= 0 {
		return
	}
	*nodes--
	if nums, ok := d.cos.GetArray(node, "Nums"); ok {
		for i := 0; i+1 < len(nums); i += 2 {
			if len(entries) >= maxPageLabelRanges {
				break
			}
			// A key that is not an integer costs its pair and nothing more: the step of two keeps the rest of the array
			// aligned, so one bad key cannot shift every later value onto the wrong page.
			key, keyOK := cos.AsInt(d.cos.Resolve(nums[i]))
			if !keyOK || key < 0 || key >= maxPages {
				continue
			}
			if _, dup := entries[key]; !dup {
				entries[key] = nums[i+1]
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
			d.indexNumberTree(kidDict, entries, depth+1, nodes, visited)
		}
	}
}

// labelRangeFrom interprets one number-tree value as a page-label range dictionary. A value that is not a dictionary is
// dropped entirely rather than turned into an empty range, so the previous range simply extends over its pages — the
// same outcome as the entry never having been written. An unknown /S, an absent or non-string /P, and an absent /St
// each degrade to that part of the range being unspecified, which is what a range that omits them means anyway.
func (d *Document) labelRangeFrom(key int64, value cos.Object) (labelRange, bool) {
	dict, ok := cos.AsDict(d.cos.Resolve(value))
	if !ok {
		return labelRange{}, false
	}
	r := labelRange{first: int(key), start: 1}
	if style, styleOK := d.cos.GetName(dict, "S"); styleOK {
		switch style {
		case "D", "R", "r", "A", "a":
			r.style = style[0]
		}
	}
	if prefix, prefixOK := d.cos.GetString(dict, "P"); prefixOK {
		// Decryption already happened at parse time through the installed decryptor, so this is plaintext whenever the
		// file key is available. It is deliberately left unsanitized; the public API sanitizes.
		r.prefix = cos.DecodeTextString(prefix)
	}
	if start, startOK := d.cos.GetInt(dict, "St"); startOK {
		r.start = min(max(start, -maxPageLabelStart), maxPageLabelStart)
	}
	return r, true
}

// formatPageLabel builds the label the given range assigns to the given 0-based page: the prefix followed by the page's
// number within the range rendered in the range's style. A range with no usable /S contributes no number at all, so its
// pages all share the prefix — including the empty label an entirely empty range dictionary produces, which is what the
// spec describes and not a case for substituting something friendlier.
func formatPageLabel(r labelRange, page int) string {
	n := r.start + int64(page-r.first)
	switch r.style {
	case 'D':
		return r.prefix + strconv.FormatInt(n, 10)
	case 'R':
		return r.prefix + formatRoman(n, true)
	case 'r':
		return r.prefix + formatRoman(n, false)
	case 'A':
		return r.prefix + formatAlpha(n, true)
	case 'a':
		return r.prefix + formatAlpha(n, false)
	default:
		return r.prefix
	}
}

// romanNumerals is the classic greedy table: each value paired with the numeral that spells it, largest first, with the
// four subtractive pairs interleaved so a single left-to-right walk produces standard form.
var romanNumerals = [...]struct {
	glyphs string
	value  int64
}{
	{glyphs: "m", value: 1000},
	{glyphs: "cm", value: 900},
	{glyphs: "d", value: 500},
	{glyphs: "cd", value: 400},
	{glyphs: "c", value: 100},
	{glyphs: "xc", value: 90},
	{glyphs: "l", value: 50},
	{glyphs: "xl", value: 40},
	{glyphs: "x", value: 10},
	{glyphs: "ix", value: 9},
	{glyphs: "v", value: 5},
	{glyphs: "iv", value: 4},
	{glyphs: "i", value: 1},
}

// formatRoman renders n as a roman numeral, uppercase when upper is set. Roman numerals have no zero and no negative
// form, and grow without bound above maxRomanPageLabel, so anything outside [1, maxRomanPageLabel] is rendered in
// decimal instead — a label a reader can still act on, rather than an empty string or a screenful of "m".
func formatRoman(n int64, upper bool) string {
	if n < 1 || n > maxRomanPageLabel {
		return strconv.FormatInt(n, 10)
	}
	var sb strings.Builder
	for _, numeral := range romanNumerals {
		for n >= numeral.value {
			sb.WriteString(numeral.glyphs)
			n -= numeral.value
		}
	}
	if upper {
		return strings.ToUpper(sb.String())
	}
	return sb.String()
}

// formatAlpha renders n in the alphabetic style ISO 32000-2 12.4.2 defines, uppercase when upper is set: a→z for 1..26,
// then the same letters repeated — aa→zz for 27..52, aaa→zzz for 53..78 — rather than base-26 counting. The style has
// no zero or negative form, and each additional 26 pages adds a character, so anything outside [1, maxAlphaPageLabel]
// is rendered in decimal instead.
func formatAlpha(n int64, upper bool) string {
	if n < 1 || n > maxAlphaPageLabel {
		return strconv.FormatInt(n, 10)
	}
	base := byte('a')
	if upper {
		base = 'A'
	}
	return strings.Repeat(string(base+byte((n-1)%26)), int((n-1)/26)+1)
}
