// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview

import (
	"github.com/richardwilkes/canvas/canvas"
	"github.com/richardwilkes/canvas/geom"

	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/render"
)

// DrawPage renders the given 0-based page's content — including its annotation appearance streams, exactly like
// RenderPage — onto the caller's canvas through the same content-stream interpreter and raster device the image APIs
// use. It is the one canvas-coupled entry point of this package: unlike the rest of the API, it exposes
// github.com/richardwilkes/canvas types, and callers who use it are deliberately coupling themselves to that module.
//
// ctm maps the page's top-left, y-down page space — PDF points, the space RenderPage rasterizes at scale 1 — onto the
// canvas. The identity matrix draws the page at 72 dpi with its top-left corner at the canvas origin;
// geom.ScaleMatrix(dpi/72, dpi/72) reproduces RenderPage's layout at that dpi. Only the affine components of ctm are
// used (PDF content is affine; any perspective entries are ignored).
//
// The caller's surface lifecycle is untouched: DrawPage only issues draw calls, never reads pixels, snapshots, or
// flushes, and the canvas's save/clip/matrix state is restored before returning — even when hostile content panics the
// interpreter, which surfaces as ErrInternal per the package's robustness contract. Content drawn before such a panic
// may already be on the canvas.
//
// DrawPage serializes with all other methods on the Document (one call into the engine at a time), but the canvas
// itself is not otherwise protected: the caller must not use it concurrently from other goroutines.
func (d *Document) DrawPage(c *canvas.Canvas, pageNumber int, ctm geom.Matrix) error {
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.released() {
		return ErrDocumentReleased
	}
	if pageNumber < 0 || pageNumber >= d.eng.pageCount() {
		return ErrInvalidPageNumber
	}
	pg, err := d.eng.loadPage(pageNumber)
	if err != nil {
		return err
	}
	return d.eng.drawPage(c, pg, ctm)
}

// drawPage wraps the caller's canvas in a raster device and runs the page through the interpreter under the composed
// page-space→canvas matrix. The canvas state is restored to its entry depth on every path out, including a
// hostile-content panic, which maps to ErrInternal.
func (e *engineDocument) drawPage(c *canvas.Canvas, pg *page, ctm geom.Matrix) (err error) {
	dev, derr := render.Wrap(c)
	if derr != nil {
		return ErrUnableToCreateImage
	}
	base, cerr := e.doc.PageCTM(pg.number, 1)
	if cerr != nil {
		return ErrUnableToLoadPage
	}
	save := c.Save()
	defer func() {
		if recover() != nil {
			err = ErrInternal
		}
		c.RestoreToCount(save)
	}()
	dev.SetStore(e.store)
	e.runPage(pg, base.Mul(gfxFromGeom(ctm)), dev)
	return nil
}

// gfxFromGeom extracts the affine part of a canvas geom.Matrix (Skia layout: scaleX, skewX, transX, skewY, scaleY,
// transY, perspective row) into the engine's PDF-style row-vector matrix. This is the exact inverse of
// internal/render's gfx→geom conversion; perspective entries have no PDF counterpart and are dropped.
func gfxFromGeom(m geom.Matrix) gfx.Matrix {
	a9 := m.As9()
	return gfx.Matrix{A: a9[0], B: a9[3], C: a9[1], D: a9[4], E: a9[2], F: a9[5]}
}
