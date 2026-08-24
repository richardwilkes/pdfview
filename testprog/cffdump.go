// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package main

import "fmt"

// dumpCFFPrivate walks a bare CFF program far enough to print the Top DICT and Private DICT operators, so the operator
// a stricter parser rejects can be identified. It is a minimal TN5176 reader that ignores everything else.
func dumpCFFPrivate(raw []byte) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("  dump panic:", r)
		}
	}()
	hdrSize := int(raw[2])
	pos := skipIndex(raw, hdrSize)     // Name INDEX
	topDicts, _ := readIndex(raw, pos) // Top DICT INDEX
	if len(topDicts) == 0 {
		fmt.Println("  no top dict")
		return
	}
	var privSize, privOff int
	fmt.Print("  top ops:")
	walkDict(topDicts[0], func(op int, operands []float64) {
		fmt.Printf(" %s", opName(op))
		if op == 18 && len(operands) == 2 { // Private [size offset]
			privSize, privOff = int(operands[0]), int(operands[1])
		}
	})
	fmt.Println()
	if privOff <= 0 || privSize <= 0 || privOff+privSize > len(raw) {
		fmt.Println("  no private dict")
		return
	}
	fmt.Print("  private ops:")
	walkDict(raw[privOff:privOff+privSize], func(op int, operands []float64) {
		fmt.Printf(" %s(%d args)", opName(op), len(operands))
	})
	fmt.Println()
}

// sanitizeCFFPrivate returns a copy of raw with the deprecated Private DICT operators ForceBoldThreshold (12 15) and
// lenIV (12 16) rewritten to the accepted, same-arity initialRandomSeed (12 19), plus whether anything changed.
func sanitizeCFFPrivate(raw []byte) (out []byte, changed bool) {
	defer func() {
		if recover() != nil {
			out, changed = raw, false
		}
	}()
	out = append([]byte(nil), raw...)
	hdrSize := int(out[2])
	pos := skipIndex(out, hdrSize)     // Name INDEX
	topDicts, _ := readIndex(out, pos) // Top DICT INDEX
	if len(topDicts) == 0 {
		return raw, false
	}
	var privSize, privOff int
	walkDict(topDicts[0], func(op int, operands []float64) {
		if op == 18 && len(operands) == 2 {
			privSize, privOff = int(operands[0]), int(operands[1])
		}
	})
	if privOff <= 0 || privSize <= 0 || privOff+privSize > len(out) {
		return raw, false
	}
	changed = sanitizeDictBytes(out[privOff : privOff+privSize])
	if !changed {
		return raw, false
	}
	return out, true
}

// sanitizeDictBytes rewrites deprecated escaped operators in place, reporting whether anything changed. It walks the
// DICT so operator bytes are never confused with operand bytes.
func sanitizeDictBytes(dict []byte) bool {
	changed := false
	for i := 0; i < len(dict); {
		b := int(dict[i])
		switch {
		case b <= 21:
			if b == 12 && i+1 < len(dict) {
				if dict[i+1] == 15 || dict[i+1] == 16 { // ForceBoldThreshold / lenIV
					dict[i+1] = 19 // initialRandomSeed: accepted, same arity, value ignored
					changed = true
				}
				i += 2
			} else {
				i++
			}
		case b == 28:
			i += 3
		case b == 29:
			i += 5
		case b == 30:
			i++
			for i < len(dict) {
				n := dict[i]
				i++
				if n&0x0f == 0x0f || n&0xf0 == 0xf0 {
					break
				}
			}
		case b >= 32 && b <= 246:
			i++
		case b >= 247 && b <= 254:
			i += 2
		default:
			i++
		}
	}
	return changed
}

func opName(op int) string {
	names := map[int]string{
		0: "version", 1: "Notice", 2: "FullName", 3: "FamilyName", 4: "Weight", 5: "FontBBox",
		6: "BlueValues", 7: "OtherBlues", 8: "FamilyBlues", 9: "FamilyOtherBlues",
		10: "StdHW", 11: "StdVW", 13: "UniqueID", 14: "XUID", 15: "charset", 16: "Encoding",
		17: "CharStrings", 18: "Private", 19: "Subrs", 20: "defaultWidthX", 21: "nominalWidthX",
		0xc00: "Copyright", 0xc01: "isFixedPitch", 0xc02: "ItalicAngle", 0xc03: "UnderlinePosition",
		0xc04: "UnderlineThickness", 0xc05: "PaintType", 0xc06: "CharstringType", 0xc07: "FontMatrix",
		0xc08: "StrokeWidth", 0xc09: "BlueScale", 0xc0a: "BlueShift", 0xc0b: "BlueFuzz",
		0xc0c: "StemSnapH", 0xc0d: "StemSnapV", 0xc0e: "ForceBold", 0xc0f: "ForceBoldThreshold",
		0xc10: "lenIV", 0xc11: "LanguageGroup", 0xc12: "ExpansionFactor", 0xc13: "initialRandomSeed",
		0xc14: "SyntheticBase", 0xc15: "PostScript", 0xc16: "BaseFontName", 0xc17: "BaseFontBlend",
		0xc1e: "ROS", 0xc1f: "CIDFontVersion", 0xc24: "FDArray", 0xc25: "FDSelect",
	}
	if n, ok := names[op]; ok {
		return n
	}
	if op >= 0xc00 {
		return fmt.Sprintf("esc%d", op-0xc00)
	}
	return fmt.Sprintf("op%d", op)
}

func readIndex(raw []byte, pos int) (entries [][]byte, next int) {
	count := int(raw[pos])<<8 | int(raw[pos+1])
	if count == 0 {
		return nil, pos + 2
	}
	offSize := int(raw[pos+2])
	offAt := func(i int) int {
		v := 0
		base := pos + 3 + i*offSize
		for j := range offSize {
			v = v<<8 | int(raw[base+j])
		}
		return v
	}
	dataStart := pos + 3 + (count+1)*offSize - 1
	for i := range count {
		entries = append(entries, raw[dataStart+offAt(i):dataStart+offAt(i+1)])
	}
	return entries, dataStart + offAt(count)
}

func skipIndex(raw []byte, pos int) int {
	_, next := readIndex(raw, pos)
	return next
}

func walkDict(dict []byte, fn func(op int, operands []float64)) {
	var operands []float64
	for i := 0; i < len(dict); {
		b := int(dict[i])
		switch {
		case b <= 21: // operator
			op := b
			i++
			if b == 12 {
				op = 0xc00 + int(dict[i])
				i++
			}
			fn(op, operands)
			operands = operands[:0]
		case b == 28:
			operands = append(operands, float64(int16(uint16(dict[i+1])<<8|uint16(dict[i+2]))))
			i += 3
		case b == 29:
			operands = append(operands, float64(int32(uint32(dict[i+1])<<24|uint32(dict[i+2])<<16|
				uint32(dict[i+3])<<8|uint32(dict[i+4]))))
			i += 5
		case b == 30: // real; skip nibbles to terminator
			operands = append(operands, 0)
			i++
			for i < len(dict) {
				n := dict[i]
				i++
				if n&0x0f == 0x0f || n&0xf0 == 0xf0 {
					break
				}
			}
		case b >= 32 && b <= 246:
			operands = append(operands, float64(b-139))
			i++
		case b >= 247 && b <= 250:
			operands = append(operands, float64((b-247)*256+int(dict[i+1])+108))
			i += 2
		case b >= 251 && b <= 254:
			operands = append(operands, -float64((b-251)*256+int(dict[i+1])+108))
			i += 2
		default:
			fn(-b, operands) // reserved byte; report as negative pseudo-op
			operands = operands[:0]
			i++
		}
	}
}
