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
	"fmt"
	"testing"
)

const rootKey Name = "Root"

// refNum returns the object number of obj when it is an indirect reference, and -1 otherwise.
func refNum(obj Object) int {
	if ref, ok := obj.(Ref); ok {
		return ref.Num
	}
	return -1
}

// TestMergeTrailersLeavesInputsAlone checks that the merge builds a fresh dictionary. Its inputs are parsed objects —
// for a cross-reference stream the newest trailer is the stream's own dictionary, and the repair sweep hands over
// dictionaries lifted straight out of the file — so writing the inherited keys (or the caller's later fallback /Root)
// into trailers[0] would alter an object another consumer may also hold.
func TestMergeTrailersLeavesInputsAlone(t *testing.T) {
	newest := Dict{"Size": Integer(5), rootKey: Ref{Num: 1}}
	older := Dict{"Size": Integer(4), rootKey: Ref{Num: 9}, "Info": Ref{Num: 2}, "ID": Array{String("x")}}
	merged := mergeTrailers([]Dict{newest, older})
	if got := refNum(merged[rootKey]); got != 1 {
		t.Errorf("merged /Root = %v, want the newest trailer's 1 0 R", merged[rootKey])
	}
	if got := refNum(merged["Info"]); got != 2 {
		t.Errorf("merged /Info = %v, want the older trailer's 2 0 R", merged["Info"])
	}
	if _, ok := newest["Info"]; ok {
		t.Error("the inherited /Info was written into the newest input dictionary")
	}
	if _, ok := newest["ID"]; ok {
		t.Error("the inherited /ID was written into the newest input dictionary")
	}
	if len(newest) != 2 || len(older) != 4 {
		t.Errorf("inputs changed size: newest %v, older %v", newest, older)
	}
	// A later edit by the caller (installRepairedTrailer supplies a fallback /Root) must not reach the inputs either.
	merged[rootKey] = Ref{Num: 42}
	if got := refNum(newest[rootKey]); got != 1 {
		t.Errorf("editing the merged trailer changed the input: newest /Root = %v", newest[rootKey])
	}
	// A single trailer is still copied, and an empty chain yields a usable dictionary.
	only := Dict{rootKey: Ref{Num: 3}}
	solo := mergeTrailers([]Dict{only})
	solo["Size"] = Integer(7)
	if _, ok := only["Size"]; ok {
		t.Error("a single-trailer merge returned the input dictionary itself")
	}
	if empty := mergeTrailers(nil); empty == nil {
		t.Error("merging no trailers returned nil")
	}
}

// TestMergedTrailerIsNotTheXrefStreamDictionary exercises the same invariant end to end: the newest section is a
// cross-reference stream without /Root, so the merge inherits /Root from the older section. The stream object itself
// must come back unchanged.
func TestMergedTrailerIsNotTheXrefStreamDictionary(t *testing.T) {
	d, err := Open(chainedXrefStreamPDF())
	if err != nil {
		t.Fatal(err)
	}
	if got := refNum(d.Trailer()[rootKey]); got != 1 {
		t.Fatalf("merged trailer /Root = %v, want 1 0 R inherited from the older section", d.Trailer()[rootKey])
	}
	stream, ok := AsStream(d.LoadObject(5))
	if !ok {
		t.Fatal("object 5 is not the newest cross-reference stream")
	}
	if _, has := stream.Dict[rootKey]; has {
		t.Error("the merge wrote /Root into the cross-reference stream's own dictionary")
	}
}

// chainedXrefStreamPDF builds a file whose newest cross-reference stream (object 5) carries no /Root and chains via
// /Prev to an older cross-reference stream (object 3) that does.
func chainedXrefStreamPDF() []byte {
	var buf bytes.Buffer
	row := func(typ byte, field2 int, field3 byte) []byte {
		return []byte{typ, byte(field2 >> 24), byte(field2 >> 16), byte(field2 >> 8), byte(field2), field3}
	}
	writeStream := func(num int, dictBody string, rows ...[]byte) {
		data := bytes.Join(rows, nil)
		fmt.Fprintf(&buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", num, dictBody, len(data))
		buf.Write(data)
		buf.WriteString("\nendstream\nendobj\n")
	}
	buf.WriteString("%PDF-1.7\n")
	off1 := buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n")
	off3 := buf.Len()
	writeStream(3, "/Type /XRef /Size 4 /W [1 4 1] /Root 1 0 R",
		row(0, 0, 255), row(1, off1, 0), row(1, off2, 0), row(1, off3, 0))
	off5 := buf.Len()
	writeStream(5, fmt.Sprintf("/Type /XRef /Size 6 /W [1 4 1] /Prev %d", off3),
		row(0, 0, 255), row(1, off1, 0), row(1, off2, 0), row(1, off3, 0), row(0, 0, 0), row(1, off5, 0))
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", off5)
	return buf.Bytes()
}
