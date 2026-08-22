// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build !goexperiment.simd

package pdfview

import (
	"reflect"
	"testing"
)

// TestUnpremultiplyWiringScalar checks that the default build renders through the scalar loop and nothing else. The
// vector kernels are not even compiled into this build, so what this really guards is the dispatch variable itself:
// it must start at the scalar implementation, since nothing here ever reassigns it.
func TestUnpremultiplyWiringScalar(t *testing.T) {
	got := reflect.ValueOf(unpremultiplyPixelsFn).Pointer()
	if want := reflect.ValueOf(unpremultiplyPixelsScalar).Pointer(); got != want {
		t.Fatalf("unpremultiplyPixelsFn is not unpremultiplyPixelsScalar (got %#x, want %#x)", got, want)
	}
}
