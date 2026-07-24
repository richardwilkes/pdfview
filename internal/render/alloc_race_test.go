// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build race

package render

// maskMissAllocCeiling is the most allocations one glyph coverage-cache miss may make (see
// TestGlyphMaskMissAllocationsBounded). The race detector's own bookkeeping roughly triples the count — a miss measures
// about 8 under it against 2.5 without — so the ceiling is raised to match, keeping the same margin over the pre-reuse
// code, which measures about 14.5 under the detector.
const maskMissAllocCeiling = 11
