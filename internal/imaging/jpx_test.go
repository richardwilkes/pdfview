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
	"errors"
	"testing"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// TestJPXNormalize pins the oracle's depth rule against the two alternatives the corpus's deliberate sub-8-bit ramp
// separates it from: MuPDF shifts, so a precision above 8 truncates its low bits (never rounds, never rescales into the
// full 8-bit range) and one below 8 left-shifts, leaving a 4-bit component's maximum sample at 240 rather than 255.
// Samples arrive centered on zero, so each case is the signed-domain value the decoder hands over.
func TestJPXNormalize(t *testing.T) {
	for _, tc := range []struct {
		name      string
		precision int
		sample    int32
		want      byte
	}{
		{name: "4-bit min", precision: 4, sample: -8, want: 0},
		{name: "4-bit mid", precision: 4, sample: 0, want: 128},
		{name: "4-bit max", precision: 4, sample: 7, want: 240},
		{name: "4-bit under range", precision: 4, sample: -1000, want: 0},
		{name: "4-bit over range", precision: 4, sample: 1000, want: 240},
		{name: "8-bit min", precision: 8, sample: -128, want: 0},
		{name: "8-bit mid", precision: 8, sample: 0, want: 128},
		{name: "8-bit max", precision: 8, sample: 127, want: 255},
		{name: "12-bit min", precision: 12, sample: -2048, want: 0},
		{name: "12-bit mid", precision: 12, sample: 0, want: 128},
		{name: "12-bit max", precision: 12, sample: 2047, want: 255},
		// 2063 >> 4 is 128; rounding the discarded bits would give 129 and rescaling 4095ths onto 255ths would give 129.
		{name: "12-bit truncates low bits", precision: 12, sample: 15, want: 128},
		// 4079 >> 4 is 254, where rescaling would reach 255: the 12-bit maximum is the only value that renders white.
		{name: "12-bit near max truncates", precision: 12, sample: 2031, want: 254},
		{name: "16-bit min", precision: 16, sample: -32768, want: 0},
		{name: "16-bit mid", precision: 16, sample: 0, want: 128},
		{name: "16-bit max", precision: 16, sample: 32767, want: 255},
		{name: "16-bit truncates low bits", precision: 16, sample: 255, want: 128},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newJPXNorm(tc.precision).at(tc.sample); got != tc.want {
				t.Fatalf("precision %d sample %d normalized to %d, want %d", tc.precision, tc.sample, got, tc.want)
			}
		})
	}
}

// TestJPXNormalizeIgnoresSignedness pins the decision behind jpxNorm taking only a precision: the codestream decoder
// documents signed and unsigned components alike as arriving centered on zero with the encoder's DC level shift still
// subtracted, so the same offset restores both and the Ssiz sign bit changes nothing.
func TestJPXNormalizeIgnoresSignedness(t *testing.T) {
	samples := []int32{-8, -1, 0, 7}
	for _, precision := range []int{4, 8, 12, 16} {
		unsigned, err := jpxRasterOf([]j2k.Component{
			{W: 4, H: 1, Precision: precision, Samples: samples},
		}, maxImagePixels)
		if err != nil {
			t.Fatalf("precision %d unsigned: %v", precision, err)
		}
		signed, err := jpxRasterOf([]j2k.Component{
			{W: 4, H: 1, Precision: precision, Signed: true, Samples: samples},
		}, maxImagePixels)
		if err != nil {
			t.Fatalf("precision %d signed: %v", precision, err)
		}
		for i := range unsigned.samples {
			if unsigned.samples[i] != signed.samples[i] {
				t.Fatalf("precision %d sample %d: unsigned %d against signed %d",
					precision, i, unsigned.samples[i], signed.samples[i])
			}
		}
	}
}

// TestJPXGrayPlaneMean pins the soft-mask reduction of a three-component payload as the plain mean of the three,
// truncated. The oracle uses no luminance weighting here: against images-jpx-smask.pdf a 77/150/28 model misses by mean
// 9.4 and max 60 of 255, and it would read the first case below as 1 only by accident of its weights.
func TestJPXGrayPlaneMean(t *testing.T) {
	for _, tc := range []struct {
		rgb  [3]byte
		want byte
	}{
		{rgb: [3]byte{1, 1, 2}, want: 1},
		{rgb: [3]byte{0, 0, 2}, want: 0},
		{rgb: [3]byte{10, 20, 31}, want: 20},
		{rgb: [3]byte{255, 0, 0}, want: 85},
		{rgb: [3]byte{0, 255, 0}, want: 85},
		{rgb: [3]byte{0, 0, 255}, want: 85},
		{rgb: [3]byte{255, 255, 255}, want: 255},
	} {
		raster := &jpxRaster{samples: tc.rgb[:], w: 1, h: 1, ncomp: 3}
		if got := raster.grayPlane(); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%v reduced to %v, want [%d]", tc.rgb, got, tc.want)
		}
	}
	// The single-component arm hands back the samples themselves, untouched.
	one := &jpxRaster{samples: []byte{3, 7}, w: 2, h: 1, ncomp: 1}
	if got := one.grayPlane(); len(got) != 2 || got[0] != 3 || got[1] != 7 {
		t.Fatalf("a one-component raster reduced to %v", got)
	}
}

// TestJPXComponentsGate pins which payloads leave the decoder's own rendering for the raw component planes. The
// components path applies none of a JP2 container's palette, `cdef`, or color-space machinery, so it may only take over
// where the PDF layer discards that machinery (the /Indexed override) or where an odd precision leaves the image path
// no correct answer and the container carries nothing the path would drop. Every combination below has corpus goldens
// behind it, which is what keeps the gate from widening on intuition.
func TestJPXComponentsGate(t *testing.T) {
	const depth = "images-jpx-depth.pdf"
	for _, tc := range []struct {
		file    string
		why     string
		stream  int
		indexed bool
		want    bool
	}{
		{file: depth, stream: 0, want: true, why: "12-bit gray"},
		{file: depth, stream: 1, want: true, why: "12-bit RGB"},
		{file: depth, stream: 2, want: false, why: "16-bit gray, which the image path truncates right"},
		{file: depth, stream: 3, want: true, why: "4-bit gray"},
		{file: "images-jpx-ixjp2.pdf", stream: 0, indexed: true, want: true, why: "palettized JP2 under /Indexed"},
		{file: "images-jpx-ixjp2.pdf", stream: 1, want: false, why: "the same payload keeping its container palette"},
		{file: "images-jpx-53.pdf", stream: 0, want: false, why: "8-bit RGB"},
		{file: "images-jpx-53.pdf", stream: 0, indexed: true, want: false, why: "8-bit RGB, three components"},
		{file: "images-jpx-ycc.pdf", stream: 0, want: false, why: "sYCC"},
		{file: "images-jpx-alpha1.pdf", stream: 0, want: false, why: "a cdef opacity channel"},
		{file: "images-jpx-raw.pdf", stream: 0, want: false, why: "a three-component bare codestream"},
		{file: "images-jpx-csoverride.pdf", stream: 2, indexed: true, want: true, why: "a bare index codestream"},
	} {
		d, streams := corpusImageStreams(t, tc.file)
		var payloads [][]byte
		for _, stream := range streams {
			if data, codec, _, err := d.ImageFilterSplit(stream.Dict, stream.Raw); err == nil && isJPX(codec) {
				payloads = append(payloads, data)
			}
		}
		if tc.stream >= len(payloads) {
			t.Fatalf("%s has %d JPX payloads, want more than %d", tc.file, len(payloads), tc.stream)
		}
		payload := payloads[tc.stream]
		bare := jpxIsCodestream(payload)
		cfg, err := jpxConfig(payload, bare)
		if err != nil {
			t.Fatalf("%s stream %d: %v", tc.file, tc.stream, err)
		}
		if got := jpxWantsComponents(payload, bare, cfg, tc.indexed); got != tc.want {
			t.Fatalf("%s stream %d (%s) with indexed=%v routed to components=%v, want %v",
				tc.file, tc.stream, tc.why, tc.indexed, got, tc.want)
		}
	}
}

// TestJPXIndexedSuppressesContainerPalette pins both halves of images-jpx-ixjp2.pdf, whose two arms wrap the same
// palettized JP2 container. Under an /Indexed PDF space the container's own pclr/cmap palette is suppressed and the
// codestream's samples are the PDF lookup table's indices; under /DeviceGray the container's palette applies and the
// resulting three-component payload beats the one-component override on the usual count mismatch, so that arm renders
// exactly as the same payload does with no /ColorSpace at all (images-jpx-palette.pdf).
func TestJPXIndexedSuppressesContainerPalette(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-jpx-ixjp2.pdf")
	if len(streams) != 2 {
		t.Fatalf("expected the /Indexed and /DeviceGray pair, got %d image streams", len(streams))
	}
	indexed, err := DecodeXObject(d, streams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.Width != 64 || indexed.Height != 64 || len(indexed.Pix) != 64*64*4 {
		t.Fatalf("decoded %dx%d with %d pixel bytes", indexed.Width, indexed.Height, len(indexed.Pix))
	}
	// The dictionary's eight-entry lookup, which deliberately shares no color with the container's pclr box.
	lookup := map[[3]byte]bool{
		{0x00, 0x00, 0x40}: true, {0x00, 0x40, 0x80}: true, {0x00, 0x80, 0x80}: true, {0x00, 0xc0, 0x60}: true,
		{0x80, 0xc0, 0x00}: true, {0xc0, 0x80, 0x00}: true, {0xe0, 0x40, 0x40}: true, {0xff, 0xc0, 0xe0}: true,
	}
	distinct := map[[3]byte]bool{}
	for p := 0; p < len(indexed.Pix); p += 4 {
		c := [3]byte(indexed.Pix[p : p+3])
		if !lookup[c] {
			t.Fatalf("pixel %d is %v, which is not a PDF lookup entry; the container's palette was applied", p/4, c)
		}
		distinct[c] = true
	}
	if len(distinct) != len(lookup) {
		t.Fatalf("only %d of the 8 lookup entries appear; the index samples did not survive", len(distinct))
	}
	if got := [3]byte(indexed.Pix[:3]); got != [3]byte{0x00, 0x00, 0x40} {
		t.Fatalf("first pixel is %v, want lookup entry 0", got)
	}
	// The /DeviceGray arm keeps the container's palette; the same payload with no /ColorSpace must match it exactly.
	gray, err := DecodeXObject(d, streams[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	bare, bareStreams := corpusImageStreams(t, "images-jpx-palette.pdf")
	if len(bareStreams) != 1 {
		t.Fatalf("expected one image stream, got %d", len(bareStreams))
	}
	plain, err := DecodeXObject(bare, bareStreams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gray.Pix) != len(plain.Pix) {
		t.Fatalf("the /DeviceGray arm decoded %d pixel bytes against the palette file's %d", len(gray.Pix),
			len(plain.Pix))
	}
	for i := range gray.Pix {
		if gray.Pix[i] != plain.Pix[i] {
			t.Fatalf("the /DeviceGray arm diverges from the container's own palette at pixel byte %d: %d vs %d",
				i, gray.Pix[i], plain.Pix[i])
		}
	}
}

// TestJPXImageMaskPaintsImage pins the stencil posture: /ImageMask true on a JPXDecode XObject is ignored outright, so
// the payload paints as an ordinary opaque image rather than as coverage the device tints with the fill paint, even
// though the dictionary carries neither /ColorSpace nor /BitsPerComponent. The payload is the byte-identical one
// images-jpx-gray.pdf renders through /DeviceGray, so the two must agree pixel for pixel.
func TestJPXImageMaskPaintsImage(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-jpx-stencil.pdf")
	if len(streams) != 1 {
		t.Fatalf("expected one image stream, got %d", len(streams))
	}
	img, err := DecodeXObject(d, streams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Stencil {
		t.Fatal("the image decoded as a stencil; /ImageMask must be ignored for JPXDecode")
	}
	if img.HasAlpha {
		t.Fatal("the image reported alpha; the oracle paints the payload opaque")
	}
	if img.Width != 64 || img.Height != 64 || len(img.Pix) != 64*64*4 {
		t.Fatalf("decoded %dx%d with %d pixel bytes", img.Width, img.Height, len(img.Pix))
	}
	grayDoc, grayStreams := corpusImageStreams(t, "images-jpx-gray.pdf")
	gray, err := DecodeXObject(grayDoc, grayStreams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range img.Pix {
		if img.Pix[i] != gray.Pix[i] {
			t.Fatalf("pixel byte %d is %d against the /DeviceGray rendering's %d", i, img.Pix[i], gray.Pix[i])
		}
	}
	// The /Mask stencil-stream path keeps declining the codec: no oracle evidence covers it.
	sub := &decoder{d: d, dict: streams[0].Dict, codec: codecJPXNames}
	if _, err = sub.stencilPlane(64, 64); !errors.Is(err, ErrUnsupportedCodec) {
		t.Fatalf("stencilPlane returned %v, want ErrUnsupportedCodec", err)
	}
}

// TestJPXDepthShifts pins the depth rule end to end against images-jpx-depth.pdf, whose four arms are 12-bit gray,
// 12-bit RGB, 16-bit gray and 4-bit gray, every dictionary declaring /BitsPerComponent 8 and no /ColorSpace so the
// codestream alone decides. The 4-bit arm is the load-bearing one: its source covers the full 0..15 range, and a
// left-shift caps it at 240 while a rescale would reach 255.
func TestJPXDepthShifts(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-jpx-depth.pdf")
	if len(streams) != 4 {
		t.Fatalf("expected the four depth arms, got %d image streams", len(streams))
	}
	// The 4-bit arm can only ever show its 16 levels; the deeper arms carry the 8-bit pattern in their high bits.
	for i, want := range []int{32, 32, 32, 12} {
		img, err := DecodeXObject(d, streams[i], nil)
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if img.Width != 64 || img.Height != 64 {
			t.Fatalf("stream %d: decoded %dx%d", i, img.Width, img.Height)
		}
		distinct := map[[3]byte]bool{}
		for p := 0; p < len(img.Pix); p += 4 {
			distinct[[3]byte(img.Pix[p:p+3])] = true
		}
		if len(distinct) < want {
			t.Fatalf("stream %d: only %d distinct colors; a deep payload rendered flat", i, len(distinct))
		}
	}
	// The 4-bit arm: every pixel is the /DeviceGray rendering of a sample that is a multiple of 16, and the brightest
	// stops at the rendering of 240 — the left shift can never produce 255, which a rescale would have reached.
	fourBit, err := DecodeXObject(d, streams[3], nil)
	if err != nil {
		t.Fatal(err)
	}
	mapping := jpxMapping(pdfcolor.DeviceGray)
	lut := mapping.lut(pdfcolor.DeviceGray, 8)
	shifted := map[byte]bool{}
	for s := 0; s < 256; s += 16 {
		shifted[lut[s].R] = true
	}
	if lut[255].R == lut[240].R {
		t.Fatal("/DeviceGray renders samples 240 and 255 alike; this arm cannot separate a shift from a rescale")
	}
	brightest := byte(0)
	for p := 0; p < len(fourBit.Pix); p += 4 {
		v := fourBit.Pix[p]
		if !shifted[v] {
			t.Fatalf("pixel %d is %d, which no 4-bit sample left-shifted by 4 can render as", p/4, v)
		}
		if v > brightest {
			brightest = v
		}
	}
	if brightest != lut[240].R {
		t.Fatalf("the brightest 4-bit sample renders as %d, want %d (sample 240)", brightest, lut[240].R)
	}
}
