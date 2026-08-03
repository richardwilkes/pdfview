// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package engine

// BlockStream represents a single code-block bytestream collected by Tier-2.
type BlockStream struct {
	Comp  int    // Component index
	Level int    // Decomposition level
	Band  string // "LL", "LH", "HL", "HH"
	W, H  int    // code-block dimensions (clipped at subband edges)
	// X0, Y0 are the code-block's top-left position within its subband, in subband
	// coordinates. A single-code-block subband (or raw raster) uses 0,0.
	X0, Y0 int
	// Zero marks an all-zero-coefficient placeholder code-block (e.g. a code-block
	// not included in any packet). Tier-1 emits zero coefficients directly without
	// any raw-pixel decode or DC level shift.
	Zero  bool
	Bytes []byte
	// Minimal packet header interpretation (MVP/degenerate LRCP case)
	// Included indicates the code-block is present in this packet.
	Included bool
	// ZBP (zero bit-planes) indicates the number of most-significant zero bit-planes skipped.
	ZBP int
	// Layers is the number of quality layers that contributed (MVP: 1)
	Layers int
	// Entropy indicates the payload appears to be entropy-coded (i.e., not a raw raster shortcut).
	Entropy bool
	// Segments carries the byte length of each independently-terminated codeword
	// segment (in stream order; Bytes is their concatenation). A block with the
	// default code-block style has a single segment covering all passes; styles that
	// terminate the MQ coder mid-block (e.g. termination on each pass) split into
	// several. SegPasses[i] is the number of coding passes carried by segment i.
	// Empty Segments means "one segment = all of Bytes" (legacy / raw-raster).
	Segments  []int
	SegPasses []int
	// Passes is the total number of coding passes represented by the block.
	Passes int
	// PacketCount is the number of packets contributing to this block in this tile-part (degenerate MVP: 1).
	PacketCount int
	// HeaderBytes is the number of bytes consumed by packet headers for this block within this tile-part (degenerate MVP: 0).
	HeaderBytes int
}

// CodeBlock holds decoded coefficients for a single code-block after Tier-1.
type CodeBlock struct {
	Comp   int // Component index
	W, H   int
	X0, Y0 int     // top-left position within the subband (subband coordinates)
	Band   string  // e.g., "LL", "LH", "HL", "HH"
	Level  int     // decomposition level (0 = original resolution, 1 = first decomposition, etc.)
	Data   []int32 // coefficients (full-scale magnitudes with sign; low bits below LowBit are zero)

	// LowPlanes is the per-coefficient lowest decoded bit-plane (same dense layout
	// as Data). For lossy (9/7), dequantization adds a per-coefficient mid-point
	// bias of 0.5·2^LowPlanes[i] (0.5 when fully decoded to plane 0). nil for raw /
	// zero blocks (no significant coefficients to bias).
	LowPlanes []int32
}

// ComponentPlane represents a decoded component plane after inverse DWT.
type ComponentPlane struct {
	Comp int // Component index
	W, H int
	Pix  []int32
}
