// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import "testing"

// Multiple quality layers. A code-block can be coded across several quality
// layers, each contributing more coding passes and bytes to the same block; the
// decoder must accumulate them into one continuous bytestream. The previous
// decoder discarded the COD layer count (treated every file as single-layer), so
// these multi-layer files decoded to a low-quality / garbage image.
//
// Both files were produced with OpenJPEG using multiple rate points; the final
// layer is lossless (-r ...,1), so a full decode must equal the original raster.

// TestDecode_Gray_3Layers: 64×64 grayscale, 3 quality layers (-r 20,10,1).
func TestDecode_Gray_3Layers(t *testing.T) {
	decodeAndCheckPGM(t, "../testdata/gray64_3layer.j2k", "../testdata/gray64x64.pgm")
}

// TestDecode_RGB_3Layers: 64×64 RGB (RCT), 3 quality layers (-r 30,10,1).
func TestDecode_RGB_3Layers(t *testing.T) {
	decodeAndCheckPPM(t, "../testdata/rgb64_3layer.j2k", "../testdata/rgb64.ppm")
}
