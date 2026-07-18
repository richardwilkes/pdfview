// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package filter implements the non-image PDF stream filters needed to decode document data — FlateDecode, LZWDecode,
// ASCIIHexDecode, ASCII85Decode, and RunLengthDecode — together with the PNG and TIFF predictor transforms and bounded
// chain application. The image-only filters (DCTDecode, CCITTFaxDecode, JBIG2Decode, JPXDecode) are handled by
// internal/imaging at rasterization time and are rejected here, as is the Crypt filter (internal/cos strips Identity
// crypt filters before building a chain and rejects named ones; document-level encryption is undone at parse time by
// internal/crypt).
//
// Decoding enforces two caps so hostile input cannot force unbounded work: a chain may apply at most MaxChainLength
// filters, and each stage's output may not exceed MaxDecodedSize(len(input)) bytes. Termination is guaranteed by these
// caps; there are no timeouts.
//
// Decoding is otherwise deliberately fault-tolerant, matching the warn-and-continue behavior of widely deployed PDF
// readers: corrupt input that still yields some output returns that partial output without an error. Resource-limit
// violations are always hard errors.
package filter

import (
	"bytes"
	"compress/flate"
	"compress/lzw"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"fmt"
	"io"
	"math"

	tifflzw "golang.org/x/image/tiff/lzw"
)

// Errors returned by this package.
var (
	ErrChainTooLong      = errors.New("filter chain is too long")
	ErrTooLarge          = errors.New("decoded stream exceeds the size limit")
	ErrUnsupportedFilter = errors.New("unsupported filter")
)

// MaxChainLength is the maximum number of filters DecodeChain applies. The spec places no limit on chain length, but no
// legitimate producer chains more than two or three filters; the cap stops hostile input from forcing unbounded
// decompression rounds.
const MaxChainLength = 8

// PDF filter names, including the abbreviated forms the spec permits for inline images. The abbreviations are accepted
// everywhere as a harmless leniency.
const (
	nameFlate      = "FlateDecode"
	nameFlateAbbr  = "Fl"
	nameLZW        = "LZWDecode"
	nameLZWAbbr    = "LZW"
	nameASCIIHex   = "ASCIIHexDecode"
	nameASCIIHexAb = "AHx"
	nameASCII85    = "ASCII85Decode"
	nameASCII85Ab  = "A85"
	nameRunLength  = "RunLengthDecode"
	nameRunLenAbbr = "RL"
)

// Params holds the decode parameters (from a stream's /DecodeParms dictionary) that the filters in this package
// consume. The zero value is not meaningful; start from DefaultParams.
type Params struct {
	// Predictor selects the predictor transform applied after Flate or LZW decoding: 1 = none, 2 = TIFF horizontal
	// differencing, 10-15 = the PNG filters (the specific value is irrelevant on decode; each row carries its own PNG
	// filter type byte).
	Predictor int
	// Colors is the number of interleaved color components per sample (predictor transforms only).
	Colors int
	// BitsPerComponent is the number of bits per color component (predictor transforms only).
	BitsPerComponent int
	// Columns is the number of samples per row (predictor transforms only).
	Columns int
	// EarlyChange selects the LZW code-width change convention: 1 (the default) increases the code width one code
	// early, 0 increases it at the standard point.
	EarlyChange int
}

// DefaultParams returns Params with the defaults ISO 32000-2 assigns when /DecodeParms omits a key.
func DefaultParams() Params {
	return Params{
		Predictor:        1,
		Colors:           1,
		BitsPerComponent: 8,
		Columns:          1,
		EarlyChange:      1,
	}
}

// Spec names one filter application within a chain.
type Spec struct {
	// Name is the PDF filter name, e.g. "FlateDecode". Abbreviated inline-image names are accepted too.
	Name string
	// Params holds the decode parameters for this filter.
	Params Params
}

// MaxDecodedSize returns the largest output each decoding stage may produce for an original input of inputLen bytes:
// max(64 MB, 256 × inputLen). The generous fixed floor accommodates small streams that legitimately expand enormously
// (such as xref streams and bitmap data), while the multiplier scales the allowance for large inputs.
func MaxDecodedSize(inputLen int) int {
	const floor = 64 << 20
	if inputLen > math.MaxInt/256 {
		return math.MaxInt
	}
	return max(floor, 256*inputLen)
}

// DecodeChain applies each filter in specs to data in order, enforcing MaxChainLength and capping every stage's output
// at MaxDecodedSize(len(data)) bytes. It returns the fully decoded bytes. data is never modified; the result may alias
// it only when specs is empty.
func DecodeChain(specs []Spec, data []byte) ([]byte, error) {
	if len(specs) > MaxChainLength {
		return nil, ErrChainTooLong
	}
	budget := MaxDecodedSize(len(data))
	for i := range specs {
		var err error
		if data, err = Decode(specs[i], data, budget); err != nil {
			return nil, fmt.Errorf("filter %s: %w", specs[i].Name, err)
		}
	}
	return data, nil
}

// Decode applies a single filter to data, capping the output at maxSize bytes. The returned slice never aliases data.
// Decode owns and may modify its result buffers, but never data itself.
func Decode(spec Spec, data []byte, maxSize int) ([]byte, error) {
	if maxSize <= 0 {
		return nil, ErrTooLarge
	}
	var out []byte
	var err error
	switch spec.Name {
	case nameFlate, nameFlateAbbr:
		out, err = flateDecode(data, maxSize)
	case nameLZW, nameLZWAbbr:
		out, err = lzwDecode(data, maxSize, spec.Params.EarlyChange)
	case nameASCIIHex, nameASCIIHexAb:
		out, err = asciiHexDecode(data, maxSize)
	case nameASCII85, nameASCII85Ab:
		out, err = ascii85Decode(data, maxSize)
	case nameRunLength, nameRunLenAbbr:
		out, err = runLengthDecode(data, maxSize)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFilter, spec.Name)
	}
	if err != nil {
		return nil, err
	}
	switch spec.Name {
	case nameFlate, nameFlateAbbr, nameLZW, nameLZWAbbr:
		// Predictors apply only to the two compression filters, per ISO 32000-2 Table 8 and Table 10.
		return applyPredictor(spec.Params, out, maxSize)
	}
	return out, nil
}

// readCapped reads r to EOF, capping the output at maxSize bytes and returning ErrTooLarge when it would exceed the
// cap. A read error after at least one byte of output is swallowed and the partial output returned, matching the fault
// tolerance described in the package comment; an error before any output is reported.
func readCapped(r io.Reader, maxSize int) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, int64(maxSize)+1))
	if len(out) > maxSize {
		return nil, ErrTooLarge
	}
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return out, nil
}

// flateDecode inflates zlib-wrapped data, falling back to a raw DEFLATE stream when the zlib header is absent or
// unusable (a reasonably common defect in the wild).
func flateDecode(data []byte, maxSize int) ([]byte, error) {
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		out, rerr := readCapped(zr, maxSize)
		zr.Close() //nolint:errcheck // In-memory reader; the data has already been read.
		switch {
		case rerr == nil:
			return out, nil
		case errors.Is(rerr, ErrTooLarge):
			return nil, rerr
		}
		// The zlib header parsed but no data could be inflated; retry below as a raw DEFLATE stream.
	}
	fr := flate.NewReader(bytes.NewReader(data))
	out, err := readCapped(fr, maxSize)
	fr.Close() //nolint:errcheck // In-memory reader; the data has already been read.
	return out, err
}

// lzwDecode decompresses LZW data with the PDF flavor selected by earlyChange: the x/image/tiff/lzw reader implements
// the EarlyChange=1 (default) convention, and the standard library reader implements EarlyChange=0.
func lzwDecode(data []byte, maxSize, earlyChange int) ([]byte, error) {
	var r io.ReadCloser
	if earlyChange == 0 {
		r = lzw.NewReader(bytes.NewReader(data), lzw.MSB, 8)
	} else {
		r = tifflzw.NewReader(bytes.NewReader(data), tifflzw.MSB, 8)
	}
	out, err := readCapped(r, maxSize)
	r.Close() //nolint:errcheck // In-memory reader; the data has already been read.
	return out, err
}

// asciiHexDecode decodes pairs of hexadecimal digits. Whitespace and invalid characters are skipped (leniency), '>'
// terminates the data, and a trailing odd digit is treated as if followed by '0', per ISO 32000-2 7.4.2.
func asciiHexDecode(data []byte, maxSize int) ([]byte, error) {
	out := make([]byte, 0, min(len(data)/2+1, maxSize))
	var hi byte
	haveHi := false
	for _, c := range data {
		if c == '>' {
			break
		}
		v := hexDigit(c)
		if v == invalidHexDigit {
			continue
		}
		if haveHi {
			if len(out) >= maxSize {
				return nil, ErrTooLarge
			}
			out = append(out, hi<<4|v)
			haveHi = false
		} else {
			hi = v
			haveHi = true
		}
	}
	if haveHi {
		if len(out) >= maxSize {
			return nil, ErrTooLarge
		}
		out = append(out, hi<<4)
	}
	return out, nil
}

// invalidHexDigit is hexDigit's result for a byte that is not a hexadecimal digit.
const invalidHexDigit = 0xff

func hexDigit(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return invalidHexDigit
	}
}

// ascii85Decode decodes base-85 data up to the "~>" terminator. A leading "<~" (the PostScript-style opener some
// producers emit) is skipped. The standard library decoder ignores embedded whitespace.
func ascii85Decode(data []byte, maxSize int) ([]byte, error) {
	data = bytes.TrimLeft(data, "\x00\t\n\f\r ")
	data = bytes.TrimPrefix(data, []byte("<~"))
	if i := bytes.IndexByte(data, '~'); i >= 0 {
		data = data[:i]
	}
	return readCapped(ascii85.NewDecoder(bytes.NewReader(data)), maxSize)
}

// runLengthDecode expands run-length encoded data per ISO 32000-2 7.4.5: a length byte L of 0-127 copies the next L+1
// bytes literally, 129-255 repeats the next byte 257-L times, and 128 marks the end of data. Truncated input is
// tolerated, returning what was decoded.
func runLengthDecode(data []byte, maxSize int) ([]byte, error) {
	out := make([]byte, 0, min(len(data), maxSize))
	i := 0
	for i < len(data) {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return out, nil
		case n < 128:
			end := min(i+n+1, len(data))
			if len(out)+(end-i) > maxSize {
				return nil, ErrTooLarge
			}
			out = append(out, data[i:end]...)
			i = end
		default:
			if i >= len(data) {
				return out, nil
			}
			count := 257 - n
			if len(out)+count > maxSize {
				return nil, ErrTooLarge
			}
			out = append(out, bytes.Repeat(data[i:i+1], count)...)
			i++
		}
	}
	return out, nil
}
