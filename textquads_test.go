// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview_test

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/richardwilkes/pdfview/internal/content"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// TestTextQuadParity is the M6 quad-parity spike, kept as a permanent text-layout regression test: it runs
// each corpus page's content through the interpreter, computes per-character quads from our font metrics
// exactly as the structured-text device will (Trm × [0..advance, descender..ascender]), locates the goldens'
// recorded search needles with a deliberately simple matcher, and requires every quad corner to land within
// quadTolerance page-space points (= pixels at 72 dpi) of MuPDF's recorded raw quads. This pins text layout —
// widths, Trm composition, and quad metrics — long before glyph rasterization or real search exist, de-risking
// M7's exact hit rectangles. Files whose fonts the engine cannot load yet are reported but not enforced; move
// them into enforced as their font support lands.
func TestTextQuadParity(t *testing.T) {
	// Corpus files whose fonts are supported for layout: embedded TrueType (glaive), embedded Type1C/CFF
	// (the IRS forms), and standard-14 substitutes. Files with font types the engine cannot lay out yet
	// join this set as their support lands.
	enforced := map[string]bool{
		"damaged-bad-offsets": true, "damaged-no-trailer": true, "damaged-startxref-zero": true,
		"encrypted-r2-rc4": true, "encrypted-r3-rc4": true, "encrypted-r4-aes": true, "encrypted-r4-rc4": true,
		"encrypted-r6-aes": true, "encrypted-r6-empty-user": true,
		"glaive": true, "hit-quad-split": true, "irs-f1040": true, "irs-fw9": true, "rotate90": true,
		"std14-styles": true, "subst-metrics": true, "text-std14": true,
		"text-type1": true, "text-type0-cid2": true, "text-type0-cid0": true, "text-trmodes": true,
	}
	goldens, err := testsupport.LoadGoldens(filepath.Join("testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	for _, golden := range goldens {
		if len(golden.Truth.Needles) == 0 {
			continue
		}
		t.Run(golden.Name, func(t *testing.T) {
			maxErr, meanErr, quads := diffGoldenQuads(t, golden, enforced[golden.Name])
			t.Logf("%s: %d quads, corner error max %.4f / mean %.4f pt", golden.Name, quads, maxErr, meanErr)
		})
	}
}

// quadTolerance is the spike's exit criterion from plan.md: every corner within 0.5 page-space points.
const quadTolerance = 0.5

// diffGoldenQuads compares one golden's recorded search quads against our computed text layout, reporting the
// largest and mean corner errors seen. When enforce is false the differences are logged instead of failing.
func diffGoldenQuads(t *testing.T, golden *testsupport.Golden, enforce bool) (maxErr, meanErr float64, totalQuads int) {
	report := func(format string, args ...any) {
		t.Helper()
		if enforce {
			t.Errorf(format, args...)
		} else {
			t.Logf("(unenforced) "+format, args...)
		}
	}
	data, err := os.ReadFile(filepath.Join("testfiles", "corpus", golden.Truth.File))
	if err != nil {
		t.Fatal(err)
	}
	document, err := doc.Open(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if golden.Truth.RequiresAuth || golden.Truth.AuthPassword != "" {
		if document.Authenticate(golden.Truth.AuthPassword) == 0 {
			t.Fatalf("Authenticate(%q) failed", golden.Truth.AuthPassword)
		}
	}
	var errSum float64
	var corners int
	for _, page := range golden.Truth.Pages {
		if len(page.SearchRaw) == 0 {
			continue
		}
		chars := extractChars(t, document, page.Page)
		for _, needle := range sortedKeys(page.SearchRaw) {
			want := page.SearchRaw[needle]
			got := searchChars(chars, needle)
			totalQuads += len(want)
			if len(got) != len(want) {
				report("page %d needle %q: got %d quads, oracle has %d", page.Page, needle, len(got), len(want))
				continue
			}
			sortQuads(got)
			want = append([][8]float32(nil), want...)
			sortQuads(want)
			for i := range want {
				for c := range 8 {
					e := math.Abs(float64(got[i][c]) - float64(want[i][c]))
					errSum += e
					corners++
					if e > maxErr {
						maxErr = e
					}
				}
				if !quadClose(got[i], want[i], quadTolerance) {
					report("page %d needle %q quad %d:\n got %v\nwant %v", page.Page, needle, i, got[i], want[i])
				}
			}
		}
	}
	if corners > 0 {
		meanErr = errSum / float64(corners)
	}
	return maxErr, meanErr, totalQuads
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quadClose(a, b [8]float32, tol float64) bool {
	for i := range 8 {
		if math.Abs(float64(a[i])-float64(b[i])) > tol {
			return false
		}
	}
	return true
}

// sortQuads orders quads by position so ours and the oracle's pair up independent of emission order.
func sortQuads(quads [][8]float32) {
	sort.Slice(quads, func(i, j int) bool {
		a, b := quads[i], quads[j]
		for c := range 8 {
			if a[c] != b[c] {
				return a[c] < b[c]
			}
		}
		return false
	})
}

// capChar is one positioned character as the capture device saw it: its quad corners, baseline start/end,
// and em size in device units, plus the extraction rune.
type capChar struct {
	uni    rune
	quad   [8]float32 // ulx, uly, urx, ury, llx, lly, lrx, lry — the goldens' searchRaw order
	origin gfx.Point
	end    gfx.Point
	size   float32
	axis   bool // axis-aligned (no rotation/skew in Trm)
}

// textCapture records every glyph the interpreter emits, deduplicating runs emitted through several device
// verbs (fill+stroke, fill+clip, ...) by run identity.
type textCapture struct {
	device.Null
	seen  map[*device.TextRun]bool
	chars []capChar
}

func (c *textCapture) FillText(run *device.TextRun, _ device.Paint) { c.record(run) }

func (c *textCapture) StrokeText(run *device.TextRun, _ *gfx.StrokeParams, _ device.Paint) {
	c.record(run)
}

func (c *textCapture) ClipText(run *device.TextRun) { c.record(run) }

func (c *textCapture) IgnoreText(run *device.TextRun) { c.record(run) }

func (c *textCapture) record(run *device.TextRun) {
	if c.seen[run] {
		return
	}
	if c.seen == nil {
		c.seen = map[*device.TextRun]bool{}
	}
	c.seen[run] = true
	asc, desc := run.Font.Ascender(), run.Font.Descender()
	for _, g := range run.Glyphs {
		ul := g.Trm.Apply(gfx.Point{X: 0, Y: asc})
		ur := g.Trm.Apply(gfx.Point{X: g.Advance, Y: asc})
		ll := g.Trm.Apply(gfx.Point{X: 0, Y: desc})
		lr := g.Trm.Apply(gfx.Point{X: g.Advance, Y: desc})
		c.chars = append(c.chars, capChar{
			uni:    g.Unicode,
			quad:   [8]float32{ul.X, ul.Y, ur.X, ur.Y, ll.X, ll.Y, lr.X, lr.Y},
			origin: g.Trm.Apply(gfx.Point{}),
			end:    g.Trm.Apply(gfx.Point{X: g.Advance}),
			size:   float32(math.Hypot(float64(g.Trm.C), float64(g.Trm.D))),
			axis:   g.Trm.B == 0 && g.Trm.C == 0,
		})
	}
}

// extractChars interprets one page's content at scale 1 (page space) and returns the emitted characters.
func extractChars(t *testing.T, document *doc.Document, pageNumber int) []capChar {
	ctm, err := document.PageCTM(pageNumber, 1)
	if err != nil {
		t.Fatalf("PageCTM(%d): %v", pageNumber, err)
	}
	capture := &textCapture{}
	if data := document.PageContents(pageNumber); len(data) > 0 {
		content.Run(document.COS(), document.PageResources(pageNumber), data, ctm, capture, nil)
	}
	return capture.chars
}

// Matcher thresholds, in em fractions of the preceding character's size: a horizontal gap of at least
// gapSpaceEm reads as a word space (MuPDF's stext synthesizes a space there — text-std14's "Kerned Text"
// carries a 0.5 em TJ gap); baselines further apart than lineBreakEm are different lines.
const (
	gapSpaceEm  = 0.2
	lineBreakEm = 0.1
)

func isSpaceChar(c capChar) bool { return unicode.IsSpace(c.uni) }

// advanceDir is the unit vector of a character's baseline advance ((1, 0) for a degenerate advance).
func advanceDir(c capChar) (ux, uy float64) {
	dx, dy := float64(c.end.X-c.origin.X), float64(c.end.Y-c.origin.Y)
	n := math.Hypot(dx, dy)
	if n == 0 {
		return 1, 0
	}
	return dx / n, dy / n
}

// lineBreakBetween reports whether cur starts a new line: its baseline origin is offset from prev's
// perpendicular to prev's advance direction (so rotated text advancing through device y stays one line).
func lineBreakBetween(prev, cur capChar) bool {
	ux, uy := advanceDir(prev)
	dx, dy := float64(cur.origin.X-prev.origin.X), float64(cur.origin.Y-prev.origin.Y)
	return math.Abs(ux*dy-uy*dx) > float64(lineBreakEm*prev.size)
}

// gapBetween is the signed distance along prev's advance direction from prev's end to cur's origin
// (negative when kerning tucks cur backward).
func gapBetween(prev, cur capChar) float32 {
	ux, uy := advanceDir(prev)
	return float32(ux*float64(cur.origin.X-prev.end.X) + uy*float64(cur.origin.Y-prev.end.Y))
}

// searchChars finds needle in the extracted characters with the simplified fz-search-compatible rules the
// spike needs: Unicode simple case folding for letters; a needle space matches a run of extracted whitespace,
// a sufficiently large gap between characters, or a line break; matches never overlap. Each match yields one
// quad per line touched.
func searchChars(chars []capChar, needle string) [][8]float32 {
	runes := []rune(needle)
	var out [][8]float32
	for i := 0; i < len(chars); {
		quads, end, ok := matchAt(chars, i, runes)
		if ok {
			out = append(out, quads...)
			i = end
			continue
		}
		i++
	}
	return out
}

// matchAt attempts a needle match starting at chars[start], returning the per-line quads and the index just
// past the match.
func matchAt(chars []capChar, start int, needle []rune) (quads [][8]float32, end int, ok bool) {
	pos := start
	segStart := start
	var segments [][2]int
	for k := 0; k < len(needle); k++ {
		if needle[k] == ' ' {
			consumed := false
			for pos < len(chars) && isSpaceChar(chars[pos]) {
				pos++
				consumed = true
			}
			if pos > start && pos < len(chars) {
				prev, cur := chars[pos-1], chars[pos]
				switch {
				case lineBreakBetween(prev, cur):
					segments = append(segments, [2]int{segStart, pos})
					segStart = pos
					consumed = true
				case gapBetween(prev, cur) >= gapSpaceEm*prev.size:
					consumed = true
				}
			}
			if !consumed {
				return nil, 0, false
			}
			continue
		}
		if pos >= len(chars) || isSpaceChar(chars[pos]) || chars[pos].uni == 0 ||
			!strings.EqualFold(string(chars[pos].uni), string(needle[k])) {
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

// segmentQuads assembles one line's matched characters into hit quads, reproducing the oracle's grouping
// (pinned by irs-fw9 and hit-quad-split.pdf; see the M6 decision log): characters extend the current quad
// horizontally while their vertical extent stays within extentSplitFraction of the quad's height — measured
// against the extent the quad's FIRST character established, which is never stretched by later merged
// characters — and a character diverging further (such as a much larger inter-word space) closes the quad
// and starts its own. Non-axis-aligned text (rotated) keeps the first/last-corner assembly; the corpus
// exercises only uniform-extent rotated runs.
func segmentQuads(seg []capChar) [][8]float32 {
	axis := true
	for _, c := range seg {
		if !c.axis {
			axis = false
			break
		}
	}
	if !axis {
		first, last := seg[0].quad, seg[len(seg)-1].quad
		return [][8]float32{{first[0], first[1], last[2], last[3], first[4], first[5], last[6], last[7]}}
	}
	var out [][8]float32
	var top, bottom, minX, maxX float32
	open := false
	flush := func() {
		if open {
			out = append(out, [8]float32{minX, top, maxX, top, minX, bottom, maxX, bottom})
			open = false
		}
	}
	for _, c := range seg {
		cTop, cBottom := c.quad[1], c.quad[5]
		cMinX, cMaxX := min(c.quad[0], c.quad[2]), max(c.quad[0], c.quad[2])
		if open {
			limit := (bottom - top) * extentSplitFraction
			if math.Abs(float64(cTop-top)) <= float64(limit) && math.Abs(float64(cBottom-bottom)) <= float64(limit) {
				minX, maxX = min(minX, cMinX), max(maxX, cMaxX)
				continue
			}
			flush()
		}
		top, bottom, minX, maxX = cTop, cBottom, cMinX, cMaxX
		open = true
	}
	flush()
	return out
}

// extentSplitFraction is the relative vertical-extent divergence beyond which the oracle starts a new hit
// quad. Probing brackets it in (0.101, 0.113) of the current quad's height (20-pt text merged a 22.6-pt
// space and split a 22.9-pt one); 1/9 sits inside the bracket. Corpus quads all sit far from the threshold.
const extentSplitFraction = 1.0 / 9
