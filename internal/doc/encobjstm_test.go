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
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/doc"
)

// The encryption side of the standard security handler at V1/R2 (40-bit RC4), written out here so a test can build a
// genuinely encrypted document rather than depending on a committed fixture. Only the pieces needed to produce /O, /U,
// the file encryption key, and per-object keys are implemented; internal/crypt is the decryption side of the same
// algorithms (ISO 32000-2 7.6.3 and 7.6.4.3).

// rc4Padding is the 32-byte password-padding string of ISO 32000-2 7.6.4.3.
var rc4Padding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// padPassword pads (or truncates) a password to exactly 32 bytes (Algorithm 2, step a).
func padPassword(pw string) []byte {
	out := make([]byte, 32)
	n := copy(out, pw)
	copy(out[n:], rc4Padding)
	return out
}

// rc4Apply runs RC4 over data. The cipher is its own inverse, so this is both the encryption performed here and the
// decryption the reader will perform.
func rc4Apply(t *testing.T, key, data []byte) []byte {
	t.Helper()
	c, err := rc4.NewCipher(key)
	if err != nil {
		t.Fatalf("rc4.NewCipher: %v", err)
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// r2OwnerValue computes the /O entry (Algorithm 3): the padded user password encrypted with a 40-bit key derived from
// the owner password.
func r2OwnerValue(t *testing.T, ownerPw, userPw string) []byte {
	t.Helper()
	key := md5.Sum(padPassword(ownerPw))
	return rc4Apply(t, key[:5], padPassword(userPw))
}

// r2FileKey computes the 40-bit file encryption key (Algorithm 2).
func r2FileKey(userPw string, o []byte, perm uint32, id0 []byte) []byte {
	sum := md5.New()
	sum.Write(padPassword(userPw))
	sum.Write(o)
	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], perm)
	sum.Write(p[:])
	sum.Write(id0)
	return sum.Sum(nil)[:5]
}

// r2UserValue computes the /U entry (Algorithm 4): the padding string encrypted with the file key.
func r2UserValue(t *testing.T, fileKey []byte) []byte {
	t.Helper()
	return rc4Apply(t, fileKey, rc4Padding)
}

// r2ObjectKey derives the per-object key for object (num, gen) from the file key (Algorithm 1).
func r2ObjectKey(fileKey []byte, num, gen int) []byte {
	sum := md5.New()
	sum.Write(fileKey)
	sum.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16), byte(gen), byte(gen >> 8)})
	return sum.Sum(nil)[:min(len(fileKey)+5, 16)]
}

// xrefStreamRow builds one cross-reference stream entry for W = [1 4 1].
func xrefStreamRow(typ byte, field2 int, field3 byte) []byte {
	return []byte{typ, byte(field2 >> 24), byte(field2 >> 16), byte(field2 >> 8), byte(field2), field3}
}

// allPermissions is the /P value with every permission bit set, as the unsigned 32-bit quantity the key derivation
// folds in; the file writes it as the signed integer -1.
const allPermissions = uint32(0xFFFFFFFF)

// encryptedObjStmPDF builds a complete one-page document encrypted with the standard security handler at V1/R2, whose
// catalog, page tree root, and page all live inside an object stream, with a cross-reference stream for its
// cross-reference data. That is what `qpdf --encrypt ... --object-streams=generate`, Acrobat, and LibreOffice all emit:
// the catalog cannot be read at all until the handler is built from /Encrypt and installed. rootNum names the object
// the trailer's /Root points at, so a caller can aim it somewhere that does not exist.
func encryptedObjStmPDF(t *testing.T, userPw, ownerPw string, rootNum int) []byte {
	t.Helper()
	const (
		catalogBody = "<< /Type /Catalog /Pages 3 0 R >>"
		pagesBody   = "<< /Type /Pages /Kids [4 0 R] /Count 1 >>"
		pageBody    = "<< /Type /Page /Parent 3 0 R /MediaBox [0 0 200 200] /Resources << >> /Contents 5 0 R >>"
		contentBody = "0 0 1 rg 20 20 160 160 re f"
	)
	id0 := []byte("0123456789abcdef")
	o := r2OwnerValue(t, ownerPw, userPw)
	fileKey := r2FileKey(userPw, o, allPermissions, id0)
	u := r2UserValue(t, fileKey)
	// Object stream 1 carries objects 2 (catalog), 3 (page tree root), and 4 (the page). Its header's offsets are
	// relative to /First, which is the header's own length.
	var header, bodies strings.Builder
	for i, body := range []string{catalogBody, pagesBody, pageBody} {
		fmt.Fprintf(&header, "%d %d\n", i+2, bodies.Len())
		bodies.WriteString(body)
		bodies.WriteByte('\n')
	}
	stmPayload := rc4Apply(t, r2ObjectKey(fileKey, 1, 0), []byte(header.String()+bodies.String()))
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	off1 := buf.Len()
	fmt.Fprintf(&buf, "1 0 obj\n<< /Type /ObjStm /N 3 /First %d /Length %d >>\nstream\n", header.Len(),
		len(stmPayload))
	buf.Write(stmPayload)
	buf.WriteString("\nendstream\nendobj\n")
	off5 := buf.Len()
	content := rc4Apply(t, r2ObjectKey(fileKey, 5, 0), []byte(contentBody))
	fmt.Fprintf(&buf, "5 0 obj\n<< /Length %d >>\nstream\n", len(content))
	buf.Write(content)
	buf.WriteString("\nendstream\nendobj\n")
	// The encryption dictionary's own strings are never encrypted (ISO 32000-2 7.6.2), so /O and /U go in as-is.
	off6 := buf.Len()
	fmt.Fprintf(&buf, "6 0 obj\n<< /Filter /Standard /V 1 /R 2 /O <%x> /U <%x> /P -1 >>\nendobj\n", o, u)
	off7 := buf.Len()
	rows := make([]byte, 0, 48)
	rows = append(rows, xrefStreamRow(0, 0, 255)...)  // 0: free
	rows = append(rows, xrefStreamRow(1, off1, 0)...) // 1: the object stream
	rows = append(rows, xrefStreamRow(2, 1, 0)...)    // 2: the catalog, inside object stream 1
	rows = append(rows, xrefStreamRow(2, 1, 1)...)    // 3: the page tree root, inside object stream 1
	rows = append(rows, xrefStreamRow(2, 1, 2)...)    // 4: the page, inside object stream 1
	rows = append(rows, xrefStreamRow(1, off5, 0)...) // 5: the content stream
	rows = append(rows, xrefStreamRow(1, off6, 0)...) // 6: the encryption dictionary
	rows = append(rows, xrefStreamRow(1, off7, 0)...) // 7: this cross-reference stream
	fmt.Fprintf(&buf, "7 0 obj\n<< /Type /XRef /Size 8 /W [1 4 1] /Root %d 0 R /Encrypt 6 0 R /ID [<%x> <%x>] "+
		"/Length %d >>\nstream\n", rootNum, id0, id0, len(rows))
	buf.Write(rows)
	fmt.Fprintf(&buf, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", off7)
	return buf.Bytes()
}

// catalogRootObj is the object number of the catalog inside encryptedObjStmPDF's object stream.
const catalogRootObj = 2

// TestEncryptedObjStmOpensWithEmptyUserPassword covers the common modern shape end to end: encryption plus object
// streams, unlocked by the empty user password the handler probes at open. The catalog is ciphertext inside an object
// stream when cos.Open runs, so the document opens only if the root check is deferred until the decryptor exists.
func TestEncryptedObjStmOpensWithEmptyUserPassword(t *testing.T) {
	d, err := doc.Open(encryptedObjStmPDF(t, "", pwOwner, catalogRootObj))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !d.IsEncrypted() {
		t.Error("IsEncrypted() = false, want true")
	}
	if d.NeedsPassword() {
		t.Fatal("NeedsPassword() = true; the empty user password should have unlocked the document")
	}
	if count := d.PageCount(); count != 1 {
		t.Fatalf("PageCount() = %d, want 1", count)
	}
	if content := decodePageContent(t, d, 0); !bytes.Contains(content, []byte("re f")) {
		t.Errorf("decoded content stream = %q, want the painted rectangle", clip(content))
	}
}

// TestEncryptedObjStmUnlocksAfterAuthentication is the same document behind a real user password. It must open locked
// rather than failing — the page tree is unreachable until the key arrives, so there are no pages to report — and
// authenticating must then produce them, which requires the root check to be retried once the key is in hand.
func TestEncryptedObjStmUnlocksAfterAuthentication(t *testing.T) {
	for _, password := range []string{pwUser, pwOwner} {
		t.Run(password, func(t *testing.T) {
			d, err := doc.Open(encryptedObjStmPDF(t, pwUser, pwOwner, catalogRootObj))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !d.NeedsPassword() {
				t.Fatal("NeedsPassword() = false, want true")
			}
			if count := d.PageCount(); count != 0 {
				t.Errorf("PageCount() = %d before authentication, want 0 (the catalog is still ciphertext)", count)
			}
			if status := d.Authenticate(password); status == 0 {
				t.Fatalf("Authenticate(%q) failed", password)
			}
			if count := d.PageCount(); count != 1 {
				t.Fatalf("PageCount() = %d after authentication, want 1", count)
			}
			if content := decodePageContent(t, d, 0); !bytes.Contains(content, []byte("re f")) {
				t.Errorf("decoded content stream = %q, want the painted rectangle", clip(content))
			}
		})
	}
}

// TestEncryptedNoUsableRootStillFails guards the deferral against becoming a blanket exemption: an encrypted document
// whose /Root names an object that does not exist has no usable root even with the key in hand, and must still fail to
// open.
func TestEncryptedNoUsableRootStillFails(t *testing.T) {
	if _, err := doc.Open(encryptedObjStmPDF(t, "", pwOwner, 99)); err == nil {
		t.Fatal("Open succeeded for an encrypted document whose /Root names a nonexistent object")
	}
}
