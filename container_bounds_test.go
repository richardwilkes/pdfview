// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview_test

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview"
)

// A container's element count is not bounded by the file's size: internal/filter lets a stream decode to max(64 MB,
// 256x input), so a few tens of kilobytes buy tens of millions of array entries. These tests reproduce the three shapes
// that exploits and hold each to a bound a small file could justify. The ceilings are generous enough not to depend on
// allocator behavior; each file drove hundreds of megabytes to over a gigabyte before the caps.
const (
	// arrayElements is the element count each hostile container claims: at two payload bytes and sixteen bytes of
	// interface slot per entry, ~24 MB of decoded content and, with append growth, ~380 MB of array allocation.
	arrayElements = 12 << 20
	// allocCeiling bounds one operation's cumulative allocation; the decoded payload is most of what remains under the
	// caps.
	allocCeiling = 192 << 20
	// liveCeiling bounds what may still be live once the operation returns with the document held open.
	liveCeiling = 128 << 20
)

// flate returns the zlib-compressed form of s, the encoding /FlateDecode names.
func flate(s string) []byte {
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zw.Write([]byte(s)) //nolint:errcheck // Writing to a bytes.Buffer cannot fail.
	zw.Close()          //nolint:errcheck // See above.
	return compressed.Bytes()
}

// allocatedDuring reports the bytes fn allocated in total, so a transient peak counts even when nothing survives it.
func allocatedDuring(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func size(n uint64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
}

// hugeTJArrayPDF builds a one-page file whose /Contents is a single flate-compressed text-showing operator with an
// array of arrayElements entries.
func hugeTJArrayPDF() []byte {
	content := flate("BT /F1 1 Tf [" + strings.Repeat("1 ", arrayElements) + "] TJ ET")
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	buf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>\nendobj\n")
	fmt.Fprintf(&buf, "4 0 obj\n<< /Filter /FlateDecode /Length %d >>\nstream\n", len(content))
	buf.Write(content)
	buf.WriteString("\nendstream\nendobj\n")
	buf.WriteString("trailer\n<< /Root 1 0 R /Size 5 >>\nstartxref\n0\n%%EOF\n")
	return buf.Bytes()
}

// TestContentStreamArrayFloodBounded covers the content-stream side: a TJ operand is one object charged one unit of the
// work budget however large it is, so a 39 KB file with a 20M-entry operand drove 623 MB of live heap through one
// RenderPage.
func TestContentStreamArrayFloodBounded(t *testing.T) {
	data := hugeTJArrayPDF()
	used := allocatedDuring(func() {
		doc, err := pdfview.New(data, 0)
		if err != nil {
			t.Error(err)
			return
		}
		defer doc.Release()
		if _, err = doc.RenderPage(0, 72, 0, ""); err != nil {
			t.Errorf("RenderPage: %v", err)
		}
	})
	if t.Failed() {
		return
	}
	if used > allocCeiling {
		t.Fatalf("rendering a %s file with a %d element TJ array allocated %s, want at most %s",
			size(uint64(len(data))), arrayElements, size(used), size(allocCeiling))
	}
}

// hugeObjStmArrayPDF builds a one-page file whose /Annots is an indirect reference into an object stream holding a
// single array of arrayElements entries. No xref is written; the COS repair scan indexes the object stream.
func hugeObjStmArrayPDF() []byte {
	inner := "6 0\n[" + strings.Repeat("1 ", arrayElements) + "]"
	payload := flate(inner)
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	buf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Annots 6 0 R >>\nendobj\n")
	fmt.Fprintf(&buf, "4 0 obj\n<< /Type /ObjStm /N 1 /First 4 /Filter /FlateDecode /Length %d >>\nstream\n",
		len(payload))
	buf.Write(payload)
	buf.WriteString("\nendstream\nendobj\n")
	buf.WriteString("trailer\n<< /Root 1 0 R /Size 7 >>\nstartxref\n0\n%%EOF\n")
	return buf.Bytes()
}

// TestObjectStreamArrayFloodBounded covers the COS side, which is worse: a parsed object lives in the document's object
// cache for the whole Document, not one render. The measured 39 KB file left ~364 MB live after the render returned.
func TestObjectStreamArrayFloodBounded(t *testing.T) {
	data := hugeObjStmArrayPDF()
	base := liveHeap()
	doc, err := pdfview.New(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()
	if _, err = doc.RenderPage(0, 72, 0, ""); err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	// The document is deliberately still open: what matters is what its caches hold onto.
	if live := liveHeap(); live > base+liveCeiling {
		t.Fatalf("a %s file with a %d element array in an object stream left %s live (from %s), want at most %s more",
			size(uint64(len(data))), arrayElements, size(live), size(base), size(liveCeiling))
	}
	runtime.KeepAlive(doc)
}

// hugeIndexXrefPDF builds a file whose cross-reference stream claims rows entries through /Index [0 rows], each a
// three-byte /W [1 1 1] row that compresses to nothing.
func hugeIndexXrefPDF(rows int) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	off1 := buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off2 := buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	off3 := buf.Len()
	buf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>\nendobj\n")
	entries := make([]byte, 3*rows)
	for i, off := range []int{0, off1, off2, off3} {
		entries[3*i], entries[3*i+1] = 1, byte(off)
	}
	entries[0], entries[2] = 0, 0xff // Object 0 is the head of the free list.
	payload := flate(string(entries))
	xrefOff := buf.Len()
	fmt.Fprintf(&buf, "4 0 obj\n<< /Type /XRef /Size %d /W [1 1 1] /Index [0 %d] /Root 1 0 R /Filter /FlateDecode "+
		"/Length %d >>\nstream\n", rows, rows, len(payload))
	buf.Write(payload)
	buf.WriteString("\nendstream\nendobj\n")
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

// TestXrefStreamIndexFloodBounded covers the cross-reference stream: a classic table pays 20 file bytes per entry, but
// a stream row costs only its decoded payload, so a 49 KB file with /Index [0 16777216] made New allocate ~1.9 GB.
func TestXrefStreamIndexFloodBounded(t *testing.T) {
	data := hugeIndexXrefPDF(4 << 20)
	var doc *pdfview.Document
	used := allocatedDuring(func() {
		var err error
		if doc, err = pdfview.New(data, 0); err != nil {
			t.Error(err)
		}
	})
	if t.Failed() {
		return
	}
	defer doc.Release()
	if used > allocCeiling {
		t.Fatalf("opening a %s file claiming 4M cross-reference entries allocated %s, want at most %s",
			size(uint64(len(data))), size(used), size(allocCeiling))
	}
	// The entries that were read still describe the document.
	if got := doc.PageCount(); got != 1 {
		t.Fatalf("PageCount = %d, want 1", got)
	}
	if _, err := doc.RenderPage(0, 72, 0, ""); err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
}

// TestOverRangeMediaBoxStillRenders pins that a /MediaBox of 1e39 (a legal PDF integer that is ±Inf as the float32
// rectFromObj stores) falls back to US Letter instead of reaching the engine seam and failing every render with
// ErrUnableToCreateImage.
func TestOverRangeMediaBoxStillRenders(t *testing.T) {
	huge := "1" + strings.Repeat("0", 39)
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	fmt.Fprintf(&buf, "3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] >>\nendobj\n", huge, huge)
	buf.WriteString("trailer\n<< /Root 1 0 R /Size 4 >>\nstartxref\n0\n%%EOF\n")
	doc, err := pdfview.New(buf.Bytes(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Release()
	page, err := doc.RenderPage(0, 72, 0, "")
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	// At 72 dpi a point is a pixel, so the fallback US Letter box renders 612x792.
	if got := page.Image.Bounds(); got.Dx() != 612 || got.Dy() != 792 {
		t.Fatalf("rendered %v, want the default MediaBox's 612x792", got)
	}
}
