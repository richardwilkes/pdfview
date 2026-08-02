// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"testing"

	_ "github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// benchDecode decodes the same in-memory codestream repeatedly so the benchmark
// measures the decode pipeline, not file I/O.
func benchDecode(b *testing.B, path string) {
	b.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read %s: %v", path, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			b.Fatalf("decode: %v", err)
		}
		_ = img
	}
}

func BenchmarkDecode_Gray256_N5(b *testing.B)  { benchDecode(b, "../testdata/gray256_n5.j2k") }
func BenchmarkDecode_Gray1024_N5(b *testing.B) { benchDecode(b, "../testdata/gray1024_n5.j2k") }
func BenchmarkDecode_RGB1024_N5(b *testing.B)  { benchDecode(b, "../testdata/rgb1024_n5.j2k") }
