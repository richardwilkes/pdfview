// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// TestDecode_RGBA64 decodes a 4-component image as RGBA (the reversible colour
// transform is applied to the first three components; the fourth is the alpha
// channel). The codestream is OpenJPEG lossless output of a 64×64 planar RGBA raw
// (R plane, then G, B, A), so the decoded image must match the raw exactly.
func TestDecode_RGBA64(t *testing.T) {
	const w, h = 64, 64
	raw, err := os.ReadFile("../testdata/rgba64.raw")
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if len(raw) != 4*w*h {
		t.Fatalf("raw size %d, want %d", len(raw), 4*w*h)
	}
	data, err := os.ReadFile("../testdata/rgba64_n2.j2k")
	if err != nil {
		t.Fatalf("read j2k: %v", err)
	}
	imgI, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	img, ok := imgI.(*image.NRGBA)
	if !ok {
		t.Fatalf("expected *image.NRGBA, got %T", imgI)
	}
	plane := func(c, x, y int) byte { return raw[c*w*h+y*w+x] }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			got := img.NRGBAAt(x, y)
			want := [4]byte{plane(0, x, y), plane(1, x, y), plane(2, x, y), plane(3, x, y)}
			if [4]byte{got.R, got.G, got.B, got.A} != want {
				t.Fatalf("pixel(%d,%d): got %v, want %v", x, y, [4]byte{got.R, got.G, got.B, got.A}, want)
			}
		}
	}
}
