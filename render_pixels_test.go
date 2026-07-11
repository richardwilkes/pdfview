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
	comparePixelsToGolden(t, "vectors", true)
	comparePixelsToGolden(t, "rotate90", false)
}

func comparePixelsToGolden(t *testing.T, name string, enforce bool) {
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
	goldenDir := filepath.Join("testfiles", "goldens", name)
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
