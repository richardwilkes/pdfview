// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos

import "testing"

// TestParseObjStmClampsHeaderCount checks that a hostile /N cannot drive the header slice allocations beyond what the
// decoded payload could possibly hold: every header pair needs at least three bytes, so the capacities stay within
// len(data)/3 no matter what /N claims. The entries that really are present must still parse.
func TestParseObjStmClampsHeaderCount(t *testing.T) {
	payload := []byte("7 0 9 4\n(a)\n(b)")
	for _, n := range []int64{2, 1 << 40} {
		d := &Document{}
		stm, err := d.parseObjStm(&Stream{
			Dict: Dict{"N": Integer(n), "First": Integer(8)},
			Raw:  payload,
		})
		if err != nil {
			t.Fatalf("/N %d: %v", n, err)
		}
		if bound := len(payload) / 3; cap(stm.nums) > bound || cap(stm.offs) > bound {
			t.Errorf("/N %d: capacities %d/%d exceed the %d-pair bound for %d payload bytes", n, cap(stm.nums),
				cap(stm.offs), bound, len(payload))
		}
		if len(stm.nums) != 2 || stm.nums[0] != 7 || stm.nums[1] != 9 {
			t.Errorf("/N %d: object numbers = %v, want [7 9]", n, stm.nums)
		}
		if len(stm.offs) != 2 || stm.offs[0] != 0 || stm.offs[1] != 4 {
			t.Errorf("/N %d: offsets = %v, want [0 4]", n, stm.offs)
		}
	}
}
