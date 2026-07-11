package cos

import (
	"errors"
	"fmt"
)

// objStm is a parsed object stream (/Type /ObjStm): the decoded payload plus the header's object-number/offset
// pairs.
type objStm struct {
	data  []byte
	nums  []int
	offs  []int
	first int
}

var (
	errObjStmEntry = errors.New("object not found in object stream")
	errObjStmSelf  = errors.New("object stream is not stored directly in the file")
)

// loadObjStm parses and caches the object stream with the given object number. The stream object itself must be
// stored directly in the file (an object stream inside another object stream is forbidden by ISO 32000-2
// 7.5.7), which also rules out recursion here.
func (d *Document) loadObjStm(num int) (*objStm, error) {
	if stm, ok := d.objStms[num]; ok {
		return stm, nil
	}
	entry, ok := d.xref[num]
	if !ok || entry.kind != xrefInFile {
		return nil, errObjStmSelf
	}
	obj, _, err := parseIndirectAt(d.data, entry.offset, num)
	if err != nil {
		return nil, err
	}
	stream, ok := obj.(*Stream)
	if !ok {
		return nil, errNotObjStm
	}
	stm, err := d.parseObjStm(stream)
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
	// Each header pair needs at least three bytes ("N 0"), so /N values beyond that are lies; clamping keeps the
	// slice allocations proportional to real data.
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

// objFromStm loads the object wantNum recorded at index idx of the object stream stmNum. When the recorded index
// does not name wantNum (stale or inconsistent cross-reference data), the header is searched for it (leniency).
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
