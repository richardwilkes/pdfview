// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos_test

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// builder assembles small PDF files with correct offsets for the classic-xref tests.
type builder struct {
	offsets map[int]int
	buf     bytes.Buffer
	maxNum  int
}

func newBuilder() *builder {
	b := &builder{offsets: make(map[int]int)}
	b.buf.WriteString("%PDF-1.7\n")
	return b
}

func (b *builder) add(num int, body string) {
	b.offsets[num] = b.buf.Len()
	fmt.Fprintf(&b.buf, "%d 0 obj\n%s\nendobj\n", num, body)
	if num > b.maxNum {
		b.maxNum = num
	}
}

func (b *builder) addStream(num int, dictBody string, data []byte) {
	b.offsets[num] = b.buf.Len()
	fmt.Fprintf(&b.buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", num, dictBody, len(data))
	b.buf.Write(data)
	b.buf.WriteString("\nendstream\nendobj\n")
	if num > b.maxNum {
		b.maxNum = num
	}
}

// finishClassic writes a classic xref table covering objects 0..maxNum plus the trailer and startxref.
func (b *builder) finishClassic(trailerExtra string) []byte {
	xrefOff := b.buf.Len()
	fmt.Fprintf(&b.buf, "xref\n0 %d\n", b.maxNum+1)
	fmt.Fprintf(&b.buf, "0000000000 65535 f \n")
	for num := 1; num <= b.maxNum; num++ {
		if off, ok := b.offsets[num]; ok {
			fmt.Fprintf(&b.buf, "%010d 00000 n \n", off)
		} else {
			fmt.Fprintf(&b.buf, "0000000000 65535 f \n")
		}
	}
	fmt.Fprintf(&b.buf, "trailer\n<< /Size %d /Root 1 0 R %s>>\nstartxref\n%d\n%%%%EOF\n",
		b.maxNum+1, trailerExtra, xrefOff)
	return b.buf.Bytes()
}

const (
	catalogBody = "<< /Type /Catalog /Pages 2 0 R >>"
	pagesBody   = "<< /Type /Pages /Kids [] /Count 0 >>"
)

func mustOpen(t *testing.T, data []byte) *cos.Document {
	t.Helper()
	d, err := cos.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func checkCatalog(t *testing.T, d *cos.Document) {
	t.Helper()
	root, ok := d.GetDict(d.Trailer(), "Root")
	if !ok {
		t.Fatal("no root dictionary")
	}
	if typ, _ := d.GetName(root, "Type"); typ != "Catalog" {
		t.Fatalf("root Type = %q", typ)
	}
	if _, ok = d.GetDict(root, "Pages"); !ok {
		t.Fatal("no pages dictionary")
	}
}

func TestClassicXref(t *testing.T) {
	b := newBuilder()
	b.add(1, catalogBody)
	b.add(2, pagesBody)
	b.addStream(3, "", []byte("BT ET"))
	d := mustOpen(t, b.finishClassic(""))
	checkCatalog(t, d)
	stream, ok := cos.AsStream(d.LoadObject(3))
	if !ok {
		t.Fatal("object 3 is not a stream")
	}
	data, err := d.StreamData(stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "BT ET" {
		t.Errorf("stream data = %q", data)
	}
	// A reference to an absent object resolves to Null, not an error.
	if _, isNull := d.Resolve(cos.Ref{Num: 99}).(cos.Null); !isNull {
		t.Error("expected Null for an absent object")
	}
}

// TestClassicXrefPrevChain exercises an incremental update: the newer section overrides object 2 and adds
// object 4, and its trailer lacks /Root, which must be inherited from the older trailer.
func TestClassicXrefPrevChain(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	off1 := buf.Len()
	buf.WriteString("1 0 obj\n" + catalogBody + "\nendobj\n")
	off2 := buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 /Version (old) >>\nendobj\n")
	xref1 := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 3\n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \n", off1, off2)
	fmt.Fprintf(&buf, "trailer\n<< /Size 3 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref1)
	// Incremental update: replace object 2, add object 4.
	off2b := buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 /Version (new) >>\nendobj\n")
	off4 := buf.Len()
	buf.WriteString("4 0 obj\n(fourth)\nendobj\n")
	xref2 := buf.Len()
	fmt.Fprintf(&buf, "xref\n2 1\n%010d 00000 n \n4 1\n%010d 00000 n \n", off2b, off4)
	fmt.Fprintf(&buf, "trailer\n<< /Size 5 /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", xref1, xref2)
	d := mustOpen(t, buf.Bytes())
	checkCatalog(t, d)
	pages, _ := d.GetDict(d.Trailer(), "Root")
	pages, _ = d.GetDict(pages, "Pages")
	if version, _ := d.GetString(pages, "Version"); string(version) != "new" {
		t.Errorf("expected the newer object 2, got version %q", version)
	}
	if s, ok := cos.AsString(d.LoadObject(4)); !ok || string(s) != "fourth" {
		t.Error("object 4 from the update section is missing")
	}
}

// xrefStreamRow builds one W=[1 4 1] entry.
func xrefStreamRow(typ byte, field2 int, field3 byte) []byte {
	return []byte{typ, byte(field2 >> 24), byte(field2 >> 16), byte(field2 >> 8), byte(field2), field3}
}

// buildXrefStreamPDF assembles a PDF whose catalog and pages objects live in an object stream and whose xref is
// a cross-reference stream, optionally compressed with Flate and the PNG up predictor — the exact shape modern
// writers produce.
func buildXrefStreamPDF(t *testing.T, usePredictor bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	// Object stream 3 holds objects 1 (catalog) and 2 (pages).
	header := fmt.Sprintf("1 0 2 %d\n", len(catalogBody)+1)
	payload := header + catalogBody + "\n" + pagesBody
	off3 := buf.Len()
	fmt.Fprintf(&buf, "3 0 obj\n<< /Type /ObjStm /N 2 /First %d /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(header), len(payload), payload)
	off4 := buf.Len()
	rows := [][]byte{
		xrefStreamRow(0, 0, 255),  // object 0: free
		xrefStreamRow(2, 3, 0),    // object 1: in object stream 3, index 0
		xrefStreamRow(2, 3, 1),    // object 2: in object stream 3, index 1
		xrefStreamRow(1, off3, 0), // object 3: the object stream
		xrefStreamRow(1, off4, 0), // object 4: this xref stream
	}
	var entries []byte
	extra := ""
	if usePredictor {
		// PNG up filter, columns = 6: each row is a filter-type byte plus (row - previous row).
		prev := make([]byte, 6)
		for _, row := range rows {
			entries = append(entries, 2)
			for i, v := range row {
				entries = append(entries, v-prev[i])
			}
			prev = row
		}
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write(entries); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		entries = z.Bytes()
		extra = " /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 6 >>"
	} else {
		for _, row := range rows {
			entries = append(entries, row...)
		}
	}
	fmt.Fprintf(&buf, "4 0 obj\n<< /Type /XRef /Size 5 /W [1 4 1] /Root 1 0 R%s /Length %d >>\nstream\n",
		extra, len(entries))
	buf.Write(entries)
	fmt.Fprintf(&buf, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", off4)
	return buf.Bytes()
}

func TestXrefStreamWithObjectStream(t *testing.T) {
	for _, usePredictor := range []bool{false, true} {
		d := mustOpen(t, buildXrefStreamPDF(t, usePredictor))
		checkCatalog(t, d)
		if size, ok := d.GetInt(d.Trailer(), "Size"); !ok || size != 5 {
			t.Errorf("predictor=%v: trailer Size = %d", usePredictor, size)
		}
	}
}

// TestHybridXref builds a classic table whose trailer points at a supplemental xref stream via /XRefStm. The
// classic table's own entries win over the stream's, and objects only the stream knows about are found.
func TestHybridXref(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	off1 := buf.Len()
	buf.WriteString("1 0 obj\n" + catalogBody + "\nendobj\n")
	off2 := buf.Len()
	buf.WriteString("2 0 obj\n" + pagesBody + "\nendobj\n")
	off5 := buf.Len()
	buf.WriteString("5 0 obj\n(stream-only)\nendobj\n")
	// The xref stream claims objects 1 and 2 are free; the classic table's entries for them must win, while
	// entry 5 (absent from the classic table) comes from the stream.
	offStm := buf.Len()
	rows := make([]byte, 0, 36)
	rows = append(rows, xrefStreamRow(0, 0, 255)...)    // 0 free
	rows = append(rows, xrefStreamRow(0, 0, 0)...)      // 1 free (decoy; classic wins)
	rows = append(rows, xrefStreamRow(0, 0, 0)...)      // 2 free (decoy; classic wins)
	rows = append(rows, xrefStreamRow(1, offStm, 0)...) // 3: the stream itself
	rows = append(rows, xrefStreamRow(0, 0, 0)...)      // 4 free
	rows = append(rows, xrefStreamRow(1, off5, 0)...)   // 5: only the stream knows this object
	fmt.Fprintf(&buf, "3 0 obj\n<< /Type /XRef /Size 6 /W [1 4 1] /Root 1 0 R /Length %d >>\nstream\n", len(rows))
	buf.Write(rows)
	buf.WriteString("\nendstream\nendobj\n")
	xrefOff := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 3\n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \n", off1, off2)
	fmt.Fprintf(&buf, "trailer\n<< /Size 6 /Root 1 0 R /XRefStm %d >>\nstartxref\n%d\n%%%%EOF\n", offStm, xrefOff)
	d := mustOpen(t, buf.Bytes())
	checkCatalog(t, d)
	if s, ok := cos.AsString(d.LoadObject(5)); !ok || string(s) != "stream-only" {
		t.Errorf("object 5 = %q; the /XRefStm entries were not read", s)
	}
	// Object 2 resolves via the classic table (the stream marked it free).
	if _, ok := cos.AsDict(d.LoadObject(2)); !ok {
		t.Error("object 2 should resolve through the classic table")
	}
}

func TestRepairStartxrefZero(t *testing.T) {
	b := newBuilder()
	b.add(1, catalogBody)
	b.add(2, pagesBody)
	data := b.buf.Bytes()
	data = append(data, []byte("trailer\n<< /Size 3 /Root 1 0 R >>\nstartxref\n0\n%%EOF\n")...)
	d := mustOpen(t, data)
	checkCatalog(t, d)
}

func TestRepairNoTrailer(t *testing.T) {
	// No xref, no trailer, no startxref: the root must be found by scanning for a /Type /Catalog object.
	b := newBuilder()
	b.add(2, pagesBody)
	b.add(1, catalogBody)
	data := append(b.buf.Bytes(), []byte("%%EOF\n")...)
	d := mustOpen(t, data)
	checkCatalog(t, d)
}

func TestRepairBadOffsets(t *testing.T) {
	// Build a valid classic file, then corrupt every xref offset by prepending junk before the first object,
	// shifting the real offsets without updating the table.
	b := newBuilder()
	b.add(1, catalogBody)
	b.add(2, pagesBody)
	b.addStream(3, "", []byte("content here"))
	data := b.finishClassic("")
	shifted := append([]byte("% shifting comment\n"), data...)
	d := mustOpen(t, shifted)
	checkCatalog(t, d)
	stream, ok := cos.AsStream(d.LoadObject(3))
	if !ok {
		t.Fatal("object 3 did not survive repair")
	}
	if raw, err := d.StreamData(stream); err != nil || string(raw) != "content here" {
		t.Errorf("stream after repair = %q, %v", raw, err)
	}
}

func TestRepairPrefersLaterDefinitions(t *testing.T) {
	b := newBuilder()
	b.add(1, catalogBody)
	b.add(2, pagesBody)
	b.add(4, "(old)")
	b.add(4, "(new)")
	data := append(b.buf.Bytes(), []byte("%%EOF\n")...)
	d := mustOpen(t, data)
	if s, ok := cos.AsString(d.LoadObject(4)); !ok || string(s) != "new" {
		t.Errorf("object 4 = %q, want the later definition", s)
	}
}

func TestResolveCycle(t *testing.T) {
	b := newBuilder()
	b.add(1, catalogBody)
	b.add(2, pagesBody)
	b.add(5, "6 0 R")
	b.add(6, "5 0 R")
	b.add(7, "7 0 R")
	d := mustOpen(t, b.finishClassic(""))
	if _, isNull := d.Resolve(cos.Ref{Num: 5}).(cos.Null); !isNull {
		t.Error("expected Null for a reference cycle")
	}
	if _, isNull := d.Resolve(cos.Ref{Num: 7}).(cos.Null); !isNull {
		t.Error("expected Null for a self-referencing object")
	}
}

func TestOpenGarbageFails(t *testing.T) {
	for _, src := range []string{
		"%PDF-1.7\nnot a real pdf",
		"",
		"startxref\n0\n%%EOF",
		strings.Repeat("garbage ", 100),
	} {
		if _, err := cos.Open([]byte(src)); err == nil {
			t.Errorf("expected Open(%q...) to fail", src[:min(20, len(src))])
		}
	}
}

// TestOpenCorpus opens every committed corpus file — including the damaged trio and the encrypted set — and
// requires a usable root, then resolves every object and decodes every unencrypted stream to shake out parsing
// faults. Behavioral parity with the oracle is asserted separately by TestParity at the repository root.
func TestOpenCorpus(t *testing.T) {
	dir := filepath.Join("..", "..", "testfiles", "corpus")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".pdf" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			data, rerr := os.ReadFile(filepath.Join(dir, entry.Name()))
			if rerr != nil {
				t.Fatal(rerr)
			}
			d, derr := cos.Open(data)
			if derr != nil {
				t.Fatalf("Open: %v", derr)
			}
			encrypted := d.Trailer()["Encrypt"] != nil
			for _, num := range d.ObjectNums() {
				obj := d.LoadObject(num)
				if stream, ok := cos.AsStream(obj); ok && !encrypted {
					// Stream decode failures are not fatal here (some corpus streams use image-only
					// filters), but they must not panic.
					d.StreamData(stream) //nolint:errcheck // See above.
				}
			}
		})
	}
}

func TestImageFilterSplit(t *testing.T) {
	const hello = "Hello"
	const filterKey cos.Name = "Filter"
	const pdf = "%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R /Size 2 >>\nstartxref\n0\n%%EOF\n"
	d := mustOpen(t, []byte(pdf))
	hexPayload := []byte("48656c6c6f>")
	// A non-image prefix filter is applied; the split stops at the codec and hands back its parms.
	dict := cos.Dict{
		filterKey:     cos.Array{cos.Name("ASCIIHexDecode"), cos.Name("DCTDecode")},
		"DecodeParms": cos.Array{cos.Null{}, cos.Dict{"ColorTransform": cos.Integer(0)}},
	}
	data, codec, parms, err := d.ImageFilterSplit(dict, hexPayload)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != hello || codec != "DCTDecode" {
		t.Fatalf("data %q codec %q", data, codec)
	}
	if v, _ := d.GetInt(parms, "ColorTransform"); v != 0 || parms == nil {
		t.Fatalf("parms: %v", parms)
	}
	// The inline abbreviations /F and /DP are honored.
	dict = cos.Dict{
		"F":  cos.Array{cos.Name("AHx"), cos.Name("CCF")},
		"DP": cos.Array{cos.Null{}, cos.Dict{"K": cos.Integer(-1)}},
	}
	if data, codec, parms, err = d.ImageFilterSplit(dict, hexPayload); err != nil {
		t.Fatal(err)
	}
	if string(data) != hello || codec != "CCF" {
		t.Fatalf("abbreviated: data %q codec %q", data, codec)
	}
	if v, _ := d.GetInt(parms, "K"); v != -1 {
		t.Fatalf("abbreviated parms: %v", parms)
	}
	// No image codec: the chain fully decodes and the codec is empty.
	dict = cos.Dict{filterKey: cos.Name("ASCIIHexDecode")}
	if data, codec, _, err = d.ImageFilterSplit(dict, hexPayload); err != nil || string(data) != hello || codec != "" {
		t.Fatalf("plain chain: %q %q %v", data, codec, err)
	}
	// Filters listed after the codec are unreachable and ignored.
	dict = cos.Dict{filterKey: cos.Array{cos.Name("JPXDecode"), cos.Name("FlateDecode")}}
	if data, codec, _, err = d.ImageFilterSplit(dict, []byte{1, 2}); err != nil || codec != "JPXDecode" || len(data) != 2 {
		t.Fatalf("post-codec filters: %q %q %v", data, codec, err)
	}
	// No filters at all: raw samples pass through.
	if data, codec, _, err = d.ImageFilterSplit(cos.Dict{}, []byte{9}); err != nil || codec != "" || len(data) != 1 {
		t.Fatalf("no filters: %q %q %v", data, codec, err)
	}
}
