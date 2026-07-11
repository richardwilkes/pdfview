package pdfview_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/richardwilkes/pdfview"
	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// TestVectorCorpusPixels is milestone M4's pixel-scope check (plan.md decision log, 2026-07-11): full-corpus
// pixel enforcement waits for M8, but the vector corpus — content the graphics core alone must reproduce — is
// compared against the goldens as soon as the real render path exists. rotate90.pdf additionally verifies the
// rotated page CTM but contains text, which renders nothing until M6, so its numbers are reported without
// being enforced until then.
func TestVectorCorpusPixels(t *testing.T) {
	comparePixelsToGolden(t, "vectors", "vectors", true)
	comparePixelsToGolden(t, "rotate90", "rotate90", false)
}

// TestImageCorpusPixels is milestone M5's pixel-scope check, following the M4 pattern: the image corpus —
// content the imaging pipeline must reproduce — is enforced against the goldens at every recorded DPI.
//
// The two stub-codec files pin the plan's blank-not-error contract. For images-jpx that is the golden itself:
// MuPDF's openjpeg rejects the payload and MuPDF drops the image, so its golden is the page with a blank image
// area — exactly the stub's output. For images-jbig2 MuPDF instead pads the failed decode into a black square
// (see the decision log), which a blank-rendering stub must not match; its render is compared against the
// images-jpx golden instead, which is byte-identical page content (same MediaBox, same vector marks, same image
// placement) with the image correctly absent.
func TestImageCorpusPixels(t *testing.T) {
	for _, name := range []string{
		"images-dct", "images-raw", "images-indexed", "images-imagemask", "images-inline",
		"images-smask", "images-ccitt", "images-jpx", "images-interpolate",
	} {
		comparePixelsToGolden(t, name, name, true)
	}
	comparePixelsToGolden(t, "images-jbig2", "images-jpx", true)
}

// comparePixelsToGolden renders corpus file name at every DPI recorded in goldenName's truth.json and compares
// pixels. goldenName equals name except for the stub-codec cross-check described on TestImageCorpusPixels.
func comparePixelsToGolden(t *testing.T, name, goldenName string, enforce bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", name+".pdf"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()
	goldenDir := filepath.Join("testfiles", "goldens", goldenName)
	truth, err := testsupport.LoadTruth(filepath.Join(goldenDir, "truth.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range truth.Pages {
		for _, dpi := range truth.DPIs {
			label := fmt.Sprintf("%s page %d dpi %d", name, page.Page, dpi)
			render, ok := page.Renders[strconv.Itoa(dpi)]
			if !ok {
				t.Fatalf("%s: golden records no render", label)
			}
			rendered, renderErr := doc.RenderPage(page.Page, dpi, 0, "")
			if renderErr != nil {
				t.Errorf("%s: RenderPage: %v", label, renderErr)
				continue
			}
			want, loadErr := testsupport.LoadImage(filepath.Join(goldenDir, render.PNG))
			if loadErr != nil {
				t.Errorf("%s: %v", label, loadErr)
				continue
			}
			diff, diffErr := testsupport.ComparePixels(rendered.Image, want)
			if diffErr != nil {
				t.Errorf("%s: %v", label, diffErr)
				continue
			}
			switch {
			case enforce && !diff.WithinDefaultThresholds():
				t.Errorf("%s: pixels diverge beyond thresholds: %s", label, diff)
			default:
				t.Logf("%s: %s", label, diff)
			}
		}
	}
}
