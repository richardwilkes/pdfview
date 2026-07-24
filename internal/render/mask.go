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
	stdcolor "image/color"
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
	"github.com/richardwilkes/pdfview/internal/gfx"
)

// Transparency groups and soft masks. A group maps to SaveLayer with the group's constant alpha and blend mode on the
// restore paint; a NON-isolated group whose composite is trivial (alpha 1, blend Normal, no knockout) is passed through
// without a layer, which reproduces non-isolated semantics exactly — interior blend modes then see the true backdrop,
// as MuPDF does (oracle-pinned by the isolation probe). A non-isolated group with a non-trivial composite still gets a
// layer, an accepted isolated approximation.
//
// Soft masks render their content to a separate offscreen surface: BeginMask swaps the canvas, EndMask reads the
// surface back, reduces it to an 8-bit coverage plane (rendered alpha for /S /Alpha; the captured MuPDF luminosity
// response for /S /Luminosity — see maskLuma), applies the /TR LUT, and opens the masked-content layer on the main
// canvas; PopMask multiplies the layer by the plane (BlendDstIn) and restores it.
//
// Every span is sized to the mask's device-space bbox, not the page: the interpreter clips mask content to that box
// (content.replayMask), so the surface, the readback, the coverage plane, and the DstIn all cover only it, and the
// area beyond it takes the single coverage value an out-of-bbox mask sample has (maskOutsideValue). This matters
// because the interpreter wraps EVERY painting operation under an active /SMask in its own Begin/End/Pop cycle — a
// page-sized allocate-and-scan per operation would otherwise cost a full page's bytes per fill, stroke, glyph run, and
// image.

// groupState is one BeginGroup's record.
type groupState struct {
	count    int // canvas save count to restore (valid when layered)
	layered  bool
	knockout bool
}

// maskState is one BeginMask..PopMask span's record.
type maskState struct {
	surf       *surface.Surface // nil when there is no mask surface (see BeginMask); released at EndMask
	saved      *canvas.Canvas
	savedClip  *path.Path       // the interrupted text-clip accumulation, restored at EndMask
	mask       *imagecore.Image // the Alpha8 coverage plane, built at EndMask
	transfer   []byte
	bytes      uint64 // the surface's charge against the device's mask-byte budget, refunded at EndMask
	x0, y0     int    // the plane's device-pixel origin
	w, h       int    // the plane's device-pixel extent
	layer      int    // masked-content layer save count (valid after EndMask)
	guard      int    // guard layer save count (surf == nil only)
	outside    uint8  // the coverage every pixel the plane does not cover takes
	ended      bool   // EndMask has run for this span; a repeat is ignored
	luminosity bool
	constant   bool // no plane: the mask is its outside value everywhere (bbox wholly off the surface)
	bounded    bool // the masked-content layer is clipped to the plane's rectangle
}

// BeginGroup implements device.Device.
func (d *Device) BeginGroup(_ gfx.Rect, isolated, knockout bool, blend device.Blend, alpha float64) {
	trivial := alpha >= 1 && blend == device.BlendNormal && !knockout
	if !isolated && trivial {
		// Non-isolated with nothing to composite: drawing inline IS the group semantics (interior blends composite
		// against the true backdrop).
		d.groupStack = append(d.groupStack, groupState{})
		return
	}
	paint := canvas.NewPaint()
	paint.Color = colorcore.ARGB(alpha8(alpha), 255, 255, 255)
	paint.BlendMode = blendModes[blend]
	count := d.c.SaveLayer(nil, paint)
	d.groupStack = append(d.groupStack, groupState{count: count, layered: true, knockout: knockout})
}

// EndGroup implements device.Device.
func (d *Device) EndGroup() {
	n := len(d.groupStack)
	if n == 0 {
		return
	}
	g := d.groupStack[n-1]
	d.groupStack = d.groupStack[:n-1]
	if g.layered {
		d.c.RestoreToCount(g.count)
	}
}

// knockoutSrc reports whether solid draws must composite with BlendSrc: inside a knockout group, each object replaces
// what earlier objects painted where it covers (ISO 32000-2 11.4.5; with the group's layer starting transparent,
// compositing the object against the initial backdrop then replacing is exactly BlendSrc under coverage). Suppressed
// while a soft-mask span is open — mask content composites normally, and a masked op lives in its own layer where Src
// has nothing to knock out.
func (d *Device) knockoutSrc() bool {
	return len(d.maskStack) == 0 && len(d.groupStack) > 0 && d.groupStack[len(d.groupStack)-1].knockout
}

// applyKnockout rewrites a solid paint's blend for knockout-group interiors.
func (d *Device) applyKnockout(paint *canvas.Paint) {
	if d.knockoutSrc() {
		paint.BlendMode = raster.BlendSrc
	}
}

// maxMaskDepth caps how many soft-mask spans may be open at once, and maxMaskPages the offscreen surface bytes they may
// hold between them, as a multiple of the page's own byte size. Each open span holds a bbox-sized offscreen surface, so
// unbounded nesting is a memory-pressure vector; the depth cap alone bounds only the COUNT, which at page-sized boxes
// is still maxMaskDepth pages of live commitment. Upstream form-XObject recursion already bounds mask nesting well
// below the depth cap; both limits keep the commitment bounded even if that upstream invariant is ever broken,
// degrading deeper masks to the no-surface path (mask content is swallowed, the masked op still draws) rather than
// allocating without limit. The budget is a multiple of the page so the outermost mask — which can legitimately cover
// the whole page at any dpi — always fits.
const (
	maxMaskDepth = 16
	maxMaskPages = 4
)

// maskByteBudget is the ceiling on the offscreen bytes all open mask spans may hold at once.
func (d *Device) maskByteBudget() uint64 {
	return maxMaskPages * 4 * uint64(d.width) * uint64(d.height)
}

// BeginMask implements device.Device.
func (d *Device) BeginMask(bbox gfx.Rect, luminosity bool, backdrop stdcolor.NRGBA, transfer []byte) {
	ms := &maskState{luminosity: luminosity, transfer: transfer, saved: d.c, savedClip: d.textClip}
	d.textClip = nil
	ms.outside = maskOutsideValue(luminosity, backdrop, transfer)
	// The mask content draws under whatever matrix the canvas being masked carries — a Wrapped device's caller may have
	// translated, scaled, or rotated theirs — so the bbox (device space as the interpreter sees it) maps through that
	// matrix to reach the pixels the coverage plane is applied at.
	base := d.c.TotalMatrix()
	var onSurface bool
	ms.x0, ms.y0, ms.w, ms.h, onSurface = d.maskBounds(bbox, &base)
	bytes := 4 * uint64(ms.w) * uint64(ms.h)
	if onSurface && len(d.maskStack) < maxMaskDepth && d.maskBytes+bytes <= d.maskByteBudget() {
		if ms.surf = surface.NewRasterN32Premul(int32(ms.w), int32(ms.h), nil); ms.surf != nil {
			ms.bytes = bytes
			d.maskBytes += bytes
		}
	}
	if ms.surf == nil {
		// No mask surface: isolate the mask content in an invisible layer so it cannot mark the page. With the bbox
		// wholly off the surface there is nothing to rasterize and the mask is its outside value everywhere, which
		// PopMask still applies; when the surface could not be created at all (depth or byte cap, allocation failure)
		// the masked op instead draws unmasked (degrade, never erase).
		ms.constant = !onSurface
		ms.guard = d.c.SaveLayerAlpha(nil, 0)
	} else {
		d.c = ms.surf.Canvas()
		// The mask surface is a fresh canvas at identity covering only the bbox, so adopt the masked canvas's matrix
		// with the surface's device-pixel origin removed: mask content then rasterizes into the same pixels the plane is
		// applied at, offset into the smaller surface.
		base.PostTranslate(float32(-ms.x0), float32(-ms.y0))
		d.c.SetMatrix(&base)
		if luminosity {
			// The mask group composites over the /BC backdrop before luminance extraction; prefilling the whole surface
			// also gives areas inside the surface but outside the group's BBox the backdrop's luminosity (areas outside
			// the surface get it through ms.outside).
			p := canvas.NewPaint()
			p.Color = colorcore.ARGB(255, backdrop.R, backdrop.G, backdrop.B)
			d.c.DrawPaint(p)
		}
	}
	d.maskStack = append(d.maskStack, ms)
}

// maskOutsideValue is the coverage every pixel outside the mask's bbox takes: the mask content cannot mark there, so an
// alpha mask samples zero and a luminosity mask samples the /BC backdrop the group composites over, both through /TR.
// It is exactly what a page-sized mask surface would hold outside the bbox.
func maskOutsideValue(luminosity bool, backdrop stdcolor.NRGBA, transfer []byte) uint8 {
	v := uint8(0)
	if luminosity {
		v = maskLuma(backdrop.R, backdrop.G, backdrop.B)
	}
	if len(transfer) == 256 { // Only a full LUT is usable; see maskPlane.
		v = transfer[v]
	}
	return v
}

// maskBounds returns the device-pixel rectangle a mask span's surface must cover: the mask content's bbox mapped
// through base, snapped outward to whole pixels with a pixel of margin so the antialiased edge of the bbox clip is
// never cut, and intersected with the surface. The interpreter clips mask content to this bbox, so nothing the mask
// paints can fall outside it. A bbox with no area carries no information — the interpreter emits an empty one when the
// mask's CTM is unusable, and a caller that does not compute one passes the zero rect — so it degrades to the whole
// surface, as do non-finite and absurd corners. ok is false when the rectangle lies entirely off the surface: the mask
// has no rasterizable content at all and reduces to its constant outside coverage.
func (d *Device) maskBounds(bbox gfx.Rect, base *geom.Matrix) (x0, y0, w, h int, ok bool) {
	if !(bbox.X1 > bbox.X0) || !(bbox.Y1 > bbox.Y0) {
		return 0, 0, d.width, d.height, true
	}
	var minX, minY, maxX, maxY float32
	for i, c := range [4][2]float32{{bbox.X0, bbox.Y0}, {bbox.X1, bbox.Y0}, {bbox.X0, bbox.Y1}, {bbox.X1, bbox.Y1}} {
		p := base.MapXY(c[0], c[1])
		if !isFinite32(p.X) || !isFinite32(p.Y) {
			return 0, 0, d.width, d.height, true
		}
		if i == 0 {
			minX, maxX, minY, maxY = p.X, p.X, p.Y, p.Y
		} else {
			minX, maxX = min(minX, p.X), max(maxX, p.X)
			minY, maxY = min(minY, p.Y), max(maxY, p.Y)
		}
	}
	// Keep the floor/ceil conversions in range; Go leaves an out-of-range float→int conversion implementation-defined
	// and the platforms disagree (mirrors rectInterior's clamp).
	const maxReasonable = 1 << 24
	if minX < -maxReasonable || maxX > maxReasonable || minY < -maxReasonable || maxY > maxReasonable {
		return 0, 0, d.width, d.height, true
	}
	x0 = max(int(math.Floor(float64(minX)))-1, 0)
	y0 = max(int(math.Floor(float64(minY)))-1, 0)
	x1 := min(int(math.Ceil(float64(maxX)))+1, d.width)
	y1 := min(int(math.Ceil(float64(maxY)))+1, d.height)
	if x1 <= x0 || y1 <= y0 {
		return 0, 0, 0, 0, false
	}
	return x0, y0, x1 - x0, y1 - y0, true
}

// EndMask implements device.Device.
func (d *Device) EndMask() {
	n := len(d.maskStack)
	if n == 0 {
		return
	}
	ms := d.maskStack[n-1]
	if ms.ended {
		// EndMask leaves the span on the stack for PopMask, so a repeated call would land here again: it would take the
		// no-surface branch (EndMask releases the surface) and restore to a guard count only that branch ever sets,
		// unwinding the canvas to save count 0 — including state the interpreter still expects to pop — and then open a
		// second masked-content layer whose count overwrote the first. The other stack operations here (EndGroup,
		// PopMask, PopClip) all guard their own stack, so this one does too.
		return
	}
	ms.ended = true
	if ms.surf != nil {
		ms.mask = d.maskPlane(ms)
	}
	d.closeMaskSpan(ms)
	switch {
	case ms.mask != nil && ms.outside == 0:
		// Nothing outside the plane survives the DstIn, so bound the masked-content layer to the plane's rectangle: the
		// layer device covers the mask's area instead of the whole page, and PopMask's DstIn only has to touch it. The
		// bounds are device pixels, so they are given with the canvas at identity and the caller's matrix put back for
		// the content draws.
		m := d.c.TotalMatrix()
		ms.layer = d.c.Save()
		d.c.ResetMatrix()
		bounds := ms.rect()
		d.c.SaveLayer(&bounds, nil)
		d.c.SetMatrix(&m)
		ms.bounded = true
	case ms.constant && ms.outside == 0:
		// Masked out everywhere: the layer's zero alpha makes canvas skip the content entirely.
		ms.layer = d.c.SaveLayerAlpha(nil, 0)
	default:
		ms.layer = d.c.SaveLayer(nil, nil)
	}
}

// closeMaskSpan undoes BeginMask's canvas swap: it puts the interrupted text-clip accumulation back and either closes
// the no-surface guard layer or restores the page canvas and releases the mask surface. The plane EndMask builds is a
// copy, so the surface (and its budget charge) goes here rather than being held for the masked op's whole span.
func (d *Device) closeMaskSpan(ms *maskState) {
	d.textClip = ms.savedClip
	if ms.surf == nil {
		d.c.RestoreToCount(ms.guard)
		return
	}
	d.c = ms.saved
	ms.surf = nil
	d.maskBytes -= ms.bytes
	ms.bytes = 0
}

// rect is the plane's device-pixel rectangle.
func (ms *maskState) rect() geom.Rect {
	return geom.RectXYWH(float32(ms.x0), float32(ms.y0), float32(ms.w), float32(ms.h))
}

// PopMask implements device.Device.
func (d *Device) PopMask() {
	n := len(d.maskStack)
	if n == 0 {
		return
	}
	ms := d.maskStack[n-1]
	d.maskStack = d.maskStack[:n-1]
	if !ms.ended {
		// EndMask never ran for this span, so there is no coverage plane and no masked-content layer: ms.layer is still
		// zero and d.c may still be the mask surface's canvas. Restoring to count 0 would unwind past the interpreter's
		// own saves — on the wrong canvas at that. Close the span the way EndMask would instead and apply nothing, the
		// mirror of EndMask's guard against a repeated call. The interpreter always pairs Begin/End/Pop, so this is
		// defense in depth.
		d.closeMaskSpan(ms)
		return
	}
	// A constant zero-coverage mask needs no DstIn at all: EndMask already realized it as an empty layer.
	if ms.mask != nil || (ms.constant && ms.outside != 0) {
		paint := canvas.NewPaint() // Opaque color: the plane's alpha is the DstIn source.
		paint.BlendMode = raster.BlendDstIn
		paint.AntiAlias = false
		// The plane is in device pixels (see BeginMask), so gate the layer with the canvas at identity rather than under
		// whatever matrix a Wrapped device's caller left in place.
		count := d.c.Save()
		d.c.ResetMatrix()
		if ms.mask != nil {
			sampling := shaders.SamplingOptions{Filter: shaders.FilterNearest}
			src := geom.RectWH(float32(ms.w), float32(ms.h))
			d.c.DrawImageRect(ms.mask, src, ms.rect(), sampling, paint, canvas.ConstraintFast)
		}
		if !ms.bounded {
			d.maskOutside(ms, paint)
		}
		d.c.RestoreToCount(count)
	}
	d.c.RestoreToCount(ms.layer)
}

// maskOutside applies the mask's constant outside coverage to the part of the canvas the coverage plane does not cover
// — the four bands around it, or the whole canvas when there is no plane. Called with the canvas at identity and paint
// already set to DstIn. Nothing is drawn where the value would leave the layer untouched anyway (a full-coverage
// outside value) or where the plane already covers everything.
func (d *Device) maskOutside(ms *maskState, paint *canvas.Paint) {
	if ms.outside == 255 { // Full coverage outside: the layer passes through there unchanged.
		return
	}
	x0, y0, x1, y1 := 0, 0, 0, 0 // With no plane the empty rectangle leaves the bands covering the whole canvas.
	if ms.mask != nil {
		x0, y0, x1, y1 = ms.x0, ms.y0, ms.x0+ms.w, ms.y0+ms.h
		if x0 <= 0 && y0 <= 0 && x1 >= d.width && y1 >= d.height {
			return
		}
	}
	paint.Color = colorcore.ARGB(ms.outside, 255, 255, 255)
	w, h := float32(d.width), float32(d.height)
	for _, r := range [4]geom.Rect{
		geom.RectLTRB(0, 0, w, float32(y0)),                     // above
		geom.RectLTRB(0, float32(y1), w, h),                     // below
		geom.RectLTRB(0, float32(y0), float32(x0), float32(y1)), // left
		geom.RectLTRB(float32(x1), float32(y0), w, float32(y1)), // right
	} {
		if !r.IsEmpty() {
			d.c.DrawRect(r, paint)
		}
	}
}

// maskPlane reduces the mask surface to an Alpha8 coverage image.
func (d *Device) maskPlane(ms *maskState) *imagecore.Image {
	img := ms.surf.MakeImageSnapshot()
	if img == nil {
		return nil
	}
	stride := ms.w * 4
	pix := make([]byte, stride*ms.h)
	info := imagecore.ImageInfo{
		Width:     int32(ms.w),
		Height:    int32(ms.h),
		ColorType: imagecore.ColorTypeRGBA8888,
		AlphaType: imagecore.AlphaTypePremul,
	}
	if !img.ReadPixels(info, pix, stride, 0, 0, imagecore.CachingDisallow) {
		return nil
	}
	plane := make([]byte, ms.w*ms.h)
	if ms.luminosity {
		// The luminosity surface is opaque (prefilled with the backdrop), so the premultiplied bytes are the straight
		// color values.
		for i, j := 0, 0; j < len(plane); i, j = i+4, j+1 {
			plane[j] = maskLuma(pix[i], pix[i+1], pix[i+2])
		}
	} else {
		for i, j := 3, 0; j < len(plane); i, j = i+4, j+1 {
			plane[j] = pix[i]
		}
	}
	// Only a full 256-entry LUT can be indexed by an arbitrary 0–255 mask value; anything shorter is unusable, so treat
	// it as identity rather than trusting the caller's slice length (parseTransfer returns nil or exactly 256).
	if len(ms.transfer) == 256 {
		for j, v := range plane {
			plane[j] = ms.transfer[v]
		}
	}
	ainfo := imagecore.ImageInfo{
		Width:     int32(ms.w),
		Height:    int32(ms.h),
		ColorType: imagecore.ColorTypeAlpha8,
		AlphaType: imagecore.AlphaTypePremul,
	}
	return imagecore.NewRasterData(ainfo, plane, ms.w)
}

// maskLuma converts one RGB color to MuPDF's luminosity-mask value. The oracle's conversion (lcms-backed, like the
// device-color tables in internal/color) was captured behaviorally from per-channel and neutral ramps: the response is
// a weighted sum of the ENCODED channel values — weights 78/159/15 out of 252, measured at the ramp tops — followed by
// the captured neutral response curve. Neutral inputs reproduce the oracle exactly by construction; primaries and
// mixtures land within ±2 of the oracle across the 800-sample probe set (the oracle itself converts DeviceGray-sourced
// mask content through a slightly different gray→gray ICC path, also within ±2). Entry 255 is pinned to 255 rather than
// the measured RGB-path 254 so fully lit mask areas pass content through unchanged (the oracle's gray-path white does
// the same).
func maskLuma(r, g, b uint8) uint8 {
	t := (78*uint32(r) + 159*uint32(g) + 15*uint32(b) + 126) / 252
	return maskNeutralLUT[t]
}

// maskNeutralLUT is the captured neutral-ramp response of the oracle's RGB→luminosity conversion (256 RGB neutral fills
// through a luminosity soft mask over solid content, dpi 72; see maskLuma).
var maskNeutralLUT = [256]uint8{
	0, 0, 1, 2, 2, 3, 4, 6, 7, 8, 9, 9, 10, 11, 13, 14,
	15, 16, 17, 17, 18, 20, 21, 22, 23, 24, 24, 25, 27, 28, 29, 30,
	31, 31, 32, 33, 35, 36, 37, 38, 39, 39, 41, 42, 43, 44, 45, 46,
	46, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62,
	62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77,
	78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93,
	94, 95, 96, 97, 98, 99, 100, 101, 102, 103, 104, 105, 106, 107, 108, 109,
	110, 111, 112, 113, 114, 115, 116, 117, 118, 119, 120, 121, 122, 123, 124, 125,
	126, 128, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140, 141, 142,
	143, 144, 145, 146, 147, 148, 149, 150, 151, 152, 153, 154, 155, 156, 157, 158,
	159, 160, 161, 162, 163, 164, 165, 166, 167, 168, 169, 170, 171, 172, 173, 174,
	175, 176, 177, 178, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190,
	191, 192, 193, 194, 195, 196, 197, 198, 199, 200, 201, 202, 203, 204, 205, 206,
	207, 208, 209, 210, 211, 212, 213, 214, 215, 216, 217, 218, 219, 220, 221, 222,
	223, 224, 225, 226, 227, 228, 229, 230, 231, 232, 233, 234, 235, 236, 237, 238,
	239, 240, 241, 242, 243, 244, 245, 246, 247, 248, 249, 250, 251, 252, 253, 255,
}
