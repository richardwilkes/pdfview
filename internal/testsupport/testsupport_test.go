package testsupport_test

import (
	"image"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// TestGoldensSelfConsistent validates the committed goldens themselves: every truth.json loads under the strict
// schema, agrees with its own page/DPI/needle structure, and every referenced PNG decodes to the recorded
// dimensions. This guards the committed artifacts (for example against line-ending or encoding corruption)
// independently of how much of the engine exists yet.
func TestGoldensSelfConsistent(t *testing.T) {
	goldens, err := testsupport.LoadGoldens(filepath.Join("..", "..", "testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	if len(goldens) == 0 {
		t.Fatal("no goldens present; run oracle/regen.sh and commit the output")
	}
	for _, golden := range goldens {
		t.Run(golden.Name, func(t *testing.T) {
			checkGolden(t, golden)
		})
	}
}

func checkGolden(t *testing.T, golden *testsupport.Golden) {
	truth := golden.Truth
	if truth.File == "" {
		t.Error("file is empty")
	}
	if len(truth.SHA256) != 64 {
		t.Errorf("sha256 %q is not 64 hex chars", truth.SHA256)
	}
	if truth.MuPDF == "" {
		t.Error("mupdf version is empty")
	}
	if truth.PageCount < 1 {
		t.Errorf("pageCount is %d", truth.PageCount)
	}
	if len(truth.Pages) != truth.PageCount {
		t.Errorf("%d page records for pageCount %d", len(truth.Pages), truth.PageCount)
	}
	if len(truth.DPIs) == 0 {
		t.Error("no dpis")
	}
	if len(truth.Auth) < 2 {
		t.Errorf("auth table has %d attempts; the dump always records at least the empty and an invalid password", len(truth.Auth))
	}
	if truth.RequiresAuth && truth.AuthPassword == "" {
		t.Error("requiresAuth is set but no authPassword was recorded")
	}
	for _, page := range truth.Pages {
		checkPage(t, golden, page)
	}
}

func checkPage(t *testing.T, golden *testsupport.Golden, page *testsupport.Page) {
	truth := golden.Truth
	for _, needle := range truth.Needles {
		if _, ok := page.SearchRaw[needle]; !ok {
			t.Errorf("page %d: no raw search quads recorded for needle %q", page.Page, needle)
		}
	}
	for _, dpi := range truth.DPIs {
		render, ok := page.Renders[strconv.Itoa(dpi)]
		if !ok {
			t.Errorf("page %d: no render recorded for dpi %d", page.Page, dpi)
			continue
		}
		for _, needle := range truth.Needles {
			if _, hasNeedle := render.Search[needle]; !hasNeedle {
				t.Errorf("page %d dpi %d: no search hits recorded for needle %q", page.Page, dpi, needle)
			}
		}
		img, err := testsupport.LoadImage(filepath.Join(golden.Dir, render.PNG))
		if err != nil {
			t.Errorf("page %d dpi %d: %v", page.Page, dpi, err)
			continue
		}
		if img.Rect.Dx() != render.Width || img.Rect.Dy() != render.Height {
			t.Errorf("page %d dpi %d: PNG is %dx%d, truth records %dx%d",
				page.Page, dpi, img.Rect.Dx(), img.Rect.Dy(), render.Width, render.Height)
		}
		diff, err := testsupport.ComparePixels(img, img)
		if err != nil {
			t.Errorf("page %d dpi %d: %v", page.Page, dpi, err)
			continue
		}
		if diff.MaxDelta != 0 || !diff.WithinDefaultThresholds() {
			t.Errorf("page %d dpi %d: self-comparison is not clean: %s", page.Page, dpi, diff)
		}
	}
}

func TestComparePixels(t *testing.T) {
	base := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := range base.Pix {
		base.Pix[i] = uint8(50 + i*3)
	}
	altered := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	copy(altered.Pix, base.Pix)
	altered.Pix[1] += 30  // pixel 0: green channel, delta 30
	altered.Pix[7] -= 10  // pixel 1: alpha channel, delta 10
	altered.Pix[12] += 39 // pixel 3: red +39 ...
	altered.Pix[13] -= 5  // ... green -5; the per-pixel delta is the max across channels, so 39

	diff, err := testsupport.ComparePixels(altered, base)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Pixels != 4 {
		t.Errorf("Pixels = %d, want 4", diff.Pixels)
	}
	if diff.Over24 != 2 {
		t.Errorf("Over24 = %d, want 2", diff.Over24)
	}
	if diff.Over8 != 3 {
		t.Errorf("Over8 = %d, want 3", diff.Over8)
	}
	if diff.MaxDelta != 39 {
		t.Errorf("MaxDelta = %d, want 39", diff.MaxDelta)
	}
	if want := float64(30+10+0+39) / 4; diff.MeanDelta != want {
		t.Errorf("MeanDelta = %v, want %v", diff.MeanDelta, want)
	}
	if diff.WithinDefaultThresholds() {
		t.Error("expected thresholds to reject this diff")
	}

	clean, err := testsupport.ComparePixels(base, base)
	if err != nil {
		t.Fatal(err)
	}
	if clean.MaxDelta != 0 || clean.Over8 != 0 || clean.Over24 != 0 || clean.MeanDelta != 0 {
		t.Errorf("self-comparison is not clean: %s", clean)
	}
	if !clean.WithinDefaultThresholds() {
		t.Error("expected thresholds to accept a clean diff")
	}

	if _, err = testsupport.ComparePixels(base, image.NewNRGBA(image.Rect(0, 0, 3, 2))); err == nil {
		t.Error("expected an error for mismatched dimensions")
	}
}
