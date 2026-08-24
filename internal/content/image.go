// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package content

import (
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/imaging"
)

// maxCachedImages caps the per-Run decoded-image LRU used when no budgeted store is wired (a store's byte budget
// replaces it). A page drawing more distinct images still renders them all; evicted ones decode again on reuse.
const maxCachedImages = 32

// drawImageXObject implements Do for /Subtype /Image: decode (cached by the resource's reference, in the document's
// budgeted store when wired, else per Run; failures cache too, so a broken image is not re-decoded per draw) and hand
// the result to the device. Every failure path draws nothing.
func (in *interp) drawImageXObject(raw cos.Object, stream *cos.Stream) {
	var img *imaging.Image
	resources := in.res[len(in.res)-1]
	// The cache is keyed on the reference alone, so an image whose decode also consults the resource frame in scope
	// (imaging.NeedsResources: a bare /ColorSpace name resolved through resources /ColorSpace) must not go through it:
	// two forms mapping /CS0 to different spaces would render the second image with the first's palette, and with the
	// document store wired the stale entry would cross pages. Those images decode per draw, charged as usual.
	if ref, isRef := raw.(cos.Ref); isRef && !imaging.NeedsResources(in.doc, stream.Dict) {
		img = in.cachedImage(ref, stream, resources)
	} else {
		img = in.decodeXObject(stream, resources)
	}
	in.drawImage(img)
}

// decodeXObject decodes one image XObject, charging the work budget for the decode: a page naming many distinct images
// must not turn a few bytes of content apiece into unbounded sample production (see budget.go).
func (in *interp) decodeXObject(stream *cos.Stream, resources cos.Dict) *imaging.Image {
	before := in.doc.DecodeWork()
	img, _ := imaging.DecodeXObject(in.doc, stream, resources) //nolint:errcheck // Failures draw nothing.
	in.charge(imageDecodeCost(img, in.doc.DecodeWork()-before, len(stream.Raw)))
	return img
}

// cachedImage decodes an image XObject through the active cache layer. Only the decodes it performs are charged: a
// cache hit costs nothing beyond the Do operator's own unit.
func (in *interp) cachedImage(ref cos.Ref, stream *cos.Stream, resources cos.Dict) *imaging.Image {
	key := ref.Key()
	if in.st != nil {
		if img, hit := in.st.Get[*imaging.Image](imageKey{ref: key}); hit {
			return img // A nil image is a cached failure (negative entry).
		}
		img := in.decodeXObject(stream, resources)
		in.st.Put(imageKey{ref: key}, img, imageSize(img))
		return img
	}
	if cached, seen := in.images.get(key); seen {
		return cached
	}
	img := in.decodeXObject(stream, resources)
	in.images.put(key, img)
	return img
}

// imageSize estimates a decoded image's cache footprint.
func imageSize(img *imaging.Image) uint64 {
	if img == nil {
		return 64
	}
	return uint64(len(img.Pix)) + 64
}

// decodeInline decodes one inline image against the resource frame in scope (named /CS entries resolve through it),
// charging the work budget. Inline images have no cache (the payload is the content stream itself), so every BI pays,
// which bounds a stream of tiny BI operators each claiming huge dimensions.
func (in *interp) decodeInline(dict cos.Dict, payload []byte) (*imaging.Image, error) {
	before := in.doc.DecodeWork()
	img, err := imaging.DecodeInline(in.doc, dict, payload, in.res[len(in.res)-1])
	in.charge(imageDecodeCost(img, in.doc.DecodeWork()-before, len(payload)))
	return img, err
}

// drawImage emits one decoded image to the device under the current CTM. A stencil tints with the fill paint (skipped
// when the fill space never marks). An ordinary image has no color source but its own samples, so its paint carries
// only the constant fill alpha and blend mode; a fill pattern in scope is irrelevant to it.
func (in *interp) drawImage(img *imaging.Image) {
	if img == nil || !in.gs.ctm.IsFinite() {
		return
	}
	if img.Stencil {
		if !in.marks(in.gs.fillSpace, in.gs.fillPattern) {
			return
		}
		in.masked(in.gs.fillAlpha, func() {
			in.dev.FillImageMask(img, in.gs.ctm, in.fillPaint())
		})
		return
	}
	in.masked(in.gs.fillAlpha, func() {
		in.dev.FillImage(img, in.gs.ctm, device.Paint{Alpha: in.gs.fillAlpha, Blend: in.gs.blend})
	})
}
