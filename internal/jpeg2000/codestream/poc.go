// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/binutil"
)

// POCTuple is one progression-order-change entry (ISO 15444-1 Table A.32). It
// selects a "progression volume": layers [0, LayE), resolutions [ResS, ResE),
// components [CompS, CompE), all precincts — enumerated in progression order
// Order. Bounds use start-inclusive / end-exclusive.
type POCTuple struct {
	ResS, CompS int
	LayE        int
	ResE, CompE int
	Order       int
}

// processPOC parses a POC marker segment into the current section's POC list
// (main header → applies to all tiles; tile-part header → that tile only). The
// tuples drive pocSequence, which reproduces OpenJPEG's packet order (one
// progression per tuple, first-occurrence wins via a shared "seen" set). Tuples
// from successive tile-parts of one tile accumulate (see the tile-part case below).
func (d *Decoder) processPOC() error {
	Lpoc, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	if Lpoc < 2 {
		return fmt.Errorf("POC: invalid length %d", Lpoc)
	}
	// CSpoc/CEpoc are 2 bytes when there are more than 256 components, else 1.
	wide := len(d.header.siz.Components) > 256
	tupleSize := 7
	if wide {
		tupleSize = 9
	}
	body := int(Lpoc) - 2
	if body <= 0 || body%tupleSize != 0 {
		return fmt.Errorf("POC: invalid body length %d (tuple size %d)", body, tupleSize)
	}
	readComp := func() (int, error) {
		if wide {
			v, err := binutil.ReadU16(d.r)
			return int(v), err
		}
		v, err := binutil.ReadU8(d.r)
		return int(v), err
	}

	n := body / tupleSize
	tuples := make([]POCTuple, 0, n)
	for i := 0; i < n; i++ {
		rs, err := binutil.ReadU8(d.r)
		if err != nil {
			return err
		}
		cs, err := readComp()
		if err != nil {
			return err
		}
		lye, err := binutil.ReadU16(d.r)
		if err != nil {
			return err
		}
		re, err := binutil.ReadU8(d.r)
		if err != nil {
			return err
		}
		ce, err := readComp()
		if err != nil {
			return err
		}
		ppoc, err := binutil.ReadU8(d.r)
		if err != nil {
			return err
		}
		if int(ppoc) > 4 {
			return fmt.Errorf("POC: invalid progression order %d", ppoc)
		}
		tuples = append(tuples, POCTuple{
			ResS: int(rs), CompS: cs, LayE: int(lye),
			ResE: int(re), CompE: ce, Order: int(ppoc),
		})
	}

	switch d.section {
	case sectionMainHeader:
		d.header.poc = tuples
	case sectionTilePartHeader:
		if d.cur.tileIndex >= 0 && d.cur.tileIndex < len(d.tiles) {
			// POC markers in successive tile-parts of the same tile ACCUMULATE — each
			// adds its tuples to the tile's progression (ISO 15444-1 A.6.6; OpenJPEG
			// opj_j2k_read_poc appends to tcp->pocs and grows numpocs). They are not a
			// replacement: a later tuple typically covers the resolutions/layers a
			// previous tuple left out, and pocSequence's shared "seen" set then emits
			// each packet exactly once, in tile-part byte order. (p0_07's tile 0 splits
			// res 0-2 into tile-part 0 and res 3 into tile-part 1 via two POC tuples.)
			d.tiles[d.cur.tileIndex].poc = append(d.tiles[d.cur.tileIndex].poc, tuples...)
		}
		d.cur.tilePartConsumed += 2 + int(Lpoc)
	default:
		return fmt.Errorf("POC: unexpected in section %v", d.section)
	}
	return nil
}

// pocSequence builds the packet (layer, resolution, component, precinct) order for
// a tile from its POC tuples, matching OpenJPEG's packet iterator: it runs one
// progression per tuple (layer start 0, layer end LayE, in order Order over the
// tuple's resolution/component range), and a shared "seen" set means each packet is
// emitted by the first tuple whose volume contains it (ISO 15444-1 A.6.6, opj
// pi.c). Ranges are clamped to the tile's actual dimensions.
func pocSequence(tuples []POCTuple, numLayers, numRes, numComps, maxPrec int) [][4]int {
	seen := make(map[[4]int]bool)
	var out [][4]int
	clamp := func(v, hi int) int {
		if v < 0 {
			return 0
		}
		if v > hi {
			return hi
		}
		return v
	}
	for _, t := range tuples {
		lHi := clamp(t.LayE, numLayers)
		rLo, rHi := clamp(t.ResS, numRes), clamp(t.ResE, numRes)
		cLo, cHi := clamp(t.CompS, numComps), clamp(t.CompE, numComps)
		for _, tup := range packetOrderRange(t.Order, 0, lHi, rLo, rHi, cLo, cHi, 0, maxPrec) {
			if !seen[tup] {
				seen[tup] = true
				out = append(out, tup)
			}
		}
	}
	return out
}
