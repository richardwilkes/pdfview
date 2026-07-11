// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package function implements PDF functions (ISO 32000-2 7.10): sampled (type 0), exponential interpolation
// (type 2), stitching (type 3), and PostScript calculator (type 4) functions. Functions map m clamped inputs
// to n clamped outputs; they parameterize Separation/DeviceN tint transforms (internal/color), shadings
// (internal/shading, M8), and transfer functions.
//
// Everything is bounded so hostile input cannot force unbounded work (see plan.md "Resource limits &
// robustness"): input dimensionality, stitching/array nesting, calculator program size, and calculator
// execution steps are all capped by the constants below; termination is guaranteed by these caps.
package function

import (
	"errors"
	"math"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// Limits, per plan.md's resource-limits policy. maxInputs bounds type 0 multilinear interpolation (2^m corner
// samples per evaluation); real functions rarely exceed 2 inputs. maxNesting bounds type 3 subfunction and
// type 4 procedure nesting. maxProgramOps bounds a parsed calculator program's total instruction count, and
// maxExecSteps bounds one evaluation's executed instructions (if/ifelse can re-run instructions; loops do not
// exist in the calculator language, but the cap also hardens against implementation slips). psStackLimit is
// the standard's own limit (ISO 32000-2 7.10.5.1).
const (
	maxInputs     = 8
	maxOutputs    = 64
	maxNesting    = 16
	maxProgramOps = 65536
	maxExecSteps  = 1 << 20
	psStackLimit  = 100
)

var (
	errNotFunction = errors.New("not a function")
	errBadDomain   = errors.New("function has a missing or invalid /Domain")
	errBadRange    = errors.New("function has a missing or invalid /Range")
	errUnsupported = errors.New("unsupported function type")
	errTooDeep     = errors.New("functions nested too deeply")
)

// Func is one parsed PDF function.
type Func interface {
	// NInputs returns the number of input values the function consumes.
	NInputs() int
	// NOutputs returns the number of output values the function produces.
	NOutputs() int
	// Eval evaluates the function. Missing inputs read as 0, extras are ignored, and inputs are clamped to the
	// function's domain (and outputs to its range, when one is declared), so Eval never fails: malformed data
	// yields clamped or zero values rather than errors.
	Eval(in []float32) []float32
}

// common holds the fields shared by every function type.
type common struct {
	domain []float32 // pairs: min, max per input
	rng    []float32 // pairs: min, max per output; may be nil where optional (types 2 and 3)
}

func (c *common) NInputs() int {
	return len(c.domain) / 2
}

// clampIn returns the inputs clamped to the domain, padding missing values with the domain minimum.
func (c *common) clampIn(in []float32) []float32 {
	m := c.NInputs()
	out := make([]float32, m)
	for i := range m {
		lo, hi := c.domain[2*i], c.domain[2*i+1]
		v := lo
		if i < len(in) {
			v = clampF(in[i], lo, hi)
		}
		out[i] = v
	}
	return out
}

// clampOut clamps the outputs to the range in place, when a range is declared.
func (c *common) clampOut(out []float32) []float32 {
	for i := range out {
		if 2*i+1 < len(c.rng) {
			out[i] = clampF(out[i], c.rng[2*i], c.rng[2*i+1])
		}
	}
	return out
}

// clampF clamps v to [lo, hi], mapping NaN to lo.
func clampF(v, lo, hi float32) float32 {
	if math.IsNaN(float64(v)) || v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// interpolate maps x from [xmin, xmax] to [ymin, ymax] (ISO 32000-2 7.10.2's Interpolate).
func interpolate(x, xmin, xmax, ymin, ymax float32) float32 {
	if xmax == xmin {
		return ymin
	}
	return ymin + (x-xmin)*(ymax-ymin)/(xmax-xmin)
}

// Parse parses obj (resolving references) as a function.
func Parse(d *cos.Document, obj cos.Object) (Func, error) {
	return parse(d, obj, 0)
}

func parse(d *cos.Document, obj cos.Object, depth int) (Func, error) {
	if depth > maxNesting {
		return nil, errTooDeep
	}
	resolved := d.Resolve(obj)
	dict, ok := cos.AsDict(resolved)
	if !ok {
		return nil, errNotFunction
	}
	kind, ok := cos.AsInt(d.Resolve(dict["FunctionType"]))
	if !ok {
		return nil, errNotFunction
	}
	var c common
	if c.domain, ok = numberPairs(d, dict, "Domain", maxInputs); !ok {
		return nil, errBadDomain
	}
	c.rng, _ = numberPairs(d, dict, "Range", maxOutputs)
	switch kind {
	case 0:
		stream, isStream := cos.AsStream(resolved)
		if !isStream {
			return nil, errNotFunction
		}
		return parseSampled(d, stream, c)
	case 2:
		return parseExponential(d, dict, c)
	case 3:
		return parseStitching(d, dict, c, depth)
	case 4:
		stream, isStream := cos.AsStream(resolved)
		if !isStream {
			return nil, errNotFunction
		}
		return parseCalculator(d, stream, c)
	default:
		return nil, errUnsupported
	}
}

// numberPairs reads dict[key] as an even-length array of finite numbers, returning at most maxPairs pairs.
func numberPairs(d *cos.Document, dict cos.Dict, key cos.Name, maxPairs int) ([]float32, bool) {
	arr, ok := d.GetArray(dict, key)
	if !ok || len(arr) < 2 || len(arr)%2 != 0 || len(arr)/2 > maxPairs {
		return nil, false
	}
	out := make([]float32, len(arr))
	for i, entry := range arr {
		v, numOK := cos.AsReal(d.Resolve(entry))
		if !numOK || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
		out[i] = float32(v)
	}
	return out, true
}

// numbers reads dict[key] as an array of finite numbers of length at most maxLen.
func numbers(d *cos.Document, dict cos.Dict, key cos.Name, maxLen int) ([]float32, bool) {
	arr, ok := d.GetArray(dict, key)
	if !ok || len(arr) > maxLen {
		return nil, false
	}
	out := make([]float32, len(arr))
	for i, entry := range arr {
		v, numOK := cos.AsReal(d.Resolve(entry))
		if !numOK || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
		out[i] = float32(v)
	}
	return out, true
}
