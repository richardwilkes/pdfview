// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// Dictionary keys the tests repeat.
const (
	keyBPC        cos.Name = "BPC"
	keyColorSpace cos.Name = "ColorSpace"
)

// testDoc returns a minimal document for Resolve calls; the image dictionaries and mask streams in these tests are
// built directly as cos values, so nothing needs to live in the file itself.
func testDoc(t *testing.T) *cos.Document {
	t.Helper()
	const pdf = "%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R /Size 2 >>\nstartxref\n0\n%%EOF\n"
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSampleReader(t *testing.T) {
	data := []byte{0b10110100, 0xab, 0xcd}
	r := sampleReader{data: data, bpc: 1}
	for i, want := range []uint32{1, 0, 1, 1, 0, 1, 0, 0} {
		if got := r.next(); got != want {
			t.Fatalf("bpc1 sample %d: got %d want %d", i, got, want)
		}
	}
	r = sampleReader{data: data, bpc: 2}
	for i, want := range []uint32{2, 3, 1, 0} {
		if got := r.next(); got != want {
			t.Fatalf("bpc2 sample %d: got %d want %d", i, got, want)
		}
	}
	r = sampleReader{data: data, bpc: 4}
	for i, want := range []uint32{0xb, 0x4, 0xa, 0xb} {
		if got := r.next(); got != want {
			t.Fatalf("bpc4 sample %d: got %d want %d", i, got, want)
		}
	}
	r = sampleReader{data: data, bpc: 16}
	if got := r.next(); got != 0xb4ab {
		t.Fatalf("bpc16 sample: got %#x", got)
	}
	// Reads past the end complete with zero samples (truncation leniency), including a straddling 16-bit read.
	if got := r.next(); got != 0xcd00 {
		t.Fatalf("bpc16 straddling sample: got %#x", got)
	}
	if got := r.next(); got != 0 {
		t.Fatalf("read past end: got %d", got)
	}
	// seek is byte-based: row alignment.
	r = sampleReader{data: data, bpc: 4}
	r.seek(1)
	if got := r.next(); got != 0xa {
		t.Fatalf("seek: got %#x", got)
	}
}

func TestGrayDecodeArray(t *testing.T) {
	d := testDoc(t)
	dict := cos.Dict{"W": cos.Integer(2), "H": cos.Integer(1), keyBPC: cos.Integer(8), "CS": cos.Name("G")}
	img, err := DecodeInline(d, dict, []byte{0x00, 0xff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[0] != 0 || img.Pix[4] != 255 {
		t.Fatalf("gray identity: %v", img.Pix)
	}
	dict["D"] = cos.Array{cos.Integer(1), cos.Integer(0)}
	img, err = DecodeInline(d, dict, []byte{0x00, 0xff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[0] != 255 || img.Pix[4] != 0 {
		t.Fatalf("gray inverted by Decode [1 0]: %v", img.Pix)
	}
}

func TestBPC16(t *testing.T) {
	d := testDoc(t)
	dict := cos.Dict{"W": cos.Integer(2), "H": cos.Integer(1), keyBPC: cos.Integer(16), "CS": cos.Name("G")}
	img, err := DecodeInline(d, dict, []byte{0x00, 0x00, 0xff, 0xff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[0] != 0 || img.Pix[4] != 255 {
		t.Fatalf("bpc16 endpoints: %v", img.Pix)
	}
}

func TestIndexedClamp(t *testing.T) {
	d := testDoc(t)
	space := cos.Array{cos.Name("Indexed"), cos.Name("DeviceRGB"), cos.Integer(1), cos.String("\xff\x00\x00\x00\x00\xff")}
	dict := cos.Dict{"W": cos.Integer(3), "H": cos.Integer(1), keyBPC: cos.Integer(8), "CS": space}
	img, err := DecodeInline(d, dict, []byte{0, 1, 200}, nil) // 200 clamps to hival 1
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[0] != 255 || img.Pix[2] != 0 {
		t.Fatalf("index 0: %v", img.Pix[:4])
	}
	if img.Pix[4] != 0 || img.Pix[6] != 255 {
		t.Fatalf("index 1: %v", img.Pix[4:8])
	}
	if img.Pix[8] != 0 || img.Pix[10] != 255 {
		t.Fatalf("out-of-range index must clamp to hival: %v", img.Pix[8:12])
	}
}

func TestStencilPolarity(t *testing.T) {
	d := testDoc(t)
	dict := cos.Dict{"IM": cos.Boolean(true), "W": cos.Integer(4), "H": cos.Integer(1)}
	img, err := DecodeInline(d, dict, []byte{0b01010000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !img.Stencil {
		t.Fatal("not a stencil")
	}
	for i, want := range []byte{255, 0, 255, 0} { // 0 bits paint under the default Decode
		if img.Pix[i] != want {
			t.Fatalf("default polarity: %v", img.Pix)
		}
	}
	dict["Decode"] = cos.Array{cos.Integer(1), cos.Integer(0)}
	img, err = DecodeInline(d, dict, []byte{0b01010000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []byte{0, 255, 0, 255} {
		if img.Pix[i] != want {
			t.Fatalf("inverted polarity: %v", img.Pix)
		}
	}
}

// expectedCCITTBitmap mirrors the corpus generator's test bitmap: a 2-pixel border box plus 4×4 diagonal stripes, 1 =
// black (see testfiles/corpus/README.md).
func expectedCCITTBitmap() (bits []bool, w, h int) {
	w, h = 32, 24
	bits = make([]bool, w*h)
	for y := range h {
		for x := range w {
			bits[y*w+x] = x < 2 || y < 2 || x >= w-2 || y >= h-2 || (x/4+y/4)%2 == 0
		}
	}
	return bits, w, h
}

// corpusImageStreams opens a corpus file and returns its image XObject streams in object-number order.
func corpusImageStreams(t *testing.T, name string) (*cos.Document, []*cos.Stream) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testfiles", "corpus", name))
	if err != nil {
		t.Fatal(err)
	}
	d, err := cos.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	var streams []*cos.Stream
	for num := 1; num < 64; num++ {
		if stream, ok := cos.AsStream(d.LoadObject(num)); ok {
			if subtype, _ := d.GetName(stream.Dict, "Subtype"); subtype == "Image" {
				streams = append(streams, stream)
			}
		}
	}
	return d, streams
}

func TestCCITTG4RoundTrip(t *testing.T) {
	d, streams := corpusImageStreams(t, "images-ccitt.pdf")
	if len(streams) != 2 {
		t.Fatalf("expected the G4 pair, got %d image streams", len(streams))
	}
	bits, w, h := expectedCCITTBitmap()
	for i, stream := range streams {
		img, err := DecodeXObject(d, stream, nil)
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if img.Width != w || img.Height != h {
			t.Fatalf("stream %d: %dx%d", i, img.Width, img.Height)
		}
		blackIs1 := i == 1 // The second stream sets /BlackIs1 true with the same payload, inverting the bits.
		for p, black := range bits {
			want := byte(255)
			if black != blackIs1 {
				want = 0
			}
			// The gray curve maps full black to 0 and full white to 255 exactly.
			if got := img.Pix[p*4]; got != want {
				t.Fatalf("stream %d pixel %d: got %d want %d", i, p, got, want)
			}
		}
	}
}

func TestDCTGrayAndRGB(t *testing.T) {
	d := testDoc(t)
	gray := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range gray.Pix {
		gray.Pix[i] = byte(i * 3)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gray, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	dict := cos.Dict{"W": cos.Integer(8), "H": cos.Integer(8), keyBPC: cos.Integer(8), "CS": cos.Name("G"), "F": cos.Name("DCT")}
	img, err := DecodeInline(d, dict, buf.Bytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 64 {
		want := int(gray.Pix[i])
		got := int(img.Pix[i*4])
		if got-want > 3 || want-got > 3 {
			t.Fatalf("gray pixel %d: got %d want ~%d", i, got, want)
		}
	}
	// Flat 2×2 blocks: the encoder's 4:2:0 chroma subsampling is then lossless, so only DCT rounding remains.
	rgb := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			rgb.Set(x, y, color.RGBA{R: uint8(x / 2 * 60), G: uint8(y / 2 * 60), B: 128, A: 255})
		}
	}
	buf.Reset()
	if err = jpeg.Encode(&buf, rgb, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	dict["CS"] = cos.Name("RGB")
	img, err = DecodeInline(d, dict, buf.Bytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 64 {
		for c := range 3 {
			want := int(rgb.Pix[i*4+c])
			got := int(img.Pix[i*4+c])
			if got-want > 6 || want-got > 6 {
				t.Fatalf("rgb pixel %d chan %d: got %d want ~%d", i, c, got, want)
			}
		}
	}
}

func TestSMaskComposite(t *testing.T) {
	d := testDoc(t)
	smask := &cos.Stream{
		Dict: cos.Dict{
			"Width": cos.Integer(2), "Height": cos.Integer(1), "BitsPerComponent": cos.Integer(8),
			keyColorSpace: cos.Name("DeviceGray"),
		},
		Raw: []byte{0x00, 0x80},
	}
	dict := cos.Dict{
		"W": cos.Integer(4), "H": cos.Integer(2), keyBPC: cos.Integer(8), "CS": cos.Name("RGB"),
		"SMask": smask,
	}
	payload := bytes.Repeat([]byte{200, 100, 50}, 8)
	img, err := DecodeInline(d, dict, payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !img.HasAlpha {
		t.Fatal("soft mask did not mark alpha")
	}
	// The 2×1 mask stretches over the 4×2 base: left half alpha 0, right half alpha 128.
	for y := range 2 {
		for x := range 4 {
			want := byte(0)
			if x >= 2 {
				want = 128
			}
			if got := img.Pix[(y*4+x)*4+3]; got != want {
				t.Fatalf("alpha at (%d,%d): got %d want %d", x, y, got, want)
			}
		}
	}
	// An /SMask overrides any /Mask entry, including a color-key array (ISO 32000-2 8.9.6.6).
	dict["Mask"] = cos.Array{cos.Integer(0), cos.Integer(255), cos.Integer(0), cos.Integer(255), cos.Integer(0), cos.Integer(255)}
	img, err = DecodeInline(d, dict, payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[3] != 0 || img.Pix[11] != 128 {
		t.Fatalf("SMask must override Mask: %v", img.Pix)
	}
}

func TestStencilMaskEntry(t *testing.T) {
	d := testDoc(t)
	mask := &cos.Stream{
		Dict: cos.Dict{"Width": cos.Integer(2), "Height": cos.Integer(1), "ImageMask": cos.Boolean(true)},
		Raw:  []byte{0b01000000},
	}
	dict := cos.Dict{
		"W": cos.Integer(2), "H": cos.Integer(1), keyBPC: cos.Integer(8), "CS": cos.Name("G"),
		"Mask": mask,
	}
	img, err := DecodeInline(d, dict, []byte{10, 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Mask sample 0 → painted (opaque), 1 → masked out.
	if img.Pix[3] != 255 || img.Pix[7] != 0 {
		t.Fatalf("stencil mask polarity: %v", img.Pix)
	}
}

func TestColorKeyMask(t *testing.T) {
	d := testDoc(t)
	dict := cos.Dict{
		"W": cos.Integer(2), "H": cos.Integer(1), keyBPC: cos.Integer(8), "CS": cos.Name("RGB"),
		"Mask": cos.Array{cos.Integer(90), cos.Integer(110), cos.Integer(0), cos.Integer(50), cos.Integer(200), cos.Integer(255)},
	}
	img, err := DecodeInline(d, dict, []byte{100, 25, 220, 100, 60, 220}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[3] != 0 {
		t.Fatalf("in-range pixel must be keyed out: %v", img.Pix[:4])
	}
	if img.Pix[7] != 255 {
		t.Fatalf("out-of-range pixel must stay opaque: %v", img.Pix[4:8])
	}
}

func TestPixelBudgets(t *testing.T) {
	d := testDoc(t)
	// Absurd absolute dimensions.
	dict := cos.Dict{"W": cos.Integer(1 << 30), "H": cos.Integer(1 << 30), keyBPC: cos.Integer(8), "CS": cos.Name("G")}
	if _, err := DecodeInline(d, dict, []byte{0}, nil); err == nil {
		t.Fatal("absurd dimensions accepted")
	}
	// Disproportionate dimensions over a tiny payload: over the 2^22-pixel floor with one byte of data.
	dict = cos.Dict{"W": cos.Integer(4096), "H": cos.Integer(2048), keyBPC: cos.Integer(8), "CS": cos.Name("G")}
	if _, err := DecodeInline(d, dict, []byte{0}, nil); err == nil {
		t.Fatal("disproportionate dimensions accepted")
	}
	// The same claim with enough payload decodes (padded with zero samples).
	img, err := DecodeInline(d, dict, make([]byte, 1024+1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 4096 || img.Height != 2048 {
		t.Fatalf("dims: %dx%d", img.Width, img.Height)
	}
}

func TestStubCodecs(t *testing.T) {
	d := testDoc(t)
	for _, codec := range []string{"JBIG2Decode", "JPXDecode"} {
		dict := cos.Dict{
			"W": cos.Integer(4), "H": cos.Integer(4), keyBPC: cos.Integer(1), "CS": cos.Name("G"),
			"F": cos.Name(codec),
		}
		img, err := DecodeInline(d, dict, []byte{1, 2, 3, 4}, nil)
		if err == nil || img != nil {
			t.Fatalf("%s: stub must decline to decode", codec)
		}
		if !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("%s: %v", codec, err)
		}
	}
}

func TestInlineNamedColorSpace(t *testing.T) {
	d := testDoc(t)
	res := cos.Dict{keyColorSpace: cos.Dict{
		"CSX": cos.Array{cos.Name("Indexed"), cos.Name("DeviceRGB"), cos.Integer(1), cos.String("\x00\xff\x00\xff\x00\xff")},
	}}
	dict := cos.Dict{"W": cos.Integer(2), "H": cos.Integer(1), keyBPC: cos.Integer(1), "CS": cos.Name("CSX")}
	img, err := DecodeInline(d, dict, []byte{0b01000000}, res)
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[1] != 255 || img.Pix[5] != 0 {
		t.Fatalf("named colorspace lookup: %v", img.Pix)
	}
	// Without the resource dictionary the name is unresolvable.
	if _, err = DecodeInline(d, dict, []byte{0}, nil); err == nil {
		t.Fatal("unresolvable colorspace accepted")
	}
}
