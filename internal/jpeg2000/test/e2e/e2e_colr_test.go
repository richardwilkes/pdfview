// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/jp2"
)

// TestDecode_JP2_RGBA_cdef decodes a 4-component JP2 whose cdef box marks the 4th
// channel as alpha. It is lossless, so the decoded NRGBA must equal the source
// planes (rgba.raw, planar R,G,B,A). The standard channel order (R,G,B,A) maps by
// position; the cdef box is parsed and tolerated.
func TestDecode_JP2_RGBA_cdef(t *testing.T) {
	const w, h = 32, 32
	raw, err := os.ReadFile("../testdata/rgba.raw")
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	data, err := os.ReadFile("../testdata/rgba.jp2")
	if err != nil {
		t.Fatalf("read jp2: %v", err)
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
			p := img.NRGBAAt(x, y)
			want := [4]byte{plane(0, x, y), plane(1, x, y), plane(2, x, y), plane(3, x, y)}
			if [4]byte{p.R, p.G, p.B, p.A} != want {
				t.Fatalf("pixel(%d,%d): got %v, want %v", x, y, [4]byte{p.R, p.G, p.B, p.A}, want)
			}
		}
	}
}

// JP2 colr box handling. A 3-component JP2 whose colr box declares the sYCC
// colour space (enumerated 18) carries Y, Cb, Cr components that must be converted
// to RGB on decode (the only colour-space conversion the decoder performs — for a
// raw codestream it never guesses; see the policy in reconstruction.go). The
// reference is OpenJPEG's own sYCC decode (sycc.jp2 was made by the library API,
// which the opj_compress CLI cannot).
func TestDecode_JP2_sYCC(t *testing.T) {
	w, h, want := readPPM(t, "../testdata/sycc_opjref.ppm")
	data, err := os.ReadFile("../testdata/sycc.jp2")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	imgI, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, ok := imgI.(*image.NRGBA); !ok {
		t.Fatalf("expected *image.NRGBA (RGB after sYCC), got %T", imgI)
	}
	if b := imgI.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("size: got %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := imgI.At(x, y).RGBA()
			i := (y*w + x) * 3
			if uint8(r>>8) != want[i] || uint8(g>>8) != want[i+1] || uint8(b>>8) != want[i+2] {
				t.Fatalf("pixel(%d,%d): got %d,%d,%d want %d,%d,%d", x, y,
					uint8(r>>8), uint8(g>>8), uint8(b>>8), want[i], want[i+1], want[i+2])
			}
		}
	}
}
