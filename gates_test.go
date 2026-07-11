// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview_test

import "testing"

// milestone is the highest milestone of the pure-Go engine port that has been completed (see plan.md). The
// pre-existing tests in pdf_test.go are gated on the milestone that wires up the functionality they exercise. At M8
// the gate calls and this file are removed, restoring pdf_test.go to its original, ungated state.
const milestone = "M7"

// gate skips t unless the current milestone is at or beyond the required one.
func gate(t *testing.T, required string) {
	t.Helper()
	order := map[string]int{"M0": 0, "M1": 1, "M2": 2, "M3": 3, "M4": 4, "M5": 5, "M6": 6, "M7": 7, "M8": 8}
	if order[milestone] < order[required] {
		t.Skipf("gated until milestone %s is complete (current: %s); see plan.md", required, milestone)
	}
}
