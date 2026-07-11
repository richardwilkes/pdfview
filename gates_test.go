package pdfview_test

import "testing"

// milestone is the highest milestone of the pure-Go engine port that has been completed (see plan.md). The
// pre-existing tests in pdf_test.go are gated on the milestone that wires up the functionality they exercise. At M8
// the gate calls and this file are removed, restoring pdf_test.go to its original, ungated state.
const milestone = "M1"

// gate skips t unless the current milestone is at or beyond the required one.
func gate(t *testing.T, required string) {
	t.Helper()
	order := map[string]int{"M0": 0, "M1": 1, "M2": 2, "M3": 3, "M4": 4, "M5": 5, "M6": 6, "M7": 7, "M8": 8}
	if order[milestone] < order[required] {
		t.Skipf("gated until milestone %s is complete (current: %s); see plan.md", required, milestone)
	}
}
