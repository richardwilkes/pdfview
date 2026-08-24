// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build !goexperiment.simd

package render

import (
	"reflect"
	"testing"
)

// TestRenderScalarWiring checks that the default build renders through the scalar implementations. The vector kernels
// are not compiled into this build, so this guards the dispatch variables' initial values.
func TestRenderScalarWiring(t *testing.T) {
	for _, c := range []struct {
		got  any
		want any
		name string
	}{
		{got: compositeMaskSpanFn, want: compositeMaskSpanScalar, name: "compositeMaskSpanFn"},
		{got: lumaPlaneFn, want: lumaPlaneScalar, name: "lumaPlaneFn"},
		{got: allFiniteFn, want: allFiniteScalar, name: "allFiniteFn"},
	} {
		got := reflect.ValueOf(c.got).Pointer()
		if want := reflect.ValueOf(c.want).Pointer(); got != want {
			t.Errorf("%s is not its scalar implementation (got %#x, want %#x)", c.name, got, want)
		}
	}
}
