// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/engine"
)

func (d *Decoder) parseLRCPPacketHeaders(
	payload []byte,
) (blocks []engine.BlockStream, headerBytes int, ok bool, err error) {
	// Raw raster shortcut: if payload size exactly matches raw pixel bytes, bypass
	// packet header parsing and treat the payload as uncompressed pixel data.
	blocks, headerBytes, ok, err = d.parseRawRasterFallback(payload)
	if err != nil || ok {
		return blocks, headerBytes, ok, err
	}

	// Standard LRCP packet header parsing (ISO 15444-1)
	blocks, headerBytes, ok, err = d.tryParseStandardLRCP(payload)
	if err != nil || ok {
		return blocks, headerBytes, ok, err
	}

	return nil, 0, false, nil
}

// bandGeom is the static precinct/code-block geometry of one (component,
// resolution, subband), following ISO 15444-1 Annex B.6/B.7 with the partition
// anchored at the CANVAS (not the tile/subband origin). For a tile whose origin
// is a multiple of the precinct/code-block size the two coincide; for an unaligned
// origin they differ (the first precinct and code-block of the subband are partial).
type bandGeom struct {
	c, r, level  int
	band         string
	subW, subH   int // subband size
	tbx0, tby0   int // subband absolute origin (subband coordinates)
	cbw, cbh     int // code-block size (possibly reduced by the precinct size)
	cbwExpn      int // log2(cbw)
	cbhExpn      int // log2(cbh)
	gcbx0, gcby0 int // floor(tbx0/cbw) — origin of the canvas-anchored code-block grid
	ncbw, ncbh   int // code-block grid spanning the subband
	tlcbgx       int // top-left code-block-group (precinct) start, subband coordinates
	tlcbgy       int
	cbgwExpn     int // log2 of the precinct size in subband coordinates
	cbghExpn     int
	npw, nph     int // precinct grid of the resolution
}

// cblkBounds returns the position (x0,y0 within the subband buffer) and size (w,h)
// of the code-block at canvas-anchored grid index (cbx,cby). The code-block covers
// the absolute subband range [(gcbx0+cbx)·cbw, +cbw) clipped to the subband, so the
// first/last block of an unaligned subband is partial.
func (g *bandGeom) cblkBounds(cbx, cby int) (x0, y0, w, h int) {
	ax0 := (g.gcbx0 + cbx) << uint(g.cbwExpn)
	ax1 := ax0 + g.cbw
	if ax0 < g.tbx0 {
		ax0 = g.tbx0
	}
	if ax1 > g.tbx0+g.subW {
		ax1 = g.tbx0 + g.subW
	}
	ay0 := (g.gcby0 + cby) << uint(g.cbhExpn)
	ay1 := ay0 + g.cbh
	if ay0 < g.tby0 {
		ay0 = g.tby0
	}
	if ay1 > g.tby0+g.subH {
		ay1 = g.tby0 + g.subH
	}
	return ax0 - g.tbx0, ay0 - g.tby0, max(ax1-ax0, 0), max(ay1-ay0, 0)
}

// precTrees is the per-(component, resolution, subband, precinct) packet-header
// decoding state: inclusion / zero-bit-plane tag trees plus per-code-block Lblock
// and first-inclusion flags, indexed by precinct-local code-block position.
type precTrees struct {
	inclTree, zbpTree *engine.TagTree
	lblock            []int
	firstIncl         []bool
	// segMax/segNum track the currently-open codeword segment per code-block (its
	// maxpasses and how many passes are filled), so segments continue correctly
	// across quality layers. segMax==0 means no segment opened yet.
	segMax, segNum []int
	nbx, nby       int // code-block grid within this precinct
	cbx0, cby0     int // origin of this precinct in the subband code-block grid
}

// Work limits for one tile's packet geometry, guarding against hostile headers.
// All three are far above any realistic image (a 4096×4096 single-precinct image
// needs well under 2^20 code-blocks). maxPackets bounds the packet-sequence slice a
// tile's progression enumerates — numLayers·numResolutions·numComps·maxPrec entries,
// 32 bytes each ([4]int) — near 32 MB, so a header pairing a large layer count with
// tiny precincts cannot demand millions of packets from a few payload bytes.
const (
	maxPrecincts  = 1 << 20
	maxCodeBlocks = 1 << 20
	maxPackets    = 1 << 20
)

// precinctExp returns the precinct-size exponents (PPx, PPy) for component c at
// resolution r. A COC marker can give a component its own precinct partition; the
// default (no precinct sizes signalled) is a single maximal precinct per resolution.
func (d *Decoder) precinctExp(c, r int) (ppx, ppy int) {
	prec := d.codFor(c).Precincts
	if r >= 0 && r < len(prec) {
		return prec[r].PPx, prec[r].PPy
	}
	return 15, 15 // default: a single maximal precinct per resolution
}

// segUnit is one codeword-segment fragment contributed by a single packet: len
// bytes carrying passes coding passes. newSeg marks the start of a fresh segment
// (vs. a continuation of the block's currently-open segment from an earlier layer).
type segUnit struct {
	len, passes int
	newSeg      bool
}

// Code-block style bits affecting segmentation (SPcod/SPcoc).
const (
	cblkStyBypass  = 0x01 // selective arithmetic-coding bypass ("lazy")
	cblkStyTermall = 0x04 // termination on each coding pass
)

// cblkSegMaxPasses returns how many coding passes a codeword segment may hold under
// the given code-block style (ISO 15444-1 B.10.7 / OpenJPEG opj_t2_init_seg). first
// marks the block's very first segment; prevMax is the previous segment's maxpasses.
func cblkSegMaxPasses(cblksty, prevMax int, first bool) int {
	switch {
	case cblksty&cblkStyTermall != 0:
		return 1
	case cblksty&cblkStyBypass != 0:
		if first {
			return 10
		}
		if prevMax == 1 || prevMax == 10 {
			return 2
		}
		return 1
	default:
		return 109 // effectively all passes in one segment
	}
}

// applyUnits folds one packet's segment fragments into a block's accumulating
// (segment byte-length, segment pass-count) lists, merging the first fragment into
// the open segment when it is a continuation.
func applyUnits(segs, segP []int, units []segUnit) ([]int, []int) {
	for _, u := range units {
		if u.newSeg || len(segs) == 0 {
			segs = append(segs, u.len)
			segP = append(segP, u.passes)
		} else {
			segs[len(segs)-1] += u.len
			segP[len(segP)-1] += u.passes
		}
	}
	return segs, segP
}

func (d *Decoder) tryParseStandardLRCP(payload []byte) (blocks []engine.BlockStream, headerBytes int, ok bool, err error) {
	// The standard packet path needs a valid COD (code-block geometry). A missing
	// or malformed COD leaves these zero; bail cleanly instead of dividing by zero.
	if d.header.cod.CodeBlockW <= 0 || d.header.cod.CodeBlockH <= 0 {
		return nil, 0, false, fmt.Errorf("missing or invalid COD code-block size")
	}

	// Quality-layer count is a tile-level parameter: a tile-part COD can override the
	// main-header layer count (e.g. a Kakadu-tiled image coding the busy centre tile
	// with more layers than the rest). It is carried only by COD, not COC, so resolve
	// it from the current tile's COD override rather than codFor (which would prefer a
	// COC that has no layer field).
	numLayers := d.header.cod.Layers
	if t := d.curTile(); t != nil && t.tileCOD != nil {
		numLayers = t.tileCOD.Layers
	}
	if numLayers == 0 {
		numLayers = 1
	}
	// maxLayer caps the quality layers contributed to the decode (0 = all).
	maxLayer := d.maxLayer
	numComps := len(d.header.siz.Components)

	// Per-component decomposition levels: a COC marker can give a component its own
	// DWT depth, so each component has its own resolution count. The packet
	// enumeration loops up to the maximum across components; a (component, resolution)
	// pair beyond a component's own depth simply has no precincts and is skipped.
	numResolutions := 1
	for c := 0; c < numComps; c++ {
		if nr := d.codFor(c).Levels + 1; nr > numResolutions {
			numResolutions = nr
		}
	}

	tile := &d.tiles[d.cur.tileIndex]

	// Build the static geometry per (component, resolution, subband) and the
	// per-(component, resolution) precinct count. maxPrec bounds the precinct loop;
	// (c,r) combinations with fewer precincts are skipped in the packet sequence.
	type geomKey struct {
		c, r int
		band string
	}
	type prKey struct{ c, r int }
	geoms := make(map[geomKey]*bandGeom)
	numprec := make(map[prKey]int)
	// resInfo[c][r] holds the resolution's precinct grid and the canvas-domain
	// precinct dimensions, used by the position-based PCRL/CPRL iterators.
	resInfo := make([][]resPrec, numComps)
	for c := range resInfo {
		resInfo[c] = make([]resPrec, numResolutions)
	}
	maxPrec := 1
	var totalCodeBlocks int64
	bandsFor := func(r int) []string {
		if r == 0 {
			return []string{"LL"}
		}
		return []string{"HL", "LH", "HH"}
	}
	for c := 0; c < numComps; c++ {
		comp := d.header.siz.Components[c]
		// Per-component code-block size: a COC marker overrides the COD default for
		// this component only.
		compCBW := d.codFor(c).CodeBlockW
		compCBH := d.codFor(c).CodeBlockH
		// Tile-component absolute coordinates on the reference grid. Sub-band and
		// resolution extents derive from these (ISO B.10.2), not from the width, so a
		// tile whose origin is not 2^Nd-aligned gets the same geometry the encoder used.
		tcx0 := ceilDiv(tile.tx0, comp.XRsiz)
		tcy0 := ceilDiv(tile.ty0, comp.YRsiz)
		tcx1 := ceilDiv(tile.tx1, comp.XRsiz)
		tcy1 := ceilDiv(tile.ty1, comp.YRsiz)
		cbwLog := floorLog2(compCBW)
		cbhLog := floorLog2(compCBH)
		// Per-component decomposition depth: this component spans resolutions 0..Nd.
		// Higher resolutions (up to the image-wide maximum) are left empty for it.
		Nd := d.codFor(c).Levels
		compResolutions := Nd + 1
		for r := 0; r < compResolutions; r++ {
			// Resolution-level coordinates of the tile-component, and the precinct grid
			// anchored at the CANVAS (ISO B.6 / opj_tcd: tl_prc = (x0>>pp)<<pp,
			// br_prc = ceil(x1)<<pp). For an origin-aligned tile these match the old
			// width-based result; for an unaligned origin the first precinct is partial.
			trx0 := engine.ResolutionLow(tcx0, Nd, r)
			trx1 := engine.ResolutionLow(tcx1, Nd, r)
			try0 := engine.ResolutionLow(tcy0, Nd, r)
			try1 := engine.ResolutionLow(tcy1, Nd, r)
			ppx, ppy := d.precinctExp(c, r)
			var npw, nph int
			tlPrcX := (trx0 >> uint(ppx)) << uint(ppx)
			tlPrcY := (try0 >> uint(ppy)) << uint(ppy)
			if trx1 > trx0 {
				npw = (engine.CeilDivPow2(trx1, ppx)<<uint(ppx) - tlPrcX) >> uint(ppx)
			}
			if try1 > try0 {
				nph = (engine.CeilDivPow2(try1, ppy)<<uint(ppy) - tlPrcY) >> uint(ppy)
			}
			if npw == 0 || nph == 0 {
				npw, nph = 0, 0
			}
			numprec[prKey{c, r}] = npw * nph
			if npw*nph > maxPrec {
				maxPrec = npw * nph
			}
			resInfo[c][r] = resPrec{
				npw: npw, nph: nph,
				cpw:    comp.XRsiz << uint(ppx+(Nd-r)),
				cph:    comp.YRsiz << uint(ppy+(Nd-r)),
				prcx0i: tlPrcX >> uint(ppx),
				prcy0i: tlPrcY >> uint(ppy),
				ppx:    ppx, ppy: ppy,
				cdx:  comp.XRsiz << uint(Nd-r),
				cdy:  comp.YRsiz << uint(Nd-r),
				trx0: trx0, try0: try0,
			}

			// Code-block-group (precinct in subband coordinates): for r>0 the subbands
			// sit at half the resolution, so the precinct halves to 2^(PP-1) and its
			// start is ceil(tl_prc/2).
			cbgwExpn, cbghExpn := ppx, ppy
			tlcbgx, tlcbgy := tlPrcX, tlPrcY
			if r > 0 {
				cbgwExpn, cbghExpn = ppx-1, ppy-1
				tlcbgx = engine.CeilDivPow2(tlPrcX, 1)
				tlcbgy = engine.CeilDivPow2(tlPrcY, 1)
			}
			if cbgwExpn < 0 {
				cbgwExpn = 0
			}
			if cbghExpn < 0 {
				cbghExpn = 0
			}
			level := Nd
			if r > 0 {
				level = Nd - r + 1
			}
			for _, band := range bandsFor(r) {
				hx := band == "HL" || band == "HH"
				hy := band == "LH" || band == "HH"
				tbx0, tbx1 := engine.SubbandBound1D(tcx0, tcx1, Nd, level, hx)
				tby0, tby1 := engine.SubbandBound1D(tcy0, tcy1, Nd, level, hy)
				subW, subH := tbx1-tbx0, tby1-tby0
				if subW < 0 {
					subW = 0
				}
				if subH < 0 {
					subH = 0
				}
				cbwExpn := min(cbwLog, cbgwExpn)
				cbhExpn := min(cbhLog, cbghExpn)
				cbw, cbh := 1<<uint(cbwExpn), 1<<uint(cbhExpn)
				// Canvas-anchored code-block grid spanning the subband.
				gcbx0 := tbx0 >> uint(cbwExpn)
				gcby0 := tby0 >> uint(cbhExpn)
				ncbw := engine.CeilDivPow2(tbx1, cbwExpn) - gcbx0
				ncbh := engine.CeilDivPow2(tby1, cbhExpn) - gcby0
				if subW == 0 {
					ncbw = 0
				}
				if subH == 0 {
					ncbh = 0
				}
				totalCodeBlocks += int64(ncbw) * int64(ncbh)
				geoms[geomKey{c, r, band}] = &bandGeom{
					c: c, r: r, level: level, band: band,
					subW: subW, subH: subH,
					tbx0: tbx0, tby0: tby0,
					cbw: cbw, cbh: cbh,
					cbwExpn: cbwExpn, cbhExpn: cbhExpn,
					gcbx0: gcbx0, gcby0: gcby0,
					ncbw: ncbw, ncbh: ncbh,
					tlcbgx: tlcbgx, tlcbgy: tlcbgy,
					cbgwExpn: cbgwExpn, cbghExpn: cbghExpn,
					npw: npw, nph: nph,
				}
			}
		}
	}
	// Bound the work induced by the geometry before enumerating packets or
	// zero-filling code-blocks: a hostile header (large image + tiny precincts or
	// code-blocks) could otherwise demand millions of packets/blocks from a tiny
	// payload. These limits are far above any real test image.
	if maxPrec > maxPrecincts {
		return nil, 0, false, fmt.Errorf("too many precincts (%d)", maxPrec)
	}
	if totalCodeBlocks > maxCodeBlocks {
		return nil, 0, false, fmt.Errorf("too many code-blocks (%d)", totalCodeBlocks)
	}

	// Lazily-built tag-tree state per (component, resolution, subband, precinct).
	type treeKey struct {
		c, r int
		band string
		prec int
	}
	trees := make(map[treeKey]*precTrees)
	getTrees := func(g *bandGeom, prec int) *precTrees {
		k := treeKey{g.c, g.r, g.band, prec}
		if t, ok := trees[k]; ok {
			return t
		}
		kx := prec % g.npw
		ky := prec / g.npw
		// Precinct's extent in subband coordinates (canvas-anchored), then the range of
		// code-blocks it owns. tlcblk = floor(prc/cblk)*cblk; the precinct's code-block
		// indices are [floor(prc.x0/cbw), ceil(prc.x1/cbw)) on the canvas grid, shifted
		// to the band's local grid (gcbx0).
		cbgxstart := g.tlcbgx + kx*(1<<uint(g.cbgwExpn))
		cbgystart := g.tlcbgy + ky*(1<<uint(g.cbghExpn))
		prcX0 := max(cbgxstart, g.tbx0)
		prcY0 := max(cbgystart, g.tby0)
		prcX1 := min(cbgxstart+(1<<uint(g.cbgwExpn)), g.tbx0+g.subW)
		prcY1 := min(cbgystart+(1<<uint(g.cbghExpn)), g.tby0+g.subH)
		cbx0 := (prcX0 >> uint(g.cbwExpn)) - g.gcbx0
		cby0 := (prcY0 >> uint(g.cbhExpn)) - g.gcby0
		cbx1 := engine.CeilDivPow2(prcX1, g.cbwExpn) - g.gcbx0
		cby1 := engine.CeilDivPow2(prcY1, g.cbhExpn) - g.gcby0
		// Clamp to the band's code-block grid: a geometrically empty band (ncbw or
		// ncbh == 0) must contribute no code-blocks, even though the precinct bounds
		// collapse to a single degenerate cell.
		cbx0 = max(cbx0, 0)
		cby0 = max(cby0, 0)
		cbx1 = min(cbx1, g.ncbw)
		cby1 = min(cby1, g.ncbh)
		nbx := max(cbx1-cbx0, 0)
		nby := max(cby1-cby0, 0)
		lb := make([]int, nbx*nby)
		for i := range lb {
			lb[i] = 3
		}
		t := &precTrees{
			inclTree:  engine.NewTagTree(max(nbx, 1), max(nby, 1)),
			zbpTree:   engine.NewTagTree(max(nbx, 1), max(nby, 1)),
			lblock:    lb,
			firstIncl: make([]bool, nbx*nby),
			segMax:    make([]int, nbx*nby),
			segNum:    make([]int, nbx*nby),
			nbx:       nbx, nby: nby,
			cbx0: cbx0, cby0: cby0,
		}
		trees[k] = t
		return t
	}

	// included records which code-blocks carried data; the rest are zero-filled
	// afterwards so every subband is fully covered for the inverse DWT.
	type cbKey struct {
		c, r     int
		band     string
		cbx, cby int
	}
	included := make(map[cbKey]bool)
	// blockIdx maps a code-block to its index in blocks, so multiple quality-layer
	// contributions accumulate into the one BlockStream.
	blockIdx := make(map[cbKey]int)

	blocks = make([]engine.BlockStream, 0)
	packetIdx := 0
	br := engine.NewRawBitReader(payload)

	// Packed packet headers (PPM/PPT): when present for this tile the packet headers
	// live in their own stream, not interleaved in the SOD. Read headers from hbr
	// over hbuf and bodies from br over payload. SOP markers stay in the body stream
	// (they delimit bodies); EPH markers travel with the headers. Without packed
	// headers hbr == br and hbuf == payload, so the in-stream path is unchanged.
	hbuf := payload
	hbr := br
	fromPPM := false
	if packed, ppm := d.packedPacketHeaders(d.cur.tileIndex); len(packed) > 0 {
		hbuf = packed
		hbr = engine.NewRawBitReader(packed)
		fromPPM = ppm
	}
	// When the headers come from the shared PPM stream, advance the global consumed
	// offset by the bytes this tile reads, so the next tile starts where this one
	// left off (PPM is one continuous stream for the whole codestream).
	if fromPPM {
		defer func() { d.ppmConsumed += hbr.BytesConsumed() }()
	}

	// Progression-order changes (POC): a tile-part POC overrides a main-header POC,
	// which overrides the COD's single default order.
	poc := d.tiles[d.cur.tileIndex].poc
	if len(poc) == 0 {
		poc = d.header.poc
	}

	// Bound the packet-sequence length before any builder enumerates it. Every builder
	// emits at most one packet per (layer, resolution, component, precinct):
	// packetOrder/pocSequence enumerate exactly that index space, and packetSeqPosition
	// visits each resolution's precinct once per component and layer (its position scan
	// steps by precinct boundaries, so its output — and its iteration count — are ≤ the
	// same product). numLayers·numResolutions·numComps·maxPrec is therefore an upper
	// bound on the seq slice. The product cannot overflow int64: numLayers ≤ 65535 (COD
	// uint16), numResolutions ≤ 33 (Levels ≤ 32), numComps ≤ 65535 (Csiz uint16), and
	// maxPrec ≤ maxPrecincts = 2^20 (enforced above), so it stays under 2^58.
	if int64(numLayers)*int64(numResolutions)*int64(numComps)*int64(maxPrec) > maxPackets {
		return nil, 0, false, fmt.Errorf("too many packets")
	}

	// The position-inner orders RPCL/PCRL/CPRL enumerate precincts by their position
	// on the reference grid, since a single precinct index does not map across
	// resolutions/components whose precinct grids differ (per-component precincts or
	// non-aligned origins). LRCP/RLCP are resolution/layer-outer with the precinct
	// innermost, so the per-resolution index sequence works directly.
	var seq [][4]int
	switch {
	case len(poc) > 0:
		seq = pocSequence(poc, numLayers, numResolutions, numComps, maxPrec)
	case d.header.cod.ProgOrder == 2 || d.header.cod.ProgOrder == 3 || d.header.cod.ProgOrder == 4: // RPCL, PCRL, CPRL
		seq = packetSeqPosition(d.header.cod.ProgOrder, numLayers, numComps, numResolutions,
			tile.tx0, tile.ty0, tile.tx1, tile.ty1, resInfo)
	default:
		seq = packetOrder(d.header.cod.ProgOrder, numLayers, numResolutions, numComps, maxPrec)
	}

	for _, tup := range seq {
		l, r, c, p := tup[0], tup[1], tup[2], tup[3]
		if p >= numprec[prKey{c, r}] {
			continue // precinct does not exist for this (component, resolution)
		}
		bands := bandsFor(r)
		cbSty := d.codFor(c).CblkStyle

		// SOP marker (0xFF91): when the COD SOP flag is set, an SOP marker segment
		// MAY precede each packet (ISO 15444-1 A.8.1 — "may be used", not "shall").
		// So consume and sanity-check it only when one is actually present; a packet
		// with no SOP is valid even with the flag set. Packets start byte-aligned.
		if d.header.cod.UseSOP {
			br.AlignToByte()
			off := br.BytesConsumed()
			if len(payload) >= off+6 && payload[off] == 0xFF && payload[off+1] == 0x91 {
				br.Skip(6) // FF91 Lsop(2)=4 Nsop(2); index not needed for sequential decode
			}
		}

		// Start a fresh packet-header bit stream (enables packet-header
		// bit-stuffing; the stuffing context does not carry across bodies).
		hbr.BeginPacketHeader()

		// Packet empty/non-empty bit (zero-length packet optimization)
		inclBit, okb := hbr.ReadBit()
		if !okb {
			if len(blocks) > 0 {
				return blocks, br.BytesConsumed(), true, nil
			}
			return nil, 0, false, nil
		}
		packetIdx++

		if inclBit == 0 {
			// Empty packet: end the header bit stream and align.
			hbr.HeaderAlign()
			if d.header.cod.UseEPH {
				off := hbr.BytesConsumed()
				if len(hbuf) >= off+2 && hbuf[off] == 0xFF && hbuf[off+1] == 0x92 {
					if _, err := hbr.ReadByte(); err != nil {
						return nil, 0, false, fmt.Errorf("truncated EPH marker")
					}
					if _, err := hbr.ReadByte(); err != nil {
						return nil, 0, false, fmt.Errorf("truncated EPH marker")
					}
				}
			}
			continue
		}

		// Non-empty packet: Phase 1 - read all code-block headers
		type cbEntry struct {
			g              *bandGeom
			cbx, cby       int
			x0, y0, w, h   int
			zbpVal, passes int
			units          []segUnit
			bodyLen        int
		}
		var entries []cbEntry

		for _, band := range bands {
			g := geoms[geomKey{c, r, band}]
			t := getTrees(g, p)
			for lcby := 0; lcby < t.nby; lcby++ {
				for lcbx := 0; lcbx < t.nbx; lcbx++ {
					cbx := t.cbx0 + lcbx
					cby := t.cby0 + lcby
					idx := lcby*t.nbx + lcbx

					var inc bool
					if !t.firstIncl[idx] {
						v, okI := t.inclTree.ProgressiveDecodeBool(hbr, lcbx, lcby, l+1)
						if !okI {
							if len(blocks) > 0 {
								return blocks, br.BytesConsumed(), true, nil
							}
							return nil, 0, false, nil
						}
						inc = v
					} else {
						b, okI := hbr.ReadBit()
						if !okI {
							if len(blocks) > 0 {
								return blocks, br.BytesConsumed(), true, nil
							}
							return nil, 0, false, nil
						}
						inc = b == 1
					}
					if !inc {
						continue
					}

					zbpVal := 0
					if !t.firstIncl[idx] {
						v, okZ := t.zbpTree.ProgressiveDecodeValue(hbr, lcbx, lcby, 16)
						if !okZ {
							return nil, 0, false, nil
						}
						zbpVal = v
						t.firstIncl[idx] = true
					}

					passes, okP := readNumPasses(hbr)
					if !okP {
						return nil, 0, false, nil
					}

					lblockInc, okL := readCommaCode(hbr)
					if !okL {
						return nil, 0, false, nil
					}
					t.lblock[idx] += lblockInc

					// Split the new passes into codeword segments per the code-block style,
					// reading one length code per segment (ISO B.10.7). Segment-open state
					// (t.segMax/segNum) persists across quality layers.
					needNew := false
					if t.segMax[idx] == 0 {
						t.segMax[idx] = cblkSegMaxPasses(cbSty, 0, true)
						t.segNum[idx] = 0
						needNew = true
					} else if t.segNum[idx] >= t.segMax[idx] {
						t.segMax[idx] = cblkSegMaxPasses(cbSty, t.segMax[idx], false)
						t.segNum[idx] = 0
						needNew = true
					}
					var units []segUnit
					bodyLen := 0
					n := passes
					firstUnit := true
					for n > 0 {
						cnt := n
						if avail := t.segMax[idx] - t.segNum[idx]; cnt > avail {
							cnt = avail
						}
						segLenBits := t.lblock[idx]
						if cnt > 1 {
							segLenBits += floorLog2(cnt)
						}
						segLenV, okS := hbr.ReadBits(segLenBits)
						if !okS {
							return nil, 0, false, nil
						}
						units = append(units, segUnit{len: int(segLenV), passes: cnt, newSeg: !firstUnit || needNew})
						bodyLen += int(segLenV)
						t.segNum[idx] += cnt
						n -= cnt
						firstUnit = false
						if n > 0 {
							t.segMax[idx] = cblkSegMaxPasses(cbSty, t.segMax[idx], false)
							t.segNum[idx] = 0
						}
					}

					x0, y0, w, h := g.cblkBounds(cbx, cby)
					entries = append(entries, cbEntry{
						g: g, cbx: cbx, cby: cby,
						x0: x0, y0: y0,
						w: w, h: h,
						zbpVal: zbpVal, passes: passes, units: units, bodyLen: bodyLen,
					})
				}
			}
		}

		// Phase 2: end header bit stream, align to byte boundary, EPH
		hbr.HeaderAlign()
		if d.header.cod.UseEPH {
			off := hbr.BytesConsumed()
			if len(hbuf) >= off+2 && hbuf[off] == 0xFF && hbuf[off+1] == 0x92 {
				hbr.Skip(2)
			}
		}

		// Phase 3: read all code-block body segments
		for _, e := range entries {
			offBody := br.BytesConsumed()
			if offBody+e.bodyLen > len(payload) {
				if len(blocks) > 0 {
					return blocks, br.BytesConsumed(), true, nil
				}
				return nil, 0, false, nil
			}
			body := make([]byte, e.bodyLen)
			for i := 0; i < e.bodyLen; i++ {
				b, err := br.ReadByte()
				if err != nil {
					return nil, 0, false, fmt.Errorf("truncated packet body")
				}
				body[i] = b
			}

			// Quality-layer limit: the body is still read (to keep the bit-stream in
			// sync) but contributions from layers at or beyond maxLayer are dropped,
			// so the block decodes to the lower quality of the first maxLayer layers.
			if maxLayer > 0 && l >= maxLayer {
				continue
			}

			// A code-block can be coded across several quality layers; each layer
			// contributes more coding passes and bytes to the SAME block. Accumulate
			// them into one BlockStream (concatenated MQ bytestream, summed passes)
			// rather than emitting a separate block per layer. `included` is marked
			// here (not at header parse) so blocks present only in dropped layers are
			// zero-filled rather than left missing.
			key := cbKey{c, e.g.r, e.g.band, e.cbx, e.cby}
			included[key] = true
			if i, ok := blockIdx[key]; ok {
				blocks[i].Bytes = append(blocks[i].Bytes, body...)
				blocks[i].Passes += e.passes
				blocks[i].Segments, blocks[i].SegPasses = applyUnits(blocks[i].Segments, blocks[i].SegPasses, e.units)
				blocks[i].Layers++
				blocks[i].PacketCount++
			} else {
				segs, segP := applyUnits(nil, nil, e.units)
				blockIdx[key] = len(blocks)
				blocks = append(blocks, engine.BlockStream{
					Comp:        c,
					Level:       e.g.level,
					Band:        e.g.band,
					W:           e.w,
					H:           e.h,
					X0:          e.x0,
					Y0:          e.y0,
					Bytes:       body,
					Included:    true,
					ZBP:         e.zbpVal,
					Layers:      1,
					Entropy:     true,
					Segments:    segs,
					SegPasses:   segP,
					Passes:      e.passes,
					PacketCount: 1,
				})
			}
		}
	}

	// Zero-fill every code-block that was never included, so each subband is fully
	// covered (the inverse DWT requires complete LL/HL/LH/HH bands).
	for k, g := range geoms {
		for cby := 0; cby < g.ncbh; cby++ {
			for cbx := 0; cbx < g.ncbw; cbx++ {
				if included[cbKey{g.c, g.r, k.band, cbx, cby}] {
					continue
				}
				x0, y0, w, h := g.cblkBounds(cbx, cby)
				blocks = append(blocks, engine.BlockStream{
					Comp:  g.c,
					Level: g.level,
					Band:  g.band,
					W:     w,
					H:     h,
					X0:    x0,
					Y0:    y0,
					Zero:  true,
				})
			}
		}
	}

	return blocks, br.BytesConsumed(), true, nil
}

// ceilDiv returns ceil(a/b) for a >= 0, b > 0.
func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// resPrec is the per-(component, resolution) precinct grid plus the canvas-domain
// precinct dimensions (precinct size mapped back to reference-grid samples).
type resPrec struct {
	npw, nph       int // number of precincts wide / high
	cpw, cph       int // precinct width / height in canvas (reference-grid) coordinates
	prcx0i, prcy0i int // canvas precinct index of this tile's first precinct
	ppx, ppy       int // precinct exponents (PPx/PPy) at this resolution
	cdx, cdy       int // reference-grid samples per resolution sample (XRsiz<<levelno)
	trx0, try0     int // resolution-level low coordinate of the tile-component
}

// packetSeqPosition enumerates packets for the position-inner progression orders
// RPCL (2), PCRL (3) and CPRL (4). These iterate precincts by their position on the
// reference grid rather than by index, because precinct grids differ across
// resolutions and components (per-component precincts, non-aligned origins; ISO
// 15444-1 B.12.1). Positions are CANVAS-absolute and the precinct grid is anchored
// at the canvas, so the tile's reference-grid region [tx0,tx1) is scanned and each
// canvas precinct index is mapped to the tile-local one via resPrec.prcx0i. Returned
// tuples are [L,R,C,P].
func packetSeqPosition(order, nl, nc, nr, tx0, ty0, tx1, ty1 int, resInfo [][]resPrec) [][4]int {
	var out [][4]int

	// emitOne appends the layer packets of (component c, resolution r) when its
	// precinct is triggered at the canvas reference-grid position (x,y) (ISO 15444-1
	// B.12.1.3, matching OpenJPEG opj_pi_next_*). A precinct is triggered when (x,y) is
	// a precinct boundary, OR when (x,y) is the tile origin and the resolution's origin
	// is not precinct-aligned (the partial first precinct). The precinct index is the
	// floored resolution-coordinate, relative to the tile's first precinct.
	emitOne := func(c, r, x, y int) {
		ri := resInfo[c][r]
		if ri.npw == 0 || ri.nph == 0 || ri.cpw == 0 || ri.cph == 0 {
			return
		}
		yHit := y%ri.cph == 0 || (y == ty0 && ri.try0%(1<<uint(ri.ppy)) != 0)
		xHit := x%ri.cpw == 0 || (x == tx0 && ri.trx0%(1<<uint(ri.ppx)) != 0)
		if !xHit || !yHit {
			return
		}
		prci := ceilDiv(x, ri.cdx)>>uint(ri.ppx) - ri.prcx0i
		prcj := ceilDiv(y, ri.cdy)>>uint(ri.ppy) - ri.prcy0i
		if prci < 0 || prcj < 0 || prci >= ri.npw || prcj >= ri.nph {
			return // position lies outside this resolution's precinct grid
		}
		prec := prcj*ri.npw + prci
		for l := 0; l < nl; l++ {
			out = append(out, [4]int{l, r, c, prec})
		}
	}

	// emitAt appends every triggered resolution of component c at (x,y), resolution
	// innermost — the precinct-inner-of-resolution form PCRL/CPRL use.
	emitAt := func(c, x, y int) {
		for r := 0; r < nr; r++ {
			emitOne(c, r, x, y)
		}
	}

	// stepFor returns the minimal precinct stride (dx,dy) over the given component
	// range, i.e. the position increment that visits every precinct boundary.
	stepFor := func(cLo, cHi int) (dx, dy int) {
		for c := cLo; c < cHi; c++ {
			for r := 0; r < nr; r++ {
				ri := resInfo[c][r]
				if ri.npw == 0 || ri.nph == 0 {
					continue
				}
				if dx == 0 || ri.cpw < dx {
					dx = ri.cpw
				}
				if dy == 0 || ri.cph < dy {
					dy = ri.cph
				}
			}
		}
		if dx <= 0 {
			dx = 1
		}
		if dy <= 0 {
			dy = 1
		}
		return dx, dy
	}

	// The scan starts at the tile origin and steps to successive precinct boundaries
	// via `step - (pos % step)` (OpenJPEG opj_pi_next_*), so the tile origin itself is
	// always visited — that is where a partial first precinct is triggered.
	if order == 2 { // RPCL: resolution, then position, component, layer
		dx, dy := stepFor(0, nc)
		for r := 0; r < nr; r++ {
			for y := ty0; y < ty1; y += dy - (y % dy) {
				for x := tx0; x < tx1; x += dx - (x % dx) {
					for c := 0; c < nc; c++ {
						emitOne(c, r, x, y)
					}
				}
			}
		}
		return out
	}

	if order == 4 { // CPRL: component, then position, resolution, layer
		for c := 0; c < nc; c++ {
			dx, dy := stepFor(c, c+1)
			for y := ty0; y < ty1; y += dy - (y % dy) {
				for x := tx0; x < tx1; x += dx - (x % dx) {
					emitAt(c, x, y)
				}
			}
		}
		return out
	}

	// PCRL: position, then component, resolution, layer
	dx, dy := stepFor(0, nc)
	for y := ty0; y < ty1; y += dy - (y % dy) {
		for x := tx0; x < tx1; x += dx - (x % dx) {
			for c := 0; c < nc; c++ {
				emitAt(c, x, y)
			}
		}
	}
	return out
}

// packetOrder enumerates packet (layer, resolution, component, precinct) indices
// in the sequence dictated by the progression order (ISO 15444-1 Table A.16):
// 0=LRCP 1=RLCP 2=RPCL 3=PCRL 4=CPRL. The four letters list the loop nesting from
// outermost to innermost; the innermost index varies fastest. Each returned tuple
// is [L, R, C, P]. (With a single precinct per resolution the precinct dimension
// is trivial; full precinct partitioning is a separate feature.)
func packetOrder(order, nl, nr, nc, np int) [][4]int {
	return packetOrderRange(order, 0, nl, 0, nr, 0, nc, 0, np)
}

// packetOrderRange is packetOrder over explicit half-open ranges per dimension
// (layer, resolution, component, precinct), used to enumerate the sub-volumes a
// POC tuple selects. Each returned tuple is [L, R, C, P].
func packetOrderRange(order, lLo, lHi, rLo, rHi, cLo, cHi, pLo, pHi int) [][4]int {
	// nest lists dimension ids (0=L,1=R,2=C,3=P) from outer to inner.
	nests := map[int][4]int{
		0: {0, 1, 2, 3}, // LRCP
		1: {1, 0, 2, 3}, // RLCP
		2: {1, 3, 2, 0}, // RPCL
		3: {3, 2, 1, 0}, // PCRL
		4: {2, 3, 1, 0}, // CPRL
	}
	nest, ok := nests[order]
	if !ok {
		nest = nests[0]
	}
	los := [4]int{lLo, rLo, cLo, pLo}
	his := [4]int{lHi, rHi, cHi, pHi}
	var out [][4]int
	var idx [4]int
	var rec func(level int)
	rec = func(level int) {
		if level == 4 {
			out = append(out, idx)
			return
		}
		dim := nest[level]
		for v := los[dim]; v < his[dim]; v++ {
			idx[dim] = v
			rec(level + 1)
		}
	}
	rec(0)
	return out
}

// readNumPasses decodes the number of coding passes per ISO 15444-1 Table B.4.
// Encoding (OpenJPEG): 0→1, 10→2, 11+2bits(!=3)→3..5, 1111+5bits(!=31)→6..36, etc.
func readNumPasses(br *engine.BitReader) (int, bool) {
	b, ok := br.ReadBit()
	if !ok {
		return 0, false
	}
	if b == 0 {
		return 1, true
	}

	b, ok = br.ReadBit()
	if !ok {
		return 0, false
	}
	if b == 0 {
		return 2, true
	}

	v, ok := br.ReadBits(2)
	if !ok {
		return 0, false
	}
	if v != 3 {
		return 3 + int(v), true
	}

	v, ok = br.ReadBits(5)
	if !ok {
		return 0, false
	}
	if v != 31 {
		return 6 + int(v), true
	}

	v, ok = br.ReadBits(7)
	if !ok {
		return 0, false
	}
	return 37 + int(v), true
}

// readCommaCode reads a unary code: count 1-bits until a 0-bit.
func readCommaCode(br *engine.BitReader) (int, bool) {
	n := 0
	for {
		b, ok := br.ReadBit()
		if !ok {
			return 0, false
		}
		if b == 0 {
			return n, true
		}
		n++
	}
}

// floorLog2 returns floor(log2(n)) for n > 0.
func floorLog2(n int) int {
	r := 0
	for n >>= 1; n > 0; n >>= 1 {
		r++
	}
	return r
}

func (d *Decoder) parseRawRasterFallback(payload []byte) ([]engine.BlockStream, int, bool, error) {
	tile := &d.tiles[d.cur.tileIndex]
	w, h := tile.tw, tile.th
	compPrecisions := d.compPrecisions()
	compXRsiz := d.compXRsiz()
	compYRsiz := d.compYRsiz()
	numComps := len(compPrecisions)

	totalNeeded := 0
	for c := 0; c < numComps; c++ {
		cw := (w + compXRsiz[c] - 1) / compXRsiz[c]
		ch := (h + compYRsiz[c] - 1) / compYRsiz[c]
		size := cw * ch
		if compPrecisions[c] > 8 {
			size *= 2
		}
		totalNeeded += size
	}

	if len(payload) != totalNeeded {
		return nil, 0, false, nil
	}

	blocks := make([]engine.BlockStream, numComps)
	off := 0
	for c := 0; c < numComps; c++ {
		cw := (w + compXRsiz[c] - 1) / compXRsiz[c]
		ch := (h + compYRsiz[c] - 1) / compYRsiz[c]
		size := cw * ch
		if compPrecisions[c] > 8 {
			size *= 2
		}
		blocks[c] = engine.BlockStream{
			Comp:        c,
			Level:       0,
			Band:        "LL",
			W:           cw,
			H:           ch,
			Bytes:       payload[off : off+size],
			Included:    true,
			Layers:      1,
			Entropy:     false,
			Passes:      1,
			Segments:    []int{size},
			PacketCount: 1,
		}
		off += size
	}
	return blocks, off, true, nil
}
