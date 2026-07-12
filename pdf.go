// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package pdfview renders PDF pages to images and extracts text-search hits, links, and the table of contents. It
// also handles password-protected documents.
//
// The package is a pure-Go PDF engine — no cgo, CGO_ENABLED=0 builds — with rasterization delegated to
// github.com/richardwilkes/canvas. New parses documents (including damaged ones, via a repair scan) and decrypts
// password-protected ones (standard security handler R2-R6); RenderPage and RenderPageForSize rasterize each
// page's content — paths, clips, colors, form XObjects, images, fonts and text, shadings, patterns, transparency
// groups, soft masks, blend modes, and annotation appearance streams — through the content-stream interpreter;
// text search returns MuPDF-compatible hit rectangles; and DrawPage draws a page's content onto a caller-owned
// canvas. The engine's behavior is pinned against the MuPDF-based github.com/richardwilkes/pdf binding it
// succeeds: coordinates exactly, pixels within committed perceptual thresholds. See README.md for the
// architecture and plan.md for the port's historical record and decision log.
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

// Possible error values
var (
	ErrNotPDFData               = errors.New("only PDF documents are supported")
	ErrUnableToCreatePDFContext = errors.New("unable to create PDF context")
	ErrInternal                 = errors.New("internal error")
	ErrUnableToOpenPDF          = errors.New("unable to open PDF")
	ErrInvalidPageNumber        = errors.New("invalid page number")
	ErrUnableToLoadPage         = errors.New("unable to load page")
	ErrUnableToCreateImage      = errors.New("unable to create image")
	ErrImageTooLarge            = errors.New("rendered image would be too large")
	ErrInvalidPageSize          = errors.New("invalid page size")
	ErrDocumentReleased         = errors.New("document has been released")
)

// Each of these variables is global and are not safe to modify when other calls to this code are being made. Generally,
// they should be modified at startup before any other use of this package.
var (
	// OverallMaxHits is the maximum number of hits returned, even if the API is called with a larger value. This is
	// here to safeguard against untrusted input that might otherwise cause an out of memory error.
	OverallMaxHits = 1000
	// OverallMaxLinks is the maximum number of links returned. This is here to safeguard against untrusted input that
	// might otherwise cause an out of memory error.
	OverallMaxLinks = 1000
	// OverallMaxTOCEntries is the maximum number of TOC entries returned. This is here to safeguard against untrusted
	// input that might otherwise cause an out of memory error.
	OverallMaxTOCEntries = 1000
	// OverallMaxPixels is the maximum number of pixels (width × height) a rendered page image may contain. Requests
	// that would produce a larger image are rejected rather than attempting a very large allocation, safeguarding
	// against untrusted input or bad sizing parameters that might otherwise cause an out of memory error. The default
	// matches the largest image permitted by the internal 32-bit limit on the rendered buffer's byte size (4 bytes per
	// pixel).
	OverallMaxPixels = math.MaxInt32 / 4
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

// Document represents PDF document. Page numbers for the exposed API are zero-based. Methods on this are safe to use
// from multiple goroutines. Calls into the underlying engine are serialized internally, so they execute one at a time.
type Document struct {
	// document is held by pointer so the public wrapper stays a single word and the mutex it contains is shared by
	// every copy of the wrapper rather than duplicated; Release drops the engine state through this shared pointer.
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
	// DestPoint is the point on the destination page (PageNumber) that an internal link targets, in rendered-image
	// pixel space. It is the zero value (0,0) for external links and for internal links whose destination has no
	// explicit coordinate (such as a /Fit destination).
	DestPoint image.Point
}

// RenderedPage holds the rendered page.
type RenderedPage struct {
	// Image is the rendered page. It is rendered with an alpha channel, and most PDF pages do not paint their own
	// background, so areas with no content are transparent rather than white. Callers that want an opaque page (for
	// example, when encoding to a format without alpha) should composite the image onto their desired background color.
	Image      *image.NRGBA
	SearchHits []image.Rectangle
	Links      []*PageLink
}

// New returns new PDF document from the provided raw bytes. Pass in 0 for maxCacheSize for no limit.
func New(buffer []byte, maxCacheSize uint64) (*Document, error) {
	// Allow some garbage to be before the PDF content, as Acrobat and MuPDF itself allow it
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

// RequiresAuthentication returns true if a password is required. Returns false if the document has been released.
func (d *Document) RequiresAuthentication() bool {
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return false
	}
	return d.eng.needsPassword()
}

// Authenticate with either the user or owner password. Returns a zero status if the document has been released.
func (d *Document) Authenticate(password string) AuthenticationStatus {
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return 0
	}
	return d.eng.authenticate(password)
}

// TableOfContents returns the table of contents for this document, if any.
func (d *Document) TableOfContents(dpi int) []*TOCEntry {
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
		// U+FFFD (the Unicode replacement character) stands in for bytes that could not be decoded as valid UTF-8,
		// such as the unmappable dot-leader glyphs some PDFs place in outline titles. It is printable and non-control,
		// so it would otherwise survive the filter below; drop it explicitly to keep those spurious characters out.
		if ch != unicode.ReplacementChar && !unicode.IsControl(ch) && unicode.IsPrint(ch) {
			sanitized = append(sanitized, ch)
		}
	}
	return strings.TrimSpace(string(sanitized))
}

// PageCount returns total number of pages in the document.
func (d *Document) PageCount() int {
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

func dpiToScale(dpi int) float64 {
	// Limit scaling to 10x; some displays report bad EDID data, causing the input DPI from programs to be wildly off.
	return min(float64(max(dpi, 1))/72, 10)
}

// RenderPage renders the specified page at the requested dpi. If search is not empty, then the bounding boxes of up to
// maxHits matching text on the page will be returned.
func (d *Document) RenderPage(pageNumber, dpi, maxHits int, search string) (*RenderedPage, error) {
	return d.render(pageNumber, maxHits, search, func(*page) (float64, error) {
		return dpiToScale(dpi), nil
	})
}

// RenderPageForSize renders the specified page to fit within the requested size. If search is not empty, then the
// bounding boxes of up to maxHits matching text on the page will be returned.
func (d *Document) RenderPageForSize(pageNumber, maxWidth, maxHeight, maxHits int, search string) (*RenderedPage, error) {
	return d.render(pageNumber, maxHits, search, func(pg *page) (float64, error) {
		if maxWidth <= 0 || maxHeight <= 0 {
			return 0, ErrInvalidPageSize
		}
		// The page extents are computed in float32 (the precision the engine's geometry pipeline carries) before
		// being widened, matching the C float precision the MuPDF-based implementation exposed.
		bw, bh := pg.bounds()
		w := float64(bw)
		h := float64(bh)
		if w <= 0 || h <= 0 {
			return 0, ErrInvalidPageSize
		}
		scale := float64(maxWidth) / w
		if ratio := float64(maxHeight) / h; ratio < scale {
			scale = ratio
		}
		// The rendered image is scaled to fit within maxWidth×maxHeight, so its pixel count is bounded by the
		// requested box. Reject an over-large request here, before doing any rendering work or allocating the pixel
		// buffer.
		if (w*scale)*(h*scale) > float64(OverallMaxPixels) {
			return 0, ErrImageTooLarge
		}
		return scale, nil
	})
}

// render is the shared body of RenderPage and RenderPageForSize. It validates the page number, loads the page, asks
// scaleFor to compute the render scale (which may inspect the page bounds and reject the request), renders, and
// assembles the result. The document lock is held throughout so all engine work is serialized.
func (d *Document) render(pageNumber, maxHits int, search string, scaleFor func(pg *page) (float64, error)) (*RenderedPage, error) {
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
	scale, err := scaleFor(pg)
	if err != nil {
		return nil, err
	}
	img, err := d.renderPage(pg, scale)
	if err != nil {
		return nil, err
	}
	return &RenderedPage{
		Image:      img,
		SearchHits: d.searchPage(pg, scale, search, maxHits),
		Links:      d.loadLinks(pg, scale),
	}, nil
}

func (d *Document) renderPage(pg *page, scale float64) (*image.NRGBA, error) {
	pix, width, height, stride, err := d.eng.rasterize(pg, scale)
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
	if size > math.MaxInt32 {
		return nil, ErrImageTooLarge
	}
	// The engine rasterizes with premultiplied alpha, but image.NRGBA expects non-premultiplied (straight) alpha, so
	// undo the premultiplication. Fully opaque (a == 255) and fully transparent (a == 0) pixels need no adjustment.
	for i := 0; i+3 < len(pix); i += 4 {
		switch a := pix[i+3]; a {
		case 0, 255:
		default:
			pix[i] = unpremultiply(pix[i], a)
			pix[i+1] = unpremultiply(pix[i+1], a)
			pix[i+2] = unpremultiply(pix[i+2], a)
		}
	}
	return &image.NRGBA{
		Pix:    pix,
		Stride: stride,
		Rect:   image.Rect(0, 0, width, height),
	}, nil
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

func (d *Document) searchPage(pg *page, scale float64, search string, maxHits int) []image.Rectangle {
	var boxes []image.Rectangle
	if search != "" && maxHits > 0 && OverallMaxHits > 0 {
		hits := d.eng.search(pg, search, min(maxHits, OverallMaxHits))
		if len(hits) > 0 {
			boxes = make([]image.Rectangle, len(hits))
			for i, q := range hits {
				boxes[i] = quadToRect(q, scale)
			}
		}
	}
	return boxes
}

// quadToRect computes the scaled, axis-aligned bounding rectangle that encloses all four corners of a search-hit quad.
// Considering every corner (rather than assuming an axis-aligned quad) keeps the box correct for rotated or skewed text.
func quadToRect(q quad, scale float64) image.Rectangle {
	minX := math.Min(math.Min(float64(q.ulX), float64(q.urX)), math.Min(float64(q.llX), float64(q.lrX)))
	minY := math.Min(math.Min(float64(q.ulY), float64(q.urY)), math.Min(float64(q.llY), float64(q.lrY)))
	maxX := math.Max(math.Max(float64(q.ulX), float64(q.urX)), math.Max(float64(q.llX), float64(q.lrX)))
	maxY := math.Max(math.Max(float64(q.ulY), float64(q.urY)), math.Max(float64(q.llY), float64(q.lrY)))
	return scaleRect(minX, minY, maxX, maxY, scale)
}

// scaleRect scales an axis-aligned rectangle by scale and converts it to integer pixel space, expanding outward so the
// box never clips its content: the min corner is floored and the max corner is ceiled.
func scaleRect(x0, y0, x1, y1, scale float64) image.Rectangle {
	return image.Rect(
		int(math.Floor(x0*scale)),
		int(math.Floor(y0*scale)),
		int(math.Ceil(x1*scale)),
		int(math.Ceil(y1*scale)),
	)
}

// scaledFloor multiplies v by scale, floors the result, and converts it to an int. A destination that carries no
// explicit coordinate (e.g. a /Fit destination, in both link targets and TOC entries) is represented as a non-finite
// value; Go's conversion of a non-finite (or out-of-range) float to int is architecture-defined — 0 on arm64 but
// math.MinInt64 on amd64 — so those values are mapped to 0 here to keep the returned coordinates deterministic across
// architectures.
func scaledFloor(v, scale float64) int {
	r := math.Floor(v * scale)
	if math.IsNaN(r) || r < math.MinInt || r > math.MaxInt {
		return 0
	}
	return int(r)
}

func (d *Document) loadLinks(pg *page, scale float64) []*PageLink {
	if OverallMaxLinks < 1 {
		return nil
	}
	var links []*PageLink
	for _, link := range d.eng.links(pg) {
		pageLink := &PageLink{
			PageNumber: -1,
			Bounds:     scaleRect(float64(link.x0), float64(link.y0), float64(link.x1), float64(link.y1), scale),
		}
		// External links keep their URI; internal links carry the engine-resolved 0-based target page — the same
		// numbering this package's API uses — plus the destination point on that page. Internal links that cannot be
		// resolved come back as page -1 and, with an empty URI, are dropped by the test below.
		if link.external {
			pageLink.URI = sanitizeString(link.uri)
		} else {
			pageLink.PageNumber = link.page
			pageLink.DestPoint = image.Pt(
				scaledFloor(float64(link.destX), scale),
				scaledFloor(float64(link.destY), scale),
			)
		}
		if pageLink.PageNumber >= 0 || pageLink.URI != "" {
			if links = append(links, pageLink); len(links) >= OverallMaxLinks {
				break
			}
		}
	}
	return links
}

// Release the underlying PDF document, releasing any resources. It is not necessary to call this, as garbage collection
// will eventually reclaim the memory once the Document is unreachable, however, doing so explicitly drops the engine
// state (parsed objects, caches, and the copy of the document bytes) immediately.
func (d *Document) Release() {
	d.release()
}

func (d *document) release() {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.eng = nil
}

// ------------------------------------------------------------------------------------------------------------------
// Engine seam. Everything below is the boundary between the frozen public API above — validation, budgeting, and
// coordinate conversion — and the engine in the internal packages. The seam types deliberately carry float32
// geometry (plan.md invariant 4): every value the original cgo implementation received as a C float must
// round-trip through float32 before the float64 scale/floor/ceil math, or the exact-value tests show off-by-ones.
// ------------------------------------------------------------------------------------------------------------------

// engineDocument holds the engine-side state for an open document. It is created by openEngine and discarded by
// release().
type engineDocument struct {
	doc *doc.Document
	// store is the maxCacheSize-budgeted resource cache (fonts, decoded images, glyph outlines) shared by all
	// of this document's renders; New's maxCacheSize argument is its byte budget (0 = unlimited).
	store *store.Store
	// dev is the raster device reused across renders while the output dimensions repeat (the common case:
	// re-rendering pages of one size at one scale). Reuse avoids allocating and page-faulting a fresh
	// multi-megabyte surface per render (see the M8 perf decision log); it is dropped on a dimension change
	// or a render panic. Safe under the document mutex like every other engine field.
	dev *render.Device
}

// page is the engine-side handle for a loaded page: its 0-based number and its displayed extent in PDF points
// (the effective box after rotation, in float32 per the funnel below). Content (resources, content streams) is
// fetched from the engine's document by page number when rendering or searching.
type page struct {
	width, height float32
	number        int
}

// outlineNode is one node of the document outline (/Outlines tree), in the shape buildTOCEntries consumes: siblings
// are linked through next, children hang off down, and x/y are the destination coordinate on the 0-based target page
// in top-left/y-down page space (NaN when the destination carries no explicit coordinate).
type outlineNode struct {
	down  *outlineNode
	next  *outlineNode
	title string
	page  int
	x, y  float32
}

// quad is a single text quadrilateral in page space, such as a search hit. Text can be rotated or skewed, so a quad
// is not necessarily axis-aligned; the corners are upper-left, upper-right, lower-left, and lower-right. Coordinates
// are float32, the precision the engine's geometry pipeline carries (matching the C float precision of the
// MuPDF-based implementation, which the exact-value tests were baselined against).
type quad struct {
	ulX, ulY, urX, urY, llX, llY, lrX, lrY float32
}

// pageLinkInfo describes one link annotation on a page, in top-left/y-down page space. Produced by the navigation
// layer (internal/doc) and consumed by loadLinks to build the public PageLink values.
type pageLinkInfo struct {
	uri            string  // raw URI for external links; empty for internal links
	page           int     // internal links: 0-based target page; -1 when external or unresolvable
	x0, y0, x1, y1 float32 // link rectangle (the clickable hot zone)
	destX, destY   float32 // internal links: explicit destination point, NaN when none (e.g. a /Fit destination)
	external       bool
}

// openEngine parses the raw PDF bytes into the engine's document state, honoring maxCacheSize as the resource-cache
// budget (0 = unlimited). Any parse failure — and, per plan.md invariant 6, any panic provoked by hostile input —
// surfaces as ErrUnableToOpenPDF rather than escaping to the caller.
func openEngine(buffer []byte, maxCacheSize uint64) (eng *engineDocument, err error) {
	defer func() {
		if recover() != nil {
			eng = nil
			err = ErrUnableToOpenPDF
		}
	}()
	// The engine retains and slices into the document bytes for the life of the Document, so take a private copy;
	// callers remain free to reuse their buffer, exactly as with the previous MuPDF-based implementation (which
	// copied into C memory).
	d, derr := doc.Open(bytes.Clone(buffer))
	if derr != nil {
		return nil, ErrUnableToOpenPDF
	}
	return &engineDocument{doc: d, store: store.New(maxCacheSize)}, nil
}

// needsPassword reports whether the document is encrypted and the empty user password does not grant access.
func (e *engineDocument) needsPassword() bool {
	return e.doc.NeedsPassword()
}

// authenticate attempts to authenticate with the given user or owner password, returning MuPDF-compatible
// status bits. doc.Authenticate produces them in the same layout as AuthenticationStatus (bit 0 = no
// authentication required, bit 1 = user, bit 2 = owner), so the value maps straight across the seam.
func (e *engineDocument) authenticate(password string) AuthenticationStatus {
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

// outline returns the root of the document outline, or nil when there is none. The engine hands back its own
// linked tree in the same shape; this conversion only re-labels it into the seam type. Sibling chains are
// walked iteratively (their length is engine-capped but can be long); recursion depth equals the outline's
// nesting depth, which the engine caps far below stack limits.
func (e *engineDocument) outline() *outlineNode {
	return convertOutline(e.doc.Outline())
}

func convertOutline(item *doc.OutlineItem) *outlineNode {
	var head *outlineNode
	tail := &head
	for ; item != nil; item = item.Next {
		node := &outlineNode{
			down:  convertOutline(item.Down),
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

// links returns the link annotations present on the page, in /Annots order, with all geometry already in
// top-left/y-down page space (the engine's navigation layer maps it).
func (e *engineDocument) links(pg *page) []pageLinkInfo {
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

// rasterize renders the page at the given scale into premultiplied RGBA pixels (4 bytes per pixel, stride bytes per
// row): the page's content streams run through the interpreter (internal/content) against the raster device
// (internal/render), and the surface is read back still premultiplied (renderPage unpremultiplies; see the
// decision log on rounding parity). The output extent must round exactly as MuPDF's fz_round_rect does, since the
// dimension goldens (and TestPDF's stride/bounds literals) were captured from it: the page extent is scaled in
// float32, then the max corner is ceiled with a small epsilon so float slop just above a whole number does not
// spill into an extra pixel row (pinned against all recorded corpus dimensions; see the M3 decision log). Per
// plan.md invariant 6, a panic provoked by hostile content anywhere under the render surfaces as ErrInternal
// rather than escaping the public API.
func (e *engineDocument) rasterize(pg *page, scale float64) (pix []byte, width, height, stride int, err error) {
	defer func() {
		if recover() != nil {
			pix = nil
			err = ErrInternal
			e.dev = nil // The device may hold half-unwound canvas state; never reuse it.
		}
	}()
	width = renderExtent(pg.width, scale)
	height = renderExtent(pg.height, scale)
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
		e.dev = dev
	}
	dev.SetStore(e.store)
	ctm, err := e.doc.PageCTM(pg.number, float32(scale))
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

// runPage runs the page's content streams and then its annotation appearance streams through the interpreter
// against the given device under the page-space→device matrix ctm. It is the one body shared by every consumer
// of a page's drawn content: rasterize (raster device), search (structured-text device), and DrawPage (raster
// device wrapped around a caller's canvas).
func (e *engineDocument) runPage(pg *page, ctm gfx.Matrix, dev device.Device) {
	if data := e.doc.PageContents(pg.number); len(data) > 0 {
		content.Run(e.doc.COS(), e.doc.PageResources(pg.number), data, ctm, dev, e.store)
	}
	e.runAnnots(pg, ctm, dev)
}

// runAnnots draws the page's annotation appearance streams after the page content, in /Annots order — matching
// MuPDF's fz_run_page, whose display list the goldens (and search results, since appearance text is searchable)
// were captured from. internal/doc has already applied the selection gates (flags, subtype, /AS state) and
// computed each appearance's ISO 32000-2 12.5.5 placement in page space; composing that with the page CTM
// positions it in device space. Each appearance runs as its own interpreter pass with a fresh default graphics
// state, inheriting the page's resources when it carries none of its own.
func (e *engineDocument) runAnnots(pg *page, ctm gfx.Matrix, dev device.Device) {
	for _, a := range e.doc.Annotations(pg.number) {
		content.RunAnnot(e.doc.COS(), e.doc.PageResources(pg.number), a.Raw, a.Stream, a.Transform.Mul(ctm), dev, e.store)
	}
}

// renderExtent converts one page-space extent to rendered pixels: float32 multiply (the engine's geometry
// precision), then ceil with MuPDF's rounding epsilon. Non-finite and absurd values collapse to 0, which the
// caller rejects.
func renderExtent(extent float32, scale float64) int {
	v := math.Ceil(float64(extent*float32(scale)) - 0.001)
	if math.IsNaN(v) || v < 0 || v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

// bounds returns the page's width and height in PDF points (the page's effective box extent after rotation),
// computed in float32.
func (p *page) bounds() (width, height float32) {
	return p.width, p.height
}

// search returns the quads of up to maxHits text matches on the page, in the emission order MuPDF's search
// reports them (the exact-value tests index hits positionally). The page's content runs through the interpreter
// once more against the structured-text device at scale 1, so the quads come back in top-left/y-down page space
// — the same space MuPDF's fz_search_stext_page reported them in through the C float funnel — and quadToRect
// applies the render scale in float64 exactly as the original implementation did. Running the pass at the
// render scale instead (sharing the rasterize pass via device.Tee) would compose every quad corner in scaled
// float32 and break that funnel; see the M7 decision log. Per plan.md invariant 6, a panic provoked by hostile
// content surfaces as no hits rather than escaping the public API.
func (e *engineDocument) search(pg *page, needle string, maxHits int) (hits []quad) {
	defer func() {
		if recover() != nil {
			hits = nil
		}
	}()
	ctm, err := e.doc.PageCTM(pg.number, 1)
	if err != nil {
		return nil
	}
	dev := stext.New()
	// Annotation appearance text is part of MuPDF's structured text (probe-pinned: widget /AP text is
	// searchable), so the stext pass runs the appearances exactly like the raster pass does.
	e.runPage(pg, ctm, dev)
	found := dev.Search(needle, maxHits)
	if len(found) == 0 {
		return nil
	}
	hits = make([]quad, len(found))
	for i, q := range found {
		hits[i] = quad{
			ulX: q.UL.X, ulY: q.UL.Y, urX: q.UR.X, urY: q.UR.Y,
			llX: q.LL.X, llY: q.LL.Y, lrX: q.LR.X, lrY: q.LR.Y,
		}
	}
	return hits
}
