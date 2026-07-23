// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package function

import (
	"errors"

	"github.com/richardwilkes/pdfview/internal/cos"
)

var errBadStitching = errors.New("invalid stitching function")

// stitching is a type 3 (stitching) function (ISO 32000-2 7.10.5): a one-input domain partitioned by Bounds into k
// subdomains, each encoded into a subfunction.
type stitching struct {
	common
	funcs  []Func
	bounds []float32
	encode []float32
}

func parseStitching(d *cos.Document, dict cos.Dict, c common, depth int) (Func, error) {
	if c.NInputs() != 1 {
		return nil, errBadStitching
	}
	s := &stitching{common: c}
	arr, ok := d.GetArray(dict, "Functions")
	if !ok || len(arr) == 0 || len(arr) > maxProgramOps {
		return nil, errBadStitching
	}
	nOut := -1
	for _, entry := range arr {
		sub, err := parse(d, entry, depth+1)
		if err != nil {
			return nil, err
		}
		if sub.NInputs() != 1 {
			return nil, errBadStitching
		}
		if nOut < 0 {
			nOut = sub.NOutputs()
		} else if sub.NOutputs() != nOut {
			return nil, errBadStitching
		}
		s.funcs = append(s.funcs, sub)
	}
	if s.bounds, ok = numbers(d, dict, "Bounds", len(arr)-1); (!ok || len(s.bounds) != len(arr)-1) && len(arr) > 1 {
		return nil, errBadStitching
	}
	// ISO 32000-2 7.10.5 requires Bounds to be in nondecreasing order within Domain:
	// Domain[0] <= Bounds[0] <= ... <= Bounds[k-2] <= Domain[1]. Rejecting malformed bounds keeps Eval's subdomain
	// scan from selecting the wrong subfunction and interpolating over an inverted [lo, hi].
	prev := c.domain[0]
	for _, b := range s.bounds {
		if b < prev || b > c.domain[1] {
			return nil, errBadStitching
		}
		prev = b
	}
	if s.encode, ok = numbers(d, dict, "Encode", 2*len(arr)); !ok || len(s.encode) != 2*len(arr) {
		return nil, errBadStitching
	}
	return s, nil
}

func (s *stitching) NOutputs() int {
	if len(s.rng) != 0 {
		return len(s.rng) / 2
	}
	return s.funcs[0].NOutputs()
}

func (s *stitching) Eval(in []float32) []float32 {
	x := s.clampIn(in)[0]
	// Select the subdomain: subfunction i covers [bounds[i-1], bounds[i]), with the domain edges at the ends and the
	// final subdomain closed on the right (ISO 32000-2 7.10.5).
	i := 0
	for i < len(s.bounds) && x >= s.bounds[i] {
		i++
	}
	lo := s.domain[0]
	if i > 0 {
		lo = s.bounds[i-1]
	}
	hi := s.domain[1]
	if i < len(s.bounds) {
		hi = s.bounds[i]
	}
	e := interpolate(x, lo, hi, s.encode[2*i], s.encode[2*i+1])
	return s.clampOut(s.funcs[i].Eval([]float32{e}))
}
