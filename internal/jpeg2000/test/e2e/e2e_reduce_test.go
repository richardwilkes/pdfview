// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// Reduced-resolution decode (JPEG 2000's signature scalability feature). Decoding
// with ReduceResolutions = N discards the N highest resolution levels, producing an
// image divided by 2^N in each dimension — without reconstructing the finest detail.
// The reference images are OpenJPEG's own reduced decode (opj_decompress -r N) of
// the same codestreams; lossless, so they must match pixel-exactly.

func decodeReducedGray(t *testing.T, j2kPath string, reduce int, refPGM string) {
	t.Helper()
	w, h, want := readPGM(t, refPGM)
	data, err := os.ReadFile(j2kPath)
	if err != nil {
		t.Fatalf("read %s: %v", j2kPath, err)
	}
	img, err := j2k.DecodeWithOptions(bytes.NewReader(data), j2k.Options{ReduceResolutions: reduce})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("reduce=%d size: got %dx%d, want %dx%d", reduce, b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if uint8(r>>8) != want[y*w+x] {
				t.Fatalf("reduce=%d pixel(%d,%d): got %d, want %d", reduce, x, y, uint8(r>>8), want[y*w+x])
			}
		}
	}
}

func decodeReducedRGB(t *testing.T, j2kPath string, reduce int, refPPM string) {
	t.Helper()
	w, h, want := readPPM(t, refPPM)
	data, err := os.ReadFile(j2kPath)
	if err != nil {
		t.Fatalf("read %s: %v", j2kPath, err)
	}
	img, err := j2k.DecodeWithOptions(bytes.NewReader(data), j2k.Options{ReduceResolutions: reduce})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("reduce=%d size: got %dx%d, want %dx%d", reduce, b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			i := (y*w + x) * 3
			if uint8(r>>8) != want[i] || uint8(g>>8) != want[i+1] || uint8(b>>8) != want[i+2] {
				t.Fatalf("reduce=%d pixel(%d,%d): got %d,%d,%d want %d,%d,%d", reduce, x, y,
					uint8(r>>8), uint8(g>>8), uint8(b>>8), want[i], want[i+1], want[i+2])
			}
		}
	}
}

// 64×64 grayscale (Nd=2): reduce 1 → 32×32, reduce 2 → 16×16.
func TestDecode_Reduce_Gray_1(t *testing.T) {
	decodeReducedGray(t, "../testdata/gray64x64_n2.j2k", 1, "../testdata/gray64x64_r1.pgm")
}
func TestDecode_Reduce_Gray_2(t *testing.T) {
	decodeReducedGray(t, "../testdata/gray64x64_n2.j2k", 2, "../testdata/gray64x64_r2.pgm")
}

// 64×64 RGB+RCT (Nd=3): reduce 1 → 32×32, reduce 2 → 16×16.
func TestDecode_Reduce_RGB_1(t *testing.T) {
	decodeReducedRGB(t, "../testdata/rgb64_n4.j2k", 1, "../testdata/rgb64_r1.ppm")
}
func TestDecode_Reduce_RGB_2(t *testing.T) {
	decodeReducedRGB(t, "../testdata/rgb64_n4.j2k", 2, "../testdata/rgb64_r2.ppm")
}
