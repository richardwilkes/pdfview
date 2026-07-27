// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package filter

import (
	"testing"
)

func TestPredictorRowLen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		p       Params
		dataLen int
		want    int
	}{
		{name: "8-bit RGB", p: Params{Colors: 3, BitsPerComponent: 8, Columns: 16}, dataLen: 1 << 20, want: 48},
		{name: "16-bit gray", p: Params{Colors: 1, BitsPerComponent: 16, Columns: 8}, dataLen: 1 << 20, want: 16},
		{name: "sub-byte rounds up", p: Params{Colors: 5, BitsPerComponent: 2, Columns: 4}, dataLen: 1 << 20, want: 5},
		{name: "clamped to data", p: Params{Colors: 3, BitsPerComponent: 8, Columns: 16}, dataLen: 7, want: 7},
		{name: "empty data", p: Params{Colors: 3, BitsPerComponent: 8, Columns: 16}, dataLen: 0, want: 0},
		// The largest layout validatePredictorParams accepts. Its untruncated row length is 2^34 bytes, so only the
		// clamp keeps make() and the row loops working from a length the data can actually supply.
		{name: "maximum layout", p: Params{Colors: 64, BitsPerComponent: 16, Columns: 1 << 24}, dataLen: 10, want: 10},
		// The 16-bit TIFF worst case: 2 * 64 * 2^24 is exactly 2^31.
		{
			name:    "maximum 16-bit TIFF layout",
			p:       Params{Colors: 64, BitsPerComponent: 16, Columns: 1 << 24},
			dataLen: 1 << 20,
			want:    1 << 20,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePredictorParams(tc.p); err != nil {
				t.Fatalf("params should be valid: %v", err)
			}
			if got := predictorRowLen(tc.p, tc.dataLen); got != tc.want {
				t.Errorf("predictorRowLen(%+v, %d) = %d, want %d", tc.p, tc.dataLen, got, tc.want)
			}
		})
	}
}
