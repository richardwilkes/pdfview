// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package imaging

import (
	"bytes"
	"io"

	"golang.org/x/image/ccitt"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// decodeCCITT expands a CCITTFaxDecode payload to packed one-bit rows (byte-aligned, MSB first) at the decode
// parameters' column count, h rows tall. The output bit convention matches PDF's decoded-data contract directly: with
// BlackIs1 false (the default) a 0 bit is black, with BlackIs1 true a 1 bit is black — the /Decode array and color
// space then interpret the bits like any other 1-bpc samples. K selects the coding scheme: negative is pure
// two-dimensional (Group 4), zero is one-dimensional (Group 3); positive (mixed one/two-dimensional Group 3) is
// attempted as Group 3, whose two-dimensional lines then terminate the decode early — the truncation leniency below
// completes the image with white. Truncated or damaged payloads keep the rows that decoded; the remainder is filled
// with white, the way deployed viewers degrade.
func (dec *decoder) decodeCCITT(h int) (data []byte, cols int, err error) {
	k := int64(0)
	cols = 1728
	align := false
	black1 := false
	if dec.parms != nil {
		if v, ok := dec.d.GetInt(dec.parms, "K"); ok {
			k = v
		}
		if v, ok := dec.d.GetInt(dec.parms, "Columns"); ok && v > 0 {
			cols = int(v)
		}
		align = dictBool(dec.d, dec.parms, "EncodedByteAlign")
		black1 = dictBool(dec.d, dec.parms, "BlackIs1")
	}
	if int64(cols)*int64(h) > maxPixelsFor(len(dec.data)) {
		return nil, 0, ErrTooLarge
	}
	sf := ccitt.Group3
	if k < 0 {
		sf = ccitt.Group4
	}
	rowBytes := (cols + 7) / 8
	out := make([]byte, rowBytes*h)
	// The x/image reader emits 1 for white and 0 for black; its Invert option flips that, which is exactly BlackIs1's
	// contract for the decoded data.
	r := ccitt.NewReader(bytes.NewReader(dec.data), ccitt.MSB, sf, cols, h, &ccitt.Options{Align: align, Invert: black1})
	n, _ := io.ReadFull(r, out) //nolint:errcheck // Partial output is kept; the remainder is filled below.
	fill := byte(0xff)          // 1 bits: white under the default convention.
	if black1 {
		fill = 0x00 // Inverted output: white is 0.
	}
	for i := n; i < len(out); i++ {
		out[i] = fill
	}
	return out, cols, nil
}

// dictBool resolves dict[key] as a boolean, false when absent or not a boolean.
func dictBool(d *cos.Document, dict cos.Dict, key cos.Name) bool {
	b, ok := d.Resolve(dict[key]).(cos.Boolean)
	return ok && bool(b)
}
