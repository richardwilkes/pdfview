// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package engine

// Minimal skeleton for the JPEG 2000 MQ arithmetic decoder (Tier-1 entropy decoding).
// This file introduces the types and method signatures we will use in Tier-1.
// NOTE: DecodeBit currently falls back to raw bit reading as a temporary step
// until full MQ arithmetic is integrated. The public surface remains the same,
// so that Tier-1 can be wired incrementally.

// The MQ-coder uses a fixed table of states that define the estimated
// probability of the LPS (the least probable symbol) via QE values and state
// transitions for MPS/LPS events. These tables are standardized.

// qeTable holds the QE values for states 0..46 (top 16-bit range for A register).
var qeTable = [47]uint16{
	0x5601, 0x3401, 0x1801, 0x0AC1, 0x0521, 0x0221, 0x5601, 0x5401,
	0x4801, 0x3801, 0x3001, 0x2401, 0x1C01, 0x1601, 0x5601, 0x5401,
	0x5101, 0x4801, 0x3801, 0x3401, 0x3001, 0x2801, 0x2401, 0x2201,
	0x1C01, 0x1801, 0x1601, 0x1401, 0x1201, 0x1101, 0x0AC1, 0x09C1,
	0x08A1, 0x0521, 0x0441, 0x02A1, 0x0221, 0x0141, 0x0111, 0x0085,
	0x0049, 0x0025, 0x0015, 0x0009, 0x0005, 0x0001, 0x5601,
}

// nmpsTable gives the next state after an MPS event per ISO 15444-1 Table C.2.
var nmpsTable = [47]uint8{
	1, 2, 3, 4, 5, 38, 7, 8,
	9, 10, 11, 12, 13, 29, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24,
	25, 26, 27, 28, 29, 30, 31, 32,
	33, 34, 35, 36, 37, 38, 39, 40,
	41, 42, 43, 44, 45, 45, 46,
}

// nlpsTable gives the next state after an LPS event per ISO 15444-1 Table C.2.
var nlpsTable = [47]uint8{
	1, 6, 9, 12, 29, 33, 6, 14,
	14, 14, 17, 18, 20, 21, 14, 14,
	15, 16, 17, 18, 19, 19, 20, 21,
	22, 23, 24, 25, 26, 27, 28, 29,
	30, 31, 32, 33, 34, 35, 36, 37,
	38, 39, 40, 41, 42, 43, 46,
}

// switchTable indicates whether to toggle MPS on LPS event at state s.
// Per ISO/IEC 15444-1 Table D.2: states 0-37 switch, states 38-46 do not.
var switchTable = [47]bool{
	true, false, false, false, false, false, true, false,
	false, false, false, false, false, false, true, false,
	false, false, false, false, false, false, false, false,
	false, false, false, false, false, false, false, false,
	false, false, false, false, false, false, false, false,
	false, false, false, false, false, false, false,
}

// MQCtx represents a single context state index and MPS value.
type MQCtx struct {
	idx uint8 // state index (0..46 in standard tables)
	mps uint8 // most probable symbol (0 or 1)
}

// MQDecoder holds the internal registers for MQ arithmetic decoding.
type MQDecoder struct {
	a        uint16     // interval register (A)
	c        uint32     // code register (C)
	ct       int8       // a bit counter
	data     []byte     // raw bytestream (MQ reads byte-by-byte, not bit-by-bit)
	pos      int        // current byte position in data
	lastByte int        // last byte read (-1 initially, used for 0xFF stuffing detection)
	br       *BitReader // kept for the raw-bit fallback path only

	// Contexts used by Tier‑1; number depends on coding passes. For MVP,
	// we will allocate a modest fixed number and index by small ints.
	ctxs []MQCtx

	// internal mode flags
	useArithmetic bool // when true, DecodeBit uses MQ arithmetic instead of raw bits
	arithInit     bool // whether arithmetic registers have been initialized
	arithReady    bool // whether arithmetic init succeeded (bytes were available)
	eosSeen       bool // true once the real bytestream is exhausted; supply 0xFF per §C.3.3

	// Raw (bypass) decoding state. Under the selective arithmetic-coding bypass
	// style, the significance-propagation and magnitude-refinement passes past the
	// fourth bit-plane are coded as raw bits (no MQ); rawMode routes DecodeBit to
	// decodeRawBit, which reads MSB-first with the 0xFF bit-stuffing of ISO D.6.
	rawMode bool
	rawC    uint32
	rawCT   int

	// trace support: when non-nil, each DecodeBit call appends an entry
	TraceLog []MQTraceEntry
	traceOn  bool

	// step counter for trace
	stepCount int
}

// MQTraceEntry records one DecodeBit call for trace comparison.
type MQTraceEntry struct {
	Step     int
	Ctx      int
	ABefore  uint16
	CBefore  uint32
	CTBefore int8
	Bit      uint8
	AAfter   uint16
	CAfter   uint32
	CTAfter  int8
	Ok       bool
}

// NewMQDecoder initializes an MQ decoder over a bytestream (after Tier‑2 assembly).
func NewMQDecoder(bytestream []byte, numCtx int) *MQDecoder {
	if numCtx <= 0 {
		numCtx = 32
	}
	d := &MQDecoder{
		a:    0x8000,
		c:    0,
		ct:   0,
		data: bytestream,
		pos:  0,
		br:   NewBitReader(bytestream), // fallback only
		ctxs: make([]MQCtx, numCtx),
		// default: enable arithmetic mode (remaining task: MQ arithmetic decoding)
		useArithmetic: true,
		arithInit:     false,
		arithReady:    false,
	}
	// Contexts start at standard initial states; for lossless most are state 0 with MPS=0.
	for i := range d.ctxs {
		d.ctxs[i] = MQCtx{idx: 0, mps: 0}
	}
	// Note: For the current fallback implementation of DecodeBit (which reads
	// raw bits via BitReader), we must not pre-consume bytes here; otherwise
	// the first bits would be skipped. We therefore delay true MQ initialization
	// until DecodeBit is switched to arithmetic mode. The following initializer
	// is provided for future use but not invoked yet:
	//   d.initArithmetic()
	return d
}

// initArithmetic performs the proper JPEG 2000 MQ INITDEC procedure.
// Per ISO/IEC 15444-1 §C.3.1:
//
//	C = (B0 << 16); ct = 8
//	BYTEIN → C |= (B1 << 8)
//	C = C << 8; ct -= 8 (ct = 0)
//	BYTEIN → C |= B2; ct = 8
//
// initArithmetic implements INITDEC per ISO 15444-1 §C.3.5.
func (d *MQDecoder) initArithmetic() {
	d.a = 0x8000
	d.c = 0
	d.ct = 0
	d.lastByte = -1
	d.arithReady = false

	// Read first byte directly into upper bits of C. When the segment is already
	// exhausted (e.g. a zero-length terminated pass under "termination on each pass"),
	// supply the 0xFF marker byte — OpenJPEG appends a 0xFF 0xFF marker after every
	// segment so its INITDEC always reads a byte here; not doing so left C missing its
	// top byte and corrupted the MQ state for empty/short segments (spurious decodes).
	if d.pos < len(d.data) {
		d.lastByte = int(d.data[d.pos])
		d.pos++
		d.c = uint32(d.lastByte) << 16
	} else {
		d.lastByte = 0xFF
		d.c = 0xFF << 16
	}

	// BYTEIN + C <<= 7; CT -= 7
	d.mqByteIn()
	d.c <<= 7
	d.ct -= 7

	d.arithReady = true
}

// RestartSegment re-points the decoder at a new, independently-terminated codeword
// segment and re-runs INITDEC over it, WITHOUT touching the context-state table.
// Used for code-block styles that terminate the MQ coder mid-block (e.g. termination
// on each coding pass): each segment is a fresh arithmetic stream, but the EBCOT
// context probabilities carry over (they are reset only under the RESET style).
func (d *MQDecoder) RestartSegment(data []byte, raw bool) bool {
	d.data = data
	d.pos = 0
	d.br = NewBitReader(data)
	d.eosSeen = false
	if raw {
		// Raw (bypass) segment: no MQ INITDEC; read bits directly.
		d.rawMode = true
		d.rawC = 0
		d.rawCT = 0
		return true
	}
	d.rawMode = false
	d.initArithmetic()
	d.arithInit = true
	return d.arithReady
}

// SetRawMode toggles raw-bit decoding for subsequent DecodeBit calls (bypass).
func (d *MQDecoder) SetRawMode(on bool) { d.rawMode = on }

// decodeRawBit reads one bypass-coded raw bit, MSB-first, applying the 0xFF
// bit-stuffing of ISO 15444-1 D.6 (after a 0xFF byte the next byte carries only 7
// data bits). Mirrors OpenJPEG opj_mqc_raw_decode. Past the segment end it supplies
// 0xFF padding, so it never fails.
func (d *MQDecoder) decodeRawBit() uint8 {
	if d.rawCT == 0 {
		if d.rawC == 0xFF {
			next := uint32(0xFF)
			if d.pos < len(d.data) {
				next = uint32(d.data[d.pos])
			}
			if next > 0x8F {
				d.rawC = 0xFF
				d.rawCT = 8
			} else {
				d.rawC = next
				d.pos++
				d.rawCT = 7
			}
		} else {
			if d.pos < len(d.data) {
				d.rawC = uint32(d.data[d.pos])
				d.pos++
			} else {
				d.rawC = 0xFF
			}
			d.rawCT = 8
		}
	}
	d.rawCT--
	return uint8((d.rawC >> uint(d.rawCT)) & 1)
}

// DecodeBit decodes a single bit using context k. The common case (arithmetic
// mode, already initialized, tracing off) is a straight call into stepArithmetic;
// the raw-bypass, lazy-init, tracing and raw-bit-fallback paths are split out so
// they impose no per-bit cost on the hot path.
func (d *MQDecoder) DecodeBit(ctx int) (bit uint8, ok bool) {
	if d.rawMode {
		return d.decodeRawBit(), true
	}
	if !d.useArithmetic {
		// Fallback: direct bit from stream
		return d.br.ReadBit()
	}
	if !d.arithInit {
		d.initArithmetic()
		d.arithInit = true
		if !d.arithReady {
			return 0, false
		}
	}
	if d.traceOn {
		return d.decodeBitTraced(ctx)
	}
	return d.stepArithmetic(ctx)
}

// decodeBitTraced is the trace-logging variant of the arithmetic step, kept off
// the hot path so production decoding pays nothing for the snapshot bookkeeping.
func (d *MQDecoder) decodeBitTraced(ctx int) (uint8, bool) {
	aBefore := d.a
	cBefore := d.c
	ctBefore := d.ct
	b, okStep := d.stepArithmetic(ctx)
	d.TraceLog = append(d.TraceLog, MQTraceEntry{
		Step:     d.stepCount,
		Ctx:      ctx,
		ABefore:  aBefore,
		CBefore:  cBefore,
		CTBefore: ctBefore,
		Bit:      b,
		AAfter:   d.a,
		CAfter:   d.c,
		CTAfter:  d.ct,
		Ok:       okStep,
	})
	d.stepCount++
	return b, okStep
}

// EnableTrace enables per-DecodeBit trace logging.
func (d *MQDecoder) EnableTrace(on bool) {
	d.traceOn = on
	if on && d.TraceLog == nil {
		d.TraceLog = make([]MQTraceEntry, 0, 256)
	}
}

// The following methods are placeholders for the full MQ implementation.
// They will be filled in later steps.

// mqByteIn implements BYTEIN per ISO 15444-1 §C.3.3.
// After a 0xFF byte, the next byte determines whether it's byte stuffing
// (next < 0x90: 7 data bits) or a marker (next >= 0x90: pad with 0xFF).
func (d *MQDecoder) mqByteIn() {
	if d.pos >= len(d.data) {
		// Past end: supply 0xFF padding
		d.c += 0xFF00
		d.ct = 8
		return
	}
	if d.lastByte == 0xFF {
		nextByte := d.data[d.pos]
		if nextByte > 0x8F {
			// Marker detected: don't consume it, pad with 0xFF
			d.c += 0xFF00
			d.ct = 8
		} else {
			// Byte stuffing: only 7 data bits
			d.pos++
			d.lastByte = int(nextByte)
			d.c += uint32(nextByte) << 9
			d.ct = 7
		}
	} else {
		b := d.data[d.pos]
		d.pos++
		d.lastByte = int(b)
		d.c += uint32(b) << 8
		d.ct = 8
	}
}

func (d *MQDecoder) renorm() bool {
	// RENORMD per ISO 15444-1 §C.3.3
	for {
		if d.ct == 0 {
			d.mqByteIn()
		}
		d.a <<= 1
		d.c <<= 1
		d.ct--
		if d.a >= 0x8000 {
			break
		}
	}
	return true
}

// stepArithmetic performs one MQ decoding step per ISO 15444-1 Table C.7,
// matching the OpenJPEG reference implementation.
func (d *MQDecoder) stepArithmetic(ctx int) (uint8, bool) {
	if ctx < 0 || ctx >= len(d.ctxs) {
		return 0, false
	}
	cx := &d.ctxs[ctx]
	qe := uint32(qeTable[cx.idx])

	// Step 1: A -= Qe
	d.a -= uint16(qe)

	if (d.c >> 16) < qe {
		// LPS sub-interval: conditional exchange per ISO 15444-1 Table C.7
		var out uint8
		if uint32(d.a) < qe {
			// Conditional exchange: output MPS
			out = cx.mps
			cx.idx = nmpsTable[cx.idx]
		} else {
			// Normal LPS
			out = cx.mps ^ 1
			if switchTable[cx.idx] {
				cx.mps ^= 1
			}
			cx.idx = nlpsTable[cx.idx]
		}
		// A = Qe of the CURRENT state (before transition), matching OpenJPEG
		d.a = uint16(qe)
		d.renorm()
		return out, true
	}

	// MPS sub-interval
	d.c -= qe << 16
	if d.a&0x8000 == 0 {
		// Renormalization needed: conditional exchange
		var out uint8
		if uint32(d.a) < qe {
			// Conditional exchange: output LPS
			out = cx.mps ^ 1
			if switchTable[cx.idx] {
				cx.mps ^= 1
			}
			cx.idx = nlpsTable[cx.idx]
		} else {
			out = cx.mps
			cx.idx = nmpsTable[cx.idx]
		}
		d.renorm()
		return out, true
	}

	// MPS, no renormalization: no state transition
	return cx.mps, true
}

// EnableArithmetic enables or disables arithmetic mode for DecodeBit.
func (d *MQDecoder) EnableArithmetic(on bool) {
	d.useArithmetic = on
}
