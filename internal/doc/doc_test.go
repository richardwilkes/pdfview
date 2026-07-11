package doc_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// pdf assembles a minimal PDF from object bodies keyed by object number. No xref is written; the COS layer's
// repair scan indexes the objects, which is itself exercised constantly this way.
func pdf(objects map[int]string) []byte {
	var sb strings.Builder
	sb.WriteString("%PDF-1.7\n")
	for num := range 100 {
		if body, ok := objects[num]; ok {
			fmt.Fprintf(&sb, "%d 0 obj\n%s\nendobj\n", num, body)
		}
	}
	sb.WriteString("%%EOF\n")
	return []byte(sb.String())
}

func mustOpen(t *testing.T, data []byte) *doc.Document {
	t.Helper()
	d, err := doc.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const (
	catalogObj = "<< /Type /Catalog /Pages 2 0 R >>"
	pageObj    = "<< /Type /Page >>"
)

func TestNestedPageTree(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 3 >>",
		3: "<< /Type /Pages /Parent 2 0 R /Kids [4 0 R 5 0 R] /Count 2 >>",
		4: "<< /Type /Page /Parent 3 0 R /Label (first) >>",
		5: "<< /Type /Page /Parent 3 0 R /Label (second) >>",
		6: "<< /Type /Page /Parent 2 0 R /Label (third) >>",
	}))
	if got := d.PageCount(); got != 3 {
		t.Fatalf("PageCount = %d, want 3", got)
	}
	for i, want := range []string{"first", "second", "third"} {
		page, err := d.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		if label, _ := d.COS().GetString(page, "Label"); string(label) != want {
			t.Errorf("page %d Label = %q, want %q", i, label, want)
		}
	}
	if _, err := d.Page(3); err == nil {
		t.Error("expected an error for a page number past the end")
	}
	if _, err := d.Page(-1); err == nil {
		t.Error("expected an error for a negative page number")
	}
	if ref, err := d.PageRef(2); err != nil || ref.Num != 6 {
		t.Errorf("PageRef(2) = %v, %v", ref, err)
	}
}

func TestPageTreeCycle(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Pages /Kids [2 0 R 4 0 R] /Count 1 >>", // Points back at its parent.
		4: pageObj,
	}))
	if got := d.PageCount(); got != 1 {
		t.Errorf("PageCount = %d, want 1", got)
	}
}

func TestPageTreeDuplicateKid(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: "<< /Type /Pages /Kids [3 0 R 3 0 R 4 0 R] /Count 3 >>",
		3: pageObj,
		4: pageObj,
	}))
	// A kid listed twice is visited once; the bogus /Count 3 is not trusted.
	if got := d.PageCount(); got != 2 {
		t.Errorf("PageCount = %d, want 2", got)
	}
}

func TestPageTreeDepthLimit(t *testing.T) {
	objects := map[int]string{
		1: catalogObj,
		2: "<< /Type /Pages /Kids [3 0 R 10 0 R] /Count 2 >>",
		3: "<< /Type /Page /Label (shallow) >>",
	}
	// A chain of 80 nested Pages nodes with a single page at the bottom, beyond the depth cap.
	for i := range 79 {
		objects[10+i] = fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", 11+i)
	}
	objects[89] = "<< /Type /Page /Label (deep) >>"
	d := mustOpen(t, pdf(objects))
	if got := d.PageCount(); got != 1 {
		t.Errorf("PageCount = %d, want 1 (the deep page is beyond the depth cap)", got)
	}
}

func TestMissingTypesInferred(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{
		1: catalogObj,
		2: "<< /Kids [3 0 R 4 0 R] >>",     // Interior node without /Type: inferred from /Kids.
		3: "<< /MediaBox [0 0 100 100] >>", // Leaf without /Type.
		4: "<< /Type /Pages /Count 5 >>",   // Pages node with no kids contributes nothing.
	}))
	if got := d.PageCount(); got != 1 {
		t.Errorf("PageCount = %d, want 1", got)
	}
}

func TestNoPagesOpensWithZeroPages(t *testing.T) {
	d := mustOpen(t, pdf(map[int]string{1: "<< /Type /Catalog >>"}))
	if got := d.PageCount(); got != 0 {
		t.Errorf("PageCount = %d, want 0", got)
	}
}

// TestCorpusPageCounts checks the walked page count of every corpus file against the oracle-recorded value in
// its golden. This includes the encrypted set: page-tree dictionaries are not encrypted, so counting works
// before authentication, exactly as the oracle did.
func TestCorpusPageCounts(t *testing.T) {
	goldens, err := testsupport.LoadGoldens(filepath.Join("..", "..", "testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	if len(goldens) == 0 {
		t.Fatal("no goldens found")
	}
	for _, golden := range goldens {
		t.Run(golden.Name, func(t *testing.T) {
			data, rerr := os.ReadFile(filepath.Join("..", "..", "testfiles", "corpus", golden.Truth.File))
			if rerr != nil {
				t.Fatal(rerr)
			}
			d, derr := doc.Open(data)
			if derr != nil {
				t.Fatalf("Open: %v", derr)
			}
			if got := d.PageCount(); got != golden.Truth.PageCount {
				t.Errorf("PageCount = %d, oracle says %d", got, golden.Truth.PageCount)
			}
		})
	}
}
