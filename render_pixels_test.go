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

// TestTextCorpusPixels is milestone M6's pixel-scope check, following the M4/M5 pattern, and from M6 exit on
// it enforces EVERY text corpus file: files inside the default gate directly, and the files whose divergence
// is measured and understood — substitute-font letterform deltas (Liberation vs the oracle's Nimbus) and
// AA-model edge redistribution on small embedded-font text — through their goldens' thresholds.json ratchets
// (see the 2026-07-11 M6-exit decision-log entry for the measured numbers behind each). The six encrypted
// files are text-std14 variants and must render identically to it once authenticated.
func TestTextCorpusPixels(t *testing.T) {
	for _, name := range []string{
		"text-std14", "hit-quad-split",
		"encrypted-r2-rc4", "encrypted-r3-rc4", "encrypted-r4-rc4", "encrypted-r4-aes",
		"encrypted-r6-aes", "encrypted-r6-empty-user",
		"text-type1", "text-type0-cid2", "text-type0-cid0", "text-type3", "text-trmodes",
		"glaive", "irs-f1040", "irs-fw9", "std14-styles", "subst-metrics",
		"damaged-bad-offsets", "damaged-no-trailer", "damaged-startxref-zero",
	} {
		comparePixelsToGolden(t, name, name, true)
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

// TestShadingCorpusPixels is milestone M8's first pixel-scope check, following the M4/M5/M6 pattern: the
// shading/pattern corpus — gradients (axial/radial), function-based shadings, mesh types 4-7, and
// shading/tiling patterns — is enforced against the goldens at every recorded DPI.
func TestShadingCorpusPixels(t *testing.T) {
	for _, name := range []string{
		"shading-axial", "shading-radial", "shading-function", "shading-mesh", "pattern-tiling",
	} {
		comparePixelsToGolden(t, name, name, true)
	}
}

// TestTransparencyCorpusPixels is milestone M8's second pixel-scope check, following the established pattern:
// the transparency corpus — all 16 blend modes over mixed backdrops, transparency groups (constant alphas,
// alpha reset, isolation, knockout), and soft masks (luminosity incl. /BC and a sampled /TR, alpha subtype,
// /SMask /None reset, gs-time anchoring) — is enforced against the goldens at every recorded DPI.
func TestTransparencyCorpusPixels(t *testing.T) {
	for _, name := range []string{
		"transparency-blend", "transparency-group", "transparency-smask-lum", "transparency-smask-alpha",
	} {
		comparePixelsToGolden(t, name, name, true)
	}
}

// TestAnnotationCorpusPixels is milestone M8's third pixel-scope check, following the established pattern: the
// annotation corpus — /AP /N appearance streams with widget /FT gating, /F flag suppression (Invisible/Hidden/
// NoView), /AS state selection, the Link/Popup never-render gates, /CA-ignored, ISO 32000-2 12.5.5 placement
// (Matrix rotation, BBox clipping, nonzero-origin BBox, reversed /Rect, degenerate skips), z-order after page
// content, and page-resource inheritance for /Resources-less appearances — is enforced against the goldens at
// every recorded DPI. All semantics were pinned by oracle probes first; see the M8 /AP decision-log entry.
func TestAnnotationCorpusPixels(t *testing.T) {
	comparePixelsToGolden(t, "annotations", "annotations", true)
}

// comparePixelsToGolden renders corpus file name at every DPI recorded in goldenName's truth.json and compares
// pixels against the golden's gate (its thresholds.json when present, else the default). goldenName equals
// name except for the stub-codec cross-check described on TestImageCorpusPixels.
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
	thresholds, err := testsupport.LoadThresholds(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
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
			case enforce && !diff.Within(thresholds):
				t.Errorf("%s: pixels diverge beyond thresholds: %s", label, diff)
			default:
				t.Logf("%s: %s", label, diff)
			}
		}
	}
}
