// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import "testing"

// POC (progression order change, 0xFF5F). The decoder honours per-tile POC by
// running one progression per tuple (layer start 0 .. LayE, over the tuple's
// resolution/component range, in the tuple's order), emitting each packet from the
// first tuple whose volume contains it — matching OpenJPEG's packet iterator.
//
// These files were produced with OpenJPEG's -POC option (and are lossless, so the
// decode must equal the source). They split the progression across resolution or
// component ranges, the realistic POC patterns. (Layer-progression POC, where the
// tuples overlap on layers, is not exercised: OpenJPEG's encoder produces a file it
// cannot itself round-trip for those specs.)

// TestDecode_POC_ResolutionSplit: resolutions 0 in LRCP, resolutions 1-2 in RPCL.
func TestDecode_POC_ResolutionSplit(t *testing.T) {
	decodeAndCheckPPM(t, "../testdata/rgb64_poc_rsplit.j2k", "../testdata/rgb64.ppm")
}

// TestDecode_POC_ComponentSplit: component 0 in LRCP, components 1-2 in RPCL.
func TestDecode_POC_ComponentSplit(t *testing.T) {
	decodeAndCheckPPM(t, "../testdata/rgb64_poc_csplit.j2k", "../testdata/rgb64.ppm")
}

// TestDecode_POC_RLCP_CPRL: resolutions 0-1 in RLCP, resolutions 2-3 in CPRL.
func TestDecode_POC_RLCP_CPRL(t *testing.T) {
	decodeAndCheckPPM(t, "../testdata/rgb64_poc_rlcp_cprl.j2k", "../testdata/rgb64.ppm")
}
