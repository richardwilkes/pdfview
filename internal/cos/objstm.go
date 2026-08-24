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
// occurrence wins, as a linear scan of the header would. The map is built on first call: a document whose
// cross-reference data agrees with the headers never needs it, while one that disagrees would otherwise pay a linear
// scan per object, O(n²) over a full sweep.
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

// maxObjStmDepth caps how many object streams may be under load at once. objStmLoading stops a stream from re-entering
// itself, but a chain of distinct object streams (each one's header keys resolving into the next via back-pointing
// xref entries) recurses through loadObjStm with no repeat, so only this bound keeps a crafted file from overflowing
// the goroutine stack. ISO 32000-2 7.5.7 forbids storing an object stream inside another, so only hostile input hits
// it.
const maxObjStmDepth = 64

// loadObjStm parses and caches the object stream with the given object number. The stream object itself must be stored
// directly in the file (ISO 32000-2 7.5.7). That does not rule out re-entry: parseObjStm resolves the header keys,
// which can lead back here; see the two guards below.
func (d *Document) loadObjStm(num int) (*objStm, error) {
	if stm, ok := d.objStms[num]; ok {
		return stm, nil
	}
	// A stream whose header keys (/N, /First, /Filter, /DecodeParms) resolve back into this same stream re-enters
	// loadObjStm for num before it is cached. Without this guard the recursion is unbounded, since maxResolveDepth
	// resets on each fresh Resolve.
	if d.objStmLoading[num] {
		return nil, errObjStmCycle
	}
	// len(objStmLoading) is the current nesting depth: each call sets its entry before recursing and deletes it
	// afterward. Capping it stops a chain of distinct streams, which the cycle guard above cannot catch.
	if len(d.objStmLoading) >= maxObjStmDepth {
		return nil, errObjStmDepth
	}
	entry, ok := d.xref[num]
	if !ok || entry.kind != xrefInFile {
		return nil, errObjStmSelf
	}
	obj, gen, err := d.parseIndirectObjectAt(entry.offset, num)
	if err != nil {
		return nil, err
	}
	stream, ok := obj.(*Stream)
	if !ok {
		return nil, errNotObjStm
	}
	// Stored directly in the file, so the payload is encrypted under the stream's own number. Decrypting it before the
	// /Filter chain means the objects parsed out of it need no further decryption (ISO 32000-2 7.6.2).
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
	// Each header pair needs at least three bytes, so /N beyond len(data)/3 is a lie; the clamp keeps the loop
	// proportional to real data. It bounds only the loop, never the allocation: the decoded payload can reach
	// internal/filter's max(64 MB, 256x input), and preallocating from the clamp let a 61 KB file declaring /N 99999999
	// over a 60 MB payload of non-numeric bytes retain ~380 MB of slices for the document's lifetime after breaking out
	// on the first entry. The slices grow from parsed entries instead.
	n = min(n, int64(len(data))/3)
	// /First and the header offsets are file-supplied, and objFromStm adds them together. Any magnitude past the payload
	// length names a position outside it anyway, so clamping both preserves every reachable position while keeping the
	// sum from wrapping to a small positive value that would slip past the bounds check there.
	limit := int64(len(data))
	stm := &objStm{
		data:  data,
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
	// Both terms are non-negative and clamped to len(stm.data) by parseObjStm, so the sum is at most twice the payload
	// length — nowhere near a wrap.
	pos := stm.first + stm.offs[idx]
	if pos < 0 || pos >= len(stm.data) {
		return nil, errObjStmEntry
	}
	p := newParser(stm.data, pos)
	return p.parseObject()
}
