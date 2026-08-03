// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// TestDecode_RGN decodes a lossless grayscale codestream with a Region-of-Interest
// (RGN max-shift, opj_compress -ROI c=0,U=7). The encoder scales the ROI
// coefficients up by 7 bit-planes so they are coded above the background; the
// decoder must decode those extra planes and shift the ROI coefficients back down.
// Ignoring the RGN truncates the ROI to the background bit-depth and corrupts it
// (this is the bug behind ISO conformance p0_03). Lossless, so the decode must
// equal the source exactly.
func TestDecode_RGN(t *testing.T) {
	const w, h = 64, 64
	want, err := os.ReadFile("../testdata/rgn.raw")
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	data, err := os.ReadFile("../testdata/rgn.j2k")
	if err != nil {
		t.Fatalf("read j2k: %v", err)
	}
	imgI, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	g, ok := imgI.(*image.Gray)
	if !ok {
		t.Fatalf("expected *image.Gray, got %T", imgI)
	}
	if b := imgI.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("size: got %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if g.GrayAt(x, y).Y != want[y*w+x] {
				t.Fatalf("pixel(%d,%d): got %d, want %d", x, y, g.GrayAt(x, y).Y, want[y*w+x])
			}
		}
	}
}
