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
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"math"
	"testing"

	"github.com/richardwilkes/pdfview/internal/filter"
)

const (
	flateName = "FlateDecode"
	lzwName   = "LZWDecode"
	hexName   = "ASCIIHexDecode"
	a85Name   = "ASCII85Decode"
	rlName    = "RunLengthDecode"
	hello     = "Hello"
)

func spec(name string) filter.Spec {
	return filter.Spec{Name: name, Params: filter.DefaultParams()}
}

func decode(t *testing.T, s filter.Spec, data []byte) []byte {
	t.Helper()
	out, err := filter.Decode(s, data, filter.MaxDecodedSize(len(data)))
	if err != nil {
		t.Fatalf("Decode(%s): %v", s.Name, err)
	}
	return out
}

func zlibCompress(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sampleData returns moderately compressible but varied data (via a fixed linear congruential sequence), long enough to
// push LZW past several code-width increases.
func sampleData() []byte {
	data := make([]byte, 8192)
	seed := uint32(42)
	for i := range data {
		seed = seed*1664525 + 1013904223
		data[i] = byte(seed>>16) % 64
	}
	return data
}

func TestFlateZlib(t *testing.T) {
	want := []byte("some reasonably compressible data data data data data")
	if got := decode(t, spec(flateName), zlibCompress(t, want)); !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlateRaw(t *testing.T) {
	want := sampleData()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(want); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := decode(t, spec(flateName), buf.Bytes()); !bytes.Equal(got, want) {
		t.Errorf("raw deflate round trip mismatch")
	}
}

func TestFlateTruncatedReturnsPartialData(t *testing.T) {
	full := zlibCompress(t, sampleData())
	got, err := filter.Decode(spec(flateName), full[:len(full)/2], filter.MaxDecodedSize(len(full)))
	if err != nil {
		t.Fatalf("expected truncated flate data to decode leniently, got %v", err)
	}
	if len(got) == 0 {
		t.Error("expected some partial output from truncated flate data")
	}
}

func TestFlateGarbageFails(t *testing.T) {
	if _, err := filter.Decode(spec(flateName), []byte("this is not flate data at all"), 1024); err == nil {
		t.Error("expected an error for garbage flate data")
	}
}

func TestDecodeTooLarge(t *testing.T) {
	data := zlibCompress(t, make([]byte, 4096))
	if _, err := filter.Decode(spec(flateName), data, 100); !errors.Is(err, filter.ErrTooLarge) {
		t.Errorf("expected ErrTooLarge, got %v", err)
	}
}

// TestLZWSpecExample decodes the worked example from ISO 32000-2 7.4.4.2 (input bytes 45 45 45 45 45 65 45 45 45 66, in
// decimal, i.e. "-----A---B"). Its codes never exceed 9 bits, so both EarlyChange conventions must decode it
// identically.
func TestLZWSpecExample(t *testing.T) {
	encoded := []byte{0x80, 0x0b, 0x60, 0x50, 0x22, 0x0c, 0x0c, 0x85, 0x01}
	want := []byte("-----A---B")
	for _, early := range []int{0, 1} {
		s := spec(lzwName)
		s.Params.EarlyChange = early
		if got := decode(t, s, encoded); !bytes.Equal(got, want) {
			t.Errorf("EarlyChange=%d: got % x, want % x", early, got, want)
		}
	}
}

func TestLZWEarlyChangeModes(t *testing.T) {
	want := sampleData()
	ec0 := lzwEncode(want, 0)
	ec1 := lzwEncode(want, 1)
	if bytes.Equal(ec0, ec1) {
		t.Fatal("expected the two EarlyChange encodings to differ for data this long")
	}
	for early, encoded := range map[int][]byte{0: ec0, 1: ec1} {
		s := spec(lzwName)
		s.Params.EarlyChange = early
		if got := decode(t, s, encoded); !bytes.Equal(got, want) {
			t.Errorf("EarlyChange=%d round trip mismatch", early)
		}
	}
}

// lzwEncode is a minimal LZW encoder (MSB packing, 8-bit literals) used only to produce test data for the two decoder
// flavors. earlyChange selects when the code width grows, mirroring the convention the corresponding decoder expects:
// both decoders bump their next-entry counter after every post-clear code they read (starting from 258), widening once
// that counter reaches 1<<width minus earlyChange. A Clear code is emitted before any 12-bit code would be needed,
// sidestepping the decoders' divergent full-table behavior.
func lzwEncode(data []byte, earlyChange int) []byte {
	const (
		clearCode = 256
		eodCode   = 257
		resetAt   = 1 << 11
	)
	var out bytes.Buffer
	var bits uint32
	var nbits, width uint
	width = 9
	emitRaw := func(code int) {
		bits |= uint32(code) << (32 - width - nbits)
		nbits += width
		for nbits >= 8 {
			out.WriteByte(byte(bits >> 24))
			bits <<= 8
			nbits -= 8
		}
	}
	emitted := 0
	emitCode := func(code int) {
		emitRaw(code)
		emitted++
		if 257+emitted >= (1<<width)-earlyChange && width < 12 {
			width++
		}
	}
	newTable := func() map[string]int {
		table := make(map[string]int, 4096)
		for i := range 256 {
			table[string([]byte{byte(i)})] = i
		}
		return table
	}
	table := newTable()
	next := eodCode + 1
	emitRaw(clearCode)
	w := ""
	for _, c := range data {
		wc := w + string([]byte{c})
		if _, ok := table[wc]; ok {
			w = wc
			continue
		}
		emitCode(table[w])
		table[wc] = next
		next++
		w = string([]byte{c})
		if next == resetAt {
			emitRaw(clearCode)
			table = newTable()
			next = eodCode + 1
			width = 9
			emitted = 0
		}
	}
	if w != "" {
		emitCode(table[w])
	}
	emitRaw(eodCode)
	if nbits > 0 {
		out.WriteByte(byte(bits >> 24))
	}
	return out.Bytes()
}

func TestASCIIHex(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"48656C6C6F>", hello},
		{"48 65 6c 6C\n6F >", hello},
		{"48656C6C6F", hello},     // missing terminator tolerated
		{"7>", "p"},               // odd final digit implies a trailing 0
		{"48zz65!6C6C6F>", hello}, // invalid characters skipped
		{">", ""},
	} {
		if got := decode(t, spec(hexName), []byte(tc.in)); string(got) != tc.want {
			t.Errorf("ASCIIHex(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestASCII85(t *testing.T) {
	want := sampleData()
	var buf bytes.Buffer
	w := ascii85.NewEncoder(&buf)
	if _, err := w.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("~>")
	if got := decode(t, spec(a85Name), buf.Bytes()); !bytes.Equal(got, want) {
		t.Error("ASCII85 round trip mismatch")
	}
	// The 'z' shorthand for four zero bytes, a leading "<~", and embedded whitespace must all be handled.
	if got := decode(t, spec(a85Name), []byte("<~z\n z~>")); !bytes.Equal(got, make([]byte, 8)) {
		t.Errorf("ASCII85 z shorthand: got % x", got)
	}
}

func TestRunLength(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want []byte
	}{
		{[]byte{2, 'a', 'b', 'c', 255, 'x', 128}, []byte("abcxx")},
		{[]byte{0, 'q', 129, 'y', 128}, append([]byte("q"), bytes.Repeat([]byte("y"), 128)...)},
		{[]byte{4, 'a', 'b'}, []byte("ab")}, // truncated literal run tolerated
		{[]byte{128, 'a'}, nil},             // EOD stops decoding
	} {
		if got := decode(t, spec(rlName), tc.in); !bytes.Equal(got, tc.want) {
			t.Errorf("RunLength(% x) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// pngFilterForward applies PNG filtering to raw so the decoder's inversion can be verified. Rows are filtered with the
// given per-row filter types.
func pngFilterForward(raw []byte, rowLen, bpp int, filters []byte) []byte {
	var out []byte
	prev := make([]byte, rowLen)
	for r := 0; r*rowLen < len(raw); r++ {
		row := raw[r*rowLen : (r+1)*rowLen]
		ft := filters[r%len(filters)]
		out = append(out, ft)
		for i := range rowLen {
			var left, upLeft byte
			if i >= bpp {
				left = row[i-bpp]
				upLeft = prev[i-bpp]
			}
			up := prev[i]
			var f byte
			switch ft {
			case 0:
				f = row[i]
			case 1:
				f = row[i] - left
			case 2:
				f = row[i] - up
			case 3:
				f = row[i] - byte((int(left)+int(up))/2)
			case 4:
				f = row[i] - paethRef(left, up, upLeft)
			}
			out = append(out, f)
		}
		prev = row
	}
	return out
}

func paethRef(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := p-int(a), p-int(b), p-int(c)
	if pa < 0 {
		pa = -pa
	}
	if pb < 0 {
		pb = -pb
	}
	if pc < 0 {
		pc = -pc
	}
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	default:
		return c
	}
}

func TestPNGPredictor(t *testing.T) {
	const columns = 16
	const colors = 3
	rowLen := columns * colors
	raw := sampleData()[:rowLen*8]
	for _, filters := range [][]byte{{0}, {1}, {2}, {3}, {4}, {0, 1, 2, 3, 4}} {
		filtered := pngFilterForward(raw, rowLen, colors, filters)
		compressed := zlibCompress(t, filtered)
		s := spec(flateName)
		s.Params.Predictor = 12
		s.Params.Colors = colors
		s.Params.Columns = columns
		if got := decode(t, s, compressed); !bytes.Equal(got, raw) {
			t.Errorf("PNG predictor with filters %v: round trip mismatch", filters)
		}
	}
}

func TestPNGPredictorSixteenBit(t *testing.T) {
	const columns = 8
	const colors = 1
	rowLen := columns * 2
	raw := sampleData()[:rowLen*4]
	filtered := pngFilterForward(raw, rowLen, 2, []byte{1, 4})
	s := spec(flateName)
	s.Params.Predictor = 15
	s.Params.Colors = colors
	s.Params.BitsPerComponent = 16
	s.Params.Columns = columns
	if got := decode(t, s, zlibCompress(t, filtered)); !bytes.Equal(got, raw) {
		t.Error("16-bit PNG predictor round trip mismatch")
	}
}

func TestPNGPredictorSubByteMultiComponent(t *testing.T) {
	// 5 components at 2 bits each is 10 bits per pixel, so bytes-per-pixel must round up to 2. A floor-division bpp
	// would use 1 here and mis-invert the Sub/Average/Paeth filters.
	const columns = 4
	const colors = 5
	const bits = 2
	rowLen := (colors*bits*columns + 7) / 8 // 40 bits -> 5 bytes
	bpp := (colors*bits + 7) / 8            // 10 bits -> 2 bytes
	raw := sampleData()[:rowLen*6]
	for _, filters := range [][]byte{{1}, {3}, {4}, {0, 1, 2, 3, 4}} {
		filtered := pngFilterForward(raw, rowLen, bpp, filters)
		s := spec(flateName)
		s.Params.Predictor = 12
		s.Params.Colors = colors
		s.Params.BitsPerComponent = bits
		s.Params.Columns = columns
		if got := decode(t, s, zlibCompress(t, filtered)); !bytes.Equal(got, raw) {
			t.Errorf("sub-byte multi-component PNG predictor with filters %v: round trip mismatch", filters)
		}
	}
}

func TestPNGPredictorBadFilterType(t *testing.T) {
	s := spec(flateName)
	s.Params.Predictor = 10
	s.Params.Columns = 4
	data := zlibCompress(t, []byte{9, 1, 2, 3, 4})
	if _, err := filter.Decode(s, data, 1024); !errors.Is(err, filter.ErrUnsupportedFilter) {
		t.Errorf("expected ErrUnsupportedFilter for a bad PNG filter type, got %v", err)
	}
}

func TestTIFFPredictor(t *testing.T) {
	raw := []byte{10, 20, 30, 12, 24, 36, 15, 30, 45, 1, 2, 3, 2, 4, 6, 3, 6, 9}
	const colors = 3
	const columns = 3
	// Apply forward horizontal differencing per row.
	filtered := append([]byte{}, raw...)
	rowLen := colors * columns
	for r := 0; r < len(filtered); r += rowLen {
		for i := r + rowLen - 1; i >= r+colors; i-- {
			filtered[i] -= filtered[i-colors]
		}
	}
	s := spec(flateName)
	s.Params.Predictor = 2
	s.Params.Colors = colors
	s.Params.Columns = columns
	if got := decode(t, s, zlibCompress(t, filtered)); !bytes.Equal(got, raw) {
		t.Errorf("TIFF predictor: got % x, want % x", got, raw)
	}
}

func TestTIFFPredictorSixteenBit(t *testing.T) {
	raw := []byte{0x01, 0x00, 0x01, 0x10, 0x01, 0x20, 0x02, 0x00, 0x02, 0x08, 0x02, 0x10}
	const columns = 3
	filtered := append([]byte{}, raw...)
	for r := 0; r < len(filtered); r += columns * 2 {
		for i := r + columns*2 - 2; i >= r+2; i -= 2 {
			v := uint16(filtered[i])<<8 | uint16(filtered[i+1])
			left := uint16(filtered[i-2])<<8 | uint16(filtered[i-1])
			v -= left
			filtered[i] = byte(v >> 8)
			filtered[i+1] = byte(v)
		}
	}
	s := spec(flateName)
	s.Params.Predictor = 2
	s.Params.BitsPerComponent = 16
	s.Params.Columns = columns
	if got := decode(t, s, zlibCompress(t, filtered)); !bytes.Equal(got, raw) {
		t.Errorf("16-bit TIFF predictor: got % x, want % x", got, raw)
	}
}

// TestPredictorMaximumLayout drives both predictors with the largest layout the parameter validation accepts. The
// untruncated row length is 2^34 bytes (2^31 for the 16-bit TIFF stride), so an int row-length computation wraps
// negative on a 32-bit build and panics in make() or walks the row loops backwards; the decoded bytes are simply the
// single truncated row the data actually contains.
func TestPredictorMaximumLayout(t *testing.T) {
	const colors = 64
	const columns = 1 << 24
	t.Run("PNG", func(t *testing.T) {
		// One filter-type byte (0 = None) followed by a short, truncated row.
		filtered := append([]byte{0}, sampleData()[:32]...)
		s := spec(flateName)
		s.Params.Predictor = 12
		s.Params.Colors = colors
		s.Params.BitsPerComponent = 16
		s.Params.Columns = columns
		if got := decode(t, s, zlibCompress(t, filtered)); !bytes.Equal(got, filtered[1:]) {
			t.Errorf("maximum-layout PNG predictor: got % x, want % x", got, filtered[1:])
		}
	})
	t.Run("TIFF8", func(t *testing.T) {
		raw := sampleData()[:32]
		filtered := append([]byte{}, raw...)
		for i := len(filtered) - 1; i >= colors; i-- {
			filtered[i] -= filtered[i-colors]
		}
		s := spec(flateName)
		s.Params.Predictor = 2
		s.Params.Colors = colors
		s.Params.Columns = columns
		if got := decode(t, s, zlibCompress(t, filtered)); !bytes.Equal(got, raw) {
			t.Errorf("maximum-layout 8-bit TIFF predictor: got % x, want % x", got, raw)
		}
	})
	t.Run("TIFF16", func(t *testing.T) {
		// The stride is 2*colors bytes, so a 32-byte payload is a single truncated row that no differencing touches;
		// it must come back unchanged rather than panic.
		raw := sampleData()[:32]
		s := spec(flateName)
		s.Params.Predictor = 2
		s.Params.Colors = colors
		s.Params.BitsPerComponent = 16
		s.Params.Columns = columns
		if got := decode(t, s, zlibCompress(t, raw)); !bytes.Equal(got, raw) {
			t.Errorf("maximum-layout 16-bit TIFF predictor: got % x, want % x", got, raw)
		}
	})
}

func TestDecodeChain(t *testing.T) {
	want := sampleData()
	compressed := zlibCompress(t, want)
	var buf bytes.Buffer
	w := ascii85.NewEncoder(&buf)
	if _, err := w.Write(compressed); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("~>")
	got, err := filter.DecodeChain([]filter.Spec{spec(a85Name), spec(flateName)}, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("ASCII85 + Flate chain mismatch")
	}
}

func TestDecodeChainTooLong(t *testing.T) {
	specs := make([]filter.Spec, filter.MaxChainLength+1)
	for i := range specs {
		specs[i] = spec(hexName)
	}
	if _, err := filter.DecodeChain(specs, []byte("00>")); !errors.Is(err, filter.ErrChainTooLong) {
		t.Errorf("expected ErrChainTooLong, got %v", err)
	}
}

func TestUnsupportedFilter(t *testing.T) {
	for _, name := range []string{"DCTDecode", "JPXDecode", "CCITTFaxDecode", "JBIG2Decode", "Crypt", "Bogus"} {
		if _, err := filter.Decode(spec(name), []byte("x"), 1024); !errors.Is(err, filter.ErrUnsupportedFilter) {
			t.Errorf("expected ErrUnsupportedFilter for %s, got %v", name, err)
		}
	}
}

// TestDecodeMaxSizeCeiling guards against the limit-arithmetic overflow that made readCapped return an empty stream:
// int64(math.MaxInt)+1 wraps to math.MinInt64, which io.LimitReader treats as immediate EOF. With the largest possible
// maxSize a valid stream must still decode fully rather than silently emptying.
func TestDecodeMaxSizeCeiling(t *testing.T) {
	want := []byte(hello)
	compressed := zlibCompress(t, want)
	got, err := filter.Decode(spec(flateName), compressed, math.MaxInt)
	if err != nil {
		t.Fatalf("Decode(%s, MaxInt): %v", flateName, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Decode(%s, MaxInt) = %q, want %q", flateName, got, want)
	}
}

func TestMaxDecodedSize(t *testing.T) {
	if got := filter.MaxDecodedSize(0); got != 64<<20 {
		t.Errorf("MaxDecodedSize(0) = %d, want %d", got, 64<<20)
	}
	if got := filter.MaxDecodedSize(1 << 20); got != 256<<20 {
		t.Errorf("MaxDecodedSize(1MB) = %d, want %d", got, 256<<20)
	}
}
