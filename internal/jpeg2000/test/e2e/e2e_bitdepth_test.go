// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"os"
	"testing"
)

// Bit depths above 8. OpenJPEG lossless stores the raw P-bit samples; the
// decoder must reproduce them exactly. Samples are read from the binary PGM/PPM
// (which store >8-bit data as big-endian 16-bit), and compared against the
// decoder's image output via At().RGBA(), which yields the raw stored value
// (Gray16 for grayscale, NRGBA64 for deep RGB).

// readDeepRaster reads a binary PGM (P5) or PPM (P6) with maxval > 255, returning
// the magic, dimensions, and samples (1 per pixel for P5, 3 interleaved for P6).
func readDeepRaster(t *testing.T, path string) (magic string, w, h int, samples []uint16) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var maxval int
	if _, err := fmt.Fscanf(r, "%s", &magic); err != nil {
		t.Fatalf("read magic: %v", err)
	}
	if _, err := fmt.Fscan(r, &w, &h, &maxval); err != nil {
		t.Fatalf("read header: %v", err)
	}
	if _, err := r.ReadByte(); err != nil { // single whitespace after maxval
		t.Fatalf("read whitespace: %v", err)
	}
	n := w * h
	if magic == "P6" {
		n *= 3
	}
	samples = make([]uint16, n)
	for i := range samples {
		hi, err := r.ReadByte()
		if err != nil {
			t.Fatalf("read sample %d: %v", i, err)
		}
		lo, err := r.ReadByte()
		if err != nil {
			t.Fatalf("read sample %d: %v", i, err)
		}
		samples[i] = uint16(hi)<<8 | uint16(lo)
	}
	return magic, w, h, samples
}

func decodeImage(t *testing.T, path string) image.Image {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode %s: %v", path, err)
	}
	return img
}

func decodeAndCheckDeep(t *testing.T, j2kPath, rasterPath string) {
	t.Helper()
	magic, w, h, want := readDeepRaster(t, rasterPath)
	img := decodeImage(t, j2kPath)
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("size mismatch: got %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if magic == "P6" {
				i := (y*w + x) * 3
				if uint16(r) != want[i] || uint16(g) != want[i+1] || uint16(b) != want[i+2] {
					t.Fatalf("pixel(%d,%d): got %d,%d,%d want %d,%d,%d", x, y, uint16(r), uint16(g), uint16(b), want[i], want[i+1], want[i+2])
				}
			} else {
				if uint16(r) != want[y*w+x] {
					t.Fatalf("pixel(%d,%d): got %d want %d", x, y, uint16(r), want[y*w+x])
				}
			}
		}
	}
}

// TestDecode_Gray16bit decodes 16-bit grayscale (precision 16, Gray16 output).
func TestDecode_Gray16bit(t *testing.T) {
	decodeAndCheckDeep(t, "../testdata/gray16bit_n3.j2k", "../testdata/gray16bit.pgm")
}

// TestDecode_Gray12bit decodes a non-byte-aligned precision (12-bit grayscale).
func TestDecode_Gray12bit(t *testing.T) {
	decodeAndCheckDeep(t, "../testdata/gray12bit_n3.j2k", "../testdata/gray12bit.pgm")
}

// TestDecode_RGB16bit decodes 16-bit RGB via the reversible colour transform
// (NRGBA64 output).
func TestDecode_RGB16bit(t *testing.T) {
	decodeAndCheckDeep(t, "../testdata/rgb16bit_n2.j2k", "../testdata/rgb16bit.ppm")
}
