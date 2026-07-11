package doc_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/doc"
)

// FuzzOpen drives the whole M1 engine surface with arbitrary bytes: document open (xref parsing and the repair
// scan), the page-tree walk, resolution of every cross-referenced object, and stream decoding through the filter
// chain. Nothing here may panic or fail to terminate; errors are expected and fine. Every committed corpus file
// is a seed, so the classic-xref, xref-stream, object-stream, damaged, and encrypted shapes all mutate.
func FuzzOpen(f *testing.F) {
	corpusDir := filepath.Join("..", "..", "testfiles", "corpus")
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		f.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".pdf" {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(corpusDir, entry.Name()))
		if rerr != nil {
			f.Fatal(rerr)
		}
		f.Add(data)
	}
	f.Add([]byte("%PDF-1.7\nnot a real pdf"))
	f.Add([]byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\ntrailer\n<< /Root 1 0 R >>\nstartxref\n0\n%%EOF"))
	f.Fuzz(func(t *testing.T, data []byte) {
		d, oerr := doc.Open(data)
		if oerr != nil {
			return
		}
		n := min(d.PageCount(), 256)
		for i := range n {
			if _, perr := d.Page(i); perr != nil {
				t.Errorf("Page(%d) failed within PageCount %d: %v", i, d.PageCount(), perr)
			}
			if _, rerr := d.PageRef(i); rerr != nil {
				t.Errorf("PageRef(%d) failed within PageCount %d: %v", i, d.PageCount(), rerr)
			}
		}
		c := d.COS()
		for _, num := range c.ObjectNums() {
			obj := c.LoadObject(num)
			if stream, ok := cos.AsStream(obj); ok {
				c.StreamData(stream) //nolint:errcheck // Decode errors on hostile input are expected; panics are not.
			}
		}
	})
}
