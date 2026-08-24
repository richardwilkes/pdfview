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
	"os"
	"path/filepath"
	"testing"
)

// The two vendored codecs are the engine's most expensive decoders, and no corpus payload is large enough to measure:
// every JBIG2 and JPX image in testfiles/corpus decodes to under 14 thousand pixels. The fixtures under testdata/bench
// are page-scale instead — a 300 dpi US Letter bilevel scan and a megapixel continuous-tone image — so the numbers
// here are the per-page decode cost a real document pays. Provenance and the exact commands are in
// testdata/bench/README.md.
//
// Every benchmark decodes through the entry point production reaches (decodeJBIG2Plane, jpxRasterFor), which the fuzz
// targets also drive, so the payload caps, the segment/SIZ guards, and the panic recovery are inside the measurement.
// Each iteration is a full cold decode.

// benchJBIG2Height is the dictionary /Height the JBIG2 plane is produced at. It matches the fixture's own page height,
// so the measurement is the decode rather than the crop-and-white-fill path a mismatched height would exercise.
const benchJBIG2Height = 3300

// benchJBIG2Width is the fixture's encoded page width, checked so a swapped payload cannot quietly change what the
// throughput figure is per pixel of.
const benchJBIG2Width = 2550

// benchJPXDim is the square edge of both JPEG 2000 fixtures.
const benchJPXDim = 1024

// benchFixture reads one committed benchmark payload.
func benchFixture(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "bench", name))
	if err != nil {
		b.Fatal(err)
	}
	return data
}

// BenchmarkJBIG2Decode measures a cold decode of a 2550x3300 bilevel page whose symbol dictionary is shared across
// thousands of text-region placements, the shape a scanned page takes. The reported MB/s is megapixels per second:
// b.SetBytes is given the decoded pixel count, since payload bytes say nothing comparable across codecs at these
// compression ratios. The decode also has to fit maxPixelsFor(len(payload)): symbol reuse compresses a text page far
// past what the cap's CCITT-derived rationale assumes, so the fixture spends a real fraction of its allowance, and the
// check below would catch a future payload that no longer fits.
func BenchmarkJBIG2Decode(b *testing.B) {
	payload := benchFixture(b, "scanned-text.jbig2")
	plane, cols, err := decodeJBIG2Plane(payload, nil, benchJBIG2Height)
	if err != nil {
		b.Fatal(err)
	}
	if cols != benchJBIG2Width {
		b.Fatalf("decoded %d columns, want %d", cols, benchJBIG2Width)
	}
	pixels := int64(cols) * benchJBIG2Height
	if budget := maxPixelsFor(len(payload)); pixels > budget {
		b.Fatalf("%d pixels against a budget of %d for %d payload bytes", pixels, budget, len(payload))
	}
	// A payload whose region data the decoder rejects comes back as an all-white page with a nil error, so the plane's
	// dimensions alone do not say the decode ran; the ink does. The fixture's source page is 9.87% ink.
	ink := 0
	for _, v := range plane {
		for bit := range 8 {
			if v&(0x80>>bit) == 0 {
				ink++
			}
		}
	}
	if int64(ink)*20 < pixels {
		b.Fatalf("only %d of %d pixels are ink; the page did not decode", ink, pixels)
	}
	b.SetBytes(pixels)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err = decodeJBIG2Plane(payload, nil, benchJBIG2Height); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJPXDecode measures cold decodes of a megapixel RGB payload under both wavelets: "53" is the reversible 5/3
// lossless path (the integer transform plus the reversible color transform) and "97" the irreversible 9/7 lossy one
// (the floating-point transform, dequantization, and the irreversible color transform). The lossy payload is also 2.5x
// smaller, so the two figures separate transform cost from coefficient-decoding cost. As above, MB/s is megapixels per
// second; samples are three per pixel here.
func BenchmarkJPXDecode(b *testing.B) {
	for _, tc := range []struct{ name, file string }{
		{name: "53", file: "rgb-53.jp2"},
		{name: "97", file: "rgb-97.jp2"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			payload := benchFixture(b, tc.file)
			// false is the /Indexed verdict an ordinary color image reaches jpxRasterFor with.
			raster, err := jpxRasterFor(payload, false)
			if err != nil {
				b.Fatal(err)
			}
			if raster.w != benchJPXDim || raster.h != benchJPXDim || raster.ncomp != 3 {
				b.Fatalf("decoded %dx%d at %d components, want %dx%d at 3", raster.w, raster.h, raster.ncomp,
					benchJPXDim, benchJPXDim)
			}
			pixels := int64(raster.w) * int64(raster.h)
			if want := int(pixels) * raster.ncomp; len(raster.samples) != want {
				b.Fatalf("raster holds %d samples, want %d", len(raster.samples), want)
			}
			b.SetBytes(pixels)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err = jpxRasterFor(payload, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
