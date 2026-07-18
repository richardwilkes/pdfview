// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package filter_test

import (
	"testing"

	"github.com/richardwilkes/pdfview/internal/filter"
)

// FuzzFilters drives every filter and predictor combination with arbitrary data and parameters. Decoding hostile input
// must never panic, and output must respect the size cap.
func FuzzFilters(f *testing.F) {
	f.Add([]byte("48656C6C6F>"), uint8(2), uint8(1), uint8(1), uint8(8), uint16(1), uint8(1))
	f.Add([]byte("87cUR@<Q~>"), uint8(3), uint8(1), uint8(1), uint8(8), uint16(1), uint8(1))
	f.Add([]byte{2, 'a', 'b', 'c', 255, 'x', 128}, uint8(4), uint8(1), uint8(1), uint8(8), uint16(1), uint8(1))
	f.Add([]byte{0x80, 0x0b, 0x60, 0x50, 0x22, 0x0c, 0x0c, 0x85, 0x01}, uint8(1), uint8(1), uint8(1), uint8(8), uint16(1), uint8(0))
	f.Add([]byte{0x80, 0x0b, 0x60, 0x50, 0x22, 0x0c, 0x0c, 0x85, 0x01}, uint8(1), uint8(1), uint8(1), uint8(8), uint16(1), uint8(1))
	f.Add(zlibHello, uint8(0), uint8(1), uint8(1), uint8(8), uint16(4), uint8(1))
	f.Add(zlibHello, uint8(0), uint8(12), uint8(3), uint8(8), uint16(4), uint8(1))
	f.Add(zlibHello, uint8(0), uint8(2), uint8(3), uint8(16), uint16(2), uint8(1))
	names := []string{"FlateDecode", "LZWDecode", "ASCIIHexDecode", "ASCII85Decode", "RunLengthDecode"}
	f.Fuzz(func(t *testing.T, data []byte, which, predictor, colors, bpc uint8, columns uint16, early uint8) {
		s := filter.Spec{
			Name: names[int(which)%len(names)],
			Params: filter.Params{
				Predictor:        int(predictor),
				Colors:           int(colors),
				BitsPerComponent: int(bpc),
				Columns:          int(columns),
				EarlyChange:      int(early) % 2,
			},
		}
		limit := filter.MaxDecodedSize(len(data))
		out, err := filter.Decode(s, data, limit)
		if err == nil && len(out) > limit {
			t.Fatalf("decoded %d bytes, over the %d-byte cap", len(out), limit)
		}
	})
}

// zlibHello is a small zlib stream ("hello hello hello\n" compressed) used as a seed.
var zlibHello = []byte{
	0x78, 0x9c, 0xca, 0x48, 0xcd, 0xc9, 0xc9, 0x57, 0xc8, 0x40, 0x27, 0x70, 0x01, 0x00, 0x00, 0x00,
	0xff, 0xff, 0x01, 0x00, 0x00, 0xff, 0xff, 0x47, 0x1d, 0x06, 0xba,
}
