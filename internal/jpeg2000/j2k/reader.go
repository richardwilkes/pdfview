// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

// Package j2k decodes and encodes raw JPEG 2000 codestreams (.j2k / .jpc / .j2c),
// the ISO/IEC 15444-1 (Part 1) entropy-coded data without a JP2 container.
//
// Decode and DecodeConfig expose the same functionality directly, and
// DecodeComponents returns the raw per-component sample planes for any component
// count (including subsampled or non-RGB images). Encode and EncodeWithOptions
// write a codestream from an image.Image, and EncodeComponents from explicit
// component planes; EncodeOptions selects lossless 5/3 or lossy 9/7, rate control,
// quality layers, tiling, precincts, progression order and code-block styles.
//
// For the JP2 file format (the boxed container around a codestream) see the
// sibling jp2 package.
package j2k

import (
	"fmt"
	"image"
	"io"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/codestream"
)

// Decode decodes a JPEG 2000 raw codestream (.j2k) from r and returns it as an image.Image.
//
// Like the standard library's image decoders, Decode allocates output buffers
// proportional to the image's declared dimensions and imposes no maximum size; only
// integer overflow of the allocation length is rejected. When decoding untrusted
// input, read the dimensions cheaply with [DecodeConfig] and decide whether to
// proceed before calling Decode — a hostile header declaring a very large image will
// attempt the full allocation and can exhaust memory.
func Decode(r io.Reader) (image.Image, error) {
	var d codestream.Decoder
	return d.Decode(r, false)
}

// Options configures a decode.
type Options struct {
	// ReduceResolutions discards this many of the highest resolution levels,
	// producing a smaller image (each dimension divided by 2^ReduceResolutions).
	// 0 decodes at full resolution. Useful for fast thumbnails — JPEG 2000 can
	// reconstruct a reduced resolution without the finest detail.
	ReduceResolutions int

	// MaxLayer caps the number of quality layers contributed to the decode, for a
	// lower-quality / progressive result. 0 decodes all layers.
	MaxLayer int
}

// DecodeWithOptions decodes a JPEG 2000 raw codestream (.j2k) with the given
// options, returning it as an image.Image.
func DecodeWithOptions(r io.Reader, opts Options) (image.Image, error) {
	var d codestream.Decoder
	d.SetReduce(opts.ReduceResolutions)
	d.SetMaxLayer(opts.MaxLayer)
	return d.Decode(r, false)
}

// Component is one decoded component at its native (subsampled, reduced)
// resolution — see codestream.Component. Samples are row-major (length W*H) in the
// signed sample domain.
type Component = codestream.DecodedComponent

// DecodeComponents decodes a JPEG 2000 raw codestream into its individual
// components at native resolution, without mapping to an image.Image. Use this for
// codestreams the image.Image path cannot represent — any component count other
// than 1, 3 or 4 (e.g. 2-channel, CMYK, or multispectral imagery) — or when the raw
// per-component samples are wanted directly. Options (reduce / max layer) apply.
func DecodeComponents(r io.Reader, opts Options) ([]Component, error) {
	var d codestream.Decoder
	d.SetReduce(opts.ReduceResolutions)
	d.SetMaxLayer(opts.MaxLayer)
	d.SetComponentsOnly(true)
	if _, err := d.Decode(r, false); err != nil {
		return nil, err
	}
	return d.Components(), nil
}

// DecodeConfig returns the color model and dimensions of a JPEG 2000 raw codestream.
func DecodeConfig(r io.Reader) (image.Config, error) {
	var d codestream.Decoder

	if _, err := d.Decode(r, true); err != nil {
		return image.Config{}, err
	}

	cm, err := d.GetColorModel()
	if err != nil {
		return image.Config{}, fmt.Errorf("j2k: %w", err)
	}

	return image.Config{
		ColorModel: cm,
		Width:      d.GetWidth(),
		Height:     d.GetHeight(),
	}, nil
}
