// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package box

import (
	"encoding/binary"
	"fmt"
	"io"
)

// JP2 box types.
const (
	boxCodestream = 0x6A703263 // 'jp2c'
	boxJP2Header  = 0x6A703268 // 'jp2h' (superbox: ihdr, colr, cdef, pclr, cmap, …)
	boxColr       = 0x636F6C72 // 'colr' (colour specification)
	boxCdef       = 0x63646566 // 'cdef' (channel definition)
	boxPclr       = 0x70636C72 // 'pclr' (palette)
	boxCmap       = 0x636D6170 // 'cmap' (component mapping)
)

// maxPaletteEntries caps the palette size (ISO allows up to 1024 entries).
const maxPaletteEntries = 1024

// maxCMapChannels caps the number of output channels a `cmap` box may define. Each
// channel becomes a full w×h plane at palette expansion (see codestream.applyPalette),
// so a hostile cmap listing thousands of channels would allocate that many image-sized
// planes. A device colour space needs at most four channels, plus opacity and auxiliary
// channels stays well under this; a longer cmap is malformed and refused, so the
// container falls back to its non-palette handling rather than mis-rendering.
const maxCMapChannels = 32

// maxCodestreamBytes caps the jp2c payload we will buffer, so a hostile box
// length cannot trigger an unbounded allocation. Far above any real test image.
const maxCodestreamBytes = 1 << 28 // 256 MiB

// maxHeaderBytes caps the jp2h superbox we will buffer (metadata is tiny).
const maxHeaderBytes = 1 << 20 // 1 MiB

// ChannelDef is one entry of a `cdef` box: channel index Cn, type Typ (0=colour,
// 1=opacity/alpha, 2=premultiplied opacity), and the colour it is associated with
// (Asoc: 1-based colour index, 0 = whole image, 0xFFFF = none).
type ChannelDef struct {
	Cn, Typ, Asoc uint16
}

// Palette is a parsed `pclr` box: a lookup table of NumEntries rows, each with
// NumColumns generated channel values. BitDepths[col] is the precision of column
// col (1..16, sign ignored here — palettes in practice are unsigned). Entries is
// [NumEntries][NumColumns].
type Palette struct {
	NumEntries int
	NumColumns int
	BitDepths  []int
	Entries    [][]int32
}

// CMapEntry is one entry of a `cmap` box: it defines an output channel as either a
// direct copy of codestream component Component (Type 0) or a palette lookup
// (Type 1) into palette column Column, indexed by component Component.
type CMapEntry struct {
	Component int // CMP
	Type      int // MTYP: 0 = direct, 1 = palette
	Column    int // PCOL
}

// JP2Info is the parsed JP2 container: the embedded codestream plus the colour /
// channel metadata needed to interpret it.
type JP2Info struct {
	Codestream []byte
	// EnumCS is the `colr` enumerated colour space (16 = sRGB, 17 = greyscale,
	// 18 = sYCC); -1 when there is no enumerated colr (e.g. an ICC profile).
	EnumCS int
	// CIELab carries the explicit range/offset parameters of an enumerated CIELab
	// colr box (EnumCS 14); nil when absent (use the CIELab defaults) or not CIELab.
	CIELab *CIELabParams
	// ICCProfile is the raw embedded ICC profile of a colr box with METH==2; nil when
	// the colour space is enumerated.
	ICCProfile []byte
	// Channels holds the `cdef` channel definitions, or nil when absent.
	Channels []ChannelDef
	// Palette holds the `pclr` lookup table, or nil when the image is not indexed.
	Palette *Palette
	// CMap holds the `cmap` component-mapping entries, or nil when absent.
	CMap []CMapEntry
}

// readBoxHeader reads the next box header. Returns total box length (lbox) and header size (hsize).
func readBoxHeader(r io.Reader) (lbox int64, tbox uint32, hsize int64, err error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, 0, 0, err
	}
	lbox = int64(binary.BigEndian.Uint32(hdr[0:4]))
	tbox = binary.BigEndian.Uint32(hdr[4:8])
	hsize = 8

	if lbox == 1 {
		// XLBox case (64-bit length)
		var xl [8]byte
		if _, err := io.ReadFull(r, xl[:]); err != nil {
			return 0, 0, 0, err
		}
		lbox = int64(binary.BigEndian.Uint64(xl[:]))
		hsize = 16
	}
	return lbox, tbox, hsize, nil
}

// ParseJP2 parses a JP2 container: it reads the `jp2h` superbox (colr / cdef) and
// extracts the `jp2c` codestream.
func ParseJP2(r io.Reader) (*JP2Info, error) {
	info := &JP2Info{EnumCS: -1}
	for {
		lbox, tbox, hsize, err := readBoxHeader(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		switch tbox {
		case boxCodestream:
			// The jp2c codestream is the final box. A truncated file (a real
			// occurrence — e.g. a partially-downloaded image) declares more bytes than
			// it carries; tolerate a short read and decode the partial codestream, as
			// OpenJPEG does. The codestream decoder already handles a stream that ends
			// without an EOC marker.
			data, err := readBoxBody(r, lbox, hsize, maxCodestreamBytes, "jp2c", true)
			if err != nil {
				return nil, err
			}
			info.Codestream = data
			return info, nil // jp2c is the last box we need

		case boxJP2Header:
			body, err := readBoxBody(r, lbox, hsize, maxHeaderBytes, "jp2h", false)
			if err != nil {
				return nil, err
			}
			parseJP2Header(body, info)

		default:
			if err := skipBox(r, lbox, hsize); err != nil {
				return nil, err
			}
		}
	}
	if info.Codestream == nil {
		return nil, fmt.Errorf("jp2c box not found in JP2 container")
	}
	return info, nil
}

// ExtractCodestreamFromJP2 parses the JP2 container and returns the raw codestream.
func ExtractCodestreamFromJP2(r io.Reader) ([]byte, error) {
	info, err := ParseJP2(r)
	if err != nil {
		return nil, err
	}
	return info.Codestream, nil
}

// readBoxBody reads a box's content (lbox==0 means "to end of stream"). When
// allowShort is set, a box whose declared length exceeds the bytes actually present
// is not an error: the available prefix is returned (used for the final jp2c
// codestream box of a truncated file, which can still be partially decoded).
func readBoxBody(r io.Reader, lbox, hsize, max int64, name string, allowShort bool) ([]byte, error) {
	if lbox == 0 {
		return io.ReadAll(io.LimitReader(r, max))
	}
	dataLen := lbox - hsize
	if dataLen < 0 {
		return nil, fmt.Errorf("invalid %s box length %d (hsize %d)", name, lbox, hsize)
	}
	if dataLen > max {
		if !allowShort {
			return nil, fmt.Errorf("%s box too large: %d bytes", name, dataLen)
		}
		dataLen = max
	}
	data := make([]byte, dataLen)
	n, err := io.ReadFull(r, data)
	if err != nil {
		if allowShort && (err == io.ErrUnexpectedEOF || err == io.EOF) {
			return data[:n], nil
		}
		return nil, fmt.Errorf("read %s failed (expected %d bytes, read %d): %w", name, dataLen, n, err)
	}
	return data, nil
}

// skipBox advances past a box whose content we do not need.
func skipBox(r io.Reader, lbox, hsize int64) error {
	if lbox == 0 {
		// Unknown-length box that extends to EOF and isn't one we want.
		_, err := io.Copy(io.Discard, r)
		return err
	}
	skipLen := lbox - hsize
	if skipLen <= 0 {
		return nil
	}
	if seeker, ok := r.(io.Seeker); ok {
		_, err := seeker.Seek(skipLen, io.SeekCurrent)
		return err
	}
	_, err := io.CopyN(io.Discard, r, skipLen)
	return err
}

// parseJP2Header walks the sub-boxes of a `jp2h` superbox for colr and cdef.
func parseJP2Header(buf []byte, info *JP2Info) {
	off := 0
	for off+8 <= len(buf) {
		lbox := int(binary.BigEndian.Uint32(buf[off : off+4]))
		tbox := binary.BigEndian.Uint32(buf[off+4 : off+8])
		hsize := 8
		end := off + lbox
		if lbox == 0 {
			end = len(buf) // to end of superbox
		}
		if lbox == 1 { // 64-bit length: skip (not used in jp2h sub-boxes we read)
			break
		}
		if lbox < hsize || end > len(buf) {
			break
		}
		content := buf[off+hsize : end]
		switch tbox {
		case boxColr:
			parseColr(content, info)
		case boxCdef:
			parseCdef(content, info)
		case boxPclr:
			parsePclr(content, info)
		case boxCmap:
			parseCmap(content, info)
		}
		off = end
	}
}

// CIELabParams holds the explicit range (R) and offset (O) parameters of an
// enumerated CIELab colr box (one per L, a, b channel). They map each integer sample
// to its CIELab value: v = (R·sample)/(2^prec−1) − (R·O)/(2^prec−1).
type CIELabParams struct{ RL, OL, RA, OA, RB, OB uint32 }

// parseColr reads a colour specification box: METH==1 enumerated (and, for CIELab,
// its optional range/offset parameters) or METH==2 an embedded ICC profile.
func parseColr(c []byte, info *JP2Info) {
	if len(c) < 3 {
		return
	}
	switch c[0] { // METH
	case 1:
		if len(c) < 7 {
			return
		}
		info.EnumCS = int(binary.BigEndian.Uint32(c[3:7]))
		// Enumerated CIELab (EnumCS 14) may carry explicit range/offset parameters
		// (24 bytes after EnumCS); their absence means the CIELab defaults apply.
		if info.EnumCS == 14 && len(c) >= 31 {
			u := func(i int) uint32 { return binary.BigEndian.Uint32(c[i : i+4]) }
			info.CIELab = &CIELabParams{RL: u(7), OL: u(11), RA: u(15), OA: u(19), RB: u(23), OB: u(27)}
		}
	case 2:
		// Restricted ICC profile: the profile bytes follow METH/PREC/APPROX.
		if len(c) > 3 {
			info.ICCProfile = append([]byte(nil), c[3:]...)
		}
	}
}

// parseCdef reads the channel-definition entries.
func parseCdef(c []byte, info *JP2Info) {
	if len(c) < 2 {
		return
	}
	n := int(binary.BigEndian.Uint16(c[0:2]))
	if 2+6*n > len(c) {
		return
	}
	defs := make([]ChannelDef, 0, n)
	for i := 0; i < n; i++ {
		o := 2 + 6*i
		defs = append(defs, ChannelDef{
			Cn:   binary.BigEndian.Uint16(c[o : o+2]),
			Typ:  binary.BigEndian.Uint16(c[o+2 : o+4]),
			Asoc: binary.BigEndian.Uint16(c[o+4 : o+6]),
		})
	}
	info.Channels = defs
}

// parsePclr reads a palette box: NE (U16) entries, NPC (U8) columns, NPC bit-depth
// bytes (Bi: low 7 bits = depth-1), then the LUT (NE × NPC values, each ceil(Bi/8)
// bytes, big-endian).
func parsePclr(c []byte, info *JP2Info) {
	if len(c) < 3 {
		return
	}
	ne := int(binary.BigEndian.Uint16(c[0:2]))
	npc := int(c[2])
	if ne < 1 || ne > maxPaletteEntries || npc < 1 || npc > 255 {
		return
	}
	if 3+npc > len(c) {
		return
	}
	depths := make([]int, npc)
	colBytes := make([]int, npc)
	for j := 0; j < npc; j++ {
		bi := c[3+j]
		depths[j] = int(bi&0x7F) + 1
		colBytes[j] = (depths[j] + 7) / 8
	}
	off := 3 + npc
	rowBytes := 0
	for j := 0; j < npc; j++ {
		rowBytes += colBytes[j]
	}
	if off+ne*rowBytes > len(c) {
		return
	}
	entries := make([][]int32, ne)
	for i := 0; i < ne; i++ {
		row := make([]int32, npc)
		for j := 0; j < npc; j++ {
			var v uint32
			for b := 0; b < colBytes[j]; b++ {
				v = (v << 8) | uint32(c[off])
				off++
			}
			row[j] = int32(v)
		}
		entries[i] = row
	}
	info.Palette = &Palette{NumEntries: ne, NumColumns: npc, BitDepths: depths, Entries: entries}
}

// parseCmap reads a component-mapping box: a sequence of (CMP U16, MTYP U8, PCOL
// U8) entries, one per output channel.
func parseCmap(c []byte, info *JP2Info) {
	n := len(c) / 4
	if n < 1 || n > maxCMapChannels {
		return
	}
	cmap := make([]CMapEntry, 0, n)
	for i := 0; i < n; i++ {
		o := 4 * i
		cmap = append(cmap, CMapEntry{
			Component: int(binary.BigEndian.Uint16(c[o : o+2])),
			Type:      int(c[o+2]),
			Column:    int(c[o+3]),
		})
	}
	info.CMap = cmap
}
