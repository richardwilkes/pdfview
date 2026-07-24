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
	"testing"
)

// newFailureCacheDoc returns a document holding one directly-stored object, number 1, whose body is the string (ok),
// and whose cross-reference entry deliberately points past the object's header, at the body. Loading object 1
// therefore fails until the entry is corrected. The repaired flag is preset so that the failure does not trigger the
// document-wide repair scan, which would rebuild the entry from the file and hide the failure.
func newFailureCacheDoc() (d *Document, goodOffset int64) {
	prefix := "%PDF-1.7\n"
	header := "1 0 obj\n"
	goodOffset = int64(len(prefix))
	return &Document{
		data:          []byte(prefix + header + "(ok)\nendobj\n"),
		xref:          map[int]xrefEntry{1: {kind: xrefInFile, offset: goodOffset + int64(len(header))}},
		objCache:      make(map[int]Object),
		objFailed:     make(map[int]error),
		objStms:       make(map[int]*objStm),
		objStmLoading: make(map[int]bool),
		repaired:      true,
	}, goodOffset
}

// TestFailedObjectLoadIsCached checks that an object that cannot be loaded is parsed at most once: a broken reference
// that a content stream names once per operator must not re-run parseIndirectAt (and its file-wide endstream scan) on
// every resolution. Repointing the cross-reference entry at the real object after the failure makes the difference
// visible — a second parse would succeed, so a Null result proves no second parse happened.
func TestFailedObjectLoadIsCached(t *testing.T) {
	d, goodOffset := newFailureCacheDoc()
	if obj, err := d.loadObject(1); err == nil {
		t.Fatalf("first load: got %v, want an error", obj)
	}
	if _, ok := d.objFailed[1]; !ok {
		t.Fatal("failure was not recorded in objFailed")
	}
	entry := d.xref[1]
	entry.offset = goodOffset
	d.xref[1] = entry
	if _, err := d.loadObject(1); err == nil {
		t.Fatal("second load re-parsed the object instead of using the cached failure")
	}
	obj := d.Resolve(Ref{Num: 1})
	if _, ok := obj.(Null); !ok {
		t.Errorf("Resolve of an unloadable reference = %v, want Null", obj)
	}
}

// TestDropCachesClearsFailures checks that the failure cache is invalidated along with the object cache. The security
// handler drops the caches after authentication, so objects that failed to parse under the pre-authentication (keyless)
// state must be retried rather than left permanently unloadable.
func TestDropCachesClearsFailures(t *testing.T) {
	d, goodOffset := newFailureCacheDoc()
	if _, err := d.loadObject(1); err == nil {
		t.Fatal("first load: want an error")
	}
	entry := d.xref[1]
	entry.offset = goodOffset
	d.xref[1] = entry
	d.DropCaches()
	obj, err := d.loadObject(1)
	if err != nil {
		t.Fatalf("load after DropCaches: %v", err)
	}
	if s, ok := obj.(String); !ok || string(s) != "ok" {
		t.Errorf("load after DropCaches = %v, want (ok)", obj)
	}
}

// TestObjStmGuardFailuresAreNotCached checks that a load refused by the object-stream re-entrancy guard is not recorded
// as a permanent failure. That guard fires because of where the load sits in the call stack — an object stream's own
// header keys resolving back into the stream — so the same object must still load when reached from the top level.
func TestObjStmGuardFailuresAreNotCached(t *testing.T) {
	prefix := "%PDF-1.7\n"
	d := &Document{
		data: []byte(prefix + "5 0 obj\n<< /Type /ObjStm /N 1 /First 4 /Length 8 >>\nstream\n7 0\n(ok)\nendstream\n" +
			"endobj\n"),
		xref: map[int]xrefEntry{
			5: {kind: xrefInFile, offset: int64(len(prefix))},
			7: {kind: xrefInStream, stmNum: 5, stmIdx: 0},
		},
		objCache:      make(map[int]Object),
		objFailed:     make(map[int]error),
		objStms:       make(map[int]*objStm),
		objStmLoading: map[int]bool{5: true}, // As if stream 5's own header keys were being resolved.
		repaired:      true,
	}
	_, err := d.loadObject(7)
	if !errors.Is(err, errObjStmCycle) {
		t.Fatalf("load while stream 5 is loading = %v, want %v", err, errObjStmCycle)
	}
	if _, ok := d.objFailed[7]; ok {
		t.Fatal("a re-entrancy refusal was recorded as a permanent failure")
	}
	delete(d.objStmLoading, 5)
	obj, err := d.loadObject(7)
	if err != nil {
		t.Fatalf("load from the top level: %v", err)
	}
	if s, ok := obj.(String); !ok || string(s) != "ok" {
		t.Errorf("load from the top level = %v, want (ok)", obj)
	}
}
