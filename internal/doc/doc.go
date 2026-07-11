// Package doc implements document-level PDF semantics on top of the COS layer. At milestone M1 that is the page
// tree: opening a document builds the flat page list (honoring the tree structure with cycle and depth guards)
// that PageCount and Page answer from. Destinations, outlines, and link annotations arrive at M3.
package doc

import (
	"errors"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/crypt"
)

// maxPageTreeDepth caps page-tree recursion; combined with the visited set it guarantees the walk terminates on
// hostile or cyclic trees (see plan.md "Resource limits & robustness").
const maxPageTreeDepth = 64

// Authentication-status bits, in the same layout as the public API's AuthenticationStatus so the root package
// maps an AuthResult across the engine seam without reinterpreting it.
const (
	// AuthNoneRequired means the document is not encrypted; any password "succeeds".
	AuthNoneRequired byte = 1 << iota
	// AuthUser means the supplied password matched the user password.
	AuthUser
	// AuthOwner means the supplied password matched the owner password.
	AuthOwner
)

var errNoSuchPage = errors.New("no such page")

// Document is one open PDF document.
type Document struct {
	cos *cos.Document
	// crypt is the standard security handler when the document is encrypted with a scheme we support; nil
	// otherwise (unencrypted, or encrypted with an unsupported handler).
	crypt *crypt.Handler
	// pages holds the leaf dictionaries of the page tree, in document order.
	pages []cos.Dict
	// pageRefs holds the indirect reference of each page when it was reached through one (the zero Ref
	// otherwise); M3's destination resolution needs page-object identity.
	pageRefs []cos.Ref
	// encrypted records whether the trailer carried an /Encrypt dictionary, even if its handler is unsupported.
	encrypted bool
}

// Open parses data as a PDF document, sets up decryption if it is encrypted, and builds its page list. The COS
// layer runs its repair scan automatically when the file's cross-reference data is broken; Open fails only when
// no usable document root can be found at all. A document whose catalog has no usable page tree opens with zero
// pages. An encrypted document opens whether or not a password is available: its page tree (dictionaries,
// names, and references) is never encrypted, so PageCount works before authentication.
func Open(data []byte) (*Document, error) {
	c, err := cos.Open(data)
	if err != nil {
		return nil, err
	}
	d := &Document{cos: c}
	d.setupEncryption()
	d.buildPageList()
	return d, nil
}

// setupEncryption builds the security handler from the trailer's /Encrypt dictionary (if any) and installs it
// as the COS layer's decryptor, trying the empty password so documents that need none become immediately
// usable. An /Encrypt dictionary the handler cannot parse leaves the document flagged encrypted but locked.
func (d *Document) setupEncryption() {
	encDict, ok := cos.AsDict(d.cos.Resolve(d.cos.Trailer()["Encrypt"]))
	if !ok {
		return
	}
	d.encrypted = true
	h, err := crypt.New(d.cos, encDict)
	if err != nil {
		return
	}
	d.cos.SetDecryptor(h)
	d.crypt = h
}

// IsEncrypted reports whether the document's trailer carried an /Encrypt dictionary.
func (d *Document) IsEncrypted() bool {
	return d.encrypted
}

// NeedsPassword reports whether a password must be supplied before the document's encrypted content can be
// read. It is false for unencrypted documents and for encrypted documents the empty password already unlocked.
func (d *Document) NeedsPassword() bool {
	if d.crypt == nil {
		return d.encrypted // Encrypted with an unsupported handler: unusable without support.
	}
	return d.crypt.NeedsPassword()
}

// Authenticate tries password against the document and returns the status bits (AuthNoneRequired / AuthUser /
// AuthOwner), matching MuPDF's fz_authenticate_password. An unencrypted document reports AuthNoneRequired for
// any password. A successful authentication drops the object cache so objects read before the file key was
// available are reparsed and decrypted.
func (d *Document) Authenticate(password string) byte {
	if !d.encrypted {
		return AuthNoneRequired
	}
	if d.crypt == nil {
		return 0
	}
	user, owner := d.crypt.Authenticate(password)
	var status byte
	if user {
		status |= AuthUser
	}
	if owner {
		status |= AuthOwner
	}
	if status != 0 {
		// The file key is now available: drop objects cached without it and rewalk the page tree so its
		// dictionaries are recaptured decrypted.
		d.cos.DropCaches()
		d.buildPageList()
	}
	return status
}

// COS returns the underlying COS-level document.
func (d *Document) COS() *cos.Document {
	return d.cos
}

// PageCount returns the number of pages in the document.
func (d *Document) PageCount() int {
	return len(d.pages)
}

// Page returns the page dictionary for the given 0-based page number.
func (d *Document) Page(pageNumber int) (cos.Dict, error) {
	if pageNumber < 0 || pageNumber >= len(d.pages) {
		return nil, errNoSuchPage
	}
	return d.pages[pageNumber], nil
}

// PageRef returns the indirect reference through which the given 0-based page was reached, or the zero Ref when
// the page dictionary was inlined directly in its parent's /Kids. Destination resolution (M3) matches pages by
// this identity.
func (d *Document) PageRef(pageNumber int) (cos.Ref, error) {
	if pageNumber < 0 || pageNumber >= len(d.pageRefs) {
		return cos.Ref{}, errNoSuchPage
	}
	return d.pageRefs[pageNumber], nil
}

// buildPageList walks the page tree from the catalog, collecting leaves in document order. The walk counts
// actual leaf nodes rather than trusting /Count entries, which repair-recovered and hostile files get wrong. A
// global visited set skips reference cycles and duplicated subtrees (each page has a single parent, so a
// legitimate tree never revisits a node), and depth is capped by maxPageTreeDepth. It is idempotent: it resets
// its output first, so it can be re-run after a successful authentication to recapture page dictionaries that
// were first parsed without the file key.
func (d *Document) buildPageList() {
	d.pages = nil
	d.pageRefs = nil
	root, ok := d.cos.GetDict(d.cos.Trailer(), "Root")
	if !ok {
		return
	}
	pagesObj := root["Pages"]
	visited := make(map[cos.Ref]bool)
	var pagesRef cos.Ref
	if ref, isRef := pagesObj.(cos.Ref); isRef {
		visited[ref] = true
		pagesRef = ref
	}
	node, ok := cos.AsDict(d.cos.Resolve(pagesObj))
	if !ok {
		return
	}
	d.walkPageTree(node, pagesRef, 0, visited)
}

func (d *Document) walkPageTree(node cos.Dict, ref cos.Ref, depth int, visited map[cos.Ref]bool) {
	if depth > maxPageTreeDepth {
		return
	}
	typ, _ := d.cos.GetName(node, "Type")
	kids, hasKids := d.cos.GetArray(node, "Kids")
	// An explicit /Type /Page is a leaf even if it (incorrectly) carries /Kids; a node with kids is an interior
	// node; a node with neither is treated as a page (leniency for repair-recovered trees with missing /Type).
	if typ == "Page" || (!hasKids && typ != "Pages") {
		d.pages = append(d.pages, node)
		d.pageRefs = append(d.pageRefs, ref)
		return
	}
	for _, kid := range kids {
		var kidRef cos.Ref
		if r, isRef := kid.(cos.Ref); isRef {
			if visited[r] {
				continue
			}
			visited[r] = true
			kidRef = r
		}
		kidDict, ok := cos.AsDict(d.cos.Resolve(kid))
		if !ok {
			continue
		}
		d.walkPageTree(kidDict, kidRef, depth+1, visited)
	}
}
