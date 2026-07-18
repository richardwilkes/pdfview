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
	"github.com/richardwilkes/canvas/geom"
	"github.com/richardwilkes/canvas/imagecore"
	"github.com/richardwilkes/canvas/path"
	"github.com/richardwilkes/canvas/raster"
	"github.com/richardwilkes/canvas/shaders"
	"github.com/richardwilkes/canvas/surface"

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// The glyph coverage cache: filling every glyph outline through the analytic-AA rasterizer on every render
// dominated the profile, so ordinary fill-mode text instead rasterizes each distinct glyph appearance ONCE into
// an Alpha8 coverage plane and blits it at integer device positions — the same idea as MuPDF's glyph bitmap
// cache. A cache entry is keyed by the glyph identity plus the FULL float32 Trm linear part and the exact
// subpixel phase of the glyph origin, so a cached blit reproduces the coverage the direct fill would have
// produced bit-for-bit (the mask is rendered by the same analytic-AA fill at the same subpixel position; only
// the final color application can differ by ±1 rounding — see TestGlyphBlitMatchesDirectFill). No quantization
// means the first render of a page mostly misses (each glyph instance has its own x phase) and re-renders hit
// 100%; that is exactly the warm protocol both the recorded perf numbers and real consumers (re-render on
// scroll/zoom) care about, and it keeps the pixel gates honest. Entries live in the document's budgeted store
// when one is wired (kind-separated by the dedicated key type), else in a per-render map.

// maxGlyphMaskDim caps a cached coverage plane's extent; glyphs rendering larger than this (display-size
// text) fall back to the merged-outline fill, whose cost is amortized over the few such glyphs a page has.
const maxGlyphMaskDim = 256

// glyphMaskKey identifies one cached glyph coverage plane: glyph identity, the Trm linear part, and the
// subpixel phase of the glyph origin. Distinct store key type per the store's kind-separation rule.
type glyphMaskKey struct {
	font   *font.Font
	gid    uint32
	a, b   float32
	c, d   float32
	fx, fy float32
}

// glyphMask is one cached coverage plane. plane is nil for glyphs the blit path cannot handle (too large or
// unrasterizable) — a cached "use the outline fill" verdict. img wraps the same coverage as a canvas image
// for the DrawImage route, created lazily since the direct composite (the overwhelmingly common route) reads
// plane straight.
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

// blitTextRun is FillText's fast path: draw the run's glyphs from cached coverage planes. It handles only
// plain solid fills — an opaque folded color, Normal blend, no pattern/shading paint, not inside a knockout
// group (whose BlendSrc rewrite must not clear pixels around the glyph) — and reports false otherwise so the
// caller runs the merged-outline path. Glyphs the mask path cannot handle (oversized, degenerate) accumulate
// into a leftover outline filled exactly like the slow path.
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
		// Translucent text composites per glyph differently from the merged outline where fringes overlap;
		// keep the pinned merged behavior for it.
		return false
	}
	// With no group or soft-mask layer open, no untracked canvas state, and every open clip level an
	// axis-aligned rectangle, the canvas draw target is the base surface at identity, and a glyph whose
	// mask rect stays inside the tracked clip interior composites identically whether canvas draws it or
	// we do — so composite straight into the surface pixmap. canvas's Alpha8 image draws always run the
	// general float shader pipeline (no sprite fast path exists for Alpha8), which the profile showed
	// dominating warm renders. Everything else goes through DrawImage so canvas applies clip and layer.
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
		if mask == nil || mask.plane == nil {
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
			d.c.DrawPath(leftover, cpaint)
		}
	}
	return true
}

// glyphMask returns the cached coverage plane for one glyph appearance, rendering it on first use. fx, fy
// are the subpixel phase of the glyph origin in [0, 1).
func (d *Device) glyphMask(f *font.Font, g *device.Glyph, gp *path.Path, fx, fy float32) *glyphMask {
	key := glyphMaskKey{font: f, gid: g.GID, a: g.Trm.A, b: g.Trm.B, c: g.Trm.C, d: g.Trm.D, fx: fx, fy: fy}
	if d.store != nil {
		if v, ok := d.store.Get(key); ok {
			if m, isMask := v.(*glyphMask); isMask {
				return m
			}
			return nil
		}
	} else if m, ok := d.glyphMasks[key]; ok {
		return m
	}
	mask, size := d.renderGlyphMask(g, gp, fx, fy)
	if d.store != nil {
		d.store.Put(key, mask, size)
		return mask
	}
	if d.glyphMasks == nil {
		d.glyphMasks = make(map[glyphMaskKey]*glyphMask)
	}
	if len(d.glyphMasks) < maxCachedGlyphPaths {
		d.glyphMasks[key] = mask
	}
	return mask
}

// renderGlyphMask fills the glyph outline at its exact subpixel position into a scratch surface and captures
// the coverage as an Alpha8 image. The mask carries the glyph's device bounding box relative to its floored
// origin, padded a pixel so analytic-AA bleed is never clipped. Returns the mask (img nil when the glyph must
// use the outline fill) and its store size estimate.
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
	c := surf.Canvas()
	// Clear only the region this mask uses (the scratch surface is sized for the largest glyph seen).
	count := c.Save()
	c.ClipRect(geom.RectWH(float32(w), float32(h)), raster.ClipIntersect, false)
	c.Clear(colorcore.Transparent)
	c.RestoreToCount(count)
	local.E -= float32(mx0)
	local.F -= float32(my0)
	m := matrix(local)
	fill := path.New()
	fill.AddPathMatrix(gp, &m, path.AddPathAppend)
	paint := canvas.NewPaint()
	paint.Color = colorcore.White
	paint.AntiAlias = true
	c.DrawPath(fill, paint)
	// White premultiplied by coverage stores the coverage in every channel; take the alpha byte (R|G<<8|B<<16|A<<24).
	pm := surf.Pixmap()
	plane := make([]byte, w*h)
	for row := range h {
		base := row * int(pm.RowPixels)
		for col := range w {
			plane[row*w+col] = uint8(pm.Pix[base+col] >> 24)
		}
	}
	info := imagecore.ImageInfo{
		Width:     int32(w),
		Height:    int32(h),
		ColorType: imagecore.ColorTypeAlpha8,
		AlphaType: imagecore.AlphaTypePremul,
	}
	img := imagecore.NewRasterData(info, plane, w)
	if img == nil {
		return &glyphMask{}, 96
	}
	return &glyphMask{img: img, plane: plane, w: int32(w), h: int32(h), ox: int32(mx0), oy: int32(my0)},
		glyphMaskSize(w, h) * 2
}

// compositeMask source-over-composites a coverage plane, tinted by the opaque color r,g,b, straight into the
// surface pixmap at integer device position (x0, y0). Callers guarantee the canvas is at its base state
// (no clip, no layer, identity matrix) so this is exactly the draw canvas would perform, minus the general
// image pipeline's per-pixel float cost. out = src·c/255 + dst·(255−c)/255 per channel, single-rounded.
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

// maskScratchSurface returns a scratch surface at least w×h, growing the cached one as needed (its contents
// are cleared by the caller). Reuse keeps mask misses from allocating a surface each.
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
