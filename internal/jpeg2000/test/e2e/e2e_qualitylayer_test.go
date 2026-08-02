// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// Quality-layer decode (progressive quality / rate scalability). Decoding with
// MaxLayer = L contributes only the first L quality layers, giving a lower-quality
// image. Matching opj_decompress -l L exactly relies on the per-coefficient
// mid-point reconstruction (a partially-decoded last bit-plane reconstructs each
// coefficient at its own plane). The reference images are opj's own -l L output.

func decodeLayerGray(t *testing.T, j2kPath string, layer int, refPGM string) {
	t.Helper()
	w, h, want := readPGM(t, refPGM)
	data, err := os.ReadFile(j2kPath)
	if err != nil {
		t.Fatalf("read %s: %v", j2kPath, err)
	}
	img, err := j2k.DecodeWithOptions(bytes.NewReader(data), j2k.Options{MaxLayer: layer})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("layer=%d size: got %dx%d, want %dx%d", layer, b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if uint8(r>>8) != want[y*w+x] {
				t.Fatalf("layer=%d pixel(%d,%d): got %d, want %d", layer, x, y, uint8(r>>8), want[y*w+x])
			}
		}
	}
}

// Lossless (5/3) 3-layer file: decoding 1 or 2 of the 3 layers must match
// opj_decompress -l L exactly (the truncated reversible coefficients get the
// integer mid-point reconstruction).
func TestDecode_Layer_Lossless_1(t *testing.T) {
	decodeLayerGray(t, "../testdata/gray64_3layer.j2k", 1, "../testdata/gray64_3layer_l1.pgm")
}
func TestDecode_Layer_Lossless_2(t *testing.T) {
	decodeLayerGray(t, "../testdata/gray64_3layer.j2k", 2, "../testdata/gray64_3layer_l2.pgm")
}

// Lossy (9/7) 3-layer file: decoding 1 or 2 of the 3 layers must match
// opj_decompress -l L exactly (the per-coefficient 0.5·2^plane dequant mid-point).
func TestDecode_Layer_Lossy_1(t *testing.T) {
	decodeLayerGray(t, "../testdata/gray64_lossy3layer.j2k", 1, "../testdata/gray64_lossy3layer_l1.pgm")
}
func TestDecode_Layer_Lossy_2(t *testing.T) {
	decodeLayerGray(t, "../testdata/gray64_lossy3layer.j2k", 2, "../testdata/gray64_lossy3layer_l2.pgm")
}
