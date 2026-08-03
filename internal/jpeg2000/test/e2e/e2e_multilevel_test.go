// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"testing"
)

// Deeper multi-level DWT on real images. Grayscale deep DWT is already covered by
// TestDecode_Gray64x64_N5 (5 levels); these add the previously untested
// combination of RCT color transform with 3+ decomposition levels.

func TestDecode_RGB64_N4(t *testing.T) {
	// 64x64 RGB (RCT), 3 decomposition levels (opj -n 4).
	decodeAndCheckPPM(t, "../testdata/rgb64_n4.j2k", "../testdata/rgb64.ppm")
}

func TestDecode_RGB64_N5(t *testing.T) {
	// 64x64 RGB (RCT), 4 decomposition levels (opj -n 5).
	decodeAndCheckPPM(t, "../testdata/rgb64_n5.j2k", "../testdata/rgb64.ppm")
}
