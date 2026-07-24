// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build !race

package render

// maskMissAllocCeiling is the most allocations one glyph coverage-cache miss may make (see
// TestGlyphMaskMissAllocationsBounded). A miss allocates the coverage plane, the mask that owns it and the cache's own
// bookkeeping — about 2.5 on average — while the pre-reuse code allocated about 8.5.
const maskMissAllocCeiling = 4
