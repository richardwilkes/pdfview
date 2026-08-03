// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// This file and everything under testdata/ are pdfview-authored (MPL-2.0), not upstream code; the vendored tree
// arrived with no tests at all, and these pin its behavior byte-for-byte so in-tree hardening cannot change a decode
// result without saying so.

package jbig2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// pinLimits is the cap the bitmap pins decode under. It is far past anything the corpus needs, so the pins measure
// decode output rather than the cap; TestCumulativeAreaCap is what measures the cap.
var pinLimits = Limits{MaxPixels: 1 << 26}

// jbig2Codec is the /Filter name the corpus scan selects on.
const jbig2Codec cos.Name = "JBIG2Decode"

// pbmHeaderLines is the number of lines each fixture spends on the PBM magic, the polarity comment, and the
// dimensions, before the one-line-per-row bitmap starts. Diff reporting uses it to turn a line number into a row.
const pbmHeaderLines = 3

// corpusPin is one JBIG2 payload the corpus carries, the page it decodes to today, and the fixture holding that page.
// Distinct payloads that encode the same source share a fixture: the stencil file reuses the arithmetic generic
// payload byte-for-byte in two roles, and the split-globals file is the self-contained symbol/text file re-split, so
// sharing is itself a pin that the reuse decodes identically.
type corpusPin struct {
	// name states the coding path exercised, so a hardening regression names the area it broke.
	name    string
	file    string
	obj     int
	w       int
	h       int
	fixture string
}

// corpusPins is the complete set of decodable JBIG2 payloads in the corpus. TestCorpusPayloadsMatchPinnedBitmaps
// checks the set against what the corpus actually holds, so a payload added, dropped, or renumbered there fails here
// rather than silently losing coverage.
var corpusPins = []corpusPin{
	{
		name:    "generic_arithmetic",
		file:    "images-jbig2-generic.pdf",
		obj:     5,
		w:       128,
		h:       80,
		fixture: "generic-arith.pbm",
	},
	{
		name:    "generic_mmr",
		file:    "images-jbig2-generic.pdf",
		obj:     6,
		w:       96,
		h:       64,
		fixture: "generic-mmr.pbm",
	},
	{
		name:    "symbol_text_arithmetic",
		file:    "images-jbig2-text.pdf",
		obj:     5,
		w:       184,
		h:       72,
		fixture: "symbol-text.pbm",
	},
	{
		name:    "symbol_text_split_globals",
		file:    "images-jbig2-globals.pdf",
		obj:     5,
		w:       184,
		h:       72,
		fixture: "symbol-text.pbm",
	},
	{
		name:    "symbol_text_huffman",
		file:    "images-jbig2-huffman.pdf",
		obj:     5,
		w:       56,
		h:       40,
		fixture: "symbol-text-huffman.pbm",
	},
	{
		name:    "pattern_halftone_mmr",
		file:    "images-jbig2-halftone.pdf",
		obj:     5,
		w:       60,
		h:       36,
		fixture: "halftone-mmr.pbm",
	},
	{
		name:    "generic_refinement",
		file:    "images-jbig2-refine.pdf",
		obj:     5,
		w:       72,
		h:       48,
		fixture: "generic-refine.pbm",
	},
	{
		name:    "stencil_imagemask",
		file:    "images-jbig2-stencil.pdf",
		obj:     5,
		w:       128,
		h:       80,
		fixture: "generic-arith.pbm",
	},
	{
		name:    "stencil_mask_entry",
		file:    "images-jbig2-stencil.pdf",
		obj:     6,
		w:       128,
		h:       80,
		fixture: "generic-arith.pbm",
	},
}

// undecodablePins are the corpus payloads that must not produce a page. Both reach the decoder through the same
// prefix as everything else; what is pinned is where each one fails, because internal/imaging/jbig2.go's white-page
// recovery only runs when construction succeeds and Decode is the call that reports the failure.
var undecodablePins = []corpusPin{
	{name: "truncated_mid_region", file: "images-jbig2-truncated.pdf", obj: 5},
	{name: "four_byte_stub", file: "images-jbig2.pdf", obj: 5},
}

// TestCorpusPayloadsMatchPinnedBitmaps decodes every JBIG2 payload the corpus carries and compares the page against a
// committed fixture, pixel for pixel. M1 established these decodes are bit-exact against the MuPDF oracle, so the
// fixtures pin correct output, not merely current output.
func TestCorpusPayloadsMatchPinnedBitmaps(t *testing.T) {
	found := corpusJBIG2Payloads(t)
	covered := make(map[string]bool, len(found))
	for _, pin := range corpusPins {
		covered[corpusKey(pin.file, pin.obj)] = true
	}
	for _, pin := range undecodablePins {
		covered[corpusKey(pin.file, pin.obj)] = true
	}
	for key := range found {
		if !covered[key] {
			t.Errorf("corpus payload %s is not pinned by any case in this file", key)
		}
	}
	for _, pin := range corpusPins {
		t.Run(pin.name, func(t *testing.T) {
			img, err := decodeEmbedded(corpusPayload(t, found, pin.file, pin.obj))
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			pinBitmap(t, pin, img)
		})
	}
}

// TestHardeningSurgerySitesMatchPinnedBitmaps re-pins the three payloads that exercise the routines the in-tree
// hardening operates on, as separately named cases: the refinement decoder reached through the region dispatch, the
// Huffman symbol-dictionary path with its export runs, and the MMR pattern/halftone path. They repeat cases from the
// table above on purpose — a failure here names the surgery site rather than the corpus file.
func TestHardeningSurgerySitesMatchPinnedBitmaps(t *testing.T) {
	found := corpusJBIG2Payloads(t)
	for _, name := range []string{"generic_refinement", "symbol_text_huffman", "pattern_halftone_mmr"} {
		pin := pinNamed(t, name)
		t.Run(name, func(t *testing.T) {
			img, err := decodeEmbedded(corpusPayload(t, found, pin.file, pin.obj))
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			pinBitmap(t, *pin, img)
		})
	}
}

// TestUndecodableCorpusPayloadsFailAtDecode pins that a truncated payload and the four-byte stub payload construct a
// decoder and then fail in DecodePage, returning no page. internal/imaging/jbig2.go depends on that split: it paints
// the full-size white page MuPDF paints only when the construction succeeded and the region decode is what failed.
func TestUndecodableCorpusPayloadsFailAtDecode(t *testing.T) {
	found := corpusJBIG2Payloads(t)
	for _, pin := range undecodablePins {
		t.Run(pin.name, func(t *testing.T) {
			payload, globals := corpusPayload(t, found, pin.file, pin.obj)
			dec, err := NewEmbeddedDecoder(payload, globals, pinLimits)
			if err != nil {
				t.Fatalf("construction failed with %v; the recovery path needs it to succeed", err)
			}
			page, err := dec.DecodePage()
			if err == nil {
				t.Fatalf("decode succeeded and returned a %dx%d page; it must report the truncation",
					page.Width(), page.Height())
			}
			if page != nil {
				t.Errorf("decode returned an error and a %dx%d page; the error path must return none",
					page.Width(), page.Height())
			}
		})
	}
}

// TestGarbagePayloadsFailToDecode pins the same split for input that is not JBIG2 at all: the embedded profile takes
// a payload as segments and nothing else, so construction accepts any non-empty garbage and the decode is what
// rejects it. An empty payload cannot carry even one segment header, which construction itself refuses.
func TestGarbagePayloadsFailToDecode(t *testing.T) {
	for _, tc := range []struct {
		name                string
		payload             []byte
		failsAtConstruction bool
	}{
		{name: "four_random_bytes", payload: []byte{0xde, 0xad, 0xbe, 0xef}},
		{name: "single_zero_byte", payload: []byte{0x00}},
		{name: "all_ones", payload: bytes.Repeat([]byte{0xff}, 12)},
		{name: "empty", payload: nil, failsAtConstruction: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := NewEmbeddedDecoder(tc.payload, nil, pinLimits)
			if tc.failsAtConstruction {
				if err == nil {
					t.Fatal("construction succeeded on a payload with no segment header at all")
				}
				return
			}
			if err != nil {
				t.Fatalf("construction failed with %v; construction accepts any non-empty payload", err)
			}
			page, err := dec.DecodePage()
			if err == nil {
				t.Fatalf("decode succeeded and returned a %dx%d page", page.Width(), page.Height())
			}
			if page != nil {
				t.Errorf("decode returned an error and a %dx%d page; the error path must return none",
					page.Width(), page.Height())
			}
		})
	}
}

// TestEmbeddedEntryShape pins the embedded-profile entry contract: what NewEmbeddedDecoder accepts, what it refuses,
// and what a second decode does. The glue in internal/imaging/jbig2.go depends on every case here.
func TestEmbeddedEntryShape(t *testing.T) {
	found := corpusJBIG2Payloads(t)
	selfContained, _ := corpusPayload(t, found, "images-jbig2-text.pdf", 5)
	split, splitGlobals := corpusPayload(t, found, "images-jbig2-globals.pdf", 5)

	// A PDF-embedded stream is headerless by definition (ISO 32000-2 7.4.7), and the entry point takes it exactly as
	// stored: no file signature, no container unwrapping, no synthesized prefix.
	t.Run("headerless_payload_decodes_directly", func(t *testing.T) {
		for _, pin := range corpusPins {
			payload, globals := corpusPayload(t, found, pin.file, pin.obj)
			if len(globals) != 0 {
				continue
			}
			page, err := decodeEmbedded(payload, nil)
			if err != nil {
				t.Errorf("%s: decode failed: %v", pin.name, err)
				continue
			}
			pinBitmap(t, pin, page)
		}
	})

	// A payload that does carry a T.88 file header is not a PDF-embedded stream; the header's bytes are read as a
	// segment header and the decode fails rather than silently skipping them.
	t.Run("file_header_is_not_stripped", func(t *testing.T) {
		prefixed := append([]byte{0x97, 0x4a, 0x42, 0x32, 0x0d, 0x0a, 0x1a, 0x0a, 0x03}, selfContained...)
		if page, err := decodeEmbedded(prefixed, nil); err == nil {
			t.Fatalf("a file-header-prefixed payload decoded to a %dx%d page", page.Width(), page.Height())
		}
	})

	// Globals are parsed at construction, so a decoder that constructs is one whose /JBIG2Globals segments were
	// usable; the page then decodes from the payload alone.
	t.Run("globals_are_parsed_at_construction", func(t *testing.T) {
		dec, err := NewEmbeddedDecoder(split, splitGlobals, pinLimits)
		if err != nil {
			t.Fatalf("construction failed: %v", err)
		}
		page, err := dec.DecodePage()
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		pinBitmap(t, *pinNamed(t, "symbol_text_split_globals"), page)
	})

	// The same payload without its globals loses the symbol dictionary the text region refers to, which is a decode
	// failure rather than a page missing its glyphs.
	t.Run("globals_are_required_when_the_payload_refers_to_them", func(t *testing.T) {
		if page, err := decodeEmbedded(split, nil); err == nil {
			t.Fatalf("decode without globals produced a %dx%d page", page.Width(), page.Height())
		}
	})

	// The corpus encodes one source two ways: symbols inline, and symbols moved to /JBIG2Globals. The pages must be
	// identical plane for plane, which pins that globals segments reach the decode exactly as inline ones do.
	t.Run("split_globals_match_self_contained", func(t *testing.T) {
		inline, err := decodeEmbedded(selfContained, nil)
		if err != nil {
			t.Fatalf("self-contained decode failed: %v", err)
		}
		external, err := decodeEmbedded(split, splitGlobals)
		if err != nil {
			t.Fatalf("split-globals decode failed: %v", err)
		}
		if inline.Width() != external.Width() || inline.Height() != external.Height() {
			t.Fatalf("split globals decoded %dx%d, self-contained decoded %dx%d", external.Width(),
				external.Height(), inline.Width(), inline.Height())
		}
		if !bytes.Equal(inline.Data(), external.Data()) {
			t.Fatalf("split globals and self-contained decoded the same source to different planes (%d bytes differ)",
				differingBytes(inline.Data(), external.Data()))
		}
	})

	// The embedded profile puts everything on page 1, and the glue calls DecodePage exactly once; the second call is
	// what reports there is nothing after it.
	t.Run("second_decode_reports_eof", func(t *testing.T) {
		dec, err := NewEmbeddedDecoder(selfContained, nil, pinLimits)
		if err != nil {
			t.Fatalf("construction failed: %v", err)
		}
		if _, err = dec.DecodePage(); err != nil {
			t.Fatalf("first decode failed: %v", err)
		}
		page, err := dec.DecodePage()
		if !errors.Is(err, io.EOF) {
			t.Fatalf("second decode returned (%v, %v), want (nil, EOF)", page, err)
		}
		if page != nil {
			t.Errorf("second decode returned a %dx%d page; it must return none", page.Width(), page.Height())
		}
	})
}

// TestCumulativeAreaCap pins the cap that Limits.MaxPixels names. Every corpus payload decodes under a cap generous
// enough for its page and fails under one a fraction of the page's area, and the failure is reported as
// ErrLimitExceeded rather than as a page. The cap is cumulative over every bitmap the decode allocates, so a payload
// whose page alone fits can still exceed it — which is the whole point, symbol bitmaps being invisible to any scan of
// the payload's headers.
func TestCumulativeAreaCap(t *testing.T) {
	found := corpusJBIG2Payloads(t)
	for _, pin := range corpusPins {
		t.Run(pin.name, func(t *testing.T) {
			payload, globals := corpusPayload(t, found, pin.file, pin.obj)
			dec, err := NewEmbeddedDecoder(payload, globals, Limits{MaxPixels: 64})
			if err != nil {
				if !errors.Is(err, ErrLimitExceeded) {
					t.Fatalf("construction under a 64-pixel cap failed with %v, want ErrLimitExceeded", err)
				}
				return
			}
			page, err := dec.DecodePage()
			if err == nil {
				t.Fatalf("a %dx%d page decoded under a 64-pixel cap", page.Width(), page.Height())
			}
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("the capped decode failed with %v, want ErrLimitExceeded", err)
			}
			if page != nil {
				t.Errorf("the capped decode returned a %dx%d page; it must return none", page.Width(), page.Height())
			}
		})
	}
}

// TestRefinementOptPathHandlesShiftedReference pins the dispatch this package deliberately leaves ungated where
// PDFium gates it. PDFium takes its optimized refinement bodies only when GRREFERENCEDX == 0 and the region is
// exactly as wide as the reference, because its bodies walk raw row pointers by whole bytes; the bodies here read
// every reference pixel through bounds-checked row accessors instead, so they are claimed correct for any offset and
// any reference width.
//
// The claim is checked without an oracle, by translation invariance: refining a W-wide region over a reference
// narrower by dx at GRREFERENCEDX == dx must produce exactly what refining it over that same reference shifted right
// by dx at GRREFERENCEDX == 0 produces — and the second configuration is the one PDFium's gate admits. Both decodes
// read the same arithmetic stream, so any divergence in context formation shows up as differing pixels. Both
// refinement templates are pinned, at several offsets, with TPGRON on and off (TPGRON exercises the typical-pixel
// prediction, which reads the reference through a different path than the context does).
func TestRefinementOptPathHandlesShiftedReference(t *testing.T) {
	const regionW, regionH = 37, 11
	for _, template := range []bool{false, true} {
		for _, tpgron := range []bool{false, true} {
			for _, dx := range []int32{1, 3, 8, 12} {
				name := fmt.Sprintf("template%d_tpgron%t_dx%d", boolToInt(template), tpgron, dx)
				t.Run(name, func(t *testing.T) {
					narrow := patternImage(regionW-int(dx), regionH+2, 0x6d)
					shifted := shiftedRight(narrow, dx, regionW)
					offset := refineWith(t, narrow, regionW, regionH, dx, template, tpgron)
					nominal := refineWith(t, shifted, regionW, regionH, 0, template, tpgron)
					if !bytes.Equal(offset.Data(), nominal.Data()) {
						t.Fatalf("refining at GRREFERENCEDX %d over a %d-wide reference differs from the nominal "+
							"configuration in %d bytes", dx, narrow.Width(), differingBytes(offset.Data(),
							nominal.Data()))
					}
					// Equality would also hold if the reference reached the context as zeros either way, so the
					// reference has to be shown to matter: a blank one of the same shape must decode differently.
					blank := refineWith(t, NewImage(regionW, regionH+2), regionW, regionH, 0, template, tpgron)
					if bytes.Equal(offset.Data(), blank.Data()) {
						t.Fatal("the same page decoded over a blank reference; the reference is not reaching the " +
							"context, so the comparison above proves nothing")
					}
				})
			}
		}
	}
}

// TestRefinementTemplate0OptMatchesUnopt pins the same claim against the package's own unoptimized template-0 body,
// which is live code (an arbitrary adaptive-template pixel selects it) and computes the context pixel by pixel
// through GetPixel. With the adaptive pixels at their nominal positions the two bodies must agree exactly, whatever
// the reference offset and width — the equivalence the ungated dispatch rests on.
func TestRefinementTemplate0OptMatchesUnopt(t *testing.T) {
	const regionW, regionH = 29, 9
	for _, dx := range []int32{0, 2, 7, 16} {
		for _, dy := range []int32{0, 1, -2} {
			t.Run(fmt.Sprintf("dx%d_dy%d", dx, dy), func(t *testing.T) {
				reference := patternImage(regionW-5, regionH+1, 0xb3)
				proc := func() *GRRDProc {
					return &GRRDProc{
						GRW:           regionW,
						GRH:           regionH,
						GRREFERENCE:   reference,
						GRREFERENCEDX: dx,
						GRREFERENCEDY: dy,
						GRAT:          [4]int8{-1, -1, -1, -1},
					}
				}
				opt, err := proc().decodeTemplate0Opt(NewArithDecoder(NewBitStream(refinementStream(), 0)),
					make([]ArithCtx, 8192), nil)
				if err != nil {
					t.Fatalf("optimized body failed: %v", err)
				}
				unopt, err := proc().decodeTemplate0Unopt(NewArithDecoder(NewBitStream(refinementStream(), 0)),
					make([]ArithCtx, 8192), nil)
				if err != nil {
					t.Fatalf("unoptimized body failed: %v", err)
				}
				if !bytes.Equal(opt.Data(), unopt.Data()) {
					t.Fatalf("the optimized and unoptimized template-0 bodies disagree in %d bytes at "+
						"GRREFERENCEDX %d, GRREFERENCEDY %d", differingBytes(opt.Data(), unopt.Data()), dx, dy)
				}
			})
		}
	}
}

// refineWith runs one refinement decode over a fixed arithmetic stream. Feeding arbitrary bytes to the arithmetic
// decoder is deliberate: two context formations that agree consume the same decisions in the same order and produce
// the same bitmap, and two that do not diverge visibly, so no encoder is needed to compare them.
func refineWith(t *testing.T, reference *Image, w, h int, dx int32, template, tpgron bool) *Image {
	t.Helper()
	size := 8192
	if template {
		size = 1024
	}
	proc := &GRRDProc{
		GRTEMPLATE:    template,
		TPGRON:        tpgron,
		GRW:           uint32(w),
		GRH:           uint32(h),
		GRREFERENCE:   reference,
		GRREFERENCEDX: dx,
		GRAT:          [4]int8{-1, -1, -1, -1},
	}
	page, err := proc.Decode(NewArithDecoder(NewBitStream(refinementStream(), 0)), make([]ArithCtx, size))
	if err != nil {
		t.Fatalf("refinement decode failed: %v", err)
	}
	return page
}

// refinementStream is the byte string the refinement pins decode. Its content is arbitrary; what matters is that
// every decode in a comparison reads the same bytes.
func refinementStream() []byte {
	data := make([]byte, 512)
	state := uint32(0x2545f491)
	for i := range data {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		data[i] = byte(state)
	}
	return data
}

// patternImage builds a reference bitmap with a repeating, non-trivial pattern, so a context formation that reads the
// wrong column or row shows up as different output rather than as the same run of blank pixels.
func patternImage(w, h int, seed byte) *Image {
	img := NewImage(int32(w), int32(h))
	for i := range img.Data() {
		img.Data()[i] = seed ^ byte(i*31)
	}
	return img
}

// shiftedRight returns src widened to width and moved right by dx, with zeros in the vacated columns — the reference
// a decode at GRREFERENCEDX 0 must see to match a decode at GRREFERENCEDX dx over src.
func shiftedRight(src *Image, dx int32, width int) *Image {
	dst := NewImage(int32(width), src.Height())
	for y := int32(0); y < src.Height(); y++ {
		for x := int32(0); x < src.Width(); x++ {
			if src.GetPixel(x, y) != 0 {
				dst.SetPixel(x+dx, y, 1)
			}
		}
	}
	return dst
}

// boolToInt names a refinement template in a subtest name.
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// decodeEmbedded runs a PDF-embedded payload through construction and the single DecodePage call the glue makes.
func decodeEmbedded(payload, globals []byte) (*Image, error) {
	dec, err := NewEmbeddedDecoder(payload, globals, pinLimits)
	if err != nil {
		return nil, err
	}
	return dec.DecodePage()
}

// pinBitmap checks a decoded page against its pin: the dimensions in the table, then every pixel against the
// committed fixture.
func pinBitmap(t *testing.T, pin corpusPin, page *Image) {
	t.Helper()
	if page == nil {
		t.Fatal("the decode returned no page")
	}
	if int(page.Width()) != pin.w || int(page.Height()) != pin.h {
		t.Fatalf("page is %dx%d, want %dx%d", page.Width(), page.Height(), pin.w, pin.h)
	}
	path := filepath.Join("testdata", pin.fixture)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	got := pbmFor(t, page)
	if !bytes.Equal(got, want) {
		reportPBMDiff(t, path, got, want)
	}
}

// pbmFor renders a decoded page as an ASCII PBM (netpbm P1): one line per row, one character per pixel, 1 meaning
// black — PBM's convention and JBIG2's alike, a set page-bitmap pixel being ink. The text form is the point: a
// hardening change that moves pixels shows up in a diff as the pixels it moved, and the fixture can be viewed as an
// image or read directly.
//
// The page arrives packed one bit per pixel, MSB first, so this is a transcription rather than a threshold: the
// fixtures pin the decoded bits themselves.
func pbmFor(t *testing.T, page *Image) []byte {
	t.Helper()
	w, h := int(page.Width()), int(page.Height())
	stride := int(page.Stride())
	data := page.Data()
	if len(data) < stride*h {
		t.Fatalf("page holds %d bytes, want at least %d for %dx%d at stride %d", len(data), stride*h, w, h, stride)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "P1\n# pdfview pin: JBIG2 page bitmap; 1 is black.\n%d %d\n", w, h)
	row := make([]byte, w+1)
	row[w] = '\n'
	for y := range h {
		src := data[y*stride:]
		for x := range w {
			if src[x>>3]&(0x80>>(x&7)) != 0 {
				row[x] = '1'
			} else {
				row[x] = '0'
			}
		}
		buf.Write(row)
	}
	return buf.Bytes()
}

// reportPBMDiff turns a fixture mismatch into the coordinates that changed, so a failing pin says which part of the
// page moved instead of only that something did.
func reportPBMDiff(t *testing.T, path string, got, want []byte) {
	t.Helper()
	gotLines := strings.Split(string(got), "\n")
	wantLines := strings.Split(string(want), "\n")
	shorter := min(len(gotLines), len(wantLines))
	differing := 0
	first := -1
	for i := range shorter {
		if gotLines[i] != wantLines[i] {
			differing++
			if first < 0 {
				first = i
			}
		}
	}
	switch {
	case first < 0 && len(gotLines) != len(wantLines):
		t.Fatalf("%s: decoded %d lines, fixture holds %d", path, len(gotLines), len(wantLines))
	case first < pbmHeaderLines:
		t.Fatalf("%s: header line %d is %q, fixture holds %q", path, first+1, gotLines[first], wantLines[first])
	default:
		row := first - pbmHeaderLines
		detail := ""
		if len(gotLines[first]) == len(wantLines[first]) {
			col := 0
			pixels := 0
			for i := range len(gotLines[first]) {
				if gotLines[first][i] != wantLines[first][i] {
					if pixels == 0 {
						col = i
					}
					pixels++
				}
			}
			detail = fmt.Sprintf(" at column %d (differing pixels in that row: %d)", col, pixels)
		}
		t.Fatalf("%s: row %d first differs%s; differing rows: %d of %d", path, row, detail, differing,
			len(wantLines)-pbmHeaderLines-1)
	}
}

// differingBytes counts the bytes two equal-length planes disagree on, for failure messages.
func differingBytes(a, b []byte) int {
	count := 0
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			count++
		}
	}
	return count + max(len(a), len(b)) - min(len(a), len(b))
}

// pinNamed returns the table entry with the given name, so the surgery-site and entry-shape tests reuse one set of
// expected dimensions and fixtures.
func pinNamed(t *testing.T, name string) *corpusPin {
	t.Helper()
	for i := range corpusPins {
		if corpusPins[i].name == name {
			return &corpusPins[i]
		}
	}
	t.Fatalf("no pin named %s", name)
	return nil
}

// corpusPayload returns one scanned payload and its globals, failing when the corpus no longer holds it.
func corpusPayload(t *testing.T, found map[string]embeddedPayload, file string, obj int) (payload, globals []byte) {
	t.Helper()
	entry, ok := found[corpusKey(file, obj)]
	if !ok {
		t.Fatalf("%s holds no JBIG2 payload in object %d", file, obj)
	}
	return entry.payload, entry.globals
}

// corpusKey names one payload by the file and object number it came from.
func corpusKey(file string, obj int) string { return fmt.Sprintf("%s#%d", file, obj) }

// embeddedPayload is one corpus stream's JBIG2 bytes with the /JBIG2Globals bytes its /DecodeParms named, if any.
type embeddedPayload struct {
	payload []byte
	globals []byte
}

// corpusJBIG2Payloads extracts every JBIG2 payload the image corpus carries, taken through the same filter split
// production uses so the bytes handed to the decoder here are the bytes the glue hands it.
func corpusJBIG2Payloads(t *testing.T) map[string]embeddedPayload {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "testfiles", "corpus", "images-jbig2*.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]embeddedPayload, len(paths))
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		d, openErr := cos.Open(data)
		if openErr != nil {
			t.Fatalf("%s: %v", path, openErr)
		}
		for _, num := range d.ObjectNums() {
			stream, isStream := cos.AsStream(d.LoadObject(num))
			if !isStream {
				continue
			}
			payload, codec, parms, splitErr := d.ImageFilterSplit(stream.Dict, stream.Raw)
			if splitErr != nil || codec != jbig2Codec {
				continue
			}
			entry := embeddedPayload{payload: payload}
			if globals, hasGlobals := d.GetStream(parms, "JBIG2Globals"); hasGlobals {
				decoded, globalsErr := d.StreamData(globals)
				if globalsErr != nil {
					t.Fatalf("%s: object %d names /JBIG2Globals that will not decode: %v", path, num, globalsErr)
				}
				entry.globals = decoded
			}
			found[corpusKey(filepath.Base(path), num)] = entry
		}
	}
	if len(found) == 0 {
		t.Fatal("the image corpus holds no JBIG2 payloads")
	}
	return found
}
