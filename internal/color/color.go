// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package color implements the PDF color spaces (ISO 32000-2 8.6) the engine needs: the device spaces (Gray/RGB/CMYK,
// matched byte for byte to the oracle's ICC-backed conversions; see convert.go), CalGray/CalRGB (approximated by their
// device analogs), ICCBased (mapped to the device space with the same component count), Indexed, and Separation/DeviceN
// with their tint transforms (internal/function). Every space converts to rendered RGB via ToNRGBA. Lab is unsupported.
package color

import (
	"errors"
	"image/color"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/function"
)

// maxSpaceDepth caps color-space nesting (Indexed bases, ICC and Separation alternates) so a self-referential space
// terminates.
const maxSpaceDepth = 8

// maxComponents caps DeviceN component counts; the standard's own limit is 32.
const maxComponents = 32

var (
	errUnsupportedSpace = errors.New("unsupported color space")
	errBadSpace         = errors.New("malformed color space")
	errTooDeep          = errors.New("color spaces nested too deeply")
)

// Space is one color space: it knows its component count, its initial color (ISO 32000-2 8.6.3: the color a cs/CS
// operator resets to), and how to convert component values to the rendered RGB space.
type Space interface {
	// NComponents returns the number of components a color in this space carries.
	NComponents() int
	// Initial returns the initial color components for this space.
	Initial() []float32
	// ToNRGBA converts components to the rendered color. Component values are clamped as needed; missing components
	// read as 0. The result always has A=255 except for spaces that never mark (Separation /None).
	ToNRGBA(comps []float32) color.NRGBA
}

// DeviceGray is the DeviceGray color space (also used for CalGray).
var DeviceGray Space = deviceGray{}

// DeviceRGB is the DeviceRGB color space (also used for CalRGB).
var DeviceRGB Space = deviceRGB{}

// DeviceCMYK is the DeviceCMYK color space.
var DeviceCMYK Space = deviceCMYK{}

type deviceGray struct{}

func (deviceGray) NComponents() int   { return 1 }
func (deviceGray) Initial() []float32 { return []float32{0} }
func (deviceGray) ToNRGBA(comps []float32) color.NRGBA {
	return grayToNRGBA(comp(comps, 0))
}

type deviceRGB struct{}

func (deviceRGB) NComponents() int   { return 3 }
func (deviceRGB) Initial() []float32 { return []float32{0, 0, 0} }
func (deviceRGB) ToNRGBA(comps []float32) color.NRGBA {
	return color.NRGBA{R: rgbByte(comp(comps, 0)), G: rgbByte(comp(comps, 1)), B: rgbByte(comp(comps, 2)), A: 255}
}

type deviceCMYK struct{}

func (deviceCMYK) NComponents() int   { return 4 }
func (deviceCMYK) Initial() []float32 { return []float32{0, 0, 0, 1} }
func (deviceCMYK) ToNRGBA(comps []float32) color.NRGBA {
	return cmykToNRGBA(comp(comps, 0), comp(comps, 1), comp(comps, 2), comp(comps, 3))
}

// comp returns comps[i], or 0 when absent.
func comp(comps []float32, i int) float32 {
	if i < len(comps) {
		return comps[i]
	}
	return 0
}

// Indexed is an /Indexed color space: one index component selecting base-space components from a table. The palette is
// resolved to device colors at parse time (hival caps it at 256 entries), so a lookup is one indexed read with no
// allocation and no base-space conversion; a /Separation or /DeviceN base would otherwise re-run its tint transform per
// pixel of a 16-bpc image, which internal/imaging's single-component LUT does not cover.
type Indexed struct {
	palette []color.NRGBA
}

// NComponents implements Space.
func (x *Indexed) NComponents() int { return 1 }

// Initial implements Space.
func (x *Indexed) Initial() []float32 { return []float32{0} }

// ToNRGBA implements Space. The index is truncated to an integer and clamped to [0, hival].
func (x *Indexed) ToNRGBA(comps []float32) color.NRGBA {
	return x.palette[clampIndex(comp(comps, 0), len(x.palette)-1)]
}

// buildPalette resolves every index in [0, hival] to its device color. Table bytes past the end of lookup read as 0.
func buildPalette(base Space, lookup []byte, hival int) []color.NRGBA {
	n := base.NComponents()
	palette := make([]color.NRGBA, hival+1)
	for idx := range palette {
		// A fresh slice per entry: the base may hand it to a tint transform that retains it.
		baseComps := make([]float32, n)
		for j := range n {
			if off := idx*n + j; off < len(lookup) {
				baseComps[j] = float32(lookup[off]) / 255
			}
		}
		palette[idx] = base.ToNRGBA(baseComps)
	}
	return palette
}

// clampIndex truncates a palette index to an int in [0, maxV]. The bounds are applied in float space because Go leaves
// an out-of-range float→int conversion implementation-defined and the platforms disagree: arm64 saturates (+Inf →
// math.MaxInt64) while amd64 wraps (+Inf → math.MinInt64), so an int-space clamp would send one index to opposite ends
// of the palette on the two architectures. A non-finite index is reachable: a /Separation or /DeviceN with an /Indexed
// alternate and a type-2 tint transform without /Range hands the raw math.Pow result (possibly ±Inf or NaN) to ToNRGBA,
// as do shading.parseGradient and shading.parseFunctionBased.
func clampIndex(v float32, maxV int) int {
	if !(v > 0) { // Also catches NaN.
		return 0
	}
	if v >= float32(maxV) { // Also catches +Inf.
		return maxV
	}
	return int(v)
}

// Separation is a /Separation or /DeviceN color space: tint components transformed into an alternate space. A
// /Separation /None space never marks the page; its ToNRGBA reports a fully transparent color, which paints nothing
// under the normal blend mode.
type Separation struct {
	alt  Space
	tint function.Func
	n    int
	none bool
}

// NComponents implements Space.
func (s *Separation) NComponents() int { return s.n }

// Initial implements Space.
func (s *Separation) Initial() []float32 {
	out := make([]float32, s.n)
	for i := range out {
		out[i] = 1
	}
	return out
}

// ToNRGBA implements Space.
func (s *Separation) ToNRGBA(comps []float32) color.NRGBA {
	if s.none {
		return color.NRGBA{}
	}
	return s.alt.ToNRGBA(s.tint.Eval(comps))
}

// RunsFunction reports whether converting one color through s evaluates a PDF function. Only a marking /Separation or
// /DeviceN does; every other space converts with fixed arithmetic or a palette resolved at parse time. Bulk callers (a
// mesh shading converts one color per vertex or patch corner, from a count the stream declares) use this to decide
// whether their loop needs its own budget.
func RunsFunction(s Space) bool {
	sep, ok := s.(*Separation)
	return ok && !sep.none
}

// Pattern is the /Pattern color space. Painting with it selects a pattern resource rather than component values; the
// interpreter (internal/content) resolves the scn-selected pattern and skips paint operations while no pattern is
// selected.
type Pattern struct {
	// Base is the underlying space of an uncolored pattern space (/Pattern base), nil otherwise.
	Base Space
}

// NComponents implements Space. An uncolored pattern carries its base components; a colored one carries none (the
// operand list holds only the pattern name, which the interpreter consumes separately).
func (p *Pattern) NComponents() int {
	if p.Base != nil {
		return p.Base.NComponents()
	}
	return 0
}

// Initial implements Space.
func (p *Pattern) Initial() []float32 { return nil }

// ToNRGBA implements Space; a pattern has no intrinsic color, so this reports transparent (never marks).
func (p *Pattern) ToNRGBA([]float32) color.NRGBA { return color.NRGBA{} }

// Parse parses obj (a name or array, resolving references) as a color space. Unsupported kinds return an error; callers
// fall back per their own policy.
func Parse(d *cos.Document, obj cos.Object) (Space, error) {
	return parseSpace(d, obj, 0)
}

func parseSpace(d *cos.Document, obj cos.Object, depth int) (Space, error) {
	if depth > maxSpaceDepth {
		return nil, errTooDeep
	}
	switch v := d.Resolve(obj).(type) {
	case cos.Name:
		return spaceForName(v)
	case cos.Array:
		return parseSpaceArray(d, v, depth)
	default:
		return nil, errBadSpace
	}
}

func spaceForName(name cos.Name) (Space, error) {
	switch name {
	case "DeviceGray", "G", "CalGray":
		return DeviceGray, nil
	case "DeviceRGB", "RGB", "CalRGB":
		return DeviceRGB, nil
	case "DeviceCMYK", "CMYK":
		return DeviceCMYK, nil
	case "Pattern":
		return &Pattern{}, nil
	default:
		return nil, errUnsupportedSpace
	}
}

//nolint:gocyclo // A flat dispatch over the color-space families of ISO 32000-2 8.6.
func parseSpaceArray(d *cos.Document, arr cos.Array, depth int) (Space, error) {
	if len(arr) == 0 {
		return nil, errBadSpace
	}
	family, ok := cos.AsName(d.Resolve(arr[0]))
	if !ok {
		return nil, errBadSpace
	}
	switch family {
	case "DeviceGray", "G", "DeviceRGB", "RGB", "DeviceCMYK", "CMYK":
		return spaceForName(family)
	case "CalGray":
		return DeviceGray, nil
	case "CalRGB":
		return DeviceRGB, nil
	case "ICCBased":
		return parseICCBased(d, arr, depth)
	case "Indexed", "I":
		return parseIndexed(d, arr, depth)
	case "Separation", "DeviceN":
		return parseSeparation(d, arr, depth)
	case "Pattern":
		if len(arr) < 2 {
			return &Pattern{}, nil
		}
		base, err := parseSpace(d, arr[1], depth+1)
		if err != nil {
			return &Pattern{}, nil //nolint:nilerr // A broken base still leaves a usable colorless pattern space.
		}
		return &Pattern{Base: base}, nil
	default:
		return nil, errUnsupportedSpace
	}
}

// parseICCBased maps an ICC profile stream to the device space with its component count; the profile itself is not
// interpreted. /N is authoritative; /Alternate is used only when /N is absent or not 1, 3, or 4.
func parseICCBased(d *cos.Document, arr cos.Array, depth int) (Space, error) {
	if len(arr) < 2 {
		return nil, errBadSpace
	}
	dict, ok := cos.AsDict(d.Resolve(arr[1]))
	if !ok {
		return nil, errBadSpace
	}
	if n, hasN := cos.AsInt(d.Resolve(dict["N"])); hasN {
		switch n {
		case 1:
			return DeviceGray, nil
		case 3:
			return DeviceRGB, nil
		case 4:
			return DeviceCMYK, nil
		}
	}
	if alt := dict["Alternate"]; alt != nil {
		return parseSpace(d, alt, depth+1)
	}
	return nil, errBadSpace
}

func parseIndexed(d *cos.Document, arr cos.Array, depth int) (Space, error) {
	if len(arr) < 4 {
		return nil, errBadSpace
	}
	base, err := parseSpace(d, arr[1], depth+1)
	if err != nil {
		return nil, err
	}
	if _, isPattern := base.(*Pattern); isPattern {
		return nil, errBadSpace // Pattern cannot be an Indexed base (ISO 32000-2 8.6.6.3).
	}
	hival, ok := cos.AsInt(d.Resolve(arr[2]))
	if !ok {
		return nil, errBadSpace
	}
	// ISO 32000-2 8.6.6.3 caps /hival at 255, but a producer that miscounts a 256-entry palette writes 256, and rejecting
	// the space is far worse than clamping: the interpreter's fallback for an unresolvable space is DeviceGray, so every
	// sc/scn operand would then read as a gray level. MuPDF clamps too.
	hival = min(max(hival, 0), 255)
	var lookup []byte
	switch table := d.Resolve(arr[3]).(type) {
	case cos.String:
		lookup = []byte(table)
	case *cos.Stream:
		data, streamErr := d.StreamData(table)
		if streamErr != nil {
			return nil, errBadSpace
		}
		lookup = data
	default:
		return nil, errBadSpace
	}
	return &Indexed{palette: buildPalette(base, lookup, int(hival))}, nil
}

func parseSeparation(d *cos.Document, arr cos.Array, depth int) (Space, error) {
	if len(arr) < 3 {
		return nil, errBadSpace
	}
	n := 1
	var none bool
	switch names := d.Resolve(arr[1]).(type) {
	case cos.Name:
		none = names == "None"
	case cos.Array: // DeviceN
		n = len(names)
		if n < 1 || n > maxComponents {
			return nil, errBadSpace
		}
	default:
		return nil, errBadSpace
	}
	alt, err := parseSpace(d, arr[2], depth+1)
	if err != nil {
		return nil, err
	}
	if _, isPattern := alt.(*Pattern); isPattern {
		return nil, errBadSpace
	}
	sep := &Separation{alt: alt, n: n, none: none}
	if none {
		// The tint transform of a None separation is irrelevant — it never marks — so tolerate a broken one.
		sep.tint = nil
		if len(arr) >= 4 {
			sep.tint, _ = function.Parse(d, arr[3]) //nolint:errcheck // Unused for None; best-effort only.
		}
		return sep, nil
	}
	if len(arr) < 4 {
		return nil, errBadSpace
	}
	tint, err := function.Parse(d, arr[3])
	if err != nil {
		return nil, err
	}
	if tint.NOutputs() < alt.NComponents() {
		return nil, errBadSpace
	}
	sep.tint = tint
	return sep, nil
}
