// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

import (
	"image"

	"github.com/richardwilkes/pdfview/internal/stext"
)

// TextPage is one page's extracted text, ready to hit-test, select, and copy. It is what a viewer needs to put a
// caret in a page and drag a selection across it: character indices to address positions with, words and lines to snap
// to, the text of any range, and the rectangles to paint that range with.
//
// # Coordinates
//
// Every point going in and every rectangle coming out is in the pixel space of the image RenderPage produces for this
// page at the dpi the TextPage was created with — the same space RenderedPage.SearchHits and PageLink.Bounds are
// reported in. A TextPage labeled for one image cannot be used with another, so AtDPI and ForSize hand back the same
// text labeled for the image RenderPage or RenderPageForSize produces at another size. ForSize is the only way to
// address a fit-to-box image: it scales by min(maxWidth/width, maxHeight/height), which is no whole dpi. Neither
// method extracts anything — the extraction itself is scale-free and the two pages share it — so a viewer extracts
// once per page and re-labels per zoom level.
//
// # What is extracted
//
// Exactly what search sees, because it is the same pass: text is collected unclipped, including text scissored away by
// a clip path, invisible text (render mode 3), and the text of annotation appearance streams. What is searchable is
// what is selectable. Characters arrive in the order the page's content streams draw them, which is reading order for
// a well-formed document and is not guaranteed to be for any other; nothing here re-flows a page into columns or
// paragraphs. A character the font gives no Unicode mapping for still occupies an index and still contributes its
// geometry to a highlight, but contributes nothing to Text. A glyph that spells more than one character — a ligature,
// or a one-to-many /ToUnicode mapping — occupies one index per character it spells, so "ﬁ" is two indices reading
// "fi", and the extra characters carry no advance of their own, sitting at the end of the glyph that drew them (IndexAt
// shares the glyph's width among them, so a caret can still land between them).
//
// # Lifetime and safety
//
// A TextPage holds no reference to the Document it came from: it keeps working, unchanged, after Release. All of its
// methods are safe to call from multiple goroutines at once (it is immutable once built), and all of them tolerate a
// nil receiver, answering as an empty page would. Ranges are half-open [start, end) pairs of character indices; every
// method clamps its arguments into range and swaps a reversed pair, so no index a caller can supply — from a stale
// page, a backwards drag, or arithmetic gone wrong — can panic.
type TextPage struct {
	// text is the extracted page itself, shared by every labeling of it; space is the pixel space this labeling
	// reports its rectangles in and reads its points from; pg is the page extent that space was sized from, kept so
	// AtDPI and ForSize can size another one without the document.
	text  *stext.Page
	space pixelSpace
	pg    page
}

// newTextPage labels an extracted page with the pixel space a render under spec produces. The bounds come from the
// same sizing authority the render itself uses (renderSpec.extents), so a highlight can never name a pixel the image
// does not have, and text is shared rather than copied: a *stext.Page is immutable, so every labeling of one page
// reads the same characters.
func newTextPage(text *stext.Page, pg *page, spec renderSpec) *TextPage {
	width, height := spec.extents(pg)
	return &TextPage{
		text:  text,
		space: pixelSpace{scale: spec.scale, bounds: image.Rect(0, 0, width, height)},
		pg:    *pg,
	}
}

// TextPage extracts the text of the given 0-based page for hit-testing and selection at the given dpi, which fixes the
// pixel space its points and rectangles are in (see TextPage's own documentation). It returns ErrDocumentReleased if
// the document has been released, ErrInvalidPageNumber for an out-of-range page, and ErrUnableToLoadPage when the page
// cannot be loaded or its geometry cannot be read — a page with no text at all is not a failure, and comes back as an
// empty TextPage.
//
// This runs the page's content through the interpreter a second time, so it costs about what the text portion of a
// render of the same page costs, and it holds the document lock for the whole pass. Call it off a latency-sensitive
// thread, and keep the result rather than re-extracting per interaction: a TextPage is immutable and outlives the
// document, so one per page — re-labeled with AtDPI or ForSize as the zoom changes, neither of which needs the
// document or its lock — serves every caret, selection, and copy on that page.
func (d *Document) TextPage(pageNumber, dpi int) (*TextPage, error) {
	if !d.usable() {
		return nil, ErrDocumentReleased
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return nil, ErrDocumentReleased
	}
	if pageNumber < 0 || pageNumber >= d.eng.pageCount() {
		return nil, ErrInvalidPageNumber
	}
	pg, err := d.eng.loadPage(pageNumber)
	if err != nil {
		return nil, err
	}
	// The extraction pass itself runs at scale 1 (page space); the render scale is carried alongside it and applied to
	// the coordinates on the way out, which is the funnel every other coordinate in this package goes through.
	text, err := d.eng.extractText(pg)
	if err != nil {
		return nil, err
	}
	return newTextPage(text, pg, renderSpec{scale: dpiToScale(dpi)}), nil
}

// AtDPI returns the same extracted text labeled for the image RenderPage produces for this page at the given dpi. The
// receiver is unchanged and the two pages share their characters, so re-labeling costs no extraction pass, no document
// lock, and no second copy of the page's text — which is what makes a TextPage per page, rather than per zoom level,
// the right thing for a viewer to hold. A nil receiver returns nil, which answers every method as an empty page does.
func (t *TextPage) AtDPI(dpi int) *TextPage {
	if t == nil {
		return nil
	}
	return newTextPage(t.text, &t.pg, renderSpec{scale: dpiToScale(dpi)})
}

// ForSize returns the same extracted text labeled for the image RenderPageForSize produces for this page within
// maxWidth×maxHeight. That image is scaled to fit the box rather than to a dpi, so AtDPI cannot name its pixel space
// and this is the method to address it with. It costs what AtDPI costs: no extraction pass and no document lock.
//
// It reports the same failures RenderPageForSize reports for the same box: ErrInvalidPageSize when either extent is
// not positive (or the page has no extent of its own), and ErrImageTooLarge when the fitted image would exceed
// OverallMaxPixels. A nil receiver reports ErrInvalidPageSize, which is what an empty page reports.
func (t *TextPage) ForSize(maxWidth, maxHeight int) (*TextPage, error) {
	if t == nil {
		return nil, ErrInvalidPageSize
	}
	spec, err := fitSpec(&t.pg, maxWidth, maxHeight)
	if err != nil {
		return nil, err
	}
	return newTextPage(t.text, &t.pg, spec), nil
}

// Len returns the number of characters on the page. Indices run from 0 through Len() inclusive: Len() is the insertion
// point past the last character, not a character. A page with no text returns 0.
func (t *TextPage) Len() int {
	if t == nil {
		return 0
	}
	return t.text.Len()
}

// IndexAt returns the insertion index in [0, Len()] nearest the given point of the rendered image. It answers the way
// a text editor answers a click: the nearest line is found first and the point is then placed between two of that
// line's characters, so a click in the left half of a two-column gutter lands at the end of the left column rather
// than the start of the right — the nearer column wins, and the earlier one wins a tie — and a click past the edge of
// the page lands at the near end of the nearest line rather than nowhere. A
// glyph that spells several characters shares its width evenly among them, so a click at the trailing edge of "ﬁ"
// lands after both letters and one at its center lands between them. A page with no text returns 0.
func (t *TextPage) IndexAt(pt image.Point) int {
	if t == nil {
		return 0
	}
	return t.text.IndexAt(t.space.unscalePoint(pt))
}

// WordAt returns the half-open range of the word containing index — what a double-click selects. A word runs while
// consecutive characters stay on one line, carry a real Unicode mapping that is not whitespace, and sit closer
// together than the gap that reads as an inter-word space, so its boundaries are the ones a search for that word would
// match at. An index on whitespace, or on a character with no Unicode mapping, selects just that character. A page
// with no text returns (0, 0).
func (t *TextPage) WordAt(index int) (start, end int) {
	if t == nil {
		return 0, 0
	}
	return t.text.WordAt(index)
}

// LineAt returns the half-open range of the line containing index — what a triple-click selects, and the unit a
// selection extended by lines grows in. A line here is a run of characters the page's own geometry keeps on one
// baseline, not a paragraph or a sentence. A page with no text returns (0, 0).
func (t *TextPage) LineAt(index int) (start, end int) {
	if t == nil {
		return 0, 0
	}
	return t.text.LineAt(index)
}

// Text returns the text of the characters in [start, end) — what a copy of that selection should put on the clipboard.
// Spaces and line breaks the page shows through geometry rather than through characters are synthesized: a gap wide
// enough to read as an inter-word space becomes one space, and a baseline change becomes one newline, neither doubled
// against whitespace the page already carries. Characters the font gives no Unicode mapping for are skipped, and a
// glyph spelling several characters contributes all of them.
func (t *TextPage) Text(start, end int) string {
	if t == nil {
		return ""
	}
	return t.text.Text(start, end)
}

// Highlights returns the rectangles to paint the selection [start, end) with, in the pixel space of the rendered
// image: one per line the range touches, split further where the text's vertical extent changes sharply within a line.
// They are grouped and rounded as RenderedPage.SearchHits is, so selecting a word and searching for it paint the same
// shape.
//
// A rectangle enclosing no pixels is dropped rather than returned, which is the one way this differs from SearchHits.
// The characters that produce one are the letters a single glyph spells beyond the first — they sit at that glyph's
// pen with no advance of their own, so their rectangle has no width to paint and no position worth reading a caret
// from. MuPDF drops the same quads when it highlights a selection. A range covering no characters, or none that paint
// anything, returns nil.
func (t *TextPage) Highlights(start, end int) []image.Rectangle {
	if t == nil {
		return nil
	}
	quads := t.text.Quads(start, end)
	if len(quads) == 0 {
		return nil
	}
	rects := make([]image.Rectangle, 0, len(quads))
	for _, q := range quads {
		if r := quadToRect(quadFromGfx(q), t.space); !r.Empty() {
			rects = append(rects, r)
		}
	}
	if len(rects) == 0 {
		return nil
	}
	return rects
}
