// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd && !amd64 && !arm64

package render

// Which of this package's vector kernels are worth dispatching to on an architecture nobody has benchmarked. None: the
// simd package's vector types exist on every port under the experiment, but until a kernel is measured there is no
// evidence it beats the scalar code, which is what this package's rendering is pinned against.
const (
	preferCompositeMask = false
	preferMaskLuma      = false
	preferAllFinite     = false
)
