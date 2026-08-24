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
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// The work budget (interp.budget, seeded with maxTotalOps) is charged for the work an operator TRIGGERS, not only for
// dispatching it. Dispatch alone is not a bound: a page of repeated Do operators costs one unit per invocation while
// each invocation re-decodes and re-scans the form's whole body, so a few tens of kilobytes of input buy hours of work.
// Every site that runs another stream's body — form XObjects, soft-mask groups, Type 3 charprocs, tiling-pattern cells
// — charges bodyCost for that body on every invocation; every resource parse an operator can force charges a flat cost;
// image decodes charge imageDecodeCost for the samples they produce (dictated by the dictionary's dimensions, not the
// payload's size); and a font load charges fontLoadCost for the program it decodes. The reference-keyed per-Run caches
// (runCaches) make repeats cheap on top of bounded: a form invoked twice decodes once, and a parsed color space,
// shading or pattern survives the fresh resource frame each form invocation pushes.

const (
	// bodyCostShift scales a stream body's length into budget units. exec scans the whole body on every invocation (and
	// the decode that produced it is at least that much work), while ordinary content averages well under 16 bytes per
	// operator, so len>>4 charges a real body about what executing it costs and charges padding that executes nothing
	// for the scan it forces.
	bodyCostShift = 4
	// resourceParseCost is the flat charge for parsing one color space, pattern, or soft mask: each resolves a
	// dictionary chain and may decode a stream of its own (lookup tables and tiling/mask bodies are charged on top).
	resourceParseCost = 1 << 10
	// shadingParseCost is the flat charge for parsing one shading or sampling one transfer function: both evaluate a PDF
	// function 256 times, and a type 4 function's evaluations are bounded but far from free.
	shadingParseCost = 1 << 12
	// fontParseCost is the flat charge for loading one font resource: Tf resolves a descriptor chain, decodes the embedded
	// program, and builds the encoding/width tables. The per-reference cache makes a repeat free but does not bound the
	// total: the cost is per DISTINCT reference, and a resource dictionary may name up to maxContainerElements of them,
	// all sharing one descriptor.
	fontParseCost = 1 << 12
	// fontProgramCostShift scales a loaded font's estimated footprint into budget units, on top of the flat charge. The
	// decode is proportional to the program's bytes and internal/filter allows one stream to inflate to max(64 MB,
	// 256xinput), so a handful of megabyte-scale fonts must cost a visible share of the budget while a page's worth of
	// ordinary embedded subsets costs a small fraction of it.
	fontProgramCostShift = 6
	// shadingSampleCostShift scales one device-side shading realization's sample count into budget units: a type 1
	// shading's grid cells (one PDF function evaluation each) and a mesh shading's triangles (one rasterized fill each).
	// Both are far more expensive per sample than an image sample (imagePixelCostShift, >>6), so they count sixteen
	// times as much. At >>2 the whole budget buys 64 realizations of a full shading.MaxGridArea grid, or 32 draws of a
	// maximally tessellated mesh, while a page's worth of ordinary gradient fills — sized from their own device extent, a
	// few tens of thousands of cells each — costs a small fraction of it.
	shadingSampleCostShift = 2
	// imagePixelCostShift scales a decoded image's pixel count into budget units. A decode touches every sample it
	// produces — unpacking, color conversion, mask compositing — and the sample count is bounded only by
	// imaging.maxPixelsFor(payload), whose floor (2^22 pixels) is independent of the payload's size: charging by payload
	// length alone would leave a stream of one-byte inline images claiming 2048x2048 dimensions essentially free. At
	// >>6, the whole budget buys roughly 64 decodes at that floor, while a page's worth of legitimate photographs costs
	// a small fraction of it.
	imagePixelCostShift = 6
	// maxCachedBodies and maxCachedBodyBytes bound the per-Run decoded-body cache: at most this many bodies, none larger
	// than the byte cap. An oversized body decodes again on reuse rather than pinning tens of megabytes for the whole
	// Run; the budget charge, not the cache, bounds re-decoding it.
	maxCachedBodies    = 16
	maxCachedBodyBytes = 1 << 20
	// maxCachedResources bounds each of the per-Run parse caches (color spaces, shadings, patterns). A parsed resource
	// can retain far more than the dictionary it came from (a /Separation tint transform holds its type 0 function's
	// whole decoded sample table, a mesh pattern its tessellation), so an unbounded map would let a page naming a few
	// thousand distinct references to one fat stream pin every realization of it for the Run. The cap sits well above
	// the handful of distinct resources real pages use; past it the least recently used entry is dropped and re-parses
	// on demand, charged again.
	maxCachedResources = 64
)

// cachedBody is one decoded stream body plus whether its decode succeeded, so a failed decode caches too (a broken
// stream must not be re-decoded on every invocation).
type cachedBody struct {
	data []byte
	ok   bool
}

// runCaches are the reference-keyed parse caches of one Run, shared by every interpreter that Run spawns (a tiling
// pattern's child interpreter shares its parent's, like the cycle set and the budget). They key on cos.RefKey rather
// than the whole cos.Ref, so two references that differ only in generation — which resolve to the same object — share
// one entry. The per-frame maps in resFrame remain the name-keyed layer: names are scoped to a resource frame,
// references are not.
type runCaches struct {
	bodies   *lruCache[cos.RefKey, cachedBody]
	spaces   *lruCache[cos.RefKey, pdfcolor.Space]
	shadings *lruCache[cos.RefKey, *shading.Shading]
	patterns *lruCache[cos.RefKey, *patternRes]
}

func newRunCaches() *runCaches {
	return &runCaches{
		bodies:   newLRUCache[cos.RefKey, cachedBody](maxCachedBodies),
		spaces:   newLRUCache[cos.RefKey, pdfcolor.Space](maxCachedResources),
		shadings: newLRUCache[cos.RefKey, *shading.Shading](maxCachedResources),
		patterns: newLRUCache[cos.RefKey, *patternRes](maxCachedResources),
	}
}

// charge debits n units of work budget, saturating at -1 (the exhausted state exec and appendGlyphs test for) so no
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

// decodeCost is bodyCost for a byte count a decode reported through cos.Document.DecodeWork: one stage's allowance
// alone reaches 64 MB and a chain's total is unbounded above that, so the shift is applied in uint64 and the result
// capped at the whole budget.
func decodeCost(n uint64) int {
	return 1 + int(min(n>>bodyCostShift, uint64(maxTotalOps)))
}

// imageDecodeCost is the budget charge for one image decode: the payload it scanned plus the samples it produced.
// decoded is what the decode's filter chains produced (cos.Document.DecodeWork), floored at the encoded payload, since
// an unfiltered image produces no decoded bytes yet is scanned in full. img is nil for a failed decode, which still did
// the scanning and inflating. The decoder caps the sample product at imaging's maxImagePixels (2^26), so the shifted
// result always fits the budget counter.
func imageDecodeCost(img *imaging.Image, decoded uint64, payload int) int {
	cost := decodeCost(max(decoded, uint64(payload)))
	if img != nil {
		cost += img.Width * img.Height >> imagePixelCostShift
	}
	return cost
}

// shadingPaintCost is the budget charge for handing one shading to the device to realize at device resolution, on top
// of the parse charge. The parse alone is not a bound, because the realization happens per PAINTING OPERATION: a type 1
// shading's grid is evaluated cell by cell into a device image, and a mesh shading's triangles are re-rasterized every
// time (the tessellation is cached on the *shading.Shading, the device-side draw is not). A flate-compressed run of sh
// operators is a few tens of kilobytes of file, so at one operator unit apiece those realizations turn a small input
// into minutes of work. The charge is an upper bound on the device's work, not a measurement: a device that caches its
// realization (internal/render caches the grid per size) does less, and one that ignores shadings does none. Axial and
// radial shadings realize as a gradient built from the ramp parseShading already sampled, so they cost only their
// operator unit.
func shadingPaintCost(sh *shading.Shading, target gfx.Matrix) int {
	switch {
	case sh == nil:
		return 0
	case sh.Kind == shading.KindFunction:
		w, h, ok := sh.GridSize(target)
		if !ok {
			return 0 // The device realizes nothing from this geometry.
		}
		return 1 + w*h>>shadingSampleCostShift
	case sh.Kind >= shading.KindFreeTriangle:
		return 1 + len(sh.Triangles)>>shadingSampleCostShift
	default:
		return 1
	}
}

// fontLoadCost is the budget charge for one font load: the flat parse charge plus the program the load decoded. f is
// nil for a failed load, which still did the flat work. The footprint is shifted in uint64 and capped at the whole
// budget.
func fontLoadCost(f *font.Font) int {
	cost := fontParseCost
	if f != nil {
		cost += int(min(f.MemoryEstimate()>>fontProgramCostShift, uint64(maxTotalOps)))
	}
	return cost
}

// streamBody decodes one executable stream body (a form XObject's, a soft mask's, a tiling cell's, a Type 3
// charproc's), charging the work budget for it and caching the decoded bytes for the Run when raw is a reference. The
// charge is per call, not per decode: the body is scanned in full every time it runs, so a cache hit makes the repeat
// cheaper, never free.
func (in *interp) streamBody(raw cos.Object, stream *cos.Stream) ([]byte, bool) {
	ref, isRef := raw.(cos.Ref)
	key := ref.Key()
	if isRef {
		if cached, hit := in.caches.bodies.get(key); hit {
			in.charge(bodyCost(len(cached.data)))
			return cached.data, cached.ok
		}
	}
	before := in.doc.DecodeWork()
	body, err := in.doc.StreamData(stream)
	// The charge is for what the decode PRODUCED, not the input it was handed: a failure produces no bytes yet may have
	// inflated internal/filter's whole max(64 MB, 256x input) allowance before reporting ErrTooLarge, so priced by input
	// length, more distinct zip-bombed bodies than the cache holds, cycled by Do, buy gigabytes of decompression from a
	// small file. An unfiltered body decodes nothing, so its own length floors the charge: exec scans it either way.
	in.charge(decodeCost(max(in.doc.DecodeWork()-before, uint64(len(body)))))
	if err != nil {
		body = nil
	}
	if isRef && len(body) <= maxCachedBodyBytes {
		in.caches.bodies.put(key, cachedBody{data: body, ok: err == nil})
	}
	return body, err == nil
}

// chargeParse charges the work budget for one resource parse: the flat cost of the resource's kind plus whatever the
// parse decoded (cos.Document.DecodeWork's delta since before, the reading taken just before the parse ran). The flat
// charge alone is not a bound, for the reason streamBody gives: one /Separation space naming a type 0 tint transform,
// or one mesh shading, inflates a stream internal/filter allows to reach 64 MB, and the parse RETAINS what it decoded,
// so a few thousand distinct references to that stream would fit a flat charge while costing gigabytes of inflation.
// A parse that decoded nothing costs the flat charge alone.
func (in *interp) chargeParse(flat int, before uint64) {
	in.charge(flat)
	if decoded := in.doc.DecodeWork() - before; decoded != 0 {
		in.charge(decodeCost(decoded))
	}
}

// parseSpace parses one /ColorSpace resource entry, charging the budget and caching the result for the whole Run when
// the entry is a reference. Failures cache as nil.
func (in *interp) parseSpace(obj cos.Object) pdfcolor.Space {
	ref, isRef := obj.(cos.Ref)
	if isRef {
		if space, hit := in.caches.spaces.get(ref.Key()); hit {
			return space
		}
	}
	before := in.doc.DecodeWork()
	space, err := pdfcolor.Parse(in.doc, obj)
	if err != nil {
		space = nil
	}
	in.chargeParse(resourceParseCost, before)
	if isRef {
		in.caches.spaces.put(ref.Key(), space)
	}
	return space
}

// parseShading parses one shading dictionary (a /Shading resource entry or a shading pattern's /Shading), charging the
// budget and caching the result for the whole Run when the entry is a reference. Failures cache as nil.
func (in *interp) parseShading(obj cos.Object) *shading.Shading {
	ref, isRef := obj.(cos.Ref)
	if isRef {
		if sh, hit := in.caches.shadings.get(ref.Key()); hit {
			return sh
		}
	}
	before := in.doc.DecodeWork()
	sh, err := shading.Parse(in.doc, obj)
	if err != nil {
		sh = nil
	}
	in.chargeParse(shadingParseCost, before)
	if isRef {
		in.caches.shadings.put(ref.Key(), sh)
	}
	return sh
}
