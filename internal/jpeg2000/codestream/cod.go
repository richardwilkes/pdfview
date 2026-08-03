// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/binutil"
)

// COD holds coding style default parameters.
type COD struct {
	Layers      int    // number of quality layers
	Progression string // progression order name, e.g. "LRCP"
	ProgOrder   int    // progression order code: 0=LRCP 1=RLCP 2=RPCL 3=PCRL 4=CPRL
	Levels      int    // number of DWT decomposition levels (MVP 1)
	CodeBlockW  int    // code-block width (e.g., 64)
	CodeBlockH  int    // code-block height (e.g., 64)
	UseSPPrec   bool   // precinct use (MVP false)
	Reversible  bool   // true for lossless 5x3
	UseSOP      bool   // start of packet markers
	UseEPH      bool   // end of packet header markers
	UseMCT      bool   // multiple component transform
	// CblkStyle is the code-block style bitfield (SPcod/SPcoc): 0x01 bypass,
	// 0x02 reset-context, 0x04 termination-on-each-pass, 0x08 vertically-causal,
	// 0x10 predictable-termination, 0x20 segmentation-symbols. Only segmentation
	// symbols are currently honoured by Tier-1; the others are recorded for future use.
	CblkStyle int
	// Precincts holds the per-resolution precinct size exponents (PPx, PPy),
	// index 0 = resolution 0 (coarsest, LL), up to resolution Levels. Empty means
	// the default of a single maximal precinct (PPx=PPy=15) per resolution.
	Precincts []PrecinctSize
}

// PrecinctSize is the base-2 exponent of a precinct's width and height for one
// resolution level, as carried in the COD/COC SPcod precinct bytes.
type PrecinctSize struct {
	PPx, PPy int
}

// processCOD parses the COD marker segment.
func (d *Decoder) processCOD() error {
	Lcod, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	if Lcod < 12 {
		return fmt.Errorf("COD: invalid length %d", Lcod)
	}
	// Scod (coding style) – precinct flag is bit 0
	scod, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	usePrecincts := (scod & 0x01) != 0
	useSOP := (scod & 0x02) != 0
	useEPH := (scod & 0x04) != 0
	// Progression order
	prog, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	// Number of quality layers (uint16)
	layers, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	// Multiple component transform (MCT)
	mct, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	// Decomposition levels (Nd)
	levelsB, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	// Code-block width/height exponents (these are exponents minus 2)
	cbWexpm2, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	cbHexpm2, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	// Code-block style (SPcod): bitfield of EBCOT options (segmentation symbols, …).
	cblkSty, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	// Wavelet transform: 0=9x7 (irreversible), 1=5x3 (reversible)
	wt, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}

	// Optional precinct sizes: one byte per resolution level (low nibble = PPx,
	// high nibble = PPy). Present only when the precinct flag (Scod bit 0) is set.
	var precincts []PrecinctSize
	remaining := int64(Lcod) - 12
	if remaining > 0 {
		if usePrecincts {
			precincts = make([]PrecinctSize, 0, remaining)
			for i := int64(0); i < remaining; i++ {
				pp, err := binutil.ReadU8(d.r)
				if err != nil {
					return err
				}
				precincts = append(precincts, PrecinctSize{
					PPx: int(pp & 0x0F),
					PPy: int(pp >> 4),
				})
			}
		} else {
			_ = binutil.DiscardN(d.r, remaining)
		}
	}

	// Validate and fill fields
	progNames := []string{"LRCP", "RLCP", "RPCL", "PCRL", "CPRL"}
	if int(prog) >= len(progNames) {
		return fmt.Errorf("unsupported: progression order %d", prog)
	}
	levels := int(levelsB)
	if levels < 0 || levels > 32 {
		return fmt.Errorf("unsupported: decomposition levels=%d", levels)
	}
	// Code-block size exponents: ISO 15444-1 Table A.18 requires 2 ≤ xcb,ycb ≤ 10
	// and xcb+ycb ≤ 12 (area ≤ 4096). The stored values are the exponent minus 2.
	xcb, ycb := int(cbWexpm2)+2, int(cbHexpm2)+2
	if xcb < 2 || xcb > 10 || ycb < 2 || ycb > 10 || xcb+ycb > 12 {
		return fmt.Errorf("invalid code-block size exponents (%d,%d)", xcb, ycb)
	}
	// Precinct exponents: for resolutions above 0 the subband precinct is 2^(PP-1),
	// so PPx/PPy must be ≥ 1 there (index 0 = resolution 0, which may be 0).
	for r, p := range precincts {
		if r > 0 && (p.PPx < 1 || p.PPy < 1) {
			return fmt.Errorf("invalid precinct exponents (%d,%d) at resolution %d", p.PPx, p.PPy, r)
		}
	}

	cod := COD{
		Progression: progNames[prog],
		ProgOrder:   int(prog),
		Layers:      int(layers),
		Levels:      levels,
		CodeBlockW:  1 << (int(cbWexpm2) + 2),
		CodeBlockH:  1 << (int(cbHexpm2) + 2),
		UseSPPrec:   usePrecincts,
		UseSOP:      useSOP,
		UseEPH:      useEPH,
		Reversible:  wt == 1,
		UseMCT:      mct != 0,
		CblkStyle:   int(cblkSty),
		Precincts:   precincts,
	}

	switch d.section {
	case sectionMainHeader:
		d.header.cod = cod
	case sectionTilePartHeader:
		d.cur.cod = cod
		c := cod
		d.tiles[d.cur.tileIndex].tileCOD = &c
		// Count this marker and its marker segment toward the current tile-part size.
		d.cur.tilePartConsumed += 2 + int(Lcod)
	default:
		return fmt.Errorf("COD: unexpected in section %v", d.section)
	}

	return nil
}

// processCOC parses a COC marker segment: a per-component override of the default
// coding style (COD). Its SPcoc payload mirrors the SPcod portion of COD (no
// progression/layers/MCT — those stay from COD). The decoder threads a
// per-component override only for the code-block size (the field opj's encoder
// varies per component); a COC that changes the decomposition levels, the wavelet
// transform, or the precinct partition is rejected cleanly rather than risking a
// silent mis-decode, since those drive the (uniform) packet/DWT geometry.
func (d *Decoder) processCOC() error {
	Lcoc, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	// Ccoc is 1 byte when the image has fewer than 257 components, else 2 bytes.
	ccocBytes := 1
	if len(d.header.siz.Components) >= 257 {
		ccocBytes = 2
	}
	// Ccoc + Scoc + SPcoc(>=5): minimum length is 2(L) + ccocBytes + 1(Scoc) + 5.
	if int(Lcoc) < 2+ccocBytes+1+5 {
		return fmt.Errorf("COC: invalid length %d", Lcoc)
	}
	var comp int
	if ccocBytes == 1 {
		b, err := binutil.ReadU8(d.r)
		if err != nil {
			return err
		}
		comp = int(b)
	} else {
		v, err := binutil.ReadU16(d.r)
		if err != nil {
			return err
		}
		comp = int(v)
	}
	if comp < 0 || comp >= len(d.header.siz.Components) {
		return fmt.Errorf("COC: component index %d out of range", comp)
	}
	scoc, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	usePrecincts := (scoc & 0x01) != 0
	levelsB, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	cbWexpm2, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	cbHexpm2, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	cocCblkSty, err := binutil.ReadU8(d.r) // code-block style
	if err != nil {
		return err
	}
	wt, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}

	var precincts []PrecinctSize
	remaining := int64(Lcoc) - int64(2+ccocBytes+1+5)
	if remaining > 0 {
		if usePrecincts {
			precincts = make([]PrecinctSize, 0, remaining)
			for i := int64(0); i < remaining; i++ {
				pp, err := binutil.ReadU8(d.r)
				if err != nil {
					return err
				}
				precincts = append(precincts, PrecinctSize{PPx: int(pp & 0x0F), PPy: int(pp >> 4)})
			}
		} else {
			_ = binutil.DiscardN(d.r, remaining)
		}
	}

	xcb, ycb := int(cbWexpm2)+2, int(cbHexpm2)+2
	if xcb < 2 || xcb > 10 || ycb < 2 || ycb > 10 || xcb+ycb > 12 {
		return fmt.Errorf("COC: invalid code-block size exponents (%d,%d)", xcb, ycb)
	}

	levels := int(levelsB)
	if levels < 0 || levels > 32 {
		return fmt.Errorf("COC: unsupported decomposition levels=%d", levels)
	}
	for r, p := range precincts {
		if r > 0 && (p.PPx < 1 || p.PPy < 1) {
			return fmt.Errorf("COC: invalid precinct exponents (%d,%d) at resolution %d", p.PPx, p.PPy, r)
		}
	}

	// Base default this COC overrides (main header COD, or the tile-part COD). The
	// COC carries a full per-component coding style: code-block size, decomposition
	// levels, wavelet transform and precinct partition all override the COD default
	// for this component only. Progression/layers/MCT are not part of SPcoc and stay
	// from the COD. The packet/DWT geometry is built per-component (see tier2 and the
	// reconstruction loop), so these overrides are honoured rather than rejected.
	base := d.header.cod
	if d.section == sectionTilePartHeader {
		base = d.cur.cod
	}

	coc := base
	coc.CodeBlockW = 1 << uint(xcb)
	coc.CodeBlockH = 1 << uint(ycb)
	coc.CblkStyle = int(cocCblkSty)
	coc.Levels = levels
	coc.Reversible = wt == 1
	coc.UseSPPrec = usePrecincts
	coc.Precincts = precincts

	switch d.section {
	case sectionMainHeader:
		if d.header.coc == nil {
			d.header.coc = make(map[int]COD)
		}
		d.header.coc[comp] = coc
	case sectionTilePartHeader:
		t := &d.tiles[d.cur.tileIndex]
		if t.tileCOC == nil {
			t.tileCOC = make(map[int]COD)
		}
		t.tileCOC[comp] = coc
		d.cur.tilePartConsumed += 2 + int(Lcoc)
	default:
		return fmt.Errorf("COC: unexpected in section %v", d.section)
	}

	return nil
}
