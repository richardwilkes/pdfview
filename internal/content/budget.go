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
	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// The work budget (interp.budget, seeded with maxTotalOps) is charged for the work an operator TRIGGERS, not merely for
// dispatching it. Charging dispatch alone is not a bound: a page whose content is nothing but repeated Do operators
// costs one unit per invocation while each invocation re-decodes and re-scans the form's whole body, so a few tens of
// kilobytes of input buy hours of work. Every site that runs another stream's body — form XObjects, soft-mask groups,
// Type 3 charprocs, tiling-pattern cells — therefore charges bodyCost for that body on every invocation, and every
// resource parse an operator can force charges a flat cost. Image decodes — inline images and image XObjects — likewise
// charge imageDecodeCost for the samples they produce, which the dictionary's dimensions dictate rather than the
// payload's size. The reference-keyed per-Run caches (runCaches) make the repeat cheap on top of being bounded: the same
// form invoked twice decodes once, and a parsed color space, shading or pattern survives the fresh resource frame each
// form invocation pushes.

const (
	// bodyCostShift scales a stream body's length into budget units. exec scans the whole body on every invocation (and
	// the decode that produced it is at least that much work), while ordinary content averages well under 16 bytes per
	// operator — so len>>4 charges a real body about what executing it costs, and charges padding that executes nothing
	// for the scan it forces.
	bodyCostShift = 4
	// resourceParseCost is the flat charge for parsing one color space, pattern, or soft mask: each resolves a
	// dictionary chain and may decode a stream of its own (lookup tables and tiling/mask bodies are charged on top).
	resourceParseCost = 1 << 10
	// shadingParseCost is the flat charge for parsing one shading or sampling one transfer function: both evaluate a PDF
	// function 256 times, and a type 4 function's evaluations are bounded but far from free, so they cost several
	// resource parses.
	shadingParseCost = 1 << 12
	// imagePixelCostShift scales a decoded image's pixel count into budget units. A decode touches every sample it
	// produces — unpacking, color conversion, mask compositing — and the sample count is bounded only by
	// imaging.maxPixelsFor(payload), whose floor (2^22 pixels) is independent of how small the payload is: charging by
	// payload length alone would leave a stream of one-byte inline images claiming 2048x2048 dimensions essentially free.
	// At >>6, the whole budget buys roughly 64 decodes at that floor, while a page's worth of legitimate photographs
	// costs a small fraction of it.
	imagePixelCostShift = 6
	// maxCachedBodies and maxCachedBodyBytes bound the per-Run decoded-body cache: at most this many bodies, none larger
	// than the byte cap. An oversized body decodes again on reuse rather than pinning tens of megabytes for the whole
	// Run — the budget charge, not the cache, is what bounds re-decoding it.
	maxCachedBodies    = 16
	maxCachedBodyBytes = 1 << 20
)

// cachedBody is one decoded stream body plus whether its decode succeeded, so a failed decode caches too (a broken
// stream must not be re-decoded on every invocation).
type cachedBody struct {
	data []byte
	ok   bool
}

// runCaches are the reference-keyed parse caches of one Run, shared by every interpreter that Run spawns (the child
// interpreter a tiling pattern's replay creates shares its parent's, like the cycle set and the budget). The per-frame
// maps in resFrame remain the name-keyed layer: names are scoped to a resource frame, references are not.
type runCaches struct {
	bodies   *lruCache[cos.Ref, cachedBody]
	spaces   map[cos.Ref]pdfcolor.Space
	shadings map[cos.Ref]*shading.Shading
	patterns map[cos.Ref]*patternRes
}

// newRunCaches returns the empty cache set for one Run.
func newRunCaches() *runCaches {
	return &runCaches{
		bodies:   newLRUCache[cos.Ref, cachedBody](maxCachedBodies),
		spaces:   make(map[cos.Ref]pdfcolor.Space),
		shadings: make(map[cos.Ref]*shading.Shading),
		patterns: make(map[cos.Ref]*patternRes),
	}
}

// charge debits n units of work budget, saturating at -1 — the exhausted state exec and appendGlyphs test for — so no
// sequence of charges can wrap the counter.
func (in *interp) charge(n int) {
	if n > in.budget {
		in.budget = -1
		return
	}
	in.budget -= n
}

// bodyCost is the budget charge for decoding or running a stream body of n bytes.
func bodyCost(n int) int {
	return 1 + n>>bodyCostShift
}

// imageDecodeCost is the budget charge for one image decode: the encoded payload it scanned plus the samples it
// produced. img is nil for a failed decode, which still scanned its payload. The product is computed in int64 because
// int is 32 bits on GOARCH=386/arm; the decoder caps it at imaging's maxImagePixels (2^26), so the shifted result is
// always small enough for the budget counter.
func imageDecodeCost(img *imaging.Image, payload int) int {
	cost := bodyCost(payload)
	if img != nil {
		cost += int(int64(img.Width) * int64(img.Height) >> imagePixelCostShift)
	}
	return cost
}

// streamBody decodes one executable stream body — a form XObject's, a soft mask's, a tiling cell's, a Type 3
// charproc's — charging the work budget for it and caching the decoded bytes for the Run when raw is a reference. The
// charge is per call, not per decode: the body is scanned in full every time it runs, so a cache hit makes the repeat
// cheaper, never free.
func (in *interp) streamBody(raw cos.Object, stream *cos.Stream) ([]byte, bool) {
	ref, isRef := raw.(cos.Ref)
	if isRef {
		if cached, hit := in.caches.bodies.get(ref); hit {
			in.charge(bodyCost(len(cached.data)))
			return cached.data, cached.ok
		}
	}
	body, err := in.doc.StreamData(stream)
	if err != nil {
		// A failed decode still did work proportional to the input it consumed before failing.
		in.charge(bodyCost(len(stream.Raw)))
		body = nil
	} else {
		in.charge(bodyCost(len(body)))
	}
	if isRef && len(body) <= maxCachedBodyBytes {
		in.caches.bodies.put(ref, cachedBody{data: body, ok: err == nil})
	}
	return body, err == nil
}

// parseSpace parses one /ColorSpace resource entry, charging the budget and caching the result for the whole Run when
// the entry is a reference. Failures cache as nil.
func (in *interp) parseSpace(obj cos.Object) pdfcolor.Space {
	ref, isRef := obj.(cos.Ref)
	if isRef {
		if space, hit := in.caches.spaces[ref]; hit {
			return space
		}
	}
	in.charge(resourceParseCost)
	space, err := pdfcolor.Parse(in.doc, obj)
	if err != nil {
		space = nil
	}
	if isRef {
		in.caches.spaces[ref] = space
	}
	return space
}

// parseShading parses one shading dictionary (a /Shading resource entry or a shading pattern's /Shading), charging the
// budget and caching the result for the whole Run when the entry is a reference. Failures cache as nil.
func (in *interp) parseShading(obj cos.Object) *shading.Shading {
	ref, isRef := obj.(cos.Ref)
	if isRef {
		if sh, hit := in.caches.shadings[ref]; hit {
			return sh
		}
	}
	in.charge(shadingParseCost)
	sh, err := shading.Parse(in.doc, obj)
	if err != nil {
		sh = nil
	}
	if isRef {
		in.caches.shadings[ref] = sh
	}
	return sh
}
