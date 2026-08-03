// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// TestDecode_Termall decodes a lossless RGB codestream coded with the "termination
// on each coding pass" code-block style (cblksty bit 0x04, opj_compress -M 4). Each
// coding pass is a separately terminated MQ segment, so Tier-2 reads one length code
// per segment and Tier-1 restarts the arithmetic decoder at each segment boundary
// (contexts persist). opj_compress can emit this via -M, so the vector is a normal
// lossless file and the decode must equal the source exactly. ISO conformance
// p0_04/p0_12 exercise the same style.
func TestDecode_Termall(t *testing.T) {
	const w, h, comps = 64, 64, 3
	want := readPlanar(t, "../testdata/termall.raw", w, h, comps)
	data, err := os.ReadFile("../testdata/termall.j2k")
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
	plane := func(c, x, y int) byte { return want[c*w*h+y*w+x] }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := imgI.At(x, y).RGBA()
			if uint8(r>>8) != plane(0, x, y) || uint8(g>>8) != plane(1, x, y) || uint8(b>>8) != plane(2, x, y) {
				t.Fatalf("pixel(%d,%d): got (%d,%d,%d), want (%d,%d,%d)", x, y,
					uint8(r>>8), uint8(g>>8), uint8(b>>8), plane(0, x, y), plane(1, x, y), plane(2, x, y))
			}
		}
	}
}
