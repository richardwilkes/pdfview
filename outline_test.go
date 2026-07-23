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

// TestConvertOutlineNextCycleTerminates verifies convertOutline does not loop forever when the engine hands back a
// sibling chain whose Next pointer forms a cycle — the visited set must cut it.
func TestConvertOutlineNextCycleTerminates(t *testing.T) {
	a := &doc.OutlineItem{Title: "a", Page: 0}
	b := &doc.OutlineItem{Title: "b", Page: 1}
	a.Next = b
	b.Next = a // cycle back to the head

	root := convertOutline(a)

	// Each distinct source node appears exactly once; the cycle is cut rather than repeated.
	var titles []string
	for node := root; node != nil; node = node.next {
		titles = append(titles, node.title)
	}
	if len(titles) != 2 || titles[0] != "a" || titles[1] != "b" {
		t.Fatalf("Next cycle not cut cleanly: got %v", titles)
	}
}

// TestConvertOutlineDownCycleTerminates verifies a Down pointer that revisits an ancestor is cut instead of recursing
// forever.
func TestConvertOutlineDownCycleTerminates(t *testing.T) {
	a := &doc.OutlineItem{Title: "a", Page: 0}
	a.Down = a // child points back at itself

	root := convertOutline(a)
	if root == nil {
		t.Fatal("expected a converted root")
	}
	if root.down != nil {
		t.Fatalf("Down cycle not cut: root.down = %+v", root.down)
	}
}

// TestConvertOutlineDepthCapped verifies a pathologically deep Down chain is bounded by maxOutlineConvertDepth rather
// than overflowing the stack. Each level is a fresh node (no cycle), so only the depth cap can stop it.
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

// TestBuildTOCEntriesCyclicOutlineTerminates verifies buildTOCEntries stays bounded even if handed a cyclic outlineNode
// tree directly: the maxAllowed budget must stop the walk instead of looping forever.
func TestBuildTOCEntriesCyclicOutlineTerminates(t *testing.T) {
	a := &outlineNode{title: "a"}
	b := &outlineNode{title: "b"}
	a.next = b
	b.next = a // Next cycle

	entries, _ := buildTOCEntries(a, 1, OverallMaxTOCEntries)
	if len(entries) != OverallMaxTOCEntries {
		t.Fatalf("cyclic outline not bounded by budget: got %d entries, want %d", len(entries), OverallMaxTOCEntries)
	}
}
