package font

import (
	"bytes"
	"sync"
	"unicode/utf8"

	otfont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/richardwilkes/pdfview/internal/font/data"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Glyph identification and outlines. Codes map to GIDs once at Load (simple fonts have at most 256 codes) via
// the per-font-type chains — the embedded program's cmaps for sfnt, the charset name sweep for bare CFF, and
// Unicode into the bundled Liberation face for substituted fonts. GlyphPath converts one glyph's outline to
// the em-normalized glyph space the device seam's Trm matrices expect (y up, 1.0 = one em).

// subInfo carries the bundled substitute face used to render a non-embedded (or unparseable-embedded) font.
type subInfo struct {
	face *otfont.Face
	upem float32
}

// liberationFor maps a canonical standard-14 name to the bundled Liberation family member that substitutes
// for it (plan.md: deterministic substitution, never system fonts). Symbol has no dingbat/pi-font stand-in
// yet; it renders through LiberationSans by Unicode value, which covers its Greek and most of its operators.
// ZapfDingbats resolves no Unicode from its aN glyph names, so it produces no outlines until a dingbat-capable
// bundle lands (decision log).
func liberationFor(std14 string) string {
	family, style := "LiberationSans", "Regular"
	switch std14 {
	case stdTimesRoman, stdTimesBold, stdTimesItalic, stdTimesBoldItalic:
		family = "LiberationSerif"
	case stdCourier, stdCourierBold, stdCourierOblique, stdCourierBoldOblique:
		family = "LiberationMono"
	}
	switch std14 {
	case stdHelveticaBold, stdTimesBold, stdCourierBold:
		style = "Bold"
	case stdHelveticaOblique, stdTimesItalic, stdCourierOblique:
		style = "Italic"
	case stdHelveticaBoldOblique, stdTimesBoldItalic, stdCourierBoldOblique:
		style = "BoldItalic"
	}
	return family + "-" + style
}

// libFonts caches the parsed Liberation font programs (shared, immutable go-text Fonts; the per-Font Faces
// wrapping them are never shared because Face caches lookups without locking).
var (
	libFontsMu sync.Mutex
	libFonts   = map[string]*otfont.Font{}
)

// liberationFont returns the parsed bundled font, or nil when unavailable.
func liberationFont(name string) *otfont.Font {
	libFontsMu.Lock()
	defer libFontsMu.Unlock()
	if ft, ok := libFonts[name]; ok {
		return ft
	}
	var ft *otfont.Font
	if raw := data.Liberation(name); raw != nil {
		if face, err := otfont.ParseTTF(bytes.NewReader(raw)); err == nil {
			ft = face.Font
		}
	}
	libFonts[name] = ft // Negative results cached too.
	return ft
}

// loadSubstitute builds the substitute shape source for a non-embedded font.
func loadSubstitute(std14 string) *subInfo {
	ft := liberationFont(liberationFor(std14))
	if ft == nil {
		return nil
	}
	upem := float32(ft.Upem())
	if upem <= 0 {
		return nil
	}
	return &subInfo{face: otfont.NewFace(ft), upem: upem}
}

// macRomanReverse maps glyph names to Mac Roman codes for the sfnt (1,0) cmap chain, inverted once from the
// generated MacRomanEncoding table (first code wins; the table has no duplicate names).
var (
	macRomanReverseOnce sync.Once
	macRomanReverse     map[string]uint32
)

func macRomanCode(name string) (uint32, bool) {
	macRomanReverseOnce.Do(func() {
		macRomanReverse = make(map[string]uint32, 256)
		for code, n := range &macRomanEncoding {
			if n != "" {
				if _, exists := macRomanReverse[n]; !exists {
					macRomanReverse[n] = uint32(code)
				}
			}
		}
	})
	code, ok := macRomanReverse[name]
	return code, ok
}

// symbolicFlags reports whether the descriptor flags mark the font symbolic (bit 3 set and bit 6, its
// mutually exclusive partner, clear — fonts claiming both are treated as non-symbolic).
func symbolicFlags(flags int) bool {
	return flags&FlagSymbolic != 0 && flags&FlagNonsymbolic == 0
}

// firstRune returns the first rune of s, 0 when s is empty.
func firstRune(s string) rune {
	if s == "" {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// buildGIDs precomputes the code→GID table for a simple font once its encoding, embedded program, and
// substitute are all settled. Codes that map nowhere stay 0 (.notdef). Substituted fonts map only through
// AGL-resolved glyph names — never the ASCII-identity Unicode fallback the extraction table carries — because
// a name the AGL cannot resolve (ZapfDingbats' aN names, private-use names) means the substitute has no
// version of that glyph; drawing the fallback letterform would be the WRONG glyph, worse than none.
func (f *Font) buildGIDs() {
	symbolic := symbolicFlags(f.Flags)
	for code := range uint32(256) {
		switch {
		case f.sfnt != nil:
			f.gids[code] = f.sfnt.gid(code, f.GlyphName(code), symbolic)
		case f.cff != nil:
			f.gids[code] = f.cff.gid(code, f.GlyphName(code))
		case f.t1 != nil:
			f.gids[code] = f.t1.gid(f.GlyphName(code))
		case f.sub != nil:
			if r := firstRune(GlyphNameToUnicode(f.GlyphName(code))); r != 0 {
				if g, ok := f.sub.face.Cmap.Lookup(r); ok {
					f.gids[code] = uint32(g)
				}
			}
		}
	}
}

// GID returns the font-program glyph index the code renders with (0 when unmapped — .notdef).
func (f *Font) GID(code uint32) uint32 {
	if f.type0 != nil {
		if f.sub != nil {
			// A substituted (non-embedded) composite font has no CID→GID program; mapping through the
			// substitute's cmap needs Unicode, which ToUnicode supplies when present.
			if r := f.Unicode(code); r != 0 {
				if g, ok := f.sub.face.Cmap.Lookup(r); ok {
					return uint32(g)
				}
			}
			return 0
		}
		return f.type0.gid(f.type0.cmap.cid(code))
	}
	if code < 256 {
		return f.gids[code]
	}
	return 0
}

// GlyphPath returns the glyph's outline in em-normalized glyph space (y up, advance 1.0 = one em), or nil
// when the glyph has no outline source (substituted fonts with no shape for it, hostile font programs, pure
// spacing glyphs report an empty — non-nil — path only when the program defines an empty outline). The
// result is freshly built on each call; the raster device caches converted paths per (font, GID).
func (f *Font) GlyphPath(gid uint32) (p *gfx.Path) {
	defer func() {
		if recover() != nil { // Hostile font programs must degrade to a missing glyph, never break the render.
			p = nil
		}
	}()
	if gid > 0xFFFF { // sfnt and CFF glyph indices are 16-bit.
		return nil
	}
	if f.type3 != nil {
		return nil // Type 3 glyphs are content streams; the interpreter executes them (no outlines exist).
	}
	if f.type0 != nil && f.type0.sfnt != nil {
		// CIDFontType2: always the direct glyf walker — CID TrueType subsets routinely lack the cmap table
		// go-text's Font layer requires (M6 decision-log warning), and one deterministic outline path beats
		// two.
		if f.type0.sfnt.glyf != nil {
			return f.type0.sfnt.glyf.path(gid)
		}
		return nil
	}
	switch {
	case f.sfnt != nil:
		if f.sfnt.face != nil {
			outline, ok := f.sfnt.face.GlyphDataOutline(tables.GlyphID(gid))
			if !ok || f.sfnt.upem <= 0 {
				return nil
			}
			return segmentsToPath(outline.Segments, gfx.Scale(1/f.sfnt.upem, 1/f.sfnt.upem))
		}
		if f.sfnt.glyf != nil { // cmap-less simple TrueType: the direct walker renders the embedded shapes.
			return f.sfnt.glyf.path(gid)
		}
		return nil
	case f.cff != nil:
		segs, _, err := f.cff.font.LoadGlyph(tables.GlyphID(gid))
		if err != nil {
			return nil
		}
		m := f.cff.matrix
		return segmentsToPath(segs, gfx.Matrix{A: m[0], B: m[1], C: m[2], D: m[3], E: m[4], F: m[5]})
	case f.t1 != nil:
		return f.t1.glyphPath(gid)
	case f.sub != nil:
		if gid == 0 {
			// A substituted code that mapped nowhere renders nothing: the substitute's .notdef box would be
			// ink the original font never had (the embedded cases keep gid 0 — MuPDF draws an embedded
			// program's own .notdef).
			return nil
		}
		outline, ok := f.sub.face.GlyphDataOutline(tables.GlyphID(gid))
		if !ok {
			return nil
		}
		return segmentsToPath(outline.Segments, gfx.Scale(1/f.sub.upem, 1/f.sub.upem))
	default:
		return nil
	}
}

// programAdvance returns the embedded program's advance for a glyph in em units (the /Widths-absent fallback;
// plan.md width rules), reporting false when no program supplies one.
func (f *Font) programAdvance(gid uint32) (float32, bool) {
	if f.sfnt != nil && f.sfnt.face != nil && f.sfnt.upem > 0 {
		return f.sfnt.face.HorizontalAdvance(opentype.GID(gid)) / f.sfnt.upem, true
	}
	if f.t1 != nil {
		return f.t1.advance(gid)
	}
	return 0, false
}

// segmentsToPath converts go-text outline segments (font units, y up) to a gfx.Path under m.
func segmentsToPath(segs []opentype.Segment, m gfx.Matrix) *gfx.Path {
	p := &gfx.Path{}
	open := false
	for _, seg := range segs {
		a := m.Apply(gfx.Point{X: seg.Args[0].X, Y: seg.Args[0].Y})
		switch seg.Op {
		case opentype.SegmentOpMoveTo:
			if open {
				p.Close() // Glyph contours are implicitly closed.
			}
			p.MoveTo(a.X, a.Y)
			open = true
		case opentype.SegmentOpLineTo:
			if open {
				p.LineTo(a.X, a.Y)
			}
		case opentype.SegmentOpQuadTo:
			if open {
				b := m.Apply(gfx.Point{X: seg.Args[1].X, Y: seg.Args[1].Y})
				p.QuadTo(a.X, a.Y, b.X, b.Y)
			}
		case opentype.SegmentOpCubeTo:
			if open {
				b := m.Apply(gfx.Point{X: seg.Args[1].X, Y: seg.Args[1].Y})
				c := m.Apply(gfx.Point{X: seg.Args[2].X, Y: seg.Args[2].Y})
				p.CubicTo(a.X, a.Y, b.X, b.Y, c.X, c.Y)
			}
		}
	}
	if open {
		p.Close()
	}
	return p
}
