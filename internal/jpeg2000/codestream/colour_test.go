// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import "testing"

// TestLabToSRGB checks the CIELab->sRGB conversion against well-known reference
// colours (D50 PCS, sRGB output). Tolerances are a couple of 8-bit LSBs (the values
// are scaled to 16 bit), covering rounding in the matrices and the gamma curve.
func TestLabToSRGB(t *testing.T) {
	const tol = 3 * 257 // ~3 LSB at 8-bit (1 LSB ≈ 257 at 16-bit)
	cases := []struct {
		name       string
		l, a, b    float64
		r, g, blue int // expected 8-bit sRGB
	}{
		{"white", 100, 0, 0, 255, 255, 255},
		{"black", 0, 0, 0, 0, 0, 0},
		{"mid grey", 53.585, 0, 0, 128, 128, 128},
		{"sRGB red", 54.291, 80.812, 69.885, 255, 0, 0},
		{"sRGB green", 87.818, -79.272, 80.992, 0, 255, 0},
		{"sRGB blue", 29.567, 68.298, -112.029, 0, 0, 255},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, g, b := labToSRGB16(tc.l, tc.a, tc.b)
			want := [3]int{tc.r * 257, tc.g * 257, tc.blue * 257}
			got := [3]uint16{r, g, b}
			for i := 0; i < 3; i++ {
				if d := int(got[i]) - want[i]; d < -tol || d > tol {
					t.Errorf("channel %d: got %d, want ~%d (8-bit got≈%d want=%d)",
						i, got[i], want[i], int(got[i])/257, want[i]/257)
				}
			}
		})
	}
}
