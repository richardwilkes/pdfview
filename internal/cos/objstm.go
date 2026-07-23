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
	"errors"
	"fmt"
)

// objStm is a parsed object stream (/Type /ObjStm): the decoded payload plus the header's object-number/offset pairs.
type objStm struct {
	data  []byte
	nums  []int
	offs  []int
	first int
}

var (
	errObjStmEntry = errors.New("object not found in object stream")
	errObjStmSelf  = errors.New("object stream is not stored directly in the file")
	errObjStmCycle = errors.New("object stream refers to itself")
	errObjStmDepth = errors.New("object stream nesting too deep")
)

// maxObjStmDepth caps how many object streams may be under load at once. The objStmLoading guard stops a single stream
// from re-entering itself, but a straight chain of distinct object streams — each one's header keys (/N, /First,
// /Filter, /DecodeParms) resolving into the next via back-pointing xref entries — recurses through loadObjStm with no
// per-stream repeat, so nothing but this depth bound keeps a crafted file of many tiny chained streams from driving the
// goroutine stack to a fatal overflow. Legitimate files never nest object streams (ISO 32000-2 7.5.7 forbids storing an
// object stream inside another), so this cap is only ever hit by hostile input.
const maxObjStmDepth = 64

// loadObjStm parses and caches the object stream with the given object number. The stream object itself must be stored
// directly in the file (an object stream inside another object stream is forbidden by ISO 32000-2 7.5.7), which also
// rules out recursion here.
func (d *Document) loadObjStm(num int) (*objStm, error) {
	if stm, ok := d.objStms[num]; ok {
		return stm, nil
	}
	// Guard against a stream whose header keys resolve (via indirect references and back-pointing xref entries) into
	// this same stream: parseObjStm below resolves /N, /First, /Filter, and /DecodeParms, any of which can re-enter
	// loadObjStm for num before it is cached. Without this the recursion is unbounded (maxResolveDepth resets on each
	// fresh Resolve), exhausting the goroutine stack.
	if d.objStmLoading[num] {
		return nil, errObjStmCycle
	}
	// len(objStmLoading) is the number of loadObjStm calls currently in progress (each sets its entry before recursing
	// and deletes it afterward), i.e. the current nesting depth. Capping it stops a chain of distinct object streams —
	// which the per-number cycle guard above cannot catch — from recursing without bound.
	if len(d.objStmLoading) >= maxObjStmDepth {
		return nil, errObjStmDepth
	}
	entry, ok := d.xref[num]
	if !ok || entry.kind != xrefInFile {
		return nil, errObjStmSelf
	}
	obj, gen, _, err := parseIndirectAt(d.data, entry.offset, num)
	if err != nil {
		return nil, err
	}
	stream, ok := obj.(*Stream)
	if !ok {
		return nil, errNotObjStm
	}
	// The object stream is stored directly in the file, so its payload is encrypted under its own number. Decrypting it
	// here (before the /Filter chain) means the objects parsed out of it need no further decryption, matching ISO
	// 32000-2 7.6.2.
	d.decryptDirect(num, gen, stream)
	d.objStmLoading[num] = true
	stm, err := d.parseObjStm(stream)
	delete(d.objStmLoading, num)
	if err != nil {
		return nil, err
	}
	d.objStms[num] = stm
	return stm, nil
}

// parseObjStm decodes an object stream and reads its header of object-number/offset pairs.
func (d *Document) parseObjStm(stream *Stream) (*objStm, error) {
	n, ok := d.GetInt(stream.Dict, "N")
	if !ok || n < 0 {
		return nil, fmt.Errorf("%w: bad /N", errNotObjStm)
	}
	first, ok := d.GetInt(stream.Dict, "First")
	if !ok || first < 0 {
		return nil, fmt.Errorf("%w: bad /First", errNotObjStm)
	}
	data, err := d.StreamData(stream)
	if err != nil {
		return nil, err
	}
	// Each header pair needs at least three bytes ("N 0"), so /N values beyond that are lies; clamping keeps the slice
	// allocations proportional to real data.
	n = min(n, int64(len(data))/2)
	stm := &objStm{
		data:  data,
		nums:  make([]int, 0, n),
		offs:  make([]int, 0, n),
		first: int(first),
	}
	p := newParser(data, 0)
	for range n {
		num, nerr := p.expectInt()
		if nerr != nil {
			break // Tolerate a short header; the entries read so far remain usable.
		}
		off, oerr := p.expectInt()
		if oerr != nil {
			break
		}
		stm.nums = append(stm.nums, int(num))
		stm.offs = append(stm.offs, int(off))
	}
	return stm, nil
}

// objFromStm loads the object wantNum recorded at index idx of the object stream stmNum. When the recorded index does
// not name wantNum (stale or inconsistent cross-reference data), the header is searched for it (leniency).
func (d *Document) objFromStm(stmNum, idx, wantNum int) (Object, error) {
	stm, err := d.loadObjStm(stmNum)
	if err != nil {
		return nil, err
	}
	if idx < 0 || idx >= len(stm.nums) || stm.nums[idx] != wantNum {
		idx = -1
		for i, num := range stm.nums {
			if num == wantNum {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, errObjStmEntry
		}
	}
	pos := stm.first + stm.offs[idx]
	if pos < 0 || pos >= len(stm.data) {
		return nil, errObjStmEntry
	}
	p := newParser(stm.data, pos)
	return p.parseObject()
}
