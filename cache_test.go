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
	"bytes"
	"os"
	"testing"
)

// TestCacheBudget pins the maxCacheSize contract (M6 exit criterion "budget honored under a tiny
// maxCacheSize"): the store is a pure cache, so any budget — unlimited, comfortable, or one byte (nothing
// ever fits) — must produce byte-identical renders, and a bounded store must never hold more than its budget.
// glaive exercises all three cached kinds: parsed fonts, glyph outlines, and (across its pages) images.
func TestCacheBudget(t *testing.T) {
	buffer, err := os.ReadFile("testfiles/corpus/glaive.pdf")
	if err != nil {
		t.Fatalf("unable to read fixture: %v", err)
	}
	render := func(maxCacheSize uint64) ([]byte, *Document) {
		doc, docErr := New(buffer, maxCacheSize)
		if docErr != nil {
			t.Fatalf("New(maxCacheSize=%d): %v", maxCacheSize, docErr)
		}
		var out []byte
		for page := range 2 {
			rendered, renderErr := doc.RenderPage(page, 72, 0, "")
			if renderErr != nil {
				t.Fatalf("RenderPage(%d, maxCacheSize=%d): %v", page, maxCacheSize, renderErr)
			}
			out = append(out, rendered.Image.Pix...)
		}
		return out, doc
	}

	reference, refDoc := render(0) // Unlimited.
	defer refDoc.Release()
	if used := refDoc.eng.store.Used(); used == 0 {
		t.Errorf("unlimited store cached nothing; the store is not wired")
	}

	for _, budget := range []uint64{1, 32 << 10, 1 << 20} {
		pix, doc := render(budget)
		if !bytes.Equal(reference, pix) {
			t.Errorf("maxCacheSize=%d changed rendered output", budget)
		}
		if used, maxSize := doc.eng.store.Used(), doc.eng.store.Max(); used > maxSize {
			t.Errorf("maxCacheSize=%d: store used %d exceeds budget", budget, used)
		}
		doc.Release()
	}

	// Rendering the same page twice under the unlimited store (cache hits throughout) must also be identical.
	again, err := refDoc.RenderPage(0, 72, 0, "")
	if err != nil {
		t.Fatalf("re-render: %v", err)
	}
	if !bytes.Equal(reference[:len(again.Image.Pix)], again.Image.Pix) {
		t.Errorf("cache-hit re-render differs from first render")
	}
}
