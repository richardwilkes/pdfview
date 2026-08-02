// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"encoding/binary"
	"testing"
)

// TestParseICCMatrixTRCFallbacks checks that profiles we cannot reduce to a usable
// matrix/TRC RGB transform are rejected (ok=false) rather than mis-applied — the
// caller then renders the raw components. A well-formed matrix/TRC profile is parsed.
func TestParseICCMatrixTRCFallbacks(t *testing.T) {
	if _, ok := parseICCMatrixTRC(nil); ok {
		t.Error("nil profile: expected ok=false")
	}
	if _, ok := parseICCMatrixTRC(make([]byte, 200)); ok {
		t.Error("zeroed profile (no RGB space, no tags): expected ok=false")
	}

	// Build a minimal valid matrix/TRC RGB profile: RGB data space, 6 tags
	// (r/g/b TRC = gamma 1.0, r/g/b XYZ = identity-ish colorants).
	build := func(matrix [3][3]float64) []byte {
		p := make([]byte, 128) // ICC header is 128 bytes; the tag table follows
		copy(p[16:20], "RGB ")
		tags := []struct {
			sig  string
			data []byte
		}{}
		curv := func() []byte { // curv, n=1, gamma 1.0 (256 in u8Fixed8)
			b := make([]byte, 14)
			copy(b, "curv")
			binary.BigEndian.PutUint32(b[8:], 1)
			binary.BigEndian.PutUint16(b[12:], 256)
			return b
		}
		xyz := func(col int) []byte {
			b := make([]byte, 20)
			copy(b, "XYZ ")
			for row := 0; row < 3; row++ {
				binary.BigEndian.PutUint32(b[8+row*4:], uint32(int32(matrix[row][col]*65536)))
			}
			return b
		}
		for _, s := range []string{"rTRC", "gTRC", "bTRC"} {
			tags = append(tags, struct {
				sig  string
				data []byte
			}{s, curv()})
		}
		tags = append(tags,
			struct {
				sig  string
				data []byte
			}{"rXYZ", xyz(0)},
			struct {
				sig  string
				data []byte
			}{"gXYZ", xyz(1)},
			struct {
				sig  string
				data []byte
			}{"bXYZ", xyz(2)})

		n := len(tags)
		table := make([]byte, 4+n*12)
		binary.BigEndian.PutUint32(table, uint32(n))
		body := []byte{}
		dataStart := 128 + len(table)
		for i, tg := range tags {
			off := dataStart + len(body)
			copy(table[4+i*12:], tg.sig)
			binary.BigEndian.PutUint32(table[4+i*12+4:], uint32(off))
			binary.BigEndian.PutUint32(table[4+i*12+8:], uint32(len(tg.data)))
			body = append(body, tg.data...)
		}
		return append(append(p, table...), body...)
	}

	// Identity colorant matrix → invertible → accepted.
	good := build([3][3]float64{{0.4, 0.3, 0.1}, {0.2, 0.7, 0.1}, {0.0, 0.1, 0.8}})
	if _, ok := parseICCMatrixTRC(good); !ok {
		t.Error("well-formed matrix/TRC profile: expected ok=true")
	}
	// Degenerate (zero) colorant matrix → rejected.
	bad := build([3][3]float64{})
	if _, ok := parseICCMatrixTRC(bad); ok {
		t.Error("degenerate (zero matrix) profile: expected ok=false")
	}
}
