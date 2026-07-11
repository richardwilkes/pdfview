// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package device

import (
	"image/color"

	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/imaging"
	"github.com/richardwilkes/pdfview/internal/shading"
)

// Tee returns a Device that forwards every operation to each of devices in order, so one interpreter pass can
// drive several consumers (the raster and structured-text devices) at once. With zero or one argument it
// avoids the indirection: zero devices yields Null and one is returned as-is.
func Tee(devices ...Device) Device {
	switch len(devices) {
	case 0:
		return Null{}
	case 1:
		return devices[0]
	default:
		return tee(devices)
	}
}

type tee []Device

func (t tee) FillPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix, paint Paint) {
	for _, d := range t {
		d.FillPath(p, evenOdd, ctm, paint)
	}
}

func (t tee) StrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix, paint Paint) {
	for _, d := range t {
		d.StrokePath(p, sp, ctm, paint)
	}
}

func (t tee) ClipPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix) {
	for _, d := range t {
		d.ClipPath(p, evenOdd, ctm)
	}
}

func (t tee) ClipStrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix) {
	for _, d := range t {
		d.ClipStrokePath(p, sp, ctm)
	}
}

func (t tee) FillText(run *TextRun, paint Paint) {
	for _, d := range t {
		d.FillText(run, paint)
	}
}

func (t tee) StrokeText(run *TextRun, sp *gfx.StrokeParams, paint Paint) {
	for _, d := range t {
		d.StrokeText(run, sp, paint)
	}
}

func (t tee) ClipText(run *TextRun) {
	for _, d := range t {
		d.ClipText(run)
	}
}

func (t tee) EndTextClip() {
	for _, d := range t {
		d.EndTextClip()
	}
}

func (t tee) IgnoreText(run *TextRun) {
	for _, d := range t {
		d.IgnoreText(run)
	}
}

func (t tee) FillImage(img *imaging.Image, ctm gfx.Matrix, alpha float64) {
	for _, d := range t {
		d.FillImage(img, ctm, alpha)
	}
}

func (t tee) FillImageMask(img *imaging.Image, ctm gfx.Matrix, paint Paint) {
	for _, d := range t {
		d.FillImageMask(img, ctm, paint)
	}
}

func (t tee) ClipImageMask(img *imaging.Image, ctm gfx.Matrix) {
	for _, d := range t {
		d.ClipImageMask(img, ctm)
	}
}

func (t tee) PopClip() {
	for _, d := range t {
		d.PopClip()
	}
}

func (t tee) BeginGroup(bbox gfx.Rect, isolated, knockout bool, blend Blend, alpha float64) {
	for _, d := range t {
		d.BeginGroup(bbox, isolated, knockout, blend, alpha)
	}
}

func (t tee) EndGroup() {
	for _, d := range t {
		d.EndGroup()
	}
}

func (t tee) BeginMask(bbox gfx.Rect, luminosity bool, backdrop color.NRGBA) {
	for _, d := range t {
		d.BeginMask(bbox, luminosity, backdrop)
	}
}

func (t tee) EndMask() {
	for _, d := range t {
		d.EndMask()
	}
}

func (t tee) PopMask() {
	for _, d := range t {
		d.PopMask()
	}
}

func (t tee) FillShading(sh *shading.Shading, ctm gfx.Matrix, paint Paint) {
	for _, d := range t {
		d.FillShading(sh, ctm, paint)
	}
}
