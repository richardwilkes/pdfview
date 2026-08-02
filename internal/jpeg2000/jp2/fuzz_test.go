// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package jp2_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/jp2"
)

// fuzzMaxPixels caps the declared image size the fuzzer fully decodes. The decoder
// imposes no arbitrary cap (it allocates proportional to the dimensions, like the std
// image package); the fuzzer uses DecodeConfig to skip a mutated header declaring a
// huge image so a proportional allocation does not OOM-kill the fuzz process.
const fuzzMaxPixels = 1 << 24 // ~16.7M px

// FuzzDecode feeds arbitrary bytes to the JP2 container decoder (box parsing plus
// the wrapped codestream). It must never panic — only return an error or an image.
func FuzzDecode(f *testing.F) {
	seeds, _ := filepath.Glob("../test/testdata/*.jp2")
	for _, p := range seeds {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(data)
		}
	}
	// The JP2 signature box, plus a truncated variant.
	f.Add([]byte{0x00, 0x00, 0x00, 0x0C, 0x6A, 0x50, 0x20, 0x20, 0x0D, 0x0A, 0x87, 0x0A})
	f.Add([]byte{0x00, 0x00, 0x00, 0x0C, 0x6A, 0x50, 0x20, 0x20})

	f.Fuzz(func(t *testing.T, data []byte) {
		if cfg, cerr := jp2.DecodeConfig(bytes.NewReader(data)); cerr == nil &&
			int64(cfg.Width)*int64(cfg.Height) > fuzzMaxPixels {
			return
		}
		img, err := jp2.Decode(bytes.NewReader(data))
		if err == nil && img == nil {
			t.Fatalf("nil error but nil image")
		}
	})
}
