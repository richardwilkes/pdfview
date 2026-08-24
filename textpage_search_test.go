// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/richardwilkes/pdfview"
	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// openGolden opens the corpus file a golden was taken from, authenticating it when the golden records a password, and
// releases it when the test ends.
func openGolden(t *testing.T, golden *testsupport.Golden) *pdfview.Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", golden.Truth.File))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatalf("New(%s): %v", golden.Truth.File, err)
	}
	t.Cleanup(doc.Release)
	if golden.Truth.RequiresAuth || golden.Truth.AuthPassword != "" {
		if doc.Authenticate(golden.Truth.AuthPassword) == 0 {
			t.Fatalf("Authenticate(%q) failed", golden.Truth.AuthPassword)
		}
	}
	return doc
}

// TestTextPageSearchMatchesRenderPage is the contract that lets a viewer search a TextPage it already holds instead of
// re-rendering the page: over every corpus fixture the goldens record needles for, at every page and DPI they record,
// TextPage.Search must produce exactly what the same render puts in RenderedPage.SearchHits — the same rectangles in
// the same order, and nil for the same needles. The two reach that answer along different paths, one searching the
// device still recording the page and the other the characters a TextPage kept, so nothing but this comparison keeps
// them in step. The needle set includes probes that must not match (text-ligature's "sacrice", "fute"), which pins the
// nil case across the corpus as well.
func TestTextPageSearchMatchesRenderPage(t *testing.T) {
	goldens, err := testsupport.LoadGoldens(filepath.Join("testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	if len(goldens) == 0 {
		t.Fatal("no goldens present; run oracle/regen.sh and commit the output")
	}
	searched := 0
	for _, golden := range goldens {
		if len(golden.Truth.Needles) == 0 {
			continue
		}
		t.Run(golden.Name, func(t *testing.T) {
			doc := openGolden(t, golden)
			for _, page := range golden.Truth.Pages {
				for _, dpi := range golden.Truth.DPIs {
					tp, textErr := doc.TextPage(page.Page, dpi)
					if textErr != nil {
						t.Fatalf("page %d dpi %d: TextPage: %v", page.Page, dpi, textErr)
					}
					for _, needle := range golden.Truth.Needles {
						rendered, renderErr := doc.RenderPage(page.Page, dpi, pdfview.OverallMaxHits, needle)
						if renderErr != nil {
							t.Fatalf("page %d dpi %d: RenderPage for %q: %v", page.Page, dpi, needle, renderErr)
						}
						got := tp.Search(needle, pdfview.OverallMaxHits)
						if !reflect.DeepEqual(got, rendered.SearchHits) {
							t.Errorf("page %d dpi %d needle %q: Search = %v, the render's SearchHits = %v",
								page.Page, dpi, needle, got, rendered.SearchHits)
						}
						searched++
					}
				}
			}
		})
	}
	if searched == 0 {
		t.Fatal("no golden records a needle, so nothing was compared")
	}
	t.Logf("compared %d needle/page/dpi combinations", searched)
}

// TestTextPageSearchMultipleAndTruncatedHits pins the two ends of the maxHits budget against the render for a needle
// the fixture carries nine times: an ample budget reports every match, and a budget below the match count truncates to
// exactly the rectangles the render truncates to, in the same order.
func TestTextPageSearchMultipleAndTruncatedHits(t *testing.T) {
	const needle = "GURPS"
	doc := openCorpus(t, "glaive.pdf")
	tp, err := doc.TextPage(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if all := tp.Search(needle, pdfview.OverallMaxHits); len(all) != 9 {
		t.Fatalf("the fixture no longer holds 9 %q hits on page 0; Search found %d", needle, len(all))
	}
	for _, maxHits := range []int{1, 3, 8, 9, 20, pdfview.OverallMaxHits} {
		rendered, renderErr := doc.RenderPage(0, 100, maxHits, needle)
		if renderErr != nil {
			t.Fatalf("maxHits %d: RenderPage: %v", maxHits, renderErr)
		}
		got := tp.Search(needle, maxHits)
		if !reflect.DeepEqual(got, rendered.SearchHits) {
			t.Errorf("maxHits %d: Search = %v, the render's SearchHits = %v", maxHits, got, rendered.SearchHits)
		}
		if want := min(maxHits, 9); len(got) != want {
			t.Errorf("maxHits %d: Search returned %d hits, want %d", maxHits, len(got), want)
		}
	}
}

// TestTextPageSearchNoMatch pins that a needle the page does not carry comes back as nil rather than as an empty
// slice, which is what the render reports, so a caller testing the result against nil sees the same thing either way.
func TestTextPageSearchNoMatch(t *testing.T) {
	doc := openCorpus(t, "text-std14.pdf")
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"absent", "Hello Worlds", "QUICKLY", "你好"} {
		rendered, renderErr := doc.RenderPage(0, 72, 100, needle)
		if renderErr != nil {
			t.Fatalf("needle %q: RenderPage: %v", needle, renderErr)
		}
		if rendered.SearchHits != nil {
			t.Fatalf("the fixture now contains %q; the render reports %v", needle, rendered.SearchHits)
		}
		if got := tp.Search(needle, 100); got != nil {
			t.Errorf("needle %q: Search = %v, want nil", needle, got)
		}
	}
}

// TestTextPageSearchAtDPIMatchesRenderPage pins that a re-labeled TextPage searches in the pixel space of the image
// RenderPage produces at that dpi, so one extraction serves every zoom level. The dpis include ones no golden records.
func TestTextPageSearchAtDPIMatchesRenderPage(t *testing.T) {
	const needle = "backup withholding"
	doc := openCorpus(t, "irs-fw9.pdf")
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	for _, dpi := range []int{72, 96, 100, 150, 200, 288} {
		rendered, renderErr := doc.RenderPage(0, dpi, 100, needle)
		if renderErr != nil {
			t.Fatalf("%d dpi: RenderPage: %v", dpi, renderErr)
		}
		if len(rendered.SearchHits) == 0 {
			t.Fatalf("%d dpi: the fixture no longer contains %q", dpi, needle)
		}
		got := tp.AtDPI(dpi).Search(needle, 100)
		if !reflect.DeepEqual(got, rendered.SearchHits) {
			t.Errorf("%d dpi: AtDPI(%d).Search = %v, the render's SearchHits = %v", dpi, dpi, got,
				rendered.SearchHits)
		}
		for _, hit := range got {
			if !hit.In(rendered.Image.Rect) {
				t.Errorf("%d dpi: hit %v is not inside the %v image", dpi, hit, rendered.Image.Rect)
			}
		}
	}
}

// TestTextPageSearchForSizeMatchesRenderPageForSize pins the same agreement for a fit-to-box image, whose scale is
// min(maxWidth/width, maxHeight/height) and so is no whole dpi: no TextPage made from a dpi can name its pixels, and
// only ForSize labels the text for it.
func TestTextPageSearchForSizeMatchesRenderPageForSize(t *testing.T) {
	const needle = "taxpayer"
	doc := openCorpus(t, "irs-fw9.pdf")
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	for _, box := range [][2]int{
		{200, 200},  // Width-limited and tiny.
		{813, 611},  // Height-limited, and neither extent a whole multiple of the page's.
		{1000, 999}, // Larger than the page renders at 72 dpi.
	} {
		rendered, renderErr := doc.RenderPageForSize(0, box[0], box[1], 100, needle)
		if renderErr != nil {
			t.Fatalf("box %v: RenderPageForSize: %v", box, renderErr)
		}
		if len(rendered.SearchHits) == 0 {
			t.Fatalf("box %v: the fixture no longer contains %q", box, needle)
		}
		fitted, fitErr := tp.ForSize(box[0], box[1])
		if fitErr != nil {
			t.Fatalf("box %v: ForSize: %v", box, fitErr)
		}
		got := fitted.Search(needle, 100)
		if !reflect.DeepEqual(got, rendered.SearchHits) {
			t.Errorf("box %v: Search = %v, the render's SearchHits = %v", box, got, rendered.SearchHits)
		}
		for _, hit := range got {
			if !hit.In(rendered.Image.Rect) {
				t.Errorf("box %v: hit %v is not inside the %v image", box, hit, rendered.Image.Rect)
			}
		}
	}
}

// TestTextPageSearchDegenerateArguments pins the arguments that answer nil without consulting the page, matching what
// the render's own search refuses: no receiver, no needle, a needle holding nothing a match could anchor on, and no
// budget.
func TestTextPageSearchDegenerateArguments(t *testing.T) {
	doc := openCorpus(t, "text-std14.pdf")
	tp, err := doc.TextPage(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	if got := tp.Search("Hello", 100); len(got) == 0 {
		t.Fatalf("the fixture no longer contains %q; the negative cases below would prove nothing", "Hello")
	}
	var nilPage *pdfview.TextPage
	if got := nilPage.Search("Hello", 100); got != nil {
		t.Errorf("a nil TextPage searched to %v, want nil", got)
	}
	if got := (&pdfview.TextPage{}).Search("Hello", 100); got != nil {
		t.Errorf("a zero TextPage searched to %v, want nil", got)
	}
	for _, tc := range []struct {
		name    string
		needle  string
		maxHits int
	}{
		{name: "empty needle", needle: "", maxHits: 100},
		{name: "space needle", needle: " ", maxHits: 100},
		{name: "whitespace-only needle", needle: " \t\n ", maxHits: 100},
		{name: "zero maxHits", needle: "Hello", maxHits: 0},
		{name: "negative maxHits", needle: "Hello", maxHits: -5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The render refuses the same arguments, so the two still agree here.
			rendered, renderErr := doc.RenderPage(0, 72, tc.maxHits, tc.needle)
			if renderErr != nil {
				t.Fatalf("RenderPage: %v", renderErr)
			}
			if rendered.SearchHits != nil {
				t.Fatalf("the render reported %v for these arguments, want nil", rendered.SearchHits)
			}
			if got := tp.Search(tc.needle, tc.maxHits); got != nil {
				t.Errorf("Search = %v, want nil", got)
			}
		})
	}
}

// TestTextPageSearchHonorsOverallMaxHits pins that the package-wide cap binds Search exactly as it binds the render:
// it lowers a larger maxHits, and a cap of zero or less turns off searching entirely. No test in this package runs in
// parallel, so mutating the global here cannot race another one.
func TestTextPageSearchHonorsOverallMaxHits(t *testing.T) {
	const needle = "GURPS"
	doc := openCorpus(t, "glaive.pdf")
	tp, err := doc.TextPage(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	full := tp.Search(needle, pdfview.OverallMaxHits)
	if len(full) != 9 {
		t.Fatalf("the fixture no longer holds 9 %q hits on page 0; Search found %d", needle, len(full))
	}
	prevCap := pdfview.OverallMaxHits
	t.Cleanup(func() { pdfview.OverallMaxHits = prevCap })
	for _, tc := range []struct {
		name string
		cap  int
		want int
	}{
		{name: "cap below maxHits", cap: 4, want: 4},
		{name: "cap of one", cap: 1, want: 1},
		{name: "cap above the match count", cap: 50, want: 9},
		{name: "cap of zero", cap: 0, want: 0},
		{name: "negative cap", cap: -1, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdfview.OverallMaxHits = tc.cap
			rendered, renderErr := doc.RenderPage(0, 100, 100, needle)
			if renderErr != nil {
				t.Fatalf("RenderPage: %v", renderErr)
			}
			got := tp.Search(needle, 100)
			if !reflect.DeepEqual(got, rendered.SearchHits) {
				t.Errorf("Search = %v, the render's SearchHits = %v", got, rendered.SearchHits)
			}
			if len(got) != tc.want {
				t.Errorf("Search returned %d hits, want %d", len(got), tc.want)
			}
			if tc.want == 0 && got != nil {
				t.Errorf("Search = %v, want nil", got)
			}
			// The hits that survive the cap are the first ones, in the order the uncapped search reported them.
			for i := range got {
				if got[i] != full[i] {
					t.Errorf("hit %d = %v, the uncapped search reports %v", i, got[i], full[i])
				}
			}
		})
	}
}

// TestTextPageSearchOutlivesRelease pins that a TextPage keeps searching after its document is gone, the whole point
// of extracting once and holding the result: a viewer can search a page it can no longer render.
func TestTextPageSearchOutlivesRelease(t *testing.T) {
	const needle = "Spaced words"
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", "text-std14.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := doc.TextPage(0, 150)
	if err != nil {
		doc.Release()
		t.Fatal(err)
	}
	rendered, err := doc.RenderPage(0, 150, 100, needle)
	if err != nil {
		doc.Release()
		t.Fatal(err)
	}
	want := rendered.SearchHits
	if len(want) == 0 {
		doc.Release()
		t.Fatalf("the fixture no longer contains %q", needle)
	}
	doc.Release()
	if got := tp.Search(needle, 100); !reflect.DeepEqual(got, want) {
		t.Errorf("after Release, Search = %v, want %v", got, want)
	}
	// Re-labeling after Release still works, and so does searching the re-labeled page.
	if got := tp.AtDPI(150).Search(needle, 100); !reflect.DeepEqual(got, want) {
		t.Errorf("after Release, AtDPI(150).Search = %v, want %v", got, want)
	}
}
