// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos

import (
	"bytes"
	"compress/zlib"
	"errors"
	"math"
	"testing"

	"github.com/richardwilkes/pdfview/internal/filter"
)

// The /DecodeParms keys filterParams reads, hoisted so the tables below and the accessor share one spelling.
const (
	predictorKey   Name = "Predictor"
	colorsKey      Name = "Colors"
	bitsKey        Name = "BitsPerComponent"
	columnsKey     Name = "Columns"
	earlyChangeKey Name = "EarlyChange"
)

// paramValue returns the field of p that key feeds.
func paramValue(p filter.Params, key Name) int {
	switch key {
	case predictorKey:
		return p.Predictor
	case colorsKey:
		return p.Colors
	case bitsKey:
		return p.BitsPerComponent
	case columnsKey:
		return p.Columns
	case earlyChangeKey:
		return p.EarlyChange
	default:
		return 0
	}
}

// TestFilterParamsPassThroughLegalValues checks that ordinary /DecodeParms values reach internal/filter untouched: the
// out-of-range guard must not disturb anything a real file carries.
func TestFilterParamsPassThroughLegalValues(t *testing.T) {
	d := &Document{}
	params := d.filterParams(Dict{
		predictorKey:   Integer(15),
		colorsKey:      Integer(3),
		bitsKey:        Integer(16),
		columnsKey:     Integer(1 << 24),
		earlyChangeKey: Integer(0),
	})
	want := filter.Params{Predictor: 15, Colors: 3, BitsPerComponent: 16, Columns: 1 << 24, EarlyChange: 0}
	if params != want {
		t.Errorf("filterParams = %+v, want %+v", params, want)
	}
	if def := d.filterParams(nil); def != filter.DefaultParams() {
		t.Errorf("filterParams(nil) = %+v, want the defaults", def)
	}
}

// TestFilterParamsRejectsTruncatingValues checks that a /DecodeParms value too large for an int on a 32-bit build
// (GOARCH=386/arm, which this package's row-length and sample-index arithmetic explicitly cares about) is not narrowed
// by an unchecked conversion. Each value below has a legal low 32 bits, so a plain int(v) would hand internal/filter a
// plausible-looking parameter — /Columns 4294967297 becomes 1 — and the predictor would then decode with a silently
// wrong row length. The saturated result must instead stay out of range on every architecture, which is checked here
// both directly and by driving the real filter chain with it.
func TestFilterParamsRejectsTruncatingValues(t *testing.T) {
	const wrap = int64(1) << 32
	for _, tc := range []struct {
		key       Name
		value     int64
		truncated int
	}{
		{key: predictorKey, value: wrap + 12, truncated: 12},
		{key: colorsKey, value: wrap + 3, truncated: 3},
		{key: bitsKey, value: wrap + 8, truncated: 8},
		{key: columnsKey, value: wrap + 1, truncated: 1},
		{key: earlyChangeKey, value: wrap, truncated: 0},
	} {
		t.Run(string(tc.key), func(t *testing.T) {
			d := &Document{}
			parms := Dict{tc.key: Integer(tc.value)}
			if tc.key != predictorKey {
				parms[predictorKey] = Integer(12) // Engage a PNG predictor so the sample-layout validation runs.
			}
			params := d.filterParams(parms)
			got := paramValue(params, tc.key)
			if got == tc.truncated {
				t.Fatalf("/%s %d narrowed to %d, the value a 32-bit truncation produces", tc.key, tc.value, got)
			}
			if got != maxFilterParam {
				t.Fatalf("/%s %d = %d, want it saturated at %d", tc.key, tc.value, got, maxFilterParam)
			}
			if int64(int32(got)) != int64(got) {
				t.Fatalf("/%s saturated to %d, which is not representable in an int on a 32-bit build", tc.key, got)
			}
			// /EarlyChange only selects between the two LZW code-width conventions, so there is nothing downstream to
			// reject it; the guarantee is that it does not collapse to the non-default 0.
			if tc.key == earlyChangeKey {
				return
			}
			if _, err := filter.DecodeChain([]filter.Spec{{Name: "FlateDecode", Params: params}},
				deflated(t, make([]byte, 64))); !errors.Is(err, filter.ErrUnsupportedFilter) {
				t.Errorf("decoding with a saturated /%s = %v, want %v", tc.key, err, filter.ErrUnsupportedFilter)
			}
		})
	}
}

// TestClampFilterParamExtremes checks the saturation at both ends of the int64 range.
func TestClampFilterParamExtremes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want int
	}{
		{in: math.MaxInt64, want: maxFilterParam},
		{in: math.MinInt64, want: -maxFilterParam},
		{in: maxFilterParam, want: maxFilterParam},
		{in: -maxFilterParam, want: -maxFilterParam},
		{in: 0, want: 0},
		{in: -1, want: -1},
	} {
		if got := clampFilterParam(tc.in); got != tc.want {
			t.Errorf("clampFilterParam(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// deflated returns data compressed with zlib, the wire format /FlateDecode expects.
func deflated(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatalf("deflate: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("deflate close: %v", err)
	}
	return buf.Bytes()
}
