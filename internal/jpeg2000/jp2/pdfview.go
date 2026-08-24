// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// This file is pdfview-authored (MPL-2.0), not upstream code. It adds the two container-level entry points the PDF
// image pipeline needs and that upstream never exposed: raw per-component planes from a JP2 (upstream's container path
// only reaches image.Image, which has already consumed the palette and color boxes) and a header-only report of the
// container metadata alongside the codestream's own component geometry.

package jp2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/box"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/codestream"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
)

// The container metadata types are aliased rather than mirrored so a caller needs to import only this package and j2k,
// and so a change to a box field cannot silently stop being carried across.
type (
	// ChannelDef is one entry of a `cdef` box; see [box.ChannelDef].
	ChannelDef = box.ChannelDef
	// Palette is a parsed `pclr` box; see [box.Palette].
	Palette = box.Palette
	// CMapEntry is one entry of a `cmap` box; see [box.CMapEntry].
	CMapEntry = box.CMapEntry
	// CIELabParams holds the explicit parameters of an enumerated CIELab `colr` box; see [box.CIELabParams].
	CIELabParams = box.CIELabParams
)

// maxComponents is the SIZ component-count ceiling of ISO/IEC 15444-1 Table A.9. Csiz is a 16-bit field, so a hostile
// header can declare far more than the standard allows; the per-component triples must also fit in the segment, which
// is what bounds the allocation below.
const maxComponents = 16384

// maxPrecision is the component precision ceiling of ISO/IEC 15444-1 Table A.11. It matches the codestream decoder's
// own limit, so a header this reports on is one that decoder will also accept.
const maxPrecision = 38

// sizFixedLen is the byte count of the SIZ marker segment from Lsiz through Csiz inclusive, before the per-component
// triples (ISO/IEC 15444-1 A.5.1).
const sizFixedLen = 38

// ComponentInfo describes one codestream component as the SIZ marker segment declares it. Every field comes from SIZ,
// not from any JP2 box: the container describes how to interpret the components, never their sample geometry.
type ComponentInfo struct {
	// Precision is the component's bits per sample, the low seven bits of Ssiz plus one. Always 1 through 38.
	Precision int
	// XRsiz and YRsiz are the component's horizontal and vertical subsampling factors relative to the reference grid.
	// Both are at least 1; a component's own width is ceil(Width/XRsiz).
	XRsiz, YRsiz int
	// Signed reports whether the samples are two's-complement signed, the top bit of Ssiz.
	Signed bool
}

// Info is a JP2 file's header: the codestream's own image geometry plus the container metadata that says how to
// interpret the decoded components. It is what a caller reads to decide whether, and how, to decode the pixels; nothing
// here costs an allocation proportional to the image.
//
// The color-interpretation fields are reported, never applied — see [DecodeComponents].
type Info struct {
	// Width and Height are the image's size on the reference grid, from SIZ: Xsiz-XOsiz by Ysiz-YOsiz. They are the
	// full-resolution dimensions, so a decode reducing resolution levels produces a smaller result.
	Width, Height int
	// EnumCS is the `colr` box enumerated color space (16 = sRGB, 17 = grayscale, 18 = sYCC), or -1 when the box gives
	// an ICC profile instead of an enumerated value.
	EnumCS int
	// Components describes each codestream component, from SIZ, in codestream order. Never empty.
	Components []ComponentInfo
	// ICCProfile is the raw profile of a `colr` box with METH==2, or nil when the color space is enumerated instead.
	ICCProfile []byte
	// Palette is the `pclr` box lookup table, or nil when the image is not indexed. An indexed image's codestream
	// carries palette indices, not color.
	Palette *Palette
	// CMap holds the `cmap` box entries, one per output channel, or nil when the box is absent. Each entry names the
	// component an output channel reads and whether it passes through a palette column.
	CMap []CMapEntry
	// Channels holds the `cdef` box entries, or nil when the box is absent. They give each channel its role (color,
	// opacity, premultiplied opacity) and the color it belongs to.
	Channels []ChannelDef
	// CIELab carries the explicit range and offset parameters of an enumerated CIELab `colr` box (EnumCS 14). It is nil
	// when the box is absent or is not CIELab, in which case the CIELab defaults apply.
	CIELab *CIELabParams
}

// DecodeComponents decodes a JP2 container into its individual codestream components at their native (subsampled,
// reduced) resolution, the container analog of [j2k.DecodeComponents]. Samples are in the signed domain described by
// [j2k.Component].
//
// It applies none of the container's color machinery — no palette expansion, no `cdef` channel reordering, no
// enumerated color-space conversion, no CIELab, no ICC profile. The planes come back as the codestream carries them,
// which is what the PDF image pipeline consumes: under an /Indexed override each sample is a palette index rather than
// a color, and a PDF /ColorSpace entry can displace the container's own space entirely. Read the metadata with
// [DecodeInfo] and apply whichever parts survive the PDF layer's rules; [Decode] and [DecodeWithOptions] give the
// container's own rendering as an image.Image.
//
// The codestream's own multiple-component transform is not container machinery and still applies, so the planes are
// post-transform.
//
// Like the rest of this package it sizes its buffers from the declared dimensions with no cap of its own, so a caller
// holding untrusted input should weigh [DecodeInfo] against its own budget first.
func DecodeComponents(r io.Reader, opts j2k.Options) ([]j2k.Component, error) {
	container, err := box.ParseJP2(r)
	if err != nil {
		return nil, fmt.Errorf("jp2: container error: %w", err)
	}
	var d codestream.Decoder
	d.SetReduce(opts.ReduceResolutions)
	d.SetMaxLayer(opts.MaxLayer)
	d.SetComponentsOnly(true)
	if _, err = d.Decode(bytes.NewReader(container.Codestream), false); err != nil {
		return nil, fmt.Errorf("jp2: codestream error: %w", err)
	}
	return d.Components(), nil
}

// DecodeInfo parses a JP2 container's boxes and its codestream's SIZ marker segment, and nothing else. No pixel is
// decoded and no coefficient buffer is allocated, so this is the cheap way to learn what a payload declares before
// committing to [DecodeComponents].
//
// It reads SIZ directly rather than running the codestream decoder's header pass, which allocates tile bookkeeping
// proportional to the declared tile grid. What it does allocate is bounded by the input: the container parse buffers
// the `jp2c` box body, and the per-component slice needs its triples to be present.
//
// The header is validated only far enough that every field returned is usable: a component with zero subsampling or
// an out-of-range precision is rejected, since a caller would divide by the one and size buffers from the other.
// Decode cost is not bounded here.
func DecodeInfo(r io.Reader) (*Info, error) {
	container, err := box.ParseJP2(r)
	if err != nil {
		return nil, fmt.Errorf("jp2: container error: %w", err)
	}
	info, err := parseSIZ(container.Codestream)
	if err != nil {
		return nil, err
	}
	info.EnumCS = container.EnumCS
	info.ICCProfile = container.ICCProfile
	info.Palette = container.Palette
	info.CMap = container.CMap
	info.Channels = container.Channels
	info.CIELab = container.CIELab
	return info, nil
}

// parseSIZ reads the image geometry and per-component declarations out of a codestream's SIZ marker segment. ISO/IEC
// 15444-1 A.5.1 places SIZ immediately after SOC; the reserved markers 0xFF30 through 0xFF3F carry no segment and are
// skipped between the two, as the codestream decoder skips them.
func parseSIZ(cs []byte) (*Info, error) {
	if len(cs) < 2 || cs[0] != 0xff || cs[1] != 0x4f {
		return nil, errors.New("jp2: codestream does not start with SOC")
	}
	off := 2
	for off+2 <= len(cs) && cs[off] == 0xff && cs[off+1] >= 0x30 && cs[off+1] <= 0x3f {
		off += 2
	}
	if off+4 > len(cs) || cs[off] != 0xff || cs[off+1] != 0x51 {
		return nil, errors.New("jp2: codestream has no SIZ marker segment")
	}
	seg := cs[off+2:]
	lsiz := int(binary.BigEndian.Uint16(seg))
	if lsiz < sizFixedLen || lsiz > len(seg) {
		return nil, fmt.Errorf("jp2: SIZ length %d does not fit the codestream", lsiz)
	}
	seg = seg[:lsiz]
	u32 := func(i int) int64 { return int64(binary.BigEndian.Uint32(seg[i : i+4])) }
	xsiz, ysiz := u32(4), u32(8)
	xosiz, yosiz := u32(12), u32(16)
	if xsiz <= xosiz || ysiz <= yosiz {
		return nil, fmt.Errorf("jp2: SIZ declares no image area: %dx%d at origin %d,%d", xsiz, ysiz, xosiz, yosiz)
	}
	csiz := int(binary.BigEndian.Uint16(seg[36:38]))
	if csiz < 1 || csiz > maxComponents {
		return nil, fmt.Errorf("jp2: SIZ component count %d out of range", csiz)
	}
	if sizFixedLen+3*csiz > lsiz {
		return nil, fmt.Errorf("jp2: SIZ length %d cannot hold %d components", lsiz, csiz)
	}
	comps := make([]ComponentInfo, csiz)
	for i := range comps {
		t := seg[sizFixedLen+3*i:]
		c := ComponentInfo{Precision: int(t[0]&0x7f) + 1, XRsiz: int(t[1]), YRsiz: int(t[2]), Signed: t[0]&0x80 != 0}
		if c.XRsiz < 1 || c.YRsiz < 1 {
			return nil, fmt.Errorf("jp2: component %d declares %dx%d subsampling", i, c.XRsiz, c.YRsiz)
		}
		if c.Precision > maxPrecision {
			return nil, fmt.Errorf("jp2: component %d declares %d bits per sample", i, c.Precision)
		}
		comps[i] = c
	}
	// Both differences are of 32-bit quantities and this repository is 64-bit only, so neither conversion truncates.
	return &Info{Width: int(xsiz - xosiz), Height: int(ysiz - yosiz), Components: comps}, nil
}
