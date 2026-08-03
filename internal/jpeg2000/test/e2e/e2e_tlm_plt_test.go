// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// TLM (tile-part length, main header) and PLT (packet length, tile-part header)
// are informational length-index markers: a reader may use them to seek, but they
// carry nothing needed to decode. The decoder must tolerate and skip them without
// desyncing. OpenJPEG's opj_compress never emits them; this vector is produced by
// Grok (grk_compress -X -L), an independent encoder, which makes it a useful
// cross-check from a second codebase. The 64×64 RGB image is tiled 2×2, so the
// stream carries TLM in the main header and a PLT per tile-part. Lossless, so the
// decode must equal the source exactly.
func TestDecode_TLM_PLT(t *testing.T) {
	w, h, want := readPPM(t, "../testdata/tlm_plt.ppm")
	data, err := os.ReadFile("../testdata/tlm_plt.j2k")
	if err != nil {
		t.Fatalf("read j2k: %v", err)
	}
	imgI, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
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
