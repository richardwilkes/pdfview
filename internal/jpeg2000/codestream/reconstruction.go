// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/engine"
)

// Colour-space interpretation policy (DELIBERATE divergence from OpenJPEG's CLI).
//
// A raw JPEG 2000 codestream (.j2k) carries NO colour-space information. The SIZ
// marker describes only component count, bit depth and subsampling; the COD "MCT"
// flag selects the reversible/irreversible *component transform* (RCT/ICT), which
// we always invert — that is a decorrelation step for compression, NOT a colour
// space. Nothing in the codestream says "these components are RGB" or "YCbCr".
//
// Therefore, for a raw codestream this decoder returns the reconstructed component
// samples as-is (component 0 -> R, 1 -> G, 2 -> B, 3 -> A) and does NOT guess a
// colour space. This is an intentional difference from the OpenJPEG command-line
// tool (opj_decompress), which heuristically assumes a 3-component — especially
// chroma-subsampled — raw codestream is sYCC and applies a YCbCr->RGB conversion on
// output. That heuristic is not derivable from the codestream and is outside ISO
// 15444-1; we do not follow it.
//
// Colour-space -> RGB conversion is correct only when a JP2 container declares it
// via the `colr` box: sRGB (enum 16) and greyscale (17) pass through as-is; sYCC
// (enum 18) and e-sYCC (enum 24) trigger a YCbCr->RGB conversion; CMYK (enum 12) is
// converted to RGB. A `cdef` box that stores the channels in a non-standard order is
// honoured (the planes are permuted into colour order first). Colour spaces requiring
// full ICC colour management — CIELab (enum 14) and an embedded ICC profile (colr
// METH=2) — are NOT converted (OpenJPEG itself uses lcms for these); such images still
// decode, with their component samples returned and rendered by component count, but
// the displayed colour is approximate — use DecodeComponents for the raw planes. The
// colr/cdef boxes are parsed at the JP2 layer (ROADMAP M6).
//
// Testing consequence: our e2e suite traces opj output exactly EXCEPT for this
// colour-space step. Subsampled / 3-component raw codestreams are validated against
// the ground-truth source planes, not opj_decompress's colour-converted PPM output.
//
// Conformance note: this divergence is invisible to ISO/IEC 15444-4 conformance.
// That suite defines correctness at the component-sample level (per-component PGX
// references); the in-codestream MCT (RCT/ICT) is part of decoding and we apply it.
// The sYCC heuristic is an opj-CLI display convenience outside ISO 15444-1 — and
// for the conformance 3-component codestreams opj itself does NOT apply it (verified:
// opj's colour PPM equals the raw components assembled as RGB, 0 differing pixels for
// p0_04 and p0_14). So the policy keeps us fully conformant while staying principled.

// ReconstructImage converts raw component planes into a final image.Image.
// It applies necessary color transforms (like RCT) and color model mapping.
func (d *Decoder) reconstructImage(fullPlanes [][]int32, w, h int) (image.Image, error) {
	// Wrap as ComponentPlanes for final processing. w,h are the output dimensions
	// (reduced when a reduced-resolution decode was requested).
	planes := make([]engine.ComponentPlane, len(d.header.siz.Components))
	for c := range d.header.siz.Components {
		planes[c] = engine.ComponentPlane{
			Comp: c,
			W:    w,
			H:    h,
			Pix:  fullPlanes[c],
		}
	}

	// Reversible multiple-component transform (RCT). It is defined on the first
	// three components (ISO 15444-1 G.1.2); any further components (e.g. an alpha
	// channel) pass through untouched. The irreversible colour transform (ICT) is
	// applied earlier, per-tile in float, before rounding. RCT applies only when all
	// three components share the reversible transform — a COC that makes them differ
	// (mixed 5/3 and 9/7) carries no colour transform.
	if len(planes) >= 3 && d.header.cod.UseMCT &&
		d.mainCodFor(0).Reversible && d.mainCodFor(1).Reversible && d.mainCodFor(2).Reversible {
		ApplyRCT(planes[:3])
	}

	// JP2 palette (pclr + cmap): the decoded component is an index into the palette;
	// expand it to the generated channels. This precedes colour-space handling and
	// the plain component→image paths.
	if len(d.paletteEntries) > 0 && len(d.cmap) > 0 {
		outPlanes, depths, err := d.applyPalette(fullPlanes, w, h)
		if err != nil {
			return nil, err
		}
		return d.toImagePalette(outPlanes, depths, w, h)
	}

	// JP2 cdef (channel definition): an image may store its colour channels in a
	// non-standard order (e.g. Cr, Cb, Y instead of Y, Cb, Cr). Reorder the planes into
	// colour order (Asoc 1, 2, 3, … then non-colour channels) so the colour-space
	// conversion below sees the channels it expects. A standard-order or absent cdef
	// leaves the planes untouched. This runs after MCT (RCT/ICT operate on the coded
	// component order) and before the colour-space dispatch.
	planes = d.reorderByChannelDefs(planes)

	// sYCC colour space (declared by a JP2 colr box, enum 18): the three components
	// are Y, Cb, Cr and must be converted to RGB. This is the only place a
	// colour-space conversion happens (see the policy above).
	if len(planes) == 3 && d.colorSpace == 18 {
		return d.toImageSYCC(planes)
	}

	// e-sYCC colour space (JP2 colr enum 24): three components Y, Cb, Cr converted to
	// RGB by the e-sYCC matrix (distinct from sYCC's coefficients).
	if len(planes) == 3 && d.colorSpace == 24 {
		return d.toImageESYCC(planes)
	}

	// CIELab colour space (JP2 colr enum 14): three components L*, a*, b* converted to
	// 16-bit sRGB through the D50 PCS (colorimetric, matching OpenJPEG's lcms path).
	if len(planes) == 3 && d.colorSpace == 14 {
		return d.toImageCIELab(planes)
	}

	// Embedded ICC profile (colr METH==2): apply a three-channel matrix/TRC RGB
	// profile to sRGB. A profile we cannot reduce to that form (LUT-based, non-RGB,
	// degenerate) is ignored and the components render as-is.
	if len(planes) == 3 && len(d.iccProfile) > 0 {
		if tr, ok := parseICCMatrixTRC(d.iccProfile); ok {
			return d.toImageICC(planes, tr)
		}
	}

	// CMYK colour space (JP2 colr enum 12): four components C, M, Y, K converted to
	// RGB. Without this a CMYK image would be mis-rendered as RGBA (K read as alpha).
	if len(planes) == 4 && d.colorSpace == 12 {
		return d.toImageCMYK(planes)
	}

	// Convert to image.Image based on component count. Four components are treated
	// as RGBA (the fourth component is the alpha channel).
	switch len(planes) {
	case 1:
		return d.toImageGray(planes[0])
	case 2:
		return d.toImageGrayAlpha(planes)
	case 3:
		return d.toImageRGB(planes)
	case 4:
		return d.toImageRGBA(planes)
	}

	return nil, fmt.Errorf("reconstruct: unsupported component count %d "+
		"(use DecodeComponents for native per-component planes)", len(planes))
}

// toImageGray converts the reconstructed component plane to image.Gray or image.Gray16
// depending on precision. Handles signed/unsigned by offsetting signed samples.
func (d *Decoder) toImageGray(plane engine.ComponentPlane) (image.Image, error) {
	if plane.W <= 0 || plane.H <= 0 {
		return nil, fmt.Errorf("toImageGray: invalid plane size %dx%d", plane.W, plane.H)
	}
	if len(d.header.siz.Components) == 0 {
		return nil, fmt.Errorf("toImageGray: no components found")
	}
	comp := d.header.siz.Components[plane.Comp]
	bits := comp.Precision
	if bits <= 8 {
		img := image.NewGray(image.Rect(0, 0, plane.W, plane.H))
		maxVal := int32((1 << uint(bits)) - 1)
		// Display level shift: map samples into the unsigned [0, 2^P-1] range. For
		// unsigned components this re-adds the 2^(P-1) the encoder subtracted; for
		// signed components (whose decoded values are already centred on 0) it is the
		// conventional viewing shift, so the true signed value is (output - 2^(P-1)).
		offset := int32(1) << uint(bits-1)
		for y := 0; y < plane.H; y++ {
			for x := 0; x < plane.W; x++ {
				v := plane.Pix[y*plane.W+x] + offset
				if v < 0 {
					v = 0
				}
				if v > maxVal {
					v = maxVal
				}
				img.Pix[y*img.Stride+x] = uint8(v & 0xFF)
			}
		}
		return img, nil
	}
	// 9..16 bits -> Gray16
	img := image.NewGray16(image.Rect(0, 0, plane.W, plane.H))
	maxVal := int32((1 << uint(bits)) - 1)
	offset := int32(1) << uint(bits-1)
	for y := 0; y < plane.H; y++ {
		for x := 0; x < plane.W; x++ {
			v := plane.Pix[y*plane.W+x] + offset
			if v < 0 {
				v = 0
			}
			if v > maxVal {
				v = maxVal
			}
			u := uint16(v)
			i := y*img.Stride + x*2
			img.Pix[i+0] = byte(u >> 8)
			img.Pix[i+1] = byte(u & 0xFF)
		}
	}
	return img, nil
}

// toImageRGB converts three reconstructed component planes (R, G, B) to an RGB
// image. When every component is ≤8 bits the result is image.NRGBA; if any
// component exceeds 8 bits it is image.NRGBA64 (each sample stored as its raw
// 0..2^P-1 value in the 16-bit channel, matching the grayscale Gray16 path).
func (d *Decoder) toImageRGB(planes []engine.ComponentPlane) (image.Image, error) {
	if len(planes) < 3 {
		return nil, fmt.Errorf("toImageRGB: expected at least 3 components, got %d", len(planes))
	}
	w, h := planes[0].W, planes[0].H

	// DC level shift: decoded components are in the signed domain (the encoder
	// subtracted 2^(P-1) from each unsigned component before the color/wavelet
	// transforms). Add it back per component after the inverse RCT. Each component
	// is clamped to its own precision range.
	offsets := [3]int32{}
	maxVals := [3]int32{}
	deep := false
	for c := 0; c < 3; c++ {
		comp := d.header.siz.Components[planes[c].Comp]
		offsets[c] = 1 << uint(comp.Precision-1)
		maxVals[c] = int32(1)<<uint(comp.Precision) - 1
		if comp.Precision > 8 {
			deep = true
		}
	}

	if deep {
		img := image.NewNRGBA64(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				idx := y*w + x
				img.SetNRGBA64(x, y, color.NRGBA64{
					R: uint16(clamp(planes[0].Pix[idx]+offsets[0], 0, maxVals[0])),
					G: uint16(clamp(planes[1].Pix[idx]+offsets[1], 0, maxVals[1])),
					B: uint16(clamp(planes[2].Pix[idx]+offsets[2], 0, maxVals[2])),
					A: 0xFFFF,
				})
			}
		}
		return img, nil
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(clamp(planes[0].Pix[idx]+offsets[0], 0, maxVals[0])),
				G: uint8(clamp(planes[1].Pix[idx]+offsets[1], 0, maxVals[1])),
				B: uint8(clamp(planes[2].Pix[idx]+offsets[2], 0, maxVals[2])),
				A: 255,
			})
		}
	}
	return img, nil
}

// toImageRGBA converts four reconstructed component planes (R, G, B, A) to an RGBA
// image: image.NRGBA when every component is ≤8 bits, otherwise image.NRGBA64. The
// fourth component is the alpha channel; like the colour channels it carries the DC
// level shift, so it is offset and clamped the same way.
func (d *Decoder) toImageRGBA(planes []engine.ComponentPlane) (image.Image, error) {
	if len(planes) < 4 {
		return nil, fmt.Errorf("toImageRGBA: expected 4 components, got %d", len(planes))
	}
	w, h := planes[0].W, planes[0].H

	offsets := [4]int32{}
	maxVals := [4]int32{}
	deep := false
	for c := 0; c < 4; c++ {
		comp := d.header.siz.Components[planes[c].Comp]
		offsets[c] = 1 << uint(comp.Precision-1)
		maxVals[c] = int32(1)<<uint(comp.Precision) - 1
		if comp.Precision > 8 {
			deep = true
		}
	}
	smp := func(c, idx int) int32 {
		return clamp(planes[c].Pix[idx]+offsets[c], 0, maxVals[c])
	}

	if deep {
		img := image.NewNRGBA64(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				idx := y*w + x
				img.SetNRGBA64(x, y, color.NRGBA64{
					R: uint16(smp(0, idx)), G: uint16(smp(1, idx)),
					B: uint16(smp(2, idx)), A: uint16(smp(3, idx)),
				})
			}
		}
		return img, nil
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(smp(0, idx)), G: uint8(smp(1, idx)),
				B: uint8(smp(2, idx)), A: uint8(smp(3, idx)),
			})
		}
	}
	return img, nil
}

// toImageGrayAlpha converts a two-component image (greyscale + alpha) to NRGBA /
// NRGBA64, replicating the grey channel into R/G/B. This is the conventional
// interpretation of a 2-component image (component 0 = luminance, component 1 =
// opacity), matching how OpenJPEG renders one. Both channels carry the DC level
// shift, so each is offset and clamped into its unsigned display range.
func (d *Decoder) toImageGrayAlpha(planes []engine.ComponentPlane) (image.Image, error) {
	if len(planes) < 2 {
		return nil, fmt.Errorf("toImageGrayAlpha: expected 2 components, got %d", len(planes))
	}
	w, h := planes[0].W, planes[0].H

	var offsets, maxVals [2]int32
	deep := false
	for c := 0; c < 2; c++ {
		comp := d.header.siz.Components[planes[c].Comp]
		offsets[c] = 1 << uint(comp.Precision-1)
		maxVals[c] = int32(1)<<uint(comp.Precision) - 1
		if comp.Precision > 8 {
			deep = true
		}
	}
	smp := func(c, idx int) int32 {
		return clamp(planes[c].Pix[idx]+offsets[c], 0, maxVals[c])
	}

	if deep {
		img := image.NewNRGBA64(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				idx := y*w + x
				g := uint16(smp(0, idx))
				img.SetNRGBA64(x, y, color.NRGBA64{R: g, G: g, B: g, A: uint16(smp(1, idx))})
			}
		}
		return img, nil
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			g := uint8(smp(0, idx))
			img.SetNRGBA(x, y, color.NRGBA{R: g, G: g, B: g, A: uint8(smp(1, idx))})
		}
	}
	return img, nil
}

// toImageSYCC converts three sYCC (Y, Cb, Cr) component planes to image.NRGBA via
// the inverse irreversible colour transform of IEC 61966-2-1, matching OpenJPEG's
// color.c sycc_to_rgb: with the chroma centred at 2^(P-1),
//
//	R = Y + ⌊1.402·Cr⌋,  G = Y − ⌊0.344·Cb + 0.714·Cr⌋,  B = Y + ⌊1.772·Cb⌋
//
// clamped to [0, 2^P−1]. (8-bit output; deeper sYCC is uncommon and not yet handled.)
func (d *Decoder) toImageSYCC(planes []engine.ComponentPlane) (image.Image, error) {
	w, h := planes[0].W, planes[0].H
	var off, mx [3]int32
	for c := 0; c < 3; c++ {
		comp := d.header.siz.Components[planes[c].Comp]
		off[c] = 1 << uint(comp.Precision-1)
		mx[c] = int32(1)<<uint(comp.Precision) - 1
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			yv := clamp(planes[0].Pix[idx]+off[0], 0, mx[0])
			cb := float64(clamp(planes[1].Pix[idx]+off[1], 0, mx[1]) - off[1])
			cr := float64(clamp(planes[2].Pix[idx]+off[2], 0, mx[2]) - off[2])
			r := clamp(yv+int32(1.402*cr), 0, mx[0])
			g := clamp(yv-int32(0.344*cb+0.714*cr), 0, mx[0])
			b := clamp(yv+int32(1.772*cb), 0, mx[0])
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
		}
	}
	return img, nil
}

// toImageESYCC converts three e-sYCC (Y, Cb, Cr) component planes (JP2 colr enum 24)
// to image.NRGBA, matching OpenJPEG's color_esycc_to_rgb. With the chroma centred at
// 2^(P-1):
//
//	R = Y − 0.0000368·Cb + 1.40199·Cr
//	G = 1.0003·Y − 0.344125·Cb − 0.7141128·Cr
//	B = 0.999823·Y + 1.77204·Cb − 0.000008·Cr
//
// computed in float32 with round-to-nearest and clamped to [0, 2^P−1]. (8-bit output,
// as for sYCC; deeper e-sYCC is uncommon and not yet handled.)
func (d *Decoder) toImageESYCC(planes []engine.ComponentPlane) (image.Image, error) {
	w, h := planes[0].W, planes[0].H
	var off, mx [3]int32
	for c := 0; c < 3; c++ {
		comp := d.header.siz.Components[planes[c].Comp]
		off[c] = 1 << uint(comp.Precision-1)
		mx[c] = int32(1)<<uint(comp.Precision) - 1
	}
	clampF := func(v float32, max int32) uint8 {
		i := int32(v + 0.5)
		return uint8(clamp(i, 0, max))
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			yv := float32(clamp(planes[0].Pix[idx]+off[0], 0, mx[0]))
			cb := float32(clamp(planes[1].Pix[idx]+off[1], 0, mx[1]) - off[1])
			cr := float32(clamp(planes[2].Pix[idx]+off[2], 0, mx[2]) - off[2])
			img.SetNRGBA(x, y, color.NRGBA{
				R: clampF(yv-0.0000368*cb+1.40199*cr, mx[0]),
				G: clampF(1.0003*yv-0.344125*cb-0.7141128*cr, mx[0]),
				B: clampF(0.999823*yv+1.77204*cb-0.000008*cr, mx[0]),
				A: 255,
			})
		}
	}
	return img, nil
}

// toImageCIELab converts three CIELab (L*, a*, b*) component planes (JP2 colr enum 14)
// to a 16-bit sRGB image (image.NRGBA64), matching OpenJPEG's lcms-based
// color_cielab_to_rgb. Each integer sample is mapped to its CIELab value using the
// colr range/offset parameters (or the CIELab defaults when none are present), then
// converted Lab(D50)→sRGB. OpenJPEG always emits 16-bit RGB here, so do we.
func (d *Decoder) toImageCIELab(planes []engine.ComponentPlane) (image.Image, error) {
	w, h := planes[0].W, planes[0].H

	var prec [3]int
	for c := 0; c < 3; c++ {
		prec[c] = d.header.siz.Components[planes[c].Comp].Precision
	}
	// Range (R) and offset (O) per channel: explicit from the colr box, else the
	// CIELab defaults (ISO 15444-1 / OpenJPEG: L in [0,100], a/b centred).
	var rl, ol, ra, oa, rb, ob float64
	if p := d.cielab; p != nil {
		rl, ol = float64(p.RL), float64(p.OL)
		ra, oa = float64(p.RA), float64(p.OA)
		rb, ob = float64(p.RB), float64(p.OB)
	} else {
		rl, ra, rb = 100, 170, 200
		ol = 0
		oa = math.Pow(2, float64(prec[1]-1))
		ob = math.Pow(2, float64(prec[2]-2)) + math.Pow(2, float64(prec[2]-3))
	}
	maxv := func(c int) float64 { return math.Pow(2, float64(prec[c])) - 1 }
	minL := -(rl * ol) / maxv(0)
	mina := -(ra * oa) / maxv(1)
	minb := -(rb * ob) / maxv(2)
	// Each component plane carries the signed (centred) sample; add 2^(P-1) to recover
	// the unsigned [0, 2^P-1] value the mapping above is defined over.
	off := [3]int32{int32(1) << uint(prec[0]-1), int32(1) << uint(prec[1]-1), int32(1) << uint(prec[2]-1)}

	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			sL := float64(planes[0].Pix[i] + off[0])
			sa := float64(planes[1].Pix[i] + off[1])
			sb := float64(planes[2].Pix[i] + off[2])
			l := minL + sL*rl/maxv(0)
			a := mina + sa*ra/maxv(1)
			b := minb + sb*rb/maxv(2)
			r, g, bl := labToSRGB16(l, a, b)
			img.SetNRGBA64(x, y, color.NRGBA64{R: r, G: g, B: bl, A: 0xFFFF})
		}
	}
	return img, nil
}

// toImageICC applies a matrix/TRC RGB ICC profile to three device-RGB component
// planes, producing sRGB. The output bit depth follows the input: NRGBA for ≤8-bit
// components, NRGBA64 for deeper ones (matching OpenJPEG, which keeps the input
// precision through the colour transform).
func (d *Decoder) toImageICC(planes []engine.ComponentPlane, tr *iccTransform) (image.Image, error) {
	w, h := planes[0].W, planes[0].H
	var off, mx [3]int32
	deep := false
	for c := 0; c < 3; c++ {
		comp := d.header.siz.Components[planes[c].Comp]
		off[c] = 1 << uint(comp.Precision-1)
		mx[c] = int32(1)<<uint(comp.Precision) - 1
		if comp.Precision > 8 {
			deep = true
		}
	}
	dev := func(c, idx int) float64 {
		v := clamp(planes[c].Pix[idx]+off[c], 0, mx[c])
		return float64(v) / float64(mx[c])
	}
	if deep {
		img := image.NewNRGBA64(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				r, g, b := tr.toSRGB16(dev(0, i), dev(1, i), dev(2, i))
				img.SetNRGBA64(x, y, color.NRGBA64{R: r, G: g, B: b, A: 0xFFFF})
			}
		}
		return img, nil
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			r, g, b := tr.toSRGB16(dev(0, i), dev(1, i), dev(2, i))
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255})
		}
	}
	return img, nil
}

// toImageCMYK converts four C, M, Y, K component planes (JP2 colr enum 12) to an
// 8-bit RGB image, matching OpenJPEG's color_cmyk_to_rgb: each channel is normalised
// to [0,1] over its own precision, inverted, and R=255·C·K, G=255·M·K, B=255·Y·K.
// (The result is always 8-bit, as in OpenJPEG.) Component samples are taken in the
// unsigned display domain (decoded value + 2^(P-1), clamped), the same convention as
// the other channel paths.
func (d *Decoder) toImageCMYK(planes []engine.ComponentPlane) (image.Image, error) {
	if len(planes) < 4 {
		return nil, fmt.Errorf("toImageCMYK: expected 4 components, got %d", len(planes))
	}
	w, h := planes[0].W, planes[0].H

	var offsets, maxVals [4]int32
	for c := 0; c < 4; c++ {
		comp := d.header.siz.Components[planes[c].Comp]
		offsets[c] = 1 << uint(comp.Precision-1)
		maxVals[c] = int32(1)<<uint(comp.Precision) - 1
	}
	// float32 throughout, matching OpenJPEG's color_cmyk_to_rgb exactly (it computes
	// in float and truncates toward zero), so the result is bit-identical to opj.
	norm := func(c, idx int) float32 {
		v := clamp(planes[c].Pix[idx]+offsets[c], 0, maxVals[c])
		return float32(v) / float32(maxVals[c])
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			cK := 1 - norm(3, idx)
			r := 1 - norm(0, idx)
			g := 1 - norm(1, idx)
			b := 1 - norm(2, idx)
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(255 * r * cK),
				G: uint8(255 * g * cK),
				B: uint8(255 * b * cK),
				A: 255,
			})
		}
	}
	return img, nil
}

// reorderByChannelDefs permutes the component planes into colour order using the JP2
// cdef channel definitions: the plane representing colour 1 (Asoc==1) comes first,
// colour 2 second, and so on, with opacity / unspecified channels appended after the
// colours. It returns planes unchanged when there is no cdef, when the cdef already
// describes the stream order, or when it does not form a usable colour permutation
// (the safe fallback — a malformed cdef must not corrupt the image).
func (d *Decoder) reorderByChannelDefs(planes []engine.ComponentPlane) []engine.ComponentPlane {
	if len(d.channelDefs) == 0 {
		return planes
	}
	// Map each plane by its component index for lookup by cdef.Comp.
	byComp := make(map[int]engine.ComponentPlane, len(planes))
	for _, p := range planes {
		byComp[p.Comp] = p
	}
	colours := make(map[int]engine.ComponentPlane) // Asoc (1-based) -> plane
	var others []engine.ComponentPlane
	maxAsoc := 0
	for _, cd := range d.channelDefs {
		p, ok := byComp[cd.Comp]
		if !ok {
			return planes // cdef references a component we don't have; bail out safely
		}
		if cd.Typ == 0 && cd.Asoc >= 1 && cd.Asoc <= len(planes) {
			if _, dup := colours[cd.Asoc]; dup {
				return planes // two channels claim the same colour; not a clean permutation
			}
			colours[cd.Asoc] = p
			if cd.Asoc > maxAsoc {
				maxAsoc = cd.Asoc
			}
		} else {
			others = append(others, p) // opacity / unspecified / whole-image
		}
	}
	// Every colour position 1..maxAsoc must be present and the colours must account for
	// all but the non-colour channels, otherwise fall back to the stream order.
	if maxAsoc == 0 || len(colours)+len(others) != len(planes) {
		return planes
	}
	out := make([]engine.ComponentPlane, 0, len(planes))
	for k := 1; k <= maxAsoc; k++ {
		p, ok := colours[k]
		if !ok {
			return planes // a colour position is missing
		}
		out = append(out, p)
	}
	out = append(out, others...)
	return out
}

func clamp(v, min, max int32) int32 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// ApplyRCT applies the Reversible Color Transform (RCT) to the component planes.
// RCT is typically used when Csiz >= 3.
func ApplyRCT(planes []engine.ComponentPlane) {
	// RCT (Reversible Color Transform), ISO/IEC 15444-1 Annex G.1.2.
	// Forward:
	//   Y0 = floor((R + 2G + B) / 4)
	//   Y1 = B - G   (component 1)
	//   Y2 = R - G   (component 2)
	// Inverse:
	//   G = Y0 - floor((Y1 + Y2) / 4)
	//   R = Y2 + G
	//   B = Y1 + G
	// Mapping: planes[0]=Y0, planes[1]=Y1 (Cb, B-G), planes[2]=Y2 (Cr, R-G)
	Y0 := planes[0].Pix
	Y1 := planes[1].Pix
	Y2 := planes[2].Pix
	// RCT requires the three components to share a size; bound the loop to the
	// shortest so a malformed stream cannot index out of range.
	n := len(Y0)
	if len(Y1) < n {
		n = len(Y1)
	}
	if len(Y2) < n {
		n = len(Y2)
	}
	for i := 0; i < n; i++ {
		y0 := Y0[i]
		y1 := Y1[i]
		y2 := Y2[i]

		g := y0 - ((y1 + y2) >> 2)
		r := y2 + g
		b := y1 + g

		Y0[i] = r
		Y1[i] = g
		Y2[i] = b
	}
}
