// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import "testing"

// Multiple tile-parts per tile. A tile's codestream data can be split across
// several tile-parts (each with its own SOT/SOD), reassembled in order before
// Tier-2. These files were produced with OpenJPEG's -TP option and have TNsot > 1
// (9 and 5 tile-parts respectively); the previous decoder rejected anything with
// TNsot != 1.

// TestDecode_TileParts_ByComponent: 64×64 RGB split into tile-parts by component
// (TNsot = 9), decoded pixel-exact.
func TestDecode_TileParts_ByComponent(t *testing.T) {
	decodeAndCheckPPM(t, "../testdata/rgb64_tp_c.j2k", "../testdata/rgb64.ppm")
}

// TestDecode_TileParts_ByResolution: 256×256 grayscale split into tile-parts by
// resolution (TNsot = 5), decoded pixel-exact.
func TestDecode_TileParts_ByResolution(t *testing.T) {
	decodeAndCheckPGM(t, "../testdata/gray256_tp_r.j2k", "../testdata/gray256.pgm")
}
