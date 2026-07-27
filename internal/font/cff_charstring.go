// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package font

import (
	"errors"

	psi "github.com/go-text/typesetting/font/cff/interpreter"
	"github.com/go-text/typesetting/font/opentype"
)

// Budgeted Type 2 charstring interpretation (Adobe TN5177), the outline source for every CFF program the engine draws:
// bare CFF/Type1C simple fonts, CIDFontType0 descendants, and the 'CFF ' table of a CFF-flavored OpenType program.
//
// go-text supplies the machine (psi.Machine) and the geometry operators (psi.CharstringReader), and its own
// cff.CFF.LoadGlyph drives them with an unbudgeted handler: psi.Machine caps subroutine *nesting* at 10 but nothing
// caps *branching*, so a charstring whose subroutines each call the next N times costs N^10 operator dispatches. A
// 213-byte program with a branch factor of 9 already needs seconds for one glyph and the cost is exactly exponential in
// the branch factor, so a slightly wider one never returns. The handler here is go-text's, operator for operator, with
// a work budget threaded through it — the same shape as the direct glyf walker's glyfWorkBudget and internal/type1's
// maxHandlerOps, which exist for the identical reason. Because it drives the very same CharstringReader methods, the
// outlines it produces are those LoadGlyph produced; only the hostile programs behave differently, and they draw
// nothing.
//
// Driving the machine ourselves means owning the subroutine arrays too: go-text keeps them unexported, so cffSubrs
// re-walks the container (INDEX and DICT readers from cff.go) for the Global Subr INDEX, the Private DICT's local Subrs
// and — for CID-keyed programs — the per-FDArray Private DICTs with the FDSelect that picks among them.

// Caps against hostile Type 2 charstrings.
const (
	// maxCFFHandlerOps bounds one glyph's interpretation. Each operator costs one unit plus one per operand on the
	// argument stack when it runs, so the budget prices the tokens actually executed rather than only the operators:
	// without the operand charge a program could pair every dispatch with a 512-deep push run (the argument stack's own
	// ceiling) and buy 500x the work for the same count. Real glyphs — including the densest CJK outlines — run a few
	// thousand units.
	maxCFFHandlerOps = 1 << 18
	// maxCFFSegments bounds one glyph's emitted outline segments, like internal/type1's cap of the same name: the
	// budget above bounds the operators, but rlineto and its relatives emit one segment per operand pair, so the
	// segment count needs its own ceiling to keep a legal-looking charstring from amplifying into hundreds of megabytes
	// of path.
	maxCFFSegments = 1 << 14
	// maxCFFSubrs bounds the entries read from one subroutine INDEX. An INDEX count is a Card16, so this drops nothing
	// a conforming program can express.
	maxCFFSubrs = 65536
	// maxCFFFontDicts bounds the FDArray entries of a CID-keyed program (FDSelect indices are single bytes).
	maxCFFFontDicts = 256
)

var errCFFBudget = errors.New("charstring work budget exhausted")

// cffSubrs holds the subroutine arrays a Type 2 charstring may call. local is indexed by font DICT: exactly one entry
// for a non-CID program, one per FDArray entry for a CID-keyed one, where fdSelect maps a GID to the entry that
// applies.
type cffSubrs struct {
	global   [][]byte
	local    [][][]byte
	fdSelect []uint8
}

// globalFor returns the global subroutines (nil-receiver safe: a program whose container walk failed simply has none).
func (c *cffSubrs) globalFor() [][]byte {
	if c == nil {
		return nil
	}
	return c.global
}

// localFor returns the local subroutines that apply to a GID.
func (c *cffSubrs) localFor(gid uint32) [][]byte {
	if c == nil || len(c.local) == 0 {
		return nil
	}
	if len(c.local) == 1 { // Non-CID programs have a single Private DICT, which every glyph shares.
		return c.local[0]
	}
	if int(gid) < len(c.fdSelect) {
		if fd := int(c.fdSelect[gid]); fd < len(c.local) {
			return c.local[fd]
		}
	}
	return nil
}

// parseCFFSubrs re-walks a CFF container for its subroutine arrays. Anything unreadable yields nil or a partial result
// rather than an error: a missing array leaves the calls into it failing, which drops the glyph, and that is the same
// degradation every other malformed-program path in this package takes.
func parseCFFSubrs(data []byte, top *cffTop, nGlyphs int) *cffSubrs {
	if len(data) < 4 {
		return nil
	}
	hdrSize := int(data[2])
	if hdrSize < 4 || hdrSize > len(data) {
		return nil
	}
	// The Global Subr INDEX is the fourth INDEX of the container, after Name, Top DICT and String (TN5176 section 1).
	pos, err := cffSkipIndex(data, hdrSize)
	if err != nil {
		return nil
	}
	if pos, err = cffSkipIndex(data, pos); err != nil {
		return nil
	}
	if pos, err = cffSkipIndex(data, pos); err != nil {
		return nil
	}
	global, _, err := cffIndex(data, pos, maxCFFSubrs)
	if err != nil {
		return nil
	}
	// One budget covers every array the program declares. A single INDEX cannot exceed maxCFFSubrs (its count is a
	// Card16), but a CID-keyed program may name up to 256 font DICTs, and pointing all of them at one large INDEX would
	// otherwise turn a 64 KB program into hundreds of megabytes of slice headers.
	budget := maxCFFSubrs - len(global)
	out := &cffSubrs{global: global}
	if !top.isCID {
		out.local = [][][]byte{cffLocalSubrs(data, top.privOff, top.privSize, &budget)}
		return out
	}
	// CID-keyed: the Private DICT (and so the local subroutines) is per font DICT, and FDSelect names the one each
	// glyph uses (TN5176 sections 18-19).
	fontDicts, _, err := cffIndex(data, top.fdArrayOff, maxCFFFontDicts)
	if err != nil || len(fontDicts) == 0 {
		return out
	}
	out.local = make([][][]byte, len(fontDicts))
	for i, dict := range fontDicts {
		privOff, privSize := cffPrivateRange(dict)
		out.local[i] = cffLocalSubrs(data, privOff, privSize, &budget)
	}
	out.fdSelect = parseCFFFDSelect(data, top.fdSelectOff, nGlyphs)
	return out
}

// cffPrivateRange reads a font DICT's Private entry (operator 18: size then offset). A DICT that does not decode
// cleanly names no usable range, the same verdict parseCFFTopDict reaches for a malformed Top DICT.
func cffPrivateRange(dict []byte) (off, size int) {
	if err := cffWalkDict(dict, func(op int, operands []float64) {
		if op == 18 && len(operands) >= 2 {
			size = clampDictOffset(operands[len(operands)-2])
			off = clampDictOffset(operands[len(operands)-1])
		}
	}); err != nil {
		return 0, 0
	}
	return off, size
}

// cffLocalSubrs reads the local Subrs INDEX of one Private DICT (operator 19, whose offset is relative to the start of
// the Private DICT data), charging its entries against the shared budget.
func cffLocalSubrs(data []byte, privOff, privSize int, budget *int) [][]byte {
	// Every bound is compared in int64 for the reason cffIndex gives: clampDictOffset admits anything up to MaxInt32,
	// so the sums below wrap negative on a 32-bit build and would let the slicing panic.
	if privOff <= 0 || privSize <= 0 || int64(privOff)+int64(privSize) > int64(len(data)) {
		return nil
	}
	subrsOff := 0
	if err := cffWalkDict(data[privOff:privOff+privSize], func(op int, operands []float64) {
		if op == 19 && len(operands) >= 1 {
			subrsOff = clampDictOffset(operands[len(operands)-1])
		}
	}); err != nil {
		return nil
	}
	if subrsOff <= 0 || int64(privOff)+int64(subrsOff) > int64(len(data)) {
		return nil
	}
	// An array trimmed to fit the budget would shift the Type 2 subroutine bias (which is derived from the array's
	// length) and resolve every call in this font DICT to the wrong routine, so one that does not fit yields nothing at
	// all: the glyphs go blank rather than drawing another routine's shape.
	if count := cffIndexCount(data, privOff+subrsOff); count < 0 || count > *budget {
		return nil
	}
	subrs, _, err := cffIndex(data, privOff+subrsOff, maxCFFSubrs)
	if err != nil {
		return nil
	}
	*budget -= len(subrs)
	return subrs
}

// parseCFFFDSelect decodes the GID→font DICT index map of a CID-keyed program (TN5176 section 19), returning nil when
// it is absent or malformed — glyphs then find no local subroutines rather than the wrong ones.
func parseCFFFDSelect(data []byte, off, nGlyphs int) []uint8 {
	if off <= 0 || off >= len(data) || nGlyphs <= 0 || nGlyphs > maxCFFSubrs {
		return nil
	}
	switch data[off] {
	case 0: // One byte per glyph.
		if int64(off)+1+int64(nGlyphs) > int64(len(data)) {
			return nil
		}
		// Copied rather than aliased: the map outlives the parse, and a subslice would pin the whole container.
		return append([]uint8(nil), data[off+1:off+1+nGlyphs]...)
	case 3: // Ranges of (first GID, FD index) with a sentinel GID closing the last one.
		return cffFDSelectRanges(data, off+1, nGlyphs, 2)
	case 4: // The same, with 32-bit GIDs.
		return cffFDSelectRanges(data, off+1, nGlyphs, 4)
	}
	return nil
}

// cffFDSelectRanges expands the range form of FDSelect. gidSize is 2 for format 3 and 4 for format 4; the range count
// and the trailing sentinel are the same width.
func cffFDSelectRanges(data []byte, pos, nGlyphs, gidSize int) []uint8 {
	be := func(at, width int) uint64 {
		var v uint64
		for i := range width {
			v = v<<8 | uint64(data[at+i])
		}
		return v
	}
	if int64(pos)+int64(gidSize) > int64(len(data)) {
		return nil
	}
	// The count is kept in an int64 until it has been bounded by the data length: format 4 declares it in 32 bits,
	// which does not fit an int on a 32-bit build.
	nRanges := int64(be(pos, gidSize))
	pos += gidSize
	// Each record is a GID plus one FD byte, and a sentinel GID follows the last one.
	if nRanges <= 0 || int64(pos)+nRanges*int64(gidSize+1)+int64(gidSize) > int64(len(data)) {
		return nil
	}
	out := make([]uint8, nGlyphs)
	covered := uint64(0)
	for i := range int(nRanges) {
		at := pos + i*(gidSize+1)
		first := be(at, gidSize)
		// TN5176 section 19 requires the records to be in increasing GID order, which is also what go-text's binary
		// search over them assumes. Enforcing it keeps each glyph written at most once: overlapping records would let a
		// short table cost nRanges × nGlyphs writes, and a mis-ordered one cannot be read correctly anyway.
		if first < covered {
			return nil
		}
		fd := data[at+gidSize]
		next := be(at+gidSize+1, gidSize) // The following record's GID, or the sentinel for the last range.
		for gid := first; gid < next && gid < uint64(nGlyphs); gid++ {
			out[gid] = fd
		}
		covered = next
	}
	return out
}

// type2Handler interprets a Type 2 charstring, mirroring go-text's own handler operator for operator so the outlines
// match what cff.CFF.LoadGlyph would have produced, and charging every dispatch against a work budget so a hostile
// subroutine call graph cannot run unbounded.
type type2Handler struct {
	cs   psi.CharstringReader
	work int
}

// Context implements psi.OperatorHandler.
func (*type2Handler) Context() psi.Context { return psi.Type2Charstring }

// Apply implements psi.OperatorHandler. Type 2 semantics clear the argument stack after every operator except the ones
// that manage it themselves (callsubr/callgsubr/return and the hint masks).
func (h *type2Handler) Apply(state *psi.Machine, op psi.Operator) error {
	h.work += 1 + int(state.ArgStack.Top)
	if h.work > maxCFFHandlerOps || len(h.cs.Segments) > maxCFFSegments {
		return errCFFBudget
	}
	var err error
	if op.IsEscaped {
		switch op.Operator {
		case 34: // hflex
			err = h.cs.Hflex(state)
		case 35: // flex
			err = h.cs.Flex(state)
		case 36: // hflex1
			err = h.cs.Hflex1(state)
		case 37: // flex1
			err = h.cs.Flex1(state)
		default:
			err = errBadCFF
		}
		state.ArgStack.Clear()
		return err
	}
	switch op.Operator {
	case 11: // return
		return state.Return() // Does not clear the argument stack.
	case 14: // endchar
		// The optional leading width operand is the PDF /Widths' business, not the outline's, so it is dropped here.
		h.cs.ClosePath()
		return psi.ErrInterrupt
	case 10: // callsubr
		return psi.LocalSubr(state) // Does not clear the argument stack.
	case 29: // callgsubr
		return psi.GlobalSubr(state) // Does not clear the argument stack.
	case 21: // rmoveto
		err = h.cs.Rmoveto(state)
	case 22: // hmoveto
		err = h.cs.Hmoveto(state)
	case 4: // vmoveto
		err = h.cs.Vmoveto(state)
	case 1, 18: // hstem, hstemhm
		h.cs.Hstem(state)
	case 3, 23: // vstem, vstemhm
		h.cs.Vstem(state)
	case 19, 20: // hintmask, cntrmask
		h.cs.Hintmask(state) // Skips the mask bytes and manages the stack itself.
		return nil
	case 5: // rlineto
		h.cs.Rlineto(state)
	case 6: // hlineto
		h.cs.Hlineto(state)
	case 7: // vlineto
		h.cs.Vlineto(state)
	case 8: // rrcurveto
		h.cs.Rrcurveto(state)
	case 24: // rcurveline
		err = h.cs.Rcurveline(state)
	case 25: // rlinecurve
		err = h.cs.Rlinecurve(state)
	case 26: // vvcurveto
		h.cs.Vvcurveto(state)
	case 27: // hhcurveto
		h.cs.Hhcurveto(state)
	case 30: // vhcurveto
		h.cs.Vhcurveto(state)
	case 31: // hvcurveto
		h.cs.Hvcurveto(state)
	default:
		err = errBadCFF
	}
	state.ArgStack.Clear()
	return err
}

// glyphSegments interprets one glyph's charstring under the work budget, reporting false when the glyph is out of range
// or the charstring failed (malformed, or over budget) — the caller then draws nothing, exactly as it did for a
// LoadGlyph error.
func (c *cffInfo) glyphSegments(gid uint32) ([]opentype.Segment, bool) {
	if c.font == nil || uint64(gid) >= uint64(len(c.font.Charstrings)) {
		return nil, false
	}
	var (
		machine psi.Machine
		handler type2Handler
	)
	if err := machine.Run(c.font.Charstrings[gid], c.subrs.localFor(gid), c.subrs.globalFor(), &handler); err != nil {
		return nil, false
	}
	return handler.cs.Segments, true
}
