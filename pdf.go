// Package pdfview renders PDF pages to images and extracts text-search hits, links, and the table of contents. It
// also handles password-protected documents.
//
// The package is a pure-Go PDF engine with rasterization delegated to github.com/richardwilkes/canvas. It is being
// built milestone by milestone — see plan.md for the working plan, milestone status, and current capabilities. With
// the COS layer in place (milestone M1), New parses documents — including damaged ones, via a repair scan — and
// PageCount is real; navigation, rendering, and search still return their zero values until their milestones land.
package pdfview

import (
	"bytes"
	"errors"
	"image"
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/richardwilkes/pdfview/internal/doc"
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
// Engine seam. Everything below is the boundary the pure-Go engine grows behind, milestone by milestone (see
// plan.md). The public methods above are fully wired to it so their validation, budgeting, and coordinate-conversion
// behavior — the frozen API contract — is already in its final shape; each milestone only replaces stub bodies here
// with calls into the internal packages.
// ------------------------------------------------------------------------------------------------------------------

// engineDocument holds the engine-side state for an open document. It is created by openEngine and discarded by
// release().
type engineDocument struct {
	doc *doc.Document
}

// page is the engine-side handle for a loaded page. Its concrete content (geometry, resources, content streams)
// arrives with the navigation and graphics milestones (M3/M4).
type page struct{}

// outlineNode is one node of the document outline (/Outlines tree), in the shape buildTOCEntries consumes: siblings
// are linked through next, children hang off down, and x/y are the destination coordinate on the 0-based target page
// in top-left/y-down page space (NaN when the destination carries no explicit coordinate). Produced at M3.
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
// layer (M3) and consumed by loadLinks to build the public PageLink values.
type pageLinkInfo struct {
	uri            string  // raw URI for external links; empty for internal links
	page           int     // internal links: 0-based target page; -1 when external or unresolvable
	x0, y0, x1, y1 float32 // link rectangle (the clickable hot zone)
	destX, destY   float32 // internal links: explicit destination point, NaN when none (e.g. a /Fit destination)
	external       bool
}

// openEngine parses the raw PDF bytes into the engine's document state, honoring maxCacheSize as the resource-cache
// budget (0 = unlimited, honored from M6). Any parse failure — and, per plan.md invariant 6, any panic provoked by
// hostile input — surfaces as ErrUnableToOpenPDF rather than escaping to the caller.
func openEngine(buffer []byte, _ uint64) (eng *engineDocument, err error) {
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
	return &engineDocument{doc: d}, nil
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

// loadPage loads the given 0-based page. At M1 this verifies the page exists in the page tree; the page's
// geometry and content arrive at M3/M4.
func (e *engineDocument) loadPage(pageNumber int) (*page, error) {
	if _, err := e.doc.Page(pageNumber); err != nil {
		return nil, ErrUnableToLoadPage
	}
	return &page{}, nil
}

// outline returns the root of the document outline, or nil when there is none. Navigation lands at M3.
func (e *engineDocument) outline() *outlineNode {
	return nil
}

// links returns the link annotations present on the page. Navigation lands at M3.
func (e *engineDocument) links(_ *page) []pageLinkInfo {
	return nil
}

// rasterize renders the page at the given scale into premultiplied RGBA pixels (4 bytes per pixel, stride bytes per
// row). The raster device — internal/render on github.com/richardwilkes/canvas — lands at M4.
func (e *engineDocument) rasterize(_ *page, _ float64) (pix []byte, width, height, stride int, err error) {
	return nil, 0, 0, 0, ErrUnableToCreateImage
}

// bounds returns the page's width and height in PDF points (the page's crop box extent after rotation), computed in
// float32. Page geometry lands at M3/M4.
func (p *page) bounds() (width, height float32) {
	return 0, 0
}

// search returns the quads of up to maxHits text matches on the page. Structured text extraction and search land at
// M7.
func (e *engineDocument) search(_ *page, _ string, _ int) []quad {
	return nil
}
