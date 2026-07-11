// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package testsupport loads the committed golden files (testfiles/goldens/<name>/truth.json plus rendered PNGs,
// produced from testfiles/corpus by the oracle module's regen.sh) and provides the comparison helpers the parity
// tests are built on. It is pure Go: the goldens are committed, so no cgo, MuPDF, or network access is ever needed
// to run the tests.
package testsupport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
)

// The types below mirror the truth.json schema written by the oracle module (oracle/schema.go). The two modules
// deliberately share no code — the oracle needs cgo and must never become a dependency of the library — so the
// schema is maintained in both places; keep them in sync. LoadTruth rejects unknown fields, so drift between the
// two surfaces as a test failure rather than silently ignored data.
//
// Coordinate spaces: all "raw" values are unscaled page-space floats exactly as MuPDF reports them — top-left
// origin, y-down, in PDF points. All other geometry is in rendered-image pixel space for the DPI it is keyed
// under, produced by the public API of the cgo binding (the behavioral contract this package's engine must match).
// Floats that MuPDF reports as non-finite (a destination with no explicit coordinate, such as /Fit) are null in
// the JSON and nil here.

// Truth is the top-level truth.json document, one per corpus file. (Field order here is dictated by
// fieldalignment; the JSON field order truth.json is written with lives in oracle/schema.go.)
type Truth struct {
	// TOC holds TableOfContents(dpi) from the public API, keyed by DPI rendered with strconv.Itoa.
	TOC map[string][]*TOCEntry `json:"toc,omitempty"`
	// File is the corpus file's base name within testfiles/corpus.
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	// MuPDF is the FZ_VERSION of the MuPDF build that produced this golden.
	MuPDF string `json:"mupdf"`
	// AuthPassword is the password the dump authenticated with before extracting the rest of this file's data.
	// It is empty when RequiresAuth is false.
	AuthPassword string `json:"authPassword,omitempty"`
	// Auth records Authenticate(password) results, each attempted on its own freshly opened document.
	Auth []AuthAttempt `json:"auth"`
	DPIs []int         `json:"dpis"`
	// Needles lists the search strings dumped for every page.
	Needles []string `json:"needles,omitempty"`
	// TOCRaw is the document outline as MuPDF reports it: raw titles and URIs, and unscaled page-space
	// destination coordinates.
	TOCRaw    []*TOCRawEntry `json:"tocRaw,omitempty"`
	Pages     []*Page        `json:"pages"`
	PageCount int            `json:"pageCount"`
	// RequiresAuth is RequiresAuthentication() on a freshly opened document.
	RequiresAuth bool `json:"requiresAuth"`
}

// AuthAttempt is one Authenticate call on a fresh document.
type AuthAttempt struct {
	Password string `json:"password"`
	// Status is the AuthenticationStatus byte: 0 = failed, bit 0 = no authentication was required, bit 1 = user
	// password authenticated, bit 2 = owner password authenticated.
	Status int `json:"status"`
}

// TOCRawEntry is one raw outline node.
type TOCRawEntry struct {
	X        *float32       `json:"x"` // nil when MuPDF reports a non-finite coordinate
	Y        *float32       `json:"y"`
	Title    string         `json:"title"`
	URI      string         `json:"uri,omitempty"`
	Children []*TOCRawEntry `json:"children,omitempty"`
	Page     int            `json:"page"`
}

// TOCEntry is one public-API TOCEntry (sanitized title, scaled ints).
type TOCEntry struct {
	Title    string      `json:"title"`
	Children []*TOCEntry `json:"children,omitempty"`
	Page     int         `json:"page"`
	X        int         `json:"x"`
	Y        int         `json:"y"`
}

// Page holds everything dumped for one 0-based page.
type Page struct {
	// SearchRaw maps each needle to the raw hit quads in MuPDF emission order. Each quad is
	// (ulx, uly, urx, ury, llx, lly, lrx, lry) in page space. A match that spans lines yields one quad per line.
	SearchRaw map[string][][8]float32 `json:"searchRaw,omitempty"`
	// Renders holds the public-API render results, keyed by DPI rendered with strconv.Itoa.
	Renders map[string]*Render `json:"renders"`
	// LinksRaw records every link MuPDF reports on the page, unfiltered — including entries the public API
	// would drop (unresolvable internal links).
	LinksRaw []*RawLink `json:"linksRaw"`
	// Bounds is the raw page bounding box (x0, y0, x1, y1) in page space.
	Bounds [4]float32 `json:"bounds"`
	Page   int        `json:"page"`
}

// RawLink is one link as MuPDF reports it, before the public API's filtering and scaling.
type RawLink struct {
	DestX *float32   `json:"destX,omitempty"` // nil when non-finite or external
	DestY *float32   `json:"destY,omitempty"`
	URI   string     `json:"uri,omitempty"`
	Rect  [4]float32 `json:"rect"`
	// Page is the resolved 0-based target page for internal links; -1 for external or unresolvable links.
	Page     int  `json:"page"`
	External bool `json:"external"`
}

// Render is one public-API RenderPage result at one DPI.
type Render struct {
	// Search maps each needle to the public-API hit rectangles (x0, y0, x1, y1).
	Search map[string][][4]int `json:"search,omitempty"`
	// PNG is the file name (within the golden directory) of the losslessly encoded rendered page.
	PNG string `json:"png"`
	// Links is the public-API link list: URI empty for internal links, Page -1 for external links, bounds and
	// dest in rendered-image pixel space.
	Links  []*Link `json:"links"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Stride int     `json:"stride"`
}

// Link is one public-API PageLink.
type Link struct {
	URI    string `json:"uri,omitempty"`
	Page   int    `json:"page"`
	Bounds [4]int `json:"bounds"`
	DestX  int    `json:"destX"`
	DestY  int    `json:"destY"`
}

// Golden is one loaded golden directory.
type Golden struct {
	Truth *Truth
	// Name is the golden directory's base name (the corpus file name without its extension).
	Name string
	// Dir is the path to the golden directory; the PNGs named by Truth live in it.
	Dir string
}

// LoadTruth reads and decodes one truth.json. Unknown fields are rejected so schema drift between the oracle
// module and this package cannot pass unnoticed.
func LoadTruth(path string) (*Truth, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // Read-only file
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var truth Truth
	if err = decoder.Decode(&truth); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &truth, nil
}

// LoadGoldens loads every golden under dir (each subdirectory must contain a truth.json), sorted by name.
func LoadGoldens(dir string) ([]*Golden, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	goldens := make([]*Golden, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		goldenDir := filepath.Join(dir, entry.Name())
		truth, truthErr := LoadTruth(filepath.Join(goldenDir, "truth.json"))
		if truthErr != nil {
			return nil, truthErr
		}
		goldens = append(goldens, &Golden{Truth: truth, Name: entry.Name(), Dir: goldenDir})
	}
	return goldens, nil
}

// LoadImage decodes a golden PNG into straight-alpha NRGBA. The oracle encodes image.NRGBA data, but the PNG
// format stores fully opaque images without an alpha channel, in which case the decoder hands back a different
// image type; the conversion to NRGBA is exact for such opaque pixels.
func LoadImage(path string) (*image.NRGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // Read-only file
	decoded, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if nrgba, ok := decoded.(*image.NRGBA); ok {
		return nrgba, nil
	}
	bounds := decoded.Bounds()
	nrgba := image.NewNRGBA(bounds)
	draw.Draw(nrgba, bounds, decoded, bounds.Min, draw.Src)
	return nrgba, nil
}

// PixelDiff summarizes a per-pixel comparison of two same-sized straight-alpha images. A pixel's delta is the
// largest absolute difference across its R, G, B, and A channels.
type PixelDiff struct {
	// MeanDelta is the mean of the per-pixel deltas.
	MeanDelta float64
	// Pixels is the total number of pixels compared.
	Pixels int
	// Over8 counts pixels whose delta exceeds 8 (including those counted by Over24).
	Over8 int
	// Over24 counts pixels whose delta exceeds 24.
	Over24 int
	// MaxDelta is the largest per-pixel delta seen.
	MaxDelta int
}

// ComparePixels compares two images of identical dimensions and reports the differences. Strides and rectangle
// origins may differ; pixels are compared in corresponding positions.
func ComparePixels(got, want *image.NRGBA) (*PixelDiff, error) {
	gotBounds := got.Bounds()
	wantBounds := want.Bounds()
	width := gotBounds.Dx()
	height := gotBounds.Dy()
	if width != wantBounds.Dx() || height != wantBounds.Dy() {
		return nil, fmt.Errorf("image dimensions differ: %dx%d vs %dx%d", width, height, wantBounds.Dx(), wantBounds.Dy())
	}
	diff := &PixelDiff{Pixels: width * height}
	var total uint64
	for y := range height {
		gotRow := got.Pix[got.PixOffset(gotBounds.Min.X, gotBounds.Min.Y+y):]
		wantRow := want.Pix[want.PixOffset(wantBounds.Min.X, wantBounds.Min.Y+y):]
		for x := range width {
			delta := 0
			for channel := range 4 {
				d := int(gotRow[x*4+channel]) - int(wantRow[x*4+channel])
				if d < 0 {
					d = -d
				}
				if d > delta {
					delta = d
				}
			}
			total += uint64(delta)
			if delta > diff.MaxDelta {
				diff.MaxDelta = delta
			}
			if delta > 8 {
				diff.Over8++
				if delta > 24 {
					diff.Over24++
				}
			}
		}
	}
	if diff.Pixels > 0 {
		diff.MeanDelta = float64(total) / float64(diff.Pixels)
	}
	return diff, nil
}

// Thresholds is one golden's pixel gate. Every file is compared against the default gate from plan.md unless
// its golden directory carries a thresholds.json override, the sanctioned mechanism for files whose measured,
// UNDERSTOOD divergence exceeds the default (substitute-font letterform deltas, AA-model edge redistribution
// on small text — see the M6 decision log). Overrides are a ratchet: once set they may only ever tighten as
// rendering fidelity improves, and each must carry its justification.
type Thresholds struct {
	// Justification documents why this golden's gate differs from the default. Required in overrides.
	Justification string `json:"justification"`
	// MaxOver24Pct and MaxOver8Pct bound the percentage (0-100) of pixels whose max channel delta exceeds
	// 24 and 8 respectively; MaxMeanDelta bounds the mean delta.
	MaxOver24Pct float64 `json:"maxOver24Pct"`
	MaxOver8Pct  float64 `json:"maxOver8Pct"`
	MaxMeanDelta float64 `json:"maxMeanDelta"`
}

// DefaultThresholds is the initial perceptual gate from plan.md: at most 2% of pixels with a delta over 24,
// at most 10% with a delta over 8, and a mean delta of at most 2.
func DefaultThresholds() Thresholds {
	return Thresholds{MaxOver24Pct: 2, MaxOver8Pct: 10, MaxMeanDelta: 2}
}

// LoadThresholds returns the gate for a golden directory: the default unless <dir>/thresholds.json overrides
// it. Unknown fields and malformed overrides are errors — a broken override must never silently widen (or
// narrow) a gate.
func LoadThresholds(dir string) (Thresholds, error) {
	data, err := os.ReadFile(filepath.Join(dir, "thresholds.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultThresholds(), nil
		}
		return Thresholds{}, err
	}
	var th Thresholds
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&th); err != nil {
		return Thresholds{}, fmt.Errorf("%s: %w", dir, err)
	}
	if th.Justification == "" || th.MaxOver24Pct <= 0 || th.MaxOver8Pct <= 0 || th.MaxMeanDelta <= 0 {
		return Thresholds{}, fmt.Errorf("%s: thresholds.json must carry a justification and positive bounds", dir)
	}
	return th, nil
}

// Within reports whether the difference passes th.
func (p *PixelDiff) Within(th Thresholds) bool {
	if p.Pixels == 0 {
		return true
	}
	return 100*float64(p.Over24) <= th.MaxOver24Pct*float64(p.Pixels) &&
		100*float64(p.Over8) <= th.MaxOver8Pct*float64(p.Pixels) &&
		p.MeanDelta <= th.MaxMeanDelta
}

// WithinDefaultThresholds reports whether the difference passes the default gate.
func (p *PixelDiff) WithinDefaultThresholds() bool {
	return p.Within(DefaultThresholds())
}

// String summarizes the diff for test failure messages.
func (p *PixelDiff) String() string {
	if p.Pixels == 0 {
		return "no pixels"
	}
	return fmt.Sprintf("%d pixels: %.2f%% over Δ24, %.2f%% over Δ8, mean Δ%.3f, max Δ%d",
		p.Pixels, 100*float64(p.Over24)/float64(p.Pixels), 100*float64(p.Over8)/float64(p.Pixels),
		p.MeanDelta, p.MaxDelta)
}
