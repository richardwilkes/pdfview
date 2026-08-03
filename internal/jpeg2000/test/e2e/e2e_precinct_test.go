// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import "testing"

// Precinct partitioning and multi-code-block-per-subband support.
//
// These images are large enough (256x256) that detail subbands span several
// code-blocks, and several use explicit precinct grids. They were generated with
// OpenJPEG 2.4.0 lossless, so the decoded pixels must match the source PGM/PPM
// exactly. The reference is the original raster (lossless round-trip).

// TestDecode_Gray256_N5 exercises multiple code-blocks per subband with the
// default single (maximal) precinct: at 256x256 with 5 DWT levels the level-1
// detail subbands are 128x128, i.e. 2x2 code-blocks of 64.
func TestDecode_Gray256_N5(t *testing.T) {
	decodeAndCheckPGM(t, "../testdata/gray256_n5.j2k", "../testdata/gray256.pgm")
}

// TestDecode_Gray256_Precincts uses precinct sizes [128,128] at resolution 0 and
// [64,64] at the finer resolutions, with 32x32 code-blocks — so most resolutions
// hold a multi-precinct grid and each precinct holds multiple code-blocks (LRCP).
func TestDecode_Gray256_Precincts(t *testing.T) {
	decodeAndCheckPGM(t, "../testdata/gray256_prec.j2k", "../testdata/gray256.pgm")
}

// TestDecode_RGB64_Precincts exercises precincts with the reversible colour
// transform: 64x64 RGB, 4 DWT levels, [32,32] precincts, 16x16 code-blocks.
func TestDecode_RGB64_Precincts(t *testing.T) {
	decodeAndCheckPPM(t, "../testdata/rgb64_prec.j2k", "../testdata/rgb64.ppm")
}

// The four non-LRCP progression orders combined with a multi-precinct grid
// ([64,64], 32x32 code-blocks). RLCP/RPCL keep resolution outer to precinct and
// use the per-resolution precinct index; PCRL/CPRL are precinct-outer and need
// position-based precinct enumeration across resolutions.
func TestDecode_Precincts_RPCL(t *testing.T) {
	decodeAndCheckPGM(t, "../testdata/gray256_prec_rpcl.j2k", "../testdata/gray256.pgm")
}

func TestDecode_Precincts_PCRL(t *testing.T) {
	decodeAndCheckPGM(t, "../testdata/gray256_prec_pcrl.j2k", "../testdata/gray256.pgm")
}

func TestDecode_Precincts_CPRL(t *testing.T) {
	decodeAndCheckPGM(t, "../testdata/gray256_prec_cprl.j2k", "../testdata/gray256.pgm")
}

// TestDecode_Tile_Precincts combines origin-aligned 128x128 tiling with [32,32]
// precincts, verifying the per-tile precinct partition.
func TestDecode_Tile_Precincts(t *testing.T) {
	decodeAndCheckPGM(t, "../testdata/gray256_tile_prec.j2k", "../testdata/gray256.pgm")
}
