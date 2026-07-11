package function

import (
	"errors"
	"math"

	"github.com/richardwilkes/pdfview/internal/cos"
)

var errBadExponential = errors.New("invalid exponential function")

// exponential is a type 2 (exponential interpolation) function (ISO 32000-2 7.10.4):
// y_j = C0_j + x^N × (C1_j − C0_j) over a one-input domain.
type exponential struct {
	common
	c0, c1 []float32
	n      float64
}

func parseExponential(d *cos.Document, dict cos.Dict, c common) (Func, error) {
	if c.NInputs() != 1 {
		return nil, errBadExponential
	}
	e := &exponential{common: c}
	n, ok := cos.AsReal(d.Resolve(dict["N"]))
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, errBadExponential
	}
	e.n = n
	e.c0, _ = numbers(d, dict, "C0", maxOutputs)
	e.c1, _ = numbers(d, dict, "C1", maxOutputs)
	switch {
	case len(e.c0) == 0 && len(e.c1) == 0:
		e.c0, e.c1 = []float32{0}, []float32{1}
	case len(e.c0) == 0:
		e.c0 = make([]float32, len(e.c1))
	case len(e.c1) == 0:
		e.c1 = make([]float32, len(e.c0))
		for i := range e.c1 {
			e.c1[i] = 1
		}
	case len(e.c0) != len(e.c1):
		return nil, errBadExponential
	}
	if len(c.rng) != 0 && len(c.rng)/2 != len(e.c0) {
		return nil, errBadExponential
	}
	return e, nil
}

func (e *exponential) NOutputs() int {
	return len(e.c0)
}

func (e *exponential) Eval(in []float32) []float32 {
	x := float64(e.clampIn(in)[0])
	// x^N of a negative base with a fractional exponent is undefined; math.Pow yields NaN there, which the
	// range clamp (or the caller's own clamping) maps to the low bound, matching lenient-reader behavior.
	p := math.Pow(x, e.n)
	out := make([]float32, len(e.c0))
	for j := range out {
		out[j] = e.c0[j] + float32(p)*(e.c1[j]-e.c0[j])
	}
	return e.clampOut(out)
}
