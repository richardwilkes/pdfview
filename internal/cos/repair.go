// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos

import (
	"bytes"
	"errors"
)

var errRepairFoundNothing = errors.New("repair scan found no objects or trailer")

// repair rebuilds the cross-reference data from scratch by sweeping the entire buffer for "N G obj" headers, trailer
// dictionaries, and object streams. It runs when the file's own cross-reference information is missing, unreadable, or
// inconsistent with the actual object layout (a load through it failed), the same recovery deployed readers perform.
// Later definitions of an object number override earlier ones, matching incremental-update semantics for files whose
// updates are intact but whose tables are broken.
func (d *Document) repair() error {
	d.repaired = true
	d.clearCaches()
	xref := make(map[int]xrefEntry)
	var trailers []Dict // Candidate trailers, in file order (later entries are newer).
	var objStmNums []int
	catalogNum := -1
	pos := 0
	for pos < len(d.data) {
		rel := bytes.Index(d.data[pos:], []byte("obj"))
		if rel < 0 {
			break
		}
		idx := pos + rel
		numStart, num, ok := headerBefore(d.data, idx)
		if !ok || !boundaryAfter(d.data, idx+3) {
			pos = idx + 3
			continue
		}
		obj, _, end, err := parseIndirectAt(d.data, int64(numStart), -1)
		if err != nil || end <= int64(idx) {
			pos = idx + 3
			continue
		}
		if num > 0 && num <= maxObjectNumber {
			xref[num] = xrefEntry{kind: xrefInFile, offset: int64(numStart)}
			switch v := obj.(type) {
			case Dict:
				if typ, _ := AsName(v["Type"]); typ == "Catalog" {
					catalogNum = num
				}
			case *Stream:
				switch typ, _ := AsName(v.Dict["Type"]); typ {
				case "ObjStm":
					objStmNums = append(objStmNums, num)
				case typeXRef:
					// A cross-reference stream's dictionary carries the document-level trailer keys.
					trailers = append(trailers, v.Dict)
				}
			}
		}
		pos = int(end)
	}
	trailers = append(trailers, d.scanTrailers()...)
	if len(xref) == 0 && len(trailers) == 0 {
		return errRepairFoundNothing
	}
	d.xref = xref
	d.installRepairedTrailer(trailers, catalogNum)
	// Register the contents of every object stream found by the sweep, without overriding directly swept definitions:
	// an object written directly in the file is at least as authoritative as a compressed copy.
	for _, stmNum := range objStmNums {
		stm, err := d.loadObjStm(stmNum)
		if err != nil {
			continue
		}
		for i, num := range stm.nums {
			if num <= 0 || num > maxObjectNumber {
				continue
			}
			if _, exists := d.xref[num]; !exists {
				d.xref[num] = xrefEntry{kind: xrefInStream, stmNum: stmNum, stmIdx: i}
			}
		}
	}
	return nil
}

// installRepairedTrailer rebuilds d.trailer from the recovered candidates. Newer candidates (later in the file) win;
// the pre-repair trailer, if any, is the final fallback. When no candidate names a /Root, the last swept /Type /Catalog
// object stands in.
func (d *Document) installRepairedTrailer(trailers []Dict, catalogNum int) {
	merged := make([]Dict, 0, len(trailers)+1)
	for i := len(trailers) - 1; i >= 0; i-- {
		merged = append(merged, trailers[i])
	}
	if d.trailer != nil {
		merged = append(merged, d.trailer)
	}
	d.trailer = mergeTrailers(merged)
	if _, ok := d.trailer["Root"]; !ok && catalogNum > 0 {
		d.trailer["Root"] = Ref{Num: catalogNum}
	}
}

// scanTrailers finds every parseable dictionary following a "trailer" keyword, in file order.
func (d *Document) scanTrailers() []Dict {
	var trailers []Dict
	pos := 0
	for pos < len(d.data) {
		rel := bytes.Index(d.data[pos:], []byte("trailer"))
		if rel < 0 {
			break
		}
		idx := pos + rel
		pos = idx + len("trailer")
		if (idx > 0 && isRegular(d.data[idx-1])) || !boundaryAfter(d.data, pos) {
			continue
		}
		p := newParser(d.data, pos)
		obj, err := p.parseObject()
		if err != nil {
			continue
		}
		if dict, ok := obj.(Dict); ok {
			trailers = append(trailers, dict)
		}
	}
	return trailers
}

// headerBefore backtracks from the "obj" keyword at idx over whitespace, a generation number, whitespace, and an object
// number, returning the object number and its start offset.
func headerBefore(data []byte, idx int) (numStart, num int, ok bool) {
	i := idx - 1
	i = skipSpaceBackward(data, i)
	genEnd := i
	for i >= 0 && data[i] >= '0' && data[i] <= '9' {
		i--
	}
	if i == genEnd { // No generation digits.
		return 0, 0, false
	}
	i = skipSpaceBackward(data, i)
	numEnd := i
	for i >= 0 && data[i] >= '0' && data[i] <= '9' {
		i--
	}
	if i == numEnd { // No object-number digits.
		return 0, 0, false
	}
	if i >= 0 && isRegular(data[i]) {
		return 0, 0, false // The number runs into other regular characters (e.g. "x12 0 obj").
	}
	numStart = i + 1
	if numEnd-numStart >= 9 { // Longer than maxObjectNumber's decimal representation; reject cheaply.
		return 0, 0, false
	}
	num = 0
	for j := numStart; j <= numEnd; j++ {
		num = num*10 + int(data[j]-'0')
	}
	return numStart, num, true
}

// skipSpaceBackward moves i backward past whitespace. It may return -1 when the start of the buffer is reached; the
// digit checks in headerBefore treat that as failure.
func skipSpaceBackward(data []byte, i int) int {
	for i >= 0 && isWhitespace(data[i]) {
		i--
	}
	return i
}

// boundaryAfter reports whether the byte at pos (if any) does not continue a regular token.
func boundaryAfter(data []byte, pos int) bool {
	return pos >= len(data) || !isRegular(data[pos])
}
