// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"
	"image"
	"image/color"
)

// maxOutputChannels caps how many output channels a palette expansion may generate
// (one full w×h int32 plane each). It mirrors box.maxCMapChannels — a device colorspace
// needs at most four channels plus opacity/aux — and bounds applyPalette independently
// of how the cmap reached the decoder.
const maxOutputChannels = 32

// CMapEntry maps one output channel to a codestream component, either directly
// (Type 0) or through a palette column (Type 1). It mirrors the JP2 `cmap` box.
type CMapEntry struct {
	Component int
	Type      int // 0 = direct, 1 = palette lookup
	Column    int
}

// SetPalette installs a JP2 palette (`pclr`) and component mapping (`cmap`) so an
// indexed image is expanded to its generated channels at reconstruction. entries
// is [NumEntries][NumColumns]; depths is the per-column bit depth.
func (d *Decoder) SetPalette(entries [][]int32, depths []int, cmap []CMapEntry) {
	d.paletteEntries = entries
	d.paletteDepths = depths
	d.cmap = cmap
}

// applyPalette expands the decoded component planes through the palette/cmap into
// one output plane per `cmap` entry, returning the planes and their bit depths.
// Each output channel is either a direct copy of a component sample or a palette
// lookup indexed by a component sample. Component samples are read in the unsigned
// display domain (decoded value + 2^(P-1)), which is the palette index.
func (d *Decoder) applyPalette(fullPlanes [][]int32, w, h int) ([][]int32, []int, error) {
	// Every cmap entry allocates a full w×h plane below, so bound the channel count
	// before the loop regardless of how d.cmap was populated. A real colorspace needs
	// at most four channels plus opacity/aux; a longer cmap is malformed and refused
	// rather than truncated (which would mis-render).
	if len(d.cmap) > maxOutputChannels {
		return nil, nil, fmt.Errorf("palette: cmap defines %d output channels (max %d)", len(d.cmap), maxOutputChannels)
	}
	ne := len(d.paletteEntries)
	out := make([][]int32, len(d.cmap))
	depths := make([]int, len(d.cmap))
	for j, cm := range d.cmap {
		if cm.Component < 0 || cm.Component >= len(fullPlanes) {
			return nil, nil, fmt.Errorf("palette: cmap component %d out of range", cm.Component)
		}
		comp := d.header.siz.Components[cm.Component]
		offset := int32(1) << uint(comp.Precision-1)
		maxIdx := int32(1)<<uint(comp.Precision) - 1
		src := fullPlanes[cm.Component]
		plane := make([]int32, w*h)
		switch cm.Type {
		case 0: // direct copy of the component sample
			depths[j] = comp.Precision
			addClampPlaneFn(plane, src, offset, 0, maxIdx)
		case 1: // palette lookup
			if cm.Column < 0 {
				return nil, nil, fmt.Errorf("palette: cmap column %d invalid", cm.Column)
			}
			if cm.Column < len(d.paletteDepths) {
				depths[j] = d.paletteDepths[cm.Column]
			} else {
				depths[j] = 8
			}
			for i := range plane {
				idx := clamp(src[i]+offset, 0, maxIdx)
				if int(idx) >= ne {
					idx = int32(ne - 1)
				}
				row := d.paletteEntries[idx]
				if cm.Column < len(row) {
					plane[i] = row[cm.Column]
				}
			}
		default:
			return nil, nil, fmt.Errorf("palette: unsupported cmap type %d", cm.Type)
		}
		out[j] = plane
	}
	return out, depths, nil
}

// addClampPlaneScalar is the direct-copy channel's per-sample loop, split out so it can be the default target of
// addClampPlaneFn (see simd_dispatch.go). src is indexed over the length of dst, so a src shorter than dst panics
// here exactly as the inline loop did.
func addClampPlaneScalar(dst, src []int32, offset, lo, hi int32) {
	for i := range dst {
		dst[i] = clamp(src[i]+offset, lo, hi)
	}
}

// toImagePalette builds the final image from palette-expanded planes (values are
// already in the unsigned display domain — no level shift). 1 channel → gray,
// 3 → RGB, 4 → RGBA; NRGBA64/Gray16 when any channel exceeds 8 bits.
func (d *Decoder) toImagePalette(planes [][]int32, depths []int, w, h int) (image.Image, error) {
	deep := false
	for _, b := range depths {
		if b > 8 {
			deep = true
		}
	}
	mx := make([]int32, len(depths))
	for i, b := range depths {
		mx[i] = int32(1)<<uint(b) - 1
	}
	at := func(c, i int) int32 { return clamp(planes[c][i], 0, mx[c]) }

	switch len(planes) {
	case 1:
		if deep {
			img := image.NewGray16(image.Rect(0, 0, w, h))
			for i := 0; i < w*h; i++ {
				u := uint16(at(0, i))
				img.Pix[i*2] = byte(u >> 8)
				img.Pix[i*2+1] = byte(u)
			}
			return img, nil
		}
		img := image.NewGray(image.Rect(0, 0, w, h))
		for i := 0; i < w*h; i++ {
			img.Pix[i] = uint8(at(0, i))
		}
		return img, nil
	case 3:
		if deep {
			img := image.NewNRGBA64(image.Rect(0, 0, w, h))
			for i := 0; i < w*h; i++ {
				img.SetNRGBA64(i%w, i/w, color.NRGBA64{
					R: uint16(at(0, i)), G: uint16(at(1, i)), B: uint16(at(2, i)), A: 0xFFFF})
			}
			return img, nil
		}
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < w*h; i++ {
			img.SetNRGBA(i%w, i/w, color.NRGBA{
				R: uint8(at(0, i)), G: uint8(at(1, i)), B: uint8(at(2, i)), A: 255})
		}
		return img, nil
	case 4:
		if deep {
			img := image.NewNRGBA64(image.Rect(0, 0, w, h))
			for i := 0; i < w*h; i++ {
				img.SetNRGBA64(i%w, i/w, color.NRGBA64{
					R: uint16(at(0, i)), G: uint16(at(1, i)), B: uint16(at(2, i)), A: uint16(at(3, i))})
			}
			return img, nil
		}
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < w*h; i++ {
			img.SetNRGBA(i%w, i/w, color.NRGBA{
				R: uint8(at(0, i)), G: uint8(at(1, i)), B: uint8(at(2, i)), A: uint8(at(3, i))})
		}
		return img, nil
	}
	return nil, fmt.Errorf("palette: unsupported output channel count %d", len(planes))
}
