package doc_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// TestCorpusNavigation compares everything the navigation layer produces — page geometry, the raw outline tree,
// and raw link annotations — against the oracle-recorded raw (unscaled, page-space) values in every golden. The
// scaled public-API forms are covered by TestParity and pdf_test.go in the root package; this test pins the
// float32 page-space values they are derived from.
func TestCorpusNavigation(t *testing.T) {
	goldens, err := testsupport.LoadGoldens(filepath.Join("..", "..", "testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	if len(goldens) == 0 {
		t.Fatal("no goldens found")
	}
	for _, golden := range goldens {
		t.Run(golden.Name, func(t *testing.T) {
			truth := golden.Truth
			data, rerr := os.ReadFile(filepath.Join("..", "..", "testfiles", "corpus", truth.File))
			if rerr != nil {
				t.Fatal(rerr)
			}
			d, derr := doc.Open(data)
			if derr != nil {
				t.Fatalf("Open: %v", derr)
			}
			if truth.RequiresAuth {
				if status := d.Authenticate(truth.AuthPassword); status == 0 {
					t.Fatalf("Authenticate(%q) failed", truth.AuthPassword)
				}
			}
			compareOutlineLevel(t, "outline", d.Outline(), truth.TOCRaw)
			for _, page := range truth.Pages {
				comparePageGeometry(t, d, page)
				comparePageLinks(t, d, page)
			}
		})
	}
}

func comparePageGeometry(t *testing.T, d *doc.Document, page *testsupport.Page) {
	t.Helper()
	w, h, err := d.PageSize(page.Page)
	if err != nil {
		t.Errorf("PageSize(%d): %v", page.Page, err)
		return
	}
	// MuPDF normalizes the effective box to the origin, so the recorded raw bounds are always [0, 0, w, h];
	// PageSize's extent must match the recorded corner exactly (float32 for float32).
	if page.Bounds[0] != 0 || page.Bounds[1] != 0 {
		t.Errorf("page %d: recorded bounds %v do not start at the origin; the geometry model needs revisiting",
			page.Page, page.Bounds)
	}
	if w != page.Bounds[2] || h != page.Bounds[3] {
		t.Errorf("page %d: PageSize = (%v, %v), oracle says (%v, %v)", page.Page, w, h, page.Bounds[2], page.Bounds[3])
	}
}

// compareOutlineLevel checks one sibling chain of the outline against the recorded raw entries, recursing into
// children. Raw titles are compared before any sanitizing; nil recorded coordinates mean MuPDF reported a
// non-finite value, which the engine represents as NaN.
func compareOutlineLevel(t *testing.T, label string, got *doc.OutlineItem, want []*testsupport.TOCRawEntry) {
	t.Helper()
	count := 0
	for _, wantEntry := range want {
		if got == nil {
			break
		}
		entryLabel := fmt.Sprintf("%s[%d]", label, count)
		if got.Title != wantEntry.Title {
			t.Errorf("%s Title = %q, oracle says %q", entryLabel, got.Title, wantEntry.Title)
		}
		if got.Page != wantEntry.Page {
			t.Errorf("%s Page = %d, oracle says %d", entryLabel, got.Page, wantEntry.Page)
		}
		if !coordMatches(got.X, wantEntry.X) || !coordMatches(got.Y, wantEntry.Y) {
			t.Errorf("%s position = (%v, %v), oracle says (%v, %v)",
				entryLabel, got.X, got.Y, coordString(wantEntry.X), coordString(wantEntry.Y))
		}
		compareOutlineLevel(t, entryLabel, got.Down, wantEntry.Children)
		got = got.Next
		count++
	}
	if count != len(want) || got != nil {
		extra := 0
		for ; got != nil; got = got.Next {
			extra++
		}
		t.Errorf("%s has %d entries, oracle says %d", label, count+extra, len(want))
	}
}

func comparePageLinks(t *testing.T, d *doc.Document, page *testsupport.Page) {
	t.Helper()
	links := d.Links(page.Page)
	if len(links) != len(page.LinksRaw) {
		t.Errorf("page %d has %d links, oracle says %d", page.Page, len(links), len(page.LinksRaw))
		return
	}
	for i, want := range page.LinksRaw {
		got := links[i]
		if got.External != want.External {
			t.Errorf("page %d link %d External = %v, oracle says %v", page.Page, i, got.External, want.External)
		}
		// The oracle records MuPDF's synthesized intra-document URI ("#page=...") for internal links; the
		// engine leaves internal URIs empty (the public API never surfaces them), so only external URIs compare.
		if want.External && got.URI != want.URI {
			t.Errorf("page %d link %d URI = %q, oracle says %q", page.Page, i, got.URI, want.URI)
		}
		if got.Page != want.Page {
			t.Errorf("page %d link %d Page = %d, oracle says %d", page.Page, i, got.Page, want.Page)
		}
		if got.X0 != want.Rect[0] || got.Y0 != want.Rect[1] || got.X1 != want.Rect[2] || got.Y1 != want.Rect[3] {
			t.Errorf("page %d link %d rect = [%v %v %v %v], oracle says %v",
				page.Page, i, got.X0, got.Y0, got.X1, got.Y1, want.Rect)
		}
		if !coordMatches(got.DestX, want.DestX) || !coordMatches(got.DestY, want.DestY) {
			t.Errorf("page %d link %d dest = (%v, %v), oracle says (%v, %v)",
				page.Page, i, got.DestX, got.DestY, coordString(want.DestX), coordString(want.DestY))
		}
	}
}

// coordMatches compares an engine coordinate with a recorded one: nil records a non-finite oracle value and
// must be NaN here; anything else must match exactly (the goldens record float32 at full round-trip precision).
func coordMatches(got float32, want *float32) bool {
	if want == nil {
		return math.IsNaN(float64(got))
	}
	return got == *want
}

func coordString(v *float32) string {
	if v == nil {
		return "nil"
	}
	return strconv.FormatFloat(float64(*v), 'g', -1, 32)
}
