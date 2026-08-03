// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// Progression orders. The same 64x64 RGB image (lossless, 3 DWT levels) encoded
// by OpenJPEG with each progression order; the decoder iterates packets in the
// order signalled by COD and must reproduce the image pixel-exactly regardless.
// Files: `opj_compress -i rgb64.ppm -n 3 -r 1 -p <ORDER>`.
func TestDecode_ProgressionOrders(t *testing.T) {
	w, h, exp := readPPM(t, "../testdata/rgb64.ppm")
	// LRCP is exercised by the existing RGB tests; cover the other four here.
	for _, order := range []string{"RLCP", "RPCL", "PCRL", "CPRL"} {
		t.Run(order, func(t *testing.T) {
			data, err := os.ReadFile("../testdata/prog_" + order + ".j2k")
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			img, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
				t.Fatalf("size mismatch: got %dx%d want %dx%d", b.Dx(), b.Dy(), w, h)
			}
			bad := 0
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					r, g, b, _ := img.At(x, y).RGBA()
					i := (y*w + x) * 3
					if byte(r>>8) != exp[i] || byte(g>>8) != exp[i+1] || byte(b>>8) != exp[i+2] {
						bad++
					}
				}
			}
			if bad != 0 {
				t.Errorf("%s: %d/%d pixels differ", order, bad, w*h)
			}
		})
	}
}
