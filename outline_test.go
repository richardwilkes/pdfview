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
	"testing"

	"github.com/richardwilkes/pdfview/internal/doc"
)

// TestConvertOutlineNextCycleTerminates pins that convertOutline's visited set cuts a Next cycle.
func TestConvertOutlineNextCycleTerminates(t *testing.T) {
	a := &doc.OutlineItem{Title: "a", Page: 0}
	b := &doc.OutlineItem{Title: "b", Page: 1}
	a.Next = b
	b.Next = a

	root := convertOutline(a)

	var titles []string
	for node := root; node != nil; node = node.next {
		titles = append(titles, node.title)
	}
	if len(titles) != 2 || titles[0] != "a" || titles[1] != "b" {
		t.Fatalf("Next cycle not cut cleanly: got %v", titles)
	}
}

// TestConvertOutlineDownCycleTerminates pins that a Down pointer revisiting an ancestor is cut.
func TestConvertOutlineDownCycleTerminates(t *testing.T) {
	a := &doc.OutlineItem{Title: "a", Page: 0}
	a.Down = a

	root := convertOutline(a)
	if root == nil {
		t.Fatal("expected a converted root")
	}
	if root.down != nil {
		t.Fatalf("Down cycle not cut: root.down = %+v", root.down)
	}
}

// TestConvertOutlineDepthCapped pins that maxOutlineConvertDepth bounds a Down chain of fresh nodes, which no visited
// set can cut.
func TestConvertOutlineDepthCapped(t *testing.T) {
	head := &doc.OutlineItem{Title: "0", Page: 0}
	cur := head
	const levels = maxOutlineConvertDepth + 50
	for i := 1; i <= levels; i++ {
		child := &doc.OutlineItem{Title: "x", Page: i}
		cur.Down = child
		cur = child
	}

	root := convertOutline(head)

	depth := 0
	for node := root; node != nil; node = node.down {
		depth++
	}
	if depth > maxOutlineConvertDepth+1 {
		t.Fatalf("depth not capped: walked %d levels, cap is %d", depth, maxOutlineConvertDepth)
	}
	if depth == 0 {
		t.Fatal("expected at least the root level")
	}
}

// TestBuildTOCEntriesCyclicOutlineTerminates pins that the maxAllowed budget stops buildTOCEntries on a cyclic
// outlineNode tree.
func TestBuildTOCEntriesCyclicOutlineTerminates(t *testing.T) {
	a := &outlineNode{title: "a"}
	b := &outlineNode{title: "b"}
	a.next = b
	b.next = a

	entries, _ := buildTOCEntries(a, 1, OverallMaxTOCEntries)
	if len(entries) != OverallMaxTOCEntries {
		t.Fatalf("cyclic outline not bounded by budget: got %d entries, want %d", len(entries), OverallMaxTOCEntries)
	}
}
