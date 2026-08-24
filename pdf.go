// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package pdfview renders PDF pages to images and extracts text-search hits, selectable page text, links, the table of
// contents, and page labels. It also handles password-protected documents.
//
// The package is a pure-Go PDF engine (no cgo; CGO_ENABLED=0 builds) with rasterization delegated to
// github.com/richardwilkes/canvas. New parses documents, repairing damaged ones, and decrypts password-protected ones
// (standard security handler R2-R6). RenderPage and RenderPageForSize rasterize a page's content — paths, clips,
// colors, form XObjects, images, fonts and text, shadings, patterns, transparency groups, soft masks, blend modes, and
// annotation appearance streams. Text search returns MuPDF-compatible hit rectangles. TextPage exposes the same
// extracted text for hit-testing, selection, and copying, with highlight rectangles in the rendered image's pixel
// space. PageLabel, PageLabels, and PagesWithLabel translate between a page's position in the file and its display
// label. DrawPage draws a page onto a caller-owned canvas. The engine's behavior is pinned against the MuPDF-based
// github.com/richardwilkes/pdf binding it succeeds: coordinates exactly, pixels within committed perceptual thresholds.
// See README.md for the architecture.
//
// # Platform requirements
//
// 64-bit platforms only. The engine's size, offset, and work-budget arithmetic assumes a 64-bit int, and the documented
// caps sit inside it: a 2^26 pixel image decodes through row strides and sample bit positions that reach 2^35, and a
// predictor row length reaches 2^34. A 32-bit build is rejected at compile time.
package pdfview

import (
	"bytes"
	"errors"
	"image"
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/richardwilkes/pdfview/internal/content"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/render"
	"github.com/richardwilkes/pdfview/internal/stext"
	"github.com/richardwilkes/pdfview/internal/store"
)

// 64-bit platforms only (see the package documentation): this is 0 where int is 64 bits and an unrepresentable -1
// where it is 32, so a 32-bit build fails with "constant -1 overflows uint".
const _ uint = ^uint(0)>>63 - 1

// Possible error values
var (
	ErrNotPDFData = errors.New("only PDF documents are supported")
	// ErrUnableToCreatePDFContext is never returned. It is retained for source compatibility with the MuPDF-based
	// github.com/richardwilkes/pdf binding, where it reported a failure to create the library's fz_context; an open
	// failure here is always ErrNotPDFData or ErrUnableToOpenPDF.
	ErrUnableToCreatePDFContext = errors.New("unable to create PDF context")
	ErrInternal                 = errors.New("internal error")
	ErrUnableToOpenPDF          = errors.New("unable to open PDF")
	ErrInvalidPageNumber        = errors.New("invalid page number")
	ErrUnableToLoadPage         = errors.New("unable to load page")
	ErrUnableToCreateImage      = errors.New("unable to create image")
	ErrImageTooLarge            = errors.New("rendered image would be too large")
	ErrInvalidPageSize          = errors.New("invalid page size")
	ErrDocumentReleased         = errors.New("document has been released")
	ErrInvalidMatrix            = errors.New("invalid matrix")
)

// These variables are global and not safe to modify while other calls into this package are in progress; set them at
// startup. The caps guard against untrusted input that would otherwise exhaust memory.
var (
	// OverallMaxHits caps the number of search hits returned, whatever maxHits a call asks for.
	OverallMaxHits = 1000
	// OverallMaxLinks caps the number of links returned.
	OverallMaxLinks = 1000
	// OverallMaxTOCEntries caps the number of TOC entries returned.
	OverallMaxTOCEntries = 1000
	// OverallMaxTextChars caps the number of characters one TextPage records; the rest of the page's text is dropped.
	// A page's glyph count is already bounded by the interpreter's work budget, but each extracted record costs about
	// 60 bytes and a TextPage is retained by the caller for as long as the text is on screen. The default (1,048,576)
	// is far above the densest real page, which runs to tens of thousands. Zero or a negative value restores the
	// engine's own default rather than disabling extraction.
	OverallMaxTextChars = 1 << 20
	// OverallMaxPixels caps the pixel count (width × height) of a rendered page image; larger requests are rejected
	// with ErrImageTooLarge. The default (268,435,456) is the largest image the raster surface will allocate: 1 GiB at
	// 4 bytes per pixel. Raising it past that does not enlarge the renderable image; the surface allocation then fails
	// with ErrUnableToCreateImage instead.
	OverallMaxPixels = render.MaxSurfacePixels
)

// AuthenticationStatus holds the result of an authentication attempt. A non-zero value indicates success and the masks
// can be used to determine further details.
type AuthenticationStatus byte

// Masks that can be used to examine AuthenticationStatus for additional details.
const (
	NoAuthenticationRequiredMask AuthenticationStatus = 1 << iota
	UserAuthenticatedMask
	OwnerAuthenticatedMask
)

type document struct {
	eng  *engineDocument
	lock sync.Mutex
}

// Document is an open PDF document. Page numbers in the API are zero-based. Methods are safe to call from multiple
// goroutines; calls into the engine are serialized.
type Document struct {
	// Held by pointer so every copy of the wrapper shares one mutex and Release drops the engine state for all of them.
	*document
}

// TOCEntry holds a single entry in the table of contents.
type TOCEntry struct {
	Title      string
	Children   []*TOCEntry
	PageNumber int
	PageX      int
	PageY      int
}

// PageLink holds a single link on a page. If PageNumber is >= 0, then this is an internal link and the URI will be
// empty.
type PageLink struct {
	URI        string
	PageNumber int
	// Bounds is the clickable hot-zone of the link on the page it appears on, in rendered-image pixel space.
	Bounds image.Rectangle
	// DestPoint is the point on the destination page (PageNumber) that an internal link targets, in the pixel space of
	// that page's rendered image, which under RenderPageForSize differs from this page's whenever the two pages differ
	// in size. It is (0,0) for external links and for internal links with no explicit coordinate (such as a /Fit
	// destination).
	DestPoint image.Point
}

// RenderedPage holds the rendered page.
type RenderedPage struct {
	// Image is the rendered page. Most PDF pages paint no background, so areas with no content are transparent rather
	// than white; callers that want an opaque page should composite it onto a background color.
	Image      *image.NRGBA
	SearchHits []image.Rectangle
	Links      []*PageLink
}

// New returns new PDF document from the provided raw bytes. Pass in 0 for maxCacheSize for no limit.
func New(buffer []byte, maxCacheSize uint64) (*Document, error) {
	// Acrobat and MuPDF tolerate leading garbage before the header.
	if !bytes.Contains(buffer[:min(1024, len(buffer))], []byte("%PDF")) {
		return nil, ErrNotPDFData
	}
	eng, err := openEngine(buffer, maxCacheSize)
	if err != nil {
		return nil, err
	}
	return &Document{document: &document{eng: eng}}, nil
}

// released reports whether the underlying document has been released. The caller must hold d.lock.
func (d *document) released() bool {
	return d.eng == nil
}

// usable reports whether the wrapper carries state a method can act on. The zero Document and a nil *Document have no
// embedded pointer, so locking the mutex behind it would dereference nil; every entry point checks this first and then
// answers as it does for a released document.
func (d *Document) usable() bool {
	return d != nil && d.document != nil
}

// RequiresAuthentication returns true if a password is required. Returns false if the document has been released.
func (d *Document) RequiresAuthentication() bool {
	if !d.usable() {
		return false
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return false
	}
	return d.eng.needsPassword()
}

// Authenticate with either the user or owner password. Returns a zero status if the document has been released.
func (d *Document) Authenticate(password string) AuthenticationStatus {
	if !d.usable() {
		return 0
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return 0
	}
	return d.eng.authenticate(password)
}

// TableOfContents returns the table of contents for this document, if any.
func (d *Document) TableOfContents(dpi int) []*TOCEntry {
	if !d.usable() {
		return nil
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return nil
	}
	entries, _ := buildTOCEntries(d.eng.outline(), float32(dpiToScale(dpi)), OverallMaxTOCEntries)
	return entries
}

func buildTOCEntries(outline *outlineNode, scale float32, maxAllowed int) (entries []*TOCEntry, remaining int) {
	if maxAllowed < 1 {
		return nil, 0
	}
	for outline != nil {
		entry := &TOCEntry{
			Title:      sanitizeString(outline.title),
			PageNumber: outline.page,
			PageX:      scaledFloor(float64(outline.x), float64(scale)),
			PageY:      scaledFloor(float64(outline.y), float64(scale)),
		}
		entries = append(entries, entry)
		maxAllowed--
		if maxAllowed <= 0 {
			break
		}
		if outline.down != nil {
			if entry.Children, maxAllowed = buildTOCEntries(outline.down, scale, maxAllowed); maxAllowed <= 0 {
				break
			}
		}
		outline = outline.next
	}
	return entries, max(maxAllowed, 0)
}

func sanitizeString(in string) string {
	sanitized := make([]rune, 0, len(in))
	for _, ch := range in {
		// U+FFFD stands in for bytes that did not decode as UTF-8, such as the unmappable dot-leader glyphs some PDFs
		// put in outline titles. It is printable and non-control, so it must be dropped explicitly.
		if ch != unicode.ReplacementChar && !unicode.IsControl(ch) && unicode.IsPrint(ch) {
			sanitized = append(sanitized, ch)
		}
	}
	return strings.TrimSpace(string(sanitized))
}

// PageCount returns total number of pages in the document.
func (d *Document) PageCount() int {
	if !d.usable() {
		return 0
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return 0
	}
	if count := d.eng.pageCount(); count > 0 {
		return count
	}
	return 0
}

// PageSize returns the given 0-based page's width and height in PDF points (1/72 inch): the extent of its effective
// box (the inherited /MediaBox intersected with any /CropBox) after /Rotate is applied, so 90 and 270 degree rotations
// swap the axes. This is the extent RenderPage rasterizes. It returns ErrDocumentReleased if the document has been
// released, ErrInvalidPageNumber for an out-of-range page, and ErrUnableToLoadPage when the page cannot be loaded.
func (d *Document) PageSize(pageNumber int) (width, height float32, err error) {
	var pg *page
	if pg, err = d.pageSize(pageNumber); err != nil {
		return 0, 0, err
	}
	return pg.width, pg.height, nil
}

// PageRenderSize returns the pixel dimensions of the image RenderPage produces for the given 0-based page at the given
// dpi, without rendering it. It applies the same scale and rounding RenderPage does, so the result matches a
// subsequent RenderPage call at the same dpi exactly. It returns ErrDocumentReleased if the document has been
// released, ErrInvalidPageNumber for an out-of-range page, and ErrUnableToLoadPage when the page cannot be loaded.
func (d *Document) PageRenderSize(pageNumber, dpi int) (width, height int, err error) {
	var pg *page
	if pg, err = d.pageSize(pageNumber); err != nil {
		return 0, 0, err
	}
	width, height = renderSpec{scale: dpiToScale(dpi)}.extents(pg)
	return width, height, nil
}

func (d *Document) pageSize(pageNumber int) (*page, error) {
	if !d.usable() {
		return nil, ErrDocumentReleased
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return nil, ErrDocumentReleased
	}
	if pageNumber < 0 || pageNumber >= d.eng.pageCount() {
		return nil, ErrInvalidPageNumber
	}
	pg, err := d.eng.loadPage(pageNumber)
	if err != nil {
		return nil, err
	}
	return pg, nil
}

func dpiToScale(dpi int) float64 {
	// Cap the scale at 10x: displays with bad EDID data make programs report wildly wrong dpi.
	return min(float64(max(dpi, 1))/72, 10)
}

// renderSpec describes how one render sizes its output: scale converts page space to pixels, and maxWidth/maxHeight,
// when positive, cap the pixel extents. RenderPage leaves the caps zero. RenderPageForSize sets them because it
// promises the image fits: its fit scale is computed in float64 while renderExtent redoes the multiply in float32
// before ceiling with a fixed 0.001 epsilon, and past roughly 17,000 px the float32 rounding error outgrows that
// epsilon, so the unclamped extent can land one pixel past the box.
type renderSpec struct {
	scale               float64
	maxWidth, maxHeight int
}

// extents returns the pixel dimensions the page renders to under this spec. It is the single authority for those
// dimensions: rasterize sizes the surface from it, and search-hit and link coordinates are bounded to it. The extent
// multiply happens in float32 (the engine's geometry precision, pinned by the MuPDF dimension goldens) while the
// coordinate multiply happens in float64 (pinned by the MuPDF coordinate goldens); the two differ in the last bits, so
// without the bound a full-page link on a US Letter page at 150 dpi reports a bottom edge one row below the 1650-row
// image.
func (s renderSpec) extents(pg *page) (width, height int) {
	width = renderExtent(pg.width, s.scale)
	height = renderExtent(pg.height, s.scale)
	if s.maxWidth > 0 {
		width = min(width, s.maxWidth)
	}
	if s.maxHeight > 0 {
		height = min(height, s.maxHeight)
	}
	return width, height
}

// SetStemDarkening enables or disables stem darkening for subsequent renders (RenderPage, RenderPageForSize, and
// DrawPage). It is enabled by default.
//
// Stem darkening dilates fill-mode text outlines by a sub-pixel, size-dependent amount before rasterization, so stems
// thinner than a pixel keep full ink coverage instead of fading to gray — the treatment platform rasterizers apply
// (CoreGraphics "font smoothing", FreeType's CFF stem darkening), tuned to match macOS Preview's text weight. Disable
// it for exact analytic-AA area coverage, the rasterization MuPDF produces and the package's parity goldens are
// recorded against. Text painted with mesh-shading or tiling patterns, stroked text, Type 3 glyphs, and text clip paths
// are never darkened.
//
// Does nothing on a released document.
func (d *Document) SetStemDarkening(enabled bool) {
	if !d.usable() {
		return
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return
	}
	d.eng.stemDarkening = enabled
}

// StemDarkening reports whether stem darkening is enabled (see SetStemDarkening). Returns false if the document has
// been released.
func (d *Document) StemDarkening() bool {
	if !d.usable() {
		return false
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return false
	}
	return d.eng.stemDarkening
}

// RenderPage renders the specified page at the requested dpi. If search is not empty, then the bounding boxes of up to
// maxHits matching text on the page will be returned.
func (d *Document) RenderPage(pageNumber, dpi, maxHits int, search string) (*RenderedPage, error) {
	return d.render(pageNumber, maxHits, search, func(*page) (renderSpec, error) {
		return renderSpec{scale: dpiToScale(dpi)}, nil
	})
}

// RenderPageForSize renders the specified page to fit within the requested size. If search is not empty, then the
// bounding boxes of up to maxHits matching text on the page will be returned.
func (d *Document) RenderPageForSize(pageNumber, maxWidth, maxHeight, maxHits int, search string) (*RenderedPage, error) {
	return d.render(pageNumber, maxHits, search, func(pg *page) (renderSpec, error) {
		return fitSpec(pg, maxWidth, maxHeight)
	})
}

// fitSpec is the render spec that fits pg within maxWidth×maxHeight: the scale is the smaller of the two ratios, and
// the box travels with it so the extents can never exceed it. RenderPageForSize and TextPage.ForSize share it, so a
// fit-to-box image and the text labeled for it are the same pixel space. It rejects a request as RenderPageForSize
// documents — ErrInvalidPageSize for a non-positive box or page, ErrImageTooLarge past OverallMaxPixels — before
// any buffer is allocated.
func fitSpec(pg *page, maxWidth, maxHeight int) (renderSpec, error) {
	if maxWidth <= 0 || maxHeight <= 0 {
		return renderSpec{}, ErrInvalidPageSize
	}
	w := float64(pg.width)
	h := float64(pg.height)
	if w <= 0 || h <= 0 {
		return renderSpec{}, ErrInvalidPageSize
	}
	scale := float64(maxWidth) / w
	if ratio := float64(maxHeight) / h; ratio < scale {
		scale = ratio
	}
	if (w*scale)*(h*scale) > float64(OverallMaxPixels) {
		return renderSpec{}, ErrImageTooLarge
	}
	return renderSpec{scale: scale, maxWidth: maxWidth, maxHeight: maxHeight}, nil
}

// render is the shared body of RenderPage and RenderPageForSize. specFor computes the render spec from the loaded page
// and may reject the request; it is carried into loadLinks, which needs it to size the destination page of an internal
// link. The document lock is held throughout.
func (d *Document) render(pageNumber, maxHits int, search string, specFor func(pg *page) (renderSpec, error)) (*RenderedPage, error) {
	if !d.usable() {
		return nil, ErrDocumentReleased
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return nil, ErrDocumentReleased
	}
	if pageNumber < 0 || pageNumber >= d.eng.pageCount() {
		return nil, ErrInvalidPageNumber
	}
	pg, err := d.eng.loadPage(pageNumber)
	if err != nil {
		return nil, err
	}
	spec, err := specFor(pg)
	if err != nil {
		return nil, err
	}
	img, err := d.renderPage(pg, spec)
	if err != nil {
		return nil, err
	}
	// Coordinates are bounded to the image that came back, not to a recomputed extent.
	self := pixelSpace{scale: spec.scale, bounds: img.Rect}
	return &RenderedPage{
		Image:      img,
		SearchHits: d.searchPage(pg, self, search, maxHits),
		Links:      d.loadLinks(pg, self, specFor),
	}, nil
}

func (d *Document) renderPage(pg *page, spec renderSpec) (*image.NRGBA, error) {
	pix, width, height, stride, err := d.eng.rasterize(pg, spec)
	if err != nil {
		return nil, err
	}
	if width <= 0 || height <= 0 {
		return nil, ErrUnableToCreateImage
	}
	if int64(width)*int64(height) > int64(OverallMaxPixels) {
		return nil, ErrImageTooLarge
	}
	size := stride * height
	if size <= 0 || len(pix) < size {
		return nil, ErrUnableToCreateImage
	}
	// The engine rasterizes premultiplied; image.NRGBA is straight alpha.
	unpremultiplyPixelsFn(pix)
	return &image.NRGBA{
		Pix:    pix,
		Stride: stride,
		Rect:   image.Rect(0, 0, width, height),
	}, nil
}

// unpremultiplyPixelsScalar converts a premultiplied RGBA byte buffer to straight alpha in place, leaving a == 0 and
// a == 255 pixels and any trailing partial pixel untouched. It is the default behind unpremultiplyPixelsFn and the
// fallback the vector kernel takes for short buffers.
func unpremultiplyPixelsScalar(pix []byte) {
	for i := 0; i+3 < len(pix); i += 4 {
		switch a := pix[i+3]; a {
		case 0, 255:
		default:
			pix[i] = unpremultiply(pix[i], a)
			pix[i+1] = unpremultiply(pix[i+1], a)
			pix[i+2] = unpremultiply(pix[i+2], a)
		}
	}
}

// unpremultiply converts a single premultiplied color component back to its straight-alpha value, rounding to nearest
// and clamping to 0xff. The caller guarantees a is neither 0 nor 0xff.
func unpremultiply(c, a uint8) uint8 {
	v := (int(c)*0xff + int(a)/2) / int(a)
	if v > 0xff {
		return 0xff
	}
	return uint8(v)
}

// pixelSpace is the rendered-image pixel space of one page: the scale page-space coordinates are multiplied by and the
// image rectangle the results are bounded to. Every coordinate the public API returns lives in one of these — the
// rendered page's own for search hits, highlights, and link bounds, the destination page's for a link's DestPoint —
// and a TextPage's points come back in through the same space.
type pixelSpace struct {
	bounds image.Rectangle
	scale  float64
}

// rect maps a page-space rectangle into this space, expanded outward to whole pixels and bounded to the image.
func (s pixelSpace) rect(x0, y0, x1, y1 float64) image.Rectangle {
	return scaleRect(x0, y0, x1, y1, s.scale).Intersect(s.bounds)
}

// unscalePoint maps a point of this space back to page space, the direction a hit test runs in. Nothing is rounded or
// bounded: the result feeds a nearest-boundary search over character geometry, and a point outside the image is a
// legitimate question there (a drag that left the page still selects to its nearest line). The zero value answers with
// the origin rather than dividing by zero.
func (s pixelSpace) unscalePoint(pt image.Point) gfx.Point {
	if s.scale <= 0 {
		return gfx.Point{}
	}
	return gfx.Point{X: float32(float64(pt.X) / s.scale), Y: float32(float64(pt.Y) / s.scale)}
}

// point maps a page-space point into this space, floored and bounded to the image.
func (s pixelSpace) point(x, y float64) image.Point {
	return image.Pt(
		min(max(scaledFloor(x, s.scale), s.bounds.Min.X), s.bounds.Max.X),
		min(max(scaledFloor(y, s.scale), s.bounds.Min.Y), s.bounds.Max.Y),
	)
}

func (d *Document) searchPage(pg *page, self pixelSpace, search string, maxHits int) []image.Rectangle {
	var boxes []image.Rectangle
	if search != "" && maxHits > 0 && OverallMaxHits > 0 {
		hits := d.eng.search(pg, search, min(maxHits, OverallMaxHits))
		if len(hits) > 0 {
			boxes = make([]image.Rectangle, len(hits))
			for i, q := range hits {
				boxes[i] = quadToRect(q, self)
			}
		}
	}
	return boxes
}

// quadToRect returns the scaled, axis-aligned bounding rectangle of all four corners of a quad, which keeps the box
// correct for rotated or skewed text.
func quadToRect(q quad, self pixelSpace) image.Rectangle {
	minX := math.Min(math.Min(float64(q.ulX), float64(q.urX)), math.Min(float64(q.llX), float64(q.lrX)))
	minY := math.Min(math.Min(float64(q.ulY), float64(q.urY)), math.Min(float64(q.llY), float64(q.lrY)))
	maxX := math.Max(math.Max(float64(q.ulX), float64(q.urX)), math.Max(float64(q.llX), float64(q.lrX)))
	maxY := math.Max(math.Max(float64(q.ulY), float64(q.urY)), math.Max(float64(q.llY), float64(q.lrY)))
	return self.rect(minX, minY, maxX, maxY)
}

// scaleRect scales an axis-aligned rectangle to integer pixel space, flooring the min corner and ceiling the max corner
// so the box never clips its content.
func scaleRect(x0, y0, x1, y1, scale float64) image.Rectangle {
	return image.Rect(
		clampFloatToInt(math.Floor(x0*scale)),
		clampFloatToInt(math.Floor(y0*scale)),
		clampFloatToInt(math.Ceil(x1*scale)),
		clampFloatToInt(math.Ceil(y1*scale)),
	)
}

// scaledFloor multiplies v by scale, floors the result, and converts it to an int. A destination with no explicit
// coordinate (a /Fit destination, in link targets and TOC entries) is a non-finite value, which maps to 0.
func scaledFloor(v, scale float64) int {
	return clampFloatToInt(math.Floor(v * scale))
}

// clampFloatToInt converts an already-rounded float to an int, mapping non-finite or out-of-range values to 0. Go's
// conversion of such a float to int is architecture-defined (0 on arm64, math.MinInt64 on amd64), so clamping keeps
// the returned coordinates deterministic across architectures.
func clampFloatToInt(r float64) int {
	// float64 represents math.MinInt (−2^63) exactly but not math.MaxInt (2^63−1), which rounds up to 2^63, so a `>`
	// test against it would let an r of exactly 2^63 through to int(r). −math.MinInt is exactly 2^63, so `>=` against
	// it rejects the first out-of-range value precisely.
	if math.IsNaN(r) || r < math.MinInt || r >= -float64(math.MinInt) {
		return 0
	}
	return int(r)
}

func (d *Document) loadLinks(pg *page, self pixelSpace, specFor func(pg *page) (renderSpec, error)) []*PageLink {
	if OverallMaxLinks < 1 {
		return nil
	}
	// DestPoint is in the pixel space of the destination page's rendered image, which RenderPageForSize scales from
	// that page's own bounds. The spaces are memoized because a page can carry thousands of links into a handful of
	// pages.
	destSpaces := make(map[int]pixelSpace)
	var links []*PageLink
	for _, link := range d.eng.links(pg) {
		pageLink := &PageLink{
			PageNumber: -1,
			Bounds:     self.rect(float64(link.x0), float64(link.y0), float64(link.x1), float64(link.y1)),
		}
		// Internal links carry the engine-resolved 0-based target page plus the destination point on it. Internal links
		// that cannot be resolved come back as page -1 with an empty URI and are dropped below.
		if link.external {
			pageLink.URI = sanitizeString(link.uri)
		} else {
			pageLink.PageNumber = link.page
			dest := d.destSpace(link.page, self, specFor, destSpaces)
			pageLink.DestPoint = dest.point(float64(link.destX), float64(link.destY))
		}
		if pageLink.PageNumber >= 0 || pageLink.URI != "" {
			if links = append(links, pageLink); len(links) >= OverallMaxLinks {
				break
			}
		}
	}
	return links
}

// destSpace returns the pixel space the given destination page renders into under this render's sizing policy,
// memoized in cache. A page that cannot be loaded or sized falls back to the rendered page's own space. Under
// RenderPage every page shares one scale, so this always returns self there.
func (d *Document) destSpace(pageNumber int, self pixelSpace, specFor func(pg *page) (renderSpec, error),
	cache map[int]pixelSpace,
) pixelSpace {
	if space, ok := cache[pageNumber]; ok {
		return space
	}
	space := self
	if destPage, err := d.eng.loadPage(pageNumber); err == nil {
		if spec, specErr := specFor(destPage); specErr == nil {
			width, height := spec.extents(destPage)
			space = pixelSpace{scale: spec.scale, bounds: image.Rect(0, 0, width, height)}
		}
	}
	cache[pageNumber] = space
	return space
}

// Release drops the engine state (parsed objects, caches, and the copy of the document bytes) immediately. It is
// optional: garbage collection reclaims the memory once the Document is unreachable.
func (d *Document) Release() {
	if !d.usable() {
		return
	}
	d.release()
}

func (d *document) release() {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.eng = nil
}

// ------------------------------------------------------------------------------------------------------------------
// Engine seam. Everything below is the boundary between the public API above — validation, budgeting, and coordinate
// conversion — and the engine in the internal packages. The seam types carry float32 geometry: every value the
// original cgo implementation received as a C float must round-trip through float32 before the float64 scale/floor/ceil
// math, or the exact-value tests show off-by-ones.
// ------------------------------------------------------------------------------------------------------------------

// engineDocument holds the engine-side state for an open document, created by openEngine and discarded by release().
type engineDocument struct {
	doc *doc.Document
	// store is the resource cache (fonts, decoded images, glyph outlines) shared by all of this document's renders;
	// New's maxCacheSize is its byte budget (0 = unlimited).
	store *store.Store
	// dev is the raster device reused across renders while the output dimensions repeat, which avoids allocating and
	// page-faulting a fresh multi-megabyte surface per render. It is dropped on a dimension change or a render panic.
	dev *render.Device
	// stemDarkening mirrors SetStemDarkening; openEngine starts it enabled.
	stemDarkening bool
}

// page is the engine-side handle for a loaded page: its 0-based number and its displayed extent in PDF points (the
// effective box after rotation). Content is fetched from the engine's document by page number.
type page struct {
	width, height float32
	number        int
}

// outlineNode is one node of the document outline (/Outlines tree), in the shape buildTOCEntries consumes: siblings are
// linked through next, children hang off down, and x/y are the destination coordinate on the 0-based target page in
// top-left/y-down page space (NaN when the destination carries no explicit coordinate).
type outlineNode struct {
	down  *outlineNode
	next  *outlineNode
	title string
	page  int
	x, y  float32
}

// quad is a single text quadrilateral in page space, such as a search hit. Text can be rotated or skewed, so a quad is
// not necessarily axis-aligned; the corners are upper-left, upper-right, lower-left, and lower-right.
type quad struct {
	ulX, ulY, urX, urY, llX, llY, lrX, lrY float32
}

// pageLinkInfo describes one link annotation on a page, in top-left/y-down page space, as loadLinks consumes it.
type pageLinkInfo struct {
	uri            string  // external links only
	page           int     // 0-based target page; -1 when external or unresolvable
	x0, y0, x1, y1 float32 // the clickable hot zone
	destX, destY   float32 // destination point; NaN when none (e.g. a /Fit destination)
	external       bool
}

// openEngine parses the raw PDF bytes into the engine's document state. Any parse failure, and any panic provoked by
// hostile input, surfaces as ErrUnableToOpenPDF.
func openEngine(buffer []byte, maxCacheSize uint64) (eng *engineDocument, err error) {
	defer func() {
		if recover() != nil {
			eng = nil
			err = ErrUnableToOpenPDF
		}
	}()
	// The engine retains and slices into the document bytes for the life of the Document, so take a private copy;
	// callers may reuse their buffer.
	d, derr := doc.Open(bytes.Clone(buffer))
	if derr != nil {
		return nil, ErrUnableToOpenPDF
	}
	return &engineDocument{doc: d, store: store.New(maxCacheSize), stemDarkening: true}, nil
}

// needsPassword reports whether the document is encrypted and the empty user password does not grant access.
func (e *engineDocument) needsPassword() bool {
	return e.doc.NeedsPassword()
}

// authenticate returns MuPDF-compatible status bits. doc.Authenticate produces them in AuthenticationStatus's layout
// (bit 0 = no authentication required, bit 1 = user, bit 2 = owner). It decrypts and re-parses the page tree on
// untrusted input, so a panic surfaces as a zero status rather than escaping the public API.
func (e *engineDocument) authenticate(password string) (status AuthenticationStatus) {
	defer func() {
		if recover() != nil {
			status = 0
		}
	}()
	return AuthenticationStatus(e.doc.Authenticate(password))
}

// pageCount returns the number of pages in the document, or 0 when it cannot be determined.
func (e *engineDocument) pageCount() int {
	return e.doc.PageCount()
}

// loadPage loads the given 0-based page, capturing its display geometry.
func (e *engineDocument) loadPage(pageNumber int) (*page, error) {
	w, h, err := e.doc.PageSize(pageNumber)
	if err != nil {
		return nil, ErrUnableToLoadPage
	}
	return &page{width: w, height: h, number: pageNumber}, nil
}

// outline returns the root of the document outline, or nil when there is none. doc.Outline walks the untrusted
// /Outlines tree; a panic provoked by a malformed tree surfaces as a nil outline rather than escaping the public API.
func (e *engineDocument) outline() (root *outlineNode) {
	defer func() {
		if recover() != nil {
			root = nil
		}
	}()
	return convertOutline(e.doc.Outline())
}

// maxOutlineConvertDepth bounds convertOutlineGuarded's recursion. It sits above the engine's own depth cap (64), so
// legitimate trees are never truncated here; it only guards against an invariant slip.
const maxOutlineConvertDepth = 128

func convertOutline(item *doc.OutlineItem) *outlineNode {
	return convertOutlineGuarded(item, 0, make(map[*doc.OutlineItem]bool))
}

// convertOutlineGuarded re-labels the engine's outline tree into the seam type, walking siblings iteratively. The
// visited set cuts any next/down cycle and the depth cap bounds recursion, so conversion terminates even if the
// engine's acyclic, depth-capped invariant is violated.
func convertOutlineGuarded(item *doc.OutlineItem, depth int, visited map[*doc.OutlineItem]bool) *outlineNode {
	if depth > maxOutlineConvertDepth {
		return nil
	}
	var head *outlineNode
	tail := &head
	for ; item != nil; item = item.Next {
		if visited[item] {
			break
		}
		visited[item] = true
		node := &outlineNode{
			down:  convertOutlineGuarded(item.Down, depth+1, visited),
			title: item.Title,
			page:  item.Page,
			x:     item.X,
			y:     item.Y,
		}
		*tail = node
		tail = &node.next
	}
	return head
}

// links returns the page's link annotations in /Annots order, with geometry already in top-left/y-down page space. A
// panic provoked by malformed annotations surfaces as no links rather than escaping the public API.
func (e *engineDocument) links(pg *page) (infos []pageLinkInfo) {
	defer func() {
		if recover() != nil {
			infos = nil
		}
	}()
	engineLinks := e.doc.Links(pg.number)
	if len(engineLinks) == 0 {
		return nil
	}
	links := make([]pageLinkInfo, len(engineLinks))
	for i, link := range engineLinks {
		links[i] = pageLinkInfo{
			uri:      link.URI,
			page:     link.Page,
			x0:       link.X0,
			y0:       link.Y0,
			x1:       link.X1,
			y1:       link.Y1,
			destX:    link.DestX,
			destY:    link.DestY,
			external: link.External,
		}
	}
	return links
}

// rasterize renders the page into premultiplied RGBA pixels (4 bytes per pixel, stride bytes per row). The surface is
// read back still premultiplied; renderPage unpremultiplies, keeping that rounding under the public API's control for
// pixel parity. The output extent must round exactly as MuPDF's fz_round_rect does, since the dimension goldens (and
// TestPDF's stride/bounds literals) were captured from it; see renderExtent. A panic provoked by hostile content
// surfaces as ErrInternal rather than escaping the public API.
func (e *engineDocument) rasterize(pg *page, spec renderSpec) (pix []byte, width, height, stride int, err error) {
	defer func() {
		if recover() != nil {
			pix = nil
			err = ErrInternal
			e.dev = nil // The device may hold half-unwound canvas state; never reuse it.
		}
	}()
	width, height = spec.extents(pg)
	if width <= 0 || height <= 0 {
		return nil, 0, 0, 0, ErrUnableToCreateImage
	}
	// Guard the surface allocation below; renderPage re-checks centrally.
	if int64(width)*int64(height) > int64(OverallMaxPixels) {
		return nil, 0, 0, 0, ErrImageTooLarge
	}
	dev := e.dev
	if dev != nil && dev.Size() == [2]int{width, height} {
		dev.Reset()
	} else {
		e.dev = nil
		if dev, err = render.New(width, height); err != nil {
			return nil, 0, 0, 0, ErrUnableToCreateImage
		}
	}
	// The reused surface is not covered by maxCacheSize, so it is only kept across renders when it fits that budget: a
	// document rendered once at high dpi would otherwise hold width*height*4 bytes (33 MB for US Letter at 300 dpi)
	// for its whole lifetime. Retention is decided before the render so a panic mid-run still finds the device in
	// e.dev to clear.
	if budget := e.store.Max(); budget == 0 || surfaceBytes(width, height) <= budget {
		e.dev = dev
	} else {
		e.dev = nil
	}
	dev.SetStore(e.store)
	dev.SetStemDarkening(e.stemDarkening)
	ctm, err := e.doc.PageCTM(pg.number, float32(spec.scale))
	if err != nil {
		return nil, 0, 0, 0, ErrUnableToLoadPage
	}
	e.runPage(pg, ctm, dev)
	pix, stride, err = dev.Pixels()
	if err != nil {
		return nil, 0, 0, 0, ErrUnableToCreateImage
	}
	return pix, width, height, stride, nil
}

// runPage runs the page's content streams and then its annotation appearance streams through the interpreter against
// dev under the page-space→device matrix ctm. It is the one body shared by rasterize, extractPageText, and DrawPage.
func (e *engineDocument) runPage(pg *page, ctm gfx.Matrix, dev device.Device) {
	if data := e.doc.PageContents(pg.number); len(data) > 0 {
		content.Run(e.doc.COS(), e.doc.PageResources(pg.number), data, ctm, dev, e.store)
	}
	e.runAnnots(pg, ctm, dev)
}

// runAnnots draws the page's annotation appearance streams after the page content, in /Annots order, matching MuPDF's
// fz_run_page, whose display list the goldens and search results were captured from. internal/doc has already applied
// the selection gates (flags, subtype, /AS state) and computed each appearance's ISO 32000-2 12.5.5 placement in page
// space. Each appearance runs as its own interpreter pass with a fresh default graphics state, inheriting the page's
// resources when it has none. The passes share one content.AnnotRun, which bounds them as a group: a page may name
// tens of thousands of annotations, and a per-annotation budget would let them all point at one appearance stream and
// re-decode it under a full budget each.
func (e *engineDocument) runAnnots(pg *page, ctm gfx.Matrix, dev device.Device) {
	annots := e.doc.Annotations(pg.number)
	if len(annots) == 0 {
		return
	}
	// PageResources re-resolves the /Resources entry through the COS layer on every call, so resolve it once per pass.
	res := e.doc.PageResources(pg.number)
	run := content.NewAnnotRun(e.store)
	for _, a := range annots {
		run.Annot(e.doc.COS(), res, a.Raw, a.Stream, a.Transform.Mul(ctm), dev)
	}
}

// surfaceBytes is the byte size of a width×height raster surface at the 4 bytes per pixel the device allocates: the
// cost of retaining the reused device between renders.
func surfaceBytes(width, height int) uint64 {
	return uint64(width) * uint64(height) * 4
}

// renderExtent converts one page-space extent to rendered pixels: float32 multiply (the engine's geometry precision),
// then ceil with MuPDF's rounding epsilon. Non-finite and absurd values collapse to 0, which the caller rejects.
func renderExtent(extent float32, scale float64) int {
	v := math.Ceil(float64(extent*float32(scale)) - 0.001)
	if math.IsNaN(v) || v < 0 || v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

// extractPageText runs the page's content and annotation appearance streams against the given structured-text device
// at scale 1, the invariant both consumers of extracted text depend on: characters (and so quads) come back in
// top-left/y-down page space, the space MuPDF's fz_search_stext_page reported hits in through the C float funnel, and
// the render scale is applied afterwards in float64. Running the pass at the render scale would compose every quad
// corner in scaled float32 and break that funnel. Appearance text is part of MuPDF's structured text (widget /AP text
// is searchable), so the pass runs the appearances as the raster pass does. A page whose CTM cannot be read reports
// ErrUnableToLoadPage, as rasterize does.
func (e *engineDocument) extractPageText(pg *page, dev *stext.Device) error {
	ctm, err := e.doc.PageCTM(pg.number, 1)
	if err != nil {
		return ErrUnableToLoadPage
	}
	e.runPage(pg, ctm, dev)
	return nil
}

// search returns the quads of up to maxHits matches on the page, in the order MuPDF's search reports them (the
// exact-value tests index hits positionally). A panic provoked by hostile content surfaces as no hits rather than
// escaping the public API.
func (e *engineDocument) search(pg *page, needle string, maxHits int) (hits []quad) {
	defer func() {
		if recover() != nil {
			hits = nil
		}
	}()
	dev := stext.New()
	if e.extractPageText(pg, dev) != nil {
		return nil
	}
	found := dev.Search(needle, maxHits)
	if len(found) == 0 {
		return nil
	}
	hits = make([]quad, len(found))
	for i, q := range found {
		hits[i] = quadFromGfx(q)
	}
	return hits
}

// extractText returns the selection model for the page's text, in the page space extractPageText documents. It is the
// same pass search makes — what is searchable is what is selectable — capped at OverallMaxTextChars characters
// because the result outlives the call.
//
// A page the engine cannot read at all yields the error, so a viewer can tell an unreadable page from an empty one. A
// panic provoked by hostile content mid-pass yields the characters recorded before it and no error: the device only
// ever appends completed characters, so its slice is a valid prefix of the page's text at every instant.
func (e *engineDocument) extractText(pg *page) (text *stext.Page, err error) {
	dev := stext.NewCapped(OverallMaxTextChars)
	defer func() {
		if recover() != nil {
			text, err = stext.NewPage(dev.Chars()), nil
		}
	}()
	if err = e.extractPageText(pg, dev); err != nil {
		return nil, err
	}
	return stext.NewPage(dev.Chars()), nil
}

// quadFromGfx re-labels a structured-text quad into the seam type, so the public API's coordinate funnel never depends
// on an internal type.
func quadFromGfx(q gfx.Quad) quad {
	return quad{
		ulX: q.UL.X, ulY: q.UL.Y, urX: q.UR.X, urY: q.UR.Y,
		llX: q.LL.X, llY: q.LL.Y, lrX: q.LR.X, lrY: q.LR.Y,
	}
}
