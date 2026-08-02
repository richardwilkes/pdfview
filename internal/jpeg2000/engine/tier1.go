// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package engine

import (
	"encoding/binary"
	"fmt"
)

// lowPlaneNoBias is a sentinel LowPlane value meaning "reconstruct this
// coefficient with no mid-point bias" (an exact integer): 0.5·2^lowPlaneNoBias
// underflows to 0 in the 9/7 dequant, and (1<<uint(lowPlaneNoBias))>>1 is 0 in
// the 5/3 path. Used for ROI-descaled coefficients whose ×2-domain magnitude is
// even (see the ROI max-shift handling in DecodeTier1).
const lowPlaneNoBias = int32(-1 << 20)

// decodeRawSamples converts raw bytes to int32 coefficients with DC level shift.
func decodeRawSamples(raw []byte, count int, bytesPer int, dcShift int32) []int32 {
	data := make([]int32, count)
	if bytesPer == 1 {
		for i := 0; i < count; i++ {
			data[i] = int32(raw[i]) - dcShift
		}
	} else {
		for i := 0; i < count; i++ {
			data[i] = int32(binary.BigEndian.Uint16(raw[2*i:2*i+2])) - dcShift
		}
	}
	return data
}

// DecodeTier1 performs MQ arithmetic decoding of code-block bytestreams
// into coefficient planes.
func DecodeTier1(
	compLevels []int, // per-component DWT decomposition levels (COD, or COC override)
	xsiz int, // cs.SIZ.Xsiz
	ysiz int, // cs.SIZ.Ysiz
	compPrecisions []int,
	compSigned []bool,
	compXRsiz []int,
	compYRsiz []int,
	blocks []BlockStream,
	guardBits []int, // per-component guard bits (QCD, or QCC override)
	stepExpns [][]int, // per-component epsilon_b per subband (nil = use precision)
	cblkStyles []int, // per-component code-block style bitfield (COD/COC SPcod)
	roiShifts []int, // per-component ROI max-shift (RGN SPrgn; 0 = no ROI)
) ([]CodeBlock, error) {
	if xsiz <= 0 || ysiz <= 0 {
		return nil, fmt.Errorf("invalid image size %dx%d", xsiz, ysiz)
	}

	if len(blocks) == 0 {
		return nil, fmt.Errorf("tier-2 provided no code-blocks")
	}

	out := make([]CodeBlock, 0, len(blocks))
	scr := &t1Scratch{} // reused across all code-blocks to avoid per-block allocation
	for _, b := range blocks {
		if b.Comp < 0 || b.Comp >= len(compPrecisions) {
			return nil, fmt.Errorf("tier-1: invalid component index %d", b.Comp)
		}

		// Per-component decomposition levels: a COC marker can give one component a
		// different DWT depth than the COD default, which changes both the subband
		// geometry and the QCD step-size index below.
		levels := 0
		if b.Comp < len(compLevels) {
			levels = compLevels[b.Comp]
		}

		cw := (xsiz + compXRsiz[b.Comp] - 1) / compXRsiz[b.Comp]
		ch := (ysiz + compYRsiz[b.Comp] - 1) / compYRsiz[b.Comp]
		subW := GetSubbandW(cw, levels, b.Level, b.Band)
		subH := GetSubbandH(ch, levels, b.Level, b.Band)

		// Block dimensions. Tier-2 sets W/H to the (clipped) code-block size and
		// X0/Y0 to the block's position within the subband. Legacy raw-raster
		// payloads leave W/H unset, in which case the block is the whole subband.
		bw, bh := b.W, b.H
		if bw <= 0 || bh <= 0 {
			bw, bh = subW, subH
		}

		// All-zero placeholder: emit zero coefficients directly (no DC shift).
		if b.Zero {
			out = append(out, CodeBlock{Comp: b.Comp, W: bw, H: bh, X0: b.X0, Y0: b.Y0,
				Band: b.Band, Level: b.Level, Data: make([]int32, bw*bh)})
			continue
		}

		// DC level shift for unsigned components: reconstruction adds 2^(P-1),
		// so raw pixel data must be pre-shifted to the signed domain.
		// Only applies to the LL subband at the coarsest level; detail subbands
		// (HL, LH, HH) are inherently signed with zero mean.
		dcShift := int32(0)
		if b.Band == "LL" && b.Comp < len(compSigned) && !compSigned[b.Comp] {
			dcShift = int32(1) << uint(compPrecisions[b.Comp]-1)
		}

		bytesPer := 1
		if compPrecisions[b.Comp] > 8 {
			bytesPer = 2
		}

		// Shortcut for raw coefficients (if Entropy is false)
		if !b.Entropy {
			neededBlk := bw * bh * bytesPer
			neededFull := cw * ch * bytesPer
			if len(b.Bytes) == neededBlk && neededBlk > 0 {
				out = append(out, CodeBlock{Comp: b.Comp, W: bw, H: bh, X0: b.X0, Y0: b.Y0, Band: b.Band, Level: b.Level,
					Data: decodeRawSamples(b.Bytes, bw*bh, bytesPer, dcShift)})
				continue
			}
			if len(b.Bytes) == neededFull {
				out = append(out, CodeBlock{Comp: b.Comp, W: cw, H: ch, Band: "LL", Level: 0,
					Data: decodeRawSamples(b.Bytes, cw*ch, bytesPer, dcShift)})
				continue
			}
			return nil, fmt.Errorf("invalid raw payload length for component %d: got %d bytes, need %d (bw=%d, bh=%d)", b.Comp, len(b.Bytes), neededBlk/bytesPer, bw, bh)
		}

		// Entropy-coded path
		effBD := compPrecisions[b.Comp]
		if z := b.ZBP; z > 0 && z < effBD {
			effBD = effBD - z
		}

		passes := b.Passes
		if passes <= 0 {
			passes = effBD * 3
		}
		// Compute numbps from QCD step sizes + guard bits (ISO 15444-1 §E.1).
		// Both are indexed per component so a QCC marker (per-component quantization
		// override) takes effect for that component only.
		var cStepExpns []int
		cGuard := -1
		if b.Comp < len(stepExpns) {
			cStepExpns = stepExpns[b.Comp]
		}
		if b.Comp < len(guardBits) {
			cGuard = guardBits[b.Comp]
		}
		if len(cStepExpns) > 0 && cGuard >= 0 {
			// Determine subband index for step size lookup
			// Band ordering: LL(0), then for each level: HL, LH, HH
			sbIdx := 0
			if b.Band != "LL" || b.Level != levels {
				sbIdx = 1 + 3*(levels-b.Level)
				switch b.Band {
				case "LH":
					sbIdx += 1
				case "HH":
					sbIdx += 2
				}
			}
			if sbIdx < len(cStepExpns) {
				numbps := cStepExpns[sbIdx] + cGuard - 1
				// May go negative when the zero-bit-plane count exceeds the background
				// magnitude depth — which is exactly what happens for an ROI block whose
				// significant bits live entirely in the shifted (high) planes. Do NOT
				// clamp here: OpenJPEG forms bpno+1 = roishift + (numbps - ZBP) without an
				// intermediate clamp, so a negative (numbps - ZBP) must survive to offset
				// the roiShift below. Clamping early would start decoding several planes
				// too high and desynchronise the MQ decoder for that block.
				effBD = numbps - b.ZBP
			}
		}

		// Region of interest (RGN max-shift): the encoder coded ROI coefficients
		// roiShift bit-planes higher than the background, so decode that many extra
		// planes; the coefficients are shifted back down after Tier-1.
		roiShift := 0
		if b.Comp < len(roiShifts) {
			roiShift = roiShifts[b.Comp]
		}
		if roiShift > 0 {
			effBD += roiShift
		}
		// Final clamp: bpno+1 = max(0, roishift + numbps - ZBP). A non-positive value
		// means the block has no decodable magnitude planes (all coefficients zero).
		if effBD < 0 {
			effBD = 0
		}

		cblkSty := 0
		if b.Comp < len(cblkStyles) {
			cblkSty = cblkStyles[b.Comp]
		}
		data, lowPlanes, ok := decodeEBCOTPasses(bw, bh, effBD, b.Bytes, b.Band, passes, cblkSty, b.Segments, b.SegPasses, nil, scr)
		if ok {
			// ROI max-shift (ISO 15444-1 H.2): the encoder scaled region-of-interest
			// coefficients up by roiShift bit-planes, so any decoded coefficient whose
			// magnitude reaches the shift threshold belongs to the ROI and is scaled
			// back down. OpenJPEG performs this on its internal ×2 ("oneplushalf")
			// magnitude domain — its datap holds twice our true magnitude — and only
			// later folds in the factor of 0.5 (lossy: ×0.5·stepsize; lossless: /2).
			// We replicate that exactly by descaling in the ×2 domain (d2) so the half
			// bit opj keeps is preserved: the integer part becomes d2>>1 and an odd d2
			// contributes a +0.5 mid-point bias via LowPlane 0 (which also yields a
			// zero 5/3 integer half, keeping the reversible path correct). The ×2
			// threshold 2^roiShift on |2·mag| is just 2^(roiShift-1) on |mag|.
			if roiShift > 0 {
				thresh := int32(1) << uint(roiShift-1)
				if lowPlanes == nil {
					lowPlanes = make([]int32, len(data))
				}
				for i, v := range data {
					mag := v
					if mag < 0 {
						mag = -mag
					}
					if mag >= thresh {
						d2 := (mag << 1) >> uint(roiShift)
						nm := d2 >> 1
						if v < 0 {
							data[i] = -nm
						} else {
							data[i] = nm
						}
						if d2&1 == 1 {
							lowPlanes[i] = 0 // +0.5 mid-point bias
						} else {
							lowPlanes[i] = lowPlaneNoBias // exact integer, no bias
						}
					}
				}
			}
			out = append(out, CodeBlock{
				Comp:      b.Comp,
				W:         bw,
				H:         bh,
				X0:        b.X0,
				Y0:        b.Y0,
				Band:      b.Band,
				Level:     b.Level,
				Data:      data,
				LowPlanes: lowPlanes,
			})
			continue
		}

		if effBD <= 0 {
			data := make([]int32, bw*bh)
			out = append(out, CodeBlock{Comp: b.Comp, W: bw, H: bh, X0: b.X0, Y0: b.Y0, Band: b.Band, Level: b.Level, Data: data})
			continue
		}

		return nil, fmt.Errorf("tier-1: failed to decode entropy-coded block for component %d", b.Comp)
	}

	return out, nil
}

func GetSubbandW(fullW int, Nd int, L int, band string) int {
	// ISO 15444-1: Resolution level r=0 is LL_Nd. Resolution level r=Nd is the full image.
	// Subbands at level L are used to synthesize resolution level r = Nd-L+1.
	r := Nd - L + 1

	wr, _ := GetResolutionSize(fullW, 1, Nd, r)

	if band == "LL" {
		// LL_L is equivalent to resolution level r-1 = Nd-L
		resW, _ := GetResolutionSize(fullW, 1, Nd, r-1)
		return resW
	}

	// Subbands HL_L, LH_L, HH_L form resolution level r from resolution level r-1.
	// wr is width at resolution r.
	// Horizontal low-pass (LL, LH) width: ceil(wr / 2)
	// Horizontal high-pass (HL, HH) width: floor(wr / 2)
	if band == "LH" {
		return (wr + 1) / 2
	}
	return wr / 2
}

func GetSubbandH(fullH int, Nd int, L int, band string) int {
	r := Nd - L + 1
	_, hr := GetResolutionSize(1, fullH, Nd, r)

	if band == "LL" {
		_, resH := GetResolutionSize(1, fullH, Nd, r-1)
		return resH
	}

	// Vertical low-pass (LL, HL) height: ceil(hr / 2)
	// Vertical high-pass (LH, HH) height: floor(hr / 2)
	if band == "HL" {
		return (hr + 1) / 2
	}
	return hr / 2
}

// --- Tier-1 (EBCOT) types ---

// tier1Ctx models a tiny subset of Tier-1 state needed for the MVP skeleton.
// In a full implementation we manage significance/neighbor flags and
// context selection; for now we keep and update the minimal state only.
type tier1Ctx struct {
	w, h int
	sw   int // row stride of the bordered grids (= w + 2)
	// Significance / sign flags on a grid with a 1-sample border on every side.
	// The border lets neighbour reads (x±1, y±1) for in-range samples stay inside
	// the slice without a bounds check; border cells are never written and read as
	// 0 (insignificant), which is exactly the out-of-block convention.
	sig []uint8
	sgn []uint8
	// vcausal enables vertically-causal context formation (code-block style 0x08):
	// when set, the three samples in the row just below a 4-row stripe boundary are
	// treated as insignificant during context formation (they belong to the next
	// stripe, not yet decoded in causal order).
	vcausal bool
}

func newTier1Ctx(w, h int) *tier1Ctx {
	sw := w + 2
	n := sw * (h + 2)
	return &tier1Ctx{
		w:   w,
		h:   h,
		sw:  sw,
		sig: make([]uint8, n),
		sgn: make([]uint8, n),
	}
}

// idx converts (x,y) to a flat index into the bordered grids. Valid for x in
// [-1, w] and y in [-1, h] (i.e. any in-range sample and its 8 neighbours), so
// callers need no bounds check.
func (t *tier1Ctx) idx(x, y int) int {
	return (y+1)*t.sw + (x + 1)
}

// setSig sets the significance flag at (x,y) to 0/1.
func (t *tier1Ctx) setSig(x, y int, v uint8) {
	t.sig[t.idx(x, y)] = v & 1
}

// getSig gets the significance flag at (x,y); the border returns 0.
func (t *tier1Ctx) getSig(x, y int) uint8 {
	return t.sig[t.idx(x, y)]
}

// setSign sets the sign bit at (x,y) to 0/1.
func (t *tier1Ctx) setSign(x, y int, v uint8) {
	t.sgn[t.idx(x, y)] = v & 1
}

// getSign gets the sign bit at (x,y); the border returns 0.
func (t *tier1Ctx) getSign(x, y int) uint8 {
	return t.sgn[t.idx(x, y)]
}

// neighborSigSum returns the count of significant neighbors (8-connectivity).
// The bordered grid lets it read the eight neighbours by fixed offset from the
// centre index, with no per-neighbour bounds check or center-skip branch.
func (t *tier1Ctx) neighborSigSum(x, y int) int {
	return t.neighborSigSumI(t.idx(x, y), y)
}

// neighborSigSumI is neighborSigSum with the bordered index precomputed by the caller.
func (t *tier1Ctx) neighborSigSumI(i, y int) int {
	s := t.sig
	sw := t.sw
	sum := s[i-sw-1] + s[i-sw] + s[i-sw+1] +
		s[i-1] + s[i+1]
	if !(t.vcausal && y&3 == 3) {
		sum += s[i+sw-1] + s[i+sw] + s[i+sw+1]
	}
	return int(sum)
}

// sigCtx returns the significance context label (0..8) for the given subband
// per ISO/IEC 15444-1 Table D.1.
//
// LL/LH: horizontal and vertical neighbors weighted equally.
// HL: horizontal neighbors weighted more heavily than vertical.
// HH: diagonal neighbors weighted more heavily.
func sigCtx(t *tier1Ctx, x, y int, band string) int {
	return sigCtxAt(t, t.idx(x, y), y, bandOrient(band))
}

// Subband orientation codes for the significance-context table (ISO/IEC 15444-1
// Table D.1). The band name is constant per code-block, so callers resolve it to
// one of these once via bandOrient and pass the int into the hot per-sample loops,
// avoiding a string comparison on every sigCtxAt call.
const (
	orientLH = 0 // LL and LH: horizontal-primary table, no swap
	orientHL = 1 // HL: horizontal/vertical contributions interchanged
	orientHH = 2 // HH: diagonal-primary table
)

// bandOrient maps a subband name to its significance-context orientation code.
func bandOrient(band string) int {
	switch band {
	case "HH":
		return orientHH
	case "HL":
		return orientHL
	default:
		return orientLH
	}
}

// sigCtxAt is sigCtx with the bordered index i = idx(x,y) and the orientation code
// orient = bandOrient(band) precomputed by the caller, avoiding a redundant idx
// computation and a per-call string compare in the hot significance/cleanup passes.
// The returned context is 0 exactly when no neighbour is significant, so callers that
// only need "any significant neighbour?" can test ctx == 0 instead of a separate scan.
func sigCtxAt(t *tier1Ctx, i, y, orient int) int {
	s := t.sig
	sw := t.sw
	h := int(s[i-1]) + int(s[i+1])
	v := int(s[i-sw])
	d := int(s[i-sw-1]) + int(s[i-sw+1])
	// Vertically-causal: ignore the row below a stripe boundary (next stripe).
	if !(t.vcausal && y&3 == 3) {
		v += int(s[i+sw])
		d += int(s[i+sw-1]) + int(s[i+sw+1])
	}
	// h,v in 0..2, d in 0..4: index the precomputed table instead of re-running the
	// branch cascade per sample.
	return int(sigCtxLUT[((orient*3+h)*3+v)*5+d])
}

// sigCtxFromHVD is the ISO/IEC 15444-1 Table D.1 significance-context cascade as a
// pure function of the orientation and the horizontal/vertical/diagonal significant-
// neighbour counts (h,v in 0..2, d in 0..4). It is evaluated once per (orient,h,v,d)
// at startup to fill sigCtxLUT; the hot path uses the table.
func sigCtxFromHVD(orient, h, v, d int) int {
	// HH has its own table.
	if orient == orientHH {
		hv := h + v
		if d == 0 {
			if hv == 0 {
				return 0
			} else if hv == 1 {
				return 1
			}
			return 2
		} else if d == 1 {
			if hv == 0 {
				return 3
			} else if hv == 1 {
				return 4
			}
			return 5
		} else if d == 2 {
			if hv == 0 {
				return 6
			}
			return 7
		}
		return 8
	}

	// LL/LH use the horizontal-primary table; HL (horizontally high-pass)
	// interchanges the horizontal and vertical contributions.
	if orient == orientHL {
		h, v = v, h
	}
	if h == 0 {
		if v == 0 {
			if d == 0 {
				return 0
			} else if d == 1 {
				return 1
			}
			return 2
		} else if v == 1 {
			return 3
		}
		return 4
	} else if h == 1 {
		if v == 0 {
			if d == 0 {
				return 5
			}
			return 6
		}
		return 7
	}
	return 8
}

// sigCtxLUT[((orient*3+h)*3+v)*5+d] is the significance context for the given
// orientation and neighbour counts, precomputed from sigCtxFromHVD.
var sigCtxLUT = func() [3 * 3 * 3 * 5]uint8 {
	var lut [3 * 3 * 3 * 5]uint8
	for orient := 0; orient < 3; orient++ {
		for h := 0; h <= 2; h++ {
			for v := 0; v <= 2; v++ {
				for d := 0; d <= 4; d++ {
					lut[((orient*3+h)*3+v)*5+d] = uint8(sigCtxFromHVD(orient, h, v, d))
				}
			}
		}
	}
	return lut
}()

// signCtxEntry is one row of the sign-context lookup table (ISO/IEC 15444-1
// Table D.3): the context label (9..13) and the XOR prediction bit.
type signCtxEntry struct {
	ctx  int
	flip uint8
}

// signCtxTable is indexed by (hc+1)*3 + (vc+1) with hc,vc in {-1,0,1}. Hoisted to
// package scope so signCtxISO does not rebuild the nine-entry literal on every call
// (it runs once per coefficient that becomes significant). Per OpenJPEG lut_spb.
var signCtxTable = [9]signCtxEntry{
	{13, 1}, // hc=-1, vc=-1
	{12, 1}, // hc=-1, vc=0
	{11, 1}, // hc=-1, vc=1
	{10, 1}, // hc=0,  vc=-1
	{9, 0},  // hc=0,  vc=0
	{10, 0}, // hc=0,  vc=1
	{11, 0}, // hc=1,  vc=-1
	{12, 0}, // hc=1,  vc=0
	{13, 0}, // hc=1,  vc=1
}

// signCtxISO returns the sign context label (9..13) and the XOR prediction bit
// per ISO/IEC 15444-1 Table D.3. flipBit=1 means the decoded bit is XOR'd with 1.
// i is the bordered-grid index of (x,y) (idx(x,y)); the four orthogonal neighbours
// are then fixed offsets ±1 / ±sw from it, so no per-neighbour index is recomputed.
// y is still needed for the vertically-causal stripe-boundary test.
func signCtxISO(t *tier1Ctx, i, y int) (ctx int, flipBit uint8) {
	sig := t.sig
	sgn := t.sgn
	sw := t.sw

	leftSig := sig[i-1]
	rightSig := sig[i+1]
	upSig := sig[i-sw]
	downSig := sig[i+sw]
	// Vertically-causal: the row below a stripe boundary belongs to the next stripe
	// and is treated as insignificant during context formation.
	if t.vcausal && y&3 == 3 {
		downSig = 0
	}

	var hc, vc int
	if leftSig != 0 && rightSig != 0 {
		ls := sgn[i-1]
		rs := sgn[i+1]
		if ls == rs {
			// Both neighbours significant with the same sign reinforce.
			if ls == 1 {
				hc = -1
			} else {
				hc = 1
			}
		}
		// Opposite signs cancel: hc stays 0.
	} else if leftSig != 0 {
		if sgn[i-1] == 0 {
			hc = 1
		} else {
			hc = -1
		}
	} else if rightSig != 0 {
		if sgn[i+1] == 0 {
			hc = 1
		} else {
			hc = -1
		}
	}

	if upSig != 0 && downSig != 0 {
		us := sgn[i-sw]
		ds := sgn[i+sw]
		if us == ds {
			if us == 1 {
				vc = -1
			} else {
				vc = 1
			}
		}
	} else if upSig != 0 {
		if sgn[i-sw] == 0 {
			vc = 1
		} else {
			vc = -1
		}
	} else if downSig != 0 {
		if sgn[i+sw] == 0 {
			vc = 1
		} else {
			vc = -1
		}
	}

	e := signCtxTable[(hc+1)*3+(vc+1)]
	return e.ctx, e.flip
}

// magRefCtx returns the magnitude refinement context (14..16)
// per ISO/IEC 15444-1 Table D.4.
// magRefCtx returns the magnitude-refinement context for the sample at bordered
// index i (= idx(x,y)); y selects the vertically-causal stripe-boundary handling.
func magRefCtx(t *tier1Ctx, i, y int, firstRef bool) int {
	if firstRef {
		// First refinement: context depends on significant neighbors
		if t.neighborSigSumI(i, y) > 0 {
			return 15
		}
		return 14
	}
	// Subsequent refinements
	return 16
}

// initMQContexts initialises all 19 MQ contexts to the values specified in
// ISO/IEC 15444-1 Table D.5.  It must be called once per code-block, after
// NewMQDecoder, before any DecodeBit call.
func initMQContexts(dec *MQDecoder) {
	// Match OpenJPEG: all contexts start at state 0, MPS=0
	for i := range dec.ctxs {
		dec.ctxs[i] = MQCtx{idx: 0, mps: 0}
	}
	// Then override specific contexts per ISO 15444-1 Table D.7
	// and OpenJPEG opj_mqc_reset_enc / decoder init:
	// ctx 0 (zero coding base): state 4, MPS=0
	dec.ctxs[0] = MQCtx{idx: 4, mps: 0}
	// ctx 17 Run-length/aggregation: state 3, MPS=0
	dec.ctxs[17] = MQCtx{idx: 3, mps: 0}
	// ctx 18 Uniform: state 46, MPS=0
	dec.ctxs[18] = MQCtx{idx: 46, mps: 0}
}

// decodeEBCOTRefinedMQWithPasses decodes with a specific number of coding passes.
// Passes=N means N coding passes total; each bitplane has up to 3 passes (SigProp+MagRef+Cleanup).
// The number of bitplanes decoded = ceil(passes/3), and the last bitplane may have fewer passes.
// cblkStyleSegmark is the code-block style bit (SPcod/SPcoc bit 5) selecting
// segmentation symbols: a 4-bit symbol (0xA) is MQ-coded with the uniform context
// at the end of every cleanup pass and must be consumed to keep the stream aligned.
const cblkStyleSegmark = 0x20

// cblkStyleBypass is the code-block style bit (SPcod/SPcoc bit 0) selecting
// selective arithmetic-coding bypass ("lazy"): from the fifth coded bit-plane the
// significance-propagation and magnitude-refinement passes are raw-coded, not MQ.
const cblkStyleBypass = 0x01

// cblkStyleReset (bit 1) resets the MQ context probability states at the start of
// every coding pass (the arithmetic registers and byte position are unaffected).
const cblkStyleReset = 0x02

// cblkStyleVcausal (bit 3) selects vertically-causal context formation.
const cblkStyleVcausal = 0x08

// cblkStyleTermall (bit 2) terminates the arithmetic coder on every coding pass, so
// each pass is an independently decodable codeword segment.
const cblkStyleTermall = 0x04

// cblkSegMaxPasses returns how many coding passes a codeword segment may hold under the
// given code-block style (ISO 15444-1 B.10.7 / OpenJPEG opj_t2_init_seg) — the same rule
// the tier-2 decoder uses to split a block into segments. first marks the very first
// segment; prevMax is the previous segment's maxpasses.
func cblkSegMaxPasses(cblksty, prevMax int, first bool) int {
	switch {
	case cblksty&cblkStyleTermall != 0:
		return 1
	case cblksty&cblkStyleBypass != 0:
		if first {
			return 10
		}
		if prevMax == 1 || prevMax == 10 {
			return 2
		}
		return 1
	default:
		return 109 // effectively all passes in one segment
	}
}

// passSSE, when non-nil, makes the decoder record per-coding-pass distortion: after
// each pass it appends to .sse the summed squared error of the current reconstruction
// (with the mid-point bias) versus .ref (the full code-block coefficients, dense w*h).
// Used by the rate-control encoder to gather per-pass rate-distortion points in a single
// decode instead of one decode per truncation point.
type passSSE struct {
	ref []int32
	sse []int64
}

// t1Scratch holds the per-code-block working buffers (significance/sign grids and the
// magnitude/refinement/low-plane state). Reusing one across a tile's many code-blocks
// turns thousands of allocations into a handful and removes most of the memclr cost;
// prepare grows the buffers to the block size and zeros them.
type t1Scratch struct {
	ctx          tier1Ctx
	mag          []uint32
	firstRefDone []bool
	lowPlane     []int32
}

func (s *t1Scratch) prepare(w, h int) {
	sw := w + 2
	gn := sw * (h + 2)
	if cap(s.ctx.sig) < gn {
		s.ctx.sig = make([]uint8, gn)
		s.ctx.sgn = make([]uint8, gn)
	} else {
		s.ctx.sig = s.ctx.sig[:gn]
		s.ctx.sgn = s.ctx.sgn[:gn]
	}
	if cap(s.mag) < gn {
		s.mag = make([]uint32, gn)
		s.firstRefDone = make([]bool, gn)
		s.lowPlane = make([]int32, gn)
	} else {
		s.mag = s.mag[:gn]
		s.firstRefDone = s.firstRefDone[:gn]
		s.lowPlane = s.lowPlane[:gn]
	}
	clear(s.ctx.sig)
	clear(s.ctx.sgn)
	clear(s.mag)
	clear(s.firstRefDone)
	clear(s.lowPlane)
	s.ctx.w, s.ctx.h, s.ctx.sw, s.ctx.vcausal = w, h, sw, false
}

func decodeEBCOTRefinedMQWithPasses(w, h, bitdepth int, bytestream []byte, band string, passes, cblksty int, segLens, segPasses []int) (out []int32, lowPlanes []int32, ok bool) {
	return decodeEBCOTPasses(w, h, bitdepth, bytestream, band, passes, cblksty, segLens, segPasses, nil, nil)
}

func decodeEBCOTPasses(w, h, bitdepth int, bytestream []byte, band string, passes, cblksty int, segLens, segPasses []int, track *passSSE, scr *t1Scratch) (out []int32, lowPlanes []int32, ok bool) {
	if w <= 0 || h <= 0 || bitdepth <= 0 {
		return nil, nil, false
	}
	if passes <= 0 {
		passes = bitdepth * 3
	}

	n := w * h
	orient := bandOrient(band)
	if scr == nil {
		scr = &t1Scratch{}
	}
	scr.prepare(w, h)
	t := &scr.ctx
	t.vcausal = cblksty&cblkStyleVcausal != 0
	// Per-sample state shares the bordered index scheme of t.sig/t.sgn so a single
	// idx works for all of them (border cells are simply unused for these).
	gn := t.sw * (h + 2)
	mag := scr.mag
	firstRefDone := scr.firstRefDone
	// lowPlane[i] is the lowest bit-plane at which coefficient i was processed
	// (made significant, or magnitude-refined). The mid-point reconstruction adds
	// half a bit-plane there, per-coefficient — matching OpenJPEG's t1 `oneplushalf`
	// (so a partially-decoded last plane reconstructs exactly, and a fully decoded
	// coefficient at plane 0 gets no bias).
	lowPlane := scr.lowPlane

	// 19 contexts per ISO/IEC 15444-1 Annex D
	dec := NewMQDecoder(bytestream, 19)
	dec.EnableArithmetic(true)
	initMQContexts(dec)

	// Codeword segments: each terminated segment is a fresh arithmetic stream over
	// its slice of bytestream (contexts persist across them). The default code-block
	// style has one segment spanning all passes; styles that terminate mid-block
	// (e.g. termination on each pass) split into several, each carrying segPasses[i]
	// coding passes. Empty segLens means the legacy single-segment behaviour.
	if len(segLens) != len(segPasses) || len(segLens) <= 1 {
		// Legacy / single-segment: the whole bytestream is one terminated segment.
		segLens = []int{len(bytestream)}
		segPasses = []int{passes}
	}
	segIdx := -1
	segOff := 0
	passesLeftInSeg := 0
	nextSegment := func(raw bool) bool {
		segIdx++
		if segIdx >= len(segLens) {
			return false
		}
		end := segOff + segLens[segIdx]
		if end > len(bytestream) {
			end = len(bytestream)
		}
		dec.RestartSegment(bytestream[segOff:end], raw)
		segOff = end
		passesLeftInSeg = 0
		if segIdx < len(segPasses) {
			passesLeftInSeg = segPasses[segIdx]
		}
		return true
	}

	sw := t.sw
	idx := func(x, y int) int { return (y+1)*sw + (x + 1) }

	// Pass ordering: starts with cleanup, then cycles sig→ref→cleanup.
	// bpno starts at bitdepth-1 (the MSB of the coded bit-planes) and
	// decrements after each full cycle of 3 passes.
	bpno := bitdepth - 1
	passtype := 2 // start with cleanup

	// rawPass reports whether the current (bpno, passtype) pass is bypass-coded:
	// under the bypass style, the sig-prop (0) and mag-ref (1) passes from the fifth
	// coded bit-plane onward (four most-significant planes excluded) are raw.
	rawPass := func() bool {
		return cblksty&cblkStyleBypass != 0 && passtype != 2 && (bitdepth-1-bpno) >= 4
	}

	// justSig tracks samples that became significant in the current bitplane.
	// Reset when bpno changes (after cleanup pass).
	justSig := make([]bool, gn)

	// makeSignificant decodes the sign bit and marks a sample as significant.
	makeSignificant := func(x, y, i int) bool {
		signCtx, flip := signCtxISO(t, i, y)
		s, ok := dec.DecodeBit(signCtx)
		if !ok {
			return false
		}
		// Under bypass (raw) coding the sign is the raw bit directly; the
		// context-based sign prediction/flip applies only to MQ-coded signs.
		if !dec.rawMode {
			s ^= flip
		}
		t.setSig(x, y, 1)
		t.setSign(x, y, s&1)
		justSig[i] = true
		mag[i] |= 1 << uint(bpno)
		lowPlane[i] = int32(bpno)
		firstRefDone[i] = false
		return true
	}

	for passno := 0; passno < passes && bpno >= 0; passno++ {
		// Advance to the next codeword segment when the current one is exhausted; if
		// there are no more segments but passes remain (truncated stream), stop. The
		// upcoming pass's raw/MQ mode selects how the new segment is initialised.
		for passesLeftInSeg == 0 {
			if !nextSegment(rawPass()) {
				passno = passes
				break
			}
		}
		if passno >= passes {
			break
		}
		dec.SetRawMode(rawPass())
		// Reset style: restore the context probabilities to their initial states at
		// the start of each coding pass (registers and byte position are untouched).
		if cblksty&cblkStyleReset != 0 {
			initMQContexts(dec)
		}
		switch passtype {
		case 0: // Significance propagation pass (stripe-column order)
			for stripeTop := 0; stripeTop < h; stripeTop += 4 {
				stripeH := 4
				if stripeTop+stripeH > h {
					stripeH = h - stripeTop
				}
				for x := 0; x < w; x++ {
					// y rises by one each step, so the bordered index advances by one row
					// stride sw — hoist the (y+1)*sw+(x+1) multiply out of the inner loop.
					i := idx(x, stripeTop)
					for dy := 0; dy < stripeH; dy, i = dy+1, i+sw {
						y := stripeTop + dy
						if t.sig[i] != 0 || justSig[i] {
							continue
						}
						// ctx == 0 means no significant neighbour (= neighborSigSum 0): skip,
						// computing the context once instead of scanning the neighbours twice.
						ctx := sigCtxAt(t, i, y, orient)
						if ctx == 0 {
							continue
						}
						// Mark as processed (PI flag equivalent) so cleanup skips this sample.
						justSig[i] = true
						b, okb := dec.DecodeBit(ctx)
						if !okb {
							return nil, nil, false
						}
						if b == 1 {
							if !makeSignificant(x, y, i) {
								return nil, nil, false
							}
						}
					}
				}
			}

		case 1: // Magnitude refinement pass (stripe-column order)
			for stripeTop := 0; stripeTop < h; stripeTop += 4 {
				stripeH := 4
				if stripeTop+stripeH > h {
					stripeH = h - stripeTop
				}
				for x := 0; x < w; x++ {
					i := idx(x, stripeTop)
					for dy := 0; dy < stripeH; dy, i = dy+1, i+sw {
						y := stripeTop + dy
						if t.sig[i] == 0 || justSig[i] {
							continue
						}
						ctx := magRefCtx(t, i, y, !firstRefDone[i])
						b, okb := dec.DecodeBit(ctx)
						if !okb {
							return nil, nil, false
						}
						if b != 0 {
							mag[i] |= 1 << uint(bpno)
						}
						lowPlane[i] = int32(bpno)
						firstRefDone[i] = true
					}
				}
			}

		case 2: // Cleanup pass
			for stripeTop := 0; stripeTop < h; stripeTop += 4 {
				stripeH := 4
				if stripeTop+stripeH > h {
					stripeH = h - stripeTop
				}
				for x := 0; x < w; x++ {
					allZero := stripeH == 4
					if allZero {
						i := idx(x, stripeTop)
						for dy := 0; dy < 4; dy, i = dy+1, i+sw {
							y := stripeTop + dy
							if t.sig[i] != 0 || justSig[i] || t.neighborSigSumI(i, y) != 0 {
								allZero = false
								break
							}
						}
					}

					if allZero {
						rl, okrl := dec.DecodeBit(17)
						if !okrl {
							return nil, nil, false
						}
						if rl == 0 {
							continue
						}
						p1, okp1 := dec.DecodeBit(18)
						if !okp1 {
							return nil, nil, false
						}
						p0, okp0 := dec.DecodeBit(18)
						if !okp0 {
							return nil, nil, false
						}
						runPos := int(p1<<1 | p0)

						i := idx(x, stripeTop)
						for dy := 0; dy < stripeH; dy, i = dy+1, i+sw {
							y := stripeTop + dy
							if t.sig[i] != 0 || justSig[i] {
								continue
							}
							if dy < runPos {
								continue
							}
							if dy == runPos {
								if !makeSignificant(x, y, i) {
									return nil, nil, false
								}
							} else {
								ctx := sigCtxAt(t, i, y, orient)
								b, okb := dec.DecodeBit(ctx)
								if !okb {
									return nil, nil, false
								}
								if b == 1 {
									if !makeSignificant(x, y, i) {
										return nil, nil, false
									}
								}
							}
						}
					} else {
						i := idx(x, stripeTop)
						for dy := 0; dy < stripeH; dy, i = dy+1, i+sw {
							y := stripeTop + dy
							if t.sig[i] != 0 || justSig[i] {
								continue
							}
							ctx := sigCtxAt(t, i, y, orient)
							b, okb := dec.DecodeBit(ctx)
							if !okb {
								return nil, nil, false
							}
							if b == 1 {
								if !makeSignificant(x, y, i) {
									return nil, nil, false
								}
							}
						}
					}
				}
			}
		}

		// Segmentation symbols (error-resilience): when enabled, a 4-bit symbol is
		// coded with the uniform context at the end of each cleanup pass. Consume it
		// so the MQ stream stays aligned; the value should be 0xA but we do not fail
		// on a mismatch (the marker is for detection, and we decode best-effort).
		if passtype == 2 && cblksty&cblkStyleSegmark != 0 {
			for k := 0; k < 4; k++ {
				if _, okb := dec.DecodeBit(18); !okb { // ctx 18 = uniform
					return nil, nil, false
				}
			}
		}

		passesLeftInSeg--

		// Per-pass distortion snapshot for rate control: SSE of the current
		// reconstruction (mid-point bias) versus the full coefficients.
		if track != nil {
			var s int64
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					i := idx(x, y)
					v := int32(mag[i])
					if lp := lowPlane[i]; lp > 0 {
						v += (int32(1) << uint(lp)) >> 1
					}
					if t.getSign(x, y) != 0 {
						v = -v
					}
					d := int64(track.ref[y*w+x] - v)
					s += d * d
				}
			}
			track.sse = append(track.sse, s)
		}

		passtype++
		if passtype == 3 {
			passtype = 0
			bpno--
			for i := range justSig {
				justSig[i] = false
			}
		}
	}

	// Assemble output with sign. out/lowPlanes use the dense w*h layout the caller
	// expects, while mag/sign/lowPlane use the bordered grid. lowPlanes[i] is the
	// lowest plane coefficient i was decoded to, so dequantization can add the
	// per-coefficient mid-point bias (0.5·2^lowPlane) — exact for a fully decoded
	// coefficient, correct for a partially decoded last plane.
	out = make([]int32, n)
	lowPlanes = make([]int32, n)
	anyTrunc := false
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := idx(x, y)
			v := int32(mag[i])
			if t.getSign(x, y) != 0 {
				v = -v
			}
			out[y*w+x] = v
			lp := lowPlane[i]
			lowPlanes[y*w+x] = lp
			if lp > 0 {
				anyTrunc = true
			}
		}
	}
	// A fully decoded block reaches plane 0 for every significant coefficient, so
	// no per-coefficient bias is needed; signal that with a nil slice (the 9/7
	// dequant then uses the default 0.5 mid-point, the 5/3 path skips the bias).
	if !anyTrunc {
		lowPlanes = nil
	}
	return out, lowPlanes, true
}
