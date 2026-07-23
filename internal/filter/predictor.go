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
	"fmt"
)

// applyPredictor reverses the predictor transform named by p on decompressed data. Predictor 1 (or less, treated as 1)
// is a no-op, 2 is TIFF horizontal differencing, and 10-15 are the PNG filters (each row carries its own filter-type
// byte, so the specific value does not matter on decode). data is owned by the caller's decode stage and may be
// modified in place; the result is capped at maxSize bytes.
func applyPredictor(p Params, data []byte, maxSize int) ([]byte, error) {
	switch {
	case p.Predictor <= 1:
		return data, nil
	case p.Predictor == 2:
		return tiffPredictor(p, data)
	case p.Predictor >= 10 && p.Predictor <= 15:
		return pngPredictor(p, data, maxSize)
	default:
		return nil, fmt.Errorf("%w: predictor %d", ErrUnsupportedFilter, p.Predictor)
	}
}

// validatePredictorParams bounds the sample-layout parameters so row-length arithmetic cannot overflow and hostile
// parameters cannot force absurd allocations.
func validatePredictorParams(p Params) error {
	if p.Colors < 1 || p.Colors > 64 {
		return fmt.Errorf("%w: predictor with %d colors", ErrUnsupportedFilter, p.Colors)
	}
	switch p.BitsPerComponent {
	case 1, 2, 4, 8, 16:
	default:
		return fmt.Errorf("%w: predictor with %d bits per component", ErrUnsupportedFilter, p.BitsPerComponent)
	}
	if p.Columns < 1 || p.Columns > 1<<24 {
		return fmt.Errorf("%w: predictor with %d columns", ErrUnsupportedFilter, p.Columns)
	}
	return nil
}

// pngPredictor reverses the PNG row filters (RFC 2083 section 6): every row is one filter-type byte followed by the
// filtered row bytes. A truncated final row is processed as far as the data goes.
func pngPredictor(p Params, data []byte, maxSize int) ([]byte, error) {
	if err := validatePredictorParams(p); err != nil {
		return nil, err
	}
	rowLen := (p.Colors*p.BitsPerComponent*p.Columns + 7) / 8
	// The number of bytes per complete pixel, rounded up to at least one, per the PNG specification's filtering model.
	// A single sub-byte pixel rounds up to 1, but a multi-component sub-byte config (e.g. 5 colors * 2 bits) spans
	// more than one byte, so round the whole pixel width up rather than flooring per component.
	bpp := max(1, (p.Colors*p.BitsPerComponent+7)/8)
	// A row cannot be longer than the data itself; clamping keeps hostile Columns values from forcing large allocations
	// for a file that does not actually contain such rows.
	rowLen = min(rowLen, len(data))
	if rowLen == 0 {
		return nil, nil
	}
	nrows := (len(data) + rowLen) / (rowLen + 1)
	if int64(nrows)*int64(rowLen) > int64(maxSize) {
		return nil, ErrTooLarge
	}
	out := make([]byte, 0, nrows*rowLen)
	prev := make([]byte, rowLen)
	row := make([]byte, rowLen)
	pos := 0
	for pos < len(data) {
		filterType := data[pos]
		pos++
		n := min(rowLen, len(data)-pos)
		copy(row, data[pos:pos+n])
		// Zero-fill the remainder of a truncated final row so the filter arithmetic below stays in bounds; only the n
		// bytes actually present are emitted.
		for i := n; i < rowLen; i++ {
			row[i] = 0
		}
		pos += n
		switch filterType {
		case 0: // None
		case 1: // Sub
			for i := bpp; i < rowLen; i++ {
				row[i] += row[i-bpp]
			}
		case 2: // Up
			for i := range rowLen {
				row[i] += prev[i]
			}
		case 3: // Average
			for i := range rowLen {
				left := 0
				if i >= bpp {
					left = int(row[i-bpp])
				}
				row[i] += byte((left + int(prev[i])) / 2)
			}
		case 4: // Paeth
			for i := range rowLen {
				var left, upLeft byte
				if i >= bpp {
					left = row[i-bpp]
					upLeft = prev[i-bpp]
				}
				row[i] += paeth(left, prev[i], upLeft)
			}
		default:
			return nil, fmt.Errorf("%w: PNG filter type %d", ErrUnsupportedFilter, filterType)
		}
		out = append(out, row[:n]...)
		prev, row = row, prev
	}
	return out, nil
}

// paeth is the PNG Paeth prediction function (RFC 2083 section 6.6).
func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa := abs(p - int(a))
	pb := abs(p - int(b))
	pc := abs(p - int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	default:
		return c
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// tiffPredictor reverses TIFF predictor 2 (horizontal differencing) in place. Only 8- and 16-bit components are
// supported; sub-byte TIFF differencing is vanishingly rare in real documents and is rejected.
func tiffPredictor(p Params, data []byte) ([]byte, error) {
	if err := validatePredictorParams(p); err != nil {
		return nil, err
	}
	switch p.BitsPerComponent {
	case 8:
		rowLen := p.Colors * p.Columns
		for r := 0; r < len(data); r += rowLen {
			end := min(r+rowLen, len(data))
			for i := r + p.Colors; i < end; i++ {
				data[i] += data[i-p.Colors]
			}
		}
	case 16:
		rowLen := 2 * p.Colors * p.Columns
		stride := 2 * p.Colors
		for r := 0; r < len(data); r += rowLen {
			end := min(r+rowLen, len(data))
			for i := r + stride; i+1 < end; i += 2 {
				v := uint16(data[i])<<8 | uint16(data[i+1])
				left := uint16(data[i-stride])<<8 | uint16(data[i-stride+1])
				v += left
				data[i] = byte(v >> 8)
				data[i+1] = byte(v)
			}
		}
	default:
		return nil, fmt.Errorf("%w: TIFF predictor with %d bits per component", ErrUnsupportedFilter,
			p.BitsPerComponent)
	}
	return data, nil
}
