// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package function implements PDF functions (ISO 32000-2 7.10): sampled (type 0), exponential interpolation (type 2),
// stitching (type 3), and PostScript calculator (type 4) functions. Functions map m clamped inputs to n clamped
// outputs; they parameterize Separation/DeviceN tint transforms (internal/color), shadings (internal/shading), and
// transfer functions.
//
// Everything is bounded so hostile input cannot force unbounded work: input dimensionality, stitching/array nesting,
// calculator program size, and calculator execution steps are all capped by the constants below; termination is
// guaranteed by these caps.
package function

import (
	"errors"
	"math"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// Limits. maxInputs bounds type 0 multilinear interpolation (2^m corner samples per evaluation); real functions rarely
// exceed 2 inputs. maxNesting bounds type 3 subfunction and type 4 procedure nesting. maxProgramOps bounds a parsed
// calculator program's total instruction count and, as a per-Parse budget, the total number of function nodes parsed
// across a whole graph (so branching cannot multiply with depth). maxExecSteps bounds one evaluation's executed
// instructions (if/ifelse can re-run instructions; loops do not exist in the calculator language, but the cap also
// hardens against implementation slips). psStackLimit is the standard's own limit (ISO 32000-2 7.10.5.1).
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
	errTooComplex  = errors.New("function graph too complex")
)

// Func is one parsed PDF function.
type Func interface {
	// NInputs returns the number of input values the function consumes.
	NInputs() int
	// NOutputs returns the number of output values the function produces.
	NOutputs() int
	// Eval evaluates the function. Missing inputs read as 0, extras are ignored, and inputs are clamped to the
	// function's domain (and outputs to its range, when one is declared), so Eval never fails: malformed data yields
	// clamped or zero values rather than errors.
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

// decodeCostShift scales a stream-backed node's decoded bytes into node-budget units. Counting nodes alone is not a
// bound on the work a graph forces: each type 0 and type 4 node decodes a stream, internal/filter lets one stream reach
// max(64 MB, 256x input), and cos.Document caches parsed objects but not decoded stream data. At >>10 the whole budget
// buys 64 MB of inflation across a Parse, which one legitimate sample table is nowhere near.
const decodeCostShift = 10

// parseState is what one Parse shares across the whole function graph it walks: the node budget, and the functions
// already parsed from an indirect reference.
type parseState struct {
	// seen memoizes by reference so a graph naming the SAME function repeatedly parses it once. A type 3 stitching
	// function whose /Functions array repeats one reference otherwise re-inflated that node's stream on every entry —
	// thousands of times over from a single cs or sh operator, which is charged for one resource parse.
	seen map[cos.RefKey]Func
	// budget caps the total number of function nodes parsed across the whole graph, not just its depth, plus the bytes
	// their streams decode (see decodeCostShift). maxNesting bounds depth alone, so a chain of stitching functions whose
	// /Functions arrays fan out to shared references could otherwise force exponential (branching^depth) parse work from
	// a tiny file. The shared budget makes total work linear in the file's declared node count.
	budget int
}

// spend debits n budget units, saturating at -1 so no sequence of charges can wrap the counter.
func (st *parseState) spend(n int) {
	if n > st.budget {
		st.budget = -1
		return
	}
	st.budget -= n
}

// spendDecoded debits the budget for the bytes a stream-backed node's decode produced (cos.Document.DecodeWork's delta
// across it). The shift is applied in uint64 and the result capped at the whole budget rather than narrowed first,
// which would overflow the int counter on GOARCH=386/arm.
func (st *parseState) spendDecoded(n uint64) {
	st.spend(int(min(n>>decodeCostShift, uint64(maxProgramOps))))
}

// Parse parses obj (resolving references) as a function.
func Parse(d *cos.Document, obj cos.Object) (Func, error) {
	return parse(d, obj, 0, &parseState{seen: make(map[cos.RefKey]Func), budget: maxProgramOps})
}

// parse parses one node of a function graph, returning the memoized result when the same reference has already been
// parsed under this Parse. Only successes are memoized: a failure may be the shared budget running out, which says
// nothing about the node itself.
func parse(d *cos.Document, obj cos.Object, depth int, st *parseState) (Func, error) {
	if depth > maxNesting {
		return nil, errTooDeep
	}
	ref, isRef := obj.(cos.Ref)
	if isRef {
		if fn, hit := st.seen[ref.Key()]; hit {
			return fn, nil
		}
	}
	fn, err := parseNode(d, obj, depth, st)
	if err != nil {
		return nil, err
	}
	if isRef {
		st.seen[ref.Key()] = fn
	}
	return fn, nil
}

// parseNode parses one node that the memo did not already hold.
func parseNode(d *cos.Document, obj cos.Object, depth int, st *parseState) (Func, error) {
	st.spend(1)
	if st.budget < 0 {
		return nil, errTooComplex
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
		return parseStream(d, st, func() (Func, error) { return parseSampled(d, stream, c) })
	case 2:
		return parseExponential(d, dict, c)
	case 3:
		return parseStitching(d, dict, c, depth, st)
	case 4:
		stream, isStream := cos.AsStream(resolved)
		if !isStream {
			return nil, errNotFunction
		}
		return parseStream(d, st, func() (Func, error) { return parseCalculator(d, stream, c) })
	default:
		return nil, errUnsupported
	}
}

// parseStream runs one stream-backed node's parse and charges the shared budget for what its decode produced, whether
// or not the parse itself succeeded — a failed parse may have inflated the whole allowance before reporting. Once the
// budget is gone the node is reported as too complex, which stops the surrounding graph the way any other exhaustion
// does.
func parseStream(d *cos.Document, st *parseState, parseIt func() (Func, error)) (Func, error) {
	before := d.DecodeWork()
	fn, err := parseIt()
	st.spendDecoded(d.DecodeWork() - before)
	if st.budget < 0 {
		return nil, errTooComplex
	}
	return fn, err
}

// numberPairs reads dict[key] as an even-length array of finite numbers, returning at most maxPairs pairs.
func numberPairs(d *cos.Document, dict cos.Dict, key cos.Name, maxPairs int) ([]float32, bool) {
	arr, ok := d.GetArray(dict, key)
	if !ok || len(arr) < 2 || len(arr)%2 != 0 || len(arr)/2 > maxPairs {
		return nil, false
	}
	return narrowAll(d, arr)
}

// numbers reads dict[key] as an array of finite numbers of length at most maxLen.
func numbers(d *cos.Document, dict cos.Dict, key cos.Name, maxLen int) ([]float32, bool) {
	arr, ok := d.GetArray(dict, key)
	if !ok || len(arr) > maxLen {
		return nil, false
	}
	return narrowAll(d, arr)
}

// narrowAll converts every entry to float32, rejecting the array unless all of them are finite *after* the narrowing: a
// legal PDF number beyond float32's range (1 followed by 39 zeros) is a finite float64 but ±Inf here, and these arrays
// are /Domain, /Range, /C0, /C1, /Encode, /Decode, /Bounds, and /Size. An infinite domain makes interpolate yield NaN,
// a type 2 function (whose /Range is optional, so nothing clamps it) would return ±Inf from Eval in contradiction of
// Func.Eval's contract. A /Size entry goes on to a float→int conversion, which parseSampled bounds in float space
// itself — being finite is not enough to make int(v) architecture-independent — so the two guards are complementary.
func narrowAll(d *cos.Document, arr cos.Array) ([]float32, bool) {
	out := make([]float32, len(arr))
	for i, entry := range arr {
		v, numOK := cos.AsReal(d.Resolve(entry))
		f := float32(v)
		if !numOK || math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return nil, false
		}
		out[i] = f
	}
	return out, true
}
