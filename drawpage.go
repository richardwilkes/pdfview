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
// Drawing is confined to the page box (the effective MediaBox∩CropBox, after rotation) mapped through ctm, so content
// a page paints outside its own box — bleed, printer's marks, or simply a stream that runs past the box — never
// reaches the rest of the caller's canvas. RenderPage bounds the same content implicitly, by rasterizing into a
// page-sized surface; that surface rounds its extent up to whole pixels, so edge-touching content can differ from this
// clip by a fraction of a pixel at the page boundary.
//
// ctm must be finite, and its composition with the page CTM must not overflow; a matrix that fails either test is
// rejected with ErrInvalidMatrix before anything is drawn.
//
// The caller's surface lifecycle is untouched: DrawPage only issues draw calls, never reads pixels, snapshots, or
// flushes, and the canvas's save/clip/matrix state is restored before returning — even when hostile content panics the
// interpreter, which surfaces as ErrInternal per the package's robustness contract. Content drawn before such a panic
// may already be on the canvas.
//
// DrawPage serializes with all other methods on the Document (one call into the engine at a time), but the canvas
// itself is not otherwise protected: the caller must not use it concurrently from other goroutines.
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
// page-space→canvas matrix. The canvas state is restored to its entry depth on every path out, including a panic —
// whether raised by hostile content or by the caller's own canvas during setup — which maps to ErrInternal.
func (e *engineDocument) drawPage(c *canvas.Canvas, pg *page, ctm geom.Matrix) (err error) {
	// The guard is the first statement so that the setup calls below are covered too: render.Wrap, PageCTM and the
	// matrix composition all touch the caller's canvas or hostile document geometry, and DrawPage's contract promises
	// every failure — including a panic out of a canvas whose surface the caller already released — surfaces as an
	// error rather than escaping. save stays negative until the state is actually pushed, since RestoreToCount(0) on a
	// canvas the caller had saved at a deeper level would unwind state this call never pushed.
	save := -1
	defer func() {
		if recover() != nil {
			err = ErrInternal
		}
		// The restore carries its own guard rather than running bare after the recover above: RestoreToCount pops
		// device clip stacks and composites any open SaveLayer — exactly the state a panic mid-transparency-group
		// leaves behind — so the restore that matters most is also the one most able to panic, and a panic raised
		// there would otherwise escape DrawPage instead of becoming ErrInternal.
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
	// Every other entry point derives its CTM from validated geometry, and the interpreter relies on that: cm and a
	// form's /Matrix only check the product they compute, assuming the incoming CTM is already finite. The caller's
	// matrix is the one unvalidated source, so reject it here rather than letting a NaN/Inf reach the device.
	full := base.Mul(render.FromGeom(ctm))
	if !full.IsFinite() {
		return ErrInvalidMatrix
	}
	save = c.Save()
	// RenderPage bounds a page's content by construction — it rasterizes into a page-sized surface, so nothing outside
	// the page box survives. The caller's canvas carries no such bound, so push the page box explicitly; without it a
	// page whose stream paints past its own box repaints whatever else the caller has on the canvas, and the
	// documented equivalence with RenderPage's layout holds only for a canvas no larger than the page. pg.width and
	// pg.height are the box's extent in the same top-left, y-down page space ctm maps from.
	dev.ClipPageBox(pg.width, pg.height, render.FromGeom(ctm))
	dev.SetStore(e.store)
	e.runPage(pg, full, dev)
	return nil
}

// restoreToCount pops the canvas back to the given save depth, reporting whether it completed. It is a function of its
// own so the restore runs under a recover that is not the one drawPage's deferred cleanup already consumed; see the
// comment at that call.
func restoreToCount(c *canvas.Canvas, save int) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	c.RestoreToCount(save)
	return true
}
