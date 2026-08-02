// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package wavelet

import (
	"testing"
)

// forward1d is the analysis counterpart of Inverse53: the reversible 5/3 forward
// lifting. It splits x (length N) into low (ceil(N/2)) and high (floor(N/2)) and
// is the exact algebraic inverse of Inverse53, so round-tripping must be lossless.
func forward1d(x []int32) (low, high []int32) {
	N := len(x)
	nL := (N + 1) / 2
	nH := N / 2
	even := make([]int32, nL)
	odd := make([]int32, nH)
	for i := 0; i < nL; i++ {
		even[i] = x[2*i]
	}
	for i := 0; i < nH; i++ {
		odd[i] = x[2*i+1]
	}
	evenAt := func(i int) int32 {
		if i < 0 {
			i = -i - 1
		}
		if i >= nL {
			i = 2*nL - i - 1
		}
		if i < 0 || i >= nL {
			return 0
		}
		return even[i]
	}
	high = make([]int32, nH)
	for i := 0; i < nH; i++ {
		high[i] = odd[i] - ((evenAt(i) + evenAt(i+1)) >> 1)
	}
	highAt := func(i int) int32 {
		if i < 0 {
			i = -i - 1
		}
		if i >= nH {
			i = 2*nH - i - 1
		}
		if i < 0 || i >= nH {
			return 0
		}
		return high[i]
	}
	low = make([]int32, nL)
	for i := 0; i < nL; i++ {
		low[i] = even[i] + ((highAt(i-1) + highAt(i) + 2) >> 2)
	}
	return low, high
}

func TestInverse53RoundTrip1D(t *testing.T) {
	// Cover odd and even lengths, including degenerate tiny sizes.
	for _, n := range []int{1, 2, 3, 4, 5, 7, 8, 9, 15, 16, 17, 33} {
		x := make([]int32, n)
		for i := range x {
			// Mixed-sign, non-trivial pattern that exercises predict+update.
			x[i] = int32((i*7+3)%19 - 9 + (i%3)*5 - (i%5)*4)
		}
		low, high := forward1d(x)
		got := make([]int32, n)
		Inverse53(low, high, got)
		for i := range x {
			if got[i] != x[i] {
				t.Fatalf("n=%d: round-trip mismatch at %d: got %d want %d\nx=%v\nlow=%v high=%v got=%v",
					n, i, got[i], x[i], x, low, high, got)
			}
		}
	}
}

// forward2d is the analysis counterpart of Synthesize53. Because the reversible
// 5/3 transform is non-linear (integer rounding), the axis order matters: it must
// be the exact reverse of Synthesize53, which inverts horizontally then
// vertically. So the forward applies the vertical (column) split first, then the
// horizontal (row) split, producing the four subbands.
func forward2d(img []int32, W, H int) (ll, lh, hl, hh Band) {
	wl := (W + 1) / 2
	wh := W / 2
	hlv := (H + 1) / 2
	hhv := H / 2

	// Vertical transform: split each column into low (hlv) and high (hhv) rows.
	vlow := make([]int32, W*hlv)
	vhigh := make([]int32, W*hhv)
	col := make([]int32, H)
	for x := 0; x < W; x++ {
		for y := 0; y < H; y++ {
			col[y] = img[y*W+x]
		}
		low, high := forward1d(col)
		for y := 0; y < hlv; y++ {
			vlow[y*W+x] = low[y]
		}
		for y := 0; y < hhv; y++ {
			vhigh[y*W+x] = high[y]
		}
	}

	ll = Band{W: wl, H: hlv, Data: make([]int32, wl*hlv)}
	lh = Band{W: wl, H: hhv, Data: make([]int32, wl*hhv)}
	hl = Band{W: wh, H: hlv, Data: make([]int32, wh*hlv)}
	hh = Band{W: wh, H: hhv, Data: make([]int32, wh*hhv)}

	// Horizontal transform: split each row of the vertical-low half into ll/hl,
	// and each row of the vertical-high half into lh/hh.
	for y := 0; y < hlv; y++ {
		low, high := forward1d(vlow[y*W : y*W+W])
		copy(ll.Data[y*wl:y*wl+wl], low)
		copy(hl.Data[y*wh:y*wh+wh], high)
	}
	for y := 0; y < hhv; y++ {
		low, high := forward1d(vhigh[y*W : y*W+W])
		copy(lh.Data[y*wl:y*wl+wl], low)
		copy(hh.Data[y*wh:y*wh+wh], high)
	}
	return ll, lh, hl, hh
}

func TestSynthesize53RoundTrip2D(t *testing.T) {
	// Include odd widths/heights so symmetric extension at both axes is exercised.
	sizes := [][2]int{{1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 3}, {3, 5}, {8, 8}, {7, 9}, {16, 16}, {33, 17}}
	for _, s := range sizes {
		W, H := s[0], s[1]
		img := make([]int32, W*H)
		for i := range img {
			x, y := i%W, i/W
			img[i] = int32((x*5+y*3)%23 - 11 + (x*y)%7)
		}
		ll, lh, hl, hh := forward2d(img, W, H)
		out := Synthesize53(ll, lh, hl, hh)
		if out.W != W || out.H != H {
			t.Fatalf("%dx%d: reconstructed size %dx%d", W, H, out.W, out.H)
		}
		for i := range img {
			if out.Data[i] != img[i] {
				t.Fatalf("%dx%d: mismatch at (%d,%d): got %d want %d",
					W, H, i%W, i/W, out.Data[i], img[i])
			}
		}
	}
}

// synthesize53Ref is a straightforward column-by-column reference 2D inverse 5/3
// (horizontal rows via Inverse53, then each column gathered, lifted, scattered). It
// matches the original Synthesize53 implementation and is used to assert the row-wise
// vertical sweep in the production code produces byte-identical output.
func synthesize53Ref(ll, lh, hl, hh Band) Band {
	wl, wh := ll.W, hl.W
	hlv, hhv := ll.H, lh.H
	W, H := wl+wh, hlv+hhv

	upper := make([]int32, W*hlv)
	for y := 0; y < hlv; y++ {
		Inverse53(ll.Data[y*ll.W:y*ll.W+ll.W], hl.Data[y*hl.W:y*hl.W+hl.W], upper[y*W:(y+1)*W])
	}
	lower := make([]int32, W*hhv)
	for y := 0; y < hhv; y++ {
		Inverse53(lh.Data[y*lh.W:y*lh.W+lh.W], hh.Data[y*hh.W:y*hh.W+hh.W], lower[y*W:(y+1)*W])
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
		Inverse53(colLow, colHigh, colOut)
		for y := 0; y < H; y++ {
			out[y*W+x] = colOut[y]
		}
	}
	return Band{W: W, H: H, Data: out}
}

// TestSynthesize53MatchesColumnRef checks that the row-wise vertical lifting in
// Synthesize53 is bit-identical to the column-by-column reference, on arbitrary
// (not necessarily invertible) subband data and many odd/even size combinations.
func TestSynthesize53MatchesColumnRef(t *testing.T) {
	sizes := [][2]int{{1, 1}, {2, 1}, {1, 2}, {2, 2}, {3, 3}, {4, 4}, {5, 3}, {3, 5},
		{8, 8}, {7, 9}, {9, 7}, {16, 16}, {17, 33}, {33, 17}, {1, 8}, {8, 1}}
	seed := int32(12345)
	rnd := func() int32 { seed = seed*1103515245 + 12345; return (seed>>8)%201 - 100 }
	for _, s := range sizes {
		W, H := s[0], s[1]
		wl, hlv := (W+1)/2, (H+1)/2
		wh, hhv := W-wl, H-hlv
		mk := func(w, h int) Band {
			d := make([]int32, w*h)
			for i := range d {
				d[i] = rnd()
			}
			return Band{W: w, H: h, Data: d}
		}
		ll, lh, hl, hh := mk(wl, hlv), mk(wl, hhv), mk(wh, hlv), mk(wh, hhv)
		got := Synthesize53(ll, lh, hl, hh)
		want := synthesize53Ref(ll, lh, hl, hh)
		if got.W != want.W || got.H != want.H {
			t.Fatalf("%dx%d: size %dx%d vs ref %dx%d", W, H, got.W, got.H, want.W, want.H)
		}
		for i := range want.Data {
			if got.Data[i] != want.Data[i] {
				t.Fatalf("%dx%d: mismatch at (%d,%d): got %d want %d",
					W, H, i%got.W, i/got.W, got.Data[i], want.Data[i])
			}
		}
	}
}

func TestResolutionSize(t *testing.T) {
	cases := []struct {
		w, h, levels, wantW, wantH int
	}{
		{64, 64, 0, 64, 64},
		{64, 64, 1, 32, 32},
		{64, 64, 2, 16, 16},
		{33, 17, 1, 17, 9},
		{33, 17, 2, 9, 5},
		{1, 1, 3, 1, 1},
		{5, 5, -1, 5, 5}, // negative treated as zero
	}
	for _, c := range cases {
		gw, gh := ResolutionSize(c.w, c.h, c.levels)
		if gw != c.wantW || gh != c.wantH {
			t.Errorf("ResolutionSize(%d,%d,%d) = %dx%d, want %dx%d",
				c.w, c.h, c.levels, gw, gh, c.wantW, c.wantH)
		}
	}
}
