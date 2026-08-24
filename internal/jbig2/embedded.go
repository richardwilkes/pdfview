// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// This file is pdfview-authored (MPL-2.0), not upstream code: it holds the ISO 32000 embedded-profile entry point and
// the cumulative allocation cap that entry point enforces through the rest of the package. See PROVENANCE.md.

package jbig2

import (
	"errors"
	"io"
)

// Limits bounds what a single decode may allocate.
type Limits struct {
	// MaxPixels caps the cumulative area, in pixels, of every bitmap one decode allocates: the page and its striped
	// growth, each region, each new symbol bitmap, each pattern cell, and each halftone gray-code plane. It bounds
	// decode work as well as memory: a symbol's area is charged before the generic-region decode that fills it, and
	// symbol dimensions are arithmetic-decoded deltas no scan of the payload can see. Zero means no cap.
	MaxPixels int64
}

// ErrLimitExceeded reports a decode that asked for more bitmap area than Limits.MaxPixels allows. It aborts the whole
// decode rather than truncating it: a caller that hits this has no partially correct page to keep.
var ErrLimitExceeded = errors.New("jbig2: decode exceeds the configured pixel limit")

// errEmptyPayload reports a payload with no bytes at all, which cannot carry even one segment header.
var errEmptyPayload = errors.New("jbig2: empty payload")

// budget is the running remainder of Limits.MaxPixels. A nil *budget is the uncapped case, so the charge sites carry
// no branch of their own.
type budget struct {
	remaining int64
	// exceeded records that a charge was refused. The decode paths report failures as a bare Result code, so this is
	// what lets the entry point return ErrLimitExceeded rather than a generic failure.
	exceeded bool
}

// maxChargeableDim bounds each dimension a charge may name, which keeps the area product inside int64 no matter what
// the stream claims. It is far past JBig2MaxImageSize, so it rejects nothing a decode could legitimately allocate.
const maxChargeableDim = 1 << 31

// newBudget returns the budget for a pixel cap, or nil when the cap is absent.
func newBudget(maxPixels int64) *budget {
	if maxPixels <= 0 {
		return nil
	}
	return &budget{remaining: maxPixels}
}

// charge deducts a w by h bitmap's area from the budget, reporting ErrLimitExceeded when it does not fit. Callers
// charge before allocating; a non-positive dimension allocates nothing and costs nothing.
func (b *budget) charge(w, h int64) error {
	if b == nil {
		return nil
	}
	if w <= 0 || h <= 0 {
		return nil
	}
	if w <= maxChargeableDim && h <= maxChargeableDim {
		if area := w * h; area <= b.remaining {
			b.remaining -= area
			return nil
		}
	}
	b.exceeded = true
	return ErrLimitExceeded
}

// exceededLimit reports whether any charge against this budget was refused.
func (b *budget) exceededLimit() bool {
	return b != nil && b.exceeded
}

// NewEmbeddedDecoder builds a decoder over a PDF-embedded JBIG2 stream: the ISO 32000-2 7.4.7 profile, which is T.88's
// sequential organization with no file header, every segment implicitly on page 1, and the /JBIG2Globals bytes parsed
// first into a shared segment context. No other container is probed for, and the globals are parsed here, so a decoder
// that constructs is one whose globals were usable.
func NewEmbeddedDecoder(payload, globals []byte, limits Limits) (*Decoder, error) {
	if len(payload) == 0 {
		return nil, errEmptyPayload
	}
	doc := NewDocument(payload, globals)
	doc.budget = newBudget(limits.MaxPixels)
	if doc.globalContext != nil {
		// One budget covers both streams: the globals' symbol dictionaries are allocations of this decode too.
		doc.globalContext.budget = doc.budget
	}
	if err := doc.parseGlobalSegments(); err != nil {
		if doc.budget.exceededLimit() {
			return nil, ErrLimitExceeded
		}
		return nil, err
	}
	return &Decoder{doc: doc}, nil
}

// DecodePage decodes the page the payload carries: one bit per pixel, MSB first, Stride() bytes per row, and a set bit
// meaning black — T.88's polarity, the inverse of the PDF image convention. Everything in the embedded profile
// belongs to page 1, so the first call returns that page and every later call reports io.EOF.
func (d *Decoder) DecodePage() (*Image, error) {
	if d.doc == nil {
		return nil, errors.New("jbig2: decoder not initialized")
	}
	for {
		switch d.doc.DecodeSequential() {
		case ResultEndReached:
			if d.doc.inPage && d.doc.page != nil {
				d.doc.inPage = false
				return d.takePage(), nil
			}
			return nil, io.EOF
		case ResultPageCompleted:
			if d.doc.page == nil {
				return nil, errors.New("jbig2: page completed with no bitmap")
			}
			return d.takePage(), nil
		case ResultFailure:
			if d.doc.budget.exceededLimit() {
				return nil, ErrLimitExceeded
			}
			return nil, errors.New("jbig2: decoding failed")
		}
	}
}

// takePage hands back the decoded page and releases the segment results that fed it, which are the bulk of what a
// symbol-heavy decode holds.
func (d *Decoder) takePage() *Image {
	page := d.doc.page
	d.pageIndex++
	d.doc.ReleasePageSegments(d.pageIndex)
	return page
}
