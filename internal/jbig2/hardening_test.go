// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// This file is pdfview-authored (MPL-2.0), not upstream code. It pins the DoS guards restored into the vendored JBIG2
// decoder: each is a hard reject before an allocation or loop that hostile input could otherwise blow up. Every case
// proves the reject fires and, where a legitimate small value can be isolated from a full decode, that the value still
// passes. The legitimate-pass side of the guards inside the entropy decoders (text-region instances, halftone grid,
// collective bitmap) is pinned end-to-end by the corpus goldens in pdfview_test.go and internal/imaging, named below.

package jbig2

import (
	"strings"
	"testing"
)

// be32 encodes a big-endian 4-byte integer, the form ReadInteger consumes.
func be32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// be16 encodes a big-endian 2-byte integer, the form ReadShortInteger consumes.
func be16(v uint16) []byte {
	return []byte{byte(v >> 8), byte(v)}
}

// regionInfoBytes builds the 17-byte region information block ParseRegionInfo reads: width, height, x, y, flags.
func regionInfoBytes(w, h int32) []byte {
	b := append([]byte{}, be32(uint32(w))...)
	b = append(b, be32(uint32(h))...)
	b = append(b, be32(0)...) // x
	b = append(b, be32(0)...) // y
	b = append(b, 0x00)       // flags
	return b
}

// bitWriter accumulates an MSB-first bit stream, the order the Huffman and range readers consume, so a hand-encoded
// symbol dictionary can drive the entropy decoders to the exact intermediate values a guard bounds.
type bitWriter struct {
	bits []byte
}

func (w *bitWriter) writeCode(s string) {
	for _, c := range s {
		if c == '1' {
			w.bits = append(w.bits, 1)
		} else {
			w.bits = append(w.bits, 0)
		}
	}
}

func (w *bitWriter) writeBits(value uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bits = append(w.bits, byte((value>>uint(i))&1))
	}
}

func (w *bitWriter) bytes() []byte {
	out := make([]byte, (len(w.bits)+7)/8)
	for i, b := range w.bits {
		if b != 0 {
			out[i/8] |= 0x80 >> uint(i%8)
		}
	}
	return out
}

// TestGuardSymbolCounts pins that parseSymbolDict rejects SDNUMEXSYMS or SDNUMNEWSYMS over 65535 before either sizes
// an allocation (PDFium jbig2_context.cpp:440-441). The stream is an arithmetic symbol dictionary header (SDTEMPLATE 1,
// so only two SDAT bytes precede the counts); at the cap and below it decodes to an empty dictionary, so the boundary
// and the small case are observable as success.
func TestGuardSymbolCounts(t *testing.T) {
	build := func(exsyms, newsyms uint32) []byte {
		b := append([]byte{}, be16(0x0400)...) // flags: SDHUFF=0, SDREFAGG=0, SDTEMPLATE=1
		b = append(b, 0x00, 0x00)              // SDAT (2 bytes for template != 0)
		b = append(b, be32(exsyms)...)
		b = append(b, be32(newsyms)...)
		b = append(b, 0x00, 0x00, 0x00, 0x00) // tail for the arithmetic decoder
		return b
	}
	for _, tc := range []struct {
		name    string
		exsyms  uint32
		newsyms uint32
		want    Result
	}{
		{"exsyms_over_cap", 65536, 0, ResultFailure},
		{"newsyms_over_cap", 0, 65536, ResultFailure},
		{"exsyms_at_cap", 65535, 0, ResultSuccess},
		{"both_zero", 0, 0, ResultSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDocument(build(tc.exsyms, tc.newsyms), nil)
			if got := d.parseSymbolDict(NewSegment()); got != tc.want {
				t.Fatalf("parseSymbolDict(exsyms=%d, newsyms=%d) = %d, want %d", tc.exsyms, tc.newsyms, got, tc.want)
			}
		})
	}
}

// TestGuardRegionSize pins that ParseRegionInfo rejects a region rectangle outside (0, 65535] in either dimension
// (PDFium IsValidImageSize, jbig2_image.cpp:127-129), the single site covering the text, halftone, generic, and
// refinement paths. A value >= 2^31 reads back negative into int32 and is caught by the <= 0 test.
func TestGuardRegionSize(t *testing.T) {
	for _, tc := range []struct {
		name string
		w    int32
		h    int32
		want Result
	}{
		{"valid_small", 1, 1, ResultSuccess},
		{"valid_at_cap", 65535, 65535, ResultSuccess},
		{"zero_width", 0, 1, ResultFailure},
		{"zero_height", 1, 0, ResultFailure},
		{"width_over_cap", 65536, 1, ResultFailure},
		{"height_over_cap", 1, 65536, ResultFailure},
		{"width_wraps_negative", -2147483648, 1, ResultFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDocument(regionInfoBytes(tc.w, tc.h), nil)
			var ri RegionInfo
			if got := d.ParseRegionInfo(&ri); got != tc.want {
				t.Fatalf("ParseRegionInfo(%d x %d) = %d, want %d", tc.w, tc.h, got, tc.want)
			}
		})
	}
}

// TestGuardTextInstances pins that parseTextRegion rejects an SBNUMINSTANCES past what the remaining stream can code
// (32 per byte; PDFium jbig2_context.cpp:662-675); without it the instance loop in jbig2_trd_proc.go spins on the
// 2^32-1 count. A legitimate SBNUMINSTANCES passing the guard is pinned by the symbol_text_arithmetic and
// symbol_text_huffman corpus goldens.
func TestGuardTextInstances(t *testing.T) {
	b := append([]byte{}, regionInfoBytes(1, 1)...)
	b = append(b, be16(0x0000)...)     // text-region flags: SBHUFF=0, SBREFINE=0
	b = append(b, be32(0xFFFFFFFF)...) // SBNUMINSTANCES
	b = append(b, 0x00, 0x00, 0x00, 0x00)
	d := NewDocument(b, nil)
	if got := d.parseTextRegion(NewSegment()); got != ResultFailure {
		t.Fatalf("parseTextRegion with SBNUMINSTANCES=0xFFFFFFFF = %d, want ResultFailure", got)
	}
}

// TestGuardHalftoneGrid pins that parseHalftoneRegion rejects a grid dimension HGW/HGH outside (0, 65535] (PDFium
// jbig2_context.cpp:933); the halftone MMR path sizes make([]int, HGW+5) from HGW. A legitimate grid passing the guard
// is pinned by the pattern_halftone_mmr corpus golden.
func TestGuardHalftoneGrid(t *testing.T) {
	build := func(hgw, hgh uint32) []byte {
		b := append([]byte{}, regionInfoBytes(1, 1)...)
		b = append(b, 0x00) // halftone flags
		b = append(b, be32(hgw)...)
		b = append(b, be32(hgh)...)
		b = append(b, 0x00, 0x00, 0x00, 0x00) // room past HGH
		return b
	}
	for _, tc := range []struct {
		name string
		hgw  uint32
		hgh  uint32
	}{
		{"zero_hgw", 0, 1},
		{"zero_hgh", 1, 0},
		{"hgw_over_cap", 65536, 1},
		{"hgh_over_cap", 1, 65536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDocument(build(tc.hgw, tc.hgh), nil)
			if got := d.parseHalftoneRegion(NewSegment()); got != ResultFailure {
				t.Fatalf("parseHalftoneRegion(HGW=%d, HGH=%d) = %d, want ResultFailure", tc.hgw, tc.hgh, got)
			}
		})
	}
}

// TestGuardMMRValidRowDecodes pins that a well-formed MMR row still decodes under the changing-element bound. For a
// 1-wide image the single 1-bit vertical-0 mode code (0x80) resolves the row against the reference edge and terminates
// it, so the changing-element index never nears the buffer bound.
func TestGuardMMRValidRowDecodes(t *testing.T) {
	dec := NewMMRDecompressor(1, 1, NewBitStream([]byte{0x80, 0x00}, 0))
	img, err := dec.Uncompress()
	if err != nil {
		t.Fatalf("valid single-row MMR failed: %v", err)
	}
	if img == nil || img.Width() != 1 || img.Height() != 1 {
		t.Fatalf("valid MMR decoded to %v, want a 1x1 image", img)
	}
}

// mmrRepeatedVL1 packs the 3-bit VL1 mode code ("010") eight times per 0x49 0x24 0x92 triple. VL1 over the reference
// edge of a 1-wide image pins the changing position while appending a changing element every code, so the current-row
// index outruns its width+5 buffer. The triple is repeated so the code reader never runs short of bits before the
// index reaches the bound.
func mmrRepeatedVL1() []byte {
	out := []byte{}
	for i := 0; i < 6; i++ {
		out = append(out, 0x49, 0x24, 0x92)
	}
	return out
}

// TestGuardMMRPathologicalErrors pins that the repeated-VL1 sequence, which walks currIdx past the row buffer, returns
// an error instead of panicking with an out-of-range write. The generic_mmr and pattern_halftone_mmr corpus goldens pin
// that valid MMR still decodes bit-for-bit.
func TestGuardMMRPathologicalErrors(t *testing.T) {
	dec := NewMMRDecompressor(1, 4, NewBitStream(mmrRepeatedVL1(), 0))
	img, err := dec.Uncompress()
	if err == nil {
		t.Fatalf("pathological repeated-VL1 MMR decoded to %v; want an error, not a panic", img)
	}
}

// TestGuardCollectiveBitmapWidth pins the TOTWIDTH cap in the Huffman symbol-dictionary path. The stream is hand-coded
// against the standard DH/DW/BMSIZE tables to drive one height class to two symbols of width 65535, so TOTWIDTH sums
// to 131070 — over the 65535 cap PDFium enforces (jbig2_sdd_proc.cpp:419-430) before the wrapping-uint32 stride math.
// The specific guard error is asserted so the case cannot pass by failing earlier. The legitimate path (small
// TOTWIDTH) is pinned by the symbol_text_huffman corpus golden.
func TestGuardCollectiveBitmapWidth(t *testing.T) {
	w := &bitWriter{}
	w.writeCode("0")       // HCDH via table 4 -> HCHEIGHT = 1
	w.writeCode("111110")  // DW1 via table 2, 32-bit range prefix
	w.writeBits(65460, 32) // DW1 = 75 + 65460 = 65535, so SYMWIDTH = 65535, TOTWIDTH = 65535
	w.writeCode("0")       // DW2 via table 2 -> 0, SYMWIDTH stays 65535, TOTWIDTH = 131070
	w.writeCode("111111")  // DW out-of-band ends the height class
	w.writeCode("0")       // BMSIZE via table 1, 4-bit range prefix
	w.writeBits(0, 4)      // BMSIZE = 0, the uncompressed collective-bitmap branch

	s := NewSDDProc()
	s.SDHUFF = true
	s.SDNUMEXSYMS = 2
	s.SDNUMNEWSYMS = 2
	s.SDHUFFDH = NewStandardTable(4)
	s.SDHUFFDW = NewStandardTable(2)
	s.SDHUFFBMSIZE = NewStandardTable(1)

	_, err := s.DecodeHuffman(NewBitStream(w.bytes(), 0), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "collective bitmap width too large") {
		t.Fatalf("DecodeHuffman with TOTWIDTH 131070 = %v, want a collective-bitmap-width rejection", err)
	}
}

// TestGuardAggregateInstances pins that REFAGGNINST, decoded inline in the symbol dictionary, is bounded by 32 per
// remaining stream byte before it becomes SBNUMINSTANCES. The stream is hand-coded to decode REFAGGNINST = 65808 from
// a handful of bytes, so it dwarfs 32*GetByteLeft; the guard error is asserted directly. This is hardening beyond
// PDFium, which decodes REFAGGNINST unguarded, and no corpus golden exercises the aggregate path — the reject is the
// pin.
func TestGuardAggregateInstances(t *testing.T) {
	w := &bitWriter{}
	w.writeCode("0")   // HCDH via table 4 -> HCHEIGHT = 1
	w.writeCode("10")  // DW1 via table 2 -> 1, SYMWIDTH = 1
	w.writeCode("111") // REFAGGNINST via table 1, 32-bit range prefix
	w.writeBits(0, 32) // REFAGGNINST = 65808 + 0

	s := NewSDDProc()
	s.SDHUFF = true
	s.SDREFAGG = true
	s.SDRTEMPLATE = true
	s.SDNUMEXSYMS = 2
	s.SDNUMNEWSYMS = 1
	s.SDHUFFDH = NewStandardTable(4)
	s.SDHUFFDW = NewStandardTable(2)
	s.SDHUFFBMSIZE = NewStandardTable(1)
	s.SDHUFFAGGINST = NewStandardTable(1)

	_, err := s.DecodeHuffman(NewBitStream(w.bytes(), 0), nil, make([]ArithCtx, 1024))
	if err == nil || !strings.Contains(err.Error(), "refaggninst exceeds stream bound") {
		t.Fatalf("DecodeHuffman with REFAGGNINST 65808 = %v, want an aggregate-instance rejection", err)
	}
}
