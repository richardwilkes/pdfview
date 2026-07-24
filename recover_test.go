// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

import "testing"

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
