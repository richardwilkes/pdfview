// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/binutil"
)

type SOT struct {
	Isot  uint16
	Psot  uint32
	TPsot uint8
	TNsot uint8
}

func (d *Decoder) processSOT() error {
	if d.section != sectionMainHeader {
		return fmt.Errorf("SOT: unexpected in section %v", d.section)
	}
	// We expect the reader to be positioned right after the SOT marker (0xFF90),
	// at the start of the Lsot field, per parseCodestreamHeader behavior.
	Lsot, err := binutil.ReadU16(d.r)
	if err != nil {
		return fmt.Errorf("read Lsot failed: %w", err)
	}
	if Lsot != 10 { // SOT segment length should be 10
		return fmt.Errorf("SOT: invalid length %d", Lsot)
	}

	// (moved TT2H parsing to after SOD payload is read and 'data' is available)
	Isot, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	Psot, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	TPsot, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	TNsot, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}

	if int(Isot) >= len(d.tiles) {
		return fmt.Errorf("SOT: tile index out of range: %d (tiles=%d)", Isot, len(d.tiles))
	}
	// A tile may be split across several tile-parts (TNsot total, this one is
	// TPsot). TNsot == 0 means the total is not yet known. We accumulate each part's
	// SOD payload in stream order and parse the concatenation later, so the TPsot/TNsot
	// values are not used for indexing — an inconsistent TPsot >= TNsot (seen in
	// real-world files where the encoder wrote a wrong TNsot) is tolerated, matching
	// OpenJPEG, rather than rejected. TPsot is a uint8 so it is inherently bounded.

	d.cur.active = true
	d.cur.tileIndex = int(Isot)
	d.tilePartTiles = append(d.tilePartTiles, int(Isot))

	d.cur.sot = SOT{
		Isot:  Isot,
		Psot:  Psot,
		TPsot: TPsot,
		TNsot: TNsot,
	}

	d.cur.cod = d.header.cod
	d.cur.qcd = d.header.qcd

	// Seed per-component coding/quantization overrides from the main header so a
	// tile-part COC/QCC overlays (rather than discards) the main-header ones. Copy
	// the maps so tile-part edits never mutate the main-header state.
	d.cur.coc = nil
	d.cur.qcc = nil
	if len(d.header.coc) > 0 {
		d.cur.coc = make(map[int]COD, len(d.header.coc))
		for k, v := range d.header.coc {
			d.cur.coc[k] = v
		}
	}
	if len(d.header.qcc) > 0 {
		d.cur.qcc = make(map[int]QCD, len(d.header.qcc))
		for k, v := range d.header.qcc {
			d.cur.qcc[k] = v
		}
	}

	d.section = sectionTilePartHeader

	// SOT contributes 2 bytes for the marker itself and Lsot bytes for the
	// marker segment, so the tile-part starts with this many bytes consumed.
	d.cur.tilePartConsumed = 2 + int(Lsot)

	return nil
}
