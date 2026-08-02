// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package engine

// BitWriter writes bits MSB-first with JPEG 2000 packet-header bit-stuffing (ISO
// 15444-1 B.10.1): whenever an emitted byte is 0xFF, the next byte may only use its
// low 7 bits (its MSB is a stuff bit, forced to 0). It is the encoder dual of
// BitReader's header-stuffing path; a round-trip BitWriter → BitReader (BeginPacketHeader)
// reproduces the bit sequence exactly.
type BitWriter struct {
	out   []byte
	cur   byte
	nbits int // bits placed into cur so far (from the MSB)
	cap   int // capacity of the current byte: 8, or 7 after a 0xFF
}

// NewBitWriter returns an empty BitWriter.
func NewBitWriter() *BitWriter {
	return &BitWriter{cap: 8}
}

// WriteBit appends one bit (0/1), MSB-first within each byte.
func (w *BitWriter) WriteBit(b uint8) {
	w.cur |= (b & 1) << uint(w.cap-1-w.nbits)
	w.nbits++
	if w.nbits == w.cap {
		w.emit()
	}
}

// WriteBits appends the low n bits of v, most-significant bit first.
func (w *BitWriter) WriteBits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.WriteBit(uint8((v >> uint(i)) & 1))
	}
}

func (w *BitWriter) emit() {
	w.out = append(w.out, w.cur)
	if w.cur == 0xFF {
		w.cap = 7 // the next byte's MSB is a stuff bit
	} else {
		w.cap = 8
	}
	w.cur = 0
	w.nbits = 0
}

// Bytes finalises the packet header: it flushes any partial byte (low bits zero) and,
// if the final byte is 0xFF, appends a stuff byte so the decoder's HeaderAlign skips
// it. After this the BitWriter must not be written to further.
func (w *BitWriter) Bytes() []byte {
	if w.nbits > 0 {
		w.emit()
	}
	if len(w.out) > 0 && w.out[len(w.out)-1] == 0xFF {
		w.out = append(w.out, 0x00)
	}
	return w.out
}
