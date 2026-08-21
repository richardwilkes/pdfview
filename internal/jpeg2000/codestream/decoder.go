// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/binutil"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/engine"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/wavelet"
)

type Decoder struct {
	r io.Reader

	header header

	// sizSeen marks that the codestream's SIZ marker segment has been processed. ISO 15444-1 A.5.1 permits exactly
	// one SIZ, in the main header; because the parser reuses sectionMainHeader between tile-parts (see processSOD),
	// the section check alone would admit a second SIZ there, letting a hostile stream replace geometry the caller
	// validated via DecodeConfig before committing to a full decode (see PROVENANCE.md, allocation hardening).
	sizSeen bool

	tiles []tileState

	// reduce is the number of highest resolution levels to discard, producing a
	// smaller image (resolution Nd-reduce). 0 = full resolution.
	reduce int

	// maxLayer caps the number of quality layers contributed to the decode, for a
	// lower-quality / progressive result. 0 = all layers.
	maxLayer int

	// colorSpace is the JP2 colr enumerated colour space (16=sRGB, 17=greyscale,
	// 18=sYCC), or 0 when decoding a raw codestream (no colour-space info). Only
	// sYCC triggers a conversion (YCbCr→RGB); see the colour-space policy in
	// reconstruction.go.
	colorSpace int

	// JP2 palette (pclr) + component mapping (cmap): when set, the decoded index
	// component(s) are expanded through the palette at reconstruction. nil for a
	// non-indexed image. See palette.go and SetPalette.
	paletteEntries [][]int32
	paletteDepths  []int
	cmap           []CMapEntry

	// channelDefs holds the JP2 cdef channel definitions (Comp index, Typ, Asoc),
	// used to reorder the component planes into colour order before colour-space
	// conversion when an image stores its channels in a non-standard order. nil when
	// no cdef box is present (the components are then taken in stream order).
	channelDefs []ChannelDef

	// cielab holds explicit CIELab range/offset parameters (colr enum 14); nil = use
	// the CIELab defaults. iccProfile holds an embedded ICC profile (colr METH==2),
	// applied to the components at reconstruction; nil when the colour is enumerated.
	cielab     *CIELabParams
	iccProfile []byte

	// nativePlanes holds each component's decoded samples at its own native
	// (subsampled, reduced) resolution, in the signed sample domain (no colour
	// transform, no display level-shift). Populated by finalizeImage; read by the
	// generic N-component accessor. nativeW/nativeH are the per-component dimensions.
	nativePlanes [][]int32
	nativeW      []int
	nativeH      []int

	// ppmMerged is the cached continuous PPM packet-header stream (Nppm prefixes
	// stripped); ppmConsumed is the shared read offset advanced as each tile consumes
	// its packet headers from it. See ppmppt.go.
	ppmMerged   []byte
	ppmConsumed int

	// componentsOnly makes finalizeImage stop after assembling the native
	// per-component planes, skipping the image.Image reconstruction (which only
	// supports 1/3/4 components). Set via DecodeComponents for the generic
	// N-component path (CMYK, 2-channel, multispectral).
	componentsOnly bool

	section parseSection

	// tilePartTiles records the tile index of each tile-part in the order tile-parts
	// appear in the codestream. Used to map PPM's per-tile-part header runs (framed
	// by Nppm counts, in tile-part order) back to tiles.
	tilePartTiles []int

	cur struct {
		active bool

		sot SOT

		tileIndex int

		cod COD
		qcd QCD

		// coc/qcc hold per-component coding/quantization overrides (COC/QCC markers)
		// in effect for the current tile: seeded from the main header at SOT, then
		// overwritten by any tile-part COC/QCC. nil until the first override.
		coc map[int]COD
		qcc map[int]QCD

		// tilePartConsumed is the number of bytes consumed in the current tile-part,
		// from the SOT marker to the current position.
		tilePartConsumed int
	}
}

type parseSection int

const (
	sectionMainHeader parseSection = iota
	sectionTilePartHeader
	sectionTilePartData
)

type header struct {
	siz SIZ
	cod COD
	qcd QCD
	poc []POCTuple  // main-header progression-order changes (apply to all tiles)
	coc map[int]COD // main-header per-component coding overrides (COC)
	qcc map[int]QCD // main-header per-component quantization overrides (QCC)
	rgn map[int]int // main-header per-component ROI max-shift (RGN SPrgn)
	// ppm holds the concatenated PPM (packed packet headers, main header) payloads
	// in Zppm order. When non-empty, every tile's packet headers come from this
	// stream (split per tile-part by the leading Nppm byte counts), not the SOD.
	ppm []byte
}

// roiShiftFor returns the ROI max-shift (RGN SPrgn) in effect for component comp in
// the given tile: a tile-part RGN overrides a main-header RGN; 0 means no ROI.
func (d *Decoder) roiShiftFor(tileIndex, comp int) int {
	if tileIndex >= 0 && tileIndex < len(d.tiles) {
		if s, ok := d.tiles[tileIndex].rgn[comp]; ok {
			return s
		}
	}
	if s, ok := d.header.rgn[comp]; ok {
		return s
	}
	return 0
}

// codFor returns the effective coding parameters for component comp, most-specific
// first: tile-part COC, tile-part COD, main-header COC, main-header COD. The
// tile is the one currently being decoded (d.cur.tileIndex).
func (d *Decoder) codFor(comp int) COD {
	if t := d.curTile(); t != nil {
		if c, ok := t.tileCOC[comp]; ok {
			return c
		}
		if t.tileCOD != nil {
			return *t.tileCOD
		}
	}
	if c, ok := d.header.coc[comp]; ok {
		return c
	}
	return d.header.cod
}

// mainCodFor returns the effective main-header coding parameters for component
// comp (main-header COC override, else the main-header COD default), ignoring any
// per-tile state. It is used for whole-image decisions taken after all tiles are
// assembled — chiefly whether the reversible colour transform applies.
func (d *Decoder) mainCodFor(comp int) COD {
	if c, ok := d.header.coc[comp]; ok {
		return c
	}
	return d.header.cod
}

// qcdFor returns the effective quantization parameters for component comp,
// most-specific first: tile-part QCC, tile-part QCD, main-header QCC, main-header QCD.
func (d *Decoder) qcdFor(comp int) QCD {
	if t := d.curTile(); t != nil {
		if q, ok := t.tileQCC[comp]; ok {
			return q
		}
		if t.tileQCD != nil {
			return *t.tileQCD
		}
	}
	if q, ok := d.header.qcc[comp]; ok {
		return q
	}
	return d.header.qcd
}

// curTile returns the tile currently being decoded, or nil.
func (d *Decoder) curTile() *tileState {
	if d.cur.tileIndex >= 0 && d.cur.tileIndex < len(d.tiles) {
		return &d.tiles[d.cur.tileIndex]
	}
	return nil
}

// ReadMarker reads a 2-byte marker from the stream.
func (d *Decoder) readMarker() (uint16, error) {
	mk, err := binutil.ReadU16(d.r)
	if err != nil {
		return 0, err
	}
	return mk, nil
}

// skipSegment skips the rest of the current segment based on its length field.
func (d *Decoder) skipSegment() error {
	l, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	if l < 2 {
		return nil
	}

	if d.section == sectionTilePartHeader {
		// Count this marker and its marker segment toward the current tile-part size.
		d.cur.tilePartConsumed += 2 + int(l)
	}

	return binutil.DiscardN(d.r, int64(l-2))
}

func (d *Decoder) Decode(r io.Reader, configOnly bool) (image.Image, error) {
	d.r = r
	d.sizSeen = false

	// Expect SOC (Start of Codestream)
	mk, err := d.readMarker()
	if err != nil {
		return nil, err
	}
	if mk != 0xFF4F {
		return nil, fmt.Errorf("invalid codestream: expected SOC (0xFF4F), got 0x%04X", mk)
	}

	for {
		mk, err := d.readMarker()
		if err != nil {
			// A codestream that ends without an explicit EOC marker (or whose final
			// tile-part used Psot=0 and ran to the end of the data) leaves the reader at
			// EOF here. Many real-world encoders omit EOC; OpenJPEG tolerates it. If we
			// have already read tile data, treat EOF as end-of-codestream and finalize
			// rather than failing. With no tile data it is a genuinely truncated stream.
			if (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) && !configOnly && len(d.tilePartTiles) > 0 {
				return d.finalizeImage()
			}
			return nil, err
		}
		switch mk {
		case 0xFF51: // SIZ
			if err := d.processSIZ(); err != nil {
				return nil, err
			}
			d.initTiles()
		case 0xFF52: // COD
			if err := d.processCOD(); err != nil {
				return nil, err
			}
		case 0xFF53: // COC (coding style component – per-component override)
			if err := d.processCOC(); err != nil {
				return nil, err
			}
		case 0xFF5C: // QCD (optional)
			if err := d.processQCD(); err != nil {
				return nil, err
			}
		case 0xFF5D: // QCC (quantization component – per-component override)
			if err := d.processQCC(); err != nil {
				return nil, err
			}
		case 0xFF5E: // RGN (region of interest)
			if err := d.processRGN(); err != nil {
				return nil, err
			}
		case 0xFF5F: // POC (progression order change)
			if err := d.processPOC(); err != nil {
				return nil, err
			}
		case 0xFF30, 0xFF31, 0xFF32, 0xFF33, 0xFF34, 0xFF35, 0xFF36, 0xFF37,
			0xFF38, 0xFF39, 0xFF3A, 0xFF3B, 0xFF3C, 0xFF3D, 0xFF3E, 0xFF3F:
			// Reserved delimiter markers (ISO 15444-1 Table A-1, 0xFF30–0xFF3F) carry no
			// marker segment — there is no length field to read. They may appear between
			// other markers (some encoders emit one before SOT); skip the 2-byte marker.
			continue
		case 0xFF55, 0xFF58: // TLM / PLT (tile-part / packet length markers)
			// TLM (main header) and PLT (tile-part header) are purely informational
			// length indices: they let a reader seek to a tile-part or packet without
			// parsing, but carry no data needed to decode. We ignore them. (OpenJPEG's
			// CLI never writes them; Grok emits them with -X / -L.)
			if err := d.skipSegment(); err != nil {
				return nil, fmt.Errorf("TLM/PLT: %w", err)
			}
		case 0xFF63: // CRG (component registration)
			// CRG records sub-pixel registration offsets between components for display
			// alignment (ISO 15444-1 A.9.1). It does not affect the decoded sample values
			// — applying it would resample components to fractional positions, which
			// neither this decoder nor OpenJPEG's reconstruction does — so we skip it.
			if err := d.skipSegment(); err != nil {
				return nil, fmt.Errorf("CRG: %w", err)
			}
		case 0xFF60: // PPM (packed packet headers, main header)
			if err := d.processPPM(); err != nil {
				return nil, fmt.Errorf("PPM: %w", err)
			}
		case 0xFF61: // PPT (packed packet headers, tile-part header)
			if err := d.processPPT(); err != nil {
				return nil, fmt.Errorf("PPT: %w", err)
			}
		case 0xFF90: // SOT -> stop header parsing; position at SOT marker start
			if configOnly {
				return nil, nil
			}
			if err := d.processSOT(); err != nil {
				return nil, err
			}
		case 0xFF93: // SOD
			if err := d.processSOD(); err != nil {
				return nil, err
			}
		case 0xFFD9: // EOC
			if configOnly {
				return nil, nil
			}
			return d.finalizeImage()
		case 0xFF64: // COM (comment) – skip
			if err := d.skipSegment(); err != nil {
				return nil, err
			}
		default:
			if err := d.skipSegment(); err != nil {
				return nil, fmt.Errorf("unsupported or malformed marker 0x%04X: %w", mk, err)
			}
		}
	}
}

func (d *Decoder) finalizeImage() (image.Image, error) {
	// Guard against a missing or malformed SIZ (e.g. EOC reached before any SIZ
	// marker), which would otherwise divide by zero or allocate an empty image.
	if err := d.header.siz.validate(); err != nil {
		return nil, fmt.Errorf("SIZ: %w", err)
	}

	// Reduced-resolution decode: discard `reduce` highest resolution levels, so the
	// output is the image at resolution Nd-reduce (each dimension divided by
	// 2^reduce). reduce=0 is a full-resolution decode.
	Nd := d.header.cod.Levels
	reduce := d.reduce
	if reduce < 0 {
		reduce = 0
	}
	if reduce > Nd {
		reduce = Nd
	}
	// The decoded image is the reference-grid region [X0,Xsiz)×[Y0,Ysiz) — a non-zero
	// image origin (XOsiz/YOsiz) means the canvas is larger than the image. Output
	// dimensions and tile placement are taken relative to that origin, at the (reduced)
	// resolution: a reference-grid coordinate c maps to ceil(c/2^reduce).
	x0r := engine.CeilDivPow2(d.header.siz.X0, reduce)
	y0r := engine.CeilDivPow2(d.header.siz.Y0, reduce)
	outW := engine.CeilDivPow2(d.header.siz.Xsiz, reduce) - x0r
	outH := engine.CeilDivPow2(d.header.siz.Ysiz, reduce) - y0r

	// Prepare component planes for the (possibly reduced) output image.
	fullPlanes := make([][]int32, len(d.header.siz.Components))
	for c := range d.header.siz.Components {
		fullPlanes[c] = make([]int32, outW*outH)
	}

	// Native per-component planes: each component at its own (subsampled, reduced)
	// resolution, assembled WITHOUT the reference-grid upsampling the image path
	// applies. These back the generic N-component accessor (DecodeNativePlanes) and
	// preserve the exact decoded samples for components the image.Image path cannot
	// represent (e.g. 2 or >4 components).
	ncw := make([]int, len(d.header.siz.Components))
	nch := make([]int, len(d.header.siz.Components))
	ncx0 := make([]int, len(d.header.siz.Components))
	ncy0 := make([]int, len(d.header.siz.Components))
	nativePlanes := make([][]int32, len(d.header.siz.Components))
	for c, comp := range d.header.siz.Components {
		ncx0[c] = engine.CeilDivPow2(ceilDiv(d.header.siz.X0, comp.XRsiz), reduce)
		ncy0[c] = engine.CeilDivPow2(ceilDiv(d.header.siz.Y0, comp.YRsiz), reduce)
		ncw[c] = engine.CeilDivPow2(ceilDiv(d.header.siz.Xsiz, comp.XRsiz), reduce) - ncx0[c]
		nch[c] = engine.CeilDivPow2(ceilDiv(d.header.siz.Ysiz, comp.YRsiz), reduce) - ncy0[c]
		nativePlanes[c] = make([]int32, ncw[c]*nch[c])
	}

	numTilesX, numTilesY := d.header.siz.NumTiles()
	if len(d.tiles) != numTilesX*numTilesY {
		return nil, fmt.Errorf("tile count mismatch: got %d, want %d", len(d.tiles), numTilesX*numTilesY)
	}

	compPrecisions := make([]int, len(d.header.siz.Components))
	compSigned := make([]bool, len(d.header.siz.Components))
	compXRsiz := make([]int, len(d.header.siz.Components))
	compYRsiz := make([]int, len(d.header.siz.Components))
	for i, c := range d.header.siz.Components {
		compPrecisions[i] = c.Precision
		compSigned[i] = c.Signed
		compXRsiz[i] = c.XRsiz
		compYRsiz[i] = c.YRsiz
	}

	for tileIndex := range d.tiles {
		tile := &d.tiles[tileIndex]

		// Parse this tile's reassembled tile-part payload into code-blocks.
		if err := d.parseTilePayload(tileIndex); err != nil {
			return nil, err
		}

		tw, th := tile.tw, tile.th

		// Collect per-component QCD/QCC guard bits and step exponents so a QCC
		// marker (per-component quantization) takes effect for its component only.
		numCompsTier1 := len(d.header.siz.Components)
		guardBits := make([]int, numCompsTier1)
		stepExpns := make([][]int, numCompsTier1)
		cblkStyles := make([]int, numCompsTier1)
		roiShifts := make([]int, numCompsTier1)
		for comp := 0; comp < numCompsTier1; comp++ {
			q := d.qcdFor(comp)
			guardBits[comp] = q.GuardBits
			// Expand the per-subband step exponents (derived quantization signals only
			// ε_0) so Tier-1 can compute each subband's magnitude bit-count.
			stepExpns[comp] = q.stepExpnsFor(d.codFor(comp).Levels)
			cblkStyles[comp] = d.codFor(comp).CblkStyle
			roiShifts[comp] = d.roiShiftFor(tileIndex, comp)
		}

		// Per-component decomposition levels: a COC marker may give a component a
		// different DWT depth than the COD default.
		compLevels := make([]int, numCompsTier1)
		for comp := 0; comp < numCompsTier1; comp++ {
			compLevels[comp] = d.codFor(comp).Levels
		}

		decodedBlocks, err := engine.DecodeTier1(
			compLevels,
			tw,
			th,
			compPrecisions,
			compSigned,
			compXRsiz,
			compYRsiz,
			tile.blocks,
			guardBits,
			stepExpns,
			cblkStyles,
			roiShifts,
		)
		if err != nil {
			return nil, fmt.Errorf("tile %d: tier-1 decode failed: %w", tileIndex, err)
		}

		numComps := len(d.header.siz.Components)

		gatherCoeffs := func(comp int) ([]engine.CodeBlock, error) {
			var coeffs []engine.CodeBlock
			for _, cb := range decodedBlocks {
				if cb.Comp == comp {
					coeffs = append(coeffs, cb)
				}
			}
			if len(coeffs) == 0 {
				return nil, fmt.Errorf("tile %d: no codeblocks for comp %d", tileIndex, comp)
			}
			return coeffs, nil
		}

		// Each component is reconstructed with its own wavelet transform and
		// decomposition depth: a COC marker can make one component reversible (5/3)
		// and another irreversible (9/7), or give them different level counts.
		// Reversible components reconstruct directly to integer planes (the reversible
		// colour transform, if any, is applied later on the assembled planes).
		// Irreversible components keep their inverse-DWT output in float so the
		// irreversible colour transform (ICT) can run before rounding.
		tilePlanes := make([]engine.ComponentPlane, numComps)
		floatBands := make([]wavelet.BandF, numComps)
		compRev := make([]bool, numComps)
		for comp := 0; comp < numComps; comp++ {
			coeffs, err := gatherCoeffs(comp)
			if err != nil {
				return nil, err
			}
			cod := d.codFor(comp)
			compRev[comp] = cod.Reversible
			tcx0 := ceilDiv(tile.tx0, compXRsiz[comp])
			tcy0 := ceilDiv(tile.ty0, compYRsiz[comp])
			tcx1 := ceilDiv(tile.tx1, compXRsiz[comp])
			tcy1 := ceilDiv(tile.ty1, compYRsiz[comp])
			if cod.Reversible {
				plane, err := engine.InverseDWT53(cod.Levels, coeffs, tcx0, tcy0, tcx1, tcy1, reduce)
				if err != nil {
					return nil, fmt.Errorf("tile %d comp %d: inverse DWT failed: %w", tileIndex, comp, err)
				}
				tilePlanes[comp] = plane
			} else {
				band, err := d.inverseDWT97(d.qcdFor(comp), cod.Levels, coeffs, compPrecisions[comp], tcx0, tcy0, tcx1, tcy1, reduce)
				if err != nil {
					return nil, fmt.Errorf("tile %d comp %d: inverse DWT failed: %w", tileIndex, comp, err)
				}
				floatBands[comp] = band
			}
		}
		// ICT is defined on the first three components and only when they share the
		// irreversible transform (a mixed-transform image carries no colour transform).
		if numComps >= 3 && d.header.cod.UseMCT && !compRev[0] && !compRev[1] && !compRev[2] {
			applyICTFloat(floatBands[:3])
		}
		// Round the irreversible components (after any ICT) into integer planes.
		for comp := 0; comp < numComps; comp++ {
			if !compRev[comp] {
				tilePlanes[comp] = roundBandF(comp, floatBands[comp])
			}
		}

		// Write each tile-component plane into the full-resolution reference grid.
		// A subsampled component (XRsiz/YRsiz > 1) is decoded at reduced size and is
		// upsampled here by sample replication (nearest-neighbour): sample (x,y)
		// fills the XRsiz×YRsiz reference cell at (x·XRsiz, y·YRsiz). Components with
		// no subsampling (the common case) reduce to a plain copy.
		// Tile origin in the (reduced) output grid, relative to the image origin.
		tox := engine.CeilDivPow2(tile.tx0, reduce) - x0r
		toy := engine.CeilDivPow2(tile.ty0, reduce) - y0r
		for comp := range d.header.siz.Components {
			plane := tilePlanes[comp]
			xr, yr := compXRsiz[comp], compYRsiz[comp]

			// Native assembly: place the tile-component plane at its native-resolution
			// origin (no upsampling). The tile-component origin in native coordinates is
			// ceil(tx0/XRsiz) reduced, minus the image's native component origin. Samples
			// are stored raw (signed domain); the reversible colour transform and the
			// range clamp are applied once, after all tiles, in the componentsOnly path.
			ntx := engine.CeilDivPow2(ceilDiv(tile.tx0, xr), reduce) - ncx0[comp]
			nty := engine.CeilDivPow2(ceilDiv(tile.ty0, yr), reduce) - ncy0[comp]
			for y := 0; y < plane.H; y++ {
				dy := nty + y
				if dy < 0 || dy >= nch[comp] {
					continue
				}
				src := y * plane.W
				dst := dy * ncw[comp]
				for x := 0; x < plane.W; x++ {
					dx := ntx + x
					if dx < 0 || dx >= ncw[comp] {
						continue
					}
					nativePlanes[comp][dst+dx] = plane.Pix[src+x]
				}
			}

			for y := 0; y < plane.H; y++ {
				v0 := y * plane.W
				for dy := 0; dy < yr; dy++ {
					ry := toy + y*yr + dy
					if ry >= outH {
						break
					}
					rowOff := ry * outW
					for x := 0; x < plane.W; x++ {
						v := plane.Pix[v0+x]
						for dx := 0; dx < xr; dx++ {
							rx := tox + x*xr + dx
							if rx >= outW {
								break
							}
							fullPlanes[comp][rowOff+rx] = v
						}
					}
				}
			}
		}
	}

	// Stash the native per-component planes (signed sample domain, before any colour
	// transform and the display level-shift) for the generic N-component accessor.
	d.nativePlanes = nativePlanes
	d.nativeW = ncw
	d.nativeH = nch

	// Generic N-component path: skip image.Image reconstruction (which only models
	// 1/3/4 components). The caller reads the native planes via Components().
	if d.componentsOnly {
		// The reversible colour transform (RCT) is part of decoding, applied here on
		// the native planes (the irreversible ICT already ran per-tile in float). It
		// is defined on the first three components and only when they share the
		// reversible transform; further components pass through. Then clamp every
		// component to its representable range (post-transform), matching OpenJPEG.
		if len(nativePlanes) >= 3 && d.header.cod.UseMCT &&
			d.mainCodFor(0).Reversible && d.mainCodFor(1).Reversible && d.mainCodFor(2).Reversible {
			ApplyRCT([]engine.ComponentPlane{
				{Pix: nativePlanes[0]}, {Pix: nativePlanes[1]}, {Pix: nativePlanes[2]},
			})
		}
		for c := range nativePlanes {
			lo := int32(-(1 << uint(compPrecisions[c]-1)))
			hi := int32(1<<uint(compPrecisions[c]-1)) - 1
			clampPlaneFn(nativePlanes[c], lo, hi)
		}
		return nil, nil
	}

	return d.reconstructImage(fullPlanes, outW, outH)
}

// clampPlaneScalar is the post-transform range clamp's per-sample loop, split out so it can be the default target of
// clampPlaneFn (see simd_dispatch.go). It holds for any lo and hi, ordered or not.
func clampPlaneScalar(p []int32, lo, hi int32) {
	for i, v := range p {
		if v < lo {
			p[i] = lo
		} else if v > hi {
			p[i] = hi
		}
	}
}

// DecodedComponent is one decoded component at its native (subsampled, reduced)
// resolution. Samples are row-major, length W*H, in the SIGNED sample domain: a
// signed component's values are centred on 0; an unsigned component's values carry
// the encoder's −2^(Precision−1) DC level shift (add it back for display). Values
// are clamped to the component's representable range.
type DecodedComponent struct {
	W, H         int
	Precision    int
	Signed       bool
	XRsiz, YRsiz int
	Samples      []int32
}

// SetComponentsOnly requests the generic N-component decode path: finalizeImage
// assembles the native per-component planes and skips image.Image reconstruction,
// so component counts other than 1/3/4 (CMYK, 2-channel, multispectral) decode.
// Read the result with Components() after Decode returns.
func (d *Decoder) SetComponentsOnly(v bool) { d.componentsOnly = v }

// Components returns the decoded components at native resolution. It is valid only
// after a successful Decode (typically with SetComponentsOnly set).
func (d *Decoder) Components() []DecodedComponent {
	out := make([]DecodedComponent, len(d.nativePlanes))
	for c := range d.nativePlanes {
		comp := DecodedComponent{W: d.nativeW[c], H: d.nativeH[c], Samples: d.nativePlanes[c]}
		if c < len(d.header.siz.Components) {
			sc := d.header.siz.Components[c]
			comp.Precision = sc.Precision
			comp.Signed = sc.Signed
			comp.XRsiz = sc.XRsiz
			comp.YRsiz = sc.YRsiz
		}
		out[c] = comp
	}
	return out
}

// GetColorModel determines the appropriate color.Model for the codestream.
func (d *Decoder) GetColorModel() (color.Model, error) {
	if len(d.header.siz.Components) == 1 {
		if d.header.siz.Components[0].Precision <= 8 {
			return color.GrayModel, nil
		}
		return color.Gray16Model, nil
	} else if n := len(d.header.siz.Components); n == 2 || n == 3 || n == 4 {
		// 2 components → greyscale+alpha, 3 → RGB, 4 → RGBA (fourth = alpha); all
		// rendered through the NRGBA(64) model. CMYK (colr enum 12) converts to 8-bit
		// RGB regardless of component precision, so it is always NRGBA.
		if n == 4 && d.colorSpace == 12 {
			return color.NRGBAModel, nil
		}
		for _, c := range d.header.siz.Components {
			if c.Precision > 8 {
				return color.NRGBA64Model, nil
			}
		}
		return color.NRGBAModel, nil
	}
	return nil, fmt.Errorf("unsupported component count: %d", len(d.header.siz.Components))
}

// SetReduce sets the number of highest resolution levels to discard, so the
// decode produces a smaller image (resolution Nd-reduce). It must be called
// before Decode. 0 (the default) is a full-resolution decode.
func (d *Decoder) SetReduce(n int) {
	if n < 0 {
		n = 0
	}
	d.reduce = n
}

// SetMaxLayer caps the number of quality layers contributed to the decode, for a
// lower-quality / progressive result. It must be called before Decode. 0 (the
// default) decodes all layers.
func (d *Decoder) SetMaxLayer(n int) {
	if n < 0 {
		n = 0
	}
	d.maxLayer = n
}

// SetColorSpace records the JP2 colr enumerated colour space (16=sRGB,
// 17=greyscale, 18=sYCC) for the reconstruction. It must be called before Decode;
// only sYCC changes the output (YCbCr→RGB).
func (d *Decoder) SetColorSpace(cs int) {
	d.colorSpace = cs
}

// ChannelDef is one JP2 cdef channel definition: Comp is the codestream component
// index, Typ the channel type (0 = colour, 1 = opacity, 2 = pre-multiplied opacity,
// 0xFFFF = unspecified), and Asoc the 1-based colour the channel represents (0 = whole
// image, 0xFFFF = none).
type ChannelDef struct{ Comp, Typ, Asoc int }

// SetChannelDefs records the JP2 cdef channel definitions so reconstruction can
// reorder the component planes into colour order (Asoc 1, 2, 3, …) before any
// colour-space conversion. It must be called before Decode.
func (d *Decoder) SetChannelDefs(defs []ChannelDef) {
	d.channelDefs = defs
}

// CIELabParams holds the explicit range (R) and offset (O) parameters of an
// enumerated CIELab colr box, one per L/a/b channel.
type CIELabParams struct{ RL, OL, RA, OA, RB, OB uint32 }

// SetCIELab records the explicit CIELab range/offset parameters from the JP2 colr
// box (nil = use the CIELab defaults). It must be called before Decode.
func (d *Decoder) SetCIELab(p *CIELabParams) {
	d.cielab = p
}

// SetICCProfile records an embedded ICC profile (colr METH==2) to apply to the
// decoded components at reconstruction. It must be called before Decode.
func (d *Decoder) SetICCProfile(p []byte) {
	d.iccProfile = p
}

// reducedSize returns the output dimensions after applying the reduce factor —
// the image region [X0,Xsiz)×[Y0,Ysiz) (which excludes any non-zero image origin)
// taken at the reduced resolution.
func (d *Decoder) reducedSize() (int, int) {
	Nd := d.header.cod.Levels
	reduce := d.reduce
	if reduce > Nd {
		reduce = Nd
	}
	w := engine.CeilDivPow2(d.header.siz.Xsiz, reduce) - engine.CeilDivPow2(d.header.siz.X0, reduce)
	h := engine.CeilDivPow2(d.header.siz.Ysiz, reduce) - engine.CeilDivPow2(d.header.siz.Y0, reduce)
	return w, h
}

func (d *Decoder) GetWidth() int {
	w, _ := d.reducedSize()
	return w
}

func (d *Decoder) GetHeight() int {
	_, h := d.reducedSize()
	return h
}

func (d *Decoder) compPrecisions() []int {
	p := make([]int, len(d.header.siz.Components))
	for i, c := range d.header.siz.Components {
		p[i] = c.Precision
	}
	return p
}

func (d *Decoder) compXRsiz() []int {
	x := make([]int, len(d.header.siz.Components))
	for i, c := range d.header.siz.Components {
		x[i] = c.XRsiz
	}
	return x
}

func (d *Decoder) compYRsiz() []int {
	y := make([]int, len(d.header.siz.Components))
	for i, c := range d.header.siz.Components {
		y[i] = c.YRsiz
	}
	return y
}
