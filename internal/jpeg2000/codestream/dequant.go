// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import "math"

// Dequantization for the irreversible (9/7) path, ISO/IEC 15444-1 Annex E.
//
// Each subband b has a step size Δ_b = (1 + μ_b/2^11) · 2^(R_b − ε_b), where
// R_b = prec + gain_b is the nominal dynamic range, ε_b/μ_b are the signalled
// exponent/mantissa, and gain_b is the subband's analysis gain. A quantized
// coefficient q is reconstructed (mid-point) as (q + sign(q)·r)·Δ_b with the
// reconstruction bias r (0.5 by default).

// subbandGain returns the log2 analysis energy gain of a subband orientation,
// used in the dynamic-range term: LL=0, HL/LH=1, HH=2.
func subbandGain(band string) int {
	switch band {
	case "HL", "LH":
		return 1
	case "HH":
		return 2
	default: // LL
		return 0
	}
}

// stepSize computes Δ_b = (1 + mant/2^11) · 2^(prec + gain − expn).
func stepSize(expn, mant, prec, gain int) float64 {
	return (1.0 + float64(mant)/2048.0) * math.Pow(2, float64(prec+gain-expn))
}

// qcdIndex maps a subband (decomposition `level` in 1..NL, orientation `band`)
// to its index in QCD.StepSizes. The LL band exists only at the deepest level
// and is index 0; detail bands follow resolution by resolution from the coarsest
// as {HL, LH, HH}.
func qcdIndex(level int, band string, NL int) int {
	if band == "LL" {
		return 0
	}
	base := 1 + 3*(NL-level)
	switch band {
	case "HL":
		return base
	case "LH":
		return base + 1
	case "HH":
		return base + 2
	}
	return 0
}

// bandStepSize returns Δ_b for a subband, resolving the quantization style.
// For reversible (no quantization) it returns 1 (the engine produces final
// integer coefficients directly, with no scaling). For scalar expounded it reads
// the per-band exponent/mantissa; for scalar derived it derives the exponent from
// the single signalled value (ε_b = ε_0 − N_L + level, μ_b = μ_0).
func (q QCD) bandStepSize(level int, band string, NL, prec int) float64 {
	gain := subbandGain(band)
	switch q.Style {
	case qStyleExpounded:
		idx := qcdIndex(level, band, NL)
		if idx < 0 || idx >= len(q.StepSizes) {
			return 0
		}
		s := q.StepSizes[idx]
		return stepSize(s.Expn, s.Mant, prec, gain)
	case qStyleDerived:
		if len(q.StepSizes) == 0 {
			return 0
		}
		// Scalar derived (SIQNT): only ε_0/μ_0 are signalled and every band shares the
		// mantissa, with ε_b = ε_0 − (N_L − level) for the band's resolution. The
		// exponent is clamped at 0 (ISO 15444-1 E.1, matching OpenJPEG j2k.c): without
		// the clamp a deep subband whose base exponent is small would get a negative
		// exponent and an astronomically large step size.
		s0 := q.StepSizes[0]
		expn := s0.Expn - NL + level
		if expn < 0 {
			expn = 0
		}
		return stepSize(expn, s0.Mant, prec, gain)
	default: // qStyleNone
		return 1.0
	}
}

// stepExpnsFor returns the per-subband quantization exponents ε_b for a component
// with NL decomposition levels, ordered LL, then {HL,LH,HH} resolution by
// resolution from the coarsest — the same order Tier-1 and qcdIndex use to derive
// the magnitude bit-count (numbps = ε_b + guard − 1). Expounded and reversible
// styles already signal one entry per subband, so their list is returned as-is.
// The derived style signals only ε_0; the rest are expanded as ε_b = max(ε_0 −
// ⌊(b−1)/3⌋, 0) (ISO 15444-1 E.1, matching OpenJPEG j2k.c) so Tier-1 can index
// every subband instead of falling back to a precision-based bit count.
func (q QCD) stepExpnsFor(NL int) []int {
	if q.Style != qStyleDerived || len(q.StepSizes) == 0 || NL < 0 {
		se := make([]int, len(q.StepSizes))
		for i, s := range q.StepSizes {
			se[i] = s.Expn
		}
		return se
	}
	e0 := q.StepSizes[0].Expn
	n := 1 + 3*NL
	se := make([]int, n)
	for i := 0; i < n; i++ {
		dec := 0
		if i > 0 {
			dec = (i - 1) / 3
		}
		if e := e0 - dec; e > 0 {
			se[i] = e
		}
	}
	return se
}

// dequantize reconstructs a quantized coefficient to its (mid-point) value.
// r is the reconstruction bias (0.5 selects the centre of the quantization
// interval). Zero stays zero.
func dequantize(q int32, delta, r float64) float64 {
	if q == 0 {
		return 0
	}
	if q > 0 {
		return (float64(q) + r) * delta
	}
	return (float64(q) - r) * delta
}
