// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import "math"

// Colour-management conversions for the JP2 colour spaces that OpenJPEG implements
// through Little CMS: enumerated CIELab (colr enum 14) and an embedded ICC profile
// (colr METH==2). We reproduce the same colorimetric transform in pure Go — Lab/device
// values → XYZ in the D50 PCS → sRGB — which matches lcms's output (the sRGB profile is
// matrix/TRC, so lcms uses a colorimetric, gamut-untabulated transform). Validated
// against opj_decompress built with lcms (worst ≈ 12/65535, i.e. exact at 8-bit).

// D50 reference white (the ICC profile connection space white point).
var d50 = [3]float64{0.9642, 1.0, 0.8249}

// bradfordD50toD65 adapts an XYZ colour from the D50 PCS white to the D65 white that
// the sRGB primaries are defined against (Bradford chromatic adaptation).
var bradfordD50toD65 = [3][3]float64{
	{0.9555766, -0.0230393, 0.0631636},
	{-0.0282895, 1.0099416, 0.0210077},
	{0.0122982, -0.0204830, 1.3299098},
}

// xyzD65toLinearSRGB is the standard XYZ(D65) → linear sRGB matrix (IEC 61966-2-1).
var xyzD65toLinearSRGB = [3][3]float64{
	{3.2404542, -1.5371385, -0.4985314},
	{-0.9692660, 1.8760108, 0.0415560},
	{0.0556434, -0.2040259, 1.0572252},
}

// sRGBGamma applies the sRGB opto-electronic transfer function to a linear value.
func sRGBGamma(c float64) float64 {
	if c <= 0 {
		return 0
	}
	if c >= 1 {
		return 1
	}
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1.0/2.4) - 0.055
}

// xyzD50ToSRGB16 converts an XYZ colour expressed against the D50 PCS white to a
// 16-bit sRGB triple: adapt D50→D65, apply the sRGB matrix, gamma-encode, scale and
// round. Shared by the CIELab and ICC paths.
func xyzD50ToSRGB16(x, y, z float64) (uint16, uint16, uint16) {
	a := &bradfordD50toD65
	x2 := a[0][0]*x + a[0][1]*y + a[0][2]*z
	y2 := a[1][0]*x + a[1][1]*y + a[1][2]*z
	z2 := a[2][0]*x + a[2][1]*y + a[2][2]*z

	m := &xyzD65toLinearSRGB
	r := sRGBGamma(m[0][0]*x2 + m[0][1]*y2 + m[0][2]*z2)
	g := sRGBGamma(m[1][0]*x2 + m[1][1]*y2 + m[1][2]*z2)
	b := sRGBGamma(m[2][0]*x2 + m[2][1]*y2 + m[2][2]*z2)

	to16 := func(v float64) uint16 { return uint16(math.Round(v * 65535)) }
	return to16(r), to16(g), to16(b)
}

// labToSRGB16 converts a CIELab colour (L*, a*, b*) — relative to the D50 white — to a
// 16-bit sRGB triple via the standard inverse Lab→XYZ transform and xyzD50ToSRGB16.
func labToSRGB16(l, a, b float64) (uint16, uint16, uint16) {
	fy := (l + 16) / 116
	fx := fy + a/500
	fz := fy - b/200
	finv := func(t float64) float64 {
		if t > 6.0/29.0 {
			return t * t * t
		}
		return 3 * (6.0 / 29.0) * (6.0 / 29.0) * (t - 4.0/29.0)
	}
	return xyzD50ToSRGB16(d50[0]*finv(fx), d50[1]*finv(fy), d50[2]*finv(fz))
}
