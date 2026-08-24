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

	"github.com/richardwilkes/pdfview/internal/render"
)

// DrawPage renders the given 0-based page's content, annotation appearance streams included, onto the caller's canvas
// through the same interpreter and raster device RenderPage uses. It is the package's one canvas-coupled entry point:
// it exposes github.com/richardwilkes/canvas types, and callers who use it couple themselves to that module.
//
// ctm maps the page's top-left, y-down page space (PDF points, the space RenderPage rasterizes at scale 1) onto the
// canvas. The identity matrix draws the page at 72 dpi with its top-left corner at the canvas origin;
// geom.ScaleMatrix(dpi/72, dpi/72) reproduces RenderPage's layout at that dpi. Only the affine components of ctm are
// used. ctm must be finite and its composition with the page CTM must not overflow; otherwise ErrInvalidMatrix is
// returned before anything is drawn.
//
// Drawing is clipped to the page box (the effective MediaBox∩CropBox, after rotation) mapped through ctm, so content
// a page paints outside its own box never reaches the rest of the caller's canvas. RenderPage bounds the same content
// by rasterizing into a page-sized surface whose extent is rounded up to whole pixels, so edge-touching content can
// differ from this clip by a fraction of a pixel at the page boundary.
//
// DrawPage only issues draw calls: it never reads pixels, snapshots, or flushes, and it restores the canvas's
// save/clip/matrix state before returning, even when hostile content panics the interpreter (reported as ErrInternal).
// Content drawn before such a panic may already be on the canvas.
//
// DrawPage serializes with all other methods on the Document, but the canvas itself is not protected: the caller must
// not use it concurrently from other goroutines.
func (d *Document) DrawPage(c *canvas.Canvas, pageNumber int, ctm geom.Matrix) error {
	if !d.usable() {
		return ErrDocumentReleased
	}
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
// page-space→canvas matrix. The canvas state is restored to its entry depth on every path out, including a panic,
// which maps to ErrInternal.
func (e *engineDocument) drawPage(c *canvas.Canvas, pg *page, ctm geom.Matrix) (err error) {
	// The guard comes first so the setup calls below are covered too: render.Wrap, PageCTM, and the matrix composition
	// all touch the caller's canvas or hostile document geometry. save stays negative until the state is actually
	// pushed; RestoreToCount(0) on a canvas the caller had saved deeper would unwind state this call never pushed.
	save := -1
	defer func() {
		if recover() != nil {
			err = ErrInternal
		}
		// The recover above is already spent, so a panic in RestoreToCount would escape DrawPage. It can panic: it pops
		// device clip stacks and composites any open SaveLayer, exactly what a panic mid-transparency-group leaves
		// behind. restoreToCount runs it under a recover of its own.
		if save >= 0 && !restoreToCount(c, save) {
			err = ErrInternal
		}
	}()
	dev, derr := render.Wrap(c)
	if derr != nil {
		return ErrUnableToCreateImage
	}
	base, cerr := e.doc.PageCTM(pg.number, 1)
	if cerr != nil {
		return ErrUnableToLoadPage
	}
	// The interpreter assumes the incoming CTM is finite: cm and a form's /Matrix only check the product they compute.
	// Every other entry point derives its CTM from validated geometry; the caller's matrix is the one unvalidated
	// source.
	full := base.Mul(render.FromGeom(ctm))
	if !full.IsFinite() {
		return ErrInvalidMatrix
	}
	save = c.Save()
	// RenderPage bounds content by rasterizing into a page-sized surface. The caller's canvas has no such bound, so push
	// the page box explicitly; without it a stream that paints past its box repaints whatever else is on the canvas.
	// pg.width and pg.height are the box's extent in the top-left, y-down page space ctm maps from.
	dev.ClipPageBox(pg.width, pg.height, render.FromGeom(ctm))
	dev.SetStore(e.store)
	dev.SetStemDarkening(e.stemDarkening)
	e.runPage(pg, full, dev)
	return nil
}

// restoreToCount pops the canvas back to the given save depth, reporting whether it completed. It is a separate
// function so the restore runs under a recover that drawPage's deferred cleanup has not already consumed.
func restoreToCount(c *canvas.Canvas, save int) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	c.RestoreToCount(save)
	return true
}
