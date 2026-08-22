// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build !goexperiment.simd

package wavelet

import (
	"reflect"
	"testing"
)

// TestScalarWiring locks that the default build's dispatch variables still point at the scalar sweeps. Nothing in
// this build should be able to reach a vector kernel — simd_on.go is not compiled into it at all — and this is the
// test that says so out loud. The experiment build's TestSIMDWiring is the mirror of it.
func TestScalarWiring(t *testing.T) {
	for name, pair := range map[string][2]any{
		"sub53SweepFn":   {sub53SweepFn, sub53SweepScalar},
		"add53SweepFn":   {add53SweepFn, add53SweepScalar},
		"scale97SweepFn": {scale97SweepFn, scale97SweepScalar},
	} {
		if reflect.ValueOf(pair[0]).Pointer() != reflect.ValueOf(pair[1]).Pointer() {
			t.Fatalf("%s: dispatch fn is not the scalar implementation", name)
		}
	}
}
