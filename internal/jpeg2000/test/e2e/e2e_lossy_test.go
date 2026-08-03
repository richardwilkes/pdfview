// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"math"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// lossyCompare decodes a lossy (9/7) file and compares it to OpenJPEG's own
// reconstruction of the same codestream, returning the max per-pixel difference
// and the PSNR.
func lossyCompare(t *testing.T, j2kPath, refPGM string) (maxDiff int, psnr float64) {
	t.Helper()
	w, h, ref := readPGM(t, refPGM)
	data, err := os.ReadFile(j2kPath)
	if err != nil {
		t.Fatalf("read %s: %v", j2kPath, err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode %s: %v", j2kPath, err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("size mismatch: got %dx%d want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	sumSq := 0.0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			d := int(byte(r>>8)) - int(ref[y*w+x])
			if d < 0 {
				d = -d
			}
			if d > maxDiff {
				maxDiff = d
			}
			sumSq += float64(d * d)
		}
	}
	mse := sumSq / float64(w*h)
	psnr = math.Inf(1)
	if mse > 0 {
		psnr = 10 * math.Log10(255*255/mse)
	}
	return maxDiff, psnr
}

// Lossy (irreversible 9/7) decode. Lossy reconstruction is deterministic given
// the codestream, so a correct decoder reproduces OpenJPEG's reconstruction up
// to floating-point rounding (and, when the stream is rate-truncated, up to the
// per-coefficient reconstruction bias). Files were produced by
// `opj_compress -i gray64x64.pgm -I -r <ratio>`.

func TestDecode_Lossy9x7_Gray64_R1(t *testing.T) {
	// Minimal truncation: must essentially match opj (only float rounding).
	maxDiff, psnr := lossyCompare(t, "../testdata/irr64_r1.j2k", "../testdata/irr64_r1_opjref.pgm")
	t.Logf("lossy 9/7 -r1 vs opj: maxDiff=%d PSNR=%.2f dB", maxDiff, psnr)
	if maxDiff > 1 {
		t.Errorf("maxDiff = %d, want <= 1", maxDiff)
	}
	if psnr < 70 {
		t.Errorf("PSNR = %.2f dB, want >= 70", psnr)
	}
}

func TestDecode_Lossy9x7_Gray64_R20(t *testing.T) {
	// Heavily rate-truncated. With per-coefficient mid-point reconstruction
	// (0.5·2^LowPlane per coefficient, matching OpenJPEG's t1), the truncated decode
	// matches opj to the last bit — the same maxDiff 1 / ~78 dB as the full decode,
	// not the ~44 dB the old per-code-block bias gave.
	maxDiff, psnr := lossyCompare(t, "../testdata/irr64_r20.j2k", "../testdata/irr64_r20_opjref.pgm")
	t.Logf("lossy 9/7 -r20 vs opj: maxDiff=%d PSNR=%.2f dB", maxDiff, psnr)
	if maxDiff > 1 {
		t.Errorf("maxDiff = %d, want <= 1", maxDiff)
	}
	if psnr < 70 {
		t.Errorf("PSNR = %.2f dB, want >= 70", psnr)
	}
}

// lossyCompareRGB is the 3-channel counterpart of lossyCompare, exercising the
// irreversible colour transform (ICT) on lossy RGB.
func lossyCompareRGB(t *testing.T, j2kPath, refPPM string) (maxDiff int, psnr float64) {
	t.Helper()
	w, h, ref := readPPM(t, refPPM)
	data, err := os.ReadFile(j2kPath)
	if err != nil {
		t.Fatalf("read %s: %v", j2kPath, err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode %s: %v", j2kPath, err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("size mismatch: got %dx%d want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	sumSq := 0.0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			got := [3]byte{byte(r >> 8), byte(g >> 8), byte(b >> 8)}
			i := (y*w + x) * 3
			for c := 0; c < 3; c++ {
				d := int(got[c]) - int(ref[i+c])
				if d < 0 {
					d = -d
				}
				if d > maxDiff {
					maxDiff = d
				}
				sumSq += float64(d * d)
			}
		}
	}
	mse := sumSq / float64(w*h*3)
	psnr = math.Inf(1)
	if mse > 0 {
		psnr = 10 * math.Log10(255*255/mse)
	}
	return maxDiff, psnr
}

func TestDecode_LossyRGB9x7_R1(t *testing.T) {
	// 64x64 RGB lossy (9/7 + ICT), minimal truncation. The colour transform runs
	// in float before rounding, so this essentially matches opj (only float
	// rounding differences), on par with the grayscale path.
	maxDiff, psnr := lossyCompareRGB(t, "../testdata/irgb64_r1.j2k", "../testdata/irgb64_r1_opjref.ppm")
	t.Logf("lossy RGB 9/7 -r1 vs opj: maxDiff=%d PSNR=%.2f dB", maxDiff, psnr)
	if psnr < 70 {
		t.Errorf("PSNR = %.2f dB, want >= 70", psnr)
	}
	if maxDiff > 1 {
		t.Errorf("maxDiff = %d, want <= 1", maxDiff)
	}
}

func TestDecode_LossyRGB9x7_R20(t *testing.T) {
	// 64x64 RGB lossy at high compression. Per-coefficient reconstruction matches
	// opj to the last bit here too (maxDiff 1), not the ~41 dB of the old per-block
	// bias.
	maxDiff, psnr := lossyCompareRGB(t, "../testdata/irgb64_r20.j2k", "../testdata/irgb64_r20_opjref.ppm")
	t.Logf("lossy RGB 9/7 -r20 vs opj: maxDiff=%d PSNR=%.2f dB", maxDiff, psnr)
	if maxDiff > 1 {
		t.Errorf("maxDiff = %d, want <= 1", maxDiff)
	}
	if psnr < 70 {
		t.Errorf("PSNR = %.2f dB, want >= 70", psnr)
	}
}
