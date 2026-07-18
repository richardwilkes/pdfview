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

// Transparency groups and soft masks. A group maps to SaveLayer with the group's constant alpha and
// blend mode on the restore paint; a NON-isolated group whose composite is trivial (alpha 1, blend Normal,
// no knockout) is passed through without a layer, which reproduces non-isolated semantics exactly — interior
// blend modes then see the true backdrop, as MuPDF does (oracle-pinned by the isolation probe). A
// non-isolated group with a non-trivial composite still gets a layer, an accepted isolated approximation.
//
// Soft masks render their content to a separate offscreen surface: BeginMask swaps the canvas, EndMask reads
// the surface back, reduces it to an 8-bit coverage plane (rendered alpha for /S /Alpha; the captured MuPDF
// luminosity response for /S /Luminosity — see maskLuma), applies the /TR LUT, and opens the masked-content
// layer on the main canvas; PopMask multiplies the layer by the plane (BlendDstIn over the full canvas) and
// restores it.

// groupState is one BeginGroup's record.
type groupState struct {
	count    int // canvas save count to restore (valid when layered)
	layered  bool
	knockout bool
}

// maskState is one BeginMask..PopMask span's record.
type maskState struct {
	surf       *surface.Surface // nil when surface creation failed (the guard layer swallows mask content)
	saved      *canvas.Canvas
	savedClip  *path.Path       // the interrupted text-clip accumulation, restored at EndMask
	mask       *imagecore.Image // the Alpha8 coverage plane, built at EndMask
	transfer   []byte
	layer      int // masked-content layer save count (valid after EndMask)
	guard      int // guard layer save count (surf == nil only)
	luminosity bool
}

// BeginGroup implements device.Device.
func (d *Device) BeginGroup(_ gfx.Rect, isolated, knockout bool, blend device.Blend, alpha float64) {
	trivial := alpha >= 1 && blend == device.BlendNormal && !knockout
	if !isolated && trivial {
		// Non-isolated with nothing to composite: drawing inline IS the group semantics (interior blends
		// composite against the true backdrop).
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

// knockoutSrc reports whether solid draws must composite with BlendSrc: inside a knockout group, each object
// replaces what earlier objects painted where it covers (ISO 32000-2 11.4.5; with the group's layer starting
// transparent, compositing the object against the initial backdrop then replacing is exactly BlendSrc under
// coverage). Suppressed while a soft-mask span is open — mask content composites normally, and a masked op
// lives in its own layer where Src has nothing to knock out.
func (d *Device) knockoutSrc() bool {
	return len(d.maskStack) == 0 && len(d.groupStack) > 0 && d.groupStack[len(d.groupStack)-1].knockout
}

// applyKnockout rewrites a solid paint's blend for knockout-group interiors.
func (d *Device) applyKnockout(paint *canvas.Paint) {
	if d.knockoutSrc() {
		paint.BlendMode = raster.BlendSrc
	}
}

// BeginMask implements device.Device.
func (d *Device) BeginMask(_ gfx.Rect, luminosity bool, backdrop stdcolor.NRGBA, transfer []byte) {
	ms := &maskState{luminosity: luminosity, transfer: transfer, saved: d.c, savedClip: d.textClip}
	d.textClip = nil
	ms.surf = surface.NewRasterN32Premul(int32(d.width), int32(d.height), nil)
	if ms.surf == nil {
		// No mask surface: isolate the mask content in an invisible layer so it cannot mark the page; the
		// masked op then draws unmasked (degrade, never erase).
		ms.guard = d.c.SaveLayerAlpha(nil, 0)
	} else {
		d.c = ms.surf.Canvas()
		if luminosity {
			// The mask group composites over the /BC backdrop before luminance extraction; prefilling the
			// whole surface also gives areas outside the group's BBox the backdrop's luminosity.
			p := canvas.NewPaint()
			p.Color = colorcore.ARGB(255, backdrop.R, backdrop.G, backdrop.B)
			d.c.DrawPaint(p)
		}
	}
	d.maskStack = append(d.maskStack, ms)
}

// EndMask implements device.Device.
func (d *Device) EndMask() {
	n := len(d.maskStack)
	if n == 0 {
		return
	}
	ms := d.maskStack[n-1]
	d.textClip = ms.savedClip
	if ms.surf == nil {
		d.c.RestoreToCount(ms.guard)
	} else {
		ms.mask = d.maskPlane(ms)
		d.c = ms.saved
	}
	ms.layer = d.c.SaveLayer(nil, nil)
}

// PopMask implements device.Device.
func (d *Device) PopMask() {
	n := len(d.maskStack)
	if n == 0 {
		return
	}
	ms := d.maskStack[n-1]
	d.maskStack = d.maskStack[:n-1]
	if ms.mask != nil {
		paint := canvas.NewPaint() // Opaque color: the plane's alpha is the DstIn source.
		paint.BlendMode = raster.BlendDstIn
		paint.AntiAlias = false
		full := geom.RectWH(float32(d.width), float32(d.height))
		sampling := shaders.SamplingOptions{Filter: shaders.FilterNearest}
		d.c.DrawImageRect(ms.mask, full, full, sampling, paint, canvas.ConstraintFast)
	}
	d.c.RestoreToCount(ms.layer)
}

// maskPlane reduces the mask surface to an Alpha8 coverage image.
func (d *Device) maskPlane(ms *maskState) *imagecore.Image {
	img := ms.surf.MakeImageSnapshot()
	if img == nil {
		return nil
	}
	stride := d.width * 4
	pix := make([]byte, stride*d.height)
	info := imagecore.ImageInfo{
		Width:     int32(d.width),
		Height:    int32(d.height),
		ColorType: imagecore.ColorTypeRGBA8888,
		AlphaType: imagecore.AlphaTypePremul,
	}
	if !img.ReadPixels(info, pix, stride, 0, 0, imagecore.CachingDisallow) {
		return nil
	}
	plane := make([]byte, d.width*d.height)
	if ms.luminosity {
		// The luminosity surface is opaque (prefilled with the backdrop), so the premultiplied bytes are the
		// straight color values.
		for i, j := 0, 0; j < len(plane); i, j = i+4, j+1 {
			plane[j] = maskLuma(pix[i], pix[i+1], pix[i+2])
		}
	} else {
		for i, j := 3, 0; j < len(plane); i, j = i+4, j+1 {
			plane[j] = pix[i]
		}
	}
	if ms.transfer != nil {
		for j, v := range plane {
			plane[j] = ms.transfer[v]
		}
	}
	ainfo := imagecore.ImageInfo{
		Width:     int32(d.width),
		Height:    int32(d.height),
		ColorType: imagecore.ColorTypeAlpha8,
		AlphaType: imagecore.AlphaTypePremul,
	}
	return imagecore.NewRasterData(ainfo, plane, d.width)
}

// maskLuma converts one RGB color to MuPDF's luminosity-mask value. The oracle's conversion (lcms-backed,
// like the device-color tables in internal/color) was captured behaviorally from per-channel and neutral
// ramps: the response is a weighted sum of the ENCODED channel values — weights 78/159/15 out of 252,
// measured at the ramp tops — followed by the captured neutral response curve. Neutral inputs reproduce the
// oracle exactly by construction; primaries and mixtures land within ±2 of the oracle across the 800-sample
// probe set (the oracle itself converts DeviceGray-sourced mask content through a slightly different
// gray→gray ICC path, also within ±2). Entry 255 is pinned to 255 rather than the measured RGB-path 254 so
// fully lit mask areas pass content through unchanged (the oracle's gray-path white does the same).
func maskLuma(r, g, b uint8) uint8 {
	t := (78*uint32(r) + 159*uint32(g) + 15*uint32(b) + 126) / 252
	return maskNeutralLUT[t]
}

// maskNeutralLUT is the captured neutral-ramp response of the oracle's RGB→luminosity conversion (256 RGB
// neutral fills through a luminosity soft mask over solid content, dpi 72; see maskLuma).
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
