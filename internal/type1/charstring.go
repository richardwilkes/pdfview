package type1

import (
	psi "github.com/go-text/typesetting/font/cff/interpreter"
	ot "github.com/go-text/typesetting/font/opentype"
)

// Charstring execution: go-text's psinterpreter supplies the Type1Charstring machine (number encodings,
// argument stack, subroutine calls without bias) and the shared CharstringReader geometry helpers; this file
// contributes the Type 1 operator handler — hsbw/sbw, seac, the flex and hint-replacement othersubr protocol,
// and div/pop — per the Adobe Type 1 Font Format specification chapter 6-8.

// Caps against hostile charstrings.
const (
	maxPSStack    = 64 // The othersubr communication stack (real fonts use at most 2 slots).
	maxFlexPoints = 64 // Collected flex reference points (the protocol produces exactly 7).
	// maxHandlerOps bounds one glyph's executed operators: subroutines can be re-called O(charstring) times,
	// so without a counter a hostile program amplifies its input length quadratically. Real glyphs run a few
	// hundred operators.
	maxHandlerOps = 1 << 18
	// maxSegments bounds one glyph's emitted outline segments (real glyphs have well under a thousand).
	maxSegments = 1 << 14
)

// Glyph interprets the named glyph's charstring, returning its outline segments (in glyph units, y up — the
// caller maps them through FontMatrix) and its advance width from hsbw/sbw. Unknown names and execution
// failures return ErrBadCharstring; hostile programs cannot panic (the machine and handler are bounds-checked
// throughout, and a recover guard backstops them).
func (f *Font) Glyph(name string) (segs []ot.Segment, advance float32, err error) {
	defer func() {
		if recover() != nil {
			segs, advance, err = nil, 0, ErrBadCharstring
		}
	}()
	h, err := f.run(name, false)
	if err != nil {
		return nil, 0, err
	}
	return h.reader.Segments, float32(h.advance), nil
}

// Advance interprets only up to the width operator (hsbw/sbw arrives first in every valid charstring),
// returning the glyph's advance in glyph units.
func (f *Font) Advance(name string) (adv float32, ok bool) {
	defer func() {
		if recover() != nil { // A hostile program degrades to no advance.
			adv, ok = 0, false
		}
	}()
	h, err := f.run(name, true)
	if err != nil {
		return 0, false
	}
	return float32(h.advance), true
}

// run executes one glyph's charstring through a fresh machine and handler.
func (f *Font) run(name string, widthOnly bool) (*handler, error) {
	cs, ok := f.CharStrings[name]
	if !ok {
		return nil, ErrBadCharstring
	}
	h := &handler{font: f, widthOnly: widthOnly}
	var m psi.Machine
	if err := m.Run(cs, f.Subrs, nil, h); err != nil {
		return nil, ErrBadCharstring
	}
	h.finish()
	return h, nil
}

// handler implements psi.OperatorHandler for Type 1 charstrings.
//
// Path-state notes: CharstringReader implements Type 2 semantics, where contours close only implicitly (on
// the next moveto or at the end). Type 1 adds the explicit closepath operator — which per its spec does NOT
// reposition the current point — so the handler tracks contour state itself (contourStart, justClosed,
// needClose) to suppress the duplicate closing segments the reader would otherwise emit, and folds the
// hsbw/sbw sidebearing origin into the first path operation instead of faking a move.
type handler struct {
	font *Font
	// psStack is the PostScript communication stack of the othersubr protocol: callothersubr pushes results
	// (or unhandled arguments), pop retrieves them.
	psStack []float64
	// flexPoints collects the reference points delivered through rmoveto while a flex sequence is active.
	flexPoints []psi.Point
	reader     psi.CharstringReader
	// flexCur tracks the moving point during flex (reader.CurrentPoint intentionally stays at the flex start
	// so the two curves emit with correct relative geometry).
	flexCur psi.Point
	// contourStart is where the current contour began (the last moveto's target).
	contourStart psi.Point
	// transX/transY translate the glyph being interpreted: zero normally, (adx-asb, ady) for the accent
	// component of a seac composite. hsbw/sbw apply it to their sidebearing point.
	transX, transY float64
	// pendingX/pendingY hold the sidebearing origin set by hsbw/sbw until the first path operation uses it.
	pendingX, pendingY float64
	// advance is the glyph's width from hsbw/sbw (the seac component runs never override the composite's).
	advance float64
	// ops counts executed operators across the glyph (including seac components) against maxHandlerOps.
	ops        int
	hasPending bool
	// justClosed records an explicit closepath, so the next moveto suppresses the reader's automatic close.
	justClosed bool
	// needClose records drawing since the last close; finish() closes only then.
	needClose bool
	inFlex    bool
	inSeac    bool
	widthOnly bool
}

// finish closes the final contour when it was left open.
func (h *handler) finish() {
	if h.needClose {
		h.reader.ClosePath()
		h.needClose = false
		h.justClosed = true
	}
}

// moveTo starts a new contour displaced (dx, dy) from the current point, folding in a pending sidebearing
// origin and suppressing the reader's automatic close when closepath already closed the contour.
func (h *handler) moveTo(state *psi.Machine, dx, dy float64) error {
	if h.hasPending {
		// The first path operation of this charstring run: the displacement is from the sidebearing origin,
		// an absolute position (the reader's current point is elsewhere when a seac component follows the
		// base glyph).
		target := psi.Point{X: h.pendingX + dx, Y: h.pendingY + dy}
		dx = target.X - h.reader.CurrentPoint.X
		dy = target.Y - h.reader.CurrentPoint.Y
		h.hasPending = false
	}
	if h.justClosed {
		// The reader's move() would append a second closing segment (its current point is off the contour
		// start after an explicit closepath); align them while preserving the absolute target.
		target := psi.Point{X: h.reader.CurrentPoint.X + dx, Y: h.reader.CurrentPoint.Y + dy}
		h.reader.CurrentPoint = h.contourStart
		dx, dy = target.X-h.contourStart.X, target.Y-h.contourStart.Y
		h.justClosed = false
	}
	state.ArgStack.Clear()
	state.ArgStack.Vals[0], state.ArgStack.Vals[1] = dx, dy
	state.ArgStack.Top = 2
	if err := h.reader.Rmoveto(state); err != nil {
		return err
	}
	h.contourStart = h.reader.CurrentPoint
	h.needClose = false
	return nil
}

// beforeDraw prepares path state for a line/curve operator.
func (h *handler) beforeDraw() {
	if h.hasPending { // Degenerate: drawing with no moveto starts at the sidebearing point.
		h.reader.CurrentPoint = psi.Point{X: h.pendingX, Y: h.pendingY}
		h.contourStart = h.reader.CurrentPoint
		h.hasPending = false
	}
	h.justClosed = false
	h.needClose = true
}

// Context implements psi.OperatorHandler.
func (h *handler) Context() psi.Context { return psi.Type1Charstring }

// Type 1 charstring operator codes (Adobe Type 1 Font Format, chapter 6).
const (
	opHstem        = 1
	opVstem        = 3
	opVmoveto      = 4
	opRlineto      = 5
	opHlineto      = 6
	opVlineto      = 7
	opRrcurveto    = 8
	opClosepath    = 9
	opCallsubr     = 10
	opReturn       = 11
	opHsbw         = 13
	opEndchar      = 14
	opRmoveto      = 21
	opHmoveto      = 22
	opVhcurveto    = 30
	opHvcurveto    = 31
	opEscDotsect   = 0
	opEscVstem3    = 1
	opEscHstem3    = 2
	opEscSeac      = 6
	opEscSbw       = 7
	opEscDiv       = 12
	opEscOthersubr = 16
	opEscPop       = 17
	opEscSetCurPt  = 33
)

// Apply implements psi.OperatorHandler. Type 1 semantics clear the argument stack after every operator except
// the stack-manipulating ones (callsubr/return/div/pop and the othersubr protocol).
func (h *handler) Apply(state *psi.Machine, op psi.Operator) error {
	h.ops++
	if h.ops > maxHandlerOps || len(h.reader.Segments) > maxSegments {
		return ErrBadCharstring // Hostile amplification (subr re-calls, unbounded geometry): stop the glyph.
	}
	if h.widthOnly && h.ops > 64 {
		return psi.ErrInterrupt // hsbw/sbw arrives among the first operators of any valid charstring.
	}
	if op.IsEscaped {
		return h.applyEscaped(state, op.Operator)
	}
	switch op.Operator {
	case opHstem:
		h.reader.Hstem(state)
	case opVstem:
		h.reader.Vstem(state)
	case opVmoveto:
		if h.inFlex {
			return h.flexMove(state, opVmoveto)
		}
		if state.ArgStack.Top < 1 {
			return ErrBadCharstring
		}
		return h.moveTo(state, 0, state.ArgStack.Vals[0])
	case opRlineto:
		h.beforeDraw()
		h.reader.Rlineto(state)
	case opHlineto:
		h.beforeDraw()
		h.reader.Hlineto(state) // With Type 1's single argument this draws exactly one horizontal segment.
	case opVlineto:
		h.beforeDraw()
		h.reader.Vlineto(state)
	case opRrcurveto:
		h.beforeDraw()
		h.reader.Rrcurveto(state)
	case opClosepath:
		h.reader.ClosePath()
		h.needClose = false
		h.justClosed = true
	case opCallsubr:
		return psi.LocalSubr(state) // No stack clear: the subroutine consumes the remaining arguments.
	case opReturn:
		return state.Return()
	case opHsbw:
		if state.ArgStack.Top < 2 {
			return ErrBadCharstring
		}
		return h.setSidebearing(state, state.ArgStack.Vals[0], 0, state.ArgStack.Vals[1])
	case opEndchar:
		h.finish()
		return psi.ErrInterrupt
	case opRmoveto:
		if h.inFlex {
			return h.flexMove(state, opRmoveto)
		}
		if state.ArgStack.Top < 2 {
			return ErrBadCharstring
		}
		return h.moveTo(state, state.ArgStack.Vals[0], state.ArgStack.Vals[1])
	case opHmoveto:
		if h.inFlex {
			return h.flexMove(state, opHmoveto)
		}
		if state.ArgStack.Top < 1 {
			return ErrBadCharstring
		}
		return h.moveTo(state, state.ArgStack.Vals[0], 0)
	case opVhcurveto:
		h.beforeDraw()
		h.reader.Vhcurveto(state)
	case opHvcurveto:
		h.beforeDraw()
		h.reader.Hvcurveto(state)
	default:
		// Unknown operators are hostile or generator quirks; skipping them (with a stack reset below) is the
		// deployed-viewer behavior.
	}
	state.ArgStack.Clear()
	return nil
}

// applyEscaped handles the two-byte (12 x) operators.
func (h *handler) applyEscaped(state *psi.Machine, op byte) error {
	switch op {
	case opEscDotsect, opEscVstem3, opEscHstem3:
		// Hints carry no geometry.
	case opEscSeac:
		return h.seac(state)
	case opEscSbw:
		if state.ArgStack.Top < 4 {
			return ErrBadCharstring
		}
		return h.setSidebearing(state, state.ArgStack.Vals[0], state.ArgStack.Vals[1], state.ArgStack.Vals[2])
	case opEscDiv:
		if state.ArgStack.Top < 2 {
			return ErrBadCharstring
		}
		b := state.ArgStack.Pop()
		a := state.ArgStack.Pop()
		if b == 0 {
			return ErrBadCharstring
		}
		state.ArgStack.Vals[state.ArgStack.Top] = a / b
		state.ArgStack.Top++
		return nil // div leaves its result for the consuming operator.
	case opEscOthersubr:
		return h.callOtherSubr(state)
	case opEscPop:
		v := 0.0
		if n := len(h.psStack); n > 0 {
			v = h.psStack[n-1]
			h.psStack = h.psStack[:n-1]
		}
		if state.ArgStack.Top >= int32(len(state.ArgStack.Vals)) {
			return ErrBadCharstring
		}
		state.ArgStack.Vals[state.ArgStack.Top] = v
		state.ArgStack.Top++
		return nil
	case opEscSetCurPt:
		// Only ever meaningful straight after a flex end, where the current point is already correct; deployed
		// interpreters ignore its operands (FreeType-compatible).
	default:
	}
	state.ArgStack.Clear()
	return nil
}

// setSidebearing implements hsbw/sbw: record the advance (unless running a seac component, whose widths are
// the composite's concern) and stage the (possibly seac-translated) sidebearing point as the origin of the
// first path operation.
func (h *handler) setSidebearing(state *psi.Machine, sbx, sby, wx float64) error {
	if !h.inSeac {
		h.advance = wx
	}
	h.pendingX, h.pendingY = h.transX+sbx, h.transY+sby
	h.hasPending = true
	state.ArgStack.Clear()
	if h.widthOnly && !h.inSeac {
		return psi.ErrInterrupt
	}
	return nil
}

// flexMove collects one flex reference point delivered via a moveto while flex is active. op selects the
// operand layout (rmoveto: dx dy; hmoveto: dx; vmoveto: dy).
func (h *handler) flexMove(state *psi.Machine, op byte) error {
	var dx, dy float64
	switch op {
	case opRmoveto:
		if state.ArgStack.Top < 2 {
			return ErrBadCharstring
		}
		dx, dy = state.ArgStack.Vals[0], state.ArgStack.Vals[1]
	case opHmoveto:
		if state.ArgStack.Top < 1 {
			return ErrBadCharstring
		}
		dx = state.ArgStack.Vals[0]
	default: // vmoveto
		if state.ArgStack.Top < 1 {
			return ErrBadCharstring
		}
		dy = state.ArgStack.Vals[0]
	}
	h.flexCur.Move(dx, dy)
	if len(h.flexPoints) >= maxFlexPoints {
		return ErrBadCharstring
	}
	h.flexPoints = append(h.flexPoints, h.flexCur)
	state.ArgStack.Clear()
	return nil
}

// callOtherSubr implements the othersubr protocol (spec chapter 8): flex (0-2), hint replacement (3), and the
// push-through convention for anything unknown.
func (h *handler) callOtherSubr(state *psi.Machine) error {
	if state.ArgStack.Top < 2 {
		return ErrBadCharstring
	}
	which := int(state.ArgStack.Pop())
	n := int32(state.ArgStack.Pop())
	if n < 0 || n > state.ArgStack.Top {
		return ErrBadCharstring
	}
	args := state.ArgStack.Vals[state.ArgStack.Top-n : state.ArgStack.Top] // Bottom-first order.
	if err := state.ArgStack.PopN(n); err != nil {
		return err
	}
	switch which {
	case 0: // Flex end: args are (flex height, endX, endY); the collected points carry the geometry.
		return h.flexEnd(args)
	case 1: // Flex begin: the following movetos collect reference points instead of moving.
		h.inFlex = true
		h.flexPoints = h.flexPoints[:0]
		h.flexCur = h.reader.CurrentPoint
	case 2: // Flex collect: the work happened in the preceding rmoveto.
	case 3: // Hint replacement: push the subr number back for the following pop; the subr only re-declares hints.
		h.pushPS(args)
	default: // Unknown othersubrs: arguments pass through to the PostScript stack for later pops.
		h.pushPS(args)
	}
	state.ArgStack.Clear()
	return nil
}

// pushPS pushes args so that subsequent pops retrieve them first-argument-first.
func (h *handler) pushPS(args []float64) {
	for i := len(args) - 1; i >= 0; i-- {
		if len(h.psStack) >= maxPSStack {
			return
		}
		h.psStack = append(h.psStack, args[i])
	}
}

// flexEnd assembles the two collected flex curves. The protocol collects exactly 7 points: the reference point
// (discarded — it exists for the hint mechanism) and the 6 control/end points of the two Béziers. The end
// coordinates duplicated in args feed the two pops that follow.
func (h *handler) flexEnd(args []float64) error {
	h.inFlex = false
	if len(h.flexPoints) < 7 {
		return ErrBadCharstring
	}
	h.beforeDraw()
	p := h.flexPoints[len(h.flexPoints)-7:]
	cur := h.reader.CurrentPoint // Unmoved since flex begin: the curve start.
	h.reader.RelativeCurveTo(
		psi.Point{X: p[1].X - cur.X, Y: p[1].Y - cur.Y},
		psi.Point{X: p[2].X - p[1].X, Y: p[2].Y - p[1].Y},
		psi.Point{X: p[3].X - p[2].X, Y: p[3].Y - p[2].Y})
	h.reader.RelativeCurveTo(
		psi.Point{X: p[4].X - p[3].X, Y: p[4].Y - p[3].Y},
		psi.Point{X: p[5].X - p[4].X, Y: p[5].Y - p[4].Y},
		psi.Point{X: p[6].X - p[5].X, Y: p[6].Y - p[5].Y})
	if len(args) >= 3 { // Feed endX, endY to the two following pops (endX pops first).
		h.pushPS(args[1:3])
	}
	return nil
}

// seac draws a composite accented character (spec 8.6.1): the base and accent glyphs are looked up by
// StandardEncoding code and interpreted into the same reader, the accent translated so its sidebearing point
// lands at (adx, ady) relative to the composite's origin (its own hsbw contributes its sbx, which the spec
// requires to equal asb). seac terminates the charstring like endchar.
func (h *handler) seac(state *psi.Machine) error {
	if h.inSeac { // The spec forbids nested composites; hostile programs try.
		return ErrBadCharstring
	}
	if state.ArgStack.Top < 5 {
		return ErrBadCharstring
	}
	asb := state.ArgStack.Vals[0]
	adx := state.ArgStack.Vals[1]
	ady := state.ArgStack.Vals[2]
	bchar := int(state.ArgStack.Vals[3])
	achar := int(state.ArgStack.Vals[4])
	state.ArgStack.Clear()
	if h.font.StdEnc == nil || bchar < 0 || bchar > 255 || achar < 0 || achar > 255 {
		return psi.ErrInterrupt // No encoding table: degrade to whatever was drawn (nothing, per the spec's layout).
	}
	if err := h.runComponent(h.font.StdEnc[bchar], 0, 0); err != nil {
		return err
	}
	if err := h.runComponent(h.font.StdEnc[achar], adx-asb, ady); err != nil {
		return err
	}
	return psi.ErrInterrupt
}

// runComponent interprets one seac component charstring into the shared reader under a translation.
func (h *handler) runComponent(name string, dx, dy float64) error {
	cs, ok := h.font.CharStrings[name]
	if !ok {
		return ErrBadCharstring
	}
	saveTransX, saveTransY := h.transX, h.transY
	h.transX, h.transY = dx, dy
	h.inSeac = true
	var m psi.Machine
	err := m.Run(cs, h.font.Subrs, nil, h)
	h.finish()
	h.inSeac = false
	h.transX, h.transY = saveTransX, saveTransY
	return err
}
