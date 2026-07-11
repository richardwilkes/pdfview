package doc_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/doc"
)

// FuzzOpen drives the whole non-cryptographic engine surface with arbitrary bytes: document open (xref parsing
// and the repair scan), the page-tree walk (including geometry capture), the navigation layer (outline walk,
// link annotations, and through them destination arrays, named-destination lookup in both stores, and URI
// classification), resolution of every cross-referenced object, and stream decoding through the filter chain.
// Nothing here may panic or fail to terminate; errors are expected and fine. Every committed corpus file is a
// seed, so the classic-xref, xref-stream, object-stream, damaged, and encrypted shapes all mutate.
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
		d.Outline()
		n := min(d.PageCount(), 256)
		for i := range n {
			if _, perr := d.Page(i); perr != nil {
				t.Errorf("Page(%d) failed within PageCount %d: %v", i, d.PageCount(), perr)
			}
			if _, rerr := d.PageRef(i); rerr != nil {
				t.Errorf("PageRef(%d) failed within PageCount %d: %v", i, d.PageCount(), rerr)
			}
			if _, _, serr := d.PageSize(i); serr != nil {
				t.Errorf("PageSize(%d) failed within PageCount %d: %v", i, d.PageCount(), serr)
			}
			d.Links(i)
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

// FuzzCrypt drives the standard security handler with mutated encrypted documents: malformed /Encrypt
// dictionaries, truncated /O/U/OE/UE entries, and bogus V/R combinations. Seeded from the encrypted corpus, it
// exercises handler construction, authentication (key derivation for every revision), and per-object stream
// decryption. No input may panic or fail to terminate; a hostile document simply fails to open, fails to
// authenticate, or leaves streams undecodable.
func FuzzCrypt(f *testing.F) {
	corpusDir := filepath.Join("..", "..", "testfiles", "corpus")
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		f.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".pdf" || !strings.HasPrefix(name, "encrypted-") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(corpusDir, name))
		if rerr != nil {
			f.Fatal(rerr)
		}
		f.Add(data)
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		d, oerr := doc.Open(data)
		if oerr != nil {
			return
		}
		// Probing the password surface must never panic regardless of how broken the /Encrypt dictionary is.
		d.NeedsPassword()
		d.IsEncrypted()
		for _, pw := range []string{"", pwUser, pwOwner, "wrong"} {
			d.Authenticate(pw)
			// After each attempt, decoding every stream forces the decryptor over each object's payload.
			c := d.COS()
			for _, num := range c.ObjectNums() {
				if stream, ok := cos.AsStream(c.LoadObject(num)); ok {
					c.StreamData(stream) //nolint:errcheck // Decode errors on hostile input are expected.
				}
			}
		}
	})
}
