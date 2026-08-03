// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"
	"io"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/engine"
)

func (d *Decoder) processSOD() error {
	if d.section != sectionTilePartHeader {
		return fmt.Errorf("SOD: unexpected in section %v", d.section)
	}
	if !d.cur.active {
		return fmt.Errorf("SOD: no active tile-part")
	}
	if d.cur.tileIndex < 0 || d.cur.tileIndex >= len(d.tiles) {
		return fmt.Errorf("SOD: invalid current tile index %d", d.cur.tileIndex)
	}
	tile := &d.tiles[d.cur.tileIndex]

	d.section = sectionTilePartData

	// Psot=0 (ISO 15444-1 A.4.2): the tile-part length is unspecified and the
	// tile-part runs to the EOC marker or the end of the codestream. By the spec this
	// can only be the final tile-part. Read everything remaining and trim a trailing
	// EOC (0xFF 0xD9) if present — packet data is bit-stuffed so the marker cannot
	// occur inside the payload. The main loop then hits EOF and finalizes.
	if d.cur.sot.Psot == 0 {
		rest, err := io.ReadAll(io.LimitReader(d.r, int64(maxTilePartBytes)+1))
		if err != nil {
			return fmt.Errorf("SOD payload (Psot=0) read failed: %w", err)
		}
		if len(rest) > maxTilePartBytes {
			return fmt.Errorf("SOD: tile-part too large (Psot=0): >%d bytes", maxTilePartBytes)
		}
		// Trim a trailing EOC marker so it is not parsed as packet data.
		if n := len(rest); n >= 2 && rest[n-2] == 0xFF && rest[n-1] == 0xD9 {
			rest = rest[:n-2]
		}
		tile.payload = append(tile.payload, rest...)
		d.section = sectionMainHeader
		d.cur.active = false
		return nil
	}

	// Include the SOD marker itself as part of the consumed tile-part bytes.
	consumed := d.cur.tilePartConsumed + 2

	if int(d.cur.sot.Psot) < consumed {
		return fmt.Errorf("SOD: Psot too small: Psot=%d consumed=%d", d.cur.sot.Psot, consumed)
	}
	remaining := int(d.cur.sot.Psot) - consumed
	// Bound the buffer a declared Psot can demand. maxTilePartBytes is the absolute
	// ceiling, but a tile-part cannot be larger than the input that carries it, so also
	// reject a Psot exceeding the bytes actually left in the reader when that is knowable
	// (the codestream and container paths both hand this decoder a *bytes.Reader, which
	// reports its unread length via Len). That refuses a hostile Psot up front instead of
	// allocating make([]byte, remaining) the input can never fill. A reader that does not
	// expose a remaining length is read incrementally (io.ReadAll grows only to the bytes
	// present), so no single up-front allocation exceeds what the input can supply.
	if remaining > maxTilePartBytes {
		return fmt.Errorf("SOD: tile-part too large: %d bytes", remaining)
	}
	if lr, ok := d.r.(interface{ Len() int }); ok && remaining > lr.Len() {
		return fmt.Errorf("SOD: tile-part exceeds remaining input: Psot wants %d, %d available", remaining, lr.Len())
	}

	// Read this tile-part's SOD payload and append it to the tile. A tile split
	// into several tile-parts is reassembled here; the concatenated payload is
	// parsed into code-blocks once, in parseTilePayload (called at finalize).
	var payload []byte
	if _, ok := d.r.(interface{ Len() int }); ok {
		payload = make([]byte, remaining)
		readN, err := io.ReadFull(d.r, payload)
		if err != nil {
			return fmt.Errorf("SOD payload read failed (expected %d, read %d): %w", remaining, readN, err)
		}
	} else {
		var err error
		if payload, err = io.ReadAll(io.LimitReader(d.r, int64(remaining))); err != nil {
			return fmt.Errorf("SOD payload read failed: %w", err)
		}
		if len(payload) != remaining {
			return fmt.Errorf("SOD payload read failed (expected %d, read %d)", remaining, len(payload))
		}
	}
	tile.payload = append(tile.payload, payload...)
	d.cur.tilePartConsumed = consumed + remaining

	d.section = sectionMainHeader
	d.cur.active = false
	return nil
}

// parseTilePayload parses a tile's accumulated SOD payload (reassembled across all
// of its tile-parts) into Tier-1 code-block streams. It must run after the whole
// codestream has been read so that every tile-part has contributed.
func (d *Decoder) parseTilePayload(tileIndex int) error {
	if tileIndex < 0 || tileIndex >= len(d.tiles) {
		return fmt.Errorf("parseTilePayload: invalid tile index %d", tileIndex)
	}
	d.cur.tileIndex = tileIndex
	tile := &d.tiles[tileIndex]

	// A tile with no tile-part data at all: the codestream's SIZ declares a tile grid
	// but provides no SOT/SOD for this tile (e.g. only the first of several declared
	// tiles is actually coded — issue726 in the nonregression corpus). OpenJPEG decodes
	// the present tiles and leaves the rest blank; match that by reconstructing this
	// tile as all-zero coefficients instead of failing to parse a non-existent packet
	// stream.
	if len(tile.payload) == 0 {
		d.fillZeroTile(tile)
		return nil
	}

	bs, _, ok, err := d.parseLRCPPacketHeaders(tile.payload)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tile %d: failed to parse packets from payload", tileIndex)
	}

	// If no code-blocks were included (e.g., all-zero coefficients), fill the tile with
	// DC-level blocks so the Tier-1 raw path yields zero coefficients.
	if len(bs) == 0 {
		d.fillZeroTile(tile)
		return nil
	}

	numComps := len(d.header.siz.Components)
	numResolutions := d.header.cod.Levels + 1
	numBands := 1 + 3*(numResolutions-1)
	tile.blocks = make([]engine.BlockStream, 0, numComps*numBands)
	tile.blocks = append(tile.blocks, bs...)
	return nil
}

// fillZeroTile reconstructs a tile as all-zero coefficients: one DC-level raw "LL"
// block per component at the component's (subsampled) tile size. Used when a tile has
// no included code-blocks, or no tile-part data at all. The DC value re-centres an
// unsigned component (the Tier-1 raw path subtracts it back to zero); a signed
// component is already centred on zero, so the raw bytes stay zero.
func (d *Decoder) fillZeroTile(tile *tileState) {
	for c := 0; c < len(d.header.siz.Components); c++ {
		comp := d.header.siz.Components[c]
		cw := (tile.tw + comp.XRsiz - 1) / comp.XRsiz
		ch := (tile.th + comp.YRsiz - 1) / comp.YRsiz
		size := cw * ch
		bytesPer := 1
		if comp.Precision > 8 {
			bytesPer = 2
			size *= 2
		}
		raw := make([]byte, size)
		if !comp.Signed {
			dc := 1 << uint(comp.Precision-1)
			if bytesPer == 1 {
				for i := range raw {
					raw[i] = byte(dc)
				}
			} else {
				for i := 0; i < cw*ch; i++ {
					raw[2*i] = byte(dc >> 8)
					raw[2*i+1] = byte(dc)
				}
			}
		}
		tile.blocks = append(tile.blocks, engine.BlockStream{
			Comp: c, Level: 0, Band: "LL", W: cw, H: ch, Bytes: raw, Entropy: false,
		})
	}
}
