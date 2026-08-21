// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build !goexperiment.simd

package filter

import (
	"reflect"
	"testing"
)

// TestScalarWiring locks that the default build dispatches to the scalar implementations. Nothing repoints these
// variables without the experiment — simd_on.go's init is the only thing that does, and it is not compiled here — so
// a failure means someone wired a kernel in unconditionally and the pure-Go build stopped being pure.
func TestScalarWiring(t *testing.T) {
	for name, pair := range map[string][2]func(dst, src []byte){
		"addRows": {addRowsFn, addRowsScalar},
	} {
		if reflect.ValueOf(pair[0]).Pointer() != reflect.ValueOf(pair[1]).Pointer() {
			t.Fatalf("%s: dispatch fn is not the scalar implementation", name)
		}
	}
}
