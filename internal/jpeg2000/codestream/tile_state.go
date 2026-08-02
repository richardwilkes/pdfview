// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import "github.com/richardwilkes/pdfview/internal/jpeg2000/engine"

type tileState struct {
	index    int
	tx0, ty0 int
	tx1, ty1 int
	tw, th   int
	// payload accumulates this tile's SOD data across all of its tile-parts (a
	// tile may be split into several tile-parts, each contributing a chunk). It is
	// parsed into blocks once, after the whole codestream has been read.
	payload []byte
	poc     []POCTuple  // per-tile progression-order changes (from a tile-part header)
	rgn     map[int]int // per-tile per-component ROI max-shift (from a tile-part RGN)
	// ppt holds the concatenated PPT (packed packet headers, tile-part) payloads
	// for this tile, in tile-part then Zppt order. When non-empty (or when the
	// main header carried PPM), this tile's packet headers are read from the packed
	// stream instead of being interleaved in the SOD payload.
	ppt []byte
	// Tile-part coding/quantization overrides (from this tile's tile-part headers).
	// A tile-part QCD/COD replaces the default for the whole tile; a tile-part
	// QCC/COC overrides one component. nil/empty means "use the main header".
	tileCOD       *COD
	tileQCD       *QCD
	tileCOC       map[int]COD
	tileQCC       map[int]QCD
	blocks        []engine.BlockStream
	decodedPlanes []engine.ComponentPlane
}

func (d *Decoder) initTiles() {
	numTilesX, numTilesY := d.header.siz.NumTiles()
	n := numTilesX * numTilesY

	d.tiles = make([]tileState, n)
	for i := range d.tiles {
		t := &d.tiles[i]
		t.index = i

		ti := i % numTilesX
		tj := i / numTilesX

		t.tx0, t.ty0, t.tx1, t.ty1 = d.header.siz.TileBounds(ti, tj)
		t.tw = t.tx1 - t.tx0
		t.th = t.ty1 - t.ty0
	}
}
