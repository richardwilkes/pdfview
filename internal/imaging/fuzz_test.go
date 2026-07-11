package imaging

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// FuzzImaging drives the image decoder with hostile documents: the input is opened as a PDF and every stream
// object is decoded both as an image XObject (when it declares /Subtype /Image, exercising /SMask and /Mask
// references) and as an inline-image dictionary over its raw payload (exercising the abbreviated keys and the
// filter split against arbitrary dictionaries). The decoder must neither panic nor hang — malformed dictionaries
// and payloads, truncated codec data, and absurd dimension claims are all bounded by the caps documented in the
// package (maxImagePixels and maxPixelsFor run before any pixel allocation) — and every successful decode must
// return a pixel buffer consistent with its own dimensions. The image corpus files are the seeds.
func FuzzImaging(f *testing.F) {
	corpus, err := filepath.Glob(filepath.Join("..", "..", "testfiles", "corpus", "images-*.pdf"))
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range corpus {
		if data, readErr := os.ReadFile(path); readErr == nil {
			f.Add(data)
		}
	}
	res := cos.Dict{keyColorSpace: cos.Dict{
		"CSX": cos.Array{cos.Name("Indexed"), cos.Name("DeviceRGB"), cos.Integer(3), cos.String("\x01\x02\x03")},
	}}
	f.Fuzz(func(t *testing.T, data []byte) {
		d, openErr := cos.Open(data)
		if openErr != nil {
			return
		}
		maxNum := 64
		if size, ok := d.GetInt(d.Trailer(), "Size"); ok && size > 0 && size < 64 {
			maxNum = int(size)
		}
		for num := 1; num <= maxNum; num++ {
			stream, ok := cos.AsStream(d.LoadObject(num))
			if !ok {
				continue
			}
			if subtype, _ := d.GetName(stream.Dict, "Subtype"); subtype == "Image" {
				img, decodeErr := DecodeXObject(d, stream, res)
				checkDecode(t, img, decodeErr)
			}
			img, decodeErr := DecodeInline(d, stream.Dict, stream.Raw, res)
			checkDecode(t, img, decodeErr)
		}
	})
}

// checkDecode asserts the decode postcondition: success returns a non-nil image whose pixel buffer matches its
// dimensions and kind.
func checkDecode(t *testing.T, img *Image, err error) {
	t.Helper()
	if err != nil {
		return
	}
	if img == nil {
		t.Fatal("nil image without error")
	}
	want := img.Width * img.Height
	if !img.Stencil {
		want *= 4
	}
	if img.Width <= 0 || img.Height <= 0 || len(img.Pix) != want {
		t.Fatalf("inconsistent decode: %dx%d stencil=%v len=%d", img.Width, img.Height, img.Stencil, len(img.Pix))
	}
}
