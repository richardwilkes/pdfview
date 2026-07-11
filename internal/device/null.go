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

// Null is a Device that ignores every operation. It exists so the interpreter can be driven without a
// consumer — most importantly by FuzzContent, which exercises the interpreter's robustness against hostile
// content streams without paying for rasterization.
type Null struct{}

// FillPath implements Device.
func (Null) FillPath(*gfx.Path, bool, gfx.Matrix, Paint) {}

// StrokePath implements Device.
func (Null) StrokePath(*gfx.Path, *gfx.StrokeParams, gfx.Matrix, Paint) {}

// ClipPath implements Device.
func (Null) ClipPath(*gfx.Path, bool, gfx.Matrix) {}

// ClipStrokePath implements Device.
func (Null) ClipStrokePath(*gfx.Path, *gfx.StrokeParams, gfx.Matrix) {}

// FillText implements Device.
func (Null) FillText(*TextRun, Paint) {}

// StrokeText implements Device.
func (Null) StrokeText(*TextRun, *gfx.StrokeParams, Paint) {}

// ClipText implements Device.
func (Null) ClipText(*TextRun) {}

// EndTextClip implements Device.
func (Null) EndTextClip() {}

// IgnoreText implements Device.
func (Null) IgnoreText(*TextRun) {}

// FillImage implements Device.
func (Null) FillImage(*imaging.Image, gfx.Matrix, float64) {}

// FillImageMask implements Device.
func (Null) FillImageMask(*imaging.Image, gfx.Matrix, Paint) {}

// ClipImageMask implements Device.
func (Null) ClipImageMask(*imaging.Image, gfx.Matrix) {}

// PopClip implements Device.
func (Null) PopClip() {}

// BeginGroup implements Device.
func (Null) BeginGroup(gfx.Rect, bool, bool, Blend, float64) {}

// EndGroup implements Device.
func (Null) EndGroup() {}

// BeginMask implements Device.
func (Null) BeginMask(gfx.Rect, bool, color.NRGBA) {}

// EndMask implements Device.
func (Null) EndMask() {}

// PopMask implements Device.
func (Null) PopMask() {}

// FillShading implements Device.
func (Null) FillShading(*shading.Shading, gfx.Matrix, float64) {}
