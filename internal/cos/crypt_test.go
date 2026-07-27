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
	"fmt"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// markingDecryptor stands in for the standard security handler: it prefixes a marker instead of actually decrypting, so
// a test can tell exactly which strings and stream payloads were run through it.
type markingDecryptor struct {
	encryptsMetadata bool
}

func (m markingDecryptor) DecryptString(_, _ int, data []byte) []byte {
	return append([]byte("S:"), data...)
}

func (m markingDecryptor) DecryptStream(_, _ int, data []byte) []byte {
	return append([]byte("D:"), data...)
}

func (m markingDecryptor) EncryptsMetadata() bool {
	return m.encryptsMetadata
}

// xorDecryptor stands in for the standard security handler with the simplest cipher of the same shape: XOR against a
// constant. It is its own inverse, so a test enciphers a payload with the very call the document later uses to decipher
// it.
type xorDecryptor struct{}

func (xorDecryptor) DecryptString(_, _ int, data []byte) []byte { return xorCipher(data) }

func (xorDecryptor) DecryptStream(_, _ int, data []byte) []byte { return xorCipher(data) }

func (xorDecryptor) EncryptsMetadata() bool { return true }

func xorCipher(data []byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ 0x5a
	}
	return out
}

// buildEncryptedObjStmPDF assembles what every modern producer emits for an encrypted file: cross-reference data in a
// cross-reference stream, the catalog and page-tree root inside an object stream, and a trailer naming /Encrypt. The
// object stream's payload is enciphered with xorCipher, so the catalog is unreachable — the payload does not even
// decode — until a Decryptor is installed.
func buildEncryptedObjStmPDF() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	header := fmt.Sprintf("1 0 2 %d\n", len(catalogBody)+1)
	payload := xorCipher([]byte(header + catalogBody + "\n" + pagesBody))
	off3 := buf.Len()
	fmt.Fprintf(&buf, "3 0 obj\n<< /Type /ObjStm /N 2 /First %d /Length %d >>\nstream\n", len(header), len(payload))
	buf.Write(payload)
	buf.WriteString("\nendstream\nendobj\n")
	off5 := buf.Len()
	buf.WriteString("5 0 obj\n<< /Filter /Standard /V 1 /R 2 >>\nendobj\n")
	off4 := buf.Len()
	rows := make([]byte, 0, 36)
	rows = append(rows, xrefStreamRow(0, 0, 255)...)  // 0: free
	rows = append(rows, xrefStreamRow(2, 3, 0)...)    // 1: the catalog, inside object stream 3
	rows = append(rows, xrefStreamRow(2, 3, 1)...)    // 2: the page tree root, inside object stream 3
	rows = append(rows, xrefStreamRow(1, off3, 0)...) // 3: the object stream
	rows = append(rows, xrefStreamRow(1, off4, 0)...) // 4: this cross-reference stream
	rows = append(rows, xrefStreamRow(1, off5, 0)...) // 5: the encryption dictionary
	fmt.Fprintf(&buf, "4 0 obj\n<< /Type /XRef /Size 6 /W [1 4 1] /Root 1 0 R /Encrypt 5 0 R /Length %d >>\nstream\n",
		len(rows))
	buf.Write(rows)
	fmt.Fprintf(&buf, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", off4)
	return buf.Bytes()
}

// TestEncryptedCatalogInObjectStreamOpens pins the deferred root check. Nothing at this layer can decrypt anything
// until the layer above builds the security handler from the trailer's /Encrypt dictionary and installs it with
// SetDecryptor, which happens only after Open returns — so an encrypted document whose catalog lives in an object
// stream must open unvalidated rather than being rejected, with ValidateRoot running the check afterward.
func TestEncryptedCatalogInObjectStreamOpens(t *testing.T) {
	d, err := cos.Open(buildEncryptedObjStmPDF())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !d.Encrypted() {
		t.Fatal("Encrypted() = false, want true")
	}
	d.SetDecryptor(xorDecryptor{})
	if err = d.ValidateRoot(); err != nil {
		t.Fatalf("ValidateRoot after SetDecryptor: %v", err)
	}
	checkCatalog(t, d)
}

// TestValidateRootBeforeDecryptorRetriesAfterward checks both halves of the deferral: the check reports an unreachable
// root faithfully while the payload is still ciphertext (it is not a no-op that always passes), and the repair sweep it
// ran blind is re-armed by SetDecryptor, so the retry that follows can sweep the object stream it could not read.
func TestValidateRootBeforeDecryptorRetriesAfterward(t *testing.T) {
	d, err := cos.Open(buildEncryptedObjStmPDF())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err = d.ValidateRoot(); err == nil {
		t.Fatal("ValidateRoot succeeded before a decryptor was installed")
	}
	d.SetDecryptor(xorDecryptor{})
	if err = d.ValidateRoot(); err != nil {
		t.Fatalf("ValidateRoot after SetDecryptor: %v", err)
	}
	checkCatalog(t, d)
}

// TestDecryptSkipsUnencryptedStreams checks the two stream types that are stored in the clear: cross-reference streams
// always (ISO 32000-2 7.5.8.2), and metadata streams when the encryption dictionary carries /EncryptMetadata false
// (7.6.2). A cross-reference stream is exempt entirely, including the strings in its own dictionary; a metadata stream's
// exemption covers the payload only — strings in its dictionary are still encrypted.
func TestDecryptSkipsUnencryptedStreams(t *testing.T) {
	const metadataPayload = "<x:xmpmeta/>"
	const contentPayload = "BT ET"
	const xrefPayload = "not really an xref"
	b := newBuilder()
	b.add(1, catalogBody)
	b.add(2, pagesBody)
	b.addStream(3, "/Type /Metadata /Subtype /XML /Note (hi)", []byte(metadataPayload))
	b.addStream(4, "", []byte(contentPayload))
	b.addStream(5, "/Type /XRef /ID [(raw)]", []byte(xrefPayload))
	d := mustOpen(t, b.finishClassic(""))
	for _, encryptsMetadata := range []bool{true, false} {
		d.SetDecryptor(markingDecryptor{encryptsMetadata: encryptsMetadata})
		wantMetadata := metadataPayload
		if encryptsMetadata {
			wantMetadata = "D:" + metadataPayload
		}
		for _, tc := range []struct {
			want string
			num  int
		}{
			{num: 3, want: wantMetadata},
			{num: 4, want: "D:" + contentPayload},
			{num: 5, want: xrefPayload},
		} {
			stream, ok := cos.AsStream(d.LoadObject(tc.num))
			if !ok {
				t.Fatalf("EncryptMetadata=%v: object %d is not a stream", encryptsMetadata, tc.num)
			}
			data, err := d.StreamData(stream)
			if err != nil {
				t.Fatalf("EncryptMetadata=%v: object %d: %v", encryptsMetadata, tc.num, err)
			}
			if string(data) != tc.want {
				t.Errorf("EncryptMetadata=%v: object %d payload = %q, want %q", encryptsMetadata, tc.num, data,
					tc.want)
			}
			if tc.num == 3 {
				if note, _ := d.GetString(stream.Dict, "Note"); string(note) != "S:hi" {
					t.Errorf("EncryptMetadata=%v: metadata dictionary string = %q, want %q", encryptsMetadata, note,
						"S:hi")
				}
			}
			if tc.num == 5 {
				id, _ := cos.AsArray(stream.Dict["ID"])
				if len(id) != 1 {
					t.Fatalf("EncryptMetadata=%v: xref /ID = %v, want one element", encryptsMetadata, id)
				}
				if s, _ := cos.AsString(id[0]); string(s) != "raw" {
					t.Errorf("EncryptMetadata=%v: xref dictionary string = %q, want %q (must not be decrypted)",
						encryptsMetadata, s, "raw")
				}
			}
		}
	}
}
