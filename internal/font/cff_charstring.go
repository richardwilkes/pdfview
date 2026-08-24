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
	"math"

	psi "github.com/go-text/typesetting/font/cff/interpreter"
	"github.com/go-text/typesetting/font/opentype"
)

// Budgeted Type 2 charstring interpretation (Adobe TN5177) for every CFF program the engine draws: bare CFF/Type1C
// simple fonts, CIDFontType0 descendants, and the 'CFF ' table of a CFF-flavored OpenType program.
//
// go-text supplies the machine (psi.Machine) and the geometry operators (psi.CharstringReader), and its own
// cff.CFF.LoadGlyph drives them unbudgeted: psi.Machine caps subroutine nesting at 10 but nothing caps branching, so a
// charstring whose subroutines each call the next N times costs N^10 dispatches. A few hundred bytes with a branch
// factor of 9 cost seconds for one glyph, and a slightly wider program never returns. The handler here is go-text's,
// operator for operator, with a work budget threaded through it, like the glyf walker's glyfWorkBudget and
// internal/type1's maxHandlerOps. It drives the same CharstringReader methods, so valid programs produce exactly the
// outlines LoadGlyph does; only hostile programs differ, and they draw nothing.
//
// Three TN5177-legal forms go-text fails or ignores are implemented because FreeType, and so MuPDF, accepts them: the
// deprecated dotsection hint runs as a no-op, the arithmetic/storage/conditional operator group (sections 4.4-4.5) runs
// (compute), and the four-operand endchar composes two standard-encoding glyphs like Type 1's seac (seacEndchar).
// Type1C conversions of Adobe-era fonts carry all three.
//
// go-text keeps the subroutine arrays unexported, so cffSubrs re-walks the container (with cff.go's INDEX and DICT
// readers) for the Global Subr INDEX, the Private DICT's local Subrs and, for CID-keyed programs, the per-FDArray
// Private DICTs with the FDSelect that picks among them.

// Caps against hostile Type 2 charstrings.
const (
	// maxCFFHandlerOps bounds one glyph's interpretation. Each operator costs one unit plus one per operand on the
	// argument stack, so a program cannot pair every dispatch with a 513-deep push run (the argument stack's size) and
	// buy 500x the work for the same count. Real glyphs, including the densest CJK outlines, run a few thousand units.
	maxCFFHandlerOps = 1 << 18
	// maxCFFSegments bounds one glyph's emitted outline segments, like internal/type1's maxSegments: rlineto and its
	// relatives emit one segment per operand pair, so the operator budget alone would let a legal-looking charstring
	// amplify into hundreds of megabytes of path.
	maxCFFSegments = 1 << 14
	// maxCFFSubrs bounds the entries read from one subroutine INDEX. An INDEX count is a Card16, so this drops nothing
	// a conforming program can express.
	maxCFFSubrs = 65536
	// maxCFFFontDicts bounds the FDArray entries of a CID-keyed program (FDSelect indices are single bytes).
	maxCFFFontDicts = 256
	// maxCFFArithValue bounds every value the arithmetic operators push. Parsed numbers are at most 16-bit-integer
	// sized, so only a mul/add chain can carry a coordinate past float32's range, and a non-finite float32 must never
	// reach outline geometry. 2^30 is far past any real coordinate and keeps a full budget's worth of accumulation finite.
	maxCFFArithValue = 1 << 30
	// cffTransientSize is the transient array behind put/get, at the size TN5177 Appendix B guarantees.
	cffTransientSize = 32
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

// globalFor returns the global subroutines (nil-safe: a program whose container walk failed has none).
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
// rather than an error: calls into a missing array fail and drop the glyph, the same degradation every other
// malformed-program path in this package takes.
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
	// One budget covers every array the program declares: a CID-keyed program may name up to 256 font DICTs, and
	// pointing all of them at one large INDEX would otherwise turn a 64 KB program into hundreds of megabytes of slice
	// headers.
	budget := maxCFFSubrs - len(global)
	out := &cffSubrs{global: global}
	if !top.isCID {
		out.local = [][][]byte{cffLocalSubrs(data, top.privOff, top.privSize, &budget)}
		return out
	}
	// CID-keyed: the Private DICT, and so the local subroutines, is per font DICT, and FDSelect names the one each glyph
	// uses (TN5176 sections 18-19).
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
	if privOff <= 0 || privSize <= 0 || privOff+privSize > len(data) {
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
	if subrsOff <= 0 || privOff+subrsOff > len(data) {
		return nil
	}
	// An array trimmed to fit the budget would shift the Type 2 subroutine bias, which derives from the array's length,
	// and resolve every call in this font DICT to the wrong routine, so one that does not fit yields nothing: the glyphs
	// go blank rather than drawing another routine's shape.
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
		if off+1+nGlyphs > len(data) {
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
	if pos+gidSize > len(data) {
		return nil
	}
	// Format 4 declares the count in 32 bits, so the record-span product below reaches 2^34 — well inside the 64-bit
	// int this engine requires.
	nRanges := int(be(pos, gidSize))
	pos += gidSize
	// Each record is a GID plus one FD byte, and a sentinel GID follows the last one.
	if nRanges <= 0 || pos+nRanges*(gidSize+1)+gidSize > len(data) {
		return nil
	}
	out := make([]uint8, nGlyphs)
	covered := uint64(0)
	for i := range nRanges {
		at := pos + i*(gidSize+1)
		first := be(at, gidSize)
		// TN5176 section 19 requires increasing GID order, which go-text's binary search also assumes. Enforcing it writes
		// each glyph at most once: overlapping records would let a short table cost nRanges × nGlyphs writes, and a
		// mis-ordered one cannot be read correctly anyway.
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

// type2Handler interprets a Type 2 charstring under the work budget, mirroring go-text's handler operator for operator
// plus the additions described in the file comment above.
type type2Handler struct {
	info *cffInfo // The owning program, for the component charstrings a seac endchar names.
	cs   psi.CharstringReader
	// transient is the scratch array behind put and get (TN5177 section 4.5), fresh per glyph like FreeType's.
	transient [cffTransientSize]float64
	work      int
	rng       uint32 // random's deterministic generator state, seeded on first use.
	inSeac    bool   // Set while interpreting a seac component, where further composition is forbidden.
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
		case 0: // dotsection
			// A Type 1 hint bracketing the dot of letters like 'i', kept by Type1C conversions of Adobe-era fonts. TN5177
			// Appendix A reserves it and FreeType runs it as a no-op; go-text's handler rejects it, dropping the whole glyph
			// over an operator that carries no geometry.
		case 3, 4, 5, 9, 10, 11, 12, 14, 15, 18, 20, 21, 22, 23, 24, 26, 27, 28, 29, 30:
			// The arithmetic, storage, and conditional operators leave their results on the stack for the operator that
			// consumes them, so they never clear it.
			return h.compute(state, op.Operator)
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
		return state.Return()
	case 14: // endchar
		// The optional leading width is dropped: /Widths supplies advances. Four operands (five with the width) are the
		// deprecated accented-glyph form, exactly the two counts FreeType accepts, so other leftovers on the stack still
		// just end the glyph.
		if state.ArgStack.Top == 4 || state.ArgStack.Top == 5 {
			if err = h.seacEndchar(state); err != nil {
				return err
			}
		}
		h.cs.ClosePath()
		return psi.ErrInterrupt
	case 10: // callsubr
		return psi.LocalSubr(state)
	case 29: // callgsubr
		return psi.GlobalSubr(state)
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
		h.cs.Hintmask(state)
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
// or the charstring failed (malformed, or over budget); the caller then draws nothing.
func (c *cffInfo) glyphSegments(gid uint32) ([]opentype.Segment, bool) {
	if c.font == nil || uint64(gid) >= uint64(len(c.font.Charstrings)) {
		return nil, false
	}
	var machine psi.Machine
	handler := type2Handler{info: c}
	if err := machine.Run(c.font.Charstrings[gid], c.subrs.localFor(gid), c.subrs.globalFor(), &handler); err != nil {
		return nil, false
	}
	return handler.cs.Segments, true
}

// compute runs one operator of the arithmetic, storage, and conditional group (TN5177 sections 4.4 and 4.5), which
// go-text's handler rejects wholesale even though old Type1C conversions reach div and its siblings. Results stay on
// the stack for the operator that consumes them. Malformed use (underflow, a zero divisor, a negative square root, an
// out-of-range index) fails the glyph, and every pushed value is bounded by maxCFFArithValue.
func (h *type2Handler) compute(state *psi.Machine, op byte) error {
	st := &state.ArgStack
	push := func(v float64) error {
		// The comparison is written so NaN fails it too.
		if int(st.Top) >= len(st.Vals) || !(v >= -maxCFFArithValue && v <= maxCFFArithValue) {
			return errBadCFF
		}
		st.Vals[st.Top] = v
		st.Top++
		return nil
	}
	unary := func(f func(v float64) float64) error {
		if st.Top < 1 {
			return errBadCFF
		}
		return push(f(st.Pop()))
	}
	binary := func(f func(a, b float64) float64) error {
		if st.Top < 2 {
			return errBadCFF
		}
		b := st.Pop()
		a := st.Pop()
		return push(f(a, b))
	}
	truth := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	switch op {
	case 3: // and
		return binary(func(a, b float64) float64 { return truth(a != 0 && b != 0) })
	case 4: // or
		return binary(func(a, b float64) float64 { return truth(a != 0 || b != 0) })
	case 5: // not
		return unary(func(v float64) float64 { return truth(v == 0) })
	case 9: // abs
		return unary(math.Abs)
	case 10: // add
		return binary(func(a, b float64) float64 { return a + b })
	case 11: // sub
		return binary(func(a, b float64) float64 { return a - b })
	case 12: // div
		if st.Top < 2 || st.Vals[st.Top-1] == 0 {
			return errBadCFF
		}
		return binary(func(a, b float64) float64 { return a / b })
	case 14: // neg
		return unary(func(v float64) float64 { return -v })
	case 15: // eq
		return binary(func(a, b float64) float64 { return truth(a == b) })
	case 18: // drop
		if st.Top < 1 {
			return errBadCFF
		}
		st.Pop()
		return nil
	case 20: // put
		if st.Top < 2 {
			return errBadCFF
		}
		i := st.Pop()
		v := st.Pop()
		if !(i >= 0 && i < cffTransientSize) {
			return errBadCFF
		}
		h.transient[int(i)] = v
		return nil
	case 21: // get
		if st.Top < 1 {
			return errBadCFF
		}
		i := st.Pop()
		if !(i >= 0 && i < cffTransientSize) {
			return errBadCFF
		}
		return push(h.transient[int(i)])
	case 22: // ifelse: s1 s2 v1 v2 → s1 when v1 <= v2, else s2.
		if st.Top < 4 {
			return errBadCFF
		}
		v2 := st.Pop()
		v1 := st.Pop()
		s2 := st.Pop()
		s1 := st.Pop()
		if v1 <= v2 {
			return push(s1)
		}
		return push(s2)
	case 23: // random
		return push(h.random())
	case 24: // mul
		return binary(func(a, b float64) float64 { return a * b })
	case 26: // sqrt
		if st.Top < 1 || st.Vals[st.Top-1] < 0 {
			return errBadCFF
		}
		return unary(math.Sqrt)
	case 27: // dup
		if st.Top < 1 {
			return errBadCFF
		}
		return push(st.Vals[st.Top-1])
	case 28: // exch
		if st.Top < 2 {
			return errBadCFF
		}
		st.Vals[st.Top-1], st.Vals[st.Top-2] = st.Vals[st.Top-2], st.Vals[st.Top-1]
		return nil
	case 29: // index: a negative operand duplicates the top element (TN5177 section 4.4).
		if st.Top < 1 {
			return errBadCFF
		}
		i := st.Pop()
		if i >= float64(st.Top) {
			return errBadCFF
		}
		if i < 0 {
			i = 0
		}
		return push(st.Vals[st.Top-1-int32(i)])
	case 30: // roll
		if st.Top < 2 {
			return errBadCFF
		}
		j := st.Pop()
		n := st.Pop()
		if !(j >= math.MinInt32 && j <= math.MaxInt32) || !(n >= 0 && n <= float64(st.Top)) {
			return errBadCFF
		}
		rollStack(st, int32(n), int32(j))
		return nil
	}
	return errBadCFF
}

// rollStack circularly shifts the top n stack entries by j positions, positive j toward the top of the stack (the
// PostScript roll convention TN5177 adopts).
func rollStack(st *psi.ArgStack, n, j int32) {
	if n <= 1 {
		return
	}
	j %= n
	if j < 0 {
		j += n
	}
	if j == 0 {
		return
	}
	group := st.Vals[st.Top-n : st.Top]
	rotated := make([]float64, n)
	for i := range group {
		rotated[(int32(i)+j)%n] = group[i]
	}
	copy(group, rotated)
}

// random returns the next value in (0,1] for operator 12 23. A page must raster identically on every run and platform,
// so this is a fixed-seed xorshift32 sequence rather than anything environmental (FreeType is deterministic here too).
func (h *type2Handler) random() float64 {
	if h.rng == 0 {
		h.rng = 2463534242 // Marsaglia's xorshift32 example seed.
	}
	h.rng ^= h.rng << 13
	h.rng ^= h.rng >> 17
	h.rng ^= h.rng << 5
	return (float64(h.rng%(1<<24)) + 1) / (1 << 24)
}

// seacEndchar composes the deprecated accented-glyph endchar (TN5177 Appendix C): adx ady bchar achar name two
// standard-encoding glyphs, the base drawn in place and the accent displaced by (adx, ady). go-text returns an empty
// outline for such glyphs; FreeType composes them. A component may not itself compose (FreeType rejects the nesting
// too), and both components share the caller's work budget, so a self-referential program terminates.
func (h *type2Handler) seacEndchar(state *psi.Machine) error {
	if h.info == nil || h.inSeac {
		return errBadCFF
	}
	top := state.ArgStack.Top
	adx := state.ArgStack.Vals[top-4]
	ady := state.ArgStack.Vals[top-3]
	base, ok := h.info.stdEncodingGID(state.ArgStack.Vals[top-2])
	if !ok {
		return errBadCFF
	}
	accent, ok := h.info.stdEncodingGID(state.ArgStack.Vals[top-1])
	if !ok {
		return errBadCFF
	}
	if err := h.seacComponent(base, 0, 0); err != nil {
		return err
	}
	// Stack values are bounded by the machine's number forms and compute's push, so the float32 narrowing is finite.
	return h.seacComponent(accent, float32(adx), float32(ady))
}

// stdEncodingGID resolves a seac operand: a StandardEncoding code whose glyph name the program's charset must map to a
// GID. A CID-keyed program has no charset names, so composition correctly fails there.
func (c *cffInfo) stdEncodingGID(code float64) (uint32, bool) {
	if !(code >= 0 && code < 256) {
		return 0, false
	}
	name := standardEncoding[int(code)]
	if name == "" {
		return 0, false
	}
	gid, ok := c.names[name]
	return gid, ok
}

// seacComponent interprets one component charstring in a fresh sub-handler and appends its outline displaced by
// (dx, dy). The sub-handler continues this handler's work budget, and the segment cap is re-checked over the combined
// outline, so composition buys a hostile program nothing.
func (h *type2Handler) seacComponent(gid uint32, dx, dy float32) error {
	if uint64(gid) >= uint64(len(h.info.font.Charstrings)) {
		return errBadCFF
	}
	sub := type2Handler{info: h.info, work: h.work, inSeac: true}
	var machine psi.Machine
	if err := machine.Run(h.info.font.Charstrings[gid], h.info.subrs.localFor(gid), h.info.subrs.globalFor(), &sub); err != nil {
		return err
	}
	h.work = sub.work
	if len(h.cs.Segments)+len(sub.cs.Segments) > maxCFFSegments {
		return errCFFBudget
	}
	for _, seg := range sub.cs.Segments {
		for i := range segmentPoints(seg.Op) {
			seg.Args[i].X += dx
			seg.Args[i].Y += dy
		}
		h.cs.Segments = append(h.cs.Segments, seg)
	}
	return nil
}

// segmentPoints is how many of a segment's three points its operation uses.
func segmentPoints(op opentype.SegmentOp) int {
	switch op {
	case opentype.SegmentOpMoveTo, opentype.SegmentOpLineTo:
		return 1
	case opentype.SegmentOpQuadTo:
		return 2
	default:
		return 3
	}
}
