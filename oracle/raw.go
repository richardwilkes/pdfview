// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package main

// This file extracts the raw, unscaled values that the public API of github.com/richardwilkes/pdf never exposes
// (page-space floats for outline destinations, link rectangles, and search quads, plus the MuPDF version), by calling
// MuPDF directly through the same vendored headers and static libraries the binding uses. The wrapper pattern (run
// every throwing MuPDF call inside fz_try/fz_catch) is adapted from that binding's own preamble.

/*
#cgo CFLAGS: -I${SRCDIR}/../../pdf/include
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/../../pdf/lib -lmupdf_darwin_amd64 -lm
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/../../pdf/lib -lmupdf_darwin_arm64 -lm
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../pdf/lib -lmupdf_linux_amd64 -lm
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/../../pdf/lib -lmupdf_linux_arm64 -lm
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../pdf/lib -lmupdf_windows_amd64 -lm -Wl,--allow-multiple-definition
#cgo windows,arm64 LDFLAGS: -L${SRCDIR}/../../pdf/lib -lmupdf_windows_arm64 -lm -Wl,--allow-multiple-definition

#include <stdlib.h>
#include <mupdf/fitz.h>

const char *oracle_fz_version(void) {
	return FZ_VERSION;
}

fz_context *oracle_fz_new_context(size_t max_store) {
	return fz_new_context(NULL, NULL, max_store);
}

int oracle_fz_register_document_handlers(fz_context *ctx) {
	int ok = 0;
	fz_var(ok);
	fz_try(ctx) {
		fz_register_document_handlers(ctx);
		ok = 1;
	}
	fz_catch(ctx) {
		ok = 0;
	}
	return ok;
}

fz_stream *oracle_fz_open_memory(fz_context *ctx, const unsigned char *data, size_t len) {
	fz_stream *stream = NULL;
	fz_var(stream);
	fz_try(ctx) {
		stream = fz_open_memory(ctx, data, len);
	}
	fz_catch(ctx) {
		stream = NULL;
	}
	return stream;
}

fz_document *oracle_fz_open_pdf_document_with_stream(fz_context *ctx, fz_stream *stream) {
	fz_document *doc = NULL;
	fz_var(doc);
	fz_try(ctx) {
		doc = fz_open_document_with_stream(ctx, "application/pdf", stream);
	}
	fz_catch(ctx) {
		doc = NULL;
	}
	return doc;
}

int oracle_fz_authenticate_password(fz_context *ctx, fz_document *doc, const char *password) {
	int result = 0;
	fz_var(result);
	fz_try(ctx) {
		result = fz_authenticate_password(ctx, doc, password);
	}
	fz_catch(ctx) {
		result = 0;
	}
	return result;
}

fz_page *oracle_fz_load_page(fz_context *ctx, fz_document *doc, int number) {
	fz_page *page = NULL;
	fz_var(page);
	fz_try(ctx) {
		page = fz_load_page(ctx, doc, number);
	}
	fz_catch(ctx) {
		page = NULL;
	}
	return page;
}

fz_rect oracle_fz_bound_page(fz_context *ctx, fz_page *page) {
	fz_rect rect = fz_empty_rect;
	fz_var(rect);
	fz_try(ctx) {
		rect = fz_bound_page(ctx, page);
	}
	fz_catch(ctx) {
		rect = fz_empty_rect;
	}
	return rect;
}

fz_outline *oracle_fz_load_outline(fz_context *ctx, fz_document *doc) {
	fz_outline *outline = NULL;
	fz_var(outline);
	fz_try(ctx) {
		outline = fz_load_outline(ctx, doc);
	}
	fz_catch(ctx) {
		outline = NULL;
	}
	return outline;
}

fz_link *oracle_fz_load_links(fz_context *ctx, fz_page *page) {
	fz_link *links = NULL;
	fz_var(links);
	fz_try(ctx) {
		links = fz_load_links(ctx, page);
	}
	fz_catch(ctx) {
		links = NULL;
	}
	return links;
}

int oracle_fz_is_external_link(fz_context *ctx, const char *uri) {
	int result = 0;
	if (uri == NULL) {
		return 0;
	}
	fz_var(result);
	fz_try(ctx) {
		result = fz_is_external_link(ctx, uri);
	}
	fz_catch(ctx) {
		result = 0;
	}
	return result;
}

typedef struct {
	int page;
	float x;
	float y;
} oracle_resolved_link;

// Resolves an internal link URI to a 0-based page number and a destination point on that page. page is -1 (and the
// point 0,0) if uri is NULL, it cannot be resolved, or it threw. When the destination carries no explicit
// coordinate (e.g. a /Fit destination) MuPDF leaves x/y non-finite (NaN); the Go caller preserves that as null.
oracle_resolved_link oracle_fz_resolve_link(fz_context *ctx, fz_document *doc, const char *uri) {
	oracle_resolved_link r = { -1, 0, 0 };
	if (uri == NULL) {
		return r;
	}
	fz_var(r);
	fz_try(ctx) {
		float x = 0, y = 0;
		fz_location loc = fz_resolve_link(ctx, doc, uri, &x, &y);
		r.page = fz_page_number_from_location(ctx, doc, loc);
		r.x = x;
		r.y = y;
	}
	fz_catch(ctx) {
		r.page = -1;
		r.x = 0;
		r.y = 0;
	}
	return r;
}

fz_display_list *oracle_fz_new_display_list_from_page(fz_context *ctx, fz_page *page) {
	fz_display_list *list = NULL;
	fz_var(list);
	fz_try(ctx) {
		list = fz_new_display_list_from_page(ctx, page);
	}
	fz_catch(ctx) {
		list = NULL;
	}
	return list;
}

int oracle_fz_search_display_list(fz_context *ctx, fz_display_list *list, const char *needle, fz_quad *hit_bbox, int hit_max) {
	int hits = 0;
	fz_var(hits);
	fz_try(ctx) {
		hits = fz_search_display_list(ctx, list, needle, NULL, hit_bbox, hit_max);
	}
	fz_catch(ctx) {
		hits = 0;
	}
	return hits;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"unsafe"
)

// rawSearchMax caps the number of quads a single raw search can return, mirroring the binding's OverallMaxHits default.
const rawSearchMax = 1000

// mupdfVersion returns the FZ_VERSION string of the linked MuPDF build.
func mupdfVersion() string {
	return C.GoString(C.oracle_fz_version())
}

// rawDoc is a document opened directly against MuPDF for raw dumping. It is independent of any document the binding has
// open.
type rawDoc struct {
	ctx  *C.fz_context
	doc  *C.fz_document
	data unsafe.Pointer
}

// openRaw opens the document and, when password is non-empty, authenticates with it. Documents that need no password
// (including encrypted documents with an empty user password, which MuPDF auto-authenticates at open) must pass
// password as "".
func openRaw(buffer []byte, password string) (*rawDoc, error) {
	r := &rawDoc{ctx: C.oracle_fz_new_context(0)}
	if r.ctx == nil {
		return nil, errors.New("raw: unable to create context")
	}
	if C.oracle_fz_register_document_handlers(r.ctx) == 0 {
		r.close()
		return nil, errors.New("raw: unable to register document handlers")
	}
	r.data = C.CBytes(buffer)
	stream := C.oracle_fz_open_memory(r.ctx, (*C.uchar)(r.data), C.size_t(len(buffer)))
	if stream == nil {
		r.close()
		return nil, errors.New("raw: unable to open memory stream")
	}
	r.doc = C.oracle_fz_open_pdf_document_with_stream(r.ctx, stream)
	C.fz_drop_stream(r.ctx, stream)
	if r.doc == nil {
		r.close()
		return nil, errors.New("raw: unable to open document")
	}
	if password != "" {
		pw := C.CString(password)
		defer C.free(unsafe.Pointer(pw))
		if C.oracle_fz_authenticate_password(r.ctx, r.doc, pw) == 0 {
			r.close()
			return nil, errors.New("raw: authentication failed")
		}
	}
	return r, nil
}

func (r *rawDoc) close() {
	if r.doc != nil {
		C.fz_drop_document(r.ctx, r.doc)
		r.doc = nil
	}
	if r.data != nil {
		C.free(r.data)
		r.data = nil
	}
	if r.ctx != nil {
		C.fz_drop_context(r.ctx)
		r.ctx = nil
	}
}

// outlineTree returns the document outline as raw entries, or nil when there is none.
func (r *rawDoc) outlineTree() []*TOCRawEntry {
	outline := C.oracle_fz_load_outline(r.ctx, r.doc)
	if outline == nil {
		return nil
	}
	defer C.fz_drop_outline(r.ctx, outline)
	return convertOutline(outline)
}

func convertOutline(outline *C.fz_outline) []*TOCRawEntry {
	var entries []*TOCRawEntry
	for ; outline != nil; outline = outline.next {
		entry := &TOCRawEntry{
			Title: goStringOrEmpty(outline.title),
			URI:   goStringOrEmpty(outline.uri),
			Page:  int(outline.page.page),
			X:     finiteOrNull(float32(outline.x)),
			Y:     finiteOrNull(float32(outline.y)),
		}
		if outline.down != nil {
			entry.Children = convertOutline(outline.down)
		}
		entries = append(entries, entry)
	}
	return entries
}

// pageRaw loads the 0-based page and returns its raw bounds, links, and per-needle search quads.
func (r *rawDoc) pageRaw(pageNumber int, needles []string) (bounds [4]float32, links []*RawLink, search map[string][][8]float32, err error) {
	page := C.oracle_fz_load_page(r.ctx, r.doc, C.int(pageNumber))
	if page == nil {
		return bounds, nil, nil, fmt.Errorf("raw: unable to load page %d", pageNumber)
	}
	defer C.fz_drop_page(r.ctx, page)
	rect := C.oracle_fz_bound_page(r.ctx, page)
	bounds = [4]float32{finite32(float32(rect.x0)), finite32(float32(rect.y0)), finite32(float32(rect.x1)), finite32(float32(rect.y1))}
	links = r.rawLinks(page)
	if len(needles) > 0 {
		if search, err = r.rawSearch(page, needles); err != nil {
			return bounds, nil, nil, fmt.Errorf("page %d: %w", pageNumber, err)
		}
	}
	return bounds, links, search, nil
}

// rawLinks returns every link on the page, unfiltered.
func (r *rawDoc) rawLinks(page *C.fz_page) []*RawLink {
	links := make([]*RawLink, 0)
	if link := C.oracle_fz_load_links(r.ctx, page); link != nil {
		firstLink := link
		for ; link != nil; link = link.next {
			rawLink := &RawLink{
				URI:      goStringOrEmpty(link.uri),
				External: C.oracle_fz_is_external_link(r.ctx, link.uri) != 0,
				Rect: [4]float32{
					finite32(float32(link.rect.x0)), finite32(float32(link.rect.y0)),
					finite32(float32(link.rect.x1)), finite32(float32(link.rect.y1)),
				},
				Page: -1,
			}
			if !rawLink.External {
				res := C.oracle_fz_resolve_link(r.ctx, r.doc, link.uri)
				rawLink.Page = int(res.page)
				rawLink.DestX = finiteOrNull(float32(res.x))
				rawLink.DestY = finiteOrNull(float32(res.y))
			}
			links = append(links, rawLink)
		}
		C.fz_drop_link(r.ctx, firstLink)
	}
	return links
}

// rawSearch returns the raw hit quads for each needle, searching the page's display list exactly as the binding does.
func (r *rawDoc) rawSearch(page *C.fz_page, needles []string) (map[string][][8]float32, error) {
	list := C.oracle_fz_new_display_list_from_page(r.ctx, page)
	if list == nil {
		return nil, errors.New("raw: unable to build display list")
	}
	defer C.fz_drop_display_list(r.ctx, list)
	search := make(map[string][][8]float32, len(needles))
	quads := make([]C.fz_quad, rawSearchMax)
	for _, needle := range needles {
		cNeedle := C.CString(needle)
		hits := int(C.oracle_fz_search_display_list(r.ctx, list, cNeedle, &quads[0], C.int(len(quads))))
		C.free(unsafe.Pointer(cNeedle))
		result := make([][8]float32, 0, hits)
		for i := range hits {
			q := quads[i]
			result = append(result, [8]float32{
				finite32(float32(q.ul.x)), finite32(float32(q.ul.y)),
				finite32(float32(q.ur.x)), finite32(float32(q.ur.y)),
				finite32(float32(q.ll.x)), finite32(float32(q.ll.y)),
				finite32(float32(q.lr.x)), finite32(float32(q.lr.y)),
			})
		}
		search[needle] = result
	}
	return search, nil
}

func goStringOrEmpty(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

// finiteOrNull returns a pointer to v, or nil when v is not finite (MuPDF's encoding for "no explicit coordinate"), so
// it marshals to JSON null.
func finiteOrNull(v float32) *float32 {
	if f := float64(v); math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &v
}

// finite32 maps non-finite values to 0 so they can be marshaled; rectangle and quad geometry from MuPDF is expected to
// always be finite, this is only a safeguard.
func finite32(v float32) float32 {
	if f := float64(v); math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return v
}
