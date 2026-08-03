// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package engine

import (
	"fmt"
	"io"
)

// Minimal bit/byte reader for Tier-1 bytestreams with JPEG 2000 byte-stuffing handling.
// In JPEG 2000, within entropy-coded segments, a byte 0xFF is followed by a stuffed 0x00
// to avoid emulating marker codes. The stuffed 0x00 must be removed and not treated as data.

type BitReader struct {
	src     []byte
	i       int    // index into src
	cur     uint32 // a bit buffer
	nbits   int    // number of valid bits in cur
	err     error
	prevFF  bool // the last raw byte read was 0xFF (to detect stuffed 0x00)
	strict  bool // when true, flag non-stuffed 0xFF as an error (sticky)
	noStuff bool // when true, disable byte stuffing handling (for packet headers)

	// headerStuff enables JPEG 2000 packet-header bit-stuffing (ISO 15444-1
	// B.10.1): whenever a 0xFF byte is consumed, the most significant bit of
	// the following byte is a stuff bit and only its low 7 bits are valid.
	// This differs from the entropy-data stuffing (0xFF -> skip a 0x00 byte).
	headerStuff bool
}

// NewBitReader creates a BitReader over the provided buffer.
func NewBitReader(buf []byte) *BitReader {
	return &BitReader{src: buf}
}

// NewRawBitReader creates a BitReader without byte stuffing handling (for packet headers).
func NewRawBitReader(buf []byte) *BitReader {
	return &BitReader{src: buf, noStuff: true}
}

// fill ensures at least k bits are available in cur, unless EOF.
func (br *BitReader) fill(k int) {
	for br.nbits < k && br.i < len(br.src) {
		b := br.src[br.i]
		br.i++
		// Packet-header bit-stuffing: after a 0xFF byte, the next byte's MSB is a
		// stuff bit; consume only its low 7 bits.
		if br.headerStuff {
			if br.prevFF {
				br.cur = (br.cur << 7) | uint32(b&0x7F)
				br.nbits += 7
			} else {
				br.cur = (br.cur << 8) | uint32(b)
				br.nbits += 8
			}
			br.prevFF = b == 0xFF
			continue
		}
		// Handle byte stuffing: if previous byte was 0xFF and this byte is 0x00, skip it.
		if !br.noStuff && br.prevFF {
			if b == 0x00 { // stuffed 0x00 -> skip
				br.prevFF = false
				continue
			}
			// If not 0x00, still proceed; in raw Tier‑1 data, 0xFF bytes must be stuffed.
			// In non-strict mode we don't want to error here; higher layers may validate stream boundaries.
			// In strict mode, record a sticky error (decoding continues; caller can check Err()).
			if br.strict && br.err == nil {
				br.err = fmt.Errorf("bitreader: non-stuffed 0xFF encountered before 0x%02X", b)
			}
		}
		br.prevFF = b == 0xFF

		br.cur = (br.cur << 8) | uint32(b)
		br.nbits += 8
	}
}

// ReadBit returns the next single bit as 0 or 1; ok=false on EOF.
func (br *BitReader) ReadBit() (bit uint8, ok bool) {
	br.fill(1)
	if br.nbits == 0 {
		return 0, false
	}
	br.nbits--
	bit = uint8((br.cur >> uint(br.nbits)) & 1)
	br.cur &= 1<<uint(br.nbits) - 1
	return bit, true
}

// ReadBits reads up to 24 bits and returns value; ok=false on EOF before any bit.
func (br *BitReader) ReadBits(n int) (val uint32, ok bool) {
	if n <= 0 {
		return 0, true
	}
	if n > 24 {
		n = 24
	}
	br.fill(n)
	if br.nbits == 0 {
		return 0, false
	}
	if br.nbits < n {
		// Return remaining bits
		val = br.cur & ((1 << uint(br.nbits)) - 1)
		br.nbits = 0
		br.cur = 0
		return val, true
	}
	shift := br.nbits - n
	val = (br.cur >> uint(shift)) & ((1 << uint(n)) - 1)
	br.cur &= 1<<uint(shift) - 1
	br.nbits -= n
	return val, true
}

// AlignToByte discards residual bits to align to the next byte boundary.
func (br *BitReader) AlignToByte() {
	rem := br.nbits % 8
	if rem > 0 {
		// drop rem bits
		br.nbits -= rem
		br.cur &= 1<<uint(br.nbits) - 1
	}
}

// ReadByte reads the next data byte (after stuffing removal) when aligned.
// Returns io.EOF when no more data is available.
func (br *BitReader) ReadByte() (byte, error) {
	br.AlignToByte()
	br.fill(8)
	if br.nbits < 8 {
		if br.nbits == 0 {
			return 0, io.EOF
		}
		val := byte(br.cur & ((1 << uint(br.nbits)) - 1))
		br.cur = 0
		br.nbits = 0
		return val, nil
	}
	val := byte((br.cur >> uint(br.nbits-8)) & 0xFF)
	br.nbits -= 8
	br.cur &= 1<<uint(br.nbits) - 1
	return val, nil
}

// BeginPacketHeader starts a fresh packet-header bit stream with bit-stuffing
// enabled. JPEG 2000 re-initializes the bit-input context at the start of every
// packet header (the stuffing context does not carry across packet bodies), so
// the buffered bits and the 0xFF-tracking state are reset. The reader must be on
// a byte boundary (true after the previous packet body / SOP marker).
func (br *BitReader) BeginPacketHeader() {
	br.headerStuff = true
	br.prevFF = false
	br.cur = 0
	br.nbits = 0
}

// HeaderAlign terminates a packet-header bit stream and aligns to the next byte
// boundary for the body. Per ISO 15444-1 B.10.1, if the last consumed header
// byte was 0xFF, the following byte is a stuff byte and is skipped. Disables
// header bit-stuffing so subsequent ReadByte calls return raw body bytes.
func (br *BitReader) HeaderAlign() {
	if br.prevFF && br.i < len(br.src) {
		// The last consumed header byte was 0xFF, so the next byte is a stuff
		// byte (not yet loaded, since prevFF is still set); skip it.
		br.i++
	}
	br.headerStuff = false
	br.prevFF = false
	br.cur = 0
	br.nbits = 0
}

// Err returns the first sticky error encountered (not used yet, reserved).
func (br *BitReader) Err() error { return br.err }

// SetStrictStuffing enables or disables strict stuffing validation.
// When enabled, encountering a 0xFF not followed by 0x00 sets a sticky error
// retrievable via Err(). Decoding is not aborted automatically.
func (br *BitReader) SetStrictStuffing(on bool) { br.strict = on }

// Skip discards n bytes, aligning to a byte boundary first.
func (br *BitReader) Skip(n int) {
	br.AlignToByte()
	for i := 0; i < n; i++ {
		br.fill(8)
		if br.nbits >= 8 {
			br.nbits -= 8
			br.cur &= 1<<uint(br.nbits) - 1
		}
	}
}

// BytesConsumed returns the number of source bytes consumed so far
// (ignoring bit-level buffering). Useful for header accounting.
func (br *BitReader) BytesConsumed() int { return br.i }
