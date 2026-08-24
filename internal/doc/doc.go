// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package doc implements document-level PDF semantics on top of the COS layer: the page tree (flattened at open into
// the page list PageCount and Page answer from), encryption setup, page geometry (the effective box and rotation, and
// the top-left/y-down space derived from them), destinations (explicit arrays and named, via the old-style /Dests
// dictionary and the /Names name tree), the document outline, link annotations, and page labels.
package doc

import (
	"errors"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/crypt"
)

// Page-tree walk guards: depth is capped, a visited set shared across the walk cuts reference cycles and duplicated
// subtrees, and the leaf count is capped so a hostile tree cannot balloon memory (as maxOutlineNodes and maxPageLinks
// cap the outline and link walks). maxPages is far above any real page count; the pages past it are dropped.
const (
	maxPageTreeDepth = 64
	maxPages         = 65536
)

// Authentication-status bits, in the same layout as the root package's AuthenticationStatus, which converts the byte
// directly.
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
	// crypt is the standard security handler when the document is encrypted with a supported scheme; nil otherwise.
	crypt *crypt.Handler
	// pageIndex maps each page's indirect reference to its 0-based page number, which is how destination arrays resolve
	// their target page. It keys on cos.RefKey, so a reference with a different generation still finds the page.
	pageIndex map[cos.RefKey]int
	// destIndex is the catalog's /Names → /Dests name tree flattened to name → destination pairs, built on the first
	// named-destination lookup (see lookupNamedDest). It is nil until then; an empty non-nil map means the document has no
	// name tree. A successful Authenticate resets it: an index built before the file key arrived is keyed on the
	// ciphertext DecryptString passed through, and would miss every name for the life of the document.
	destIndex map[string]cos.Object
	// pageLabels is the /PageLabels number tree expanded to one unsanitized label per page, built on the first page-label
	// query (see PageLabels). It is nil until then and non-nil afterward, even for a zero-page document. A successful
	// Authenticate resets it: /P prefixes decoded before the file key arrived are ciphertext, and buildPageList re-runs,
	// so the page count can change.
	pageLabels []string
	// pages holds the leaf dictionaries of the page tree, in document order.
	pages []cos.Dict
	// pageRefs holds the indirect reference of each page when it was reached through one (the zero Ref otherwise);
	// pageIndex is its inverse.
	pageRefs []cos.Ref
	// geoms holds each page's effective display geometry (inherited /MediaBox ∩ /CropBox plus /Rotate), captured during
	// the page-tree walk.
	geoms []pageGeom
	// resources holds each page's (inheritable) /Resources entry, unresolved, captured during the walk.
	resources []cos.Object
	// encrypted records whether the trailer carried an /Encrypt dictionary, even if its handler is unsupported.
	encrypted bool
	// pageLabelsPresent records whether the pageLabels build found at least one range (see HasPageLabels). It is
	// meaningful only once pageLabels is non-nil.
	pageLabelsPresent bool
}

// Open parses data as a PDF document, sets up decryption if it is encrypted, and builds its page list. The COS layer
// repairs broken cross-reference data automatically; Open fails only when no usable document root exists. A catalog
// without a usable page tree opens with zero pages. An encrypted document opens whether or not a password is available:
// dictionaries stored directly in the file are not encrypted, so PageCount works before authentication unless the page
// tree sits in an object stream.
func Open(data []byte) (*Document, error) {
	c, err := cos.Open(data)
	if err != nil {
		return nil, err
	}
	d := &Document{cos: c}
	d.setupEncryption()
	// cos.Open defers its root check for an encrypted document, since a catalog stored in an object stream cannot be read
	// before the decryptor is installed. Run it now, but only for a readable document: one still waiting for a password
	// cannot resolve that catalog yet and must open locked, as an encrypted document whose catalog sits directly in the
	// file already does. Authenticate rebuilds the page list once the key arrives.
	if c.Encrypted() && !d.NeedsPassword() {
		if err = c.ValidateRoot(); err != nil {
			return nil, err
		}
	}
	d.buildPageList()
	return d, nil
}

// setupEncryption builds the security handler from the trailer's /Encrypt dictionary, if any, and installs it as the
// COS layer's decryptor; crypt.New tries the empty password, so documents that need none are immediately usable. An
// /Encrypt dictionary the handler cannot parse leaves the document flagged encrypted but locked.
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

// NeedsPassword reports whether a password must be supplied before the document's encrypted content can be read. It is
// false for unencrypted documents and for encrypted documents the empty password already unlocked.
func (d *Document) NeedsPassword() bool {
	if d.crypt == nil {
		return d.encrypted // An unsupported handler stays locked.
	}
	return d.crypt.NeedsPassword()
}

// Authenticate tries password against the document and returns the status bits (AuthNoneRequired / AuthUser /
// AuthOwner), matching MuPDF's fz_authenticate_password. An unencrypted document reports AuthNoneRequired for any
// password. A success drops the object cache so objects read before the file key was available are reparsed and
// decrypted.
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
		// The file key is now available: drop everything cached without it (nothing gates named-destination or page-label
		// queries on authentication, so destIndex and pageLabels may hold ciphertext) and rewalk the page tree so its
		// dictionaries are recaptured decrypted. Re-run the deferred root check first: a catalog stored in an object stream
		// was unreadable until now, and a repair sweep provoked while its payload was ciphertext rebuilt the cross-reference
		// table without the objects inside it, which no later load notices (an absent entry resolves to Null, not an error).
		d.cos.DropCaches()
		d.destIndex = nil
		d.pageLabels = nil
		d.pageLabelsPresent = false
		d.cos.ValidateRoot() //nolint:errcheck // A root that is still unusable yields an empty page list below.
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

// PageRef returns the indirect reference through which the given 0-based page was reached, or the zero Ref when the
// page dictionary was inlined directly in its parent's /Kids. Destination resolution matches pages by this identity.
func (d *Document) PageRef(pageNumber int) (cos.Ref, error) {
	if pageNumber < 0 || pageNumber >= len(d.pageRefs) {
		return cos.Ref{}, errNoSuchPage
	}
	return d.pageRefs[pageNumber], nil
}

// buildPageList walks the page tree from the catalog, collecting leaves in document order. It counts actual leaves
// rather than trusting /Count, which repair-recovered and hostile files get wrong, and resets its output first so it
// can be re-run after a successful authentication.
func (d *Document) buildPageList() {
	d.pages = nil
	d.pageRefs = nil
	d.geoms = nil
	d.resources = nil
	d.pageIndex = make(map[cos.RefKey]int)
	root, ok := d.cos.GetDict(d.cos.Trailer(), "Root")
	if !ok {
		return
	}
	pagesObj := root["Pages"]
	visited := make(map[cos.RefKey]bool)
	var pagesRef cos.Ref
	if ref, isRef := pagesObj.(cos.Ref); isRef {
		visited[ref.Key()] = true
		pagesRef = ref
	}
	node, ok := cos.AsDict(d.cos.Resolve(pagesObj))
	if !ok {
		return
	}
	d.walkPageTree(node, pagesRef, 0, visited, inheritedAttrs{})
}

func (d *Document) walkPageTree(node cos.Dict, ref cos.Ref, depth int, visited map[cos.RefKey]bool, attrs inheritedAttrs) {
	if depth > maxPageTreeDepth || len(d.pages) >= maxPages {
		return
	}
	attrs = attrs.override(node)
	typ, _ := d.cos.GetName(node, "Type")
	kids, hasKids := d.cos.GetArray(node, "Kids")
	// An explicit /Type /Page is a leaf even with a stray /Kids; a node with neither /Kids nor /Type /Pages is a page too,
	// since repair-recovered trees lose /Type.
	if typ == "Page" || (!hasKids && typ != "Pages") {
		if ref != (cos.Ref{}) {
			d.pageIndex[ref.Key()] = len(d.pages)
		}
		d.pages = append(d.pages, node)
		d.pageRefs = append(d.pageRefs, ref)
		d.geoms = append(d.geoms, d.resolveGeom(attrs))
		d.resources = append(d.resources, attrs.resources)
		return
	}
	for _, kid := range kids {
		if len(d.pages) >= maxPages {
			return
		}
		var kidRef cos.Ref
		if r, isRef := kid.(cos.Ref); isRef {
			if visited[r.Key()] {
				continue
			}
			visited[r.Key()] = true
			kidRef = r
		}
		kidDict, ok := cos.AsDict(d.cos.Resolve(kid))
		if !ok {
			continue
		}
		d.walkPageTree(kidDict, kidRef, depth+1, visited, attrs)
	}
}

// PageSize returns the given 0-based page's displayed width and height in PDF points: the extent of its effective box
// (inherited /MediaBox ∩ /CropBox) after /Rotate is applied, so 90/270 rotations swap the axes.
func (d *Document) PageSize(pageNumber int) (width, height float32, err error) {
	if pageNumber < 0 || pageNumber >= len(d.geoms) {
		return 0, 0, errNoSuchPage
	}
	width, height = d.geoms[pageNumber].displaySize()
	return width, height, nil
}
