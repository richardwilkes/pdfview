// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"
	"io"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/binutil"
)

// PPM (0xFF60) and PPT (0xFF61) relocate the packet headers out of the bit-stream
// and into their own marker segments (ISO/IEC 15444-1 A.7.4 / A.7.5). With them,
// the SOD payload of a tile-part holds only packet bodies (and optional SOP
// markers); the headers — include info, zero-bit-plane tag trees, pass counts,
// length codes, and any EPH markers — live in the packed stream. The decoder
// accumulates that stream here and Tier-2 reads headers from it instead of from
// the body bit-stream (see tryParseStandardLRCP / packedPacketHeaders).
//
//   - PPT lives in a tile-part header and carries that tile-part's headers. A tile
//     (across its tile-parts) may emit several PPT markers; their Ippt payloads
//     concatenate, in marker (Zppt) order, into one per-tile header stream.
//   - PPM lives in the main header and carries the headers for ALL tile-parts of
//     the codestream as ONE continuous byte stream, framed only for transport as
//     repeated [Nppm:4][Ippm:Nppm] runs. The Nppm boundaries are arbitrary — they
//     do NOT align with tile-parts (a run may hold several tile-parts' headers, and
//     a tile-part's headers may straddle runs or PPM markers). So the marker
//     payloads are concatenated (Zppm order), the Nppm length prefixes stripped, and
//     the resulting stream consumed sequentially as tile-parts are decoded (matching
//     OpenJPEG opj_j2k_merge_ppm). d.ppmConsumed tracks the shared read offset.

// processPPT parses one PPT marker into the current tile's packed-header stream.
func (d *Decoder) processPPT() error {
	Lppt, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	if Lppt < 3 {
		return fmt.Errorf("PPT: invalid length %d", Lppt)
	}
	// Zppt (1 byte) orders multiple PPT markers within a tile-part; markers arrive
	// in stream order, which is Zppt order, so it is consumed but not used to sort.
	if _, err := binutil.ReadU8(d.r); err != nil {
		return err
	}
	body := make([]byte, int(Lppt)-3)
	if _, err := io.ReadFull(d.r, body); err != nil {
		return err
	}
	if d.section != sectionTilePartHeader {
		return fmt.Errorf("PPT: only valid in a tile-part header")
	}
	if d.cur.tileIndex < 0 || d.cur.tileIndex >= len(d.tiles) {
		return fmt.Errorf("PPT: no current tile")
	}
	t := &d.tiles[d.cur.tileIndex]
	t.ppt = append(t.ppt, body...)
	d.cur.tilePartConsumed += 2 + int(Lppt)
	return nil
}

// processPPM parses one PPM marker into the main-header packed-header stream.
func (d *Decoder) processPPM() error {
	Lppm, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	if Lppm < 3 {
		return fmt.Errorf("PPM: invalid length %d", Lppm)
	}
	if _, err := binutil.ReadU8(d.r); err != nil { // Zppm
		return err
	}
	body := make([]byte, int(Lppm)-3)
	if _, err := io.ReadFull(d.r, body); err != nil {
		return err
	}
	if d.section != sectionMainHeader {
		return fmt.Errorf("PPM: only valid in the main header")
	}
	d.header.ppm = append(d.header.ppm, body...)
	return nil
}

// packedPacketHeaders returns the packet-header byte stream available to the given
// tile when PPM or PPT is in force, or nil when the headers are interleaved in the
// SOD as usual. The second result reports whether the bytes came from the shared
// PPM stream (so the caller advances d.ppmConsumed by what it reads); PPT is
// per-tile and self-contained.
//
// PPT is per tile and already concatenated. PPM is one continuous stream shared by
// every tile-part; this returns it from the current shared offset (d.ppmConsumed),
// and the tile consumes the prefix its packets need.
func (d *Decoder) packedPacketHeaders(tileIndex int) (data []byte, fromPPM bool) {
	if tileIndex >= 0 && tileIndex < len(d.tiles) {
		if t := d.tiles[tileIndex].ppt; len(t) > 0 {
			return t, false
		}
	}
	if len(d.header.ppm) == 0 {
		return nil, false
	}
	m := d.mergedPPM()
	if d.ppmConsumed >= len(m) {
		return nil, false
	}
	return m[d.ppmConsumed:], true
}

// mergedPPM concatenates the PPM marker payloads (already in Zppm order) and strips
// the [Nppm:4] transport length prefixes, yielding the continuous packet-header
// stream for the whole codestream (OpenJPEG opj_j2k_merge_ppm). Cached.
func (d *Decoder) mergedPPM() []byte {
	if d.ppmMerged != nil {
		return d.ppmMerged
	}
	data := d.header.ppm
	merged := make([]byte, 0, len(data))
	off := 0
	for off+4 <= len(data) {
		n := int(data[off])<<24 | int(data[off+1])<<16 | int(data[off+2])<<8 | int(data[off+3])
		off += 4
		if n < 0 || off+n > len(data) {
			n = len(data) - off // tolerate a truncated final run
		}
		merged = append(merged, data[off:off+n]...)
		off += n
	}
	if merged == nil {
		merged = []byte{}
	}
	d.ppmMerged = merged
	return d.ppmMerged
}
