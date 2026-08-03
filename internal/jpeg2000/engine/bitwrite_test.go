// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package engine

import (
	"math/rand"
	"testing"
)

// TestBitWriterRoundTrip checks that bits written with the header-stuffing BitWriter
// read back identically through the BitReader's packet-header path, including runs
// that produce 0xFF bytes (which trigger the stuff bit).
func TestBitWriterRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for trial := 0; trial < 2000; trial++ {
		n := rng.Intn(500)
		bits := make([]uint8, n)
		w := NewBitWriter()
		for i := range bits {
			// Bias toward 1s so 0xFF bytes (and stuffing) occur often.
			if rng.Intn(4) != 0 {
				bits[i] = 1
			}
			w.WriteBit(bits[i])
		}
		data := w.Bytes()

		br := NewRawBitReader(data)
		br.BeginPacketHeader()
		for i := range bits {
			b, ok := br.ReadBit()
			if !ok {
				t.Fatalf("trial %d: ran out of bits at %d/%d", trial, i, n)
			}
			if b != bits[i] {
				t.Fatalf("trial %d: bit %d: wrote %d, read %d", trial, i, bits[i], b)
			}
		}
	}
}

// TestTagTreeRoundTrip checks the tag-tree encoder against the decoder: encode all
// leaf values progressively (raster order, shared state) and decode them back. Covers
// both ProgressiveEncodeValue (full value, e.g. zero-bit-planes) and the
// ProgressiveEncodeBool inclusion form at a fixed threshold.
func TestTagTreeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(6))
	for trial := 0; trial < 1000; trial++ {
		w := 1 + rng.Intn(12)
		h := 1 + rng.Intn(12)
		maxVal := 1 + rng.Intn(20)
		vals := make([]int, w*h)
		for i := range vals {
			vals[i] = rng.Intn(maxVal)
		}

		// Encode full values.
		enc := NewTagTree(w, h)
		for i, v := range vals {
			enc.Set(i%w, i/w, v)
		}
		enc.ResetKnown()
		nv := enc.nodeVals()
		bw := NewBitWriter()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				enc.ProgressiveEncodeValue(bw, nv, x, y, 16)
			}
		}
		data := bw.Bytes()

		dec := NewTagTree(w, h)
		dec.ResetKnown()
		br := NewRawBitReader(data)
		br.BeginPacketHeader()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				got, ok := dec.ProgressiveDecodeValue(br, x, y, 16)
				if !ok {
					t.Fatalf("trial %d value: decode failed at (%d,%d)", trial, x, y)
				}
				if got != vals[y*w+x] {
					t.Fatalf("trial %d value: (%d,%d): encoded %d, decoded %d", trial, x, y, vals[y*w+x], got)
				}
			}
		}

		// Inclusion form: at threshold `th`, a leaf is "included" iff its value < th.
		th := rng.Intn(maxVal + 1)
		enc.ResetKnown()
		nv = enc.nodeVals()
		bw = NewBitWriter()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				enc.ProgressiveEncodeBool(bw, nv, x, y, th)
			}
		}
		data = bw.Bytes()

		dec.ResetKnown()
		br = NewRawBitReader(data)
		br.BeginPacketHeader()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				inc, ok := dec.ProgressiveDecodeBool(br, x, y, th)
				if !ok {
					t.Fatalf("trial %d incl: decode failed at (%d,%d)", trial, x, y)
				}
				if inc != (vals[y*w+x] < th) {
					t.Fatalf("trial %d incl th=%d: (%d,%d) value %d: encoded incl=%v, decoded %v",
						trial, th, x, y, vals[y*w+x], vals[y*w+x] < th, inc)
				}
			}
		}
	}
}
