// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package wavelet

import "testing"

// benchBands builds four consistent subbands of total size W×H (low half = ceil/2).
func benchBands53(W, H int) (ll, lh, hl, hh Band) {
	wl, hlv := (W+1)/2, (H+1)/2
	wh, hhv := W-wl, H-hlv
	seed := int32(7)
	rnd := func() int32 { seed = seed*1103515245 + 12345; return (seed >> 9) % 4096 }
	mk := func(w, h int) Band {
		d := make([]int32, w*h)
		for i := range d {
			d[i] = rnd()
		}
		return Band{W: w, H: h, Data: d}
	}
	return mk(wl, hlv), mk(wl, hhv), mk(wh, hlv), mk(wh, hhv)
}

func benchBands97(W, H int) (ll, lh, hl, hh BandF) {
	wl, hlv := (W+1)/2, (H+1)/2
	wh, hhv := W-wl, H-hlv
	seed := int32(7)
	rnd := func() float64 { seed = seed*1103515245 + 12345; return float64((seed>>9)%4096) * 0.01 }
	mk := func(w, h int) BandF {
		d := make([]float64, w*h)
		for i := range d {
			d[i] = rnd()
		}
		return BandF{W: w, H: h, Data: d}
	}
	return mk(wl, hlv), mk(wl, hhv), mk(wh, hlv), mk(wh, hhv)
}

func BenchmarkSynthesize53_1024(b *testing.B) {
	ll, lh, hl, hh := benchBands53(1024, 1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Synthesize53(ll, lh, hl, hh)
	}
}

func BenchmarkSynthesize97_1024(b *testing.B) {
	ll, lh, hl, hh := benchBands97(1024, 1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Synthesize97(ll, lh, hl, hh)
	}
}
