// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package wavelet

import (
	"math"
	"testing"
)

// forward97 is the analysis counterpart of Inverse97: the exact algebraic
// inverse of the synthesis lifting (predict α, update β, predict γ, update δ,
// then scaling). Round-tripping it through Inverse97 must recover the input.
func forward97(x []float64) (low, high []float64) {
	N := len(x)
	nL := (N + 1) / 2
	nH := N / 2
	// Degenerate resolution (≤1 low-pass, no high-pass): the lone sample passes
	// through unscaled, matching Inverse97's degenerate convention (OpenJPEG skips
	// the scaling step), so the round-trip stays an identity.
	if !(nH > 0 || nL > 1) {
		low = make([]float64, nL)
		high = make([]float64, nH)
		for i := 0; i < nL; i++ {
			low[i] = x[2*i]
		}
		for i := 0; i < nH; i++ {
			high[i] = x[2*i+1]
		}
		return low, high
	}
	e := make([]float64, nL)
	o := make([]float64, nH)
	for i := 0; i < nL; i++ {
		e[i] = x[2*i]
	}
	for i := 0; i < nH; i++ {
		o[i] = x[2*i+1]
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
	// Analysis order (reverse of synthesis): predict α, update β, predict γ, update δ.
	for n := 0; n < nH; n++ {
		o[n] += c97Alpha * (eAt(n) + eAt(n+1))
	}
	for n := 0; n < nL; n++ {
		e[n] += c97Beta * (oAt(n-1) + oAt(n))
	}
	for n := 0; n < nH; n++ {
		o[n] += c97Gamma * (eAt(n) + eAt(n+1))
	}
	for n := 0; n < nL; n++ {
		e[n] += c97Delta * (oAt(n-1) + oAt(n))
	}
	// Scaling: low *= 1/K, high *= K.
	low = make([]float64, nL)
	high = make([]float64, nH)
	for i := 0; i < nL; i++ {
		low[i] = e[i] * (1.0 / c97K)
	}
	for i := 0; i < nH; i++ {
		high[i] = o[i] * c97K
	}
	return low, high
}

const eps97 = 1e-9

func TestInverse97RoundTrip1D(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5, 7, 8, 9, 15, 16, 17, 33} {
		x := make([]float64, n)
		for i := range x {
			x[i] = float64((i*7+3)%19-9) + 0.25*float64(i%5) - 0.5*float64(i%3)
		}
		low, high := forward97(x)
		got := make([]float64, n)
		Inverse97(low, high, got)
		for i := range x {
			if math.Abs(got[i]-x[i]) > eps97 {
				t.Fatalf("n=%d: round-trip mismatch at %d: got %g want %g", n, i, got[i], x[i])
			}
		}
	}
}

// forward2d97 mirrors forward2d (vertical then horizontal) for the 9/7 transform.
func forward2d97(img []float64, W, H int) (ll, lh, hl, hh BandF) {
	wl := (W + 1) / 2
	wh := W / 2
	hlv := (H + 1) / 2
	hhv := H / 2

	vlow := make([]float64, W*hlv)
	vhigh := make([]float64, W*hhv)
	col := make([]float64, H)
	for x := 0; x < W; x++ {
		for y := 0; y < H; y++ {
			col[y] = img[y*W+x]
		}
		low, high := forward97(col)
		for y := 0; y < hlv; y++ {
			vlow[y*W+x] = low[y]
		}
		for y := 0; y < hhv; y++ {
			vhigh[y*W+x] = high[y]
		}
	}

	ll = BandF{W: wl, H: hlv, Data: make([]float64, wl*hlv)}
	lh = BandF{W: wl, H: hhv, Data: make([]float64, wl*hhv)}
	hl = BandF{W: wh, H: hlv, Data: make([]float64, wh*hlv)}
	hh = BandF{W: wh, H: hhv, Data: make([]float64, wh*hhv)}
	for y := 0; y < hlv; y++ {
		low, high := forward97(vlow[y*W : y*W+W])
		copy(ll.Data[y*wl:y*wl+wl], low)
		copy(hl.Data[y*wh:y*wh+wh], high)
	}
	for y := 0; y < hhv; y++ {
		low, high := forward97(vhigh[y*W : y*W+W])
		copy(lh.Data[y*wl:y*wl+wl], low)
		copy(hh.Data[y*wh:y*wh+wh], high)
	}
	return ll, lh, hl, hh
}

// synthesize97Ref is the column-by-column reference 2D inverse 9/7 (the original
// implementation): horizontal rows via Inverse97, then each column gathered, lifted,
// scattered. Used to assert the row-wise vertical sweep is bit-identical.
func synthesize97Ref(ll, lh, hl, hh BandF) BandF {
	wl, wh := ll.W, hl.W
	hlv, hhv := ll.H, lh.H
	W, H := wl+wh, hlv+hhv

	upper := make([]float64, W*hlv)
	for y := 0; y < hlv; y++ {
		Inverse97(ll.Data[y*ll.W:y*ll.W+ll.W], hl.Data[y*hl.W:y*hl.W+hl.W], upper[y*W:(y+1)*W])
	}
	lower := make([]float64, W*hhv)
	for y := 0; y < hhv; y++ {
		Inverse97(lh.Data[y*lh.W:y*lh.W+lh.W], hh.Data[y*hh.W:y*hh.W+hh.W], lower[y*W:(y+1)*W])
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
		Inverse97(colLow, colHigh, colOut)
		for y := 0; y < H; y++ {
			out[y*W+x] = colOut[y]
		}
	}
	return BandF{W: W, H: H, Data: out}
}

// TestSynthesize97MatchesColumnRef checks the row-wise vertical 9/7 lifting in
// Synthesize97 is bit-identical to the column-by-column reference (the per-sample
// float arithmetic is the same expression in the same order, so equality is exact).
func TestSynthesize97MatchesColumnRef(t *testing.T) {
	sizes := [][2]int{{1, 1}, {2, 1}, {1, 2}, {2, 2}, {3, 3}, {4, 4}, {5, 3}, {3, 5},
		{8, 8}, {7, 9}, {9, 7}, {16, 16}, {17, 33}, {33, 17}, {1, 8}, {8, 1}}
	seed := int32(98765)
	rnd := func() float64 { seed = seed*1103515245 + 12345; return float64((seed>>8)%2001-1000) * 0.013 }
	for _, s := range sizes {
		W, H := s[0], s[1]
		wl, hlv := (W+1)/2, (H+1)/2
		wh, hhv := W-wl, H-hlv
		mk := func(w, h int) BandF {
			d := make([]float64, w*h)
			for i := range d {
				d[i] = rnd()
			}
			return BandF{W: w, H: h, Data: d}
		}
		ll, lh, hl, hh := mk(wl, hlv), mk(wl, hhv), mk(wh, hlv), mk(wh, hhv)
		got := Synthesize97(ll, lh, hl, hh)
		want := synthesize97Ref(ll, lh, hl, hh)
		if got.W != want.W || got.H != want.H {
			t.Fatalf("%dx%d: size %dx%d vs ref %dx%d", W, H, got.W, got.H, want.W, want.H)
		}
		for i := range want.Data {
			if got.Data[i] != want.Data[i] {
				t.Fatalf("%dx%d: mismatch at (%d,%d): got %g want %g",
					W, H, i%got.W, i/got.W, got.Data[i], want.Data[i])
			}
		}
	}
}

func TestSynthesize97RoundTrip2D(t *testing.T) {
	sizes := [][2]int{{1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 3}, {3, 5}, {8, 8}, {7, 9}, {16, 16}, {33, 17}}
	for _, s := range sizes {
		W, H := s[0], s[1]
		img := make([]float64, W*H)
		for i := range img {
			x, y := i%W, i/W
			img[i] = float64((x*5+y*3)%23-11) + 0.5*float64((x*y)%7)
		}
		ll, lh, hl, hh := forward2d97(img, W, H)
		out := Synthesize97(ll, lh, hl, hh)
		if out.W != W || out.H != H {
			t.Fatalf("%dx%d: reconstructed size %dx%d", W, H, out.W, out.H)
		}
		for i := range img {
			if math.Abs(out.Data[i]-img[i]) > eps97 {
				t.Fatalf("%dx%d: mismatch at (%d,%d): got %g want %g",
					W, H, i%W, i/W, out.Data[i], img[i])
			}
		}
	}
}
