// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package content tokenizes and interprets PDF content streams (ISO 32000-2 8–9), driving a device.Device: path
// construction and painting, graphics-state management (q/Q/cm, the ExtGState subset below), clipping (W/W*), color
// operators, form XObject recursion, image XObjects and inline images (decoded by internal/imaging), text objects
// (fonts via internal/font), shadings and patterns (internal/shading), and transparency — groups, soft masks, and blend
// modes.
//
// Robustness contract: unknown operators are skipped with the operand list reset (the convention every deployed viewer
// follows); operators with missing or mistyped operands are skipped likewise; unbalanced q/Q at stream end
// auto-unwinds; and all work is bounded — graphics-state depth, form recursion (with a cycle set), operand count,
// container nesting, per-operand element count, and total work (executed operators plus the stream bodies, resource
// parses, image decodes and device-side shading realizations those operators trigger; see budget.go) are all capped —
// so hostile input terminates without timeouts. The interpreter guarantees the device's push/pop balance no matter how
// malformed the content is.
package content

import (
	"math"

	pdfcolor "github.com/richardwilkes/pdfview/internal/color"
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
	"github.com/richardwilkes/pdfview/internal/store"
)

// Store key types: one dedicated comparable type per cached resource kind (see internal/store's package comment), keyed
// by the resource entry's object reference. The key is the reference's resolution identity (cos.RefKey), not the whole
// cos.Ref: a generation the document never consults must not split one object across two cache entries.
type (
	fontKey  struct{ ref cos.RefKey }
	imageKey struct{ ref cos.RefKey }
)

// Limits. maxQDepth caps q/Q nesting; pushes beyond it are ignored (with their matching Qs ignored too, so pairing
// survives). maxFormDepth is the XObject recursion cap; the per-page cycle set makes self-referential forms terminate
// even below it. maxOperands bounds the operand list — when content pushes more, the surplus is dropped, keeping the
// operands an operator actually consumes. maxDashEntries matches the dash-array truncation MuPDF exhibits. maxTotalOps
// bounds one Run's total work (across form recursion): operators charge one unit each, and the stream bodies and
// resource parses they trigger charge by budget.go's cost model — the backstop that keeps pathological streams from
// turning small inputs into huge work.
const (
	maxQDepth      = 256
	maxFormDepth   = 12
	maxOperands    = 64
	maxDashEntries = 32
	maxTotalOps    = 1 << 22
	// maxCachedFonts caps the per-Run font LRU used when no budgeted store is wired (matching maxCachedImages' role
	// for images); with a store, its byte budget replaces this cap.
	maxCachedFonts = 64
)

// pendingClip states.
const (
	clipNone uint8 = iota
	clipNonZero
	clipEvenOdd
)

// gstate is the graphics state the q/Q stack manages.
type gstate struct {
	fillSpace   pdfcolor.Space
	strokeSpace pdfcolor.Space
	// fillPattern/strokePattern are the selected pattern resources when the respective space is a /Pattern space (nil
	// until an scn/SCN names one); the *PatCTM matrices are the composed pattern-space→device matrices captured when
	// the pattern was selected.
	fillPattern   *patternRes
	strokePattern *patternRes
	// softMask is the active ExtGState soft mask (nil when /SMask is /None or absent); softMaskCTM is the CTM captured
	// when the gs operator installed it — the mask's anchor space (oracle-pinned; see softmask.go).
	softMask     *softMaskRes
	fillComps    []float32
	strokeComps  []float32
	sp           gfx.StrokeParams
	text         textParams
	ctm          gfx.Matrix
	fillPatCTM   gfx.Matrix
	strokePatCTM gfx.Matrix
	softMaskCTM  gfx.Matrix
	fillAlpha    float64
	strokeAlpha  float64
	// clips counts device clips pushed while this state has been current; Q (or the auto-unwind) pops them.
	clips int
	blend device.Blend
}

// textParams are the text-state parameters of the graphics state (ISO 32000-2 9.3.1). They persist across BT/ET pairs
// and are saved/restored by q/Q like the rest of the graphics state; only the text and line matrices (on interp, reset
// by BT) are scoped to the text object.
type textParams struct {
	font        *font.Font
	size        float32
	charSpacing float32 // Tc
	wordSpacing float32 // Tw
	scale       float32 // Tz operand / 100
	leading     float32 // TL
	rise        float32 // Ts
	mode        int     // Tr, render modes 0-7
}

// clone returns a deep copy (the slices are mutated in place by the color and dash operators).
func (g *gstate) clone() gstate {
	out := *g
	out.fillComps = append([]float32(nil), g.fillComps...)
	out.strokeComps = append([]float32(nil), g.strokeComps...)
	out.sp = g.sp.Clone()
	return out
}

// resFrame holds the per-resource-frame parse caches (one frame per entry of the res stack), keyed by resource name so
// repeated operators cannot force repeated stream decodes. Negative results cache as nil.
type resFrame struct {
	spaces   map[cos.Name]pdfcolor.Space
	shadings map[cos.Name]*shading.Shading
	patterns map[cos.Name]*patternRes
	// softMasks caches parsed ExtGState /SMask entries keyed by the ExtGState resource name (nil for /None and
	// failures); the anchoring CTM is captured per gs invocation, not cached.
	softMasks map[cos.Name]*softMaskRes
}

// interp interprets content streams for one Run call.
type interp struct {
	doc *cos.Document
	dev device.Device
	// st is the document-scoped budgeted resource store; fonts and decoded images cache there across runs when it is
	// non-nil (the per-Run maps below are the nil-store fallback).
	st *store.Store
	// res is the resource-dictionary stack; lookups use only the top (form resources replace, not merge).
	res []cos.Dict
	// frames are the per-resource-frame parse caches, parallel to res.
	frames  []resFrame
	gsStack []gstate
	// operands is the pending operand list in content order (index 0 is the operator's first operand).
	operands []cos.Object
	path     *gfx.Path
	// active is the form/pattern/charproc cycle set, keyed by resolution identity so a reference that alternates
	// generations cannot slip past it (cos.RefKey).
	active map[cos.RefKey]bool
	// images caches decoded image XObjects (nil for failed decodes) for this Run, an LRU capped at maxCachedImages
	// entries so a re-used image stays cached rather than being dropped once the cap is reached. It is itself nil when
	// st is wired, which is the caching path then; child interpreters share whichever the parent has.
	images *lruCache[cos.RefKey, *imaging.Image]
	// fonts caches loaded fonts (nil for failed loads) for this Run, keyed by the resource entry's reference; an LRU
	// capped at maxCachedFonts entries, nil with a store wired, exactly like images.
	fonts *lruCache[cos.RefKey, *font.Font]
	// caches holds the Run's reference-keyed decoded-body and parsed-resource caches (budget.go); child interpreters
	// share the set with their parent.
	caches *runCaches
	gs     gstate
	cur    gfx.Point
	start  gfx.Point
	// tm and tlm are the text and text-line matrices of the current text object (reset by BT).
	tm  gfx.Matrix
	tlm gfx.Matrix
	// qFloor is the gsStack depth below which Q may not pop — the boundary of the executing stream (form content cannot
	// pop its caller's states).
	qFloor int
	// qOverflow counts ignored q pushes beyond maxQDepth so their matching Qs are ignored too.
	qOverflow int
	formDepth int
	budget    int
	// textClipRuns counts the ClipText calls of the current text object; ET (or forced text-object end) finalizes them
	// with one EndTextClip.
	textClipRuns int
	// streamCTM is the CTM in effect at the start of the currently executing stream — the "default space" that anchors
	// pattern coordinates (ISO 32000-2 8.7.3.1). exec saves and sets it per stream body.
	streamCTM gfx.Matrix
	// t3Shape tracks Type 3 charproc execution: t3None outside procs, t3Colored inside a proc before d0/d1 resolve it,
	// t3Shape after d1 — which makes the proc a shape mask, so its own color operators are ignored and the caller's
	// fill color paints (ISO 32000-2 9.6.4).
	t3Shape uint8
	// suppressColor blocks the color operators for the whole interpreter: set on the child interpreter that replays an
	// uncolored tiling pattern's cell, whose content is a stencil painted with the pattern color (ISO 32000-2 8.7.3.3).
	suppressColor bool
	hasCur        bool
	inText        bool
	pending       uint8
}

// t3Shape states.
const (
	t3None uint8 = iota
	t3Colored
	t3Mask
)

// Run interprets a content stream against dev. resources is the page's resource dictionary (nil when the page has
// none); ctm maps user space to device space; st is the document's budgeted resource store (nil degrades to per-Run
// caching only — correct, just re-parsing across runs). Malformed content degrades — operators are skipped, never
// escalated — so Run does not fail; panics from truly hostile input are the caller's concern (the public API wraps
// rendering in a recover guard).
func Run(d *cos.Document, resources cos.Dict, data []byte, ctm gfx.Matrix, dev device.Device, st *store.Store) {
	in := newInterp(d, resources, ctm, dev, st)
	in.exec(data)
	// Auto-unwind whatever the stream left unbalanced, keeping the device's push/pop pairing intact.
	for len(in.gsStack) > 0 {
		in.restoreState()
	}
	in.popClips(0)
}

// AnnotRun is one page's annotation appearance pass: every appearance the page draws runs through the same AnnotRun, so
// they share one work budget and one set of reference-keyed caches instead of getting a fresh full budget each.
//
// The sharing is the bound, not an optimization. A page's /Annots may name up to internal/doc's cap (65536) of
// annotations, and internal/doc hands each one its appearance stream — commonly the SAME stream, since nothing stops
// every annotation from pointing at one appearance object. Run per annotation, each pass would re-decode that stream
// (through internal/filter's max(64 MB, 256x input) inflation allowance) and re-execute it against a full maxTotalOps
// budget, so a few kilobytes of crafted file would buy tens of thousands of decodes and interpretations — exactly the
// "small input, huge work" the package comment's bounded-work contract rules out. With one budget across the page, the
// appearances together cost what a single Run costs; with one body cache, the second annotation naming a stream reuses
// the first's decoded bytes.
type AnnotRun struct {
	caches *runCaches
	images *lruCache[cos.RefKey, *imaging.Image]
	fonts  *lruCache[cos.RefKey, *font.Font]
	active map[cos.RefKey]bool
	st     *store.Store
	budget int
}

// NewAnnotRun returns the shared state for one page's annotation appearance streams. st is the document's budgeted
// resource store (nil degrades to the per-pass LRUs, exactly as in Run).
func NewAnnotRun(st *store.Store) *AnnotRun {
	r := &AnnotRun{
		caches: newRunCaches(),
		active: make(map[cos.RefKey]bool),
		st:     st,
		budget: maxTotalOps,
	}
	if st == nil {
		r.images = newLRUCache[cos.RefKey, *imaging.Image](maxCachedImages)
		r.fonts = newLRUCache[cos.RefKey, *font.Font](maxCachedFonts)
	}
	return r
}

// Annot interprets one annotation appearance stream (a form XObject) against dev, charging r's shared budget. ctm must
// already compose the ISO 32000-2 12.5.5 placement matrix (internal/doc's Annot.Transform) with the page CTM; the
// form's own /Matrix and /BBox clip apply inside, exactly as for a form invoked by Do. pageResources is the page's
// resource dictionary: an appearance stream without /Resources of its own inherits it (probe-pinned). Malformed content
// degrades exactly as in Run.
func (r *AnnotRun) Annot(d *cos.Document, pageResources cos.Dict, raw cos.Object, stream *cos.Stream, ctm gfx.Matrix, dev device.Device) {
	if r.budget < 0 {
		return // The page's appearances have spent the shared budget; the rest draw nothing, like an exhausted Run.
	}
	in := baseInterp(d, pageResources, ctm, dev, r.st)
	in.active = r.active
	in.images = r.images
	in.fonts = r.fonts
	in.caches = r.caches
	in.budget = r.budget
	in.execForm(raw, stream)
	for len(in.gsStack) > 0 {
		in.restoreState()
	}
	in.popClips(0)
	r.budget = in.budget
}

// newInterp builds a fresh interpreter with the default graphics state, its own cycle set, resource caches, and full
// work budget. Run uses it; annotation appearances share one AnnotRun's state and a tiling pattern's cell replay uses
// newChild instead.
func newInterp(d *cos.Document, resources cos.Dict, ctm gfx.Matrix, dev device.Device, st *store.Store) *interp {
	in := baseInterp(d, resources, ctm, dev, st)
	in.active = make(map[cos.RefKey]bool)
	if st == nil {
		// The two LRUs are the no-store fallback only: with a store wired, every image and font lookup goes to it and
		// these are never consulted, so they are not built (a nil cache reports a miss and discards puts).
		in.images = newLRUCache[cos.RefKey, *imaging.Image](maxCachedImages)
		in.fonts = newLRUCache[cos.RefKey, *font.Font](maxCachedFonts)
	}
	in.caches = newRunCaches()
	in.budget = maxTotalOps
	return in
}

// newChild builds the interpreter that executes one tiling pattern cell against dev. It shares the parent's cycle set,
// parse caches, image/font LRUs, and remaining work budget (the caller folds the child's leftover budget back), and
// enters one form level deeper. The sharing is not just consistency: a single fill replays up to maxReplayTiles cells,
// so allocating a cycle set, a runCaches set, and two LRUs per cell — only to drop them — is pure garbage per cell.
func (in *interp) newChild(resources cos.Dict, ctm gfx.Matrix, dev device.Device) *interp {
	child := baseInterp(in.doc, resources, ctm, dev, in.st)
	child.active = in.active
	child.images = in.images
	child.fonts = in.fonts
	child.caches = in.caches
	child.formDepth = in.formDepth + 1
	child.budget = in.budget
	return child
}

// baseInterp builds the parts of an interpreter that never come from a parent: the default graphics state, the
// resource stack, and the path buffer. The caller supplies the cycle set, caches, and budget.
func baseInterp(d *cos.Document, resources cos.Dict, ctm gfx.Matrix, dev device.Device, st *store.Store) *interp {
	in := &interp{
		doc:    d,
		dev:    dev,
		st:     st,
		res:    []cos.Dict{resources},
		frames: []resFrame{{spaces: map[cos.Name]pdfcolor.Space{}}},
		gs: gstate{
			ctm:         ctm,
			fillSpace:   pdfcolor.DeviceGray,
			strokeSpace: pdfcolor.DeviceGray,
			fillComps:   pdfcolor.DeviceGray.Initial(),
			strokeComps: pdfcolor.DeviceGray.Initial(),
			sp: gfx.StrokeParams{
				Width:      1,
				MiterLimit: 10,
			},
			fillAlpha:   1,
			strokeAlpha: 1,
		},
		path: &gfx.Path{},
	}
	in.gs.text.scale = 1
	return in
}

// exec runs one stream body (the page's content or one form XObject's). It restores the q/Q balance it is entered with:
// states pushed by this body and clips pushed at its entry level are popped before returning.
func (in *interp) exec(data []byte) {
	savedFloor := in.qFloor
	entryDepth := len(in.gsStack)
	entryClips := in.gs.clips
	savedOverflow := in.qOverflow
	savedStreamCTM := in.streamCTM
	in.qFloor = entryDepth
	in.qOverflow = 0
	// The CTM at stream entry is this stream's default space — the anchor for pattern coordinates.
	in.streamCTM = in.gs.ctm
	// Text objects are per-stream: a form invoked mid-text-object gets fresh matrices, and its own unclosed text object
	// is force-closed at its end (so ClipText accumulations never leak across streams).
	savedTm, savedTlm, savedInText, savedClipRuns := in.tm, in.tlm, in.inText, in.textClipRuns
	in.tm, in.tlm, in.inText, in.textClipRuns = gfx.Identity(), gfx.Identity(), false, 0
	// A fresh stream starts with no operands: a form's body must not see the Do operator's own operand list.
	in.operands = in.operands[:0]
	lex := cos.NewLexer(data, 0)
	for in.budget >= 0 {
		tok, ok := lex.Next()
		if !ok {
			// Lexical error: the position advanced, so scanning terminates regardless, but it is charged like an
			// operator anyway. Without the charge a stream of pure garbage would be scanned end to end no matter how
			// little budget remained, and the package's "all work is bounded by maxTotalOps" contract would hold only
			// via the stream length.
			in.budget--
			continue
		}
		if tok.Kind == cos.TokenEOF {
			break
		}
		// Keywords other than the three object keywords are operators (BI hands off to the inline-image handler, which
		// keeps the tokenizer in sync across the binary payload while decoding and drawing it).
		if word, isOp := operatorWord(tok); isOp {
			in.budget--
			if word == "BI" {
				in.opInlineImage(lex, data)
			} else {
				in.op(word)
			}
			in.operands = in.operands[:0]
			continue
		}
		if obj, objOK := parseTopOperand(lex, tok); objOK {
			// The list keeps the OLDEST maxOperands operands, discarding everything past the cap: operators consume from
			// its start, so the retained prefix is exactly what any operator would read, and the cap is invisible to
			// every one of them. Keeping the newest instead would shift an operator's operands by the flood's length —
			// after 70 stray numbers, cm would read pushes #7-#12 rather than #1-#6.
			if len(in.operands) >= maxOperands {
				continue
			}
			in.operands = append(in.operands, obj)
		}
	}
	in.opEndText() // Force-close a truncated text object, flushing any pending text clip before the unwind.
	for len(in.gsStack) > entryDepth {
		in.restoreState()
	}
	in.popClips(entryClips)
	in.tm, in.tlm, in.inText, in.textClipRuns = savedTm, savedTlm, savedInText, savedClipRuns
	in.qFloor = savedFloor
	in.qOverflow = savedOverflow
	in.streamCTM = savedStreamCTM
	in.operands = in.operands[:0]
}

// restoreState implements Q: pop the clips this state pushed, then the state itself.
func (in *interp) restoreState() {
	if len(in.gsStack) == 0 {
		return
	}
	in.popClips(0)
	in.gs = in.gsStack[len(in.gsStack)-1]
	in.gsStack = in.gsStack[:len(in.gsStack)-1]
}

// popClips pops device clips until the current state holds exactly want.
func (in *interp) popClips(want int) {
	for in.gs.clips > want {
		in.dev.PopClip()
		in.gs.clips--
	}
}

// ---- operand access helpers (positional, from the operand list's start, like every deployed interpreter) ----

// floats returns the first n operands as float32 values, reporting whether all n were present and numeric.
func (in *interp) floats(n int) ([]float32, bool) {
	if len(in.operands) < n {
		return nil, false
	}
	out := make([]float32, n)
	for i := range n {
		v, ok := cos.AsReal(in.operands[i])
		if !ok {
			return nil, false
		}
		out[i] = float32(v)
	}
	return out, true
}

// float1 returns the single leading numeric operand.
func (in *interp) float1() (float32, bool) {
	v, ok := in.floats(1)
	if !ok {
		return 0, false
	}
	return v[0], true
}

// int1 returns the single leading integer-valued operand.
func (in *interp) int1() (int64, bool) {
	if len(in.operands) < 1 {
		return 0, false
	}
	return cos.AsInt(in.operands[0])
}

// name1 returns the single leading name operand.
func (in *interp) name1() (cos.Name, bool) {
	if len(in.operands) < 1 {
		return "", false
	}
	return cos.AsName(in.operands[0])
}

// leadingFloats returns every leading numeric operand (stopping at the first non-number).
func (in *interp) leadingFloats(maxN int) []float32 {
	out := make([]float32, 0, min(maxN, len(in.operands)))
	for _, obj := range in.operands {
		if len(out) >= maxN {
			break
		}
		v, ok := cos.AsReal(obj)
		if !ok {
			break
		}
		out = append(out, float32(v))
	}
	return out
}

// resource resolves /Resources[category][name], returning the raw (possibly Ref) entry so callers can use reference
// identity, plus whether the entry exists.
func (in *interp) resource(category, name cos.Name) (cos.Object, bool) {
	top := in.res[len(in.res)-1]
	if top == nil {
		return nil, false
	}
	cat, ok := in.doc.GetDict(top, category)
	if !ok {
		return nil, false
	}
	obj, ok := cat[name]
	return obj, ok
}

// colorSpace resolves a cs/CS operand: the four directly nameable spaces, then the resource dictionary, with per-frame
// caching.
func (in *interp) colorSpace(name cos.Name) (pdfcolor.Space, bool) {
	switch name {
	case "DeviceGray", "DeviceRGB", "DeviceCMYK", namePattern:
		space, err := pdfcolor.Parse(in.doc, name)
		if err != nil {
			return nil, false
		}
		return space, true
	}
	cache := in.frames[len(in.frames)-1].spaces
	if space, ok := cache[name]; ok {
		return space, space != nil
	}
	obj, ok := in.resource("ColorSpace", name)
	if !ok {
		cache[name] = nil
		return nil, false
	}
	// Negative entries are cached too (here and in parseSpace): repeated failures must not repeat the work.
	space := in.parseSpace(obj)
	cache[name] = space
	return space, space != nil
}

// isFinitePt reports whether both coordinates are finite.
func isFinitePt(x, y float32) bool {
	return !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0) &&
		!math.IsNaN(float64(y)) && !math.IsInf(float64(y), 0)
}

// operatorWord classifies a token: keywords are operators except the three object keywords, which are operands.
func operatorWord(tok cos.Token) (string, bool) {
	if tok.Kind != cos.TokenKeyword {
		return "", false
	}
	word := string(tok.Bytes)
	if word == "true" || word == "false" || word == "null" {
		return "", false
	}
	return word, true
}
