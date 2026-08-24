// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// This file is pdfview-authored (MPL-2.0), not upstream code. Vendoring removed the image.RegisterFormat init functions
// from j2k and jp2, since a library must not mutate the process-global image registry; the upstream end-to-end tests
// reach the decoders through image.Decode, so the registration is restored here, scoped to the test binary.

package e2e_test

import (
	"image"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/jp2"
)

func init() {
	image.RegisterFormat("j2k", "\xff\x4f", j2k.Decode, j2k.DecodeConfig)
	// JP2 Signature Box: 00 00 00 0C 6A 50 20 20
	image.RegisterFormat("jp2", "\x00\x00\x00\x0c\x6a\x50\x20\x20", jp2.Decode, jp2.DecodeConfig)
}
