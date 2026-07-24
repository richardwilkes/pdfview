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
	index map[int]int // Object number to header index; built on demand by indexOf().
	data  []byte
	nums  []int
	offs  []int
	first int
}

// indexOf returns the header index of the given object number, or -1 if the stream does not carry it. The first
// occurrence wins, matching what a linear scan of the header would find. The lookup map is built on the first call,
// since a document whose cross-reference data agrees with the stream headers never needs it, while one that disagrees
// would otherwise pay a linear scan per object — O(n²) over a full sweep of a stream with many entries.
func (s *objStm) indexOf(num int) int {
	if s.index == nil {
		s.index = make(map[int]int, len(s.nums))
		for i, n := range s.nums {
			if _, exists := s.index[n]; !exists {
				s.index[n] = i
			}
		}
	}
	if idx, ok := s.index[num]; ok {
		return idx
	}
	return -1
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
	// Each header pair needs at least three bytes (a digit, a separator, and a digit), so /N values beyond that are
	// lies; clamping keeps the slice allocations proportional to real data.
	n = min(n, int64(len(data))/3)
	// /First and the header offsets are file-supplied and otherwise unbounded, and objFromStm adds them together. Any
	// magnitude past the payload length can only ever name a position outside it, so clamping both here preserves every
	// reachable position while keeping that sum from wrapping — either in int on 32-bit builds, or in int64 for a pair
	// of astronomically large values. Without it a wrap to a small positive value slips past the bounds check there and
	// parses an object from the wrong offset inside the stream.
	limit := int64(len(data))
	stm := &objStm{
		data:  data,
		nums:  make([]int, 0, n),
		offs:  make([]int, 0, n),
		first: int(min(first, limit)),
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
		if off < 0 || off > limit {
			off = limit // Out of spec either way; limit makes objFromStm's bounds check reject the entry.
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
		if idx = stm.indexOf(wantNum); idx < 0 {
			return nil, errObjStmEntry
		}
	}
	// Both terms are clamped to len(stm.data) by parseObjStm, so widening to int64 leaves the sum well inside range on
	// every architecture.
	pos := int64(stm.first) + int64(stm.offs[idx])
	if pos < 0 || pos >= int64(len(stm.data)) {
		return nil, errObjStmEntry
	}
	p := newParser(stm.data, int(pos))
	return p.parseObject()
}
