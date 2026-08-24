// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// This file is pdfview-authored (MPL-2.0), not upstream code. It pins DecodeComponents and DecodeInfo — the two
// container entry points pdfview added — against the vendored vectors and their ground-truth planes. The external
// test package is deliberate: it proves the PDF glue can consume both entry points importing only jp2 and j2k.

package jp2_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/jp2"
)

// testdataDir holds the vendored vectors. Nothing here adds a fixture; every assertion is against a file upstream
// already ships with its own ground-truth planes.
const testdataDir = "../test/testdata"

// sweepBudget caps the samples the corruption sweep will let a damaged header talk it into decoding. Every vector here
// is 32x32 or smaller, so nothing legitimate comes near it; it only stops a flipped dimension byte from turning the
// sweep into a multi-gigabyte allocation.
const sweepBudget = 1 << 20

// sweepStride is how often the corruption sweep follows a damaged payload all the way through a full decode. Every
// offset still reaches DecodeInfo; only the decode behind it is sampled, so the sweep stays worth its place in every
// `build.sh -a` run under the race detector.
const sweepStride = 5

// read returns a vendored vector's bytes.
func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

// unsigned undoes the DC level shift a decoded component carries, returning the sample values as the source planes
// hold them. Every vector used here is 8-bit unsigned, so the shift is the only difference.
func unsigned(t *testing.T, c j2k.Component) []byte {
	t.Helper()
	if c.Signed || c.Precision != 8 {
		t.Fatalf("component is %d-bit signed=%v; this helper handles 8-bit unsigned only", c.Precision, c.Signed)
	}
	out := make([]byte, len(c.Samples))
	for i, s := range c.Samples {
		v := s + 128
		if v < 0 || v > 255 {
			t.Fatalf("sample %d is %d, outside the 8-bit range after the level shift", i, v)
		}
		out[i] = byte(v)
	}
	return out
}

// TestDecodeInfoGeometry checks the header report against vectors whose dimensions and component shape are known, and
// against the container decoder's own header pass. DecodeInfo reads SIZ itself rather than running that pass, so the
// cross-check is what keeps the two from drifting apart.
func TestDecodeInfoGeometry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		comps         int
		enumCS        int
	}{
		{name: "gray4x4.jp2", width: 4, height: 4, comps: 1, enumCS: 17},
		{name: "rgb32_n2.jp2", width: 32, height: 32, comps: 3, enumCS: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := jp2.DecodeInfo(bytes.NewReader(read(t, tc.name)))
			if err != nil {
				t.Fatalf("DecodeInfo: %v", err)
			}
			if info.Width != tc.width || info.Height != tc.height {
				t.Errorf("size: got %dx%d, want %dx%d", info.Width, info.Height, tc.width, tc.height)
			}
			if len(info.Components) != tc.comps {
				t.Fatalf("components: got %d, want %d", len(info.Components), tc.comps)
			}
			if info.EnumCS != tc.enumCS {
				t.Errorf("EnumCS: got %d, want %d", info.EnumCS, tc.enumCS)
			}
			for i, c := range info.Components {
				if c.Precision != 8 || c.Signed || c.XRsiz != 1 || c.YRsiz != 1 {
					t.Errorf("component %d: got precision %d signed %v subsampling %dx%d, want 8, false, 1x1",
						i, c.Precision, c.Signed, c.XRsiz, c.YRsiz)
				}
			}
		})
	}
}

// TestDecodeInfoMatchesDecodeConfig checks every vendored container: the dimensions DecodeInfo reads out of SIZ must be
// the ones the vendored header pass reports, and the component count must be the one a full component decode produces.
func TestDecodeInfoMatchesDecodeConfig(t *testing.T) {
	names, err := filepath.Glob(filepath.Join(testdataDir, "*.jp2"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no .jp2 vectors found: %v", err)
	}
	for _, path := range names {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data := read(t, filepath.Base(path))
			info, err := jp2.DecodeInfo(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("DecodeInfo: %v", err)
			}
			cfg, err := jp2.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if info.Width != cfg.Width || info.Height != cfg.Height {
				t.Errorf("size: DecodeInfo %dx%d, DecodeConfig %dx%d",
					info.Width, info.Height, cfg.Width, cfg.Height)
			}
			comps, err := jp2.DecodeComponents(bytes.NewReader(data), j2k.Options{})
			if err != nil {
				t.Fatalf("DecodeComponents: %v", err)
			}
			if len(comps) != len(info.Components) {
				t.Fatalf("component count: DecodeInfo %d, DecodeComponents %d", len(info.Components), len(comps))
			}
			for i, c := range comps {
				h := info.Components[i]
				if c.Precision != h.Precision || c.Signed != h.Signed || c.XRsiz != h.XRsiz || c.YRsiz != h.YRsiz {
					t.Errorf("component %d: DecodeInfo %+v, DecodeComponents precision %d signed %v subsampling %dx%d",
						i, h, c.Precision, c.Signed, c.XRsiz, c.YRsiz)
				}
			}
		})
	}
}

// TestDecodeComponentsPaletteIndices is the /Indexed case that motivated these entry points. pclr.jp2 stores one index
// component plus a 16-entry palette; the container decoder expands it to RGB, and DecodeComponents must not. The
// indices are proven right by running them back through the palette Info reports and reproducing the expansion the
// vector's own ground-truth file holds.
func TestDecodeComponentsPaletteIndices(t *testing.T) {
	data := read(t, "pclr.jp2")
	info, err := jp2.DecodeInfo(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeInfo: %v", err)
	}
	if info.Palette == nil {
		t.Fatal("no pclr box reported")
	}
	if info.Palette.NumEntries != 16 || info.Palette.NumColumns != 3 {
		t.Fatalf("palette: got %d entries of %d columns, want 16 of 3",
			info.Palette.NumEntries, info.Palette.NumColumns)
	}
	for i, d := range info.Palette.BitDepths {
		if d != 8 {
			t.Errorf("palette column %d: got depth %d, want 8", i, d)
		}
	}
	if len(info.CMap) != 3 {
		t.Fatalf("cmap: got %d entries, want 3", len(info.CMap))
	}
	for i, e := range info.CMap {
		if e.Type != 1 || e.Component != 0 || e.Column != i {
			t.Errorf("cmap %d: got %+v, want component 0 through palette column %d", i, e, i)
		}
	}

	comps, err := jp2.DecodeComponents(bytes.NewReader(data), j2k.Options{})
	if err != nil {
		t.Fatalf("DecodeComponents: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("components: got %d, want 1 (the index plane, not the expanded colour)", len(comps))
	}
	indices := unsigned(t, comps[0])
	if comps[0].W != info.Width || comps[0].H != info.Height {
		t.Fatalf("index plane is %dx%d, want %dx%d", comps[0].W, comps[0].H, info.Width, info.Height)
	}
	for i, v := range indices {
		if int(v) >= info.Palette.NumEntries {
			t.Fatalf("sample %d is %d, not a palette index (%d entries) — the palette was expanded",
				i, v, info.Palette.NumEntries)
		}
	}

	// pclr_rgb.raw is the expansion OpenJPEG produces, interleaved R,G,B. Applying the reported palette and cmap to the
	// reported indices has to reproduce it byte for byte.
	want := read(t, "pclr_rgb.raw")
	if len(want) != len(indices)*len(info.CMap) {
		t.Fatalf("pclr_rgb.raw is %d bytes, want %d", len(want), len(indices)*len(info.CMap))
	}
	for i, idx := range indices {
		for ch, e := range info.CMap {
			got := byte(info.Palette.Entries[idx][e.Column])
			if got != want[i*len(info.CMap)+ch] {
				t.Fatalf("pixel %d channel %d: expanded to %d, want %d", i, ch, got, want[i*len(info.CMap)+ch])
			}
		}
	}
}

// TestDecodeComponentsRGBAChannels checks the four-component container: the planes come back natively, matching the
// source the vector was generated from, and Info reports the cdef box that marks the fourth channel as opacity.
func TestDecodeComponentsRGBAChannels(t *testing.T) {
	data := read(t, "rgba.jp2")
	info, err := jp2.DecodeInfo(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeInfo: %v", err)
	}
	if len(info.Components) != 4 {
		t.Fatalf("components: got %d, want 4", len(info.Components))
	}
	if len(info.Channels) != 4 {
		t.Fatalf("cdef: got %d entries, want 4", len(info.Channels))
	}
	opacity := 0
	for _, c := range info.Channels {
		if c.Typ == 1 || c.Typ == 2 {
			opacity++
			if c.Cn != 3 {
				t.Errorf("opacity channel maps to component %d, want 3", c.Cn)
			}
		}
	}
	if opacity != 1 {
		t.Fatalf("cdef declares %d opacity channels, want 1: %+v", opacity, info.Channels)
	}

	comps, err := jp2.DecodeComponents(bytes.NewReader(data), j2k.Options{})
	if err != nil {
		t.Fatalf("DecodeComponents: %v", err)
	}
	if len(comps) != 4 {
		t.Fatalf("components: got %d, want 4", len(comps))
	}
	// rgba.raw is the source planes, component-major, one byte per sample.
	want := read(t, "rgba.raw")
	plane := info.Width * info.Height
	if len(want) != 4*plane {
		t.Fatalf("rgba.raw is %d bytes, want %d", len(want), 4*plane)
	}
	for i, c := range comps {
		if c.W != info.Width || c.H != info.Height {
			t.Fatalf("component %d is %dx%d, want %dx%d", i, c.W, c.H, info.Width, info.Height)
		}
		if got := unsigned(t, c); !bytes.Equal(got, want[i*plane:(i+1)*plane]) {
			t.Errorf("component %d does not match rgba.raw", i)
		}
	}
}

// TestDecodeComponentsSYCCUnconverted checks the color-space contract. sycc.jp2 declares sYCC, which the container
// decoder converts to RGB; DecodeComponents must hand back the stored YCC planes instead. sycc.raw is the source the
// vector was encoded from, so an exact match proves no conversion ran, and comparing against the container decoder's
// own output proves the conversion is real and was skipped rather than absent.
func TestDecodeComponentsSYCCUnconverted(t *testing.T) {
	data := read(t, "sycc.jp2")
	info, err := jp2.DecodeInfo(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeInfo: %v", err)
	}
	if info.EnumCS != 18 {
		t.Fatalf("EnumCS: got %d, want 18 (sYCC)", info.EnumCS)
	}

	comps, err := jp2.DecodeComponents(bytes.NewReader(data), j2k.Options{})
	if err != nil {
		t.Fatalf("DecodeComponents: %v", err)
	}
	if len(comps) != 3 {
		t.Fatalf("components: got %d, want 3", len(comps))
	}
	want := read(t, "sycc.raw")
	plane := info.Width * info.Height
	if len(want) != 3*plane {
		t.Fatalf("sycc.raw is %d bytes, want %d", len(want), 3*plane)
	}
	got := make([][]byte, len(comps))
	for i, c := range comps {
		got[i] = unsigned(t, c)
		if !bytes.Equal(got[i], want[i*plane:(i+1)*plane]) {
			t.Errorf("component %d does not match sycc.raw — a colour transform was applied", i)
		}
	}

	// The container decoder does convert, so its rendering must differ from the planes above; otherwise this test would
	// pass just as well if sYCC conversion had quietly stopped working everywhere.
	img, err := jp2.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	same := true
	for y := 0; y < info.Height && same; y++ {
		for x := 0; x < info.Width; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			i := y*info.Width + x
			if byte(r>>8) != got[0][i] || byte(g>>8) != got[1][i] || byte(b>>8) != got[2][i] {
				same = false
				break
			}
		}
	}
	if same {
		t.Error("the container decode equals the raw planes; sYCC conversion did not run on either path")
	}
}

// TestDecodeErrors checks the malformed-input contract: both entry points reject a payload they cannot read, and
// neither panics. The truncation sweep matters because the components-only path is not what the vendored fuzz targets
// exercise — they drive the image.Image decode.
func TestDecodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "not a JP2", data: []byte("this is not a JPEG 2000 file at all")},
		{name: "signature only", data: []byte{0x00, 0x00, 0x00, 0x0c, 0x6a, 0x50, 0x20, 0x20, 0x0d, 0x0a, 0x87, 0x0a}},
		{name: "bare codestream", data: []byte{0xff, 0x4f, 0xff, 0x51}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if info, err := jp2.DecodeInfo(bytes.NewReader(tc.data)); err == nil {
				t.Errorf("DecodeInfo accepted the payload: %+v", info)
			}
			if comps, err := jp2.DecodeComponents(bytes.NewReader(tc.data), j2k.Options{}); err == nil {
				t.Errorf("DecodeComponents accepted the payload: %d components", len(comps))
			}
		})
	}

	names, err := filepath.Glob(filepath.Join(testdataDir, "*.jp2"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no .jp2 vectors found: %v", err)
	}
	for _, path := range names {
		t.Run("damaged "+filepath.Base(path), func(t *testing.T) {
			data := read(t, filepath.Base(path))
			// Every prefix, plus a single flipped byte at the same offset, so both the container walk and the codestream
			// parse meet damage at every position. DecodeInfo sees them all; a full decode costs far more under the
			// race detector, so it takes a strided sample. The stride is odd so it does not settle onto one column of
			// the four-byte header fields.
			for n := range len(data) {
				_, _ = jp2.DecodeInfo(bytes.NewReader(data[:n]))
				bad := bytes.Clone(data)
				bad[n] ^= 0xff
				info, infoErr := jp2.DecodeInfo(bytes.NewReader(bad))
				if n%sweepStride != 0 {
					continue
				}
				// A prefix can only shrink what the header declares, so decoding one is always cheap.
				_, _ = jp2.DecodeComponents(bytes.NewReader(data[:n]), j2k.Options{})
				// A flipped byte in SIZ can declare a gigapixel image, and the codestream decoder sizes its buffers from
				// the declared dimensions with no cap of its own, so the sweep gates the decode on the header report —
				// the contract DecodeInfo exists to serve.
				if infoErr != nil || int64(info.Width)*int64(info.Height)*int64(len(info.Components)) > sweepBudget {
					continue
				}
				_, _ = jp2.DecodeComponents(bytes.NewReader(bad), j2k.Options{})
			}
		})
	}
}
