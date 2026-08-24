// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package stext implements the structured-text device: it records every character the content-stream interpreter emits
// — through any text verb, in emission order, unclipped — and provides fz_search_stext_page-compatible search over the
// recorded characters, plus the selection model (Page) that hit-tests, words, lines, text, and highlight quads are
// read from. Both readers work on the one recorded stream and share the line, word, and quad heuristics below, so what
// a search matches and what a selection covers can never disagree.
//
// The device deliberately ignores clip pushes: MuPDF's structured-text extraction is unclipped, so text scissored away
// by a clip path is still searchable, and invisible text (render mode 3, arriving through IgnoreText) is recorded too.
// Character quads are computed exactly as pinned against the oracle: Trm × [0..advance, descender..ascender], in the
// coordinate space of the interpreter pass's CTM. Search hits therefore come back in that same space; the engine seam
// runs the pass at scale 1 so they are page-space values matching the goldens' searchRaw quads bit-for-bit.
package stext

import (
	"image/color"
	"math"
	"unicode"
	"unicode/utf8"

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Char is one positioned character as the interpreter emitted it: its bounding quad, baseline start and end, and em
// size in the pass's device units, plus the extraction rune used for matching.
type Char struct {
	// Quad is the character's bounds, Trm × [0..advance, descender..ascender]. Its corner order matches the oracle's
	// searchRaw quads (upper-left, upper-right, lower-left, lower-right in the text's orientation).
	Quad gfx.Quad
	// Origin and End are the baseline start (Trm × (0,0)) and end (Trm × (advance,0)).
	Origin gfx.Point
	End    gfx.Point
	// Rune is the extraction/search value (0 when the font provides no Unicode mapping; such characters never match a
	// needle, exactly as pinned against the oracle).
	Rune rune
	// Size is the em size in device units (the vertical scale of the Trm).
	Size float32
	// Axis reports an axis-aligned Trm (no rotation or skew).
	Axis bool
}

// Device records the characters of every text run the interpreter emits. It implements device.Device; all non-text
// operations (paths, images, clips, groups, masks) are ignored via the embedded Null — except the soft-mask brackets,
// which nothing is drawn for but which delimit replayed content for the run-identity bookkeeping below. Build one with
// New or NewCapped; a directly constructed zero value carries a zero cap and so records nothing.
type Device struct {
	device.Null
	// last is the run recorded most recently at the current soft-mask nesting level. The interpreter delivers one run
	// through several verbs (fill+stroke for render mode 2, fill+clip for mode 4, ...) back-to-back, so comparing
	// against the previous delivery is enough to record each run's characters exactly once, at first delivery. Only
	// that one pointer is retained, so the device does not pin a page's entire glyph stream alive for the search pass.
	last *device.TextRun
	// masks saves last across soft-mask content: the interpreter replays the mask ahead of each painting verb, so a
	// mask body that shows text lands between a run's fill and stroke deliveries.
	masks []*device.TextRun
	chars []Char
	// limit is this device's own character cap; see the maxChars constant for what a cap is for and NewCapped for why
	// a caller might want a tighter one than the default.
	limit int
}

// New returns an empty structured-text device recording up to the default maxChars characters.
func New() *Device {
	return NewCapped(0)
}

// NewCapped returns an empty structured-text device recording up to limit characters, where a limit of zero or less
// means the default cap (see maxChars). A caller whose result outlives the pass — an extraction whose Page the
// application holds on to, rather than a search whose hits are transient — has its own reason to bound what a single
// page can pin in memory, so it names its own cap here. Any positive limit is used as given, above the default as well
// as below it: the default is the safe answer for a caller that has not thought about it, not a ceiling on one that
// has.
func NewCapped(limit int) *Device {
	if limit <= 0 {
		limit = maxChars
	}
	return &Device{limit: limit}
}

// FillText implements device.Device.
func (d *Device) FillText(run *device.TextRun, _ device.Paint) { d.record(run) }

// StrokeText implements device.Device.
func (d *Device) StrokeText(run *device.TextRun, _ *gfx.StrokeParams, _ device.Paint) { d.record(run) }

// ClipText implements device.Device.
func (d *Device) ClipText(run *device.TextRun) { d.record(run) }

// IgnoreText implements device.Device.
func (d *Device) IgnoreText(run *device.TextRun) { d.record(run) }

// Chars returns the recorded characters in emission order. The slice is owned by the device; callers must not mutate
// it.
func (d *Device) Chars() []Char {
	return d.chars
}

// BeginMask implements device.Device. Soft-mask content is replayed ahead of each painting verb, so its own text runs
// arrive between the deliveries of the run being painted; stacking the enclosing run keeps that run's later verbs
// deduplicated while the mask body's runs deduplicate among themselves.
func (d *Device) BeginMask(_ gfx.Rect, _ bool, _ color.NRGBA, _ []byte) {
	d.masks = append(d.masks, d.last)
	d.last = nil
}

// EndMask implements device.Device.
func (d *Device) EndMask() {
	if n := len(d.masks); n > 0 {
		d.last = d.masks[n-1]
		d.masks = d.masks[:n-1]
	}
}

// maxChars caps the recorded characters of one page. The interpreter's work budget bounds how many glyphs a page can
// show, but not the memory their records take: one Char is ~60 bytes, so an unbounded slice turns a 61 KB file (a form
// XObject holding a huge Tj, invoked repeatedly) into most of a gigabyte of live heap on the search pass. The cap is
// far above the densest real page — a full page of 6-point text runs to tens of thousands of characters — and the
// excess is dropped exactly as the link and outline walks drop theirs: what was recorded stays searchable.
const maxChars = 1 << 20

// record appends run's characters, once per run regardless of how many verbs delivered it. Characters past the
// device's cap are dropped.
func (d *Device) record(run *device.TextRun) {
	if run == d.last {
		return
	}
	d.last = run
	if len(d.chars) >= d.limit {
		return
	}
	asc, desc := run.Font.Ascender(), run.Font.Descender()
	for _, g := range run.Glyphs {
		if len(d.chars) >= d.limit {
			return
		}
		first, rest := expand(g)
		d.chars = append(d.chars, Char{
			Quad: gfx.Quad{
				UL: g.Trm.Apply(gfx.Point{X: 0, Y: asc}),
				UR: g.Trm.Apply(gfx.Point{X: g.Advance, Y: asc}),
				LL: g.Trm.Apply(gfx.Point{X: 0, Y: desc}),
				LR: g.Trm.Apply(gfx.Point{X: g.Advance, Y: desc}),
			},
			Origin: g.Trm.Apply(gfx.Point{}),
			End:    g.Trm.Apply(gfx.Point{X: g.Advance}),
			Rune:   first,
			Size:   float32(math.Hypot(float64(g.Trm.C), float64(g.Trm.D))),
			Axis:   g.Trm.B == 0 && g.Trm.C == 0,
		})
		d.recordFillers(&d.chars[len(d.chars)-1], rest)
	}
}

// recordFillers appends the characters spelling out a glyph's further runes. They carry no advance of their own and sit
// where the pen already stands — at base's end — so the text after the glyph keeps the spacing the glyph laid down.
// This is MuPDF's filler-glyph geometry: fz_add_stext_char_imp hands a no-glyph character the current pen as both its
// start and its end point and leaves the pen there, while the character's quad still spans the font's full
// ascender-to-descender height.
func (d *Device) recordFillers(base *Char, rest []rune) {
	if len(rest) == 0 {
		return
	}
	pen := base.End
	// The quad's vertical reach as a displacement from the baseline, which is what a zero-width quad at the pen is
	// built from: the glyph's matrix has already rotated and scaled it, and must not translate it a second time.
	upX, upY := base.Quad.UL.X-base.Origin.X, base.Quad.UL.Y-base.Origin.Y
	downX, downY := base.Quad.LL.X-base.Origin.X, base.Quad.LL.Y-base.Origin.Y
	up := gfx.Point{X: pen.X + upX, Y: pen.Y + upY}
	down := gfx.Point{X: pen.X + downX, Y: pen.Y + downY}
	for _, r := range rest {
		if len(d.chars) >= d.limit {
			return
		}
		d.chars = append(d.chars, Char{
			Quad:   gfx.Quad{UL: up, UR: up, LL: down, LR: down},
			Origin: pen,
			End:    pen,
			Rune:   r,
			Size:   base.Size,
			Axis:   base.Axis,
		})
	}
}

// expand splits a glyph's extraction runes into the one the glyph's own quad carries and the ones filler characters
// carry after it. Two independent sources put more than one rune behind a single glyph, and MuPDF applies both, in this
// order:
//
//   - A one-to-many /ToUnicode mapping (Glyph.Rest), which pdf_show_char shows as filler glyphs after the real one.
//   - A ligature code point, which fz_add_stext_char decomposes into the letters it draws, so a glyph reaching
//     extraction as U+FB02 is searchable as "fl". MuPDF applies this to every rune it is handed — the fillers of a
//     one-to-many mapping reach it as their own text-span items — so the /ToUnicode runes run through it too.
//
// The overwhelmingly common glyph maps to one ordinary rune and allocates nothing here.
func expand(g device.Glyph) (first rune, rest []rune) {
	if len(g.Rest) == 0 {
		if lig := ligature(g.Unicode); lig != "" {
			return rune(lig[0]), []rune(lig[1:])
		}
		return g.Unicode, nil
	}
	out := make([]rune, 0, len(g.Rest)+3)
	for i := range len(g.Rest) + 1 {
		r := g.Unicode
		if i > 0 {
			r = g.Rest[i-1]
		}
		if lig := ligature(r); lig != "" {
			for _, c := range lig {
				out = append(out, c)
			}
			continue
		}
		out = append(out, r)
	}
	return out[0], out[1:]
}

// ligature returns the letters a Unicode alphabetic-presentation ligature stands for, or "" for every other rune. The
// set is the one MuPDF's fz_add_stext_char decomposes (its FZ_STEXT_PRESERVE_LIGATURES option turns this off, and the
// search path never sets it); the two st ligatures both spell "st". Every replacement is ASCII, so indexing the string
// by byte indexes it by rune.
func ligature(r rune) string {
	switch r {
	case 0xFB00:
		return "ff"
	case 0xFB01:
		return "fi"
	case 0xFB02:
		return "fl"
	case 0xFB03:
		return "ffi"
	case 0xFB04:
		return "ffl"
	case 0xFB05, 0xFB06: // Long st and st.
		return "st"
	}
	return ""
}

// Search finds needle in the recorded characters and returns the hit quads in emission order, at most maxQuads of them
// (a match that would overflow the budget is truncated and the search stops, so the count is exact — matching the
// original implementation, whose fixed quad buffer fz_search_stext_page filled and no further). The matching rules
// replicate fz_search_stext_page black-box, as pinned by the quad-parity tests and the probe corpus: Unicode simple
// case folding for non-space runes; a needle whitespace rune matches a run of extracted whitespace characters, a
// horizontal gap of at least gapSpaceEm (a synthesized inter-word space), or a line break; a word never silently spans
// a line break; matches do not overlap; each match yields one quad per line touched, split further wherever
// segmentQuads' corner-distance rule refuses to merge. A needle with no non-space rune returns no hits.
func (d *Device) Search(needle string, maxQuads int) []gfx.Quad {
	return searchChars(d.chars, needle, maxQuads)
}

// Matcher thresholds, in em fractions of a character's size, pinned behaviorally against the oracle. A horizontal gap
// of at least gapSpaceEm reads as a word space (MuPDF's stext synthesizes a space there — text-std14's "Kerned Text"
// needle carries a 0.5 em TJ gap), measured against the preceding character's size.
//
// The two line thresholds are MuPDF's own BASE_MAX_DIST and SPACE_MAX_DIST (source/fitz/stext-device.c), which its
// structured-text device applies together: a character joins the line it follows while it sits within baseMaxDistEm
// of that line's baseline AND within spaceMaxDistEm along it, and starts a new line as soon as either distance is
// reached. Both are measured against the size of the character being placed, not the one before it — probing a raised
// 10-point digit between 20-point letters brackets the cutoff at 0.8 of the digit's em, not of the letters'.
//
// The perpendicular measurement is what keeps rotated text that advances through device y on one line. The along-line
// measurement is what still separates a wrapped line from the one above it: the next line starts far back along the
// advance direction, which is a break even though its baseline moved by well under one em.
const (
	gapSpaceEm     = 0.2
	baseMaxDistEm  = 0.8
	spaceMaxDistEm = 0.8
)

// The hit-quad merge fuzzes, in em fractions of the size of the character being merged. They are MuPDF's own values,
// set in fz_new_search (source/fitz/stext-search.c) as hfuzz = 0.5 ("merge large gaps") and vfuzz = 0.1, and applied by
// add_quad: a character joins the quad under construction only while both of its leading corners sit within
// quadHFuzzEm along the line and within quadVFuzzEm across it of the two corners that quad currently ends at. Like the
// line thresholds above they scale by the size of the character being placed, not the one before it.
//
// The vertical fuzz is what splits a hit around an oversized inter-word space or a raised digit, and its boundary is
// exact rather than approximate: the comparisons are strict, so a character whose corners diverge by exactly
// quadVFuzzEm of its em — a digit raised 0.1 em among letters of its own size — starts a new quad.
const (
	quadHFuzzEm = 0.5
	quadVFuzzEm = 0.1
)

func searchChars(chars []Char, needle string, maxQuads int) []gfx.Quad {
	runes := []rune(needle)
	hasWordRune := false
	for _, r := range runes {
		if !unicode.IsSpace(r) {
			hasWordRune = true
			break
		}
	}
	if !hasWordRune || maxQuads <= 0 {
		return nil
	}
	var out []gfx.Quad
	for i := 0; i < len(chars) && len(out) < maxQuads; {
		quads, end, ok := matchAt(chars, i, runes)
		if !ok {
			i++
			continue
		}
		for _, q := range quads {
			if len(out) == maxQuads {
				break
			}
			out = append(out, q)
		}
		i = end
	}
	return out
}

// matchAt attempts a needle match starting at chars[start], returning the per-line quads and the index just past the
// match.
func matchAt(chars []Char, start int, needle []rune) (quads []gfx.Quad, end int, ok bool) {
	pos := start
	segStart := start
	var segments [][2]int
	for _, r := range needle {
		if unicode.IsSpace(r) {
			consumed := false
			for pos < len(chars) && isSpaceChar(chars[pos]) {
				pos++
				consumed = true
			}
			// pos > 0, not pos > start: a needle whose first rune is whitespace consumes no characters here, so the
			// synthesized alternatives must still be consulted against the predecessor of the match's own starting
			// character. Only a match starting at the very first recorded character has no predecessor.
			if pos > 0 && pos < len(chars) {
				prev, cur := chars[pos-1], chars[pos]
				switch {
				case lineBreakBetween(prev, cur):
					segments = append(segments, [2]int{segStart, pos})
					segStart = pos
					consumed = true
				case gapBetween(prev, cur) >= gapSpaceEm*prev.Size:
					consumed = true
				}
			}
			if !consumed {
				return nil, 0, false
			}
			continue
		}
		if pos >= len(chars) || isSpaceChar(chars[pos]) || chars[pos].Rune == 0 || !foldEqual(chars[pos].Rune, r) {
			return nil, 0, false
		}
		if pos > segStart && lineBreakBetween(chars[pos-1], chars[pos]) {
			return nil, 0, false // A word may not silently span a line break.
		}
		pos++
	}
	segments = append(segments, [2]int{segStart, pos})
	for _, seg := range segments {
		if seg[0] >= seg[1] {
			continue
		}
		quads = append(quads, segmentQuads(chars[seg[0]:seg[1]])...)
	}
	return quads, pos, true
}

func isSpaceChar(c Char) bool { return unicode.IsSpace(c.Rune) }

// foldEqual reports whether a and b are the same rune under Unicode simple case folding. It is the rune-level
// equivalent of strings.EqualFold over their single-rune strings — the matcher's innermost comparison, run once per
// (character, needle-rune) pair, so the two heap strings that spelling allocated dominated search cost on text-heavy
// pages. Runes that string() could not have encoded (negative, surrogate, or above unicode.MaxRune) fold to
// utf8.RuneError, exactly as the conversion would have replaced them.
func foldEqual(a, b rune) bool {
	if !utf8.ValidRune(a) {
		a = utf8.RuneError
	}
	if !utf8.ValidRune(b) {
		b = utf8.RuneError
	}
	if a == b {
		return true
	}
	// A simple-folding orbit is a short cycle through the case variants of a rune; walking a's finds b iff they fold
	// together.
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}

// advanceDir is the unit vector of a character's baseline advance ((1, 0) for a degenerate advance).
func advanceDir(c Char) (ux, uy float64) {
	dx, dy := float64(c.End.X-c.Origin.X), float64(c.End.Y-c.Origin.Y)
	n := math.Hypot(dx, dy)
	if n == 0 {
		return 1, 0
	}
	return dx / n, dy / n
}

// lineBreakBetween reports whether cur starts a new line rather than continuing prev's: either its baseline origin is
// offset from prev's by baseMaxDistEm perpendicular to the advance direction, or it sits spaceMaxDistEm or further
// from where prev's advance left the pen along that direction. See the threshold constants for why both distances
// decide this and why cur's size scales them.
//
// The second test is what a wrapped line trips: its first character is barely off the previous baseline but a long way
// back along it. Without that test a 0.8 em threshold on the baseline alone would swallow the wrap, and a threshold
// tight enough to catch the wrap would split every superscript off its own line — which is what the search matcher did
// before, losing hits MuPDF reports.
func lineBreakBetween(prev, cur Char) bool {
	// The direction is taken once and used for both distances: gapBetween would compute it a second time, and the
	// matcher asks this of every character pair it walks.
	ux, uy := advanceDir(prev)
	dx, dy := float64(cur.Origin.X-prev.Origin.X), float64(cur.Origin.Y-prev.Origin.Y)
	if math.Abs(ux*dy-uy*dx) >= float64(baseMaxDistEm*cur.Size) {
		return true
	}
	gx, gy := float64(cur.Origin.X-prev.End.X), float64(cur.Origin.Y-prev.End.Y)
	return math.Abs(ux*gx+uy*gy) >= float64(spaceMaxDistEm*cur.Size)
}

// gapBetween is the signed distance along prev's advance direction from prev's end to cur's origin (negative when
// kerning tucks cur backward).
func gapBetween(prev, cur Char) float32 {
	ux, uy := advanceDir(prev)
	return float32(ux*float64(cur.Origin.X-prev.End.X) + uy*float64(cur.Origin.Y-prev.End.Y))
}

// hdist and vdist are add_quad's two corner distances, transcribed from source/fitz/stext-search.c. vdist is the
// distance from a to b perpendicular to the line's direction. hdist is MuPDF's formula AS WRITTEN, minus sign included:
// it is the along-line projection only when the direction is axis-aligned, and something else — neither a projection
// nor a perpendicular — for a rotated one. It is reproduced verbatim rather than corrected because the oracle's quads
// ARE this formula's output, so "fixing" the sign would move the hits of every rotated page away from MuPDF's.
func hdist(dirX, dirY float64, a, b gfx.Point) float64 {
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	return math.Abs(dx*dirX - dy*dirY)
}

func vdist(dirX, dirY float64, a, b gfx.Point) float64 {
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	return math.Abs(dx*dirY - dy*dirX)
}

// virtualSpace builds the space character MuPDF's structured-text device would have recorded for the word-sized gap
// between prev and cur, so that segmentQuads sees the stream add_quad sees. fz_add_stext_char_imp
// (source/fitz/stext-device.c) synthesizes that space from the INCOMING glyph's trm, font and size, spanning from the
// pen the previous glyph left behind to the incoming glyph's origin — so the space carries cur's em size and cur's
// ascender-to-descender reach, measured at prev's pen on its leading edge and at cur's origin on its trailing one.
//
// Its trailing corners are therefore cur's own leading corners, and are copied rather than recomputed so that they are
// bit-identical: the space always merges into cur at exactly zero distance, which is what makes a word gap cost the hit
// nothing horizontally. Its leading corners are cur's vertical reach standing at the pen, which is what the vertical
// tests at the gap end up comparing the open quad against.
func virtualSpace(prev, cur Char) Char {
	upX, upY := cur.Quad.UL.X-cur.Origin.X, cur.Quad.UL.Y-cur.Origin.Y
	downX, downY := cur.Quad.LL.X-cur.Origin.X, cur.Quad.LL.Y-cur.Origin.Y
	return Char{
		Quad: gfx.Quad{
			UL: gfx.Point{X: prev.End.X + upX, Y: prev.End.Y + upY},
			UR: cur.Quad.UL,
			LL: gfx.Point{X: prev.End.X + downX, Y: prev.End.Y + downY},
			LR: cur.Quad.LL,
		},
		Origin: prev.End,
		End:    cur.Origin,
		Rune:   ' ',
		Size:   cur.Size,
		Axis:   cur.Axis,
	}
}

// segmentQuads assembles one line's matched characters into hit quads, reproducing add_quad
// (source/fitz/stext-search.c) exactly. Each character in turn extends the quad under construction while all four of
// the corner distances below hold — its lower-left against the quad's lower-right and its upper-left against the quad's
// upper-right, each within quadHFuzzEm along the line and quadVFuzzEm across it — and a character failing any one of
// them closes that quad and opens its own. A merge REPLACES the open quad's trailing corners with the character's own
// rather than unioning the two, so a quad always reaches from the leading corners of the first character merged into it
// to the trailing corners of the last, which is what the oracle's quads over a kerned or backward-stepping run show.
//
// One rule serves every orientation. Vertically-mirrored axis-aligned text (Trm.D > 0, ascender below descender in
// y-down space) needs no special case because vdist is an absolute distance; a uniformly rotated run needs none either,
// because its adjacent characters' corners coincide exactly, so the run merges into the single first-to-last-corner
// quad that a rotation-specific assembly would have produced.
//
// The virtual space is the one thing here that add_quad does not do, and it stands in for something upstream of it: a
// word-sized gap between two glyphs is a real space character in MuPDF's stream (see virtualSpace) that add_quad merges
// like any other, while pdfview's device records no such character and reads the gap itself as a word space instead
// (see gapSpaceEm). Synthesizing the space here, ahead of the character that closes the gap, keeps the two models in
// step: without it a TJ gap of half an em or more — text-std14's "Kerned Text" — would exceed quadHFuzzEm and split a
// hit the oracle reports as one quad. With it the gap is bridged horizontally, the vertical tests still run at the gap
// against the following character's extents, and a vertical split at a gap opens its new quad at the pen, covering the
// gap, rather than at the character past it. Gaps too narrow to read as a word space are left alone, so add_quad's own
// test sees them raw — which is what quadHFuzzEm is for.
func segmentQuads(seg []Char) []gfx.Quad {
	// add_quad measures against line->dir, the direction of the whole line the hit sits on. A segment never crosses a
	// line break — the matcher cuts one there and a selection asks for one line at a time — so the direction its first
	// character advances in is that line's direction.
	dirX, dirY := advanceDir(seg[0])
	var out []gfx.Quad
	var open gfx.Quad
	building := false
	add := func(c Char) {
		hfuzz, vfuzz := float64(quadHFuzzEm*c.Size), float64(quadVFuzzEm*c.Size)
		if building &&
			hdist(dirX, dirY, open.LR, c.Quad.LL) < hfuzz && vdist(dirX, dirY, open.LR, c.Quad.LL) < vfuzz &&
			hdist(dirX, dirY, open.UR, c.Quad.UL) < hfuzz && vdist(dirX, dirY, open.UR, c.Quad.UL) < vfuzz {
			open.UR, open.LR = c.Quad.UR, c.Quad.LR
			return
		}
		if building {
			out = append(out, open)
		}
		open, building = c.Quad, true
	}
	for i, c := range seg {
		if i > 0 {
			if prev := seg[i-1]; gapBetween(prev, c) >= gapSpaceEm*prev.Size {
				add(virtualSpace(prev, c))
			}
		}
		add(c)
	}
	if building {
		out = append(out, open)
	}
	return out
}
