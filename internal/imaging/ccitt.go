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
// parameters' column count, h rows tall. The bit convention is PDF's decoded-data contract: with BlackIs1 false (the
// default) a 0 bit is black, with BlackIs1 true a 1 bit is black. K selects the scheme: negative is Group 4, zero is
// one-dimensional Group 3; positive (mixed Group 3) is attempted as Group 3, whose two-dimensional lines then end the
// decode early. Truncated or damaged payloads keep the rows that decoded and fill the remainder with white, the way
// deployed viewers degrade.
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
			c, inRange := decodeParmDim(v)
			if !inRange {
				return nil, 0, ErrTooLarge
			}
			cols = c
		}
		align = dictBool(dec.d, dec.parms, "EncodedByteAlign")
		black1 = dictBool(dec.d, dec.parms, "BlackIs1")
	}
	// Bound each dimension before multiplying, as run does: an unbounded cols×h could overflow int64 and slip under
	// the budget check.
	if cols > maxImagePixels || h > maxImagePixels || int64(cols)*int64(h) > maxPixelsFor(len(dec.data)) {
		return nil, 0, ErrTooLarge
	}
	sf := ccitt.Group3
	if k < 0 {
		sf = ccitt.Group4
	}
	rowBytes := (cols + 7) / 8
	out := make([]byte, rowBytes*h)
	// The x/image reader emits 1 for white and 0 for black; Invert flips that, which is exactly BlackIs1's contract.
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
