// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

// Package wavelet implements reversible (integer) discrete wavelet transforms.
//
// It is deliberately free of any JPEG 2000 specifics: it operates purely on
// coefficient arrays and knows nothing about code-blocks, subband-orientation
// strings, or the codestream. This keeps it self-contained so it can later be
// extracted into a standalone, publishable module. JPEG 2000-specific subband
// organization lives in the engine package, which imports this one one-way.
package wavelet

// Band is a rectangular array of coefficients in row-major (y*W+x) order.
type Band struct {
	W, H int
	Data []int32
}

// Inverse53 performs one-dimensional inverse reversible 5/3 lifting.
//
// low has length ceil(N/2), high has length floor(N/2), and dst (length N)
// receives the interleaved reconstruction: even-indexed samples come from the
// low-pass band, odd-indexed samples from the high-pass band. Boundary handling
// uses whole-sample symmetric extension and the integer rounding rules of the
// reversible 5/3 transform (ISO/IEC 15444-1). If dst is shorter than N the call
// is a no-op.
func Inverse53(low, high, dst []int32) {
	inverse53(low, high, dst, make([]int32, len(low)))
}

// inverse53 is Inverse53 with the low-pass working copy supplied by the caller in
// lbuf (which must have len >= len(low)); the 2D synthesis reuses a single buffer
// across all rows and columns instead of allocating one per 1D transform.
func inverse53(low, high, dst, lbuf []int32) {
	nL := len(low)
	nH := len(high)
	N := nL + nH
	if len(dst) < N || len(lbuf) < nL {
		return
	}

	// Step 1: undo the update step on the low-pass samples:
	//   L'[i] = L[i] - floor((H[i-1] + H[i] + 2) / 4)
	// Work on a copy so neighbour reads see the original high-pass values.
	L := lbuf[:nL]
	copy(L, low)
	// Access high with symmetric extension.
	hAt := func(i int) int32 {
		if i < 0 {
			i = -i - 1
		}
		if i >= nH {
			i = 2*nH - i - 1
		}
		if i < 0 || i >= nH { // degenerate case nH == 0
			return 0
		}
		return high[i]
	}
	for i := 0; i < nL; i++ {
		hl := hAt(i - 1)
		hr := hAt(i)
		L[i] = L[i] - ((hl + hr + 2) >> 2)
	}

	// Step 2: undo the prediction step on the high-pass samples, using the
	// updated low-pass values:
	//   odd[i] = H[i] + floor((L'[i] + L'[i+1]) / 2)
	lAt := func(i int) int32 {
		if i < 0 {
			i = -i - 1
		}
		if i >= nL {
			i = 2*nL - i - 1
		}
		if i < 0 || i >= nL {
			return 0
		}
		return L[i]
	}

	for i := 0; i < nL; i++ {
		if 2*i >= N { // malformed (inconsistent low/high sizes): don't index out of range
			break
		}
		dst[2*i] = L[i]
		if 2*i+1 < N && i < nH {
			pred := (lAt(i) + lAt(i+1)) >> 1
			dst[2*i+1] = high[i] + pred
		}
	}
}

// Inverse53Cas is Inverse53 generalised to the sub-band sample parity `cas`
// (ISO/IEC 15444-1 F.3.4; OpenJPEG opj_dwt_decode_1_). cas is the parity of the
// reconstructed resolution's low coordinate: 0 means the first output sample is
// even (low-pass), 1 means it is odd (high-pass). For a tile whose origin is a
// multiple of 2^Nd every level has cas 0 and this reduces to Inverse53; a tile at
// an unaligned origin makes cas 1 at some levels, where the low/high interleave —
// and which lifting equation runs first — swap.
func Inverse53Cas(low, high, dst []int32, cas int) {
	if cas == 0 {
		Inverse53(low, high, dst)
		return
	}
	sn := len(low)  // low-pass (S) count
	dn := len(high) // high-pass (D) count
	N := sn + dn
	if len(dst) < N {
		return
	}
	// Interleave: with cas 1 the low-pass samples land on odd output positions and
	// the high-pass on even ones (the mirror of cas 0).
	for i := 0; i < sn; i++ {
		dst[1+2*i] = low[i]
	}
	for i := 0; i < dn; i++ {
		dst[2*i] = high[i]
	}
	// S(i)=dst[2i] (even, here high-pass), D(i)=dst[2i+1] (odd, here low-pass).
	// SS_ clamps the even array to dn, DD_ clamps the odd array to sn (ISO macros).
	ss := func(i int) int32 {
		if i < 0 {
			i = 0
		} else if i >= dn {
			i = dn - 1
		}
		return dst[2*i]
	}
	dd := func(i int) int32 {
		if i < 0 {
			i = 0
		} else if i >= sn {
			i = sn - 1
		}
		return dst[2*i+1]
	}
	if sn == 0 && dn == 1 {
		dst[0] /= 2
		return
	}
	for i := 0; i < sn; i++ { // undo update on the low-pass (odd positions)
		dst[2*i+1] -= (ss(i) + ss(i+1) + 2) >> 2
	}
	for i := 0; i < dn; i++ { // undo predict on the high-pass (even positions)
		dst[2*i] += (dd(i) + dd(i-1)) >> 1
	}
}

// Synthesize53 performs a single-level 2D inverse 5/3 DWT from the four
// subbands and returns the reconstructed band.
//
// The subband dimensions must be consistent: ll is (wl x hl_), lh is (wl x hh_),
// hl is (wh x hl_), hh is (wh x hh_). The result is (wl+wh) x (hl_+hh_): the
// inverse transform is applied first along rows (horizontal) then columns
// (vertical).
func Synthesize53(ll, lh, hl, hh Band) Band {
	wl := ll.W  // low-pass column count
	wh := hl.W  // high-pass column count
	hlv := ll.H // low-pass row count
	hhv := lh.H // high-pass row count
	W := wl + wh
	H := hlv + hhv

	// Reusable low-pass working buffer for the 1D transforms (sized for the longest
	// low half encountered: wl across rows, hlv down columns).
	lbuf := make([]int32, max(wl, hlv))

	// Horizontal inverse lifting: reconstruct each row from its low/high halves.
	upper := make([]int32, W*hlv)
	for y := 0; y < hlv; y++ {
		inverse53(ll.Data[y*ll.W:y*ll.W+ll.W], hl.Data[y*hl.W:y*hl.W+hl.W], upper[y*W:(y+1)*W], lbuf)
	}
	lower := make([]int32, W*hhv)
	for y := 0; y < hhv; y++ {
		inverse53(lh.Data[y*lh.W:y*lh.W+lh.W], hh.Data[y*hh.W:y*hh.W+hh.W], lower[y*W:(y+1)*W], lbuf)
	}

	// Vertical inverse lifting, done row-wise in place rather than column-by-column.
	// The column-gather/lift/scatter approach touched `out` with stride W (one cache
	// line per sample); operating on whole rows keeps each lifting step contiguous and
	// auto-vectorizable. Low rows land on even output rows, high rows on odd (cas 0).
	out := make([]int32, W*H)
	for i := 0; i < hlv; i++ {
		copy(out[2*i*W:2*i*W+W], upper[i*W:i*W+W])
	}
	for i := 0; i < hhv; i++ {
		copy(out[(2*i+1)*W:(2*i+1)*W+W], lower[i*W:i*W+W])
	}
	inverse53VerticalCas0(out, W, hlv, hhv)

	return Band{W: W, H: H, Data: out}
}

// inverse53VerticalCas0 performs the vertical half of the 2D inverse 5/3 lifting in
// place on `out` (W columns, hlv low rows interleaved on even output rows, hhv high
// rows on odd) for the aligned parity cas 0. It is the column-wise Inverse53 reshaped
// into two row-wise sweeps so each step runs over contiguous memory:
//
//	even[i] -= (high[i-1] + high[i] + 2) >> 2   (undo update, reading original high rows)
//	odd[i]  += (even'[i] + even'[i+1]) >> 1     (undo predict, reading updated even rows)
//
// with symmetric extension at the row boundaries, exactly as Inverse53's hAt/lAt.
func inverse53VerticalCas0(out []int32, W, hlv, hhv int) {
	// Undo the update step on the low (even) rows using the neighbouring high rows.
	// When there are no high rows the term is (0+0+2)>>2 = 0, so the sweep is skipped.
	if hhv > 0 {
		sub53SweepFn(out, W, hlv, hhv)
	}
	// Undo the prediction step on the high (odd) rows using the updated low rows.
	add53SweepFn(out, W, hlv, hhv)
}

// sub53SweepScalar is the update sweep's loop, split out so it can be the default target of sub53SweepFn (see
// simd_dispatch.go). Dispatch is per sweep rather than per row: the row loop stays whole and inlined here, which a
// per-row indirect call would have cost the scalar path at small W.
func sub53SweepScalar(out []int32, W, hlv, hhv int) {
	for i := 0; i < hlv; i++ {
		e := out[2*i*W : 2*i*W+W]
		hl := highRow53(out, W, hhv, i-1)
		hr := highRow53(out, W, hhv, i)
		for x := 0; x < W; x++ {
			e[x] -= (hl[x] + hr[x] + 2) >> 2
		}
	}
}

// add53SweepScalar is the prediction sweep's loop, split out for the same reason as sub53SweepScalar.
func add53SweepScalar(out []int32, W, hlv, hhv int) {
	for i := 0; i < hhv; i++ {
		o := out[(2*i+1)*W : (2*i+1)*W+W]
		ll := lowRow53(out, W, hlv, i)
		lr := lowRow53(out, W, hlv, i+1)
		for x := 0; x < W; x++ {
			o[x] += (ll[x] + lr[x]) >> 1
		}
	}
}

// highRow53 returns the high-pass row at index j (symmetrically extended over the
// hhv high rows, which occupy the odd output rows). Mirrors Inverse53's hAt clamping.
func highRow53(out []int32, W, hhv, j int) []int32 {
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

// lowRow53 returns the low-pass row at index j (symmetrically extended over the hlv
// low rows, which occupy the even output rows). Mirrors Inverse53's lAt clamping.
func lowRow53(out []int32, W, hlv, j int) []int32 {
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

// Synthesize53Cas is Synthesize53 with explicit horizontal/vertical sub-band
// sample parities (casX, casY) for tiles whose origin is not 2^Nd-aligned. With
// casX==casY==0 it is identical to Synthesize53.
func Synthesize53Cas(ll, lh, hl, hh Band, casX, casY int) Band {
	if casX == 0 && casY == 0 {
		return Synthesize53(ll, lh, hl, hh)
	}
	wl := ll.W
	wh := hl.W
	hlv := ll.H
	hhv := lh.H
	W := wl + wh
	H := hlv + hhv

	upper := make([]int32, W*hlv)
	for y := 0; y < hlv; y++ {
		Inverse53Cas(ll.Data[y*ll.W:y*ll.W+ll.W], hl.Data[y*hl.W:y*hl.W+hl.W], upper[y*W:(y+1)*W], casX)
	}
	lower := make([]int32, W*hhv)
	for y := 0; y < hhv; y++ {
		Inverse53Cas(lh.Data[y*lh.W:y*lh.W+lh.W], hh.Data[y*hh.W:y*hh.W+hh.W], lower[y*W:(y+1)*W], casX)
	}

	out := make([]int32, W*H)
	colLow := make([]int32, hlv)
	colHigh := make([]int32, hhv)
	colOut := make([]int32, H)
	for x := 0; x < W; x++ {
		for y := 0; y < hlv; y++ {
			colLow[y] = upper[y*W+x]
		}
		for y := 0; y < hhv; y++ {
			colHigh[y] = lower[y*W+x]
		}
		Inverse53Cas(colLow, colHigh, colOut, casY)
		for y := 0; y < H; y++ {
			out[y*W+x] = colOut[y]
		}
	}
	return Band{W: W, H: H, Data: out}
}

// ResolutionSize returns the dimensions of an image after `levels` forward 5/3
// decompositions, i.e. the size of the LL band `levels` levels down. Each level
// halves both dimensions, rounding up. Negative levels are treated as zero.
func ResolutionSize(fullW, fullH, levels int) (int, int) {
	w, h := fullW, fullH
	for i := 0; i < levels; i++ {
		w = (w + 1) / 2
		h = (h + 1) / 2
	}
	return w, h
}
