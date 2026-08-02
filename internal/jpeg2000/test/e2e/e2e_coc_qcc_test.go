// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// COC (0xFF53) and QCC (0xFF5D) carry per-component overrides of the default
// coding (COD) and quantization (QCD). The opj_compress CLI never emits them, so
// these vectors are produced by a patched OpenJPEG library encoder that forces a
// single component to differ (see jpeg2000-test-vectors). Before this support the
// decoder silently skipped the markers and mis-decoded the overridden component.

// readPlanar reads a planar (component-major) raw file of w*h*comps bytes.
func readPlanar(t *testing.T, path string, w, h, comps int) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(b) != w*h*comps {
		t.Fatalf("%s: got %d bytes, want %d", path, len(b), w*h*comps)
	}
	return b
}

// TestDecode_COC_RGB_Lossless decodes a lossless RGB codestream whose green
// component carries a COC marker setting a 16×16 code-block size (the default,
// from COD, is 64×64). Lossless ⇒ the decode must equal the source exactly; if
// the decoder ignored the COC the green plane's code-block grid would be wrong
// and Tier-2 would desync.
func TestDecode_COC_RGB_Lossless(t *testing.T) {
	const w, h, comps = 32, 32, 3
	want := readPlanar(t, "../testdata/coc_rgb.raw", w, h, comps)
	data, err := os.ReadFile("../testdata/coc_rgb.j2k")
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
			gr, gg, gb := uint8(r>>8), uint8(g>>8), uint8(b>>8)
			if gr != plane(0, x, y) || gg != plane(1, x, y) || gb != plane(2, x, y) {
				t.Fatalf("pixel(%d,%d): got (%d,%d,%d), want (%d,%d,%d)",
					x, y, gr, gg, gb, plane(0, x, y), plane(1, x, y), plane(2, x, y))
			}
		}
	}
}

// TestDecode_QCC_RGB_Lossy decodes an irreversible (9/7) RGB codestream whose
// blue component carries a QCC marker that coarsens its quantization (step-size
// exponents +2 relative to QCD). The reference is OpenJPEG's own decode of the
// same file: if the decoder ignored the QCC it would dequantize the blue
// component with the wrong step size and drift from opj. Lossy, so allow a small
// rounding tolerance.
func TestDecode_QCC_RGB_Lossy(t *testing.T) {
	const w, h, comps = 32, 32, 3
	ref := readPlanar(t, "../testdata/qcc_rgb_opjref.raw", w, h, comps)
	data, err := os.ReadFile("../testdata/qcc_rgb.j2k")
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
	plane := func(c, x, y int) int { return int(ref[c*w*h+y*w+x]) }
	maxDiff := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := imgI.At(x, y).RGBA()
			got := [3]int{int(uint8(r >> 8)), int(uint8(g >> 8)), int(uint8(b >> 8))}
			for c := 0; c < comps; c++ {
				if d := got[c] - plane(c, x, y); d != 0 {
					if d < 0 {
						d = -d
					}
					if d > maxDiff {
						maxDiff = d
					}
				}
			}
		}
	}
	// Our 9/7 path matches opj's reconstruction to within ±1 LSB (rounding); a
	// decoder that ignored QCC would be off by many levels on the blue channel.
	if maxDiff > 1 {
		t.Fatalf("QCC lossy: maxDiff=%d vs opj reference (want ≤1)", maxDiff)
	}
}
