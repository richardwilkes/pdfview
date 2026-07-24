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
	"math"
	"reflect"
	"testing"
)

const firstKey = "First"

// TestParseObjStmClampsHeaderCount checks that a hostile /N cannot drive the header slice allocations beyond what the
// decoded payload could possibly hold: every header pair needs at least three bytes, so the capacities stay within
// len(data)/3 no matter what /N claims. The entries that really are present must still parse.
func TestParseObjStmClampsHeaderCount(t *testing.T) {
	payload := []byte("7 0 9 4\n(a)\n(b)")
	for _, n := range []int64{2, 1 << 40} {
		d := &Document{}
		stm, err := d.parseObjStm(&Stream{
			Dict: Dict{"N": Integer(n), firstKey: Integer(8)},
			Raw:  payload,
		})
		if err != nil {
			t.Fatalf("/N %d: %v", n, err)
		}
		if bound := len(payload) / 3; cap(stm.nums) > bound || cap(stm.offs) > bound {
			t.Errorf("/N %d: capacities %d/%d exceed the %d-pair bound for %d payload bytes", n, cap(stm.nums),
				cap(stm.offs), bound, len(payload))
		}
		if len(stm.nums) != 2 || stm.nums[0] != 7 || stm.nums[1] != 9 {
			t.Errorf("/N %d: object numbers = %v, want [7 9]", n, stm.nums)
		}
		if len(stm.offs) != 2 || stm.offs[0] != 0 || stm.offs[1] != 4 {
			t.Errorf("/N %d: offsets = %v, want [0 4]", n, stm.offs)
		}
	}
}

// TestParseObjStmClampsHeaderOffsets checks that an unbounded /First and unbounded header offsets cannot combine into a
// position inside the payload. Both are file-supplied and objFromStm adds them together, so without clamping a pair such
// as /First math.MaxInt64 with an offset of -(math.MaxInt64-3) lands on offset 3 — an object parsed from the wrong place
// in the stream, reported as if it were the one asked for. Every such entry must be rejected instead.
func TestParseObjStmClampsHeaderOffsets(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		first int64
	}{
		{name: "huge /First with a negative offset", first: math.MaxInt64, body: "9 -9223372036854775804"},
		{name: "huge /First with a zero offset", first: math.MaxInt64, body: "9 0"},
		{name: "sane /First with a negative offset", first: 6, body: "9 -3"},
		{name: "sane /First with a huge offset", first: 6, body: "9 9223372036854775807"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &Document{}
			stm, err := d.parseObjStm(&Stream{
				Dict: Dict{"N": Integer(1), firstKey: Integer(test.first)},
				Raw:  []byte(test.body + "\n(a)\n(b)\n(c)"),
			})
			if err != nil {
				t.Fatalf("parseObjStm: %v", err)
			}
			d.objStms = map[int]*objStm{5: stm}
			// Both terms must stay within the payload so that their sum is always representable, and the sum itself must
			// land outside it so that the entry is reported as missing rather than parsed from a bogus position.
			if stm.first < 0 || stm.first > len(stm.data) {
				t.Errorf("first = %d, want within [0, %d]", stm.first, len(stm.data))
			}
			if stm.offs[0] < 0 || stm.offs[0] > len(stm.data) {
				t.Errorf("offset = %d, want within [0, %d]", stm.offs[0], len(stm.data))
			}
			if obj, oerr := d.objFromStm(5, 0, 9); !errors.Is(oerr, errObjStmEntry) {
				t.Errorf("objFromStm = %v, %v; want %v", obj, oerr, errObjStmEntry)
			}
		})
	}
}

// newTestObjStmDoc returns a document holding one object stream, number 5, whose header names objects 7, 9, and 7 again
// (a deliberate duplicate) at the payloads "(a)", "(b)", and "(c)" respectively.
func newTestObjStmDoc(t *testing.T) (*Document, *objStm) {
	t.Helper()
	d := &Document{}
	stm, err := d.parseObjStm(&Stream{
		Dict: Dict{"N": Integer(3), firstKey: Integer(12)},
		Raw:  []byte("7 0 9 4 7 8\n(a)\n(b)\n(c)"),
	})
	if err != nil {
		t.Fatalf("parseObjStm: %v", err)
	}
	d.objStms = map[int]*objStm{5: stm}
	return d, stm
}

// TestObjFromStmRecoversFromWrongIndex checks the leniency path taken when the cross-reference data's index into an
// object stream does not name the object being asked for: the object must still be found, the first header entry for a
// repeated object number wins (as the previous linear scan did), and an object the stream does not carry is still an
// error.
func TestObjFromStmRecoversFromWrongIndex(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
		idx  int
	}{
		{name: "correct index", idx: 1, want: "b"},
		{name: "stale index", idx: 0, want: "b"},
		{name: "index past the header", idx: 99, want: "b"},
		{name: "negative index", idx: -1, want: "b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, _ := newTestObjStmDoc(t)
			obj, err := d.objFromStm(5, test.idx, 9)
			if err != nil {
				t.Fatalf("objFromStm: %v", err)
			}
			if s, ok := obj.(String); !ok || string(s) != test.want {
				t.Errorf("object = %v, want %s", obj, test.want)
			}
		})
	}
	t.Run("duplicate object number takes the first entry", func(t *testing.T) {
		d, _ := newTestObjStmDoc(t)
		obj, err := d.objFromStm(5, 2, 7) // Index 2 does name 7, but so does index 0, which the scan must prefer.
		if err != nil {
			t.Fatalf("objFromStm: %v", err)
		}
		if s, ok := obj.(String); !ok || string(s) != "c" {
			t.Errorf("object = %v, want c", obj)
		}
		if obj, err = d.objFromStm(5, 1, 7); err != nil {
			t.Fatalf("objFromStm: %v", err)
		}
		if s, ok := obj.(String); !ok || string(s) != "a" {
			t.Errorf("object = %v, want a", obj)
		}
	})
	t.Run("object not in the stream", func(t *testing.T) {
		d, _ := newTestObjStmDoc(t)
		if _, err := d.objFromStm(5, 0, 42); !errors.Is(err, errObjStmEntry) {
			t.Errorf("err = %v, want %v", err, errObjStmEntry)
		}
	})
}

// TestObjStmIndexBuiltOnce checks that the object-number lookup map backing the leniency path is built lazily and then
// reused. Reuse is what keeps an exhaustive sweep of a stream whose cross-reference indices are all wrong linear rather
// than quadratic: without it, every object costs a scan of the whole header.
func TestObjStmIndexBuiltOnce(t *testing.T) {
	d, stm := newTestObjStmDoc(t)
	if stm.index != nil {
		t.Error("lookup map was built before any lookup needed it")
	}
	if _, err := d.objFromStm(5, 1, 9); err != nil { // The index is correct, so no map is needed.
		t.Fatalf("objFromStm: %v", err)
	}
	if stm.index != nil {
		t.Error("lookup map was built for a lookup that the recorded index satisfied")
	}
	if _, err := d.objFromStm(5, 0, 9); err != nil {
		t.Fatalf("objFromStm: %v", err)
	}
	first := reflect.ValueOf(stm.index).Pointer()
	if first == 0 {
		t.Fatal("lookup map was not built for a lookup the recorded index failed")
	}
	for _, num := range []int{7, 9, 42} {
		if _, err := d.objFromStm(5, 0, num); err != nil && !errors.Is(err, errObjStmEntry) {
			t.Fatalf("objFromStm %d: %v", num, err)
		}
		if again := reflect.ValueOf(stm.index).Pointer(); again != first {
			t.Fatalf("lookup for %d rebuilt the map (%#x, was %#x)", num, again, first)
		}
	}
}
