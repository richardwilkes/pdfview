// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

import (
	"errors"
	"testing"

	"github.com/richardwilkes/canvas/geom"
	"github.com/richardwilkes/canvas/surface"
)

// The engine seam methods authenticate, outline, links, and the page-label pair each run untrusted work through the
// internal engine (decryption plus page-tree re-parsing, outline-tree resolution, /Annots resolution, and /PageLabels
// number-tree flattening respectively). Per the package's "hostile input surfaces as an error, never a panic" contract,
// each must recover any panic and return a safe zero value rather than letting it escape the public API, exactly like
// openEngine, rasterize, and search.
//
// A nil doc is a reliable panic source: doc.Authenticate reads d.encrypted, doc.Outline reads d.cos, doc.Links reads
// d.pages, and doc.PageLabels reads d.pageLabels and then d.cos, so each dereferences the nil receiver immediately.
// Without the recover guards these calls crash the test binary; with them they return the documented zero value.

func TestAuthenticateRecoversPanic(t *testing.T) {
	e := &engineDocument{} // doc is nil
	if status := e.authenticate("secret"); status != 0 {
		t.Fatalf("expected zero status when the engine panics, got %d", status)
	}
}

func TestOutlineRecoversPanic(t *testing.T) {
	e := &engineDocument{} // doc is nil
	if root := e.outline(); root != nil {
		t.Fatalf("expected nil outline when the engine panics, got %+v", root)
	}
}

func TestPageLabelsRecoversPanic(t *testing.T) {
	e := &engineDocument{} // doc is nil
	if labels := e.pageLabels(); labels != nil {
		t.Fatalf("expected nil labels when the engine panics, got %+v", labels)
	}
	if has := e.hasPageLabels(); has {
		t.Fatal("expected false when the engine panics, got true")
	}
}

func TestLinksRecoversPanic(t *testing.T) {
	e := &engineDocument{} // doc is nil
	if infos := e.links(&page{number: 0}); infos != nil {
		t.Fatalf("expected nil links when the engine panics, got %+v", infos)
	}
}

// TestExtractTextRecoversPanic covers the one seam whose safe value is not a nil: TextPage hands its caller a *TextPage
// whose methods index into this page, so a panic must still produce a usable empty page rather than a nil one — and
// rather than the panic itself, since a mid-page failure is precisely when the characters recorded so far are worth
// returning. A panic is reported as no error for that reason, unlike a page the engine cannot read at all, which
// TestExtractTextReportsAnUnreadablePage covers. doc.PageCTM dereferences the nil document immediately, before
// anything has been recorded.
func TestExtractTextRecoversPanic(t *testing.T) {
	e := &engineDocument{} // doc is nil
	text, err := e.extractText(&page{number: 0})
	if err != nil {
		t.Fatalf("expected no error when the engine panics, got %v", err)
	}
	if text == nil {
		t.Fatal("expected an empty page when the engine panics, got nil")
	}
	if got := text.Len(); got != 0 {
		t.Fatalf("expected no characters when the engine panics, got %d", got)
	}
	if got := text.Text(0, 10); got != "" {
		t.Fatalf("expected no text when the engine panics, got %q", got)
	}
	if got := text.Quads(0, 10); got != nil {
		t.Fatalf("expected no quads when the engine panics, got %+v", got)
	}
}

// TestExtractTextReportsAnUnreadablePage pins the other half of that contract: a page whose geometry the engine cannot
// read is a failure a viewer can act on, not an empty page it would show as having no text. The page number is past
// the end of a real document, which is what PageCTM refuses.
func TestExtractTextReportsAnUnreadablePage(t *testing.T) {
	d := openInternal(t, "text-std14.pdf")
	text, err := d.eng.extractText(&page{number: 1 << 20})
	if !errors.Is(err, ErrUnableToLoadPage) {
		t.Fatalf("error = %v, want ErrUnableToLoadPage", err)
	}
	if text != nil {
		t.Errorf("a failed extraction returned a page as well as its error: %+v", text)
	}
	// The public entry point cannot be given such a page number, but it reports the same error when the engine's own
	// read fails, and it never hands back a half-built TextPage alongside one.
	if _, err = d.TextPage(1<<20, 72); !errors.Is(err, ErrInvalidPageNumber) {
		t.Errorf("TextPage error = %v, want ErrInvalidPageNumber", err)
	}
}

// TestDrawPageRecoversSetupPanic pins the placement of drawPage's guard. DrawPage's contract is that every failure
// surfaces as an error, but the setup it runs before drawing anything — wrapping the caller's canvas, reading the page
// CTM, composing the matrices — can itself panic: a canvas whose surface the caller already released panics inside
// render.Wrap, and a nil engine document panics in PageCTM (the reliable stand-in used here). With the guard installed
// after that setup, such a panic escaped to the caller instead of becoming ErrInternal. The canvas must come back
// untouched, since nothing was saved before the panic.
func TestDrawPageRecoversSetupPanic(t *testing.T) {
	surf := surface.NewRasterN32Premul(8, 8, nil)
	if surf == nil {
		t.Fatal("unable to create surface")
	}
	c := surf.Canvas()
	saves := c.SaveCount()
	e := &engineDocument{} // doc is nil: PageCTM panics after render.Wrap has succeeded.
	if err := e.drawPage(c, &page{number: 0}, geom.IdentityMatrix()); !errors.Is(err, ErrInternal) {
		t.Fatalf("expected ErrInternal when the setup panics, got %v", err)
	}
	if got := c.SaveCount(); got != saves {
		t.Fatalf("drawPage left the canvas save count at %d, want %d", got, saves)
	}
}
