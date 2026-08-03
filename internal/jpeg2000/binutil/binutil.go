// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package binutil

import (
	"encoding/binary"
	"io"
)

// ReadU8 reads a single byte from the stream as uint8.
func ReadU8(r io.Reader) (uint8, error) {
	b, err := ReadN(r, 1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// ReadU16 reads a big-endian uint16 from the stream.
func ReadU16(r io.Reader) (uint16, error) {
	b, err := ReadN(r, 2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

// ReadU32 reads a big-endian uint32 from the stream.
func ReadU32(r io.Reader) (uint32, error) {
	b, err := ReadN(r, 4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

// ReadN reads exactly n bytes from the stream.
func ReadN(r io.Reader, n int64) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// DiscardN discards n bytes from the stream.
func DiscardN(r io.Reader, n int64) error {
	_, err := io.CopyN(io.Discard, r, n)
	return err
}
