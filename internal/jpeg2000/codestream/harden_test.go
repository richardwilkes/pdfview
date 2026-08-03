// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package codestream

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/box"
)

// buf is a tiny big-endian codestream/box byte assembler for the crafted hostile
// payloads below.
type buf struct{ b []byte }

func (w *buf) u8(v int) *buf  { w.b = append(w.b, byte(v)); return w }
func (w *buf) u16(v int) *buf { w.b = binary.BigEndian.AppendUint16(w.b, uint16(v)); return w }
func (w *buf) u32(v int) *buf { w.b = binary.BigEndian.AppendUint32(w.b, uint32(v)); return w }
func (w *buf) raw(p []byte) *buf {
	w.b = append(w.b, p...)
	return w
}

// sizSeg writes a SIZ marker segment for a single 8-bit unsigned component of the
// given dimensions in a single tile.
func sizSeg(w *buf, xsiz, ysiz int) {
	w.u16(0xFF51).u16(41).u16(0) // marker, Lsiz, Rsiz
	w.u32(xsiz).u32(ysiz).u32(0).u32(0)
	w.u32(xsiz).u32(ysiz).u32(0).u32(0) // XTsiz=Xsiz, YTsiz=Ysiz (one tile), origins 0
	w.u16(1)                            // Csiz
	w.u8(7).u8(1).u8(1)                 // Ssiz (precision 8, unsigned), XRsiz, YRsiz
}

// codSeg writes a COD marker segment with the given quality-layer count and
// decomposition levels, default single precinct per resolution, reversible 5/3.
func codSeg(w *buf, layers, levels int) {
	w.u16(0xFF52).u16(12) // marker, Lcod
	w.u8(0).u8(0)         // Scod (no precincts), progression LRCP
	w.u16(layers)         // SGcod: number of layers
	w.u8(0).u8(levels)    // MCT, decomposition levels
	w.u8(4).u8(4)         // code-block width/height exponents minus 2 (64x64)
	w.u8(0).u8(1)         // code-block style, wavelet (1 = reversible 5/3)
}

// f1LayerBomb is an 83-byte bare codestream declaring a 1024x1024 single-component
// image with COD layers=0xFFFF and 17 decomposition levels: a single precinct per
// resolution but numLayers*numResolutions = 65535*18 packets, past maxPackets. Before
// the tier2 packet-sequence bound it would size a ~35 MB [][4]int sequence (and the
// position builders would iterate it); the bound rejects it before any allocation.
func f1LayerBomb() []byte {
	w := &buf{}
	w.u16(0xFF4F) // SOC
	sizSeg(w, 1024, 1024)
	codSeg(w, 0xFFFF, 17)
	// SOT: Psot covers SOT(12) + SOD(2) + payload(8) = 22.
	w.u16(0xFF90).u16(10).u16(0).u32(22).u8(0).u8(1)
	w.u16(0xFF93)                         // SOD
	w.raw([]byte{0, 0, 0, 0, 0, 0, 0, 0}) // 8 payload bytes (never parsed)
	w.u16(0xFFD9)                         // EOC
	return w.b
}

// f3PsotBomb is a 78-byte bare codestream whose single tile-part declares
// Psot=0x0FFFFFFF (just under maxTilePartBytes) over a 64x64 image carrying only a
// handful of payload bytes. Before the SOD remaining-input bound it allocated
// make([]byte, 0x0FFFFFFF-14) = ~256 MiB up front; the bound rejects it against the
// bytes actually left in the reader.
func f3PsotBomb() []byte {
	w := &buf{}
	w.u16(0xFF4F) // SOC
	sizSeg(w, 64, 64)
	codSeg(w, 1, 5)
	w.u16(0xFF90).u16(10).u16(0).u32(0x0FFFFFFF).u8(0).u8(1) // SOT with hostile Psot
	w.u16(0xFF93)                                            // SOD
	w.raw([]byte{0, 0, 0, 0, 0})                             // 5 payload bytes
	return w.b
}

// f2CmapBomb is a ~41 KB JP2 container over a 2048x2048 single-component image whose
// jp2h carries a pclr plus a cmap declaring 10240 output channels. Each channel costs a
// full 2048x2048 int32 plane in applyPalette, so unguarded it would allocate ~160 GB of
// planes; parseCmap refuses a cmap longer than maxCMapChannels, so the container falls
// back to its non-palette handling.
func f2CmapBomb() []byte {
	// jp2c codestream: SOC, SIZ, COD, QCD (reversible), a Psot=0 tile-part with no packet
	// data (so the tile fills as zero coefficients), EOC.
	cs := &buf{}
	cs.u16(0xFF4F)
	sizSeg(cs, 2048, 2048)
	codSeg(cs, 1, 5)
	// QCD: reversible (style 0), one epsilon byte per subband (1 + 3*levels = 16).
	cs.u16(0xFF5C).u16(19).u8(0)
	cs.raw(make([]byte, 16))
	cs.u16(0xFF90).u16(10).u16(0).u32(0).u8(0).u8(1) // SOT Psot=0 (runs to EOC)
	cs.u16(0xFF93)                                   // SOD
	cs.u16(0xFFD9)                                   // EOC

	// pclr sub-box: 1 entry, 1 column, 8-bit depth.
	pclr := []byte{0x00, 0x01, 0x01, 0x07, 0x00}
	// cmap sub-box: 10240 entries of 4 bytes each. The content is never interpreted (the
	// cmap is refused for length before any entry is read), so a printable filler keeps
	// the committed seed file compact.
	const cmapChannels = 10240
	cmapContent := bytes.Repeat([]byte{' '}, cmapChannels*4)

	jp2h := &buf{}
	jp2h.u32(8 + len(pclr)).u32(0x70636C72).raw(pclr)               // pclr box
	jp2h.u32(8 + len(cmapContent)).u32(0x636D6170).raw(cmapContent) // cmap box

	out := &buf{}
	out.u32(8 + len(jp2h.b)).u32(0x6A703268).raw(jp2h.b) // jp2h superbox
	out.u32(8 + len(cs.b)).u32(0x6A703263).raw(cs.b)     // jp2c box
	return out.b
}

// TestPacketSequenceBoundRejectsLayerBomb pins F1: the layer/resolution product bomb is
// rejected by the packet-sequence bound rather than sizing a multi-megabyte sequence.
func TestPacketSequenceBoundRejectsLayerBomb(t *testing.T) {
	var d Decoder
	_, err := d.Decode(bytes.NewReader(f1LayerBomb()), false)
	if err == nil {
		t.Fatal("expected the layer bomb to be rejected")
	}
	if !strings.Contains(err.Error(), "too many packets") {
		t.Fatalf("expected a packet-count rejection, got %v", err)
	}
}

// TestTilePartExceedingInputRejected pins F3: a Psot larger than the bytes left in the
// reader is refused before the up-front payload allocation.
func TestTilePartExceedingInputRejected(t *testing.T) {
	var d Decoder
	_, err := d.Decode(bytes.NewReader(f3PsotBomb()), false)
	if err == nil {
		t.Fatal("expected the Psot bomb to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds remaining input") {
		t.Fatalf("expected a remaining-input rejection, got %v", err)
	}
}

// TestOversizedCmapRefused pins the box half of F2: a cmap longer than maxCMapChannels is
// dropped at parse, leaving the palette present but the mapping empty so the container
// decodes without palette expansion.
func TestOversizedCmapRefused(t *testing.T) {
	info, err := box.ParseJP2(bytes.NewReader(f2CmapBomb()))
	if err != nil {
		t.Fatalf("container parse failed: %v", err)
	}
	if info.Palette == nil {
		t.Fatal("expected the pclr palette to be parsed")
	}
	if info.CMap != nil {
		t.Fatalf("expected the oversized cmap to be refused, got %d entries", len(info.CMap))
	}
}

// TestApplyPaletteChannelCap pins the decoder half of F2: applyPalette refuses a cmap
// with more output channels than maxOutputChannels before allocating any plane.
func TestApplyPaletteChannelCap(t *testing.T) {
	var d Decoder
	d.header.siz.Components = []Component{{Precision: 8}}
	d.paletteEntries = [][]int32{{0}}
	d.paletteDepths = []int{8}
	d.cmap = make([]CMapEntry, maxOutputChannels+1)
	// A modest w*h keeps the test cheap; unguarded this would allocate 33 planes of it.
	if _, _, err := d.applyPalette([][]int32{make([]int32, 16)}, 4, 4); err == nil {
		t.Fatal("expected applyPalette to refuse an over-long cmap")
	}
}

// TestWriteFuzzSeeds regenerates the FuzzJPX regression seeds from the canonical payload
// builders above. It is gated on WRITE_FUZZ_SEEDS so ordinary test runs never touch the
// corpus; run it once (WRITE_FUZZ_SEEDS=1 go test -run TestWriteFuzzSeeds ./...) to
// refresh the committed seed files.
func TestWriteFuzzSeeds(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_SEEDS") == "" {
		t.Skip("set WRITE_FUZZ_SEEDS=1 to (re)generate the FuzzJPX seed files")
	}
	dir := filepath.Join("..", "..", "imaging", "testdata", "fuzz", "FuzzJPX")
	for name, payload := range map[string][]byte{
		"jpx_f1_packet_seq_bomb.seed": f1LayerBomb(),
		"jpx_f3_psot_bomb.seed":       f3PsotBomb(),
		"jpx_f2_cmap_bomb.seed":       f2CmapBomb(),
	} {
		body := "go test fuzz v1\n[]byte(" + strconv.Quote(string(payload)) + ")\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
