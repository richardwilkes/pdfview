// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package stext

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/richardwilkes/pdfview/internal/gfx"
)

// line is one run of consecutive characters that lineBreakBetween did not separate. It is the unit hit-testing snaps
// to and the unit highlight quads are grouped by. The runs are the character stream's own — the interpreter emits
// characters in content-stream order, so a line is always a contiguous index range and the runs partition
// [0, len(chars)) in increasing order — not a re-flow of the page into visual reading order.
type line struct {
	// bounds is the axis-aligned extent of every corner of the run's quads, which is what a hit test measures its
	// point against: the distance from a pointer to a line has nothing to do with the direction that line advances in,
	// and a rotated or skewed line's quads are not axis-aligned.
	bounds gfx.Rect
	// ux, uy is the run's advance direction, taken from its first character that actually advances. Filler characters
	// (the extra letters a ligature or a one-to-many /ToUnicode mapping spells) sit at the pen with Origin == End, so
	// advanceDir gives them its (1, 0) degenerate fallback — right for horizontal text and wrong for everything else.
	// A line that shows nothing but fillers keeps that same fallback, having no better answer available.
	ux, uy     float64
	start, end int
}

// project returns the scalar coordinate of a point along the line's advance direction. Positions within a line are
// compared through this rather than by x: rotated text advances through device y, and mirrored text advances backwards
// through x, so a raw coordinate comparison orders those lines wrongly (or not at all).
func (l line) project(x, y float32) float64 {
	return l.ux*float64(x) + l.uy*float64(y)
}

// newLines splits chars into the runs lineBreakBetween separates — the same predicate the matcher refuses to let a
// word span, so a selection's idea of a line and a search hit's idea of a line can never disagree.
func newLines(chars []Char) []line {
	if len(chars) == 0 {
		return nil
	}
	var lines []line
	start := 0
	for i := 1; i < len(chars); i++ {
		if lineBreakBetween(chars[i-1], chars[i]) {
			lines = append(lines, newLine(chars, start, i))
			start = i
		}
	}
	return append(lines, newLine(chars, start, len(chars)))
}

// newLine measures one run: the extent its characters' quads cover and the direction its text advances in. The caller
// guarantees start < end.
func newLine(chars []Char, start, end int) line {
	l := line{ux: 1, start: start, end: end}
	haveDir := false
	for i := start; i < end; i++ {
		q := chars[i].Quad
		for c, pt := range [4]gfx.Point{q.UL, q.UR, q.LL, q.LR} {
			if i == start && c == 0 {
				l.bounds = gfx.Rect{X0: pt.X, Y0: pt.Y, X1: pt.X, Y1: pt.Y}
				continue
			}
			l.bounds.X0 = min(l.bounds.X0, pt.X)
			l.bounds.Y0 = min(l.bounds.Y0, pt.Y)
			l.bounds.X1 = max(l.bounds.X1, pt.X)
			l.bounds.Y1 = max(l.bounds.Y1, pt.Y)
		}
		if !haveDir && chars[i].Origin != chars[i].End {
			l.ux, l.uy = advanceDir(chars[i])
			haveDir = true
		}
	}
	return l
}

// Page is the selection model over one page's recorded characters: an immutable, indexable view of the exact stream
// the device emitted and Search matches against. Indices are positions in that stream, so a selection is a half-open
// [start, end) pair of them and an insertion point is a single index in [0, Len()].
//
// Nothing here re-flows the page. Characters arrive in content-stream order — reading order for well-formed documents
// and nothing in particular for the rest — and the lines and words carved out of them come from the same geometric
// heuristics the matcher uses, so what selects as one line or one word is what a search hit treats as one.
//
// A Page is immutable once built, so it holds no lock and any number of goroutines may read it at once. Every method
// tolerates a nil receiver and a page with no characters, answering both with the same zero values.
type Page struct {
	chars []Char
	lines []line
}

// NewPage builds the selection model for a recorded character stream, taking ownership of the slice — Device.Chars
// hands back the device's own, which the device has no further use for once its pass has ended. The caller must not
// mutate it afterwards: the line runs computed here index into it.
func NewPage(chars []Char) *Page {
	return &Page{chars: chars, lines: newLines(chars)}
}

// Len returns the number of characters on the page. Indices run from 0 through Len() inclusive: Len() is the insertion
// point past the last character, not a character.
func (p *Page) Len() int {
	if p == nil {
		return 0
	}
	return len(p.chars)
}

// IndexAt returns the insertion index in [0, Len()] nearest pt, which is a point in the same space as the recorded
// characters. It reproduces what a text editor does with a click: the nearest line is chosen first, and the point is
// then placed between two of that line's characters. So a click in the left half of a two-column gutter lands at the
// end of the left column rather than at the start of the right one — the nearer column wins, and the earlier one wins
// a tie — and a click off the page entirely lands at the near end of the nearest line rather than nowhere at all. A glyph that spells several characters — a ligature, or a one-to-many
// /ToUnicode mapping — shares its advance evenly among them here, so a caret can land between any two of its letters
// and a click at its trailing edge lands after all of them; see glyphSpan. An empty page returns 0.
func (p *Page) IndexAt(pt gfx.Point) int {
	if p == nil || len(p.lines) == 0 {
		return 0
	}
	l := p.lines[p.nearestLine(pt)]
	// The scan is linear by necessity, not by neglect: kerning inside a TJ array can tuck a character backwards past
	// its predecessor's midpoint, so the projections within a line are not sorted and a binary search over them would
	// answer with a position from the wrong part of the line.
	target := l.project(pt.X, pt.Y)
	for i := l.start; i < l.end; {
		n := p.glyphSpan(i, l.end)
		base := p.chars[i]
		from, to := l.project(base.Origin.X, base.Origin.Y), l.project(base.End.X, base.End.Y)
		for k := range n {
			if from+(to-from)*(float64(k)+0.5)/float64(n) > target {
				return i + k
			}
		}
		i += n
	}
	return l.end
}

// glyphSpan returns how many characters, from index i and before end, one glyph spelled: the character at i and the
// fillers after it. A filler carries no advance of its own and sits at its glyph's pen (see Device.recordFillers), so
// it is recognizable as a zero-advance character whose origin is its predecessor's end. The hit test treats the group
// as one glyph whose advance its letters share, because the page shows one shape there: measured by their own geometry
// every caret position of the group would sit at the trailing edge, and a point just short of the pen would land
// between the glyph's first letter and its second — inside a shape the reader sees no boundary in.
func (p *Page) glyphSpan(i, end int) int {
	n := 1
	for i+n < end {
		c, prev := p.chars[i+n], p.chars[i+n-1]
		if c.Origin != c.End || c.Origin != prev.End {
			break
		}
		n++
	}
	return n
}

// nearestLine returns the position in p.lines of the line whose bounds pt is nearest, preferring the earlier line on a
// tie. The strict comparison is what makes the tie predictable: a point exactly between two stacked lines picks the
// upper one and a point centered in a two-column gutter picks the left column, both being the earlier line in emission
// order. Non-finite geometry (which hostile content can produce) yields a NaN distance, which never wins the
// comparison, so such a line is simply never chosen.
func (p *Page) nearestLine(pt gfx.Point) int {
	best := 0
	bestDist := math.Inf(1)
	for i := range p.lines {
		if d := distanceToRect(p.lines[i].bounds, pt); d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// distanceToRect is the euclidean distance from pt to the nearest point of r, and 0 when pt is inside r.
func distanceToRect(r gfx.Rect, pt gfx.Point) float64 {
	x, y := float64(pt.X), float64(pt.Y)
	dx := max(float64(r.X0)-x, x-float64(r.X1), 0)
	dy := max(float64(r.Y0)-y, y-float64(r.Y1), 0)
	return math.Hypot(dx, dy)
}

// LineAt returns the half-open character range of the line containing index — the unit a triple-click, or a
// selection extended by lines, works in. The index is clamped into range, so Len() names the last line rather than
// nothing at all. An empty page returns (0, 0).
func (p *Page) LineAt(index int) (start, end int) {
	if p == nil || len(p.lines) == 0 {
		return 0, 0
	}
	l := p.lines[p.lineOf(index)]
	return l.start, l.end
}

// lineOf returns the position in p.lines of the line containing index, clamping index into [0, len(chars)). The runs
// partition the stream in increasing order, so a binary search is exact here — unlike the within-line scans, whose
// projections kerning can leave unsorted. The caller guarantees the page has at least one line.
func (p *Page) lineOf(index int) int {
	index = min(max(index, 0), len(p.chars)-1)
	return sort.Search(len(p.lines), func(i int) bool { return index < p.lines[i].end })
}

// WordAt returns the half-open character range of the word containing index — what a double-click selects. A word
// grows while consecutive characters stay on one line, carry a real Unicode mapping that is not whitespace, and sit
// closer together than the gap at which the matcher synthesizes a word space, so a word's boundaries here are the
// boundaries a search for that word would match at. Landing on whitespace, or on a character the font gives no
// mapping for, selects just that character. The index is clamped into range; an empty page returns (0, 0).
func (p *Page) WordAt(index int) (start, end int) {
	if p == nil || len(p.chars) == 0 {
		return 0, 0
	}
	index = min(max(index, 0), len(p.chars)-1)
	l := p.lines[p.lineOf(index)]
	if !isWordChar(p.chars[index]) {
		return index, index + 1
	}
	start, end = index, index+1
	for start > l.start && joinsWord(p.chars[start-1], p.chars[start]) {
		start--
	}
	for end < l.end && joinsWord(p.chars[end-1], p.chars[end]) {
		end++
	}
	return start, end
}

// isWordChar reports whether c can be part of a word: it must carry a Unicode mapping (Rune == 0 means the font
// provided none, and such a character never matches a needle either) and must not be whitespace.
func isWordChar(c Char) bool {
	return c.Rune != 0 && !unicode.IsSpace(c.Rune)
}

// joinsWord reports whether cur continues the word prev belongs to: both are word characters, and the gap between them
// along prev's advance direction is narrower than the one the matcher reads as an inter-word space.
func joinsWord(prev, cur Char) bool {
	return isWordChar(prev) && isWordChar(cur) && gapBetween(prev, cur) < gapSpaceEm*prev.Size
}

// Text returns the text of the characters in [start, end), carrying the spaces and line breaks the page's geometry
// implies but its character stream does not contain. The arguments are clamped into range and swapped when out of
// order, so any pair of indices names some (possibly empty) selection.
//
// Both synthesized separators are measured between STREAM neighbors — the character actually recorded before this one,
// even when that character contributed no text — because the pen positions the decision rests on are theirs: a line
// break emits a newline, and a gap of at least the matcher's word-space width emits a single space. Neither is doubled
// against whitespace the stream already carries, and neither is emitted before the first real character, where it
// would separate nothing. A character the font gives no Unicode mapping for (Rune == 0) contributes nothing to the
// text but still holds its place in the stream, so the geometry on either side of it stays honest. Ligature fillers
// need no special handling for the same reason: they sit at the pen with Origin == End, leaving the gap to whatever
// follows measured from the base glyph's advance end.
//
// A separator waits for the character that will follow it rather than being written where it was decided, so a
// selection ending on an unmapped character ends on the last letter it actually spelled instead of on a space or a
// newline separating nothing. A newline decided later supersedes a waiting space, which it implies.
func (p *Page) Text(start, end int) string {
	if p == nil {
		return ""
	}
	start, end = p.clamp(start, end)
	var buf strings.Builder
	var last, pending rune
	for i := start; i < end; i++ {
		cur := p.chars[i]
		if i > start {
			switch prev := p.chars[i-1]; {
			case lineBreakBetween(prev, cur):
				if last != 0 && last != '\n' {
					pending = '\n'
				}
			case gapBetween(prev, cur) >= gapSpaceEm*prev.Size:
				if last != 0 && !unicode.IsSpace(last) && pending == 0 {
					pending = ' '
				}
			}
		}
		if cur.Rune != 0 {
			// A newline stands whatever follows it, because the break happened; a space stands down against
			// whitespace the stream already carries, which would otherwise be doubled.
			if pending == '\n' || (pending == ' ' && !unicode.IsSpace(cur.Rune)) {
				buf.WriteRune(pending)
			}
			pending = 0
			buf.WriteRune(cur.Rune)
			last = cur.Rune
		}
	}
	return buf.String()
}

// Quads returns the highlight geometry for [start, end): one quad per line the range touches, split further exactly
// where a search hit would split, because this is the same segmentQuads the matcher assembles its hits with. A
// selected word and the same word found by Search therefore paint identically. The arguments are clamped and ordered
// as Text's are; a range covering no characters returns nil.
//
// A quad with no width is dropped. Only the letters a single glyph spells beyond the first produce one — they sit at
// that glyph's pen carrying no advance, so there is no shape there to paint and nothing a caret could be read from,
// and the glyph they belong to already paints for all of them. MuPDF drops the same quads out of a selection
// (on_highlight_char skips a character whose quad has no width) while its search path keeps them, so this is the one
// place a selection is allowed to differ from a search hit over the same characters.
func (p *Page) Quads(start, end int) []gfx.Quad {
	if p == nil {
		return nil
	}
	start, end = p.clamp(start, end)
	if start >= end {
		return nil
	}
	// The runs partition the stream in increasing order, so the range touches one contiguous span of them: lineOf
	// finds where that span starts with a binary search, and the first run beyond the range ends it. A drag asks for
	// this on every pointer move, and a page can carry thousands of lines.
	var out []gfx.Quad
	for i := p.lineOf(start); i < len(p.lines); i++ {
		l := p.lines[i]
		if l.start >= end {
			break
		}
		if s, e := max(start, l.start), min(end, l.end); s < e {
			for _, q := range segmentQuads(p.chars[s:e]) {
				if !widthless(q) {
					out = append(out, q)
				}
			}
		}
	}
	return out
}

// widthless reports whether q covers no width along the text's own direction, which makes it unpaintable whatever its
// height. The corners are compared exactly rather than within a tolerance because the quads this drops are built from
// a single point (see Device.recordFillers), and a narrow-but-real glyph must keep its quad.
func widthless(q gfx.Quad) bool {
	return q.UL == q.UR && q.LL == q.LR
}

// clamp bounds a caller's range to the page and puts it in order, so a backwards drag (end before start) or an index
// held over from a longer page names a real selection instead of indexing out of range.
func (p *Page) clamp(start, end int) (from, to int) {
	if start > end {
		start, end = end, start
	}
	return min(max(start, 0), len(p.chars)), min(max(end, 0), len(p.chars))
}
