// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/jp2"
)

// TestDecode_JP2_pclr decodes an indexed-colour JP2: a single 8-bit index
// component plus a `pclr` palette (16 sRGB entries) and a `cmap` box mapping the
// component through the palette to R, G, B. OpenJPEG's opj_compress CLI and its
// public library API cannot emit a palette, so the container is hand-built (see
// jpeg2000-test-vectors gen/pclr); OpenJPEG *decodes* it, so the ground truth is
// the palette expansion (verified equal to opj's own decode at generation time).
// The codestream is lossless, so the index plane is exact and the RGB output must
// equal the palette lookup byte-for-byte.
func TestDecode_JP2_pclr(t *testing.T) {
	const w, h = 32, 32
	want, err := os.ReadFile("../testdata/pclr_rgb.raw") // interleaved R,G,B
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	data, err := os.ReadFile("../testdata/pclr.jp2")
	if err != nil {
		t.Fatalf("read jp2: %v", err)
	}
	imgI, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, ok := imgI.(*image.NRGBA); !ok {
		t.Fatalf("expected *image.NRGBA (palette → RGB), got %T", imgI)
	}
	if b := imgI.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("size: got %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := imgI.At(x, y).RGBA()
			i := (y*w + x) * 3
			if uint8(r>>8) != want[i] || uint8(g>>8) != want[i+1] || uint8(b>>8) != want[i+2] {
				t.Fatalf("pixel(%d,%d): got (%d,%d,%d), want (%d,%d,%d)", x, y,
					uint8(r>>8), uint8(g>>8), uint8(b>>8), want[i], want[i+1], want[i+2])
			}
		}
	}
}
