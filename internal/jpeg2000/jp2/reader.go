// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

// Package jp2 decodes the JPEG 2000 JP2 file format (.jp2), the ISO/IEC 15444-1
// box container that wraps a raw codestream together with image metadata such as
// the colour space (an enumerated sRGB / grey / sYCC / CMYK value or an embedded
// ICC profile) and channel definitions.
//
// The package parses the JP2 boxes, applies the declared colour transform, and
// delegates the codestream itself to the j2k package.
package jp2

import (
	"bytes"
	"fmt"
	"image"
	"io"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/box"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/codestream"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// Decode decodes a JPEG 2000 image (.jp2) from r and returns it as an image.Image.
//
// Like the standard library's image decoders, Decode allocates output buffers
// proportional to the image's declared dimensions and imposes no maximum size; only
// integer overflow of the allocation length is rejected. When decoding untrusted
// input, read the dimensions cheaply with [DecodeConfig] and decide whether to
// proceed before calling Decode — a hostile header declaring a very large image will
// attempt the full allocation and can exhaust memory.
func Decode(r io.Reader) (image.Image, error) {
	return DecodeWithOptions(r, j2k.Options{})
}

// DecodeWithOptions decodes a JPEG 2000 image (.jp2) with the given options (see
// j2k.Options, e.g. reduced-resolution decode), returning it as an image.Image.
// The JP2 colr box is honoured: an sYCC image is converted to RGB.
func DecodeWithOptions(r io.Reader, opts j2k.Options) (image.Image, error) {
	info, err := box.ParseJP2(r)
	if err != nil {
		return nil, fmt.Errorf("jp2: container error: %w", err)
	}

	var d codestream.Decoder
	d.SetReduce(opts.ReduceResolutions)
	d.SetMaxLayer(opts.MaxLayer)
	if info.EnumCS > 0 {
		d.SetColorSpace(info.EnumCS)
	}
	if info.Palette != nil && len(info.CMap) > 0 {
		cmap := make([]codestream.CMapEntry, len(info.CMap))
		for i, e := range info.CMap {
			cmap[i] = codestream.CMapEntry{Component: e.Component, Type: e.Type, Column: e.Column}
		}
		d.SetPalette(info.Palette.Entries, info.Palette.BitDepths, cmap)
	}
	if len(info.Channels) > 0 {
		defs := make([]codestream.ChannelDef, len(info.Channels))
		for i, c := range info.Channels {
			defs[i] = codestream.ChannelDef{Comp: int(c.Cn), Typ: int(c.Typ), Asoc: int(c.Asoc)}
		}
		d.SetChannelDefs(defs)
	}
	if info.CIELab != nil {
		d.SetCIELab(&codestream.CIELabParams{
			RL: info.CIELab.RL, OL: info.CIELab.OL,
			RA: info.CIELab.RA, OA: info.CIELab.OA,
			RB: info.CIELab.RB, OB: info.CIELab.OB,
		})
	}
	if len(info.ICCProfile) > 0 {
		d.SetICCProfile(info.ICCProfile)
	}

	img, err := d.Decode(bytes.NewReader(info.Codestream), false)
	if err != nil {
		return nil, fmt.Errorf("jp2: codestream error: %w", err)
	}
	return img, nil
}

// DecodeConfig returns the color model and dimensions of a JPEG 2000 image (.jp2).
func DecodeConfig(r io.Reader) (image.Config, error) {
	info, err := box.ParseJP2(r)
	if err != nil {
		return image.Config{}, fmt.Errorf("jp2: config container error: %w", err)
	}

	var d codestream.Decoder
	if info.EnumCS > 0 {
		d.SetColorSpace(info.EnumCS)
	}
	if _, err := d.Decode(bytes.NewReader(info.Codestream), true); err != nil {
		return image.Config{}, fmt.Errorf("jp2: config codestream error: %w", err)
	}

	cm, err := d.GetColorModel()
	if err != nil {
		return image.Config{}, fmt.Errorf("jp2: %w", err)
	}
	return image.Config{
		ColorModel: cm,
		Width:      d.GetWidth(),
		Height:     d.GetHeight(),
	}, nil
}
