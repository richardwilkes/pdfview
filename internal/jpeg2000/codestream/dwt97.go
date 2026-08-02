// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"
	"math"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/engine"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/wavelet"
)

// inverseDWT97 reconstructs a component for the irreversible (9/7) path: it
// dequantizes the entropy-decoded coefficients per subband and runs the
// multi-level inverse 9/7 transform. The result is returned as floating-point
// coefficients (rounding is deferred so the colour transform can run in float);
// values are in the signed sample domain (DC level shift is applied later).
func (d *Decoder) inverseDWT97(qcd QCD, Nd int, coeffs []engine.CodeBlock, prec, tcx0, tcy0, tcx1, tcy1, reduce int) (wavelet.BandF, error) {
	if len(coeffs) == 0 {
		return wavelet.BandF{}, fmt.Errorf("inverseDWT97: no coefficients")
	}
	if reduce < 0 {
		reduce = 0
	}
	if reduce > Nd {
		reduce = Nd
	}
	compIdx := coeffs[0].Comp

	// deqInto dequantizes one code-block's coefficients into dst (a float buffer
	// over the full subband), blitting at the block's (X0,Y0). The reconstruction
	// adds a per-coefficient mid-point bias of 0.5·2^LowPlane: a coefficient decoded
	// to plane p is known to within 2^p, so its bin centre is 0.5·2^p above the
	// decoded magnitude (0.5 when fully decoded to plane 0). Using the per-coefficient
	// plane — rather than one plane for the whole block — makes a partially decoded
	// last bit-plane reconstruct correctly (matching OpenJPEG's t1). Writes are
	// clipped to the (geometrically sized) destination band so a block whose nominal
	// extent overruns a degenerate band cannot slice out of range.
	deqInto := func(cb *engine.CodeBlock, dst *wavelet.BandF) {
		delta := qcd.bandStepSize(cb.Level, cb.Band, Nd, prec)
		for y := 0; y < cb.H; y++ {
			dy := cb.Y0 + y
			if dy < 0 || dy >= dst.H {
				continue
			}
			for x := 0; x < cb.W; x++ {
				dx := cb.X0 + x
				if dx < 0 || dx >= dst.W {
					continue
				}
				i := y*cb.W + x
				lp := int32(0)
				if cb.LowPlanes != nil {
					lp = cb.LowPlanes[i]
				}
				dst.Data[dy*dst.W+dx] = dequantize(cb.Data[i], delta, math.Ldexp(0.5, int(lp)))
			}
		}
	}

	// Assemble full dequantized float subbands per (level, band) from all the
	// component's code-blocks. Each subband is sized from the ISO geometry
	// (GetSubbandW/H over the component's dimensions), NOT from the code-block
	// extents: on tiny tiles with many decomposition levels a subband can be
	// degenerate in one dimension, and extent-derived sizes then disagree between
	// siblings (LL/LH must share the low width, LL/HL the low height, …), which
	// breaks the inverse transform. Geometric sizing keeps the four bands at every
	// level mutually consistent by construction.
	bands := make(map[int]map[string]*wavelet.BandF)
	alloc := func(L int, b string) {
		if bands[L] == nil {
			bands[L] = make(map[string]*wavelet.BandF)
		}
		gw, gh := engine.SubbandDims(tcx0, tcy0, tcx1, tcy1, Nd, L, b)
		if gw < 0 {
			gw = 0
		}
		if gh < 0 {
			gh = 0
		}
		bands[L][b] = &wavelet.BandF{W: gw, H: gh, Data: make([]float64, gw*gh)}
	}
	// Allocate the complete geometric set: the coarsest LL plus the three detail
	// bands at every decomposition level. Sizing them all from the ISO geometry (not
	// the code-block extents) makes the four bands at each level mutually consistent,
	// so the synthesis never sees a missing or mis-sized sibling — code-blocks blit
	// in, anything absent (a degenerate or undecoded band) stays zero.
	alloc(Nd, "LL")
	for L := 1; L <= Nd; L++ {
		alloc(L, "HL")
		alloc(L, "LH")
		alloc(L, "HH")
	}
	for i := range coeffs {
		cb := &coeffs[i]
		if cb.Comp != compIdx {
			continue
		}
		// A well-formed stream only carries the (level, band) pairs allocated above;
		// a malformed one can present an out-of-range level or stray band, for which
		// there is no destination band — skip it rather than dereference nil.
		lb, ok := bands[cb.Level]
		if !ok {
			continue
		}
		if dst := lb[cb.Band]; dst != nil {
			deqInto(cb, dst)
		}
	}
	band := func(L int, b string) *wavelet.BandF {
		if bands[L] == nil {
			return nil
		}
		return bands[L][b]
	}

	// Single-level (Nd==0) or coarsest-LL passthrough.
	if Nd == 0 {
		ll := band(0, "LL")
		if ll == nil {
			return wavelet.BandF{}, fmt.Errorf("inverseDWT97: no LL block at level 0")
		}
		return *ll, nil
	}

	top := band(Nd, "LL")
	if top == nil {
		return wavelet.BandF{}, fmt.Errorf("inverseDWT97: missing LL subband at level %d", Nd)
	}
	cur := *top
	for k := Nd; k > reduce; k-- {
		lh, hl, hh := band(k, "LH"), band(k, "HL"), band(k, "HH")
		// Substitute empty bands for geometrically degenerate (zero width or height)
		// detail subbands that carry no code-blocks on tiny images; size them from
		// the present siblings (see fillMissing97 / the 5/3 counterpart).
		lh, hl, hh = fillMissing97(&cur, lh, hl, hh)
		if !consistentSubbands(cur.W, cur.H, lh.W, lh.H, hl.W, hl.H, hh.W, hh.H) {
			return wavelet.BandF{}, fmt.Errorf("inverseDWT97: inconsistent subband dimensions at level %d", k)
		}
		// Sample parity of the resolution this level reconstructs (r = Nd-k+1): when
		// the tile origin is not 2^Nd-aligned the low coordinate can be odd, flipping
		// the inverse-DWT interleave (cas) along that axis.
		r := Nd - k + 1
		casX := engine.ResolutionLow(tcx0, Nd, r) & 1
		casY := engine.ResolutionLow(tcy0, Nd, r) & 1
		cur = wavelet.Synthesize97Cas(cur, *lh, *hl, *hh, casX, casY)
	}
	return cur, nil
}

// fillMissing97 substitutes empty float bands for any of LH/HL/HH absent because
// they are geometrically degenerate, sizing them from the low-pass band and the
// present siblings (the 9/7 analogue of fillMissing53).
func fillMissing97(ll, lh, hl, hh *wavelet.BandF) (a, b, c *wavelet.BandF) {
	lowW, lowH := ll.W, ll.H
	highW := 0
	switch {
	case hl != nil:
		highW = hl.W
	case hh != nil:
		highW = hh.W
	}
	highH := 0
	switch {
	case lh != nil:
		highH = lh.H
	case hh != nil:
		highH = hh.H
	}
	empty := func(w, h int) *wavelet.BandF {
		if w < 0 {
			w = 0
		}
		if h < 0 {
			h = 0
		}
		return &wavelet.BandF{W: w, H: h, Data: make([]float64, w*h)}
	}
	if lh == nil {
		lh = empty(lowW, highH)
	}
	if hl == nil {
		hl = empty(highW, lowH)
	}
	if hh == nil {
		hh = empty(highW, highH)
	}
	return lh, hl, hh
}

// consistentSubbands reports whether the four subbands at one decomposition level
// have the dimensions the inverse DWT requires: LL and LH share the (low) width,
// HL and HH share the (high) width, LL and HL share the (low) height, and LH and
// HH share the (high) height. Malformed or truncated streams can violate this,
// which would otherwise slice out of range inside the wavelet package.
func consistentSubbands(llW, llH, lhW, lhH, hlW, hlH, hhW, hhH int) bool {
	return llW == lhW && hlW == hhW && llH == hlH && lhH == hhH
}

// roundBandF rounds a float band to an int32 component plane (round-half-up).
func roundBandF(comp int, b wavelet.BandF) engine.ComponentPlane {
	pix := make([]int32, len(b.Data))
	for i, v := range b.Data {
		pix[i] = int32(math.Floor(v + 0.5))
	}
	return engine.ComponentPlane{Comp: comp, W: b.W, H: b.H, Pix: pix}
}

// applyICTFloat applies the inverse ICT (YCbCr→RGB) in floating point, in place
// over the three component bands. Running it before rounding avoids the extra
// quantization the int-domain ApplyICT incurs.
func applyICTFloat(bands []wavelet.BandF) {
	y, cb, cr := bands[0].Data, bands[1].Data, bands[2].Data
	// The inverse ICT is defined only when the three components share a size (a
	// requirement of MCT); a malformed stream can yield mismatched bands, so bound
	// the loop to the shortest rather than indexing out of range.
	n := len(y)
	if len(cb) < n {
		n = len(cb)
	}
	if len(cr) < n {
		n = len(cr)
	}
	for i := 0; i < n; i++ {
		yi, cbi, cri := y[i], cb[i], cr[i]
		y[i] = yi + 1.402*cri
		cb[i] = yi - 0.344136*cbi - 0.714136*cri
		cr[i] = yi + 1.772*cbi
	}
}
