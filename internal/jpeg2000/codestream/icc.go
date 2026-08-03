// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"encoding/binary"
	"math"
)

// Minimal ICC colour transform for the common case OpenJPEG handles via Little CMS:
// a three-channel matrix/TRC RGB INPUT or DISPLAY profile (rTRC/gTRC/bTRC tone curves
// plus the rXYZ/gXYZ/bXYZ colorant matrix into the D50 PCS). For such a profile the
// transform to sRGB is fully deterministic — linearise each channel through its tone
// curve, apply the colorant matrix to XYZ(D50), then XYZ(D50)→sRGB — and is bit-exact
// against lcms. Profiles we cannot reduce to this form (LUT-based A2B/B2A tables, a
// non-RGB device space, or a malformed/degenerate matrix) return ok=false; the caller
// then renders the raw components and leaves full colour management to the application.

type toneCurve struct {
	gamma float64 // used when lut is nil; 1.0 = linear
	lut   []float64
}

// lin maps a device value u in [0,1] to its linear value in [0,1].
func (t toneCurve) lin(u float64) float64 {
	if u < 0 {
		u = 0
	} else if u > 1 {
		u = 1
	}
	if t.lut == nil {
		if t.gamma == 1 {
			return u
		}
		return math.Pow(u, t.gamma)
	}
	n := len(t.lut)
	if n == 1 {
		return t.lut[0]
	}
	p := u * float64(n-1)
	i := int(p)
	if i >= n-1 {
		return t.lut[n-1]
	}
	f := p - float64(i)
	return t.lut[i]*(1-f) + t.lut[i+1]*f
}

type iccTransform struct {
	matrix [3][3]float64 // linear RGB -> XYZ(D50)
	trc    [3]toneCurve
}

// parseICCMatrixTRC parses an ICC profile into a matrix/TRC RGB→XYZ(D50) transform.
// It returns ok=false for any profile that is not a usable three-channel RGB
// matrix/TRC profile (the safe fallback: the caller renders raw components).
func parseICCMatrixTRC(p []byte) (*iccTransform, bool) {
	if len(p) < 132 {
		return nil, false
	}
	if string(p[16:20]) != "RGB " { // data colour space must be RGB
		return nil, false
	}
	nTags := int(binary.BigEndian.Uint32(p[128:132]))
	if nTags <= 0 || nTags > 1<<12 || 132+nTags*12 > len(p) {
		return nil, false
	}
	tags := make(map[string][2]int, nTags)
	for i := 0; i < nTags; i++ {
		o := 132 + i*12
		sig := string(p[o : o+4])
		off := int(binary.BigEndian.Uint32(p[o+4 : o+8]))
		sz := int(binary.BigEndian.Uint32(p[o+8 : o+12]))
		if off < 0 || sz < 0 || off+sz > len(p) || off+8 > len(p) {
			continue
		}
		tags[sig] = [2]int{off, sz}
	}

	var tr iccTransform
	xyzCols := [3]string{"rXYZ", "gXYZ", "bXYZ"}
	for c, sig := range xyzCols {
		t, ok := tags[sig]
		if !ok || t[1] < 20 || string(p[t[0]:t[0]+4]) != "XYZ " {
			return nil, false
		}
		o := t[0] + 8
		// s15Fixed16: a signed 16.16 fixed-point number.
		tr.matrix[0][c] = float64(int32(binary.BigEndian.Uint32(p[o:o+4]))) / 65536
		tr.matrix[1][c] = float64(int32(binary.BigEndian.Uint32(p[o+4:o+8]))) / 65536
		tr.matrix[2][c] = float64(int32(binary.BigEndian.Uint32(p[o+8:o+12]))) / 65536
	}
	// Reject a degenerate (zero / non-invertible) colorant matrix — a malformed profile.
	if det3(&tr.matrix) == 0 {
		return nil, false
	}

	for c, sig := range [3]string{"rTRC", "gTRC", "bTRC"} {
		t, ok := tags[sig]
		if !ok {
			return nil, false
		}
		curve, ok := parseTRC(p, t[0], t[1])
		if !ok {
			return nil, false
		}
		tr.trc[c] = curve
	}
	return &tr, true
}

// parseTRC reads a 'curv' or 'para' tone-reproduction curve.
func parseTRC(p []byte, off, sz int) (toneCurve, bool) {
	if off+12 > len(p) {
		return toneCurve{}, false
	}
	switch string(p[off : off+4]) {
	case "curv":
		n := int(binary.BigEndian.Uint32(p[off+8 : off+12]))
		switch {
		case n == 0: // identity
			return toneCurve{gamma: 1}, true
		case n == 1: // single u8Fixed8 gamma
			if off+14 > len(p) {
				return toneCurve{}, false
			}
			return toneCurve{gamma: float64(binary.BigEndian.Uint16(p[off+12:off+14])) / 256}, true
		default: // n-entry uint16 LUT
			if off+12+2*n > len(p) {
				return toneCurve{}, false
			}
			lut := make([]float64, n)
			for k := 0; k < n; k++ {
				lut[k] = float64(binary.BigEndian.Uint16(p[off+12+2*k:off+14+2*k])) / 65535
			}
			return toneCurve{lut: lut}, true
		}
	case "para":
		// Parametric curve, function types 0–4 (ICC.1 10.18). Only the simplest,
		// most common type-0 (pure power) is handled directly; the rest fall back.
		if off+12 > len(p) {
			return toneCurve{}, false
		}
		if binary.BigEndian.Uint16(p[off+8:off+10]) == 0 && off+16 <= len(p) {
			g := float64(int32(binary.BigEndian.Uint32(p[off+12:off+16]))) / 65536
			return toneCurve{gamma: g}, true
		}
		return toneCurve{}, false
	}
	return toneCurve{}, false
}

func det3(m *[3][3]float64) float64 {
	return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
}

// toSRGB16 converts a device RGB triple (each in [0,1]) to 16-bit sRGB.
func (t *iccTransform) toSRGB16(r, g, b float64) (uint16, uint16, uint16) {
	lr, lg, lb := t.trc[0].lin(r), t.trc[1].lin(g), t.trc[2].lin(b)
	m := &t.matrix
	x := m[0][0]*lr + m[0][1]*lg + m[0][2]*lb
	y := m[1][0]*lr + m[1][1]*lg + m[1][2]*lb
	z := m[2][0]*lr + m[2][1]*lg + m[2][2]*lb
	return xyzD50ToSRGB16(x, y, z)
}
