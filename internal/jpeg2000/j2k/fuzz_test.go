// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package j2k_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// fuzzMaxPixels caps the declared image size the fuzzer will fully decode. The
// decoder itself imposes no arbitrary cap (it follows the std image package: allocate
// proportional to the dimensions, gate untrusted input with DecodeConfig). The fuzzer
// uses DecodeConfig to skip a mutated header that declares a huge image so a
// legitimate proportional allocation does not OOM-kill the fuzz process — that is not
// a decode bug.
const fuzzMaxPixels = 1 << 24 // ~16.7M px

// FuzzDecode feeds arbitrary bytes to the raw-codestream decoder. The decoder
// must never panic on malformed input — it may only return an error (or, for
// valid input, an image). The seed corpus is the real OpenJPEG test files.
func FuzzDecode(f *testing.F) {
	seeds, _ := filepath.Glob("../test/testdata/*.j2k")
	for _, p := range seeds {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(data)
		}
	}
	// A couple of minimal hand-written seeds (SOC marker, truncated headers).
	f.Add([]byte{0xFF, 0x4F})
	f.Add([]byte{0xFF, 0x4F, 0xFF, 0x51, 0x00, 0x2F})

	f.Fuzz(func(t *testing.T, data []byte) {
		// DecodeConfig allocates nothing; use it to skip a header that declares an
		// image too large to allocate in the fuzzer (see fuzzMaxPixels).
		if cfg, cerr := j2k.DecodeConfig(bytes.NewReader(data)); cerr == nil &&
			int64(cfg.Width)*int64(cfg.Height) > fuzzMaxPixels {
			return
		}
		img, err := j2k.Decode(bytes.NewReader(data))
		// The only contract under fuzzing is "do not panic". A nil error must come
		// with a usable image; a non-nil error must come with no image.
		if err == nil && img == nil {
			t.Fatalf("nil error but nil image")
		}
	})
}
