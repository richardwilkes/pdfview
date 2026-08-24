// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build !goexperiment.simd

package jbig2

import (
	"reflect"
	"testing"
)

// TestDispatchIsScalarWithoutExperiment pins the default build's promise: with no goexperiment.simd, nothing exists to
// repoint the dispatch variables, so they hold the scalar functions. TestKernelsWiredWithExperiment in
// simd_equiv_test.go pins the other direction.
func TestDispatchIsScalarWithoutExperiment(t *testing.T) {
	for _, entry := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{
			name: "composeBytesFn",
			got:  reflect.ValueOf(composeBytesFn).Pointer(),
			want: reflect.ValueOf(composeBytes).Pointer(),
		},
		{
			name: "composeShiftedRunFn",
			got:  reflect.ValueOf(composeShiftedRunFn).Pointer(),
			want: reflect.ValueOf(composeShiftedRunScalar).Pointer(),
		},
	} {
		if entry.got != entry.want {
			t.Errorf("%s does not hold its scalar default in a build without goexperiment.simd", entry.name)
		}
	}
}
