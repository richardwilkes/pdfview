// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bufio"
	"fmt"
	"os"
	"testing"
)

// Signed samples (SIZ Ssiz sign bit). The encoder applies no DC level shift to
// signed components, so the decoded values are centred on 0. For an image.Image
// output the decoder applies the conventional viewing shift of +2^(P-1), mapping
// the signed range [-2^(P-1), 2^(P-1)-1] into the unsigned [0, 2^P-1] channel; the
// true signed value is therefore (output - 2^(P-1)).
//
// Reference data is signed PGX (header "PG ML - <prec> <w> <h>") produced and
// round-tripped losslessly by OpenJPEG 2.4.0.

// readPGX reads a signed big-endian PGX file, returning precision and samples as
// signed ints.
func readPGX(t *testing.T, path string) (prec, w, h int, samples []int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var pg, endian, sign string
	if _, err := fmt.Fscan(r, &pg, &endian, &sign, &prec, &w, &h); err != nil {
		t.Fatalf("read PGX header: %v", err)
	}
	if _, err := r.ReadByte(); err != nil { // newline after header
		t.Fatalf("read newline: %v", err)
	}
	samples = make([]int, w*h)
	for i := range samples {
		if prec <= 8 {
			b, err := r.ReadByte()
			if err != nil {
				t.Fatalf("read sample %d: %v", i, err)
			}
			samples[i] = int(int8(b))
		} else {
			hi, err := r.ReadByte()
			if err != nil {
				t.Fatalf("read sample %d: %v", i, err)
			}
			lo, err := r.ReadByte()
			if err != nil {
				t.Fatalf("read sample %d: %v", i, err)
			}
			samples[i] = int(int16(uint16(hi)<<8 | uint16(lo)))
		}
	}
	return prec, w, h, samples
}

func decodeAndCheckSigned(t *testing.T, j2kPath, pgxPath string) {
	t.Helper()
	prec, w, h, want := readPGX(t, pgxPath)
	img := decodeImage(t, j2kPath)
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("size mismatch: got %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	offset := 1 << uint(prec-1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			var got int
			if prec <= 8 {
				got = int(uint8(r >> 8))
			} else {
				got = int(uint16(r))
			}
			if got != want[y*w+x]+offset {
				t.Fatalf("pixel(%d,%d): got %d, want %d (signed %d)", x, y, got, want[y*w+x]+offset, want[y*w+x])
			}
		}
	}
}

// TestDecode_Signed8 decodes signed 8-bit grayscale (Gray output path).
func TestDecode_Signed8(t *testing.T) {
	decodeAndCheckSigned(t, "../testdata/signed8_n2.j2k", "../testdata/signed8.pgx")
}

// TestDecode_Signed12 decodes signed 12-bit grayscale (Gray16 output path).
func TestDecode_Signed12(t *testing.T) {
	decodeAndCheckSigned(t, "../testdata/signed12_n2.j2k", "../testdata/signed12.pgx")
}
