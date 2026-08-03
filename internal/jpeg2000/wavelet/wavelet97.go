// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package wavelet

// Irreversible 9/7 (CDF 9/7) inverse wavelet transform, used by lossy JPEG 2000.
//
// Unlike the reversible 5/3 transform these are floating-point and operate on
// dequantized coefficients. The transform is defined by lifting (ISO/IEC
// 15444-1 Annex F): four lifting steps plus a scaling step, with whole-sample
// symmetric extension at the boundaries.
//
// NOTE: the round-trip (forward∘inverse = identity) is unit-tested here, which
// proves invertibility and correct boundary handling, but does NOT by itself
// pin the scaling convention to the spec. Final validation of Inverse97 against
// real OpenJPEG lossy output happens when the full lossy path is wired up.

// CDF 9/7 lifting coefficients (ISO/IEC 15444-1 Table F.4).
const (
	c97Alpha = -1.586134342059924
	c97Beta  = -0.052980118572961
	c97Gamma = 0.882911075530934
	c97Delta = 0.443506852043971
	c97K     = 1.230174104914001 // low-pass scaling gain
)

// BandF is a rectangular array of floating-point coefficients in row-major
// (y*W+x) order — the 9/7 counterpart of Band.
type BandF struct {
	W, H int
	Data []float64
}

// Inverse97 performs one-dimensional inverse irreversible 9/7 lifting.
//
// low has length ceil(N/2), high has length floor(N/2), and dst (length N)
// receives the interleaved reconstruction: even-indexed samples from the
// low-pass band, odd-indexed from the high-pass band. Boundary handling uses
// whole-sample symmetric extension. If dst is shorter than N the call is a no-op.
//
// The lifting reverses the analysis order with negated coefficients:
// undo scaling, then undo update(δ), predict(γ), update(β), predict(α).
func Inverse97(low, high, dst []float64) {
	inverse97(low, high, dst, make([]float64, len(low)), make([]float64, len(high)))
}

// inverse97 is Inverse97 with the even/odd working buffers supplied by the caller
// (ebuf len >= len(low), obuf len >= len(high)); the 2D synthesis reuses a single
// pair across all rows instead of allocating two buffers per 1D transform.
func inverse97(low, high, dst, ebuf, obuf []float64) {
	nL := len(low)
	nH := len(high)
	N := nL + nH
	if len(dst) < N || len(ebuf) < nL || len(obuf) < nH {
		return
	}
	// Degenerate resolution (no high-pass, ≤1 low-pass): the lone low/LL coefficient
	// passes through without scaling — OpenJPEG returns before the K step, and the LL
	// step size is not halved, so no factor applies.
	if !(nH > 0 || nL > 1) {
		for i := 0; i < nL && 2*i < len(dst); i++ {
			dst[2*i] = low[i]
		}
		for i := 0; i < nH && 2*i+1 < len(dst); i++ {
			dst[2*i+1] = high[i]
		}
		return
	}

	// e[] holds even (low-pass) samples, o[] holds odd (high-pass) samples.
	e := ebuf[:nL]
	o := obuf[:nH]
	// Undo scaling: analysis multiplied low by 1/K and high by K.
	for i := 0; i < nL; i++ {
		e[i] = low[i] * c97K
	}
	for i := 0; i < nH; i++ {
		o[i] = high[i] * (1.0 / c97K)
	}

	eAt := func(i int) float64 {
		if i < 0 {
			i = -i - 1
		}
		if i >= nL {
			i = 2*nL - i - 1
		}
		if i < 0 || i >= nL {
			return 0
		}
		return e[i]
	}
	oAt := func(i int) float64 {
		if i < 0 {
			i = -i - 1
		}
		if i >= nH {
			i = 2*nH - i - 1
		}
		if i < 0 || i >= nH {
			return 0
		}
		return o[i]
	}

	// Undo update 2 (δ), then predict 2 (γ), update 1 (β), predict 1 (α).
	for n := 0; n < nL; n++ {
		e[n] -= c97Delta * (oAt(n-1) + oAt(n))
	}
	for n := 0; n < nH; n++ {
		o[n] -= c97Gamma * (eAt(n) + eAt(n+1))
	}
	for n := 0; n < nL; n++ {
		e[n] -= c97Beta * (oAt(n-1) + oAt(n))
	}
	for n := 0; n < nH; n++ {
		o[n] -= c97Alpha * (eAt(n) + eAt(n+1))
	}

	// The even/odd interleave assumes a valid split (nL = ⌈N/2⌉, nH = ⌊N/2⌋); a
	// malformed stream can yield inconsistent band sizes, so guard each write rather
	// than indexing out of range (the fuzz contract is "error, never panic").
	for i := 0; i < nL && 2*i < len(dst); i++ {
		dst[2*i] = e[i]
	}
	for i := 0; i < nH && 2*i+1 < len(dst); i++ {
		dst[2*i+1] = o[i]
	}
}

// Inverse97Cas is Inverse97 generalised to the sub-band sample parity `cas`
// (the 9/7 counterpart of Inverse53Cas). cas 0 (the aligned case) delegates to
// Inverse97; cas 1 places the low-pass samples on odd output positions and the
// high-pass on even ones, and shifts each lifting step's neighbour indices by one
// to match (ISO/IEC 15444-1 F.3; OpenJPEG opj_v8dwt_decode with a=cas, b=1-cas).
func Inverse97Cas(low, high, dst []float64, cas int) {
	if cas == 0 {
		Inverse97(low, high, dst)
		return
	}
	nL := len(low)
	nH := len(high)
	N := nL + nH
	if len(dst) < N {
		return
	}
	// Degenerate resolution (cas 1: no low-pass, ≤1 high-pass): OpenJPEG returns
	// before the scaling step, so the lone HIGH coefficient passes through with
	// neither the 2/K nor the K step — but because opj halves the non-LL step size
	// (the "two_invK" compensation in tcd.c) while we keep the full step size, the
	// matching value is high/2.
	if !(nL > 0 || nH > 1) {
		for i := 0; i < nL && 1+2*i < len(dst); i++ {
			dst[1+2*i] = low[i]
		}
		for i := 0; i < nH && 2*i < len(dst); i++ {
			dst[2*i] = high[i] * 0.5
		}
		return
	}
	e := make([]float64, nL)
	o := make([]float64, nH)
	for i := 0; i < nL; i++ {
		e[i] = low[i] * c97K
	}
	for i := 0; i < nH; i++ {
		o[i] = high[i] * (1.0 / c97K)
	}
	eAt := func(i int) float64 {
		if i < 0 {
			i = -i - 1
		}
		if i >= nL {
			i = 2*nL - i - 1
		}
		if i < 0 || i >= nL {
			return 0
		}
		return e[i]
	}
	oAt := func(i int) float64 {
		if i < 0 {
			i = -i - 1
		}
		if i >= nH {
			i = 2*nH - i - 1
		}
		if i < 0 || i >= nH {
			return 0
		}
		return o[i]
	}
	// cas 1 shifts the neighbour pairs relative to cas 0: the low-pass update reads
	// (n, n+1) of the high-pass and the high-pass predict reads (n-1, n) of the
	// low-pass (the mirror of cas 0's (n-1, n) and (n, n+1)).
	for n := 0; n < nL; n++ {
		e[n] -= c97Delta * (oAt(n) + oAt(n+1))
	}
	for n := 0; n < nH; n++ {
		o[n] -= c97Gamma * (eAt(n-1) + eAt(n))
	}
	for n := 0; n < nL; n++ {
		e[n] -= c97Beta * (oAt(n) + oAt(n+1))
	}
	for n := 0; n < nH; n++ {
		o[n] -= c97Alpha * (eAt(n-1) + eAt(n))
	}
	// Interleave: low-pass to odd positions, high-pass to even.
	for i := 0; i < nL && 1+2*i < len(dst); i++ {
		dst[1+2*i] = e[i]
	}
	for i := 0; i < nH && 2*i < len(dst); i++ {
		dst[2*i] = o[i]
	}
}

// Synthesize97Cas is Synthesize97 with explicit horizontal/vertical sub-band
// sample parities. casX==casY==0 is identical to Synthesize97.
func Synthesize97Cas(ll, lh, hl, hh BandF, casX, casY int) BandF {
	if casX == 0 && casY == 0 {
		return Synthesize97(ll, lh, hl, hh)
	}
	wl := ll.W
	wh := hl.W
	hlv := ll.H
	hhv := lh.H
	W := wl + wh
	H := hlv + hhv

	upper := make([]float64, W*hlv)
	for y := 0; y < hlv; y++ {
		Inverse97Cas(ll.Data[y*ll.W:y*ll.W+ll.W], hl.Data[y*hl.W:y*hl.W+hl.W], upper[y*W:(y+1)*W], casX)
	}
	lower := make([]float64, W*hhv)
	for y := 0; y < hhv; y++ {
		Inverse97Cas(lh.Data[y*lh.W:y*lh.W+lh.W], hh.Data[y*hh.W:y*hh.W+hh.W], lower[y*W:(y+1)*W], casX)
	}

	out := make([]float64, W*H)
	colLow := make([]float64, hlv)
	colHigh := make([]float64, hhv)
	colOut := make([]float64, H)
	for x := 0; x < W; x++ {
		for y := 0; y < hlv; y++ {
			colLow[y] = upper[y*W+x]
		}
		for y := 0; y < hhv; y++ {
			colHigh[y] = lower[y*W+x]
		}
		Inverse97Cas(colLow, colHigh, colOut, casY)
		for y := 0; y < H; y++ {
			out[y*W+x] = colOut[y]
		}
	}
	return BandF{W: W, H: H, Data: out}
}

// Synthesize97 performs a single-level 2D inverse 9/7 DWT from the four subbands
// and returns the reconstructed band. Like Synthesize53 it inverts horizontally
// (rows) then vertically (columns); subband dimensions must be consistent.
func Synthesize97(ll, lh, hl, hh BandF) BandF {
	wl := ll.W
	wh := hl.W
	hlv := ll.H
	hhv := lh.H
	W := wl + wh
	H := hlv + hhv

	// Reusable even/odd working buffers for the horizontal 1D transforms (sized for
	// the longest low/high half across the rows: wl and wh respectively).
	ebuf := make([]float64, wl)
	obuf := make([]float64, wh)

	upper := make([]float64, W*hlv)
	for y := 0; y < hlv; y++ {
		inverse97(ll.Data[y*ll.W:y*ll.W+ll.W], hl.Data[y*hl.W:y*hl.W+hl.W], upper[y*W:(y+1)*W], ebuf, obuf)
	}
	lower := make([]float64, W*hhv)
	for y := 0; y < hhv; y++ {
		inverse97(lh.Data[y*lh.W:y*lh.W+lh.W], hh.Data[y*hh.W:y*hh.W+hh.W], lower[y*W:(y+1)*W], ebuf, obuf)
	}

	// Vertical inverse lifting, done row-wise in place rather than column-by-column,
	// so each scaling/lifting step runs over contiguous memory instead of touching
	// `out` with stride W. Low rows land on even output rows, high rows on odd (cas 0).
	out := make([]float64, W*H)
	for i := 0; i < hlv; i++ {
		copy(out[2*i*W:2*i*W+W], upper[i*W:i*W+W])
	}
	for i := 0; i < hhv; i++ {
		copy(out[(2*i+1)*W:(2*i+1)*W+W], lower[i*W:i*W+W])
	}
	inverse97VerticalCas0(out, W, hlv, hhv)

	return BandF{W: W, H: H, Data: out}
}

// inverse97VerticalCas0 performs the vertical half of the 2D inverse 9/7 lifting in
// place on `out` (W columns, hlv low rows on even output rows, hhv high rows on odd)
// for the aligned parity cas 0. It is the column-wise Inverse97 reshaped into row-wise
// sweeps: undo scaling, then the four lifting steps (δ update, γ predict, β update,
// α predict), each reading whole rows of the opposite parity with the same symmetric
// row-boundary extension as Inverse97's eAt/oAt.
func inverse97VerticalCas0(out []float64, W, hlv, hhv int) {
	// Degenerate column (no high rows, ≤1 low row): the lone coefficient passes
	// through unscaled, exactly as Inverse97 returns before the K step.
	if !(hhv > 0 || hlv > 1) {
		return
	}
	evenRow := func(j int) []float64 { return lowRow97(out, W, hlv, j) }
	oddRow := func(j int) []float64 { return highRow97(out, W, hhv, j) }

	// Undo scaling: analysis multiplied low by 1/K and high by K.
	for i := 0; i < hlv; i++ {
		e := evenRow(i)
		for x := 0; x < W; x++ {
			e[x] *= c97K
		}
	}
	for i := 0; i < hhv; i++ {
		o := oddRow(i)
		for x := 0; x < W; x++ {
			o[x] *= 1.0 / c97K
		}
	}

	// Undo update 2 (δ) on the even (low) rows. With no high rows the term is 0.
	if hhv > 0 {
		for n := 0; n < hlv; n++ {
			e := evenRow(n)
			oa, ob := oddRow(n-1), oddRow(n)
			for x := 0; x < W; x++ {
				e[x] -= c97Delta * (oa[x] + ob[x])
			}
		}
	}
	// Undo predict 2 (γ) on the odd (high) rows.
	for n := 0; n < hhv; n++ {
		o := oddRow(n)
		ea, eb := evenRow(n), evenRow(n+1)
		for x := 0; x < W; x++ {
			o[x] -= c97Gamma * (ea[x] + eb[x])
		}
	}
	// Undo update 1 (β) on the even rows.
	if hhv > 0 {
		for n := 0; n < hlv; n++ {
			e := evenRow(n)
			oa, ob := oddRow(n-1), oddRow(n)
			for x := 0; x < W; x++ {
				e[x] -= c97Beta * (oa[x] + ob[x])
			}
		}
	}
	// Undo predict 1 (α) on the odd rows.
	for n := 0; n < hhv; n++ {
		o := oddRow(n)
		ea, eb := evenRow(n), evenRow(n+1)
		for x := 0; x < W; x++ {
			o[x] -= c97Alpha * (ea[x] + eb[x])
		}
	}
}

// lowRow97 returns the low-pass row at index j (symmetrically extended over the hlv
// low rows on the even output rows). Mirrors Inverse97's eAt clamping.
func lowRow97(out []float64, W, hlv, j int) []float64 {
	if j < 0 {
		j = -j - 1
	}
	if j >= hlv {
		j = 2*hlv - j - 1
	}
	if j < 0 {
		j = 0
	} else if j >= hlv {
		j = hlv - 1
	}
	r := 2 * j * W
	return out[r : r+W]
}

// highRow97 returns the high-pass row at index j (symmetrically extended over the hhv
// high rows on the odd output rows). Mirrors Inverse97's oAt clamping.
func highRow97(out []float64, W, hhv, j int) []float64 {
	if j < 0 {
		j = -j - 1
	}
	if j >= hhv {
		j = 2*hhv - j - 1
	}
	if j < 0 {
		j = 0
	} else if j >= hhv {
		j = hhv - 1
	}
	r := (2*j + 1) * W
	return out[r : r+W]
}
