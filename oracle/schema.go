// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package main

// This file defines the truth.json schema. internal/testsupport in the root module mirrors these types read-side
// (the two modules deliberately share no code, so the library never depends on this module); keep the two in sync.
//
// Coordinate spaces: all "raw" values are unscaled page-space floats exactly as MuPDF reports them — top-left
// origin, y-down, in PDF points, after the page's /Rotate and MediaBox/CropBox handling have been folded in by
// MuPDF. All other geometry is in rendered-image pixel space for the DPI it is keyed under, produced by the public
// API of github.com/richardwilkes/pdf (the behavioral contract pdfview must match). Floats that MuPDF reports as
// non-finite (a destination with no explicit coordinate, such as /Fit) are encoded as JSON null.

// Truth is the top-level truth.json document, one per corpus file.
type Truth struct {
	// File is the corpus file's base name (the corpus lives in testfiles/corpus, goldens in
	// testfiles/goldens/<name> where <name> is File without its extension).
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	// MuPDF is the FZ_VERSION of the MuPDF build that produced this golden.
	MuPDF     string `json:"mupdf"`
	PageCount int    `json:"pageCount"`
	// RequiresAuth is RequiresAuthentication() on a freshly opened document.
	RequiresAuth bool `json:"requiresAuth"`
	// Auth records Authenticate(password) results, each attempted on its own freshly opened document. The list
	// always includes the empty password and a deliberately invalid password, plus every password passed to the
	// dump command.
	Auth []AuthAttempt `json:"auth"`
	// AuthPassword is the password the dump authenticated with before extracting the rest of this file's data. It
	// is present only when RequiresAuth is true; documents with an empty user password need no authentication call.
	AuthPassword string `json:"authPassword,omitempty"`
	DPIs         []int  `json:"dpis"`
	// Needles lists the search strings dumped for every page, in searchRaw (raw quads) and renders[dpi].search
	// (scaled hit rectangles).
	Needles []string `json:"needles,omitempty"`
	// TOCRaw is the document outline as MuPDF reports it: raw titles and URIs, and unscaled page-space
	// destination coordinates.
	TOCRaw []*TOCRawEntry `json:"tocRaw,omitempty"`
	// TOC holds TableOfContents(dpi) from the public API, keyed by DPI: sanitized titles and scaled integer
	// coordinates.
	TOC   map[string][]*TOCEntry `json:"toc,omitempty"`
	Pages []*Page                `json:"pages"`
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
	Title    string         `json:"title"`
	URI      string         `json:"uri,omitempty"`
	Page     int            `json:"page"`
	X        *float32       `json:"x"` // null when MuPDF reports a non-finite coordinate
	Y        *float32       `json:"y"`
	Children []*TOCRawEntry `json:"children,omitempty"`
}

// TOCEntry is one public-API TOCEntry (sanitized title, scaled ints).
type TOCEntry struct {
	Title    string      `json:"title"`
	Page     int         `json:"page"`
	X        int         `json:"x"`
	Y        int         `json:"y"`
	Children []*TOCEntry `json:"children,omitempty"`
}

// Page holds everything dumped for one 0-based page.
type Page struct {
	Page int `json:"page"`
	// Bounds is the raw page bounding box (x0, y0, x1, y1) in page space.
	Bounds [4]float32 `json:"bounds"`
	// LinksRaw records every link MuPDF reports on the page, unfiltered — including entries the public API would
	// drop (unresolvable internal links).
	LinksRaw []*RawLink `json:"linksRaw"`
	// SearchRaw maps each needle to the raw hit quads in MuPDF emission order. Each quad is
	// (ulx, uly, urx, ury, llx, lly, lrx, lry) in page space. A match that spans lines yields one quad per line.
	SearchRaw map[string][][8]float32 `json:"searchRaw,omitempty"`
	// Renders holds the public-API render results, keyed by DPI.
	Renders map[string]*Render `json:"renders"`
}

// RawLink is one link as MuPDF reports it, before the public API's filtering and scaling.
type RawLink struct {
	// URI is the raw link URI (for internal links this is MuPDF's synthesized destination URI, which the public
	// API discards).
	URI      string     `json:"uri,omitempty"`
	External bool       `json:"external"`
	Rect     [4]float32 `json:"rect"`
	// Page is the resolved 0-based target page for internal links; -1 for external or unresolvable links.
	Page  int      `json:"page"`
	DestX *float32 `json:"destX,omitempty"` // null/absent when non-finite or external
	DestY *float32 `json:"destY,omitempty"`
}

// Render is one public-API RenderPage result at one DPI.
type Render struct {
	// PNG is the file name (within the golden directory) of the losslessly encoded image.NRGBA output.
	PNG    string `json:"png"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Stride int    `json:"stride"`
	// Links is the public-API link list: URI empty for internal links, Page -1 for external links, bounds and
	// dest in rendered-image pixel space.
	Links []*Link `json:"links"`
	// Search maps each needle to the public-API hit rectangles (x0, y0, x1, y1).
	Search map[string][][4]int `json:"search,omitempty"`
}

// Link is one public-API PageLink.
type Link struct {
	URI    string `json:"uri,omitempty"`
	Page   int    `json:"page"`
	Bounds [4]int `json:"bounds"`
	DestX  int    `json:"destX"`
	DestY  int    `json:"destY"`
}
