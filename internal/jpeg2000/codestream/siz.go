// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/binutil"
)

// SIZ holds image and component basic information parsed from the SIZ marker segment.
type SIZ struct {
	Xsiz, Ysiz     int // Reference grid size (width, height)
	X0, Y0         int // Image origin (usually 0)
	XTsiz, YTsiz   int // Tile size
	XTOsiz, YTOsiz int // Tile origin
	Components     []Component
}

// Component describes one image component.
type Component struct {
	Precision int  // bits per sample (8 or 16 for MVP)
	Signed    bool // signed samples; MVP may treat as unsigned unless specified
	XRsiz     int  // horizontal subsampling (MVP assumes 1)
	YRsiz     int  // vertical subsampling (MVP assumes 1)
}

// NumTiles returns the total number of tiles (numX, numY).
func (s *SIZ) NumTiles() (int, int) {
	numX := (s.Xsiz - s.XTOsiz + s.XTsiz - 1) / s.XTsiz
	numY := (s.Ysiz - s.YTOsiz + s.YTsiz - 1) / s.YTsiz
	return numX, numY
}

// TileBounds returns the boundary of the tile (ti, tj) on the reference grid.
// ti and tj are 0-indexed tile indices.
func (s *SIZ) TileBounds(ti, tj int) (x0, y0, x1, y1 int) {
	tx0 := s.XTOsiz + ti*s.XTsiz
	ty0 := s.YTOsiz + tj*s.YTsiz
	tx1 := tx0 + s.XTsiz
	ty1 := ty0 + s.YTsiz

	// Intersection with image bounds (X0, Y0) to (Xsiz, Ysiz)
	if tx0 < s.X0 {
		tx0 = s.X0
	}
	if ty0 < s.Y0 {
		ty0 = s.Y0
	}
	if tx1 > s.Xsiz {
		tx1 = s.Xsiz
	}
	if ty1 > s.Ysiz {
		ty1 = s.Ysiz
	}
	return tx0, ty0, tx1, ty1
}

// processSIZ parses the SIZ marker segment.
func (d *Decoder) processSIZ() error {
	if d.section != sectionMainHeader {
		return fmt.Errorf("SIZ: unexpected in section %v", d.section)
	}
	if d.sizSeen {
		return fmt.Errorf("SIZ: duplicate marker")
	}

	Lsiz, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	if Lsiz < 38 { // minimal size for SIZ
		return fmt.Errorf("SIZ: invalid length %d", Lsiz)
	}
	// Rsiz (capabilities) – 2 bytes (unused here)
	if _, err := binutil.ReadU16(d.r); err != nil {
		return err
	}
	Xsiz, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	Ysiz, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	X0, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	Y0, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	// Tiling fields
	XTsiz, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	YTsiz, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	XTOsiz, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	YTOsiz, err := binutil.ReadU32(d.r)
	if err != nil {
		return err
	}
	Csiz, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	if Csiz < 1 {
		return fmt.Errorf("invalid Csiz: %d", Csiz)
	}

	d.header.siz = SIZ{
		Xsiz:       int(Xsiz),
		Ysiz:       int(Ysiz),
		X0:         int(X0),
		Y0:         int(Y0),
		XTsiz:      int(XTsiz),
		YTsiz:      int(YTsiz),
		XTOsiz:     int(XTOsiz),
		YTOsiz:     int(YTOsiz),
		Components: make([]Component, Csiz),
	}

	// Per-component: Ssizi (1 byte), XRsiz(1), YRsiz(1) for each component
	for i := 0; i < int(Csiz); i++ {
		b, err := binutil.ReadN(d.r, 3)
		if err != nil {
			return err
		}
		ssizi := b[0]
		precision := int((ssizi & 0x7F) + 1) // bits per sample = Ssiz+1
		signed := (ssizi & 0x80) != 0
		xrsiz := int(b[1])
		yrsiz := int(b[2])

		d.header.siz.Components[i] = Component{
			Precision: precision,
			Signed:    signed,
			XRsiz:     xrsiz,
			YRsiz:     yrsiz,
		}
	}

	// Discard any extra bytes (should be none with Csiz accurately read)
	consumed := 2 + 2 + 4*8 + 2 + 3*int64(Csiz)
	if int64(Lsiz) > consumed {
		_ = binutil.DiscardN(d.r, int64(Lsiz)-consumed)
	}

	if err := d.header.siz.validate(); err != nil {
		return fmt.Errorf("SIZ: %w", err)
	}
	d.sizSeen = true
	return nil
}

// Bounds for header sanity. Following the standard library's image decoders, we do
// NOT impose an arbitrary maximum image size: a large but representable image is
// accepted and Decode allocates output planes proportional to its dimensions. The
// only image-size guard is against integer overflow of the allocation length (see
// validate), so a hostile huge declared size errors cleanly instead of panicking —
// it does not bound the memory a legitimately large image may use. Callers decoding
// UNTRUSTED input should read the dimensions cheaply with DecodeConfig and decide
// whether to proceed (the std-library idiom). maxAllocSamples is the largest []int32
// plane length whose byte size still fits a platform int.
const (
	maxAllocSamples = int64(^uint(0)>>1) / 4 // overflow ceiling for an int32 plane
	maxTileCount    = 1 << 20                // max number of tiles
	maxPrecision    = 38                     // ISO 15444-1 component precision ceiling

	// maxTilePartBytes bounds a single tile-part (SOD) payload buffer declared via
	// Psot, guarding against an unbounded allocation from a hostile SOT.
	maxTilePartBytes = 1 << 28 // 256 MiB
)

// validate rejects malformed or pathologically large SIZ parameters so that
// downstream code never divides by zero or allocates an unbounded buffer.
func (s *SIZ) validate() error {
	if s.Xsiz <= s.X0 || s.Ysiz <= s.Y0 {
		return fmt.Errorf("non-positive image area: size %dx%d origin %d,%d", s.Xsiz, s.Ysiz, s.X0, s.Y0)
	}
	if s.X0 < 0 || s.Y0 < 0 {
		return fmt.Errorf("negative image origin %d,%d", s.X0, s.Y0)
	}
	// Integer-overflow guard ONLY (no arbitrary size cap — see the const block and
	// DecodeConfig): reject sizes whose int32 plane length (or its ×components total)
	// would overflow a platform int, so make() never gets a wrapped value. A large but
	// representable image passes and allocates proportionally; that is the caller's to
	// gate via DecodeConfig on untrusted input.
	w := int64(s.Xsiz) - int64(s.X0) // > 0 (checked above)
	h := int64(s.Ysiz) - int64(s.Y0) // > 0 (checked above)
	if w > maxAllocSamples/h {
		return fmt.Errorf("image dimensions too large to allocate: %dx%d", w, h)
	}
	area := w * h
	if nc := int64(len(s.Components)); nc > 0 && area > maxAllocSamples/nc {
		return fmt.Errorf("image too large to allocate: %dx%d × %d components", w, h, nc)
	}
	if s.XTsiz <= 0 || s.YTsiz <= 0 {
		return fmt.Errorf("non-positive tile size %dx%d", s.XTsiz, s.YTsiz)
	}
	if s.XTOsiz < 0 || s.YTOsiz < 0 || s.XTOsiz > s.X0 || s.YTOsiz > s.Y0 {
		return fmt.Errorf("invalid tile origin %d,%d (image origin %d,%d)", s.XTOsiz, s.YTOsiz, s.X0, s.Y0)
	}
	// The first tile must cover the image start (ISO 15444-1 B.3): XTOsiz+XTsiz > X0.
	if s.XTOsiz+s.XTsiz <= s.X0 || s.YTOsiz+s.YTsiz <= s.Y0 {
		return fmt.Errorf("tile grid does not cover image origin")
	}
	numX, numY := s.NumTiles()
	if numX <= 0 || numY <= 0 || int64(numX)*int64(numY) > maxTileCount {
		return fmt.Errorf("invalid tile count %dx%d", numX, numY)
	}
	for i, c := range s.Components {
		if c.XRsiz <= 0 || c.YRsiz <= 0 {
			return fmt.Errorf("component %d: non-positive subsampling %dx%d", i, c.XRsiz, c.YRsiz)
		}
		if c.Precision < 1 || c.Precision > maxPrecision {
			return fmt.Errorf("component %d: precision %d out of range", i, c.Precision)
		}
	}
	return nil
}
