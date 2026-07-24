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
	"github.com/richardwilkes/pdfview/internal/imaging"
)

// maxCachedImages caps the per-Run decoded-image LRU used when no budgeted store is wired. A page drawing more distinct
// images than this still renders them all; the least-recently-used ones decode again if reused after eviction. With a
// store, its byte budget replaces this cap.
const maxCachedImages = 32

// drawImageXObject implements Do for /Subtype /Image: decode (cached by the resource's reference — in the document's
// budgeted store when wired, else per Run; failures are cached too, so a broken image is not re-decoded per draw) and
// hand the result to the device. Every failure path simply draws nothing: the page keeps rendering, blank where the
// image would be.
func (in *interp) drawImageXObject(raw cos.Object, stream *cos.Stream) {
	var img *imaging.Image
	resources := in.res[len(in.res)-1]
	if ref, isRef := raw.(cos.Ref); isRef {
		img = in.cachedImage(ref, stream, resources)
	} else {
		img = in.decodeXObject(stream, resources)
	}
	in.drawImage(img)
}

// decodeXObject decodes one image XObject, charging the work budget for the decode it performs: a page naming many
// distinct images must not turn a few bytes of content apiece into unbounded sample production (see budget.go).
// Failures draw nothing.
func (in *interp) decodeXObject(stream *cos.Stream, resources cos.Dict) *imaging.Image {
	img, _ := imaging.DecodeXObject(in.doc, stream, resources) //nolint:errcheck // Failures draw nothing.
	in.charge(imageDecodeCost(img, len(stream.Raw)))
	return img
}

// cachedImage decodes an image XObject through the active cache layer. Only the decodes it actually performs are
// charged: a cache hit did no work beyond the Do operator's own unit, which is what makes a repeatedly drawn image
// cheap.
func (in *interp) cachedImage(ref cos.Ref, stream *cos.Stream, resources cos.Dict) *imaging.Image {
	if in.st != nil {
		if v, hit := in.st.Get(imageKey{ref: ref}); hit {
			if img, isImg := v.(*imaging.Image); isImg {
				return img
			}
			return nil // Cached failure (negative entry).
		}
		img := in.decodeXObject(stream, resources)
		in.st.Put(imageKey{ref: ref}, img, imageSize(img))
		return img
	}
	if cached, seen := in.images.get(ref); seen {
		return cached
	}
	img := in.decodeXObject(stream, resources)
	in.images.put(ref, img)
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
// charging the work budget for it. Inline images have no cache — the payload is the content stream itself — so every BI
// pays, which is what bounds a stream of tiny BI operators each claiming huge dimensions.
func (in *interp) decodeInline(dict cos.Dict, payload []byte) (*imaging.Image, error) {
	img, err := imaging.DecodeInline(in.doc, dict, payload, in.res[len(in.res)-1])
	in.charge(imageDecodeCost(img, len(payload)))
	return img, err
}

// drawImage emits one decoded image to the device under the current CTM: stencils tint with the fill paint (skipped
// entirely when the fill space never marks), ordinary images carry the constant fill alpha.
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
		in.dev.FillImage(img, in.gs.ctm, in.gs.fillAlpha)
	})
}
