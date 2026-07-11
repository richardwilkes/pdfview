// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package function

import (
	"errors"
	"math"

	"github.com/richardwilkes/pdfview/internal/cos"
)

var errBadCalculator = errors.New("invalid calculator function")

// psOp enumerates the PostScript calculator operators (ISO 32000-2 7.10.5.2, Table 42).
type psOp uint8

const (
	opNum    psOp = iota // push instr.num
	opIf                 // pop bool; run thenProc when true
	opIfElse             // pop bool; run thenProc when true, elseProc otherwise
	opAbs
	opAdd
	opAtan
	opCeiling
	opCos
	opCvi
	opCvr
	opDiv
	opExp
	opFloor
	opIdiv
	opLn
	opLog
	opMod
	opMul
	opNeg
	opRound
	opSin
	opSqrt
	opSub
	opTruncate
	opAnd
	opBitshift
	opEq
	opFalse
	opGe
	opGt
	opLe
	opLt
	opNe
	opNot
	opOr
	opTrue
	opXor
	opCopy
	opDup
	opExch
	opIndex
	opPop
	opRoll
)

var psOps = map[string]psOp{
	"abs": opAbs, "add": opAdd, "atan": opAtan, "ceiling": opCeiling, "cos": opCos, "cvi": opCvi, "cvr": opCvr,
	"div": opDiv, "exp": opExp, "floor": opFloor, "idiv": opIdiv, "ln": opLn, "log": opLog, "mod": opMod,
	"mul": opMul, "neg": opNeg, "round": opRound, "sin": opSin, "sqrt": opSqrt, "sub": opSub,
	"truncate": opTruncate, "and": opAnd, "bitshift": opBitshift, "eq": opEq, "false": opFalse, "ge": opGe,
	"gt": opGt, "le": opLe, "lt": opLt, "ne": opNe, "not": opNot, "or": opOr, "true": opTrue, "xor": opXor,
	"copy": opCopy, "dup": opDup, "exch": opExch, "index": opIndex, "pop": opPop, "roll": opRoll,
}

// psInstr is one parsed calculator instruction. Procedures appear only as the bodies of if/ifelse, so the
// parser folds each `{...} if` / `{...} {...} ifelse` sequence into a single instruction carrying its bodies.
type psInstr struct {
	thenProc []psInstr
	elseProc []psInstr
	num      float64
	op       psOp
}

// calculator is a type 4 (PostScript calculator) function.
type calculator struct {
	common
	program []psInstr
	nOut    int
}

func parseCalculator(d *cos.Document, stream *cos.Stream, c common) (Func, error) {
	if len(c.rng) == 0 {
		return nil, errBadRange // Range is required for type 4.
	}
	data, err := d.StreamData(stream)
	if err != nil {
		return nil, err
	}
	lex := cos.NewLexer(data, 0)
	// The program is one procedure: skip to its opening brace.
	tok, ok := lex.Next()
	if !ok || tok.Kind != cos.TokenBraceOpen {
		return nil, errBadCalculator
	}
	budget := maxProgramOps
	program, err := parseProc(lex, 0, &budget)
	if err != nil {
		return nil, err
	}
	return &calculator{common: c, program: program, nOut: len(c.rng) / 2}, nil
}

// parseProc parses instructions until the procedure's closing brace. A nested procedure is legal only as the
// body of an if/ifelse that immediately follows it; anything else is an error, as the standard requires.
func parseProc(lex *cos.Lexer, depth int, budget *int) ([]psInstr, error) {
	if depth > maxNesting {
		return nil, errTooDeep
	}
	var out []psInstr
	var procs [][]psInstr // pending {...} bodies awaiting if/ifelse
	for {
		*budget--
		if *budget < 0 {
			return nil, errBadCalculator
		}
		tok, ok := lex.Next()
		if !ok {
			return nil, errBadCalculator
		}
		switch tok.Kind {
		case cos.TokenBraceClose, cos.TokenEOF:
			if tok.Kind == cos.TokenEOF || len(procs) != 0 {
				return nil, errBadCalculator
			}
			return out, nil
		case cos.TokenInt:
			if len(procs) != 0 {
				return nil, errBadCalculator
			}
			out = append(out, psInstr{op: opNum, num: float64(tok.Int)})
		case cos.TokenReal:
			if len(procs) != 0 {
				return nil, errBadCalculator
			}
			out = append(out, psInstr{op: opNum, num: tok.Real})
		case cos.TokenBraceOpen:
			proc, err := parseProc(lex, depth+1, budget)
			if err != nil {
				return nil, err
			}
			if procs = append(procs, proc); len(procs) > 2 {
				return nil, errBadCalculator
			}
		case cos.TokenKeyword:
			word := string(tok.Bytes)
			switch {
			case word == "if" && len(procs) == 1:
				out = append(out, psInstr{op: opIf, thenProc: procs[0]})
				procs = nil
			case word == "ifelse" && len(procs) == 2:
				out = append(out, psInstr{op: opIfElse, thenProc: procs[0], elseProc: procs[1]})
				procs = nil
			default:
				op, known := psOps[word]
				if !known || len(procs) != 0 {
					return nil, errBadCalculator
				}
				out = append(out, psInstr{op: op})
			}
		default:
			return nil, errBadCalculator
		}
	}
}

func (c *calculator) NOutputs() int {
	return c.nOut
}

// psStack is the calculator's operand stack. Values are float64 with a parallel bool flag; the boolean
// operators are logical on booleans and bitwise on numbers (treated as 32-bit integers), matching the
// standard's typed semantics closely enough for real-world transfer and tint functions.
type psStack struct {
	vals   [psStackLimit]float64
	n      int
	bools  [psStackLimit]bool
	failed bool
}

func (s *psStack) push(v float64) {
	if s.n >= psStackLimit {
		s.failed = true
		return
	}
	s.vals[s.n] = v
	s.bools[s.n] = false
	s.n++
}

func (s *psStack) pushBool(b bool) {
	if s.n >= psStackLimit {
		s.failed = true
		return
	}
	s.vals[s.n] = 0
	if b {
		s.vals[s.n] = 1
	}
	s.bools[s.n] = true
	s.n++
}

func (s *psStack) pop() float64 {
	if s.n == 0 {
		s.failed = true
		return 0
	}
	s.n--
	return s.vals[s.n]
}

func (s *psStack) popBool() bool {
	return s.pop() != 0
}

func (c *calculator) Eval(in []float32) []float32 {
	x := c.clampIn(in)
	var stack psStack
	for _, v := range x {
		stack.push(float64(v))
	}
	steps := maxExecSteps
	execPS(c.program, &stack, &steps)
	// Outputs are the top n stack values, in stack order (the last output on top).
	out := make([]float32, c.nOut)
	for j := c.nOut - 1; j >= 0; j-- {
		out[j] = float32(stack.pop())
	}
	return c.clampOut(out)
}

//nolint:gocyclo // A flat operator dispatch switch; splitting it would only obscure the correspondence to Table 42.
func execPS(program []psInstr, s *psStack, steps *int) {
	for i := range program {
		*steps--
		if *steps < 0 || s.failed {
			return
		}
		instr := &program[i]
		switch instr.op {
		case opNum:
			s.push(instr.num)
		case opIf:
			if s.popBool() {
				execPS(instr.thenProc, s, steps)
			}
		case opIfElse:
			if s.popBool() {
				execPS(instr.thenProc, s, steps)
			} else {
				execPS(instr.elseProc, s, steps)
			}
		case opAbs:
			s.push(math.Abs(s.pop()))
		case opAdd:
			b, a := s.pop(), s.pop()
			s.push(a + b)
		case opAtan:
			den, num := s.pop(), s.pop()
			deg := math.Atan2(num, den) * 180 / math.Pi
			if deg < 0 {
				deg += 360
			}
			s.push(deg)
		case opCeiling:
			s.push(math.Ceil(s.pop()))
		case opCos:
			s.push(math.Cos(s.pop() * math.Pi / 180))
		case opCvi, opTruncate:
			s.push(math.Trunc(s.pop()))
		case opCvr:
			s.push(s.pop())
		case opDiv:
			b, a := s.pop(), s.pop()
			s.push(a / b) // Division by zero yields ±Inf/NaN; the range clamp maps those to bounds.
		case opExp:
			b, a := s.pop(), s.pop()
			s.push(math.Pow(a, b))
		case opFloor:
			s.push(math.Floor(s.pop()))
		case opIdiv:
			b, a := int32(s.pop()), int32(s.pop())
			if b == 0 {
				s.failed = true
				return
			}
			s.push(float64(a / b))
		case opLn:
			s.push(math.Log(s.pop()))
		case opLog:
			s.push(math.Log10(s.pop()))
		case opMod:
			b, a := int32(s.pop()), int32(s.pop())
			if b == 0 {
				s.failed = true
				return
			}
			s.push(float64(a % b))
		case opMul:
			b, a := s.pop(), s.pop()
			s.push(a * b)
		case opNeg:
			s.push(-s.pop())
		case opRound:
			s.push(math.Round(s.pop()))
		case opSin:
			s.push(math.Sin(s.pop() * math.Pi / 180))
		case opSqrt:
			s.push(math.Sqrt(s.pop()))
		case opSub:
			b, a := s.pop(), s.pop()
			s.push(a - b)
		case opAnd, opOr, opXor:
			execPSBits(instr.op, s)
		case opBitshift:
			shift, a := int32(s.pop()), int32(s.pop())
			switch {
			case shift >= 32 || shift <= -32:
				s.push(0)
			case shift >= 0:
				s.push(float64(a << shift))
			default:
				s.push(float64(a >> -shift))
			}
		case opEq:
			s.pushBool(s.pop() == s.pop())
		case opNe:
			s.pushBool(s.pop() != s.pop())
		case opFalse:
			s.pushBool(false)
		case opTrue:
			s.pushBool(true)
		case opGe:
			b, a := s.pop(), s.pop()
			s.pushBool(a >= b)
		case opGt:
			b, a := s.pop(), s.pop()
			s.pushBool(a > b)
		case opLe:
			b, a := s.pop(), s.pop()
			s.pushBool(a <= b)
		case opLt:
			b, a := s.pop(), s.pop()
			s.pushBool(a < b)
		case opNot:
			if s.n > 0 && s.bools[s.n-1] {
				s.pushBool(!s.popBool())
			} else {
				s.push(float64(^int32(s.pop())))
			}
		case opCopy:
			n := int(s.pop())
			if n < 0 || n > s.n || s.n+n > psStackLimit {
				s.failed = true
				return
			}
			base := s.n - n
			for j := range n {
				s.vals[s.n+j] = s.vals[base+j]
				s.bools[s.n+j] = s.bools[base+j]
			}
			s.n += n
		case opDup:
			if s.n == 0 || s.n >= psStackLimit {
				s.failed = true
				return
			}
			s.vals[s.n] = s.vals[s.n-1]
			s.bools[s.n] = s.bools[s.n-1]
			s.n++
		case opExch:
			if s.n < 2 {
				s.failed = true
				return
			}
			s.vals[s.n-1], s.vals[s.n-2] = s.vals[s.n-2], s.vals[s.n-1]
			s.bools[s.n-1], s.bools[s.n-2] = s.bools[s.n-2], s.bools[s.n-1]
		case opIndex:
			n := int(s.pop())
			if n < 0 || n >= s.n {
				s.failed = true
				return
			}
			v, b := s.vals[s.n-1-n], s.bools[s.n-1-n]
			if b {
				s.pushBool(v != 0)
			} else {
				s.push(v)
			}
		case opPop:
			s.pop()
		case opRoll:
			j, n := int(s.pop()), int(s.pop())
			if n < 0 || n > s.n {
				s.failed = true
				return
			}
			if n > 0 && j != 0 {
				rollSlice(s.vals[s.n-n:s.n], j)
				rollBools(s.bools[s.n-n:s.n], j)
			}
		}
	}
}

// execPSBits implements and/or/xor: logical when both operands are booleans, bitwise on 32-bit integers
// otherwise.
func execPSBits(op psOp, s *psStack) {
	bothBool := s.n >= 2 && s.bools[s.n-1] && s.bools[s.n-2]
	b, a := s.pop(), s.pop()
	if bothBool {
		ab, bb := a != 0, b != 0
		switch op {
		case opAnd:
			s.pushBool(ab && bb)
		case opOr:
			s.pushBool(ab || bb)
		default:
			s.pushBool(ab != bb)
		}
		return
	}
	ai, bi := int32(a), int32(b)
	switch op {
	case opAnd:
		s.push(float64(ai & bi))
	case opOr:
		s.push(float64(ai | bi))
	default:
		s.push(float64(ai ^ bi))
	}
}

// rollSlice rotates v by j positions toward the top of the stack (positive j moves elements up), PostScript
// roll semantics.
func rollSlice(v []float64, j int) {
	n := len(v)
	j = ((j % n) + n) % n
	if j == 0 {
		return
	}
	tmp := make([]float64, n)
	for i := range v {
		tmp[(i+j)%n] = v[i]
	}
	copy(v, tmp)
}

func rollBools(v []bool, j int) {
	n := len(v)
	j = ((j % n) + n) % n
	if j == 0 {
		return
	}
	tmp := make([]bool, n)
	for i := range v {
		tmp[(i+j)%n] = v[i]
	}
	copy(v, tmp)
}
