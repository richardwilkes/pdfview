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
	"compress/zlib"
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

// xrefRow encodes one cross-reference stream entry under /W [1 4 1].
func xrefRow(typ byte, field2 int, field3 byte) []byte {
	return []byte{typ, byte(field2 >> 24), byte(field2 >> 16), byte(field2 >> 8), byte(field2), field3}
}

// chainedXrefStreamPDF builds a file whose newest cross-reference stream (object 5) carries no /Root and chains via
// /Prev to an older cross-reference stream (object 3) that does.
func chainedXrefStreamPDF() []byte {
	var buf bytes.Buffer
	row := xrefRow
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

// TestXrefStreamRequiresDirectFilter covers both halves of the rule that a cross-reference stream's dictionary must be
// self-contained. A direct /Filter decodes normally and the section's entries land in the table; an indirect one is
// refused before StreamData can resolve it, because resolving it would read against the very table this section is
// still defining — here through a stale entry from the newer classic table that points object 7 somewhere it is not.
// The old behavior is visible in the object-2 assertion: resolving the reference failed, which pulled the document-wide
// repair scan into the middle of the cross-reference load, and the rebuilt table it installed (later definition of a
// number wins) replaced the classic table (newest increment wins) that had already been read.
func TestXrefStreamRequiresDirectFilter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantMarker string
		indirect   bool
	}{
		{name: "direct", indirect: false, wantMarker: "from-the-xref-stream"},
		{name: "indirect", indirect: true, wantMarker: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Open(hybridIndirectFilterPDF(tc.indirect))
			if err != nil {
				t.Fatal(err)
			}
			if d.repaired {
				t.Error("the repair scan ran; the classic table is complete and authoritative")
			}
			pages, ok := AsDict(d.LoadObject(2))
			if !ok {
				t.Fatalf("object 2 = %v, want a dictionary", d.LoadObject(2))
			}
			if tag, _ := d.GetString(pages, "Tag"); string(tag) != "from-the-table" {
				t.Errorf("object 2 /Tag = %q, want %q: the classic table names the file's first definition of "+
					"object 2, and only a repair sweep would name the second", tag, "from-the-table")
			}
			// Object 4 is supplied by the /XRefStm alone, so it is present exactly when that section was read.
			marker := ""
			if dict, isDict := AsDict(d.LoadObject(4)); isDict {
				str, _ := d.GetString(dict, "Marker")
				marker = string(str)
			}
			if marker != tc.wantMarker {
				t.Errorf("object 4 /Marker = %q, want %q", marker, tc.wantMarker)
			}
		})
	}
}

// hybridIndirectFilterPDF builds a hybrid-reference file whose /XRefStm cross-reference stream names its /Filter as
// 7 0 R when indirect is set and as /FlateDecode directly when it is not. The classic table locates object 7 at an
// offset that does not hold it, so resolving that reference fails — which, before the fix, ran the repair scan from
// inside the cross-reference load. The table is complete and is the only thing distinguishing the file's two
// definitions of object 2: it names the first, while a repair sweep names the last definition of each number. Object 4
// appears only in the cross-reference stream's own /Index, so its presence reports whether that section was read.
func hybridIndirectFilterPDF(indirect bool) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	off1 := buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 /Tag (from-the-table) >>\nendobj\n")
	off4 := buf.Len()
	buf.WriteString("4 0 obj\n<< /Marker (from-the-xref-stream) >>\nendobj\n")
	buf.WriteString("7 0 obj\n/FlateDecode\nendobj\n")
	off3 := buf.Len()
	filter := "/FlateDecode"
	if indirect {
		filter = "7 0 R"
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zw.Write(xrefRow(1, off4, 0)) //nolint:errcheck // Writing to a bytes.Buffer cannot fail.
	zw.Close()                    //nolint:errcheck // See above.
	fmt.Fprintf(&buf, "3 0 obj\n<< /Type /XRef /Size 8 /W [1 4 1] /Index [4 1] /Filter %s /Length %d >>\nstream\n",
		filter, compressed.Len())
	buf.Write(compressed.Bytes())
	buf.WriteString("\nendstream\nendobj\n")
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 /Tag (from-the-sweep) >>\nendobj\n")
	xrefOff := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 4\n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \n%010d 00000 n \n",
		off1, off2, off3)
	// Object 7 is real, but the table places it two bytes into object 1, where its header does not match.
	fmt.Fprintf(&buf, "7 1\n%010d 00000 n \n", off1+2)
	fmt.Fprintf(&buf, "trailer\n<< /Size 8 /Root 1 0 R /XRefStm %d >>\nstartxref\n%d\n%%%%EOF\n", off3, xrefOff)
	return buf.Bytes()
}

// TestHasIndirect pins the shapes the cross-reference stream check must catch: a bare reference, one buried in the
// array form of /Filter, and one used as a /DecodeParms value. Nesting past any legal shape counts as indirect too,
// which keeps the walk bounded on hostile input.
func TestHasIndirect(t *testing.T) {
	deep := Object(Integer(1))
	for range maxDirectCheckDepth + 1 {
		deep = Array{deep}
	}
	for _, tc := range []struct {
		obj  Object
		name string
		want bool
	}{
		{name: "absent", obj: nil, want: false},
		{name: "name", obj: Name("FlateDecode"), want: false},
		{name: "array of names", obj: Array{Name("ASCII85Decode"), Name("FlateDecode")}, want: false},
		{name: "dictionary of numbers", obj: Dict{"Predictor": Integer(12), columnsKey: Integer(5)}, want: false},
		{name: "reference", obj: Ref{Num: 7}, want: true},
		{name: "reference in an array", obj: Array{Name("FlateDecode"), Ref{Num: 7}}, want: true},
		{name: "reference as a parameter", obj: Dict{columnsKey: Ref{Num: 7}}, want: true},
		{name: "reference in an array of parameters", obj: Array{Dict{columnsKey: Ref{Num: 7}}}, want: true},
		{name: "nested past any legal shape", obj: deep, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasIndirect(tc.obj, 0); got != tc.want {
				t.Errorf("hasIndirect(%v) = %v, want %v", tc.obj, got, tc.want)
			}
		})
	}
}
