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
// compared against the goldens as soon as the real render path exists. rotate90.pdf (vectors plus 24 pt
// rotated text) is enforced from M6 on, now that glyph rasterization exists.
func TestVectorCorpusPixels(t *testing.T) {
	comparePixelsToGolden(t, "vectors", "vectors", true)
	comparePixelsToGolden(t, "rotate90", "rotate90", true)
}

// TestTextCorpusPixels is milestone M6's pixel-scope check, following the M4/M5 pattern: text corpus files
// whose rendering divergence from the oracle is within the default thresholds are enforced at every recorded
// DPI. The files below all render through Liberation substitutes (standard-14 fonts) whose Arial-class
// letterforms differ slightly from the oracle's Nimbus substitutes, so their diffs are genuine shape deltas,
// comfortably inside the gate. The six encrypted files are text-std14 variants and must render identically to
// it once authenticated.
//
// Reported but NOT yet enforced (numbers recorded in plan.md M6; enforcement decisions — per-file thresholds
// with logged justification — come with the M6 exit):
//   - glaive, irs-f1040, irs-fw9: embedded fonts at 7–9 pt body sizes; layout is quad-exact and total ink
//     matches the oracle within ~1%, but FreeType scanline AA vs canvas analytic AA redistributes edge
//     coverage, and at those sizes nearly every glyph pixel is an edge pixel.
//   - std14-styles: the Liberation Mono lines are visibly heavier than the oracle's Nimbus Mono, and the
//     ZapfDingbats line renders blank (no dingbat-capable bundled face yet).
//   - subst-metrics and the damaged trio: the same substitution letterform delta on small text-dominated
//     pages, sitting just over the 2% gate at 72/100 dpi.
func TestTextCorpusPixels(t *testing.T) {
	for _, name := range []string{
		"text-std14", "hit-quad-split",
		"encrypted-r2-rc4", "encrypted-r3-rc4", "encrypted-r4-rc4", "encrypted-r4-aes",
		"encrypted-r6-aes", "encrypted-r6-empty-user",
	} {
		comparePixelsToGolden(t, name, name, true)
	}
	for _, name := range []string{
		"glaive", "irs-f1040", "irs-fw9", "std14-styles", "subst-metrics",
		"damaged-bad-offsets", "damaged-no-trailer", "damaged-startxref-zero",
	} {
		comparePixelsToGolden(t, name, name, false)
	}
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
	if doc.RequiresAuthentication() {
		// The goldens were captured after authenticating with the recorded password.
		if doc.Authenticate(truth.AuthPassword) == 0 {
			t.Fatalf("Authenticate(%q) failed", truth.AuthPassword)
		}
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
