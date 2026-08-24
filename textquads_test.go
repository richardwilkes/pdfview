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
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/richardwilkes/pdfview/internal/content"
	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/stext"
	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// TestTextQuadParity is the pre-scale half of the search-parity bar: it runs each corpus page through the interpreter
// against the structured-text device at scale 1, searches for every recorded needle, and requires the hit quads to
// match MuPDF's raw page-space quads positionally (same count, same order) with every corner within quadTolerance
// points. TestParity is the post-scale half, comparing the public API's scaled hit rectangles at every recorded DPI.
func TestTextQuadParity(t *testing.T) {
	goldens, err := testsupport.LoadGoldens(filepath.Join("testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	for _, golden := range goldens {
		if len(golden.Truth.Needles) == 0 {
			continue
		}
		t.Run(golden.Name, func(t *testing.T) {
			maxErr, meanErr, quads := diffGoldenQuads(t, golden)
			t.Logf("%s: %d quads, corner error max %.4f / mean %.4f pt", golden.Name, quads, maxErr, meanErr)
		})
	}
}

// quadTolerance is the pre-scale corner tolerance in page-space points; the measured worst across the corpus is glaive
// at 0.0022 pt.
const quadTolerance = 0.01

// diffGoldenQuads compares one golden's recorded search quads against the structured-text device's search results,
// reporting the largest and mean corner errors seen.
func diffGoldenQuads(t *testing.T, golden *testsupport.Golden) (maxErr, meanErr float64, totalQuads int) {
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", golden.Truth.File))
	if err != nil {
		t.Fatal(err)
	}
	document, err := doc.Open(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if golden.Truth.RequiresAuth || golden.Truth.AuthPassword != "" {
		if document.Authenticate(golden.Truth.AuthPassword) == 0 {
			t.Fatalf("Authenticate(%q) failed", golden.Truth.AuthPassword)
		}
	}
	var errSum float64
	var corners int
	for _, page := range golden.Truth.Pages {
		if len(page.SearchRaw) == 0 {
			continue
		}
		dev := extractText(t, document, page.Page)
		for _, needle := range sortedKeys(page.SearchRaw) {
			want := page.SearchRaw[needle]
			found := dev.Search(needle, math.MaxInt)
			got := make([][8]float32, len(found))
			for i, q := range found {
				got[i] = [8]float32{q.UL.X, q.UL.Y, q.UR.X, q.UR.Y, q.LL.X, q.LL.Y, q.LR.X, q.LR.Y}
			}
			totalQuads += len(want)
			if len(got) != len(want) {
				t.Errorf("page %d needle %q: got %d quads, oracle has %d", page.Page, needle, len(got), len(want))
				continue
			}
			for i := range want {
				worst := 0.0
				for c := range 8 {
					e := math.Abs(float64(got[i][c]) - float64(want[i][c]))
					errSum += e
					corners++
					if e > maxErr {
						maxErr = e
					}
					if e > worst {
						worst = e
					}
				}
				if worst > quadTolerance {
					t.Errorf("page %d needle %q quad %d:\n got %v\nwant %v", page.Page, needle, i, got[i], want[i])
				}
			}
		}
	}
	if corners > 0 {
		meanErr = errSum / float64(corners)
	}
	return maxErr, meanErr, totalQuads
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// extractText interprets one page's content at scale 1 (page space) against a fresh structured-text device, exactly as
// the engine's search seam does.
func extractText(t *testing.T, document *doc.Document, pageNumber int) *stext.Device {
	ctm, err := document.PageCTM(pageNumber, 1)
	if err != nil {
		t.Fatalf("PageCTM(%d): %v", pageNumber, err)
	}
	dev := stext.New()
	if data := document.PageContents(pageNumber); len(data) > 0 {
		content.Run(document.COS(), document.PageResources(pageNumber), data, ctm, dev, nil)
	}
	// Appearance text is part of MuPDF's structured text, so this capture runs the appearances after the page content
	// as the engine's search seam does.
	annots := content.NewAnnotRun(nil)
	for _, a := range document.Annotations(pageNumber) {
		annots.Annot(document.COS(), document.PageResources(pageNumber), a.Raw, a.Stream, a.Transform.Mul(ctm), dev)
	}
	return dev
}
