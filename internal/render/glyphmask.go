// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package render

import (
	"math"

	"github.com/richardwilkes/canvas/canvas"
	"github.com/richardwilkes/canvas/colorcore"
	"github.com/richardwilkes/canvas/imagecore"
	"github.com/richardwilkes/canvas/path"
	"github.com/richardwilkes/canvas/raster"
	"github.com/richardwilkes/canvas/shaders"
	"github.com/richardwilkes/canvas/surface"

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/store"
)

// The glyph coverage cache: filling every glyph outline through the analytic-AA rasterizer on every render dominated
// the profile, so ordinary fill-mode text instead rasterizes each distinct glyph appearance ONCE into an Alpha8
// coverage plane and blits it at integer device positions — the same idea as MuPDF's glyph bitmap cache. A cache entry
// is keyed by the glyph identity plus the FULL float32 Trm linear part and the exact subpixel phase of the glyph
// origin, so a cached blit reproduces the coverage the direct fill would have produced bit-for-bit (the mask is
// rendered by the same analytic-AA fill at the same subpixel position; only the final color application can differ by
// ±1 rounding — see TestGlyphBlitMatchesDirectFill). No quantization means the first render of a page mostly misses
// (each glyph instance has its own x phase) and re-renders hit 100%; that is exactly the warm protocol both the
// recorded perf numbers and real consumers (re-render on scroll/zoom) care about, and it keeps the pixel gates honest.
// Entries live in the document's budgeted store when one is wired (kind-separated by the dedicated key type), else in a
// per-render map. Neither cache's occupancy may steer a glyph onto a different path — see glyphMask for why cache state
// has to stay invisible to the pixels.

// maxGlyphMaskDim caps a cached coverage plane's extent; glyphs rendering larger than this (display-size text) fall
// back to the merged-outline fill, whose cost is amortized over the few such glyphs a page has.
const maxGlyphMaskDim = 256

// Stem darkening: exact analytic-AA coverage renders a stem narrower than a pixel as a smear of mid grays, so at body
// sizes text comes out visibly lighter than the platform rasterizers users compare against — CoreGraphics dilates glyph
// outlines ("font smoothing"), and FreeType darkens CFF stems, for exactly this reason. When enabled, glyph fills are
// drawn stroke-and-fill with a sub-pixel pen so sub-pixel features saturate to full ink while letterform geometry stays
// within half the pen width of exact. The width below was fitted against macOS Preview renders of embedded-CFF body
// text at 72/150/300 dpi (ink coverage matches within 1% relative at each): linear in the glyph's device pixel size at
// small sizes, capped at one device pixel so display sizes gain only a hair of weight. Stroking is per glyph appearance
// and cached with the coverage plane, so warm renders pay nothing.
const (
	// stemDarkenPerPpem is the pen width per device pixel of em: 8 pt text at 300 dpi (33 ppem) gets a 0.5 px pen.
	stemDarkenPerPpem = 0.015
	// stemDarkenMaxPx caps the pen for display sizes (measured against Preview: its dilation stops growing around a
	// device pixel).
	stemDarkenMaxPx = 1.0
)

// stemDarkenWidth returns the stroke-and-fill pen width, in device pixels, dilating a glyph outline whose glyph→device
// linear part is (a, b, c, d) — zero when darkening is off or the transform is degenerate. sqrt|det| is the geometric
// mean of the glyph's device pixel size (its ppem for an unrotated square transform), which keeps the width stable
// under rotation and reasonable under skew. Computed in float64 so finite float32 inputs cannot overflow the products.
func (d *Device) stemDarkenWidth(ma, mb, mc, md float32) float32 {
	if !d.stemDarkening {
		return 0
	}
	det := math.Abs(float64(ma)*float64(md) - float64(mb)*float64(mc))
	w := stemDarkenPerPpem * math.Sqrt(det)
	if !(w > 0) { // Degenerate or NaN transforms fall back to the exact fill.
		return 0
	}
	if w > stemDarkenMaxPx {
		w = stemDarkenMaxPx
	}
	return float32(w)
}

// runStemDarkenWidth returns the dilation pen width for a whole run: the glyphs of a run share their Trm linear part
// (only the origin advances within one show-text operator), so the first finite glyph speaks for all of them.
func (d *Device) runStemDarkenWidth(run *device.TextRun) float32 {
	if !d.stemDarkening {
		return 0
	}
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		if g.Trm.IsFinite() {
			return d.stemDarkenWidth(g.Trm.A, g.Trm.B, g.Trm.C, g.Trm.D)
		}
	}
	return 0
}

// applyStemDarkening turns a fill paint into the equivalent stroke-and-fill with pen width w (a no-op for w <= 0). The
// stroker merges the pen outline and the source path into one geometry filled in a single pass, so coverage saturates
// rather than double-blending — translucent text composites exactly once. Round joins keep thin-stem corners from
// growing miter spikes; the butt cap stays, so degenerate open contours that fill to nothing still draw nothing.
func applyStemDarkening(p *canvas.Paint, w float32) {
	if w <= 0 {
		return
	}
	p.Style = canvas.StyleStrokeAndFill
	p.StrokeWidth = w
	p.Join = canvas.JoinRound
}

// maxGlyphMaskBytes caps the live coverage planes the per-device cache holds. Bounding this cache by entry count is not
// a bound on its memory: one plane may be maxGlyphMaskDim² = 64 KiB, so a few thousand entries is hundreds of
// megabytes. At 16 MiB the cap holds tens of thousands of ordinary text-size glyphs — well past what any real page
// draws — while a page of display-size glyphs costs 16 MiB rather than a quarter of a gigabyte.
const maxGlyphMaskBytes = 16 << 20

// glyphMaskKey identifies one cached glyph coverage plane: glyph identity, the Trm linear part, the subpixel phase of
// the glyph origin, and whether stem darkening dilated it. Distinct store key type per the store's kind-separation
// rule. dark must be part of the key even though the darkening width is derived from a–d: the same document can render
// with darkening toggled between renders (the per-device map and the store both outlive a render), and a plane
// rasterized under one setting must never satisfy a lookup under the other.
type glyphMaskKey struct {
	font   *font.Font
	gid    uint32
	a, b   float32
	c, d   float32
	fx, fy float32
	dark   bool
}

// glyphMask is one cached coverage plane. plane is nil for glyphs the blit path cannot handle (too large or
// unrasterizable) — a cached "use the outline fill" verdict. img wraps the same coverage as a canvas image for the
// DrawImage route, created lazily since the direct composite (the overwhelmingly common route) reads plane straight.
type glyphMask struct {
	img   *imagecore.Image
	plane []byte
	w, h  int32
	ox    int32 // mask top-left relative to the glyph origin's floored device position
	oy    int32
}

// image returns the mask's canvas image, wrapping the coverage plane on first use.
func (m *glyphMask) image() *imagecore.Image {
	if m.img == nil {
		info := imagecore.ImageInfo{
			Width:     m.w,
			Height:    m.h,
			ColorType: imagecore.ColorTypeAlpha8,
			AlphaType: imagecore.AlphaTypePremul,
		}
		m.img = imagecore.NewRasterData(info, m.plane, int(m.w))
	}
	return m.img
}

// glyphMaskSize estimates a mask's cache footprint for the store budget.
func glyphMaskSize(w, h int) uint64 { return uint64(w*h) + 96 }

// blitTextRun is FillText's fast path: draw the run's glyphs from cached coverage planes. It handles only plain solid
// fills — an opaque folded color, Normal blend, no pattern/shading paint, not inside a knockout group (whose BlendSrc
// rewrite must not clear pixels around the glyph) — and reports false otherwise so the caller runs the merged-outline
// path. Glyphs the mask path cannot handle (oversized, degenerate) accumulate into a leftover outline filled exactly
// like the slow path.
func (d *Device) blitTextRun(run *device.TextRun, p device.Paint) bool {
	if p.Shading != nil || p.Tiling != nil || p.Blend != device.BlendNormal || d.knockoutSrc() {
		return false
	}
	alpha := p.Alpha
	if alpha < 0 {
		alpha = 0
	} else if alpha > 1 {
		alpha = 1
	}
	if uint8(alpha*float64(p.Color.A)+0.5) != 255 {
		// Translucent text composites per glyph differently from the merged outline where fringes overlap; keep the
		// pinned merged behavior for it.
		return false
	}
	// With no group or soft-mask layer open, no untracked canvas state, and every open clip level an axis-aligned
	// rectangle, the canvas draw target is the base surface at identity, and a glyph whose mask rect stays inside the
	// tracked clip interior composites identically whether canvas draws it or we do — so composite straight into the
	// surface pixmap. canvas's Alpha8 image draws always run the general float shader pipeline (no sprite fast path
	// exists for Alpha8), which the profile showed dominating warm renders. Everything else goes through DrawImage so
	// canvas applies clip and layer.
	interior := d.clipInterior()
	direct := interior.rect && len(d.groupStack) == 0 && len(d.maskStack) == 0 && d.untrackedState == 0
	paint := canvas.NewPaint()
	paint.Color = colorcore.ARGB(255, p.Color.R, p.Color.G, p.Color.B)
	// The blit rectangle is pixel-aligned; all antialiasing lives in the mask's coverage values.
	paint.AntiAlias = false
	sampling := shaders.SamplingOptions{Filter: shaders.FilterNearest}
	var leftover *path.Path
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		gp := d.glyphPath(run.Font, g.GID)
		if gp == nil {
			continue
		}
		if !g.Trm.IsFinite() {
			continue
		}
		ox := float32(math.Floor(float64(g.Trm.E)))
		oy := float32(math.Floor(float64(g.Trm.F)))
		mask := d.glyphMask(run.Font, g, gp, g.Trm.E-ox, g.Trm.F-oy)
		// Clamp the glyph origin exactly where every sibling float→int site here does. Trm passed IsFinite above, but
		// ox/oy can still be finite ~3.4e38, so int(ox)/int(oy) below would overflow (saturating, architecture-defined).
		// A glyph whose device origin is that far out cannot go through the mask blit; fold it into the leftover outline
		// like any other glyph the fast path declines. Mirrors renderGlyphMask's 1<<24 bounds clamp.
		const maxReasonable = 1 << 24
		originOverflow := ox < -maxReasonable || ox > maxReasonable || oy < -maxReasonable || oy > maxReasonable
		if mask == nil || mask.plane == nil || originOverflow {
			// The glyphs arriving here are exactly the ones renderGlyphMask declined, which includes declining
			// BECAUSE the outline's transformed corners were non-finite or past 1<<24 — so the same bounds test the
			// merged-outline path applies (textOutline) has to apply here too. Without it a glyph the slow path skips
			// crosses into canvas with ±Inf/NaN coordinates on the fast path, which every ordinary opaque solid fill
			// takes, and buildPath's "no non-finite geometry crosses this seam" guarantee would hold on only one of
			// the two paths that draw the same run.
			if b := gp.Bounds(); !rectFiniteUnder(b.Left, b.Top, b.Right, b.Bottom, g.Trm) {
				continue
			}
			if leftover == nil {
				leftover = path.New()
			}
			m := matrix(g.Trm)
			leftover.AddPathMatrix(gp, &m, path.AddPathAppend)
			continue
		}
		bx0 := int(ox) + int(mask.ox)
		by0 := int(oy) + int(mask.oy)
		if direct && bx0 >= interior.x0 && by0 >= interior.y0 &&
			bx0+int(mask.w) <= interior.x1 && by0+int(mask.h) <= interior.y1 {
			d.compositeMask(mask, bx0, by0, p.Color.R, p.Color.G, p.Color.B)
			continue
		}
		if img := mask.image(); img != nil {
			d.c.DrawImage(img, ox+float32(mask.ox), oy+float32(mask.oy), sampling, paint)
		}
	}
	if leftover != nil && !leftover.IsEmpty() {
		if cpaint, ok := d.preparePaint(p, nil); ok {
			// The same run-level pen the merged-outline path applies (FillText), so a glyph declined by the mask path
			// darkens exactly as it would have on the slow path.
			applyStemDarkening(cpaint, d.runStemDarkenWidth(run))
			d.c.DrawPath(leftover, cpaint)
		}
	}
	return true
}

// glyphMask returns the cached coverage plane for one glyph appearance, rendering it on first use. fx, fy are the
// subpixel phase of the glyph origin in [0, 1).
//
// Which glyphs take the mask path can never depend on how much the cache is holding: the store is a pure cache, and a
// budget of any size — down to one byte, where nothing is ever retained — must leave rendered output byte-identical
// (see the store package comment, pinned by TestCacheBudget). Feeding hit rate or retention back into the choice would
// break exactly that, since a blit and the merged-outline fill it replaces agree only within ±1 of compositing
// rounding, so a cache-driven fallback would make a page's pixels a function of its budget. The cost of a miss is
// therefore kept as low as it can be rather than avoided: everything a miss touches beyond the coverage plane it must
// produce — the scratch surface, its clear, the transformed outline path, the fill paint, the canvas image wrapper —
// is reused, deferred or done in place, leaving the analytic-AA fill (which the merged outline would pay anyway) as
// nearly the whole of it.
func (d *Device) glyphMask(f *font.Font, g *device.Glyph, gp *path.Path, fx, fy float32) *glyphMask {
	key := glyphMaskKey{
		font: f, gid: g.GID, a: g.Trm.A, b: g.Trm.B, c: g.Trm.C, d: g.Trm.D, fx: fx, fy: fy,
		dark: d.stemDarkening,
	}
	st := d.maskStore()
	if st != nil {
		if v, ok := st.Get(key); ok {
			if m, isMask := v.(*glyphMask); isMask {
				return m
			}
			return nil
		}
	} else if m, ok := d.glyphMasks[key]; ok {
		return m
	}
	mask, size := d.renderGlyphMask(g, gp, fx, fy)
	if st != nil {
		st.Put(key, mask, size)
		return mask
	}
	if d.glyphMasks == nil {
		d.glyphMasks = make(map[glyphMaskKey]*glyphMask)
	}
	if d.glyphMaskBytes+size > maxGlyphMaskBytes {
		// The map has no eviction of its own, and simply refusing further entries retires the cache for the rest of the
		// render: every later glyph appearance would miss, rebuild its plane, and throw it away, so a text-heavy page
		// pays the miss cost on every remaining glyph with no prospect of a hit. Dropping the map instead keeps the cap
		// on live planes while letting the page go on caching what it draws next. The cap is on BYTES, not entries:
		// planes differ in size by three orders of magnitude, so an entry count bounds nothing about the memory held.
		// Retention is invisible to output: a hit reproduces the plane a miss would have rendered, bit for bit.
		clear(d.glyphMasks)
		d.glyphMaskBytes = 0
	}
	d.glyphMasks[key] = mask
	d.glyphMaskBytes += size
	return mask
}

// maskStore returns the store coverage planes cache in, or nil when they cache in the per-device map instead.
//
// The document store holds them only when it carries a byte budget to evict them under. A plane's key is the glyph
// identity plus the full float32 Trm linear part and the exact subpixel phase of its origin, so distinct keys
// accumulate with GLYPHS DRAWN rather than with the distinct resources the rest of the store holds — and under the
// unlimited budget New(buffer, 0) selects, nothing in the store is ever evicted, so "no limit" would come to mean
// memory proportional to every glyph the document has ever rendered. The per-device map bounds itself
// (maxGlyphMaskBytes) and, with a store wired, survives Reset, so the re-render-at-the-same-size protocol the blit path
// exists for still hits — and a re-render at a DIFFERENT size could not have hit either way, since the Trm in the key
// changes with the scale.
func (d *Device) maskStore() *store.Store {
	if d.store == nil || d.store.Max() == 0 {
		return nil
	}
	return d.store
}

// renderGlyphMask fills the glyph outline at its exact subpixel position into a scratch surface and captures the
// coverage as an Alpha8 image. The mask carries the glyph's device bounding box relative to its floored origin, padded
// a pixel so analytic-AA bleed is never clipped. Returns the mask (plane nil when the glyph must use the outline fill)
// and its store size estimate.
func (d *Device) renderGlyphMask(g *device.Glyph, gp *path.Path, fx, fy float32) (mask *glyphMask, size uint64) {
	local := gfx.Matrix{A: g.Trm.A, B: g.Trm.B, C: g.Trm.C, D: g.Trm.D, E: fx, F: fy}
	b := gp.Bounds()
	var minX, minY, maxX, maxY float32
	for i, corner := range [4][2]float32{{b.Left, b.Top}, {b.Right, b.Top}, {b.Left, b.Bottom}, {b.Right, b.Bottom}} {
		x, y := local.ApplyXY(corner[0], corner[1])
		if i == 0 {
			minX, maxX, minY, maxY = x, x, y, y
		} else {
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
		}
	}
	if !isFinite32(minX) || !isFinite32(minY) || !isFinite32(maxX) || !isFinite32(maxY) {
		return &glyphMask{}, 96
	}
	// Reject degenerate text matrices whose device bounds, though finite, dwarf any real glyph: the floor/ceil below
	// would otherwise overflow int (architecture-defined, saturating to MinInt64 on amd64) and the w/h subtraction
	// could wrap back into a small positive value that slips past the maxGlyphMaskDim gate as an all-zero plane,
	// silently dropping the glyph instead of taking the outline fallback. Mirrors rectInterior's 1<<24 clamp.
	const maxReasonable = 1 << 24
	if minX < -maxReasonable || minY < -maxReasonable || maxX > maxReasonable || maxY > maxReasonable {
		return &glyphMask{}, 96
	}
	mx0 := int(math.Floor(float64(minX))) - 1
	my0 := int(math.Floor(float64(minY))) - 1
	w := int(math.Ceil(float64(maxX))) + 1 - mx0
	h := int(math.Ceil(float64(maxY))) + 1 - my0
	if w <= 0 || h <= 0 || w > maxGlyphMaskDim || h > maxGlyphMaskDim {
		return &glyphMask{}, 96
	}
	surf := d.maskScratchSurface(w, h)
	if surf == nil {
		return &glyphMask{}, 96
	}
	pm := surf.Pixmap()
	if pm == nil || int(pm.Width) < w || int(pm.Height) < h {
		// No backing store, or one smaller than maskScratchSurface was asked for: degrade to the outline fill rather
		// than zeroing or reading back out of bounds.
		return &glyphMask{}, 96
	}
	// Zero just the w×h corner this mask uses (the scratch surface is sized for the largest glyph seen) straight in the
	// backing store. Clearing through the canvas — save, clip to the region, Clear, restore — allocated a paint and a
	// blend blitter per miss and ran the general draw pipeline to write constant zeroes; a direct pixmap write is the
	// same store compositeMask already performs, and nothing ever snapshots this surface, so there is no copy-on-write
	// state the canvas would need to see invalidated.
	for row := range h {
		clear(pm.Pix[row*int(pm.RowPixels):][:w])
	}
	local.E -= float32(mx0)
	local.F -= float32(my0)
	m := matrix(local)
	// One scratch path and one paint, reused by every miss: both were allocated fresh per glyph, and with the outline
	// storage retained across rewinds the transformed copy costs only the points it holds. The scratch cannot be shared
	// out from under itself — the fill below calls into canvas, never back into the device — and gp is always a cached
	// glyph-space outline, never this path.
	if d.maskPath == nil {
		d.maskPath = path.New()
		d.maskPath.SetVolatile(true) // Rebuilt for every glyph; nothing downstream should cache it.
		d.maskPaint = canvas.NewPaint()
		d.maskPaint.Color = colorcore.White
		d.maskPaint.AntiAlias = true
	}
	fill := d.maskPath
	fill.Rewind() // Restores the winding fill and empty storage a fresh path would have had.
	fill.AddPathMatrix(gp, &m, path.AddPathAppend)
	// The paint is shared across misses, so both branches assign the style fields every time. The dilated geometry
	// extends at most stemDarkenMaxPx/2 past the outline bounds (round joins spike no further), which the mask rect's
	// one-pixel padding above already absorbs.
	d.maskPaint.Style = canvas.StyleFill
	d.maskPaint.StrokeWidth = 0
	if w := d.stemDarkenWidth(g.Trm.A, g.Trm.B, g.Trm.C, g.Trm.D); w > 0 {
		applyStemDarkening(d.maskPaint, w)
	}
	surf.Canvas().DrawPath(fill, d.maskPaint)
	// White premultiplied by coverage stores the coverage in every channel; take the alpha byte (R|G<<8|B<<16|A<<24).
	plane := coveragePlane(pm, w, h)
	// The canvas image stays unbuilt: the direct pixmap composite reads plane straight and is the route nearly every
	// glyph takes, so wrapping the plane here would allocate a wrapper per miss that nothing reads (image() builds one
	// the first time a glyph actually goes through DrawImage, and the blit skips the glyph if that ever fails). The
	// size estimate still charges for it, since a mask that does reach DrawImage keeps one for the cache entry's life.
	return &glyphMask{plane: plane, w: int32(w), h: int32(h), ox: int32(mx0), oy: int32(my0)}, glyphMaskSize(w, h) * 2
}

// coveragePlane extracts the w×h alpha coverage from a white-on-transparent scratch pixmap into a tightly packed
// Alpha8 plane. It returns nil rather than panicking when pm is nil — the same nil guard compositeMask and Pixels
// apply before touching a surface's backing store, and the one renderGlyphMask makes before it zeroes the region it is
// about to read back.
func coveragePlane(pm *raster.Pixmap, w, h int) []byte {
	if pm == nil {
		return nil
	}
	plane := make([]byte, w*h)
	for row := range h {
		base := row * int(pm.RowPixels)
		for col := range w {
			plane[row*w+col] = uint8(pm.Pix[base+col] >> 24)
		}
	}
	return plane
}

// compositeMask source-over-composites a coverage plane, tinted by the opaque color r,g,b, straight into the surface
// pixmap at integer device position (x0, y0). Callers guarantee the canvas is at its base state (no clip, no layer,
// identity matrix) so this is exactly the draw canvas would perform, minus the general image pipeline's per-pixel float
// cost. out = src·c/255 + dst·(255−c)/255 per channel, single-rounded.
func (d *Device) compositeMask(mask *glyphMask, x0, y0 int, r, g, b uint8) {
	pm := d.surf.Pixmap()
	if pm == nil {
		return
	}
	w, h := int(mask.w), int(mask.h)
	cx0, cy0 := max(x0, 0), max(y0, 0)
	cx1, cy1 := min(x0+w, int(pm.Width)), min(y0+h, int(pm.Height))
	if cx0 >= cx1 || cy0 >= cy1 {
		return
	}
	srcR, srcG, srcB := uint32(r), uint32(g), uint32(b)
	srcWord := srcR | srcG<<8 | srcB<<16 | 0xff<<24
	for y := cy0; y < cy1; y++ {
		mrow := mask.plane[(y-y0)*w:]
		drow := pm.Pix[y*int(pm.RowPixels):]
		for x := cx0; x < cx1; x++ {
			c := uint32(mrow[x-x0])
			switch c {
			case 0:
			case 255:
				drow[x] = srcWord
			default:
				inv := 255 - c
				dst := drow[x]
				dr := (srcR*c + (dst&0xff)*inv + 127) / 255
				dg := (srcG*c + (dst>>8&0xff)*inv + 127) / 255
				db := (srcB*c + (dst>>16&0xff)*inv + 127) / 255
				da := (255*c + (dst>>24&0xff)*inv + 127) / 255
				drow[x] = dr | dg<<8 | db<<16 | da<<24
			}
		}
	}
}

// maskScratchSurface returns a scratch surface at least w×h, growing the cached one as needed (its contents are cleared
// by the caller). Reuse keeps mask misses from allocating a surface each.
func (d *Device) maskScratchSurface(w, h int) *surface.Surface {
	if d.maskScratch == nil || int(d.maskScratch.Width()) < w || int(d.maskScratch.Height()) < h {
		nw := max(w, 64)
		nh := max(h, 64)
		if d.maskScratch != nil {
			nw = max(nw, int(d.maskScratch.Width()))
			nh = max(nh, int(d.maskScratch.Height()))
		}
		d.maskScratch = surface.NewRasterN32Premul(int32(nw), int32(nh), nil)
	}
	return d.maskScratch
}
