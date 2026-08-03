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

	"github.com/richardwilkes/pdfview/internal/cos"
)

// FuzzJBIG2 drives raw JBIG2Decode payloads and /JBIG2Globals bytes through decodeJBIG2Plane, the same entry point the
// three switch sites reach, so the segment-header scan, the pixel budget, and the panic recovery wrap the third-party
// decoder exactly as they do in production. The third argument is the dictionary /Height the plane is produced at, so
// the fuzzer explores the crop and white-fill paths as well as the decode. The decoder must neither panic nor hang,
// must never return a plane inconsistent with the column count it reports, and must never produce one past the budget
// the payload's own size allows. Seeds are the corpus files' JBIG2 payloads paired with their globals streams.
func FuzzJBIG2(f *testing.F) {
	for _, seed := range codecSeeds(f, codecJBIG2Names) {
		f.Add(seed.payload, seed.globals, seed.height)
	}
	f.Fuzz(func(t *testing.T, payload, globals []byte, h int) {
		plane, cols, err := decodeJBIG2Plane(payload, globals, h)
		if err != nil {
			return
		}
		budget := maxPixelsFor(len(payload))
		if cols <= 0 || h <= 0 || int64(cols)*int64(h) > budget {
			t.Fatalf("decoded %d columns for %d rows against a budget of %d", cols, h, budget)
		}
		if want := ((cols + 7) / 8) * h; len(plane) != want {
			t.Fatalf("plane is %d bytes, want %d for %dx%d", len(plane), want, cols, h)
		}
	})
}

// FuzzJPX drives raw JPXDecode payloads through jpxRasterFor, the entry point run() and alphaPlane reach, so the
// header-parse budget check and the panic recovery wrap the third-party decoder exactly as they do in production. Each
// payload runs under both /Indexed verdicts, since that flag selects between the container's own rendering and the raw
// component planes. The decoder must neither panic nor hang, and every raster it returns must be self-consistent and
// inside the budget the payload's own size allows. Seeds are the corpus files' JPX payloads, both JP2-wrapped and bare
// codestreams.
func FuzzJPX(f *testing.F) {
	for _, seed := range codecSeeds(f, codecJPXNames) {
		f.Add(seed.payload)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		for _, indexed := range []bool{false, true} {
			raster, err := jpxRasterFor(payload, indexed)
			if err != nil {
				continue
			}
			budget := maxPixelsFor(len(payload))
			if raster.w <= 0 || raster.h <= 0 || int64(raster.w)*int64(raster.h) > budget {
				t.Fatalf("indexed=%v: raster is %dx%d against a budget of %d", indexed, raster.w, raster.h, budget)
			}
			if raster.ncomp != 1 && raster.ncomp != 3 && raster.ncomp != 4 {
				t.Fatalf("indexed=%v: raster reports %d color components", indexed, raster.ncomp)
			}
			if want := raster.w * raster.h * raster.ncomp; len(raster.samples) != want {
				t.Fatalf("indexed=%v: raster holds %d samples, want %d for %dx%d at %d components",
					indexed, len(raster.samples), want, raster.w, raster.h, raster.ncomp)
			}
			if raster.alpha != nil && len(raster.alpha) != raster.w*raster.h {
				t.Fatalf("indexed=%v: raster holds %d alpha bytes, want %d", indexed, len(raster.alpha),
					raster.w*raster.h)
			}
		}
	})
}

// codecSeed is one corpus payload for a codec, with the /JBIG2Globals stream it names (nil for every other codec and
// for JBIG2 streams that carry their segments inline) and the dictionary /Height it was stored with.
type codecSeed struct {
	payload []byte
	globals []byte
	height  int
}

// codecSeeds returns every payload the image corpus carries for codec, taken through the same filter split production
// uses so the seeds are exactly the bytes the glue is handed.
func codecSeeds(f *testing.F, codec cos.Name) []codecSeed {
	f.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "testfiles", "corpus", "images-*.pdf"))
	if err != nil {
		f.Fatal(err)
	}
	var seeds []codecSeed
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		d, openErr := cos.Open(data)
		if openErr != nil {
			continue
		}
		for _, num := range d.ObjectNums() {
			stream, isStream := cos.AsStream(d.LoadObject(num))
			if !isStream {
				continue
			}
			payload, streamCodec, parms, splitErr := d.ImageFilterSplit(stream.Dict, stream.Raw)
			if splitErr != nil || streamCodec != codec {
				continue
			}
			seed := codecSeed{payload: payload, height: 1}
			if height, hasHeight := d.GetInt(stream.Dict, "Height"); hasHeight && height > 0 && height < maxImagePixels {
				seed.height = int(height)
			}
			if globals, hasGlobals := d.GetStream(parms, "JBIG2Globals"); hasGlobals {
				if decoded, globalsErr := d.StreamData(globals); globalsErr == nil {
					seed.globals = decoded
				}
			}
			seeds = append(seeds, seed)
		}
	}
	if len(seeds) == 0 {
		f.Fatalf("no %s payloads in the image corpus", codec)
	}
	return seeds
}
