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
	"fmt"
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
// the repair scan (see loadObject), and that the deferral is not permanent: d.repaired stays false, so the next failing
// load from the top level still repairs.
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

// TestRepairDeferredWhileLoadingXref checks the same deferral for a load reached from loadXref (see
// Document.xrefLoading), and that the failure is not cached: it was decided against an incomplete table, so the object
// stays loadable once the table is whole.
func TestRepairDeferredWhileLoadingXref(t *testing.T) {
	d := newRepairableDoc()
	d.xrefLoading = true // As if a cross-reference stream's own /Filter were being resolved.
	if obj, err := d.loadObject(1); err == nil {
		t.Fatalf("load during the cross-reference load = %v, want an error rather than a mid-flight repair", obj)
	}
	if d.repaired {
		t.Fatal("repair ran while the cross-reference load was in flight")
	}
	if len(d.objFailed) != 0 {
		t.Fatalf("the failure was cached as %v, but it was decided against an incomplete table", d.objFailed)
	}
	d.xrefLoading = false
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

// TestClearCachesKeepsInFlightObjStmLoads checks that clearCaches and repair leave objStmLoading alone, so the
// re-entrancy guard stays armed for frames still on the stack.
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
	if typ, _ := d.GetName(dict, "Type"); typ != typeCatalog {
		t.Errorf("object 2 = %v, want the catalog", dict)
	}
}

// TestRepairAcceptsZeroPaddedObjectNumbers checks that the repair sweep recovers an object whose header number is
// zero-padded ("0000000012 0 obj"), as writers reserving a fixed-width field emit: the digit-count rejection must count
// significant digits, not the token's length.
func TestRepairAcceptsZeroPaddedObjectNumbers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		num    int
		want   bool
	}{
		{name: "unpadded", header: "12 0 obj", num: 12, want: true},
		{name: "padded to ten digits", header: "0000000012 0 obj", num: 12, want: true},
		{name: "padded to twenty digits", header: "00000000000000000012 0 obj", num: 12, want: true},
		{name: "all zeros", header: "0000000000 0 obj", num: 0, want: false}, // Object 0 is never a real object.
		{name: "genuinely too large", header: "999999999 0 obj", num: 0, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Document{
				data:          []byte(pdfPrefix + tc.header + " <</Type/Catalog>>\nendobj\n"),
				xref:          make(map[int]xrefEntry),
				objCache:      make(map[int]Object),
				objFailed:     make(map[int]error),
				objStms:       make(map[int]*objStm),
				objStmLoading: make(map[int]bool),
			}
			if err := d.repair(); err != nil && tc.want {
				t.Fatalf("repair: %v", err)
			}
			if _, ok := d.xref[tc.num]; ok != tc.want {
				t.Fatalf("object %d recovered = %v, want %v (xref holds %v)", tc.num, ok, tc.want, d.xref)
			}
			if !tc.want {
				return
			}
			dict, ok := AsDict(d.LoadObject(tc.num))
			if !ok {
				t.Fatalf("object %d = %v, want a dictionary", tc.num, d.LoadObject(tc.num))
			}
			if typ, _ := d.GetName(dict, "Type"); typ != typeCatalog {
				t.Errorf("object %d = %v, want the catalog", tc.num, dict)
			}
		})
	}
}

// TestRepairReplacesDeadRoot covers the trailer a damaged file usually leaves: intact but naming an object the file no
// longer defines. The swept catalog must replace a /Root that is absent or dead, while one that resolves is kept even
// when the sweep found a later (superseded) catalog.
func TestRepairReplacesDeadRoot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trailer string
		want    int
	}{
		{name: "dead reference", trailer: "<< /Size 6 /Root 99 0 R >>", want: 5},
		{name: "reference to a non-dictionary", trailer: "<< /Size 6 /Root 3 0 R >>", want: 5},
		{name: "no /Root at all", trailer: "<< /Size 6 >>", want: 5},
		{name: "live reference is kept", trailer: "<< /Size 6 /Root 1 0 R >>", want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Objects 1 and 5 are both catalogs, and the sweep's fallback is the last one it saw, so the two answers are
			// distinguishable: a live /Root must beat the swept catalog, and a dead one must give way to it.
			var b bytes.Buffer
			b.WriteString(pdfPrefix)
			b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R /Which (first) >>\nendobj\n")
			b.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n")
			b.WriteString("3 0 obj\n(not a dictionary)\nendobj\n")
			b.WriteString("5 0 obj\n<< /Type /Catalog /Pages 2 0 R /Which (last) >>\nendobj\n")
			// startxref 0 points at the file header, so the cross-reference load fails and the sweep runs.
			fmt.Fprintf(&b, "trailer\n%s\nstartxref\n0\n%%%%EOF\n", tc.trailer)
			d, err := Open(b.Bytes())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if ref, ok := d.trailer[rootKey].(Ref); !ok || ref.Num != tc.want {
				t.Errorf("repaired /Root = %v, want %d 0 R", d.trailer[rootKey], tc.want)
			}
			root, ok := AsDict(d.Resolve(d.trailer[rootKey]))
			if !ok {
				t.Fatalf("repaired /Root = %v, which does not resolve to a dictionary", d.trailer[rootKey])
			}
			if typ, _ := d.GetName(root, "Type"); typ != typeCatalog {
				t.Errorf("repaired root = %v, want a catalog", root)
			}
		})
	}
}

// TestRepairKeepsUnresolvedRootWhenEncrypted checks that an encrypted document's unresolved /Root is kept: it may sit
// in an object stream this layer cannot decode yet, and a substituted catalog would outlive the decryptor's arrival
// (see installRepairedRoot).
func TestRepairKeepsUnresolvedRootWhenEncrypted(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(pdfPrefix)
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R /Stale true >>\nendobj\n")
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n")
	b.WriteString("4 0 obj\n<< /Filter /Standard /V 4 /R 4 /Length 128 >>\nendobj\n")
	b.WriteString("trailer\n<< /Size 5 /Root 9 0 R /Encrypt 4 0 R >>\nstartxref\n0\n%%EOF\n")
	d, err := Open(b.Bytes())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !d.Encrypted() {
		t.Fatal("the document is not reported as encrypted")
	}
	if ref, ok := d.trailer[rootKey].(Ref); !ok || ref.Num != 9 {
		t.Errorf("repaired /Root = %v, want the file's own 9 0 R kept until the document can be decrypted",
			d.trailer[rootKey])
	}
}

// TestRepairSweepIsBounded is a denial-of-service regression alarm, not a benchmark: a quadratic sweep (one resuming a
// few bytes past each failed candidate; see repair) costs about a minute at this size, the linear one milliseconds, so
// the limit is far from both.
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
