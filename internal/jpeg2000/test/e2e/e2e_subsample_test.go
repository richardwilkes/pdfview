// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// Chroma-subsampled components (4:2:0). A component with XRsiz/YRsiz > 1 is coded
// at reduced resolution and must be upsampled to the reference grid on output. The
// decoder upsamples by sample replication (nearest-neighbour). The previous
// decoder wrote the small plane into the corner with no upsampling.
//
// sub420.raw is the ground-truth planar source (a single 64×64 component plus two
// 2×2-subsampled 32×32 components). The codestream is OpenJPEG lossless output
// with no colour transform (mct=0), so each decoded channel must equal its source
// component replicated across its subsampling cell.
//
// NOTE: this is validated against the ground-truth source, NOT opj_decompress's
// PPM output, on purpose. For a raw codestream opj's CLI heuristically assumes
// 3-component subsampled data is sYCC and applies a YCbCr→RGB conversion; we
// deliberately do not (a raw codestream declares no colour space). See the
// colour-space policy in internal/codestream/reconstruction.go.
func TestDecode_Subsample420(t *testing.T) {
	const W, H = 64, 64
	raw, err := os.ReadFile("../testdata/sub420.raw")
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if len(raw) != W*H+2*(W/2)*(H/2) {
		t.Fatalf("raw size %d unexpected", len(raw))
	}
	y := raw[0 : W*H]                // full-resolution component 0
	cb := raw[W*H : W*H+(W/2)*(H/2)] // 2×2-subsampled component 1
	cr := raw[W*H+(W/2)*(H/2):]      // 2×2-subsampled component 2

	data, err := os.ReadFile("../testdata/sub420_n2.j2k")
	if err != nil {
		t.Fatalf("read j2k: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != W || b.Dy() != H {
		t.Fatalf("size mismatch: got %dx%d, want %dx%d", b.Dx(), b.Dy(), W, H)
	}
	for py := 0; py < H; py++ {
		for px := 0; px < W; px++ {
			r, g, b, _ := img.At(px, py).RGBA()
			wantR := y[py*W+px]
			wantG := cb[(py/2)*(W/2)+px/2] // nearest-neighbour upsampling
			wantB := cr[(py/2)*(W/2)+px/2]
			if uint8(r>>8) != wantR || uint8(g>>8) != wantG || uint8(b>>8) != wantB {
				t.Fatalf("pixel(%d,%d): got %d,%d,%d want %d,%d,%d", px, py,
					uint8(r>>8), uint8(g>>8), uint8(b>>8), wantR, wantG, wantB)
			}
		}
	}
}
