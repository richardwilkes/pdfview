// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// emptyPDF has a valid catalog whose page tree holds no pages. It opens successfully and reports a page count of zero.
const emptyPDF = `%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [] /Count 0 >>
endobj
trailer
<< /Size 3 /Root 1 0 R >>
startxref
0
%%EOF
`

// onePagePDF has a single, blank 200x200 page.
const onePagePDF = `%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>
endobj
trailer
<< /Size 4 /Root 1 0 R >>
startxref
0
%%EOF
`

// A document that opens with zero pages must be reported as having nothing to render rather than failing with the
// "invalid page number" that an unconditional RenderPage(0, ...) produces.
func TestExtractWithNoPages(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "empty.pdf")
	if err := os.WriteFile(path, []byte(emptyPDF), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extract(path, ""); err != nil {
		t.Fatalf("expected no error for a zero-page document, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "page0.png")); !os.IsNotExist(err) {
		t.Errorf("expected no page0.png to be written for a zero-page document, got %v", err)
	}
}

// The zero-page guard must not disturb the normal path: a document with pages still renders page 0 to page0.png.
func TestExtractWithOnePage(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "one.pdf")
	if err := os.WriteFile(path, []byte(onePagePDF), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extract(path, ""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "page0.png"))
	if err != nil {
		t.Fatalf("expected page0.png to be written, got %v", err)
	}
	if fi.Size() == 0 {
		t.Error("expected page0.png to be non-empty")
	}
}
