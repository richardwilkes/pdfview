// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package render is the raster device: the only package that imports github.com/richardwilkes/canvas (plan.md
// invariant 3; canvas/gpu is never imported, keeping the build cgo- and purego-free). It draws the interpreter's
// device calls onto a premultiplied N32 raster surface and hands the premultiplied pixels back through Pixels;
// the root package's unpremultiply loop converts them to the straight alpha image.NRGBA the public API promises
// (reading back premultiplied is deliberate — see the 2026-07-11 decision-log entry on rounding parity).
//
// Milestone M4 implemented path fills, strokes (with dashing), and clips; M5 added images (RGBA draws and
// Alpha8 stencil tinting under the image CTM); M6 added text: glyph outlines cached in glyph space and
// filled/stroked/clipped under each glyph's Trm; M8 added shadings/patterns (shading.go) and transparency —
// groups, soft masks, blend modes, knockout (mask.go).
package render

import (
	"errors"
	"math"

	"github.com/richardwilkes/canvas/canvas"
	"github.com/richardwilkes/canvas/geom"
	"github.com/richardwilkes/canvas/imagecore"
	"github.com/richardwilkes/canvas/path"
	"github.com/richardwilkes/canvas/patheffect"
	"github.com/richardwilkes/canvas/raster"
	"github.com/richardwilkes/canvas/shaders"
	"github.com/richardwilkes/canvas/skcolor"
	"github.com/richardwilkes/canvas/surface"

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/font"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
	"github.com/richardwilkes/pdfview/internal/store"
)

// ErrSurface is reported when the raster surface cannot be created (non-positive or absurd dimensions).
var ErrSurface = errors.New("unable to create raster surface")

// Device rasterizes device calls onto a canvas surface. Create one per render with New; it is not safe for
// concurrent use (the document mutex in the public API serializes renders anyway).
type Device struct {
	surf *surface.Surface
	c    *canvas.Canvas
	// store, when set, caches converted glyph outlines across renders under the document's byte budget; the
	// per-render glyphPaths map is the fallback without one (see glyphPath).
	store *store.Store
	// glyphPaths caches converted glyph-space outlines for this render (see glyphPath).
	glyphPaths map[glyphKey]*path.Path
	// textClip accumulates ClipText outlines (device space) until EndTextClip pushes them as one clip.
	textClip *path.Path
	// clipStack records the canvas save count at each clip push so PopClip can restore precisely.
	clipStack []int
	// groupStack and maskStack track open transparency groups and soft-mask spans (see mask.go).
	groupStack []groupState
	maskStack  []*maskState
	width      int
	height     int
}

// SetStore wires the document's budgeted resource store into the device, migrating the glyph-path cache from
// per-render to document scope (plan.md internal/store).
func (d *Device) SetStore(st *store.Store) { d.store = st }

// New returns a device rendering to a zeroed (fully transparent) width×height premultiplied RGBA surface.
func New(width, height int) (*Device, error) {
	if width <= 0 || height <= 0 || width > 1<<30/4/max(height, 1) {
		return nil, ErrSurface
	}
	surf := surface.NewRasterN32Premul(int32(width), int32(height), nil)
	if surf == nil {
		return nil, ErrSurface
	}
	return &Device{surf: surf, c: surf.Canvas(), width: width, height: height}, nil
}

// Pixels reads back the rendered image as premultiplied RGBA bytes (4 per pixel), row-major with the returned
// stride. The alpha stays premultiplied by design; the caller unpremultiplies (see the package comment).
func (d *Device) Pixels() (pix []byte, stride int, err error) {
	stride = d.width * 4
	pix = make([]byte, stride*d.height)
	info := imagecore.ImageInfo{
		Width:     int32(d.width),
		Height:    int32(d.height),
		ColorType: imagecore.ColorTypeRGBA8888,
		AlphaType: imagecore.AlphaTypePremul,
	}
	img := d.surf.MakeImageSnapshot()
	if img == nil || !img.ReadPixels(info, pix, stride, 0, 0, imagecore.CachingDisallow) {
		return nil, 0, ErrSurface
	}
	return pix, stride, nil
}

// matrix converts a gfx.Matrix (PDF row-vector [a b c d e f]) to canvas's geom.Matrix.
func matrix(m gfx.Matrix) geom.Matrix {
	var out geom.Matrix
	out.SetAll(m.A, m.C, m.E, m.B, m.D, m.F, 0, 0, 1)
	return out
}

// buildPath converts a gfx.Path to a canvas path with the given fill rule.
func buildPath(p *gfx.Path, evenOdd bool) *path.Path {
	out := path.New()
	if evenOdd {
		out.SetFillType(path.FillEvenOdd)
	}
	pi := 0
	pts := p.Points
	for _, verb := range p.Verbs {
		switch verb {
		case gfx.MoveTo:
			if pi+1 > len(pts) {
				return out
			}
			out.MoveTo(pts[pi].X, pts[pi].Y)
			pi++
		case gfx.LineTo:
			if pi+1 > len(pts) {
				return out
			}
			out.LineTo(pts[pi].X, pts[pi].Y)
			pi++
		case gfx.QuadTo:
			if pi+2 > len(pts) {
				return out
			}
			out.QuadTo(pts[pi].X, pts[pi].Y, pts[pi+1].X, pts[pi+1].Y)
			pi += 2
		case gfx.CubicTo:
			if pi+3 > len(pts) {
				return out
			}
			out.CubicTo(pts[pi].X, pts[pi].Y, pts[pi+1].X, pts[pi+1].Y, pts[pi+2].X, pts[pi+2].Y)
			pi += 3
		case gfx.ClosePath:
			out.Close()
		}
	}
	return out
}

// paintFor builds the canvas paint for a fill or stroke. The folded paint alpha multiplies the (normally
// opaque) resolved color's own alpha. Antialiasing is always on, matching the oracle's rendering.
func paintFor(p device.Paint) *canvas.Paint {
	paint := canvas.NewPaint()
	alpha := p.Alpha
	if alpha < 0 {
		alpha = 0
	} else if alpha > 1 {
		alpha = 1
	}
	a := uint8(alpha*float64(p.Color.A) + 0.5)
	paint.Color = skcolor.ARGB(a, p.Color.R, p.Color.G, p.Color.B)
	paint.BlendMode = blendModes[p.Blend]
	paint.AntiAlias = true
	return paint
}

// blendModes maps the PDF blend enum to canvas blend modes, index-aligned with device.Blend's declaration
// order.
var blendModes = [...]raster.BlendMode{
	device.BlendNormal:     raster.BlendSrcOver,
	device.BlendMultiply:   raster.BlendMultiply,
	device.BlendScreen:     raster.BlendScreen,
	device.BlendOverlay:    raster.BlendOverlay,
	device.BlendDarken:     raster.BlendDarken,
	device.BlendLighten:    raster.BlendLighten,
	device.BlendColorDodge: raster.BlendColorDodge,
	device.BlendColorBurn:  raster.BlendColorBurn,
	device.BlendHardLight:  raster.BlendHardLight,
	device.BlendSoftLight:  raster.BlendSoftLight,
	device.BlendDifference: raster.BlendDifference,
	device.BlendExclusion:  raster.BlendExclusion,
	device.BlendHue:        raster.BlendHue,
	device.BlendSaturation: raster.BlendSaturation,
	device.BlendColor:      raster.BlendColor,
	device.BlendLuminosity: raster.BlendLuminosity,
}

// strokeInto applies the stroke parameters to a canvas paint. PDF dash semantics are adapted to the stroker's
// requirements here: an empty or all-zero array means solid; an odd-length array repeats with on/off roles
// alternating, which equals the doubled array; invalid values (negative handled upstream, non-finite phase)
// fall back to solid. A zero width requests a hairline, which the stroker draws one device pixel wide.
func strokeInto(paint *canvas.Paint, sp *gfx.StrokeParams) {
	paint.Style = canvas.StyleStroke
	paint.StrokeWidth = sp.Width
	paint.MiterLimit = sp.MiterLimit
	switch sp.Cap {
	case gfx.RoundCap:
		paint.Cap = canvas.CapRound
	case gfx.SquareCap:
		paint.Cap = canvas.CapSquare
	default:
		paint.Cap = canvas.CapButt
	}
	switch sp.Join {
	case gfx.RoundJoin:
		paint.Join = canvas.JoinRound
	case gfx.BevelJoin:
		paint.Join = canvas.JoinBevel
	default:
		paint.Join = canvas.JoinMiter
	}
	if intervals := dashIntervals(sp.Dash); intervals != nil {
		// MakeDash validates (even count, non-negative, positive sum, finite phase) and yields nil for
		// anything unusable, which leaves the stroke solid.
		paint.PathEffect = patheffect.MakeDash(intervals, sp.DashPhase)
	}
}

// dashIntervals normalizes a PDF dash array for the stroker: nil for solid (empty or all-zero), and doubled
// when the entry count is odd (PDF repeats the array with on/off roles swapped each cycle, which is what the
// doubled array encodes).
func dashIntervals(dash []float32) []float32 {
	if len(dash) == 0 {
		return nil
	}
	sum := float32(0)
	for _, v := range dash {
		sum += v
	}
	if !(sum > 0) { // Catches all-zero and NaN sums.
		return nil
	}
	if len(dash)%2 == 1 {
		doubled := make([]float32, 0, 2*len(dash))
		doubled = append(doubled, dash...)
		doubled = append(doubled, dash...)
		return doubled
	}
	return append([]float32(nil), dash...)
}

// FillPath implements device.Device. Paints carrying a gradient/tiling pattern draw with the corresponding
// shader; mesh-shading patterns clip to the path and draw their triangles.
func (d *Device) FillPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix, paint device.Paint) {
	cp := buildPath(p, evenOdd)
	m := matrix(ctm)
	if isMesh(paint) {
		cp.Transform(&m)
		d.fillMeshInto(cp, paint)
		return
	}
	if paint.Tiling != nil {
		cp.Transform(&m)
		d.fillTilingInto(cp, paint)
		return
	}
	cpaint, ok := d.preparePaint(paint, &ctm)
	if !ok {
		return
	}
	d.withShadingBBox(paint, func() {
		count := d.c.Save()
		d.c.Concat(&m)
		d.c.DrawPath(cp, cpaint)
		d.c.RestoreToCount(count)
	})
}

// StrokePath implements device.Device.
func (d *Device) StrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix, paint device.Paint) {
	cp := buildPath(p, false)
	m := matrix(ctm)
	if isMesh(paint) {
		// The stroked region cannot become a clip path directly; composite the mesh through the stroke's
		// coverage in a layer instead.
		d.maskedMesh(paint, func(mask *canvas.Paint) {
			strokeInto(mask, sp)
			count := d.c.Save()
			d.c.Concat(&m)
			d.c.DrawPath(cp, mask)
			d.c.RestoreToCount(count)
		})
		return
	}
	cpaint, ok := d.preparePaint(paint, &ctm)
	if !ok {
		return
	}
	strokeInto(cpaint, sp)
	d.withShadingBBox(paint, func() {
		count := d.c.Save()
		d.c.Concat(&m)
		d.c.DrawPath(cp, cpaint)
		d.c.RestoreToCount(count)
	})
}

// ClipPath implements device.Device. The path is transformed to device space here (rather than concatenating
// the matrix) so the clip can be pushed without disturbing the canvas matrix for later draws.
func (d *Device) ClipPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix) {
	d.clipStack = append(d.clipStack, d.c.Save())
	cp := buildPath(p, evenOdd)
	m := matrix(ctm)
	cp.Transform(&m)
	d.c.ClipPath(cp, raster.ClipIntersect, true)
}

// ClipStrokePath implements device.Device. No M4 content produces it (W clips use the fill region); it clips
// to the stroke's bounding region conservatively until the text-clip work (M6) needs better.
func (d *Device) ClipStrokePath(p *gfx.Path, _ *gfx.StrokeParams, ctm gfx.Matrix) {
	d.ClipPath(p, false, ctm)
}

// PopClip implements device.Device.
func (d *Device) PopClip() {
	if n := len(d.clipStack); n > 0 {
		d.c.RestoreToCount(d.clipStack[n-1])
		d.clipStack = d.clipStack[:n-1]
	}
}

// glyphKey identifies one glyph outline in the per-render path cache. Fonts are cached per content Run
// keyed by resource reference, so the pointer is stable for all of one page's runs.
type glyphKey struct {
	font *font.Font
	gid  uint32
}

// glyphPath returns the cached canvas path for one glyph in em-normalized glyph space, converting (and
// caching, including failures as nil) on first use. With a store wired, converted paths live there — shared
// across renders and bounded by the document's byte budget; otherwise the per-render map caches them.
func (d *Device) glyphPath(f *font.Font, gid uint32) *path.Path {
	key := glyphKey{font: f, gid: gid}
	if d.store != nil {
		if v, ok := d.store.Get(key); ok {
			if p, isPath := v.(*path.Path); isPath {
				return p
			}
			return nil // Cached failure (negative entry).
		}
	} else if p, ok := d.glyphPaths[key]; ok {
		return p
	}
	var p *path.Path
	var size uint64 = 64
	if g := f.GlyphPath(gid); g != nil && !g.IsEmpty() {
		p = buildPath(g, false)
		size += uint64(len(g.Verbs)) + uint64(len(g.Points))*8
	}
	if d.store != nil {
		d.store.Put(key, p, size)
		return p
	}
	if d.glyphPaths == nil {
		d.glyphPaths = make(map[glyphKey]*path.Path)
	}
	if len(d.glyphPaths) < maxCachedGlyphPaths {
		d.glyphPaths[key] = p
	}
	return p
}

// maxCachedGlyphPaths caps the per-render glyph path cache; a page rarely uses more than a few hundred
// distinct glyphs, so the cap only bites on hostile content (which then just re-converts).
const maxCachedGlyphPaths = 4096

// textOutline merges a run's glyph outlines into one device-space path: each glyph's cached glyph-space
// outline is appended under its Trm. Glyph fills use the non-zero winding rule (glyph contours are wound for
// it; PDF's even-odd text mode does not exist). under, when non-nil, maps the accumulated result to another
// space (used by StrokeText to build the path in user space instead).
func (d *Device) textOutline(run *device.TextRun, under *gfx.Matrix) *path.Path {
	out := path.New()
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		gp := d.glyphPath(run.Font, g.GID)
		if gp == nil {
			continue
		}
		trm := g.Trm
		if under != nil {
			trm = trm.Mul(*under)
		}
		if !trm.IsFinite() {
			continue
		}
		m := matrix(trm)
		out.AddPathMatrix(gp, &m, path.AddPathAppend)
	}
	return out
}

// FillText implements device.Device: fill the run's merged outline (already in device space via the Trms)
// with the non-zero winding rule, antialiased, matching the oracle's glyph rasterization.
func (d *Device) FillText(run *device.TextRun, paint device.Paint) {
	p := d.textOutline(run, nil)
	if p.IsEmpty() {
		return
	}
	if isMesh(paint) {
		d.fillMeshInto(p, paint)
		return
	}
	if paint.Tiling != nil {
		d.fillTilingInto(p, paint)
		return
	}
	cpaint, ok := d.preparePaint(paint, nil) // The outline is device space; the shader anchors directly.
	if !ok {
		return
	}
	d.withShadingBBox(paint, func() {
		d.c.DrawPath(p, cpaint)
	})
}

// StrokeText implements device.Device. Stroke parameters are user-space quantities: the pen applies under
// the run's CTM alone, not under the text matrix or font size (ISO 32000-2 9.3.6), so the merged outline is
// built in user space (Trm·CTM⁻¹) and stroked exactly like a user-space path. A degenerate CTM draws
// nothing (there is no meaningful pen).
func (d *Device) StrokeText(run *device.TextRun, sp *gfx.StrokeParams, paint device.Paint) {
	inv, ok := run.CTM.Invert()
	if !ok {
		return
	}
	p := d.textOutline(run, &inv)
	if p.IsEmpty() {
		return
	}
	m := matrix(run.CTM)
	if isMesh(paint) {
		d.maskedMesh(paint, func(mask *canvas.Paint) {
			strokeInto(mask, sp)
			count := d.c.Save()
			d.c.Concat(&m)
			d.c.DrawPath(p, mask)
			d.c.RestoreToCount(count)
		})
		return
	}
	ctm := run.CTM
	cpaint, okPaint := d.preparePaint(paint, &ctm)
	if !okPaint {
		return
	}
	strokeInto(cpaint, sp)
	d.withShadingBBox(paint, func() {
		count := d.c.Save()
		d.c.Concat(&m)
		d.c.DrawPath(p, cpaint)
		d.c.RestoreToCount(count)
	})
}

// ClipText implements device.Device: accumulate the run's device-space outline into the pending text clip,
// finalized by EndTextClip.
func (d *Device) ClipText(run *device.TextRun) {
	if d.textClip == nil {
		d.textClip = path.New()
	}
	d.textClip.AddPath(d.textOutline(run, nil), path.AddPathAppend)
}

// EndTextClip implements device.Device: push the text clip accumulated by ClipText since the last
// EndTextClip as one clip level. A text object whose clip accumulation produced no outlines clips
// everything away, per the text-clip semantics (the region is the union of the shown glyphs).
func (d *Device) EndTextClip() {
	d.clipStack = append(d.clipStack, d.c.Save())
	clip := d.textClip
	if clip == nil {
		clip = path.New()
	}
	d.textClip = nil
	d.c.ClipPath(clip, raster.ClipIntersect, true)
}

// IgnoreText implements device.Device.
func (d *Device) IgnoreText(*device.TextRun) {}

// rasterImage wraps a decoded image's pixels as a canvas image: straight-alpha RGBA for ordinary images
// (the sampling pipeline premultiplies), Alpha8 for stencils (which the pipeline tints with the paint color —
// exactly PDF's image-mask semantics). Returns nil for empty or inconsistent pixel data.
func rasterImage(img *imaging.Image) *imagecore.Image {
	if img == nil || img.Width <= 0 || img.Height <= 0 {
		return nil
	}
	info := imagecore.ImageInfo{Width: int32(img.Width), Height: int32(img.Height)}
	rowBytes := img.Width
	switch {
	case img.Stencil:
		info.ColorType = imagecore.ColorTypeAlpha8
		info.AlphaType = imagecore.AlphaTypePremul
	case img.HasAlpha:
		info.ColorType = imagecore.ColorTypeRGBA8888
		info.AlphaType = imagecore.AlphaTypeUnpremul
		rowBytes *= 4
	default:
		info.ColorType = imagecore.ColorTypeRGBA8888
		info.AlphaType = imagecore.AlphaTypeOpaque
		rowBytes *= 4
	}
	return imagecore.NewRasterData(info, img.Pix, rowBytes)
}

// drawImage draws ci across the unit square of the ctm's source space: PDF image space puts the first sample
// row at the top of that square (ISO 32000-2 8.9.5.2), so the image's pixel grid is flipped into the square
// before the ctm applies. /Interpolate selects linear sampling; without it samples stay unfiltered (nearest),
// the mapping calibrated against the oracle's renders.
func (d *Device) drawImage(ci *imagecore.Image, img *imaging.Image, ctm gfx.Matrix, paint *canvas.Paint) {
	w, h := float32(img.Width), float32(img.Height)
	ctm = gridfit(ctm)
	m := matrix(ctm)
	var flip geom.Matrix
	flip.SetAll(1/w, 0, 0, 0, -1/h, 1, 0, 0, 1)
	sampling := shaders.SamplingOptions{Filter: shaders.FilterNearest}
	if img.Interpolate {
		sampling.Filter = shaders.FilterLinear
	}
	count := d.c.Save()
	d.c.Concat(&m)
	d.c.Concat(&flip)
	d.c.DrawImageRect(ci, geom.RectWH(w, h), geom.RectWH(w, h), sampling, paint, canvas.ConstraintFast)
	d.c.RestoreToCount(count)
}

// gridfit snaps a rectilinear image transform outward to whole device pixels — the unit square's device
// extent becomes floor(min)…ceil(max) per axis — reproducing the oracle's hard, pixel-aligned image edges
// (pinned against the image goldens at fractional scales: a 425.0 edge computed as 424.99997 in float32 snaps
// to 424, a 154.166 max edge to 155). Skew and rotation pass through untouched: only axis-aligned transforms
// (in either axis order) can snap without shearing, matching the oracle's behavior of antialiasing rotated
// images' edges.
func gridfit(m gfx.Matrix) gfx.Matrix {
	switch {
	case m.B == 0 && m.C == 0 && m.A != 0 && m.D != 0:
		m.A, m.E = snapSpan(m.A, m.E)
		m.D, m.F = snapSpan(m.D, m.F)
	case m.A == 0 && m.D == 0 && m.B != 0 && m.C != 0:
		// A 90/270-degree transform: x comes from the C/F pair, y from the B/E pair.
		m.C, m.E = snapSpan(m.C, m.E)
		m.B, m.F = snapSpan(m.B, m.F)
	}
	return m
}

// snapSpan snaps one axis of a rectilinear transform: the interval [off, off+extent] (in either direction)
// expands to floor(min)…ceil(max), keeping the extent's sign.
func snapSpan(extent, off float32) (newExtent, newOff float32) {
	lo, hi := float64(off), float64(off+extent)
	if lo > hi {
		lo, hi = hi, lo
	}
	lo, hi = math.Floor(lo), math.Ceil(hi)
	if extent < 0 {
		return float32(lo - hi), float32(hi)
	}
	return float32(hi - lo), float32(lo)
}

// FillImage implements device.Device. alpha is the folded constant fill alpha; it modulates the image through
// the paint's alpha channel.
func (d *Device) FillImage(img *imaging.Image, ctm gfx.Matrix, alpha float64) {
	ci := rasterImage(img)
	if ci == nil {
		return
	}
	if alpha < 0 {
		alpha = 0
	} else if alpha > 1 {
		alpha = 1
	}
	paint := canvas.NewPaint()
	paint.AntiAlias = true
	paint.Color = skcolor.ARGB(uint8(alpha*255+0.5), 255, 255, 255)
	d.applyKnockout(paint)
	d.drawImage(ci, img, ctm, paint)
}

// FillImageMask implements device.Device: the stencil's Alpha8 pixels are tinted by the fill paint's color
// (with its folded alpha), PDF's image-mask stencil semantics. A pattern paint tints through its shader
// instead (an alpha-only image samples the paint's shader, exactly PDF's pattern-stenciling semantics);
// mesh-shading patterns composite through the stencil's coverage in a layer.
func (d *Device) FillImageMask(img *imaging.Image, ctm gfx.Matrix, paint device.Paint) {
	ci := rasterImage(img)
	if ci == nil {
		return
	}
	if isMesh(paint) {
		d.maskedMesh(paint, func(mask *canvas.Paint) {
			d.drawImage(ci, img, ctm, mask)
		})
		return
	}
	// The image draw runs under gridfit(ctm) composed with the image-space flip; the shader local matrix
	// must unwind that full transform, so compute it here exactly as drawImage will.
	fit := gridfit(ctm)
	flip := gfx.Matrix{A: 1 / float32(img.Width), D: -1 / float32(img.Height), F: 1}
	total := flip.Mul(fit)
	cpaint, ok := d.preparePaint(paint, &total)
	if !ok {
		return
	}
	d.withShadingBBox(paint, func() {
		d.drawImage(ci, img, ctm, cpaint)
	})
}

// ClipImageMask implements device.Device. No producer exists until tiling patterns and Type3 glyphs recurse
// (M6/M8), so the mask bits are not yet consulted: the clip is the mask's unit square under the ctm, a correct
// outer bound (a mask can only mark inside its square).
func (d *Device) ClipImageMask(_ *imaging.Image, ctm gfx.Matrix) {
	square := &gfx.Path{}
	square.Rect(0, 0, 1, 1)
	d.ClipPath(square, false, ctm)
}

// FillShading implements device.Device: paint the shading across the current clip (the sh operator). The
// shading's own geometry — gradient extent under decal/clamp tiling, the function domain, mesh triangles —
// bounds the painted area; the /BBox clip applies when present.
func (d *Device) FillShading(sh *shading.Shading, ctm gfx.Matrix, paint device.Paint) {
	p := device.Paint{Shading: sh, PatternCTM: ctm, Alpha: paint.Alpha, Blend: paint.Blend}
	if isMesh(p) {
		d.withShadingBBox(p, func() {
			d.drawMesh(sh, ctm, p.Alpha, p.Blend)
		})
		return
	}
	cpaint, ok := d.preparePaint(p, nil)
	if !ok {
		return
	}
	full := path.New()
	full.MoveTo(0, 0)
	full.LineTo(float32(d.width), 0)
	full.LineTo(float32(d.width), float32(d.height))
	full.LineTo(0, float32(d.height))
	full.Close()
	d.withShadingBBox(p, func() {
		d.c.DrawPath(full, cpaint)
	})
}
