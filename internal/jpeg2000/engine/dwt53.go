// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package engine

import (
	"fmt"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/wavelet"
)

// InverseDWT53 performs multi-level inverse 5x3 lifting on decoded subband coefficients.
// It synthesizes from level Nd down to level `reduce`, returning the LL band at that
// level — i.e. the image at resolution Nd-reduce. reduce=0 reconstructs the full
// image; reduce=Nd returns the coarsest LL. TW, TH are the tile dimensions at full
// resolution.
//
// This is the JPEG 2000-specific adapter: it groups CodeBlocks by level/band and
// drives the generic, codestream-agnostic transform in the wavelet package.
// InverseDWT53 runs the multi-level reversible (5/3) inverse transform. tcx0/tcy0
// are the tile-component absolute coordinates (reference grid mapped through the
// component subsampling); they give the per-resolution sample parity (cas) so a
// tile-component whose low coordinate is odd interleaves correctly — the reversible
// counterpart of the 9/7 path's casX/casY.
func InverseDWT53(Nd int, coeffs []CodeBlock, tcx0, tcy0, tcx1, tcy1, reduce int) (ComponentPlane, error) {
	if len(coeffs) == 0 {
		return ComponentPlane{}, fmt.Errorf("inverseDWT53: no coefficients")
	}
	if reduce < 0 {
		reduce = 0
	}
	if reduce > Nd {
		reduce = Nd
	}

	compIdx := coeffs[0].Comp

	// Size every subband from the ISO B.10.2 geometry (absolute tile-component
	// coordinates), NOT from the code-block extents: on a tiny / heavily clipped
	// tile-component a coarse subband can be degenerate (zero width or height) and
	// then carries no code-blocks at all, so an extent-derived size would be missing
	// or disagree between the four siblings. Geometric sizing keeps LL/LH/HL/HH at
	// every level mutually consistent (shared low/high widths and heights) by
	// construction, so the synthesis always sees a complete set — the absent bands
	// are simply zero. This mirrors the 9/7 path (codestream/dwt97.go).
	bands := map[int]map[string]*wavelet.Band{}
	alloc := func(L int, b string) *wavelet.Band {
		gw, gh := SubbandDims(tcx0, tcy0, tcx1, tcy1, Nd, L, b)
		if gw < 0 {
			gw = 0
		}
		if gh < 0 {
			gh = 0
		}
		bnd := &wavelet.Band{W: gw, H: gh, Data: make([]int32, gw*gh)}
		if bands[L] == nil {
			bands[L] = map[string]*wavelet.Band{}
		}
		bands[L][b] = bnd
		return bnd
	}
	alloc(Nd, "LL")
	for L := 1; L <= Nd; L++ {
		alloc(L, "HL")
		alloc(L, "LH")
		alloc(L, "HH")
	}
	band := func(L int, b string) *wavelet.Band {
		if bands[L] == nil {
			return nil
		}
		return bands[L][b]
	}

	// Blit each code-block into its geometric band, applying the per-coefficient
	// reversible mid-point ((1<<LowPlane)>>1) for a truncated (quality-layer) decode.
	// Writes are clipped to the destination band so a block whose nominal extent
	// overruns a degenerate band cannot slice out of range.
	for i := range coeffs {
		cb := &coeffs[i]
		if cb.Comp != compIdx {
			continue
		}
		lb, ok := bands[cb.Level]
		if !ok {
			continue
		}
		dst := lb[cb.Band]
		if dst == nil {
			continue
		}
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
				v := cb.Data[y*cb.W+x]
				if cb.LowPlanes != nil && v != 0 {
					half := (int32(1) << uint(cb.LowPlanes[y*cb.W+x])) >> 1
					if v > 0 {
						v += half
					} else {
						v -= half
					}
				}
				dst.Data[dy*dst.W+dx] = v
			}
		}
	}

	// Multi-level synthesis: LL_{r-1} = DWT_INV(LL_r, LH_r, HL_r, HH_r). Stop once
	// the LL band at level `reduce` has been reconstructed. The coarsest LL is always
	// allocated above (possibly zero-sized), so there is no "missing LL" case.
	currentLL := *band(Nd, "LL")
	if Nd == 0 {
		return ComponentPlane{Comp: compIdx, W: currentLL.W, H: currentLL.H, Pix: currentLL.Data}, nil
	}
	for k := Nd; k > reduce; k-- {
		lh, hl, hh := band(k, "LH"), band(k, "HL"), band(k, "HH")
		// Geometric sizing guarantees the four siblings agree on the shared low/high
		// widths and heights; assert it so a future regression surfaces here rather
		// than slicing out of range inside the wavelet package.
		if currentLL.W != lh.W || hl.W != hh.W || currentLL.H != hl.H || lh.H != hh.H {
			return ComponentPlane{}, fmt.Errorf("inverseDWT53: inconsistent subband dimensions at level %d", k)
		}
		// Sample parity of the resolution this level reconstructs (r = Nd-k+1): an
		// unaligned tile-component origin can make the low coordinate odd, flipping the
		// inverse interleave along that axis (matches the 9/7 path).
		r := Nd - k + 1
		casX := ResolutionLow(tcx0, Nd, r) & 1
		casY := ResolutionLow(tcy0, Nd, r) & 1
		currentLL = wavelet.Synthesize53Cas(currentLL, *lh, *hl, *hh, casX, casY)
	}
	return ComponentPlane{Comp: compIdx, W: currentLL.W, H: currentLL.H, Pix: currentLL.Data}, nil
}

// GetResolutionSize returns the dimensions of the image at resolution level r.
// ISO 15444-1: r=0 is coarsest (LL_Nd), r=Nd is finest (full size).
func GetResolutionSize(fullW, fullH, Nd, r int) (int, int) {
	if r < 0 {
		return 0, 0
	}
	if r > Nd {
		r = Nd
	}
	// r steps from coarsest to finest correspond to Nd-r levels of decomposition.
	return wavelet.ResolutionSize(fullW, fullH, Nd-r)
}
