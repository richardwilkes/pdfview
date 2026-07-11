// Package doc implements document-level PDF semantics on top of the COS layer. At milestone M1 that is the page
// tree: opening a document builds the flat page list (honoring the tree structure with cycle and depth guards)
// that PageCount and Page answer from. Destinations, outlines, and link annotations arrive at M3.
package doc

import (
	"errors"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// maxPageTreeDepth caps page-tree recursion; combined with the visited set it guarantees the walk terminates on
// hostile or cyclic trees (see plan.md "Resource limits & robustness").
const maxPageTreeDepth = 64

var errNoSuchPage = errors.New("no such page")

// Document is one open PDF document.
type Document struct {
	cos *cos.Document
	// pages holds the leaf dictionaries of the page tree, in document order.
	pages []cos.Dict
	// pageRefs holds the indirect reference of each page when it was reached through one (the zero Ref
	// otherwise); M3's destination resolution needs page-object identity.
	pageRefs []cos.Ref
}

// Open parses data as a PDF document and builds its page list. The COS layer runs its repair scan automatically
// when the file's cross-reference data is broken; Open fails only when no usable document root can be found at
// all. A document whose catalog has no usable page tree opens with zero pages.
func Open(data []byte) (*Document, error) {
	c, err := cos.Open(data)
	if err != nil {
		return nil, err
	}
	d := &Document{cos: c}
	d.buildPageList()
	return d, nil
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
// legitimate tree never revisits a node), and depth is capped by maxPageTreeDepth.
func (d *Document) buildPageList() {
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
