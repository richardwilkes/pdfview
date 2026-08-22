// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview"
)

// pageLabelsPDF builds a document of pages blank US Letter pages whose catalog carries the given /PageLabels /Nums
// array. An empty nums omits /PageLabels entirely, which is the unlabeled document the fallback cases need. No xref is
// supplied (startxref 0) so the engine rebuilds it, exactly as in letterLinkPDF.
func pageLabelsPDF(nums string, pages int) string {
	var sb strings.Builder
	labels := ""
	if nums != "" {
		labels = fmt.Sprintf(" /PageLabels << /Nums [%s] >>", nums)
	}
	fmt.Fprintf(&sb, "%%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R%s >>\nendobj\n", labels)
	sb.WriteString("2 0 obj\n<< /Type /Pages /Kids [")
	for i := range pages {
		if i > 0 {
			sb.WriteString(" ")
		}
		fmt.Fprintf(&sb, "%d 0 R", i+3)
	}
	fmt.Fprintf(&sb, "] /Count %d >>\nendobj\n", pages)
	for i := range pages {
		fmt.Fprintf(&sb, "%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>\nendobj\n", i+3)
	}
	fmt.Fprintf(&sb, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n0\n%%%%EOF\n", pages+3)
	return sb.String()
}

// frontMatterNums labels a six-page document the way a book does: pages 0-3 are front matter numbered in lowercase
// roman (i, ii, iii, iv) and pages 4-5 restart the numbering in decimal (1, 2). It is the shape the whole page-label
// API exists for, since it is exactly the case where a page's label is not its position.
const frontMatterNums = "0 << /S /r >> 4 << /S /D >>"

// openPageLabelsDoc opens a document built by pageLabelsPDF and arranges for its release.
func openPageLabelsDoc(t *testing.T, nums string, pages int) *pdfview.Document {
	t.Helper()
	doc, err := pdfview.New([]byte(pageLabelsPDF(nums, pages)), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(doc.Release)
	return doc
}

// TestPageLabels pins the forward direction of the API against a document whose labels restart mid-way: each page's
// label, the whole-document slice, and the out-of-range answers.
func TestPageLabels(t *testing.T) {
	doc := openPageLabelsDoc(t, frontMatterNums, 6)
	if !doc.HasPageLabels() {
		t.Error("HasPageLabels = false for a document that defines /PageLabels, want true")
	}
	want := []string{"i", "ii", "iii", "iv", "1", "2"}
	if got := doc.PageLabels(); !slices.Equal(got, want) {
		t.Errorf("PageLabels = %q, want %q", got, want)
	}
	for pageNumber, label := range want {
		if got := doc.PageLabel(pageNumber); got != label {
			t.Errorf("PageLabel(%d) = %q, want %q", pageNumber, got, label)
		}
	}
	// A page number outside the document answers with the empty string rather than panicking or wrapping around.
	for _, pageNumber := range []int{-1, 6, 1 << 30} {
		if got := doc.PageLabel(pageNumber); got != "" {
			t.Errorf("PageLabel(%d) = %q, want an empty string", pageNumber, got)
		}
	}
}

// TestPagesWithLabel pins the reverse direction: the matching rules (case folding, sanitization of the query) and the
// answers for a query that matches nothing at all.
func TestPagesWithLabel(t *testing.T) {
	doc := openPageLabelsDoc(t, frontMatterNums, 6)
	for _, tc := range []struct {
		name  string
		query string
		want  []int
	}{
		{name: "exact", query: "iv", want: []int{3}},
		{name: "case folded", query: "IV", want: []int{3}},
		{name: "mixed case", query: "iI", want: []int{1}},
		{name: "whitespace padded", query: "  iv\t\n", want: []int{3}},
		{name: "decimal range", query: "2", want: []int{5}},
		{name: "miss", query: "xiv", want: nil},
		// A page's ordinal is not its label here, so the physical numbering must not answer for the labels: page 3 is
		// labeled "iv", and nothing in the document is labeled "4".
		{name: "ordinal is not a label", query: "4", want: nil},
		{name: "empty", query: "", want: nil},
		{name: "whitespace only", query: " \t ", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := doc.PagesWithLabel(tc.query); !slices.Equal(got, tc.want) {
				t.Errorf("PagesWithLabel(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestPagesWithLabelReportsEveryMatch pins that a repeated label reports all of its pages, ascending. Two ranges that
// both number from 1 — the appendix-restarts-at-1 shape real documents have — put the same label on two pages, and a
// caller resolving a label to a page needs to see both rather than whichever one the walk happened to reach first.
func TestPagesWithLabelReportsEveryMatch(t *testing.T) {
	doc := openPageLabelsDoc(t, "0 << /S /D >> 3 << /S /D >>", 6)
	want := []string{"1", "2", "3", "1", "2", "3"}
	if got := doc.PageLabels(); !slices.Equal(got, want) {
		t.Fatalf("PageLabels = %q, want %q", got, want)
	}
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{query: "1", want: []int{0, 3}},
		{query: "3", want: []int{2, 5}},
	} {
		if got := doc.PagesWithLabel(tc.query); !slices.Equal(got, tc.want) {
			t.Errorf("PagesWithLabel(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

// TestPageLabelsFallBackToOrdinals pins the fallback for a document that defines no /PageLabels at all: every page is
// labeled with its own decimal ordinal, those labels take part in the reverse lookup like any other, and HasPageLabels
// is what tells the caller the labels came from the fallback rather than the document.
func TestPageLabelsFallBackToOrdinals(t *testing.T) {
	doc := openPageLabelsDoc(t, "", 3)
	if doc.HasPageLabels() {
		t.Error("HasPageLabels = true for a document with no /PageLabels, want false")
	}
	want := []string{"1", "2", "3"}
	if got := doc.PageLabels(); !slices.Equal(got, want) {
		t.Errorf("PageLabels = %q, want %q", got, want)
	}
	if got := doc.PageLabel(1); got != "2" {
		t.Errorf("PageLabel(1) = %q, want \"2\"", got)
	}
	if got := doc.PagesWithLabel("2"); !slices.Equal(got, []int{1}) {
		t.Errorf("PagesWithLabel(\"2\") = %v, want [1]", got)
	}
}

// TestPageLabelsCopiesTheEngineCache pins that PageLabels hands out a fresh slice every call. The engine builds the
// labels once and caches them for the life of the document, so returning that slice would let a caller who sorts or
// rewrites its copy change what every later call — and every PagesWithLabel lookup — reports.
func TestPageLabelsCopiesTheEngineCache(t *testing.T) {
	doc := openPageLabelsDoc(t, frontMatterNums, 6)
	first := doc.PageLabels()
	if len(first) != 6 {
		t.Fatalf("PageLabels returned %d labels, want 6", len(first))
	}
	for i := range first {
		first[i] = "clobbered"
	}
	want := []string{"i", "ii", "iii", "iv", "1", "2"}
	if got := doc.PageLabels(); !slices.Equal(got, want) {
		t.Errorf("PageLabels after mutating an earlier result = %q, want %q", got, want)
	}
	if got := doc.PageLabel(0); got != "i" {
		t.Errorf("PageLabel(0) after mutating an earlier result = %q, want \"i\"", got)
	}
	if got := doc.PagesWithLabel("iv"); !slices.Equal(got, []int{3}) {
		t.Errorf("PagesWithLabel(\"iv\") after mutating an earlier result = %v, want [3]", got)
	}
}
