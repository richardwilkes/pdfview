// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package engine

// Tile-origin-aware sub-band geometry (ISO/IEC 15444-1 Equations B-14/B-15).
//
// The size of a resolution level or a sub-band depends on the tile-component's
// ABSOLUTE coordinates on the reference grid, not merely on the tile's width or
// height: each level boundary is ceil(coord / 2^k), and ceil(a/2^k) - ceil(b/2^k)
// is not a function of (a-b) alone. For a tile whose origin is a multiple of 2^Nd
// the absolute form collapses to the width-only one (which is why aligned tiles
// have always decoded correctly); for an unaligned origin they differ, and only
// the absolute form matches the encoder.

// CeilDivPow2 returns ceil(a / 2^k) for any sign of a and k >= 0.
func CeilDivPow2(a, k int) int {
	if k <= 0 {
		return a
	}
	d := 1 << uint(k)
	if a >= 0 {
		return (a + d - 1) / d
	}
	return -((-a) / d)
}

// ResolutionLow returns the low coordinate (x0 or y0) of resolution level r for a
// tile-component whose absolute low coordinate is tc0 and which has Nd levels of
// decomposition. r is 0 (coarsest, the LL band) .. Nd (full resolution). The parity
// of this value is the inverse-DWT `cas` for the synthesis that builds level r.
func ResolutionLow(tc0, Nd, r int) int {
	return CeilDivPow2(tc0, Nd-r)
}

// SubbandBound1D returns the [b0, b1) extent of a sub-band along one axis given the
// tile-component's absolute coordinates [tc0, tc1) on that axis. `level` is the
// sub-band's decomposition level (1..Nd; the LL band uses level Nd with highPass
// false). highPass selects the high-pass half along this axis (xob/yob = 1).
func SubbandBound1D(tc0, tc1, Nd, level int, highPass bool) (int, int) {
	// level 0 means no decomposition (the LL band of an Nd==0 image): the band is
	// the full tile-component, with no sub-sampling.
	if level <= 0 {
		return tc0, tc1
	}
	xob := 0
	if highPass {
		xob = 1
	}
	shift := (1 << uint(level-1)) * xob
	b0 := CeilDivPow2(tc0-shift, level)
	b1 := CeilDivPow2(tc1-shift, level)
	if b1 < b0 {
		b1 = b0
	}
	return b0, b1
}

// bandHighPass reports whether sub-band `band` is high-pass along the x and y axes.
// LL: (false,false); HL: (true,false); LH: (false,true); HH: (true,true).
func bandHighPass(band string) (hx, hy bool) {
	switch band {
	case "HL":
		return true, false
	case "LH":
		return false, true
	case "HH":
		return true, true
	default: // "LL"
		return false, false
	}
}

// SubbandDims returns the (width, height) of sub-band `band` at decomposition
// `level` for a tile-component spanning the absolute coordinates [tcx0,tcx1) ×
// [tcy0,tcy1) with Nd levels. This is the tile-origin-aware replacement for
// GetSubbandW/GetSubbandH.
func SubbandDims(tcx0, tcy0, tcx1, tcy1, Nd, level int, band string) (int, int) {
	hx, hy := bandHighPass(band)
	bx0, bx1 := SubbandBound1D(tcx0, tcx1, Nd, level, hx)
	by0, by1 := SubbandBound1D(tcy0, tcy1, Nd, level, hy)
	return bx1 - bx0, by1 - by0
}
