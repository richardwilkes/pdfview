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

// The engine seams authenticate, outline, links, and the page-label pair run untrusted work (decryption plus page-tree
// re-parsing, outline resolution, /Annots resolution, /PageLabels number-tree flattening). Each must recover a panic
// and return a safe zero value, like openEngine, rasterize, and search. A nil doc is a reliable panic source: every one
// of the doc methods dereferences the receiver immediately.

func TestAuthenticateRecoversPanic(t *testing.T) {
	e := &engineDocument{}
	if status := e.authenticate("secret"); status != 0 {
		t.Fatalf("expected zero status when the engine panics, got %d", status)
	}
}

func TestOutlineRecoversPanic(t *testing.T) {
	e := &engineDocument{}
	if root := e.outline(); root != nil {
		t.Fatalf("expected nil outline when the engine panics, got %+v", root)
	}
}

func TestPageLabelsRecoversPanic(t *testing.T) {
	e := &engineDocument{}
	if labels := e.pageLabels(); labels != nil {
		t.Fatalf("expected nil labels when the engine panics, got %+v", labels)
	}
	if has := e.hasPageLabels(); has {
		t.Fatal("expected false when the engine panics, got true")
	}
}

func TestLinksRecoversPanic(t *testing.T) {
	e := &engineDocument{}
	if infos := e.links(&page{number: 0}); infos != nil {
		t.Fatalf("expected nil links when the engine panics, got %+v", infos)
	}
}

// TestExtractTextRecoversPanic covers the one seam whose safe value is not nil: a panic must still produce a usable
// empty TextPage, reported without an error, because a mid-page failure is when the characters recorded so far are
// worth returning. A page the engine cannot read at all is TestExtractTextReportsAnUnreadablePage's case.
func TestExtractTextRecoversPanic(t *testing.T) {
	e := &engineDocument{}
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

// TestExtractTextReportsAnUnreadablePage pins that a page whose geometry the engine cannot read (a page number past the
// end, which PageCTM refuses) is an error a viewer can act on, not an empty page.
func TestExtractTextReportsAnUnreadablePage(t *testing.T) {
	d := openInternal(t, "text-std14.pdf")
	text, err := d.eng.extractText(&page{number: 1 << 20})
	if !errors.Is(err, ErrUnableToLoadPage) {
		t.Fatalf("error = %v, want ErrUnableToLoadPage", err)
	}
	if text != nil {
		t.Errorf("a failed extraction returned a page as well as its error: %+v", text)
	}
	// The public entry point rejects such a page number before the engine sees it.
	if _, err = d.TextPage(1<<20, 72); !errors.Is(err, ErrInvalidPageNumber) {
		t.Errorf("TextPage error = %v, want ErrInvalidPageNumber", err)
	}
}

// TestDrawPageRecoversSetupPanic pins that drawPage's guard covers its setup: render.Wrap, PageCTM, and the matrix
// composition can panic (a released canvas in Wrap; a nil engine document in PageCTM, the stand-in here) and must
// surface as ErrInternal. The canvas must come back untouched, since nothing was saved before the panic.
func TestDrawPageRecoversSetupPanic(t *testing.T) {
	surf := surface.NewRasterN32Premul(8, 8, nil)
	if surf == nil {
		t.Fatal("unable to create surface")
	}
	c := surf.Canvas()
	saves := c.SaveCount()
	e := &engineDocument{} // PageCTM panics after render.Wrap has succeeded.
	if err := e.drawPage(c, &page{number: 0}, geom.IdentityMatrix()); !errors.Is(err, ErrInternal) {
		t.Fatalf("expected ErrInternal when the setup panics, got %v", err)
	}
	if got := c.SaveCount(); got != saves {
		t.Fatalf("drawPage left the canvas save count at %d, want %d", got, saves)
	}
}
