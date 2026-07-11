// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package shading will parse PDF shadings (types 1–7) into a normalized form and tessellate the mesh kinds
// (plan.md milestone M8). The Shading type is defined now so the device seam (internal/device) has its final
// method signatures from M4 on; the parser and the type's fields land with M8.
package shading

// Shading is one parsed shading dictionary in normalized form. Until M8 lands the parser, no producer exists
// and the type carries nothing; the device seam's FillShading and the Paint payload are declared against it so
// their signatures never change.
type Shading struct {
	_ struct{}
}
