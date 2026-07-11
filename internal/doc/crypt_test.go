// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package doc_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/doc"
	"github.com/richardwilkes/pdfview/internal/testsupport"
)

// pwUser and pwOwner are the passwords the encrypted corpus was generated with.
const (
	pwUser  = "user"
	pwOwner = "owner"
)

// encryptedCorpus lists the encrypted corpus files and the password that unlocks each for content decryption.
var encryptedCorpus = []struct {
	file     string
	password string
}{
	{"encrypted-r2-rc4.pdf", pwUser},
	{"encrypted-r3-rc4.pdf", pwUser},
	{"encrypted-r4-rc4.pdf", pwUser},
	{"encrypted-r4-aes.pdf", pwUser},
	{"encrypted-r6-aes.pdf", pwUser},
	{"encrypted-r6-empty-user.pdf", ""},
}

// TestDecryptContentStream authenticates each encrypted corpus file and decodes its page content stream,
// confirming the file key, per-object key, and RC4/AES decryption all line up: the plaintext must be a valid
// content stream that paints the "Hello" the goldens search for.
func TestDecryptContentStream(t *testing.T) {
	for _, tc := range encryptedCorpus {
		t.Run(tc.file, func(t *testing.T) {
			data := readCorpus(t, tc.file)
			d, err := doc.Open(data)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if status := d.Authenticate(tc.password); status == 0 {
				t.Fatalf("Authenticate(%q) failed", tc.password)
			}
			content := decodePageContent(t, d, 0)
			if !bytes.Contains(content, []byte("Hello")) {
				t.Errorf("decoded content stream does not contain the expected text; got %q", clip(content))
			}
		})
	}
}

// TestDecryptWrongPasswordLeavesContentEncrypted confirms that without the key the content stream does not
// decode to plaintext — a guard that the "Hello" assertion above is really exercising decryption.
func TestDecryptWrongPasswordLeavesContentEncrypted(t *testing.T) {
	data := readCorpus(t, "encrypted-r4-aes.pdf")
	d, err := doc.Open(data)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if status := d.Authenticate("wrong"); status != 0 {
		t.Fatalf("Authenticate(\"wrong\") = %d, want 0", status)
	}
	// Without the key the raw payload is still ciphertext, so the /Filter chain either fails outright or yields
	// garbage. Either way the plaintext "Hello" must not appear.
	page, err := d.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	c := d.COS()
	for _, stream := range contentStreams(c, page) {
		if decoded, derr := c.StreamData(stream); derr == nil && bytes.Contains(decoded, []byte("Hello")) {
			t.Errorf("content decoded to plaintext without a valid password: %q", clip(decoded))
		}
	}
}

// TestNeedsPasswordMatchesGoldens checks NeedsPassword against every golden's recorded requiresAuth.
func TestNeedsPasswordMatchesGoldens(t *testing.T) {
	goldens, err := testsupport.LoadGoldens(filepath.Join("..", "..", "testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	for _, golden := range goldens {
		t.Run(golden.Name, func(t *testing.T) {
			d, derr := doc.Open(readCorpus(t, golden.Truth.File))
			if derr != nil {
				t.Fatalf("Open: %v", derr)
			}
			if got := d.NeedsPassword(); got != golden.Truth.RequiresAuth {
				t.Errorf("NeedsPassword = %v, oracle says %v", got, golden.Truth.RequiresAuth)
			}
		})
	}
}

// TestAuthBitsMatchGoldens replays every recorded Authenticate attempt on a fresh document and checks the
// status bits against the oracle — the M2 exit-criteria parity table (corpus × {"", user, owner, wrong}).
func TestAuthBitsMatchGoldens(t *testing.T) {
	goldens, err := testsupport.LoadGoldens(filepath.Join("..", "..", "testfiles", "goldens"))
	if err != nil {
		t.Fatal(err)
	}
	var attempts int
	for _, golden := range goldens {
		t.Run(golden.Name, func(t *testing.T) {
			for _, attempt := range golden.Truth.Auth {
				d, derr := doc.Open(readCorpus(t, golden.Truth.File))
				if derr != nil {
					t.Fatalf("Open: %v", derr)
				}
				if got := int(d.Authenticate(attempt.Password)); got != attempt.Status {
					t.Errorf("Authenticate(%q) = %d, oracle says %d", attempt.Password, got, attempt.Status)
				}
				attempts++
			}
		})
	}
	t.Logf("checked %d authentication attempts across %d files", attempts, len(goldens))
}

func readCorpus(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testfiles", "corpus", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// decodePageContent concatenates and decodes the content stream(s) of the given 0-based page.
func decodePageContent(t *testing.T, d *doc.Document, pageNumber int) []byte {
	t.Helper()
	page, err := d.Page(pageNumber)
	if err != nil {
		t.Fatalf("Page(%d): %v", pageNumber, err)
	}
	c := d.COS()
	var out []byte
	for _, stream := range contentStreams(c, page) {
		decoded, derr := c.StreamData(stream)
		if derr != nil {
			t.Fatalf("StreamData: %v", derr)
		}
		out = append(out, decoded...)
		out = append(out, '\n')
	}
	return out
}

// contentStreams returns the page's /Contents as a slice of streams (it may be a single stream or an array).
func contentStreams(c *cos.Document, page cos.Dict) []*cos.Stream {
	switch v := c.Resolve(page["Contents"]).(type) {
	case *cos.Stream:
		return []*cos.Stream{v}
	case cos.Array:
		streams := make([]*cos.Stream, 0, len(v))
		for _, e := range v {
			if s, ok := cos.AsStream(c.Resolve(e)); ok {
				streams = append(streams, s)
			}
		}
		return streams
	default:
		return nil
	}
}

// clip shortens a byte slice for error messages.
func clip(b []byte) []byte {
	if len(b) > 96 {
		return b[:96]
	}
	return b
}
