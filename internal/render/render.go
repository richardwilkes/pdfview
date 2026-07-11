// Package render is the raster device: the only package that imports github.com/richardwilkes/canvas (plan.md
// invariant 3; canvas/gpu is never imported, keeping the build cgo- and purego-free). It draws the interpreter's
// device calls onto a premultiplied N32 raster surface and hands the premultiplied pixels back through Pixels;
// the root package's unpremultiply loop converts them to the straight alpha image.NRGBA the public API promises
// (reading back premultiplied is deliberate — see the 2026-07-11 decision-log entry on rounding parity).
//
// Milestone M4 implemented path fills, strokes (with dashing), and clips; M5 added images (RGBA draws and
// Alpha8 stencil tinting under the image CTM). The text methods land with M6 and shadings/groups/masks with M8;
// until then those calls are no-ops except for their stack obligations, which are always honored so the
// interpreter's push/pop pairing holds.
package render

import (
	"errors"
	stdcolor "image/color"
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
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// ErrSurface is reported when the raster surface cannot be created (non-positive or absurd dimensions).
var ErrSurface = errors.New("unable to create raster surface")

// Device rasterizes device calls onto a canvas surface. Create one per render with New; it is not safe for
// concurrent use (the document mutex in the public API serializes renders anyway).
type Device struct {
	surf *surface.Surface
	c    *canvas.Canvas
	// clipStack records the canvas save count at each clip push so PopClip can restore precisely.
	clipStack []int
	width     int
	height    int
}

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

// FillPath implements device.Device.
func (d *Device) FillPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix, paint device.Paint) {
	cp := buildPath(p, evenOdd)
	m := matrix(ctm)
	count := d.c.Save()
	d.c.Concat(&m)
	d.c.DrawPath(cp, paintFor(paint))
	d.c.RestoreToCount(count)
}

// StrokePath implements device.Device.
func (d *Device) StrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix, paint device.Paint) {
	cp := buildPath(p, false)
	cpaint := paintFor(paint)
	strokeInto(cpaint, sp)
	m := matrix(ctm)
	count := d.c.Save()
	d.c.Concat(&m)
	d.c.DrawPath(cp, cpaint)
	d.c.RestoreToCount(count)
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

// FillText implements device.Device (text lands at M6).
func (d *Device) FillText(*device.TextRun, device.Paint) {}

// StrokeText implements device.Device (text lands at M6).
func (d *Device) StrokeText(*device.TextRun, *gfx.StrokeParams, device.Paint) {}

// ClipText implements device.Device. Glyph outlines have not landed yet, so nothing accumulates; the clip
// pushed by EndTextClip is a no-op level until they do.
func (d *Device) ClipText(*device.TextRun) {}

// EndTextClip implements device.Device: push the text clip accumulated by ClipText since the last
// EndTextClip as one clip level. Until glyph outlines land the level is pushed without a clip region (a
// wrong-but-safe degrade: clipping to the glyphs' union would need the glyphs; clipping to nothing would
// wrongly erase subsequent content), keeping the PopClip pairing intact.
func (d *Device) EndTextClip() {
	d.clipStack = append(d.clipStack, d.c.Save())
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
	d.drawImage(ci, img, ctm, paint)
}

// FillImageMask implements device.Device: the stencil's Alpha8 pixels are tinted by the fill paint's color
// (with its folded alpha), PDF's image-mask stencil semantics.
func (d *Device) FillImageMask(img *imaging.Image, ctm gfx.Matrix, paint device.Paint) {
	ci := rasterImage(img)
	if ci == nil {
		return
	}
	d.drawImage(ci, img, ctm, paintFor(paint))
}

// ClipImageMask implements device.Device. No producer exists until tiling patterns and Type3 glyphs recurse
// (M6/M8), so the mask bits are not yet consulted: the clip is the mask's unit square under the ctm, a correct
// outer bound (a mask can only mark inside its square).
func (d *Device) ClipImageMask(_ *imaging.Image, ctm gfx.Matrix) {
	square := &gfx.Path{}
	square.Rect(0, 0, 1, 1)
	d.ClipPath(square, false, ctm)
}

// BeginGroup implements device.Device (transparency groups land at M8). The layer indirection already exists
// so group content composites as a unit once blends/isolation arrive.
func (d *Device) BeginGroup(_ gfx.Rect, _, _ bool, _ device.Blend, alpha float64) {
	if alpha < 0 {
		alpha = 0
	} else if alpha > 1 {
		alpha = 1
	}
	d.c.SaveLayerAlpha(nil, uint8(alpha*255+0.5))
}

// EndGroup implements device.Device.
func (d *Device) EndGroup() {
	d.c.Restore()
}

// BeginMask implements device.Device (soft masks land at M8).
func (d *Device) BeginMask(gfx.Rect, bool, stdcolor.NRGBA) {}

// EndMask implements device.Device (soft masks land at M8).
func (d *Device) EndMask() {}

// PopMask implements device.Device (soft masks land at M8).
func (d *Device) PopMask() {}

// FillShading implements device.Device (shadings land at M8).
func (d *Device) FillShading(*shading.Shading, gfx.Matrix, float64) {}
