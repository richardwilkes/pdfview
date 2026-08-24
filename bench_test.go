// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview_test

import (
	"os"
	"testing"

	"github.com/richardwilkes/pdfview"
)

// BenchmarkRenderGlaive150 measures warm renders of glaive's pages at 150 dpi with no search, the protocol the cgo
// baseline (github.com/richardwilkes/pdf) was measured under.
func BenchmarkRenderGlaive150(b *testing.B) {
	data, err := os.ReadFile("testfiles/corpus/glaive.pdf")
	if err != nil {
		b.Fatal(err)
	}
	doc, err := pdfview.New(data, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer doc.Release()
	for _, page := range []int{0, 1} {
		b.Run("page"+string(rune('0'+page)), func(b *testing.B) {
			// Warm the per-document caches before timing.
			if _, err = doc.RenderPage(page, 150, 0, ""); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for b.Loop() {
				if _, err = doc.RenderPage(page, 150, 0, ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
