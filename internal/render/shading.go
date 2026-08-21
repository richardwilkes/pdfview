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

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// Shadings map to canvas shaders: axial → linear gradient, radial → two-point conical gradient (Skia's conical
// implements the PDF/PostScript circle-interpolation semantics), function-based → a domain-grid image shader, and the
// mesh kinds → flat tessellated triangles drawn directly. /Extend uses TileClamp when both ends extend and TileDecal
// when neither does; MIXED extend is realized as a single decal draw over parametrically extended geometry (the
// boundary color duplicated over the extension). Tiling patterns render one cell into an offscreen surface wrapped in a
// repeating image shader, the rasterized cell cached in the document store (see tileImage).

// Limits: caps on the offscreen resolutions hostile content can request. A function-based shading evaluates its
// function once per grid cell (shading.MaxGridDim/MaxGridArea bound that work — they live beside the shading because
// the interpreter charges its work budget against the same grid); a tiling cell rasterizes at the pattern's device
// scale (maxTileDim/maxTileArea bound the surface), degrading to a coarser tile beyond them. maxExtendFactor bounds the
// parametric gradient extension search. maxTileCopies bounds how many neighbor-cell copies replay into one tile when
// the cell box overlaps its steps.
const (
	maxTileDim      = 2048
	maxTileArea     = 1 << 22
	maxExtendFactor = 1 << 20
	maxTileCopies   = 4
)

// preparePaint builds the canvas paint for a device paint that may carry a gradient/function shading or a tiling
// pattern. ctm, when non-nil, is the matrix the draw will run under (Concat), so the shader's local matrix maps pattern
// space back through its inverse; nil means the draw happens in device space. The bool result is false when the paint
// cannot be realized (degenerate matrices, unusable pattern) — the draw is skipped, matching viewer degradation. Mesh
// shadings never come through here (their draw paths clip and paint triangles instead).
func (d *Device) preparePaint(p device.Paint, ctm *gfx.Matrix) (*canvas.Paint, bool) {
	if p.Shading == nil && p.Tiling == nil {
		paint := paintFor(p)
		d.applyKnockout(paint) // Solid draws replace inside knockout groups (mask.go); patterns keep SrcOver.
		return paint, true
	}
	local := p.PatternCTM
	if p.Shading != nil {
		// MuPDF paints every shading kind through its shade painter, which samples at the pixel's top-right device
		// corner where our shaders sample the center; shift the shading geometry by (-0.5, +0.5) device pixels to line
		// the ramps and decal boundaries up (see drawMesh, pinned by the goldens).
		local = local.Mul(gfx.Translate(-0.5, 0.5))
	}
	// toDevice is the pattern/shading space → DEVICE map; local is that same map carried back into the space the draw
	// runs in, which is what canvas needs for the shader (it composes the local matrix with the draw's own CTM). Every
	// decision about how big something must be to cover the surface — a gradient's extension factor, a function
	// shading's evaluation grid — belongs to toDevice: the drawing CTM cancels out of the composition, so measuring
	// against local scales those decisions by its inverse (see coverageCorners and functionShader). tileShader already
	// works this way, taking its device scale from patCTM.
	toDevice := local
	if ctm != nil {
		inv, ok := ctm.Invert()
		if !ok {
			return nil, false
		}
		local = local.Mul(inv)
	}
	var shader shaders.Shader
	if p.Shading != nil {
		shader = d.shadingShader(p.Shading, local, toDevice)
	} else {
		shader = d.tileShader(p.Tiling, local, p.PatternCTM)
	}
	if shader == nil {
		return nil, false
	}
	paint := canvas.NewPaint()
	paint.Color = colorcore.ARGB(alpha8(p.Alpha), 255, 255, 255)
	paint.BlendMode = blendModes[p.Blend]
	paint.AntiAlias = true
	paint.Shader = shader
	return paint, true
}

func alpha8(alpha float64) uint8 {
	if alpha < 0 {
		alpha = 0
	} else if alpha > 1 {
		alpha = 1
	}
	return uint8(alpha*255 + 0.5)
}

// isMesh reports whether the paint carries a mesh shading, which draws as triangles rather than a shader.
func isMesh(p device.Paint) bool {
	return p.Shading != nil && p.Shading.Kind >= shading.KindFreeTriangle
}

// shadingShader builds the shader for a non-mesh shading; local maps the shading's target space to the space the draw
// runs in, toDevice maps it to device pixels (see preparePaint).
func (d *Device) shadingShader(sh *shading.Shading, local, toDevice gfx.Matrix) shaders.Shader {
	switch sh.Kind {
	case shading.KindAxial:
		return d.axialShader(sh, local, toDevice)
	case shading.KindRadial:
		return d.radialShader(sh, local, toDevice)
	case shading.KindFunction:
		return d.functionShader(sh, local, toDevice)
	default:
		return nil
	}
}

// gradientRamp converts sampled stops to the canvas color/position arrays, extending the parametric span by e0 before
// offset 0 and e1 after offset 1 (in units of the original span) with duplicated boundary colors.
//
// The offsets map in float64 and come back through rampPos, which forces the array non-decreasing within [0, 1]. A large
// one-sided extension (the factors are clamped only at maxExtendFactor) compresses the whole original span into ~1e-6 of
// the ramp, below float32's resolution there, so the mapped offsets collapse onto each other and the float32 rounding of
// a monotonic sequence is no longer guaranteed to stay ordered; a non-finite stop offset would otherwise cross into
// canvas untouched as well. The visual result of such an extension is a hard boundary either way — this only keeps the
// arrays handed to shaders.NewLinearGradient/NewTwoPointConicalGradient inside the contract they document.
func gradientRamp(stops []shading.Stop, e0, e1 float32) (colors []colorcore.Color, pos []float32) {
	if len(stops) == 0 {
		return nil, nil
	}
	span := 1 + float64(e0) + float64(e1)
	n := len(stops)
	if e0 > 0 {
		n++
	}
	if e1 > 0 {
		n++
	}
	colors = make([]colorcore.Color, 0, n)
	pos = make([]float32, 0, n)
	prev := float32(0)
	if e0 > 0 {
		c := stops[0].Color
		colors = append(colors, colorcore.ARGB(c.A, c.R, c.G, c.B))
		pos = append(pos, prev)
	}
	for _, s := range stops {
		colors = append(colors, colorcore.ARGB(s.Color.A, s.Color.R, s.Color.G, s.Color.B))
		prev = rampPos((float64(s.Offset)+float64(e0))/span, prev)
		pos = append(pos, prev)
	}
	if e1 > 0 {
		c := stops[len(stops)-1].Color
		colors = append(colors, colorcore.ARGB(c.A, c.R, c.G, c.B))
		pos = append(pos, 1)
	}
	return colors, pos
}

// rampPos narrows one ramp position to float32, holding it at or above the previous position and no higher than 1. The
// comparison is written so a NaN (which no comparison satisfies) lands on prev rather than propagating.
func rampPos(p float64, prev float32) float32 {
	v := float32(p)
	if !(v > prev) {
		return prev
	}
	return min(v, 1)
}

// coverageCorners maps the device surface's corners into the shading's target space, for sizing gradient extensions.
// toDevice is the shading target space → device map, NOT the shader's local matrix: the extension has to be big enough
// to cover the surface's own pixels, so the corners must come back through the full map. Inverting local instead would
// carry the device corners through the drawing CTM first, sizing the extension by ctm(deviceCorners) — too small
// wherever the drawing CTM shrinks relative to the pattern CTM, which leaves part of the surface unpainted (it is exact
// only at scale 1, where the y-flip of the usual page CTM is an involution).
func (d *Device) coverageCorners(toDevice gfx.Matrix) ([4]gfx.Point, bool) {
	inv, ok := toDevice.Invert()
	if !ok {
		return [4]gfx.Point{}, false
	}
	w, h := float32(d.width), float32(d.height)
	var out [4]gfx.Point
	for i, c := range [4]gfx.Point{{X: 0, Y: 0}, {X: w, Y: 0}, {X: 0, Y: h}, {X: w, Y: h}} {
		x, y := inv.ApplyXY(c.X, c.Y)
		if !isFinite32(x) || !isFinite32(y) {
			return out, false
		}
		out[i] = gfx.Point{X: x, Y: y}
	}
	return out, true
}

func isFinite32(v float32) bool {
	f := float64(v)
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// axialShader builds the linear-gradient shader for a type 2 shading.
func (d *Device) axialShader(sh *shading.Shading, local, toDevice gfx.Matrix) shaders.Shader {
	if len(sh.Stops) == 0 {
		return nil
	}
	p0 := geom.Point{X: sh.Coords[0], Y: sh.Coords[1]}
	p1 := geom.Point{X: sh.Coords[2], Y: sh.Coords[3]}
	lm := matrix(local)
	tile := shaders.TileDecal
	e0, e1 := float32(0), float32(0)
	switch {
	case sh.Extend[0] && sh.Extend[1]:
		tile = shaders.TileClamp
	case sh.Extend[0] || sh.Extend[1]:
		// Mixed extend: extend the parametric span far enough to cover the surface on the extended side and keep decal
		// so the other side stays unpainted.
		dx, dy := p1.X-p0.X, p1.Y-p0.Y
		lenSq := dx*dx + dy*dy
		if lenSq <= 0 || !isFinite32(lenSq) {
			return nil
		}
		corners, ok := d.coverageCorners(toDevice)
		if !ok {
			return nil
		}
		sMin, sMax := axialSpan(p0, dx, dy, lenSq, corners)
		if sh.Extend[0] {
			e0 = float32(min(-sMin+1, maxExtendFactor))
		}
		if sh.Extend[1] {
			e1 = float32(min(sMax, maxExtendFactor))
		}
		p0 = geom.Point{X: p0.X - e0*dx, Y: p0.Y - e0*dy}
		p1 = geom.Point{X: p1.X + e1*dx, Y: p1.Y + e1*dy}
	}
	// Whatever the extension arithmetic produced, only finite geometry crosses into canvas (see axialSpan).
	if !isFinite32(p0.X) || !isFinite32(p0.Y) || !isFinite32(p1.X) || !isFinite32(p1.Y) {
		return nil
	}
	colors, pos := gradientRamp(sh.Stops, e0, e1)
	return shaders.NewLinearGradient(p0, p1, colors, pos, tile, &lm)
}

// axialSpan returns the parametric range the surface's corners occupy along the p0 + t*(dx, dy) axis, clamped to
// include the gradient's own [0, 1] span. The projections run in float64 on purpose: the inputs are finite float32s,
// but a float32 difference of two large opposite-sign coordinates overflows to ±Inf, and Inf*0 — the ordinary case for
// an axis-aligned gradient, where dx or dy is exactly 0 — is NaN, which Go's min/max propagate into the extension
// factors, from there into the extended endpoints and the ramp's stop offsets, and on into canvas. In float64 no
// difference or product of finite float32s can overflow, so with lenSq already known positive and finite every
// projection is finite, and the results stay in the range the caller's maxExtendFactor clamp bounds.
func axialSpan(p0 geom.Point, dx, dy, lenSq float32, corners [4]gfx.Point) (sMin, sMax float64) {
	sMin, sMax = 0, 1
	for _, c := range corners {
		s := ((float64(c.X)-float64(p0.X))*float64(dx) + (float64(c.Y)-float64(p0.Y))*float64(dy)) / float64(lenSq)
		sMin = min(sMin, s)
		sMax = max(sMax, s)
	}
	return sMin, sMax
}

// radialShader builds the two-point conical shader for a type 3 shading.
func (d *Device) radialShader(sh *shading.Shading, local, toDevice gfx.Matrix) shaders.Shader {
	if len(sh.Stops) == 0 {
		return nil
	}
	c0 := gfx.Point{X: sh.Coords[0], Y: sh.Coords[1]}
	r0 := sh.Coords[2]
	c1 := gfx.Point{X: sh.Coords[3], Y: sh.Coords[4]}
	r1 := sh.Coords[5]
	lm := matrix(local)
	tile := shaders.TileDecal
	e0, e1 := float32(0), float32(0)
	switch {
	case sh.Extend[0] && sh.Extend[1]:
		tile = shaders.TileClamp
	case sh.Extend[0] || sh.Extend[1]:
		corners, ok := d.coverageCorners(toDevice)
		if !ok {
			return nil
		}
		if sh.Extend[0] {
			e0 = radialExtension(c0, c1, r0, r1, corners, true)
		}
		if sh.Extend[1] {
			e1 = radialExtension(c0, c1, r0, r1, corners, false)
		}
		dx, dy, dr := c1.X-c0.X, c1.Y-c0.Y, r1-r0
		c0, c1 = gfx.Point{X: c0.X - e0*dx, Y: c0.Y - e0*dy}, gfx.Point{X: c1.X + e1*dx, Y: c1.Y + e1*dy}
		r0, r1 = max(r0-e0*dr, 0), max(r1+e1*dr, 0)
	}
	// A finite-but-enormous /Coords can make the extension above overflow (a radius delta of ±Inf drives the extended
	// radius to Inf, an extended center to ±Inf); only finite geometry crosses into canvas.
	if !isFinite32(c0.X) || !isFinite32(c0.Y) || !isFinite32(c1.X) || !isFinite32(c1.Y) ||
		!isFinite32(r0) || !isFinite32(r1) {
		return nil
	}
	colors, pos := gradientRamp(sh.Stops, e0, e1)
	return shaders.NewTwoPointConicalGradient(geom.Point{X: c0.X, Y: c0.Y}, r0,
		geom.Point{X: c1.X, Y: c1.Y}, r1, colors, pos, tile, &lm)
}

// radialExtension finds the parametric extension factor (in units of the t span) that either covers every corner with
// the extended circle or reaches the radius-zero cutoff PDF prescribes (ISO 32000-2 8.7.4.5.4: extension continues
// until the circles cover the area or the radius becomes 0).
//
// The search runs in float64 for the reason axialSpan does: every input is a finite float32, but their float32
// differences and products can overflow to ±Inf and produce NaN (Inf-Inf in a center delta, Inf*0 in a
// coincident-center gradient), which would flow out as a NaN factor and put NaN circles in front of canvas. In float64
// nothing formed from finite float32s overflows, and the result is bounded by maxExtendFactor, so it converts back
// exactly.
func radialExtension(c0, c1 gfx.Point, r0, r1 float32, corners [4]gfx.Point, atStart bool) float32 {
	dr := float64(r1) - float64(r0)
	// The factor at which the extended radius reaches zero, when it shrinks in this direction.
	limit := float64(maxExtendFactor)
	if atStart && dr > 0 {
		limit = float64(r0) / dr
	} else if !atStart && dr < 0 {
		limit = float64(r1) / -dr
	}
	covered := func(e float64) bool {
		t := 1 + e
		if atStart {
			t = -e
		}
		cx := float64(c0.X) + t*(float64(c1.X)-float64(c0.X))
		cy := float64(c0.Y) + t*(float64(c1.Y)-float64(c0.Y))
		r := float64(r0) + t*dr
		if r < 0 {
			return false
		}
		for _, q := range corners {
			dx, dy := float64(q.X)-cx, float64(q.Y)-cy
			if dx*dx+dy*dy > r*r {
				return false
			}
		}
		return true
	}
	for e := float64(1); e <= maxExtendFactor; e *= 2 {
		if e >= limit {
			return float32(limit)
		}
		if covered(e) {
			return float32(e)
		}
	}
	return float32(min(float64(maxExtendFactor), limit))
}

// functionShader realizes a type 1 shading as an image shader: the function is evaluated over a grid spanning the
// domain at roughly device resolution (shading.GridSize caps it), and the image is placed by the domain-to-device
// mapping with decal tiling so points outside the domain stay unpainted.
//
// The grid is sized from toDevice, not from the shader's local matrix: "device resolution" means device PIXELS, and
// local carries the drawing CTM's inverse, so sizing from it scales the grid by the inverse of the drawing CTM — a
// magnifying cm renders the shading blocky, and a shrinking one inflates the grid toward shading.MaxGridArea while
// internal/content charged the work budget from the pattern CTM (see budget.go's shadingPaintCost). Both packages have
// to size from the same numbers, which is why the caps live in internal/shading; content uses the pattern CTM, so this
// does too.
func (d *Device) functionShader(sh *shading.Shading, local, toDevice gfx.Matrix) shaders.Shader {
	w, h, ok := sh.GridSize(toDevice)
	if !ok {
		return nil
	}
	x0, x1, y0, y1 := sh.Domain[0], sh.Domain[1], sh.Domain[2], sh.Domain[3]
	// Image pixel -> domain -> /Matrix -> target space. The per-cell extents are formed in float64 for the reason
	// axialSpan runs there: each /Domain entry is individually finite, but the float32 difference of two far-apart
	// bounds (a /Domain of [-3e38 3e38 0 1]) overflows to +Inf, which would scale the whole placement by infinity.
	toDomain := gfx.Matrix{
		A: float32((float64(x1) - float64(x0)) / float64(w)),
		D: float32((float64(y1) - float64(y0)) / float64(h)),
		E: x0,
		F: y0,
	}
	// Only finite geometry crosses into canvas — the same gate axialShader applies to its own placement. Checked before
	// the grid is realized, since canvas drops a fill with a non-finite local matrix and the up-to-MaxGridArea function
	// evaluations behind it would be spent painting nothing.
	full := toDomain.Mul(sh.Matrix.Mul(local))
	if !full.IsFinite() {
		return nil
	}
	img := d.functionImage(sh, w, h)
	if img == nil {
		return nil
	}
	lm := matrix(full)
	sampling := shaders.SamplingOptions{Filter: shaders.FilterNearest}
	return shaders.NewImage(img, shaders.TileDecal, shaders.TileDecal, sampling, &lm)
}

// funcGridKey identifies one realized function-based shading grid: the parsed shading (immutable after shading.Parse,
// and the interpreter hands out one instance per shading reference, so the pointer IS the content identity — the same
// reasoning glyphMaskKey keys on a *font.Font) plus the grid size it was evaluated at. Distinct store key type per the
// store's kind-separation rule.
type funcGridKey struct {
	sh   *shading.Shading
	w, h int
}

// funcGridSize estimates a realized grid's cache footprint for the store budget.
func funcGridSize(w, h int) uint64 { return 4*uint64(w)*uint64(h) + 128 }

// maxCachedFuncGrids caps the per-render grid map used when no budgeted store is wired; at the grid-area cap each entry
// is 1 MB, so the map holds a few megabytes at most.
const maxCachedFuncGrids = 8

// functionImage returns sh's domain grid realized at w×h, evaluating the shading's function once per cell on first use.
// The realization depends only on the shading and that grid size — where it lands on the surface lives in the shader's
// matrix, not in the image — so it is cached and reused by every later draw of the same shading at the same size.
// Without the cache each painting operation re-evaluated the whole grid (up to shading.MaxGridArea function evaluations
// for a 1 MB image), which is why the interpreter charges its budget per painting operation and not per parse: a
// repeated sh costs one operator unit apiece, and a type 4 /Function's evaluations are bounded but far from free.
func (d *Device) functionImage(sh *shading.Shading, w, h int) *imagecore.Image {
	key := funcGridKey{sh: sh, w: w, h: h}
	if d.store != nil {
		if img, ok := d.store.Get[*imagecore.Image](key); ok {
			return img // A nil image is a cached failure (negative entry).
		}
	} else if img, ok := d.funcGrids[key]; ok {
		return img
	}
	img := d.realizeFunctionGrid(sh, w, h)
	if d.store != nil {
		d.store.Put(key, img, funcGridSize(w, h))
		return img
	}
	if d.funcGrids == nil {
		d.funcGrids = make(map[funcGridKey]*imagecore.Image)
	}
	if len(d.funcGrids) >= maxCachedFuncGrids {
		// Dropping the map, rather than refusing further entries, for the reason glyphMask does it: refusing would retire
		// the cache for the rest of the render, so every later draw would re-evaluate its grid with no prospect of a hit.
		// Retention is invisible to output — a hit reproduces the grid a miss would have evaluated, sample for sample.
		clear(d.funcGrids)
	}
	d.funcGrids[key] = img
	return img
}

// realizeFunctionGrid evaluates the shading's function once per cell of a w×h grid spanning its domain, at the cell
// centers, and wraps the samples as an opaque RGBA image. nil (a cached failure) when the image cannot be created.
func (d *Device) realizeFunctionGrid(sh *shading.Shading, w, h int) *imagecore.Image {
	x0, x1, y0, y1 := sh.Domain[0], sh.Domain[1], sh.Domain[2], sh.Domain[3]
	// Cell extents in float64, matching functionShader: the float32 difference of two far-apart finite domain bounds
	// overflows to +Inf and would place every sample of the grid at the same infinite domain point.
	dx := (float64(x1) - float64(x0)) / float64(w)
	dy := (float64(y1) - float64(y0)) / float64(h)
	pix := make([]byte, w*h*4)
	for j := range h {
		y := float32(float64(y0) + (float64(j)+0.5)*dy)
		row := pix[j*w*4:]
		for i := range w {
			x := float32(float64(x0) + (float64(i)+0.5)*dx)
			c := sh.ColorAt(x, y)
			row[i*4] = c.R
			row[i*4+1] = c.G
			row[i*4+2] = c.B
			row[i*4+3] = 255
		}
	}
	info := imagecore.ImageInfo{
		Width:     int32(w),
		Height:    int32(h),
		ColorType: imagecore.ColorTypeRGBA8888,
		AlphaType: imagecore.AlphaTypeOpaque,
	}
	return imagecore.NewRasterData(info, pix, w*4)
}

// clampDim converts a pixel extent to a grid dimension in [1, maxV]. The bounds are applied in float space on purpose:
// Go leaves a float→int conversion implementation-defined when the value does not fit, and the platforms disagree —
// amd64 saturates to math.MinInt64, which an int-space clamp reads as "too small" and rounds UP to 1 (a shading
// rendered as one flat color, a tiling pattern as a 1×1 tile), while arm64 saturates to math.MaxInt64. A shading
// /Matrix that blows the domain up to a 1e30 device extent, or an XStep×scale product that overflows to +Inf, must
// therefore never reach the conversion unclamped.
func clampDim(v float32, maxV int) int {
	if !(v >= 1) { // Catches NaN along with everything under a pixel.
		return 1
	}
	if v >= float32(maxV) { // Also catches +Inf.
		return maxV
	}
	return int(v)
}

// tileShader renders one tiling-pattern cell into an offscreen surface at the pattern's device scale and wraps it in a
// repeating image shader. local maps pattern space to the drawing space; patCTM is the full pattern-space→device matrix
// (used only for scale so the tile rasterizes at device resolution).
func (d *Device) tileShader(t *device.Tiling, local, patCTM gfx.Matrix) shaders.Shader {
	if !(t.XStep > 0) || !(t.YStep > 0) || !isFinite32(t.XStep) || !isFinite32(t.YStep) {
		return nil
	}
	sx := float32(math.Hypot(float64(patCTM.A), float64(patCTM.B)))
	sy := float32(math.Hypot(float64(patCTM.C), float64(patCTM.D)))
	if !isFinite32(sx) || !isFinite32(sy) || sx <= 0 || sy <= 0 {
		return nil
	}
	w := clampDim(t.XStep*sx+0.5, maxTileDim)
	h := clampDim(t.YStep*sy+0.5, maxTileDim)
	for w*h > maxTileArea {
		w = max(w/2, 1)
		h = max(h/2, 1)
	}
	// Pattern-space window [X0, X0+XStep] x [Y0, Y0+YStep] maps to the tile image with y flipped (image rows grow
	// downward while pattern y grows upward under the usual page CTM; patCTM's own flip is applied when the shader
	// samples, so the window mapping keeps pattern orientation).
	fw := float32(w) / t.XStep
	fh := float32(h) / t.YStep
	window := gfx.Matrix{A: fw, D: -fh, E: -t.BBox.X0 * fw, F: (t.BBox.Y0 + t.YStep) * fh}
	inv, ok := window.Invert()
	if !ok {
		return nil
	}
	img := d.tileImage(t, window, w, h)
	if img == nil {
		return nil
	}
	lm := matrix(inv.Mul(local))
	sampling := shaders.SamplingOptions{Filter: shaders.FilterNearest}
	return shaders.NewImage(img, shaders.TileRepeat, shaders.TileRepeat, sampling, &lm)
}

// tileCellKey identifies one rasterized tiling-pattern cell in the document store: the interpreter's identity for the
// cell content (device.Tiling.Key) plus the pixel size it was rasterized at. Distinct store key type per the store's
// kind-separation rule.
type tileCellKey struct {
	pattern any
	w, h    int
}

// tileImageSize estimates a rasterized cell's cache footprint for the store budget.
func tileImageSize(w, h int) uint64 { return 4*uint64(w)*uint64(h) + 128 }

// tileImage returns the pattern's cell rasterized at w×h, rendering it on first use. The result depends only on the
// cell content and that pixel size — window is derived from both, and the pattern's rotation and placement live in the
// shader's matrix, not in the tile — so with a store wired the image is cached there and shared by every later draw of
// the pattern at the same scale, across pages. Failures cache too (they are a property of the same inputs). Without a
// store every call rasterizes.
func (d *Device) tileImage(t *device.Tiling, window gfx.Matrix, w, h int) *imagecore.Image {
	key := tileCellKey{pattern: t.Key, w: w, h: h}
	cacheable := t.Key != nil && d.store != nil
	if cacheable {
		if img, ok := d.store.Get[*imagecore.Image](key); ok {
			return img // A nil image is a cached failure (negative entry).
		}
	}
	img := d.rasterizeTile(t, window, w, h)
	if cacheable {
		d.store.Put(key, img, tileImageSize(w, h))
	}
	return img
}

// rasterizeTile replays one cell's content into a w×h offscreen surface and snapshots it.
func (d *Device) rasterizeTile(t *device.Tiling, window gfx.Matrix, w, h int) *imagecore.Image {
	cell, err := New(w, h)
	if err != nil {
		return nil
	}
	cell.store = d.store
	// Cells whose box exceeds the steps spill into neighbors' windows; replay the necessary neighbor copies.
	nx := spillCopies(t.BBox.X1-t.BBox.X0, t.XStep)
	ny := spillCopies(t.BBox.Y1-t.BBox.Y0, t.YStep)
	// Corner form: the extents X1-X0/Y1-Y0 overflow to +Inf for a box spanning more than float32's range, and the
	// non-finite corners that yields clip the whole cell away (buildPath's guard would drop the path outright).
	bboxPath := &gfx.Path{}
	bboxPath.RectCorners(t.BBox.X0, t.BBox.Y0, t.BBox.X1, t.BBox.Y1)
	for i := -nx; i <= 0; i++ {
		for j := -ny; j <= 0; j++ {
			ctm := gfx.Translate(float32(i)*t.XStep, float32(j)*t.YStep).Mul(window)
			cell.ClipPath(bboxPath, false, ctm) // The cell content is clipped to /BBox (ISO 32000-2 8.7.3.1).
			t.Replay(cell, ctm)
			cell.PopClip()
		}
	}
	return cell.surf.MakeImageSnapshot()
}

// spillCopies reports how many neighbor cells (per direction, capped) can spill content into one tile window when the
// cell box is larger than the tile step.
func spillCopies(extent, step float32) int {
	if !(extent > step) {
		return 0
	}
	// The cap is applied in float space, as every other conversion in this package does (clampDim, rectInterior,
	// maskBounds, renderGlyphMask, fillTilingInto's lattice bounds, blitTextRun's origin clamp): extent is a float32
	// difference of two validated /BBox corners, so it reaches +Inf for a box spanning more than float32's range, and
	// int(+Inf) is implementation-defined (amd64 wraps to MinInt64, arm64 saturates to MaxInt64). Bounding first keeps
	// the result from depending on that. The comparison also rejects NaN.
	n := math.Ceil(float64(extent/step)) - 1
	if !(n > 0) {
		return 0
	}
	if n > maxTileCopies {
		return maxTileCopies
	}
	return int(n)
}

// maxReplayTiles caps how many cell replays one fill may trigger; beyond it the fill falls back to the repeating-image
// shader (coarser but bounded).
const maxReplayTiles = 4096

// fillTilingInto paints a tiling pattern into the device-space path by replaying the cell content once per lattice
// position at full device resolution — the fidelity MuPDF gets by replaying tiles — rather than resampling one
// rasterized tile. Falls back to the image-shader path when the fill would need an unbounded number of replays.
func (d *Device) fillTilingInto(devicePath *path.Path, p device.Paint) {
	t := p.Tiling
	inv, ok := p.PatternCTM.Invert()
	if !ok {
		return
	}
	bounds := devicePath.Bounds()
	var px0, py0, px1, py1 float32
	for i, c := range [4][2]float32{{bounds.Left, bounds.Top}, {bounds.Right, bounds.Top}, {bounds.Left, bounds.Bottom}, {bounds.Right, bounds.Bottom}} {
		x, y := inv.ApplyXY(c[0], c[1])
		if !isFinite32(x) || !isFinite32(y) {
			return
		}
		if i == 0 {
			px0, px1, py0, py1 = x, x, y, y
		} else {
			px0, px1 = min(px0, x), max(px1, x)
			py0, py1 = min(py0, y), max(py1, y)
		}
	}
	if !(t.XStep > 0) || !(t.YStep > 0) || !isFinite32(t.XStep) || !isFinite32(t.YStep) {
		return
	}
	// The lattice bounds are computed in float64 and validated BEFORE the int conversions: a hostile step (a denormal
	// /YStep, say) overflows the float32 division to ±Inf, and Go's out-of-range float→int conversion saturates — j0 ==
	// j1 == MaxInt64 passes an nx*ny cap yet `for j := j0; j <= j1; j++` never terminates (j++ wraps). Found by the
	// veraPDF soak; anything outside a sane index range takes the shader fallback.
	fi0 := math.Floor(float64((px0 - t.BBox.X1) / t.XStep))
	fi1 := math.Ceil(float64((px1 - t.BBox.X0) / t.XStep))
	fj0 := math.Floor(float64((py0 - t.BBox.Y1) / t.YStep))
	fj1 := math.Ceil(float64((py1 - t.BBox.Y0) / t.YStep))
	const maxLatticeIndex = 1 << 30 // far beyond any real lattice; NaN/Inf fail these comparisons too
	replayable := fi0 >= -maxLatticeIndex && fi1 <= maxLatticeIndex && fj0 >= -maxLatticeIndex && fj1 <= maxLatticeIndex
	i0, i1 := int(fi0), int(fi1)
	j0, j1 := int(fj0), int(fj1)
	nx, ny := i1-i0+1, j1-j0+1
	if !replayable || nx <= 0 || ny <= 0 || nx > maxReplayTiles || ny > maxReplayTiles || nx*ny > maxReplayTiles {
		// Too many tiles for replay: use the repeating-image shader instead (the path is device space, so the shader
		// anchors directly).
		if cpaint, okPaint := d.preparePaint(p, nil); okPaint {
			d.c.DrawPath(devicePath, cpaint)
		}
		return
	}
	// The per-tile clips and layer below are canvas state the device's clip tracking does not see, and Replay re-enters
	// the device with them active; keep the direct glyph blits off for the duration.
	d.untrackedState++
	defer func() { d.untrackedState-- }()
	count := d.c.Save()
	d.c.ClipPath(devicePath, raster.ClipIntersect, true)
	layered := p.Alpha < 1 || p.Blend != device.BlendNormal
	if layered {
		layerPaint := canvas.NewPaint()
		layerPaint.Color = colorcore.ARGB(alpha8(p.Alpha), 255, 255, 255)
		layerPaint.BlendMode = blendModes[p.Blend]
		d.c.SaveLayer(nil, layerPaint)
	}
	// One scratch path, rewound and rebuilt per cell, serves every cell of every fill: a fill replays up to
	// maxReplayTiles of them and each needs only a transformed copy of the same five-point box, so cloning per cell was
	// this loop's whole allocation cost. ClipPath rasterizes the path into clip runs before returning and retains no
	// reference to it, and the scratch is rebuilt from t.BBox at the top of every iteration, so a nested tiling fill
	// re-entering the device through Replay cannot leave stale contents behind.
	if d.tileScratch == nil {
		d.tileScratch = path.New()
		d.tileScratch.SetVolatile(true) // Its contents change per cell; nothing downstream should cache them.
	}
	clip := d.tileScratch
	for i := i0; i <= i1; i++ {
		for j := j0; j <= j1; j++ {
			// MuPDF rasterizes one tile and blits the copies at integer device offsets; quantizing each copy's device
			// translation reproduces that (every cell renders at cell (0,0)'s subpixel phase), pinned against the
			// tiling goldens at fractional scales.
			sx := float32(i)*t.XStep*p.PatternCTM.A + float32(j)*t.YStep*p.PatternCTM.C
			sy := float32(i)*t.XStep*p.PatternCTM.B + float32(j)*t.YStep*p.PatternCTM.D
			rx := float32(math.Floor(float64(sx)))
			ry := float32(math.Floor(float64(sy)))
			ctm := p.PatternCTM.Mul(gfx.Translate(rx, ry))
			// Both factors of the offset are validated, their product is not: a finite matrix and a finite step still
			// multiply past float32's range, and the ctm built from that is what Replay takes as a child interpreter's
			// INITIAL CTM — the one precondition drawpage.go rejects its caller's matrix up front to preserve, since cm
			// and a form's /Matrix only check the products they compute. A cell that far out cannot touch the surface
			// anyway, so it is skipped rather than replayed under a poisoned matrix and an empty clip.
			if !isFinite32(sx) || !isFinite32(sy) || !ctm.IsFinite() ||
				!rectFiniteUnder(t.BBox.X0, t.BBox.Y0, t.BBox.X1, t.BBox.Y1, ctm) {
				continue
			}
			m := matrix(ctm)
			clip.Rewind()
			clip.MoveTo(t.BBox.X0, t.BBox.Y0)
			clip.LineTo(t.BBox.X1, t.BBox.Y0)
			clip.LineTo(t.BBox.X1, t.BBox.Y1)
			clip.LineTo(t.BBox.X0, t.BBox.Y1)
			clip.Close()
			clip.Transform(&m)
			tileCount := d.c.Save()
			d.c.ClipPath(clip, raster.ClipIntersect, true) // Cell content is clipped to /BBox.
			t.Replay(d, ctm)
			d.c.RestoreToCount(tileCount)
		}
	}
	if layered {
		d.c.Restore()
	}
	d.c.RestoreToCount(count)
}

// withShadingBBox wraps draw in the shading's /BBox clip (in the shading target space mapped by PatternCTM), when one
// is present.
func (d *Device) withShadingBBox(p device.Paint, draw func()) {
	sh := p.Shading
	if sh == nil || sh.BBox == nil {
		draw()
		return
	}
	// Through buildPath's seam, in corner form: shading.rectFrom validated the four /BBox entries individually, exactly
	// as content.rectFrom does for a form's box, and exactly as there that validation does not survive the mapping into
	// the space the box is clipped in. A box that maps past float32's range covers the whole surface — it clips nothing
	// — so the draw goes out unclipped, where clipping to the ±Inf corners paints nothing at all.
	var box gfx.Path
	box.RectCorners(sh.BBox.X0, sh.BBox.Y0, sh.BBox.X1, sh.BBox.Y1)
	bb, ok := buildPathIn(&box, false, p.PatternCTM)
	if !ok {
		draw()
		return
	}
	count := d.c.Save()
	d.c.ClipPath(bb, raster.ClipIntersect, true)
	draw()
	d.c.RestoreToCount(count)
}

// drawMesh paints a tessellated mesh's flat triangles under the pattern matrix. Triangles are drawn without
// antialiasing so shared edges neither seam nor double-blend (pixel centers land in exactly one triangle), matching
// MuPDF's non-antialiased mesh rasterization; the region's outer boundary usually comes from a clip, which carries its
// own antialiasing.
func (d *Device) drawMesh(sh *shading.Shading, patCTM gfx.Matrix, alpha float64, blend device.Blend) {
	a := alpha8(alpha)
	m := matrix(patCTM)
	count := d.c.Save()
	// MuPDF's shade painter tests each pixel at its top-right device corner where Skia's non-AA fill tests the center
	// (pinned against the mesh goldens: at integer boundaries MuPDF's coverage sits one pixel left/down of center
	// sampling). Shift the whole mesh by (-0.5, +0.5) device pixels — with a hair extra so exactly-on-boundary centers
	// resolve the way MuPDF's inclusive/exclusive edges do.
	d.c.Translate(-0.501, 0.501)
	d.c.Concat(&m)
	paint := canvas.NewPaint()
	paint.AntiAlias = false
	paint.BlendMode = blendModes[blend]
	// One scratch path, rewound per triangle, serves every triangle of every draw: a mesh carries up to maxTriangles of
	// them and the whole loop re-runs for each fill, stroke, image mask or sh operator that uses the shading (the
	// tessellation is cached on the *shading.Shading; the rasterization cannot be — each triangle carries its own flat
	// color and lands under a different matrix — which is why the interpreter charges its work budget per painting
	// operation for the triangles a draw rasterizes, see content's shadingPaintCost), so a fresh path each time was the
	// dominant allocation here. Rewind keeps the verb/point storage, leaving the per-triangle cost at three points.
	// The scratch cannot be shared out from under itself: drawMesh only calls into canvas, never back into the device,
	// and a tiling replay that paints a mesh does so on its own cell device (rasterizeTile).
	if d.meshScratch == nil {
		d.meshScratch = path.New()
		d.meshScratch.SetVolatile(true) // Its contents change on every draw; nothing downstream should cache them.
	}
	p := d.meshScratch
	for i := range sh.Triangles {
		tri := &sh.Triangles[i]
		p.Rewind()
		p.MoveTo(tri.P[0].X, tri.P[0].Y)
		p.LineTo(tri.P[1].X, tri.P[1].Y)
		p.LineTo(tri.P[2].X, tri.P[2].Y)
		p.Close()
		ca := uint8((uint32(a)*uint32(tri.Color.A) + 127) / 255)
		paint.Color = colorcore.ARGB(ca, tri.Color.R, tri.Color.G, tri.Color.B)
		d.c.DrawPath(p, paint)
	}
	d.c.RestoreToCount(count)
}

// fillMeshInto clips to the device-space path and draws the mesh through it. A nil path means the caller's region does
// not fit float32 once mapped into device space, which is a region covering everything: the mesh then draws unclipped
// rather than through the ±Inf-cornered path canvas would turn into an empty clip.
func (d *Device) fillMeshInto(devicePath *path.Path, p device.Paint) {
	count := d.c.Save()
	if devicePath != nil {
		d.c.ClipPath(devicePath, raster.ClipIntersect, true)
	}
	d.withShadingBBox(p, func() {
		d.drawMesh(p.Shading, p.PatternCTM, p.Alpha, p.Blend)
	})
	d.c.RestoreToCount(count)
}

// maskedMesh draws the mesh into a layer and keeps only the region drawMask covers (BlendDstIn), applying the paint's
// alpha and blend at the layer composite. Used where a clip path is unavailable: stroked regions and image masks
// painted with a mesh-shading pattern.
func (d *Device) maskedMesh(p device.Paint, drawMask func(mask *canvas.Paint)) {
	layerPaint := canvas.NewPaint()
	layerPaint.Color = colorcore.ARGB(alpha8(p.Alpha), 255, 255, 255)
	layerPaint.BlendMode = blendModes[p.Blend]
	d.c.SaveLayer(nil, layerPaint)
	d.withShadingBBox(p, func() {
		d.drawMesh(p.Shading, p.PatternCTM, 1, device.BlendNormal)
	})
	mask := canvas.NewPaint()
	mask.AntiAlias = true
	mask.Color = colorcore.White
	mask.BlendMode = raster.BlendDstIn
	drawMask(mask)
	d.c.Restore()
}
