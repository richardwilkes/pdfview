package pdfview_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/richardwilkes/pdfview"
	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// TestParity walks the committed goldens (testfiles/goldens/<name>/truth.json plus PNGs, produced from
// testfiles/corpus by oracle/regen.sh) and compares everything the engine can already answer against the recorded
// MuPDF behavior. Each comparison is gated on the milestone that delivers the capability, using the same milestone
// constant as gates_test.go: before that milestone the comparison is silently skipped, from it on any divergence
// fails. The corpus/golden pairing (sha256) is verified unconditionally. Capability gates, matching the milestone
// exit criteria in plan.md:
//
//	M1  New succeeds; PageCount (for documents needing no authentication)
//	M2  RequiresAuthentication; Authenticate status bits; PageCount for encrypted documents
//	M3  TableOfContents at every recorded DPI
//	M4  RenderPage succeeds; image dimensions and stride; links
//	M7  search hit rectangles
//	M8  pixel comparison within thresholds
//
// At M8 the milestone gating is deleted along with gates_test.go and every comparison becomes unconditional.
func TestParity(t *testing.T) {
	goldens, err := testsupport.LoadGoldens(filepath.Join("testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	if len(goldens) == 0 {
		t.Fatal("no goldens present; run oracle/regen.sh and commit the output")
	}
	for _, golden := range goldens {
		t.Run(golden.Name, func(t *testing.T) {
			parityOne(t, golden)
		})
	}
}

// milestoneOrder mirrors the ordering used by gate() in gates_test.go; it is repeated here because that file is
// deleted at M8 while this one is then rewritten to compare unconditionally.
var milestoneOrder = map[string]int{"M0": 0, "M1": 1, "M2": 2, "M3": 3, "M4": 4, "M5": 5, "M6": 6, "M7": 7, "M8": 8}

// capabilityReached reports whether the current milestone (gates_test.go) includes the given capability.
func capabilityReached(required string) bool {
	return milestoneOrder[milestone] >= milestoneOrder[required]
}

func parityOne(t *testing.T, golden *testsupport.Golden) {
	truth := golden.Truth
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", truth.File))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != truth.SHA256 {
		t.Fatalf("sha256 of corpus file %s is %s but its golden records %s; regenerate with oracle/regen.sh",
			truth.File, got, truth.SHA256)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		if capabilityReached("M1") {
			t.Fatalf("New: %v", err)
		}
		t.Skipf("engine cannot open documents until M1 (New returned %q)", err)
	}
	defer doc.Release()
	parityAuth(t, truth, data, doc)
	if capabilityReached("M1") {
		if got := doc.PageCount(); got != truth.PageCount {
			t.Errorf("PageCount = %d, oracle says %d", got, truth.PageCount)
		}
	}
	parityTOC(t, truth, doc)
	parityRenders(t, golden, doc)
}

// parityAuth compares the authentication surface and leaves doc authenticated for the comparisons that follow.
// Below M2 it skips the rest of the subtest for documents that cannot be used without authentication.
func parityAuth(t *testing.T, truth *testsupport.Truth, data []byte, doc *pdfview.Document) {
	if !capabilityReached("M2") {
		if truth.RequiresAuth {
			t.Skipf("document requires authentication; encryption arrives at M2")
		}
		return
	}
	if got := doc.RequiresAuthentication(); got != truth.RequiresAuth {
		t.Errorf("RequiresAuthentication = %v, oracle says %v", got, truth.RequiresAuth)
	}
	// Every recorded attempt ran against a fresh document, so replay them the same way.
	for _, attempt := range truth.Auth {
		fresh, err := pdfview.New(data, 0)
		if err != nil {
			t.Fatalf("New for auth attempt: %v", err)
		}
		if got := int(fresh.Authenticate(attempt.Password)); got != attempt.Status {
			t.Errorf("Authenticate(%q) = %d, oracle says %d", attempt.Password, got, attempt.Status)
		}
		fresh.Release()
	}
	if truth.RequiresAuth {
		if doc.Authenticate(truth.AuthPassword) == 0 {
			t.Fatalf("Authenticate(%q) failed; the oracle authenticated with it", truth.AuthPassword)
		}
	}
}

func parityTOC(t *testing.T, truth *testsupport.Truth, doc *pdfview.Document) {
	if !capabilityReached("M3") {
		return
	}
	for _, dpi := range truth.DPIs {
		label := fmt.Sprintf("TableOfContents(%d)", dpi)
		compareTOCLevel(t, label, doc.TableOfContents(dpi), truth.TOC[strconv.Itoa(dpi)])
	}
}

func compareTOCLevel(t *testing.T, label string, got []*pdfview.TOCEntry, want []*testsupport.TOCEntry) {
	if len(got) != len(want) {
		t.Errorf("%s has %d entries, oracle says %d", label, len(got), len(want))
		return
	}
	for i, wantEntry := range want {
		gotEntry := got[i]
		entryLabel := fmt.Sprintf("%s[%d]", label, i)
		if gotEntry.Title != wantEntry.Title {
			t.Errorf("%s Title = %q, oracle says %q", entryLabel, gotEntry.Title, wantEntry.Title)
		}
		if gotEntry.PageNumber != wantEntry.Page {
			t.Errorf("%s PageNumber = %d, oracle says %d", entryLabel, gotEntry.PageNumber, wantEntry.Page)
		}
		if gotEntry.PageX != wantEntry.X || gotEntry.PageY != wantEntry.Y {
			t.Errorf("%s position = (%d, %d), oracle says (%d, %d)",
				entryLabel, gotEntry.PageX, gotEntry.PageY, wantEntry.X, wantEntry.Y)
		}
		compareTOCLevel(t, entryLabel, gotEntry.Children, wantEntry.Children)
	}
}

func parityRenders(t *testing.T, golden *testsupport.Golden, doc *pdfview.Document) {
	if !capabilityReached("M4") {
		return
	}
	truth := golden.Truth
	for _, page := range truth.Pages {
		for _, dpi := range truth.DPIs {
			render, ok := page.Renders[strconv.Itoa(dpi)]
			if !ok {
				t.Fatalf("golden records no render for page %d dpi %d", page.Page, dpi)
			}
			parityRender(t, golden, doc, page.Page, dpi, render)
		}
	}
}

func parityRender(t *testing.T, golden *testsupport.Golden, doc *pdfview.Document, pageNumber, dpi int, render *testsupport.Render) {
	label := fmt.Sprintf("page %d dpi %d", pageNumber, dpi)
	needles := golden.Truth.Needles
	firstNeedle := ""
	if len(needles) > 0 {
		firstNeedle = needles[0]
	}
	rendered, err := doc.RenderPage(pageNumber, dpi, pdfview.OverallMaxHits, firstNeedle)
	if err != nil {
		t.Errorf("%s: RenderPage: %v", label, err)
		return
	}
	if rendered.Image == nil {
		t.Errorf("%s: no image", label)
		return
	}
	if rendered.Image.Rect.Dx() != render.Width || rendered.Image.Rect.Dy() != render.Height ||
		rendered.Image.Stride != render.Stride {
		t.Errorf("%s: image %dx%d stride %d, oracle says %dx%d stride %d", label,
			rendered.Image.Rect.Dx(), rendered.Image.Rect.Dy(), rendered.Image.Stride,
			render.Width, render.Height, render.Stride)
	}
	compareLinks(t, label, rendered.Links, render.Links)
	if capabilityReached("M7") && len(needles) > 0 {
		compareHits(t, fmt.Sprintf("%s search %q", label, firstNeedle), rendered.SearchHits, render.Search[firstNeedle])
		for _, needle := range needles[1:] {
			again, searchErr := doc.RenderPage(pageNumber, dpi, pdfview.OverallMaxHits, needle)
			if searchErr != nil {
				t.Errorf("%s: RenderPage for search %q: %v", label, needle, searchErr)
				continue
			}
			compareHits(t, fmt.Sprintf("%s search %q", label, needle), again.SearchHits, render.Search[needle])
		}
	}
	if capabilityReached("M8") {
		want, loadErr := testsupport.LoadImage(filepath.Join(golden.Dir, render.PNG))
		if loadErr != nil {
			t.Errorf("%s: %v", label, loadErr)
			return
		}
		diff, diffErr := testsupport.ComparePixels(rendered.Image, want)
		if diffErr != nil {
			t.Errorf("%s: %v", label, diffErr)
			return
		}
		if !diff.WithinDefaultThresholds() {
			t.Errorf("%s: pixels diverge beyond thresholds: %s", label, diff)
		}
	}
}

func compareLinks(t *testing.T, label string, got []*pdfview.PageLink, want []*testsupport.Link) {
	if len(got) != len(want) {
		t.Errorf("%s has %d links, oracle says %d", label, len(got), len(want))
		return
	}
	for i, wantLink := range want {
		gotLink := got[i]
		linkLabel := fmt.Sprintf("%s link %d", label, i)
		if gotLink.URI != wantLink.URI {
			t.Errorf("%s URI = %q, oracle says %q", linkLabel, gotLink.URI, wantLink.URI)
		}
		if gotLink.PageNumber != wantLink.Page {
			t.Errorf("%s PageNumber = %d, oracle says %d", linkLabel, gotLink.PageNumber, wantLink.Page)
		}
		if wantBounds := image.Rect(wantLink.Bounds[0], wantLink.Bounds[1], wantLink.Bounds[2], wantLink.Bounds[3]); gotLink.Bounds != wantBounds {
			t.Errorf("%s Bounds = %v, oracle says %v", linkLabel, gotLink.Bounds, wantBounds)
		}
		if wantDest := image.Pt(wantLink.DestX, wantLink.DestY); gotLink.DestPoint != wantDest {
			t.Errorf("%s DestPoint = %v, oracle says %v", linkLabel, gotLink.DestPoint, wantDest)
		}
	}
}

func compareHits(t *testing.T, label string, got []image.Rectangle, want [][4]int) {
	if len(got) != len(want) {
		t.Errorf("%s has %d hits, oracle says %d", label, len(got), len(want))
		return
	}
	for i, wantHit := range want {
		if wantRect := image.Rect(wantHit[0], wantHit[1], wantHit[2], wantHit[3]); got[i] != wantRect {
			t.Errorf("%s hit %d = %v, oracle says %v", label, i, got[i], wantRect)
		}
	}
}
