// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestStepSize(t *testing.T) {
	// Hand-computed Δ = (1 + mant/2048) · 2^(prec+gain−expn), prec=8.
	cases := []struct {
		expn, mant, gain int
		want             float64
	}{
		{14, 1824, 0, (1 + 1824.0/2048) * math.Pow(2, 8+0-14)}, // LL5
		{14, 1776, 1, (1 + 1776.0/2048) * math.Pow(2, 8+1-14)}, // HL5
		{10, 1890, 2, (1 + 1890.0/2048) * math.Pow(2, 8+2-10)}, // HH1
	}
	for _, c := range cases {
		got := stepSize(c.expn, c.mant, 8, c.gain)
		if !approx(got, c.want) {
			t.Errorf("stepSize(expn=%d,mant=%d,gain=%d) = %g, want %g", c.expn, c.mant, c.gain, got, c.want)
		}
	}
	// Spot-check a concrete value: LL5 step must be ~0.0295410.
	if got := stepSize(14, 1824, 8, 0); !approx(got, 0.0295410156250) {
		t.Errorf("LL5 step = %g, want 0.029541015625", got)
	}
}

func TestQCDIndex(t *testing.T) {
	NL := 5
	cases := []struct {
		level int
		band  string
		want  int
	}{
		{5, "LL", 0},
		{5, "HL", 1}, {5, "LH", 2}, {5, "HH", 3},
		{4, "HL", 4}, {4, "LH", 5}, {4, "HH", 6},
		{1, "HL", 13}, {1, "LH", 14}, {1, "HH", 15},
	}
	for _, c := range cases {
		if got := qcdIndex(c.level, c.band, NL); got != c.want {
			t.Errorf("qcdIndex(level=%d,band=%s,NL=%d) = %d, want %d", c.level, c.band, NL, got, c.want)
		}
	}
}

// bandStepSize over the real opj lossy QCD (expounded), mapping each subband to
// its exponent/mantissa and checking a few representative bands.
func TestBandStepSize_Expounded(t *testing.T) {
	// Same 16-entry table as the QCD parse test (Expn, Mant), NL=5, prec=8.
	q := QCD{
		Style: qStyleExpounded,
		StepSizes: []QCDStepSize{
			{14, 1824}, {14, 1776}, {14, 1776}, {14, 1728},
			{13, 1792}, {13, 1792}, {13, 1760},
			{12, 1872}, {12, 1872}, {12, 1896},
			{10, 5}, {10, 5}, {10, 71}, {10, 2003}, {10, 2003}, {10, 1890},
		},
	}
	NL, prec := 5, 8
	cases := []struct {
		level int
		band  string
		expn  int
		mant  int
		gain  int
	}{
		{5, "LL", 14, 1824, 0},
		{5, "HL", 14, 1776, 1},
		{5, "HH", 14, 1728, 2},
		{1, "HH", 10, 1890, 2},
		{1, "HL", 10, 2003, 1},
	}
	for _, c := range cases {
		want := stepSize(c.expn, c.mant, prec, c.gain)
		got := q.bandStepSize(c.level, c.band, NL, prec)
		if !approx(got, want) {
			t.Errorf("bandStepSize(level=%d,band=%s) = %g, want %g", c.level, c.band, got, want)
		}
	}
}

func TestBandStepSize_ReversibleIsUnity(t *testing.T) {
	q := QCD{Style: qStyleNone, StepSizes: []QCDStepSize{{Expn: 8}}}
	if got := q.bandStepSize(3, "HH", 5, 8); got != 1.0 {
		t.Errorf("reversible bandStepSize = %g, want 1.0", got)
	}
}

func TestDequantize(t *testing.T) {
	const d = 0.5
	if v := dequantize(0, d, 0.5); v != 0 {
		t.Errorf("dequantize(0) = %g, want 0", v)
	}
	if v := dequantize(3, d, 0.5); !approx(v, (3+0.5)*d) {
		t.Errorf("dequantize(3) = %g, want %g", v, (3+0.5)*d)
	}
	if v := dequantize(-3, d, 0.5); !approx(v, (-3-0.5)*d) {
		t.Errorf("dequantize(-3) = %g, want %g", v, (-3-0.5)*d)
	}
}
