// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/binutil"
)

// processRGN parses an RGN marker segment (region of interest, ISO 15444-1 A.6.3).
// Only the implicit max-shift method (Srgn==0) is defined by Part 1: the encoder
// scales ROI coefficients up by SPrgn bit-planes so they are coded above the
// background; the decoder decodes the extra planes and shifts those coefficients
// back down (see Tier-1). The shift is stored per component, scoped to the main
// header (all tiles) or the current tile-part.
func (d *Decoder) processRGN() error {
	Lrgn, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	crgnBytes := 1
	if len(d.header.siz.Components) >= 257 {
		crgnBytes = 2
	}
	if int(Lrgn) < 2+crgnBytes+2 {
		return fmt.Errorf("RGN: invalid length %d", Lrgn)
	}
	var comp int
	if crgnBytes == 1 {
		b, err := binutil.ReadU8(d.r)
		if err != nil {
			return err
		}
		comp = int(b)
	} else {
		v, err := binutil.ReadU16(d.r)
		if err != nil {
			return err
		}
		comp = int(v)
	}
	if comp < 0 || comp >= len(d.header.siz.Components) {
		return fmt.Errorf("RGN: component index %d out of range", comp)
	}
	srgn, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	sprgn, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	if srgn != 0 {
		return fmt.Errorf("unsupported: RGN style %d (only implicit max-shift)", srgn)
	}

	switch d.section {
	case sectionMainHeader:
		if d.header.rgn == nil {
			d.header.rgn = make(map[int]int)
		}
		d.header.rgn[comp] = int(sprgn)
	case sectionTilePartHeader:
		t := &d.tiles[d.cur.tileIndex]
		if t.rgn == nil {
			t.rgn = make(map[int]int)
		}
		t.rgn[comp] = int(sprgn)
		d.cur.tilePartConsumed += 2 + int(Lrgn)
	default:
		return fmt.Errorf("RGN: unexpected in section %v", d.section)
	}

	return nil
}
