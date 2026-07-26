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

// The engine seam methods authenticate, outline, and links each run untrusted work through the internal engine
// (decryption plus page-tree re-parsing, outline-tree resolution, and /Annots resolution respectively). Per the
// package's "hostile input surfaces as an error, never a panic" contract, each must recover any panic and return a safe
// zero value rather than letting it escape the public API, exactly like openEngine, rasterize, and search.
//
// A nil doc is a reliable panic source: doc.Authenticate reads d.encrypted, doc.Outline reads d.cos, and doc.Links
// reads d.pages, so each dereferences the nil receiver immediately. Without the recover guards these calls crash the
// test binary; with them they return the documented zero value.

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

func TestLinksRecoversPanic(t *testing.T) {
	e := &engineDocument{} // doc is nil
	if infos := e.links(&page{number: 0}); infos != nil {
		t.Fatalf("expected nil links when the engine panics, got %+v", infos)
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
