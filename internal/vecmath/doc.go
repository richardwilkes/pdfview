// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package vecmath holds the small arithmetic helpers shared by this module's goexperiment.simd vector kernels.
//
// Everything with an implementation lives in files guarded by //go:build goexperiment.simd, since the standard
// library's simd package only exists under that experiment. This file carries no build tag so that the package
// always has at least one Go source file and the default (non-experiment) build of ./... still succeeds; it
// deliberately declares nothing.
//
// The helpers here are the pieces every kernel needs and nobody should re-derive: reciprocal-multiply division by
// the alpha (255) and luminosity-weight (252) denominators, each backed by an exhaustive proof over its documented
// input domain in vecmath_test.go. That test file also pins the simd semantics the kernels are designed around —
// wrapping (not saturating) 8-bit adds, arithmetic right shifts on signed lanes, truncate-toward-zero float
// conversion, and little-endian lane order across reshapes — so a change in those semantics fails here loudly
// instead of quietly corrupting pixels somewhere downstream.
package vecmath
