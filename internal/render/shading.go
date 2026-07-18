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
// repeating image shader.

// Limits: caps on the offscreen resolutions hostile content can request. A function-based shading evaluates its
// function once per grid cell (maxFunctionArea bounds that work); a tiling cell rasterizes at the pattern's device
// scale (maxTileDim/maxTileArea bound the surface), degrading to a coarser tile beyond them. maxExtendFactor bounds the
// parametric gradient extension search. maxTileCopies bounds how many neighbor-cell copies replay into one tile when
// the cell box overlaps its steps.
const (
	maxFunctionDim  = 512
	maxFunctionArea = 1 << 18
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
	if ctm != nil {
		inv, ok := ctm.Invert()
		if !ok {
			return nil, false
		}
		local = local.Mul(inv)
	}
	var shader shaders.Shader
	if p.Shading != nil {
		shader = d.shadingShader(p.Shading, local)
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
// runs in.
func (d *Device) shadingShader(sh *shading.Shading, local gfx.Matrix) shaders.Shader {
	switch sh.Kind {
	case shading.KindAxial:
		return d.axialShader(sh, local)
	case shading.KindRadial:
		return d.radialShader(sh, local)
	case shading.KindFunction:
		return d.functionShader(sh, local)
	default:
		return nil
	}
}

// gradientRamp converts sampled stops to the canvas color/position arrays, extending the parametric span by e0 before
// offset 0 and e1 after offset 1 (in units of the original span) with duplicated boundary colors.
func gradientRamp(stops []shading.Stop, e0, e1 float32) (colors []colorcore.Color, pos []float32) {
	span := 1 + e0 + e1
	n := len(stops)
	if e0 > 0 {
		n++
	}
	if e1 > 0 {
		n++
	}
	colors = make([]colorcore.Color, 0, n)
	pos = make([]float32, 0, n)
	if e0 > 0 {
		c := stops[0].Color
		colors = append(colors, colorcore.ARGB(c.A, c.R, c.G, c.B))
		pos = append(pos, 0)
	}
	for _, s := range stops {
		colors = append(colors, colorcore.ARGB(s.Color.A, s.Color.R, s.Color.G, s.Color.B))
		pos = append(pos, (s.Offset+e0)/span)
	}
	if e1 > 0 {
		c := stops[len(stops)-1].Color
		colors = append(colors, colorcore.ARGB(c.A, c.R, c.G, c.B))
		pos = append(pos, 1)
	}
	return colors, pos
}

// coverageCorners maps the device surface's corners into the space local maps FROM (the shading target space), for
// sizing gradient extensions.
func (d *Device) coverageCorners(local gfx.Matrix) ([4]gfx.Point, bool) {
	inv, ok := local.Invert()
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
func (d *Device) axialShader(sh *shading.Shading, local gfx.Matrix) shaders.Shader {
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
		corners, ok := d.coverageCorners(local)
		if !ok {
			return nil
		}
		sMin, sMax := float32(0), float32(1)
		for _, c := range corners {
			s := ((c.X-p0.X)*dx + (c.Y-p0.Y)*dy) / lenSq
			sMin = min(sMin, s)
			sMax = max(sMax, s)
		}
		if sh.Extend[0] {
			e0 = min(-sMin+1, maxExtendFactor)
		}
		if sh.Extend[1] {
			e1 = min(sMax, maxExtendFactor)
		}
		p0 = geom.Point{X: p0.X - e0*dx, Y: p0.Y - e0*dy}
		p1 = geom.Point{X: p1.X + e1*dx, Y: p1.Y + e1*dy}
	}
	colors, pos := gradientRamp(sh.Stops, e0, e1)
	return shaders.NewLinearGradient(p0, p1, colors, pos, tile, &lm)
}

// radialShader builds the two-point conical shader for a type 3 shading.
func (d *Device) radialShader(sh *shading.Shading, local gfx.Matrix) shaders.Shader {
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
		corners, ok := d.coverageCorners(local)
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
	colors, pos := gradientRamp(sh.Stops, e0, e1)
	return shaders.NewTwoPointConicalGradient(geom.Point{X: c0.X, Y: c0.Y}, r0,
		geom.Point{X: c1.X, Y: c1.Y}, r1, colors, pos, tile, &lm)
}

// radialExtension finds the parametric extension factor (in units of the t span) that either covers every corner with
// the extended circle or reaches the radius-zero cutoff PDF prescribes (ISO 32000-2 8.7.4.5.4: extension continues
// until the circles cover the area or the radius becomes 0).
func radialExtension(c0, c1 gfx.Point, r0, r1 float32, corners [4]gfx.Point, atStart bool) float32 {
	dr := r1 - r0
	// The factor at which the extended radius reaches zero, when it shrinks in this direction.
	cap32 := float32(maxExtendFactor)
	if atStart && dr > 0 {
		cap32 = r0 / dr
	} else if !atStart && dr < 0 {
		cap32 = r1 / -dr
	}
	covered := func(e float32) bool {
		var t, r float32
		if atStart {
			t = -e
		} else {
			t = 1 + e
		}
		cx := c0.X + t*(c1.X-c0.X)
		cy := c0.Y + t*(c1.Y-c0.Y)
		r = r0 + t*dr
		if r < 0 {
			return false
		}
		for _, q := range corners {
			dx, dy := q.X-cx, q.Y-cy
			if dx*dx+dy*dy > r*r {
				return false
			}
		}
		return true
	}
	for e := float32(1); e <= maxExtendFactor; e *= 2 {
		if e >= cap32 {
			return cap32
		}
		if covered(e) {
			return e
		}
	}
	return min(float32(maxExtendFactor), cap32)
}

// functionShader realizes a type 1 shading as an image shader: the function is evaluated over a grid spanning the
// domain at roughly device resolution (capped), and the image is placed by the domain-to-device mapping with decal
// tiling so points outside the domain stay unpainted.
func (d *Device) functionShader(sh *shading.Shading, local gfx.Matrix) shaders.Shader {
	x0, x1, y0, y1 := sh.Domain[0], sh.Domain[1], sh.Domain[2], sh.Domain[3]
	if !(x1 > x0) || !(y1 > y0) {
		return nil
	}
	full := sh.Matrix.Mul(local)
	// Size the grid from the domain rectangle's device extent.
	var minX, minY, maxX, maxY float32
	for i, c := range [4]gfx.Point{{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x0, Y: y1}, {X: x1, Y: y1}} {
		px, py := full.ApplyXY(c.X, c.Y)
		if !isFinite32(px) || !isFinite32(py) {
			return nil
		}
		if i == 0 {
			minX, maxX, minY, maxY = px, px, py, py
		} else {
			minX, maxX = min(minX, px), max(maxX, px)
			minY, maxY = min(minY, py), max(maxY, py)
		}
	}
	w := clampDim(int(maxX-minX+1), maxFunctionDim)
	h := clampDim(int(maxY-minY+1), maxFunctionDim)
	for w*h > maxFunctionArea {
		w = max(w/2, 1)
		h = max(h/2, 1)
	}
	pix := make([]byte, w*h*4)
	for j := range h {
		y := y0 + (float32(j)+0.5)/float32(h)*(y1-y0)
		row := pix[j*w*4:]
		for i := range w {
			x := x0 + (float32(i)+0.5)/float32(w)*(x1-x0)
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
	img := imagecore.NewRasterData(info, pix, w*4)
	if img == nil {
		return nil
	}
	// Image pixel -> domain -> /Matrix -> target space.
	toDomain := gfx.Matrix{A: (x1 - x0) / float32(w), D: (y1 - y0) / float32(h), E: x0, F: y0}
	lm := matrix(toDomain.Mul(full))
	sampling := shaders.SamplingOptions{Filter: shaders.FilterNearest}
	return shaders.NewImage(img, shaders.TileDecal, shaders.TileDecal, sampling, &lm)
}

func clampDim(v, maxV int) int {
	if v < 1 {
		return 1
	}
	if v > maxV {
		return maxV
	}
	return v
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
	w := clampDim(int(t.XStep*sx+0.5), maxTileDim)
	h := clampDim(int(t.YStep*sy+0.5), maxTileDim)
	for w*h > maxTileArea {
		w = max(w/2, 1)
		h = max(h/2, 1)
	}
	cell, err := New(w, h)
	if err != nil {
		return nil
	}
	cell.store = d.store
	// Pattern-space window [X0, X0+XStep] x [Y0, Y0+YStep] maps to the tile image with y flipped (image rows grow
	// downward while pattern y grows upward under the usual page CTM; patCTM's own flip is applied when the shader
	// samples, so the window mapping keeps pattern orientation).
	fw := float32(w) / t.XStep
	fh := float32(h) / t.YStep
	window := gfx.Matrix{A: fw, D: -fh, E: -t.BBox.X0 * fw, F: (t.BBox.Y0 + t.YStep) * fh}
	// Cells whose box exceeds the steps spill into neighbors' windows; replay the necessary neighbor copies.
	nx := spillCopies(t.BBox.X1-t.BBox.X0, t.XStep)
	ny := spillCopies(t.BBox.Y1-t.BBox.Y0, t.YStep)
	bboxPath := &gfx.Path{}
	bboxPath.Rect(t.BBox.X0, t.BBox.Y0, t.BBox.X1-t.BBox.X0, t.BBox.Y1-t.BBox.Y0)
	for i := -nx; i <= 0; i++ {
		for j := -ny; j <= 0; j++ {
			ctm := gfx.Translate(float32(i)*t.XStep, float32(j)*t.YStep).Mul(window)
			cell.ClipPath(bboxPath, false, ctm) // The cell content is clipped to /BBox (ISO 32000-2 8.7.3.1).
			t.Replay(cell, ctm)
			cell.PopClip()
		}
	}
	img := cell.surf.MakeImageSnapshot()
	if img == nil {
		return nil
	}
	inv, ok := window.Invert()
	if !ok {
		return nil
	}
	lm := matrix(inv.Mul(local))
	sampling := shaders.SamplingOptions{Filter: shaders.FilterNearest}
	return shaders.NewImage(img, shaders.TileRepeat, shaders.TileRepeat, sampling, &lm)
}

// spillCopies reports how many neighbor cells (per direction, capped) can spill content into one tile window when the
// cell box is larger than the tile step.
func spillCopies(extent, step float32) int {
	if !(extent > step) {
		return 0
	}
	n := int(math.Ceil(float64(extent/step))) - 1
	return min(n, maxTileCopies)
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
	bboxPath := path.New()
	bboxPath.MoveTo(t.BBox.X0, t.BBox.Y0)
	bboxPath.LineTo(t.BBox.X1, t.BBox.Y0)
	bboxPath.LineTo(t.BBox.X1, t.BBox.Y1)
	bboxPath.LineTo(t.BBox.X0, t.BBox.Y1)
	bboxPath.Close()
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
			m := matrix(ctm)
			clip := bboxPath.Clone()
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
	count := d.c.Save()
	bb := path.New()
	bb.MoveTo(sh.BBox.X0, sh.BBox.Y0)
	bb.LineTo(sh.BBox.X1, sh.BBox.Y0)
	bb.LineTo(sh.BBox.X1, sh.BBox.Y1)
	bb.LineTo(sh.BBox.X0, sh.BBox.Y1)
	bb.Close()
	m := matrix(p.PatternCTM)
	bb.Transform(&m)
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
	for i := range sh.Triangles {
		tri := &sh.Triangles[i]
		p := path.New()
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

// fillMeshInto clips to the device-space path and draws the mesh through it.
func (d *Device) fillMeshInto(devicePath *path.Path, p device.Paint) {
	count := d.c.Save()
	d.c.ClipPath(devicePath, raster.ClipIntersect, true)
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
