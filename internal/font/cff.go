package font

import (
	"errors"
	"math"
	"strconv"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// A minimal CFF (Compact Font Format) container reader, written against Adobe TN5176. go-text's cff package
// provides charstring interpretation and glyph loading but does not expose the Top DICT's FontBBox or
// FontMatrix, which the engine needs because FreeType — and therefore the oracle's MuPDF build — takes a bare
// CFF font's ascender/descender from its FontBBox (see internal/font's package comment). The INDEX and DICT
// walkers here are also the base for the CID-keyed charset/FDSelect reader that lands with Type0 support.

var errBadCFF = errors.New("malformed CFF data")

// cffTop is the Top DICT subset the engine consumes.
type cffTop struct {
	bbox      [4]float32 // FontBBox: x0, y0, x1, y1 in font units (0 0 0 0 when absent)
	matrix    [6]float32 // FontMatrix (0.001 0 0 0.001 0 0 default)
	hasBBox   bool
	hasMatrix bool
}

// parseCFFTopDict reads the header, skips the Name INDEX, and decodes the first Top DICT.
func parseCFFTopDict(data []byte) (*cffTop, error) {
	if len(data) < 4 {
		return nil, errBadCFF
	}
	hdrSize := int(data[2])
	if hdrSize < 4 || hdrSize > len(data) {
		return nil, errBadCFF
	}
	pos, err := cffSkipIndex(data, hdrSize) // Name INDEX
	if err != nil {
		return nil, err
	}
	entries, _, err := cffIndex(data, pos, 1)
	if err != nil || len(entries) == 0 {
		return nil, errBadCFF
	}
	top := &cffTop{matrix: [6]float32{0.001, 0, 0, 0.001, 0, 0}}
	if err = cffWalkDict(entries[0], func(op int, operands []float64) {
		switch {
		case op == 5 && len(operands) >= 4: // FontBBox
			for i := range 4 {
				top.bbox[i] = float32(operands[i])
			}
			top.hasBBox = true
		case op == 0x0c07 && len(operands) >= 6: // FontMatrix (escaped operator 12 7)
			for i := range 6 {
				top.matrix[i] = float32(operands[i])
			}
			top.hasMatrix = true
		}
	}); err != nil {
		return nil, err
	}
	return top, nil
}

// metrics converts the Top DICT to em-normalized ascender/descender the FreeType way: the FontBBox's
// yMax/yMin divided by the units-per-em implied by the FontMatrix (1/|yy|, 1000 for the standard matrix).
func (t *cffTop) metrics() (asc, desc float32, ok bool) {
	if !t.hasBBox || (t.bbox[1] == 0 && t.bbox[3] == 0) {
		return 0, 0, false
	}
	upem := float32(1000)
	if t.hasMatrix && t.matrix[3] != 0 {
		yy := t.matrix[3]
		if yy < 0 {
			yy = -yy
		}
		upem = 1 / yy
	}
	if upem <= 0 || math.IsNaN(float64(upem)) || math.IsInf(float64(upem), 0) {
		return 0, 0, false
	}
	yMin, yMax := t.bbox[1], t.bbox[3]
	if yMin > yMax {
		yMin, yMax = yMax, yMin
	}
	return yMax / upem, yMin / upem, true
}

// cffIndex reads an INDEX at pos, returning up to maxEntries entry slices and the offset just past the INDEX.
// An INDEX is: count (Card16), offSize (Card8, 1-4), count+1 offsets (1-based), then the data.
func cffIndex(data []byte, pos, maxEntries int) (entries [][]byte, next int, err error) {
	if pos < 0 || pos+2 > len(data) {
		return nil, 0, errBadCFF
	}
	count := int(data[pos])<<8 | int(data[pos+1])
	pos += 2
	if count == 0 {
		return nil, pos, nil
	}
	if pos >= len(data) {
		return nil, 0, errBadCFF
	}
	offSize := int(data[pos])
	pos++
	if offSize < 1 || offSize > 4 {
		return nil, 0, errBadCFF
	}
	offEnd := pos + (count+1)*offSize
	if offEnd < pos || offEnd > len(data) {
		return nil, 0, errBadCFF
	}
	offset := func(i int) int {
		v := 0
		for b := range offSize {
			v = v<<8 | int(data[pos+i*offSize+b])
		}
		return v
	}
	dataStart := offEnd - 1 // Offsets are 1-based from the byte before the data.
	last := offset(count)
	end := dataStart + last
	if last < 1 || end < dataStart || end > len(data) {
		return nil, 0, errBadCFF
	}
	n := min(count, maxEntries)
	entries = make([][]byte, 0, n)
	for i := range n {
		lo, hi := offset(i), offset(i+1)
		if lo < 1 || hi < lo || dataStart+hi > len(data) {
			return nil, 0, errBadCFF
		}
		entries = append(entries, data[dataStart+lo:dataStart+hi])
	}
	return entries, end, nil
}

// cffSkipIndex advances past an INDEX without materializing entries.
func cffSkipIndex(data []byte, pos int) (int, error) {
	_, next, err := cffIndex(data, pos, 0)
	return next, err
}

// cffWalkDict decodes DICT tokens (TN5176 table 3/4), invoking fn for each operator with its operands.
func cffWalkDict(dict []byte, fn func(op int, operands []float64)) error {
	var operands []float64
	const maxDictOperands = 48 // The largest legal operand count is small; floods are hostile.
	for i := 0; i < len(dict); {
		b0 := int(dict[i])
		switch {
		case b0 <= 21: // Operator.
			op := b0
			i++
			if b0 == 12 {
				if i >= len(dict) {
					return errBadCFF
				}
				op = 0x0c00 | int(dict[i])
				i++
			}
			fn(op, operands)
			operands = operands[:0]
			continue
		case b0 == 28:
			if i+3 > len(dict) {
				return errBadCFF
			}
			operands = append(operands, float64(int16(uint16(dict[i+1])<<8|uint16(dict[i+2]))))
			i += 3
		case b0 == 29:
			if i+5 > len(dict) {
				return errBadCFF
			}
			v := uint32(dict[i+1])<<24 | uint32(dict[i+2])<<16 | uint32(dict[i+3])<<8 | uint32(dict[i+4])
			operands = append(operands, float64(int32(v)))
			i += 5
		case b0 == 30: // Real: packed BCD nibbles until 0xf.
			v, n, err := cffReal(dict[i+1:])
			if err != nil {
				return err
			}
			operands = append(operands, v)
			i += 1 + n
		case b0 >= 32 && b0 <= 246:
			operands = append(operands, float64(b0-139))
			i++
		case b0 >= 247 && b0 <= 250:
			if i+2 > len(dict) {
				return errBadCFF
			}
			operands = append(operands, float64((b0-247)*256+int(dict[i+1])+108))
			i += 2
		case b0 >= 251 && b0 <= 254:
			if i+2 > len(dict) {
				return errBadCFF
			}
			operands = append(operands, float64(-(b0-251)*256-int(dict[i+1])-108))
			i += 2
		default: // 22..27 and 31 are reserved.
			return errBadCFF
		}
		if len(operands) > maxDictOperands {
			return errBadCFF
		}
	}
	return nil
}

// cffReal decodes a packed-BCD real, returning its value and the bytes consumed.
func cffReal(data []byte) (value float64, consumed int, err error) {
	var sb []byte
	for i := range data {
		for _, nib := range [2]byte{data[i] >> 4, data[i] & 0xf} {
			switch {
			case nib <= 9:
				sb = append(sb, '0'+nib)
			case nib == 0xa:
				sb = append(sb, '.')
			case nib == 0xb:
				sb = append(sb, 'E')
			case nib == 0xc:
				sb = append(sb, 'E', '-')
			case nib == 0xe:
				sb = append(sb, '-')
			case nib == 0xf:
				v, parseErr := parseCFFFloat(string(sb))
				if parseErr != nil {
					return 0, 0, errBadCFF
				}
				return v, i + 1, nil
			default: // 0xd is reserved.
				return 0, 0, errBadCFF
			}
		}
		if len(sb) > 64 {
			return 0, 0, errBadCFF
		}
	}
	return 0, 0, errBadCFF
}

// parseCFFFloat parses the ASCII form assembled from packed BCD, rejecting non-finite results.
func parseCFFFloat(s string) (float64, error) {
	if s == "" {
		return 0, errBadCFF
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errBadCFF
	}
	return v, nil
}

// parseCFFTopFromStream decodes a FontFile3 stream and extracts its Top DICT, tolerating hostile bytes.
func parseCFFTopFromStream(d *cos.Document, s *cos.Stream) *cffTop {
	raw, err := d.StreamData(s)
	if err != nil || len(raw) == 0 {
		return nil
	}
	top, err := parseCFFTopDict(raw)
	if err != nil {
		return nil
	}
	return top
}
