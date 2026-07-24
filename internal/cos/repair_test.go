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
	"strings"
	"testing"
	"time"
)

// newRepairableDoc returns a document whose single object, number 1, holds the string (ok) but whose cross-reference
// entry points at the body rather than the header, so loading it fails until the repair sweep rebuilds the entry.
func newRepairableDoc() *Document {
	prefix := pdfPrefix
	header := "1 0 obj\n"
	return &Document{
		data:          []byte(prefix + header + "(ok)\nendobj\n"),
		xref:          map[int]xrefEntry{1: {kind: xrefInFile, offset: int64(len(prefix) + len(header))}},
		objCache:      make(map[int]Object),
		objFailed:     make(map[int]error),
		objStms:       make(map[int]*objStm),
		objStmLoading: make(map[int]bool),
	}
}

// TestRepairDeferredWhileObjStmLoading checks that a failed load nested inside an object-stream parse does not trigger
// the document-wide repair scan. Repair replaces the cross-reference table and drops every cache while those frames are
// still parsing against the old one, and its own loadObjStm sweep would re-enter a stream whose parse is suspended
// further down the stack. The deferral must not be permanent: d.repaired stays false, so the next failing load reached
// from the top level still repairs.
func TestRepairDeferredWhileObjStmLoading(t *testing.T) {
	d := newRepairableDoc()
	d.objStmLoading[5] = true // As if object stream 5's own header keys were being resolved.
	if obj, err := d.loadObject(1); err == nil {
		t.Fatalf("load while stream 5 is loading = %v, want an error rather than a mid-flight repair", obj)
	}
	if d.repaired {
		t.Fatal("repair ran while an object-stream load was in flight")
	}
	if !d.objStmLoading[5] {
		t.Fatal("the in-flight object-stream marker was discarded")
	}
	delete(d.objStmLoading, 5)
	d.DropCaches() // The security handler's cache drop; here it clears the cached failure so the retry is visible.
	obj, err := d.loadObject(1)
	if err != nil {
		t.Fatalf("load from the top level: %v", err)
	}
	if s, ok := obj.(String); !ok || string(s) != "ok" {
		t.Errorf("load from the top level = %v, want (ok)", obj)
	}
	if !d.repaired {
		t.Error("the top-level load did not run the repair scan")
	}
}

// TestClearCachesKeepsInFlightObjStmLoads checks that clearCaches leaves objStmLoading alone. That map is not a cache
// but the set of loadObjStm frames currently on the stack; replacing it strands their markers in the discarded map and
// disarms the re-entrancy guard for the rest of the recursion, letting a stream be re-entered while it is still being
// parsed.
func TestClearCachesKeepsInFlightObjStmLoads(t *testing.T) {
	d := newRepairableDoc()
	d.objStmLoading[9] = true
	d.clearCaches()
	if !d.objStmLoading[9] {
		t.Fatal("clearCaches discarded an in-flight object-stream marker")
	}
	if _, err := d.loadObjStm(9); !errors.Is(err, errObjStmCycle) {
		t.Errorf("re-entering stream 9 after clearCaches = %v, want %v", err, errObjStmCycle)
	}
	// A full repair goes through clearCaches too, and must leave the guard armed the same way.
	d.objStmLoading[9] = true
	if err := d.repair(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if _, err := d.loadObjStm(9); !errors.Is(err, errObjStmCycle) {
		t.Errorf("re-entering stream 9 after repair = %v, want %v", err, errObjStmCycle)
	}
}

// TestParseIndirectReportsResumePositionOnFailure checks that a failed parse reports how far it read. The repair sweep
// charges itself for that work and continues past it; without the report it would advance three bytes and re-lex the
// same span from every following candidate.
func TestParseIndirectReportsResumePositionOnFailure(t *testing.T) {
	// An unterminated hex string reads to end of input, so nothing after it is worth re-examining.
	data := []byte("1 0 obj <41414141")
	if _, _, end, err := parseIndirectAt(data, 0, -1); err == nil {
		t.Error("expected an error for an unterminated hex string")
	} else if end != int64(len(data)) {
		t.Errorf("resume position = %d, want %d (end of input)", end, len(data))
	}
	// A parse that stops early reports only what it read — lookahead returned to the pushback stack is not counted as
	// consumed — so the next object's "obj" keyword stays ahead of the resume point and the sweep, which backtracks
	// from that keyword over the number pair, still finds it.
	data = []byte("1 0 obj <</A 1\n2 0 obj <</Type/Catalog>>\nendobj\n")
	_, _, end, err := parseIndirectAt(data, 0, -1)
	if err == nil {
		t.Fatal("expected an error for an unterminated dictionary")
	}
	if keyword := int64(bytes.Index(data, []byte("2 0 obj")) + len("2 0 ")); end > keyword {
		t.Errorf("resume position = %d, want no more than %d so object 2 remains reachable", end, keyword)
	}
}

// TestRepairFindsObjectAfterFailedParse pins the recovery side of resuming past a failed attempt: an object whose
// header follows a malformed one must still be swept up.
func TestRepairFindsObjectAfterFailedParse(t *testing.T) {
	d := &Document{
		data:          []byte(pdfPrefix + "1 0 obj <</A 1\n2 0 obj <</Type/Catalog>>\nendobj\n"),
		xref:          make(map[int]xrefEntry),
		objCache:      make(map[int]Object),
		objFailed:     make(map[int]error),
		objStms:       make(map[int]*objStm),
		objStmLoading: make(map[int]bool),
	}
	if err := d.repair(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if _, ok := d.xref[2]; !ok {
		t.Fatal("repair did not recover object 2, which follows a malformed object")
	}
	if ref, ok := d.trailer["Root"].(Ref); !ok || ref.Num != 2 {
		t.Errorf("repaired /Root = %v, want a reference to object 2", d.trailer["Root"])
	}
	dict, ok := AsDict(d.LoadObject(2))
	if !ok {
		t.Fatalf("object 2 = %v, want a dictionary", d.LoadObject(2))
	}
	if typ, _ := d.GetName(dict, "Type"); typ != "Catalog" {
		t.Errorf("object 2 = %v, want the catalog", dict)
	}
}

// TestRepairSweepIsBounded checks that the repair scan does not amplify hostile input. Both the object sweep and the
// trailer sweep used to build a fresh parser at every candidate offset and, on failure, advance the cursor by only a
// few bytes — while the failed parse itself had already read toward end of input through an unterminated string. That
// is quadratic: measured through the public API, a body of repeated "1 0 obj <" cost 0.19 s at 50 KB, 0.66 s at 100 KB
// and 2.49 s at 200 KB, putting a 4 MB file at roughly a quarter hour. The limit here is enormously larger than the
// repaired cost (milliseconds) and far below the pre-fix cost of about a minute for this size, so it is a
// denial-of-service regression alarm rather than a benchmark.
func TestRepairSweepIsBounded(t *testing.T) {
	const (
		size  = 1 << 20
		limit = 10 * time.Second
	)
	for _, tc := range []struct {
		name string
		unit string
	}{
		{name: "object headers", unit: "1 0 obj <\n"},
		{name: "trailer keywords", unit: "trailer <\n"},
		{name: "literal strings", unit: "1 0 obj (\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("%PDF-1.7\n" + strings.Repeat(tc.unit, size/len(tc.unit)))
			start := time.Now()
			// No usable root is expected; the point is that the attempt terminates promptly.
			if _, err := Open(data); err == nil {
				t.Error("expected the hostile body to fail to open")
			}
			if elapsed := time.Since(start); elapsed > limit {
				t.Errorf("repair of %d bytes took %v, want well under %v", len(data), elapsed, limit)
			}
		})
	}
}
