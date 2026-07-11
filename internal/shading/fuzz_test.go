// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package shading

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// FuzzShading drives the shading parser (and through it the mesh bit reader, tessellation, and function
// sampling) over every object of an arbitrary document, seeded with the shading corpus files. The contract is
// the usual one: hostile input may fail to parse, but must never panic, hang, or allocate unboundedly.
func FuzzShading(f *testing.F) {
	for _, name := range []string{
		"shading-axial.pdf", "shading-radial.pdf", "shading-function.pdf", "shading-mesh.pdf", "pattern-tiling.pdf",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testfiles", "corpus", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		d, err := cos.Open(data)
		if err != nil {
			return
		}
		const maxObjects = 64
		for i, num := range d.ObjectNums() {
			if i >= maxObjects {
				break
			}
			sh, parseErr := Parse(d, d.LoadObject(num))
			if parseErr != nil || sh == nil {
				continue
			}
			if len(sh.Triangles) > maxTriangles {
				panic("triangle budget exceeded")
			}
			if sh.ColorAt != nil {
				sh.ColorAt(0.5, 0.5) // The closure must be callable on any parsed shading.
			}
		}
	})
}
