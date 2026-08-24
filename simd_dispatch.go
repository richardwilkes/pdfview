// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

// The seam every vector kernel in this package is swapped in through. Each variable names the scalar implementation,
// which a default build runs; under GOEXPERIMENT=simd, simd_on.go's init repoints it at the vector kernel when the
// machine can run it. Call sites call the variable unconditionally. Each kernel gates on length itself and falls back
// to the scalar function below its gate, so a short buffer costs one extra call.
var unpremultiplyPixelsFn = unpremultiplyPixelsScalar
