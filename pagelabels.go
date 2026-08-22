// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

import "strings"

// PageLabel returns the display label the document assigns to the given 0-based page: the number a reader shows for it,
// which is not necessarily its position in the file. Labels come from the catalog's /PageLabels number tree (ISO
// 32000-2 12.4.2), whose ranges each number their pages in one of five styles — decimal ("1"), uppercase or lowercase
// roman ("I", "i"), or uppercase or lowercase alphabetic ("A", "a") — optionally behind a prefix and starting from a
// number of the range's choosing, so a document can label its first four pages "i" through "iv" and then restart at 1.
//
// Pages no range covers — every page of a document that defines no tree, and any page before the tree's first range —
// are labeled with their decimal ordinal, the same thing a reader falls back to, so there is always a label to show;
// HasPageLabels reports whether the labels are the document's own or that fallback. A range that names neither a style
// nor a prefix genuinely labels its pages with the empty string, and that is reported as-is rather than replaced with
// something friendlier.
//
// The label is sanitized exactly as outline titles are: undecodable, control, and non-printable characters are dropped
// and the surrounding whitespace is trimmed. It returns "" for a page number outside 0 through PageCount()-1, and for a
// released or zero-value document.
func (d *Document) PageLabel(pageNumber int) string {
	if !d.usable() {
		return ""
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return ""
	}
	labels := d.eng.pageLabels()
	if pageNumber < 0 || pageNumber >= len(labels) {
		return ""
	}
	return sanitizeString(labels[pageNumber])
}

// PageLabels returns the display label of every page, indexed by 0-based page number, so the result holds PageCount
// entries. It is what PageLabel reports for each page in turn — the same five styles, the same decimal fallback for
// pages the /PageLabels tree does not cover, the same empty label for a range that specifies nothing, and the same
// sanitization — gathered in one call rather than one call per page.
//
// The slice is built fresh on every call and belongs entirely to the caller, who may sort, filter, or otherwise modify
// it without disturbing the document or a slice an earlier call returned. It returns nil for a document with no pages,
// and for a released or zero-value document.
func (d *Document) PageLabels() []string {
	if !d.usable() {
		return nil
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return nil
	}
	labels := d.eng.pageLabels()
	if len(labels) == 0 {
		return nil
	}
	// The engine hands back its own cache, so this is a copy rather than a re-slice: the caller is free to modify what
	// it gets, and every later page-label call must still see the labels the document actually defines.
	sanitized := make([]string, len(labels))
	for i, label := range labels {
		sanitized[i] = sanitizeString(label)
	}
	return sanitized
}

// PagesWithLabel returns the 0-based number of every page whose display label matches label, in ascending order. It is
// the reverse of PageLabel, and it reports all matches rather than one because page labels are not unique: a document
// that restarts its numbering — front matter in roman, then chapters from "1", then an appendix from "1" again —
// genuinely has more than one page labeled "1".
//
// The comparison is case-insensitive (Unicode case folding), so "IV" finds the page labeled "iv" and "a-3" finds "A-3".
// Both sides are sanitized the way PageLabel sanitizes its result before they are compared, so a query carrying
// surrounding whitespace or stray control characters still matches. Pages that fall back to their decimal ordinal
// participate like any other, so "5" finds page 4 in a document that defines no labels at all.
//
// It returns nil when no page matches, when label is empty or sanitizes to empty, and for a released or zero-value
// document.
func (d *Document) PagesWithLabel(label string) []int {
	if !d.usable() {
		return nil
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return nil
	}
	needle := sanitizeString(label)
	if needle == "" {
		return nil
	}
	var pages []int
	for pageNumber, candidate := range d.eng.pageLabels() {
		if strings.EqualFold(needle, sanitizeString(candidate)) {
			pages = append(pages, pageNumber)
		}
	}
	return pages
}

// HasPageLabels reports whether the document defines page labels of its own, meaning the catalog's /PageLabels number
// tree yielded at least one usable range. It is the only way to tell a document that labels its pages "1", "2", "3"
// from one that carries no tree at all and got the same labels from PageLabel's decimal fallback, which matters to a
// caller that wants to show a label only when the document asked for one. It returns false for a released or zero-value
// document.
func (d *Document) HasPageLabels() bool {
	if !d.usable() {
		return false
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return false
	}
	return d.eng.hasPageLabels()
}

// pageLabels returns one label per page, decoded but not yet sanitized, or nil when they cannot be built. The slice is
// the engine's own cache — built on the first call and reused by every later one — so it is read here and never handed
// past the public methods uncopied. doc.PageLabels flattens the untrusted /PageLabels number tree on that first call; a
// panic provoked by a malformed tree surfaces as no labels rather than escaping the public API, matching the guards on
// the other engine calls.
func (e *engineDocument) pageLabels() (labels []string) {
	defer func() {
		if recover() != nil {
			labels = nil
		}
	}()
	return e.doc.PageLabels()
}

// hasPageLabels reports whether the document's /PageLabels tree yielded at least one usable range. It triggers the same
// build pageLabels does and carries the same guard, so a malformed tree reports no labels rather than panicking.
func (e *engineDocument) hasPageLabels() (has bool) {
	defer func() {
		if recover() != nil {
			has = false
		}
	}()
	return e.doc.HasPageLabels()
}
