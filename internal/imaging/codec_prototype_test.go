// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package imaging

import "testing"

// TestJBIG2CorpusDecode decodes the arithmetic generic-region corpus file and checks that both images come back at
// their dictionary dimensions carrying real ink. The bit polarity is the load-bearing part: the corpus bitmaps are
// marks on white (testfiles/corpus/README.md), so ink must be the minority of the page, which an inverted decode
// fails.
func TestJBIG2CorpusDecode(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-jbig2-generic.pdf")
	if len(streams) != 2 {
		t.Fatalf("expected the arithmetic and MMR pair, got %d image streams", len(streams))
	}
	for i, want := range []struct{ w, h int }{{w: 128, h: 80}, {w: 96, h: 64}} {
		img, err := DecodeXObject(d, streams[i], nil)
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if img.Width != want.w || img.Height != want.h {
			t.Fatalf("stream %d: decoded %dx%d, want %dx%d", i, img.Width, img.Height, want.w, want.h)
		}
		if img.Stencil || len(img.Pix) != want.w*want.h*4 {
			t.Fatalf("stream %d: stencil=%v with %d pixel bytes", i, img.Stencil, len(img.Pix))
		}
		black, white := 0, 0
		for p := 0; p < len(img.Pix); p += 4 {
			switch img.Pix[p] {
			case 0:
				black++
			case 255:
				white++
			}
		}
		if black == 0 || white == 0 {
			t.Fatalf("stream %d: %d black and %d white pixels; the page did not decode", i, black, white)
		}
		if black+white != want.w*want.h {
			t.Fatalf("stream %d: %d of %d pixels are neither black nor white", i, want.w*want.h-black-white,
				want.w*want.h)
		}
		if black >= white {
			t.Fatalf("stream %d: %d black against %d white; marks on white must leave ink the minority",
				i, black, white)
		}
	}
}

// TestJBIG2CorpusStencil decodes the same payload in its ImageMask role: images-jbig2-stencil.pdf carries the
// byte-identical payload images-jbig2-generic.pdf renders through /DeviceGray, so the stencil must mark exactly the
// pixels that render black there, which pins the default /Decode [0 1] against the glue's bit polarity.
func TestJBIG2CorpusStencil(t *testing.T) {
	grayDoc, grayStreams := corpusImageStreams(t, "images-jbig2-generic.pdf")
	gray, err := DecodeXObject(grayDoc, grayStreams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	d, streams := corpusImageStreams(t, "images-jbig2-stencil.pdf")
	if len(streams) == 0 {
		t.Fatal("no image streams")
	}
	img, err := DecodeXObject(d, streams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !img.Stencil || img.Width != gray.Width || img.Height != gray.Height || len(img.Pix) != 128*80 {
		t.Fatalf("decoded %dx%d stencil=%v with %d bytes", img.Width, img.Height, img.Stencil, len(img.Pix))
	}
	marked := 0
	for i, v := range img.Pix {
		if v == 255 {
			marked++
		}
		if ink := gray.Pix[i*4] == 0; ink != (v == 255) {
			t.Fatalf("sample %d: stencil coverage %d against gray level %d", i, v, gray.Pix[i*4])
		}
	}
	if marked == 0 || marked == len(img.Pix) {
		t.Fatalf("%d of %d samples marked; the stencil did not decode", marked, len(img.Pix))
	}
}

// TestJPXCorpusDecode decodes the lossless 5/3 RGB corpus file. The payload's own dimensions are authoritative and its
// first sample is the corpus generator's saturated red bar, so both a blank decode and a mis-ordered component
// pipeline are caught.
func TestJPXCorpusDecode(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-jpx-53.pdf")
	if len(streams) != 1 {
		t.Fatalf("expected one image stream, got %d", len(streams))
	}
	img, err := DecodeXObject(d, streams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 64 || img.Height != 64 || len(img.Pix) != 64*64*4 {
		t.Fatalf("decoded %dx%d with %d pixel bytes", img.Width, img.Height, len(img.Pix))
	}
	if got := [4]byte(img.Pix[:4]); got != [4]byte{255, 0, 0, 255} {
		t.Fatalf("first pixel is %v, want opaque red", got)
	}
	distinct := map[[3]byte]bool{}
	for p := 0; p < len(img.Pix); p += 4 {
		distinct[[3]byte(img.Pix[p:p+3])] = true
	}
	if len(distinct) < 64 {
		t.Fatalf("only %d distinct colors; the gradient did not decode", len(distinct))
	}
	if img.HasAlpha {
		t.Fatal("an RGB payload with no opacity channel reported alpha")
	}
}

// TestJPXCorpusIndexedOverride decodes the /ColorSpace override arms: a bare single-component codestream under an
// /Indexed space must read its samples as palette indices, and the /Decode [7 0] arm must render identically because
// the oracle ignores /Decode for this codec.
func TestJPXCorpusIndexedOverride(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-jpx-csoverride.pdf")
	if len(streams) != 4 {
		t.Fatalf("expected the four override arms, got %d image streams", len(streams))
	}
	plain, err := DecodeXObject(d, streams[2], nil)
	if err != nil {
		t.Fatal(err)
	}
	flipped, err := DecodeXObject(d, streams[3], nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Width != 64 || plain.Height != 64 {
		t.Fatalf("decoded %dx%d", plain.Width, plain.Height)
	}
	if got := [4]byte(plain.Pix[:4]); got != [4]byte{255, 0, 0, 255} {
		t.Fatalf("first pixel is %v, want palette entry 0 (opaque red)", got)
	}
	for i := range plain.Pix {
		if plain.Pix[i] != flipped.Pix[i] {
			t.Fatalf("/Decode [7 0] changed pixel byte %d from %d to %d; it must be ignored for JPXDecode",
				i, plain.Pix[i], flipped.Pix[i])
		}
	}
	// The /DeviceGray arm names one component against a three-component codestream, so the payload's own space wins
	// and it renders exactly like the /DeviceRGB arm over the same payload.
	rgb, err := DecodeXObject(d, streams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	gray, err := DecodeXObject(d, streams[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rgb.Pix {
		if rgb.Pix[i] != gray.Pix[i] {
			t.Fatalf("the component-count mismatch was honored at pixel byte %d: %d vs %d", i, rgb.Pix[i], gray.Pix[i])
		}
	}
}

// TestJBIG2TruncatedPaintsWhite pins the truncation leniency: the corpus payload is cut inside a region segment whose
// declared length the page never reaches, and the oracle paints a full-size all-white image rather than dropping it.
func TestJBIG2TruncatedPaintsWhite(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-jbig2-truncated.pdf")
	if len(streams) != 1 {
		t.Fatalf("expected one image stream, got %d", len(streams))
	}
	img, err := DecodeXObject(d, streams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 128 || img.Height != 80 {
		t.Fatalf("decoded %dx%d, want the page information segment's 128x80", img.Width, img.Height)
	}
	for p := 0; p < len(img.Pix); p += 4 {
		if img.Pix[p] != 255 || img.Pix[p+1] != 255 || img.Pix[p+2] != 255 || img.Pix[p+3] != 255 {
			t.Fatalf("pixel %d is %v, want opaque white", p/4, img.Pix[p:p+4])
		}
	}
}

// TestJPXTruncatedDeclines pins the other truncation posture: the oracle runs its JPEG 2000 decoder strictly, so a
// codestream cut in half is a hard failure and the image is dropped rather than partly painted.
func TestJPXTruncatedDeclines(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-jpx-truncated.pdf")
	if len(streams) != 2 {
		t.Fatalf("expected the JP2 and bare-codestream pair, got %d image streams", len(streams))
	}
	for i, stream := range streams {
		if _, err := DecodeXObject(d, stream, nil); err == nil {
			t.Fatalf("stream %d decoded; a truncated codestream must be declined", i)
		}
	}
}
