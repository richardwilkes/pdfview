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
	"fmt"
	"testing"

	"github.com/richardwilkes/pdfview/internal/doc"
)

// encDestName is the destination name the encrypted name tree below is keyed on. It appears twice in the file, once in
// the annotation's /GoTo action and once as the name tree's key, encrypted under a different per-object key each time.
const encDestName = "Chapter1"

// encryptedNamedDestPDF builds a two-page document encrypted with the standard security handler at V1/R2, whose catalog
// and page tree sit directly in the file (readable before authentication, since only strings and streams are
// enciphered) and whose named destination lives in a /Names → /Dests name tree. The tree's key and the /GoTo action's
// /D naming it are both encrypted strings, so a name tree flattened before the file key arrives is keyed on ciphertext.
// Page 0 carries the link; page 1 is its target.
func encryptedNamedDestPDF(t *testing.T, userPw, ownerPw string) []byte {
	t.Helper()
	id0 := []byte("0123456789abcdef")
	o := r2OwnerValue(t, ownerPw, userPw)
	fileKey := r2FileKey(userPw, o, allPermissions, id0)
	u := r2UserValue(t, fileKey)
	// The same plaintext name, enciphered under each containing object's own key, so the two ciphertexts differ.
	destInAnnot := rc4Apply(t, r2ObjectKey(fileKey, 5, 0), []byte(encDestName))
	destInTree := rc4Apply(t, r2ObjectKey(fileKey, 7, 0), []byte(encDestName))
	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /Names 6 0 R >>",
		2: twoKidPages,
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Annots [5 0 R] >>",
		4: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
		5: fmt.Sprintf("<< /Type /Annot /Subtype /Link /Rect [10 10 20 20] /A << /S /GoTo /D <%x> >> >>", destInAnnot),
		6: "<< /Dests 7 0 R >>",
		7: fmt.Sprintf("<< /Names [<%x> [4 0 R /XYZ 20 180 0]] >>", destInTree),
		// The encryption dictionary's own strings are never encrypted (ISO 32000-2 7.6.2), so /O and /U go in as-is.
		8: fmt.Sprintf("<< /Filter /Standard /V 1 /R 2 /O <%x> /U <%x> /P -1 >>", o, u),
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(bodies)+1)
	for num := 1; num <= len(bodies); num++ {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, bodies[num])
	}
	xrefOff := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for num := 1; num < len(offsets); num++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[num])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Encrypt 8 0 R /ID [<%x> <%x>] >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets), id0, id0, xrefOff)
	return buf.Bytes()
}

// TestNamedDestIndexRebuiltAfterAuthentication pins that Authenticate drops the named-destination index. Nothing gates
// lookups on a password, so a lookup on the locked document flattens the /Names → /Dests tree keyed on ciphertext;
// kept, that index would miss every name for the life of the document.
func TestNamedDestIndexRebuiltAfterAuthentication(t *testing.T) {
	for _, password := range []string{pwUser, pwOwner} {
		t.Run(password, func(t *testing.T) {
			d, err := doc.Open(encryptedNamedDestPDF(t, pwUser, pwOwner))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !d.NeedsPassword() {
				t.Fatal("NeedsPassword() = false, want true")
			}
			// The page tree is never encrypted, so this lookup happens on the locked document and builds the poisoned
			// index. It must miss: the two ciphertexts were produced under different per-object keys.
			if count := d.PageCount(); count != 2 {
				t.Fatalf("PageCount() = %d before authentication, want 2", count)
			}
			locked := d.Links(0)
			if len(locked) != 1 {
				t.Fatalf("got %d links before authentication, want 1", len(locked))
			}
			if locked[0].Page != -1 {
				t.Fatalf("named dest resolved to page %d on a locked document, want -1; the test is not building the "+
					"stale index it means to", locked[0].Page)
			}
			if status := d.Authenticate(password); status == 0 {
				t.Fatalf("Authenticate(%q) failed", password)
			}
			links := d.Links(0)
			if len(links) != 1 {
				t.Fatalf("got %d links after authentication, want 1", len(links))
			}
			// Page 1, /XYZ 20 180 on a 200x200 page: (20, 200-180) top-left. Rect [10 10 20 20] maps to [10 180 20 190].
			checkLink(t, links[0], [4]float32{10, 180, 20, 190}, 1, 20, 20)
		})
	}
}

// TestNamedDestIndexSurvivesFailedAuthentication guards the other side: a wrong password changes nothing, so the index
// built while locked is still the correct one for the still-locked document and dropping it would only cost a rewalk.
func TestNamedDestIndexSurvivesFailedAuthentication(t *testing.T) {
	d, err := doc.Open(encryptedNamedDestPDF(t, pwUser, pwOwner))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if links := d.Links(0); len(links) != 1 || links[0].Page != -1 {
		t.Fatalf("links before authentication = %+v, want one unresolved link", links)
	}
	if status := d.Authenticate("wrong"); status != 0 {
		t.Fatalf("Authenticate(\"wrong\") = %d, want 0", status)
	}
	if links := d.Links(0); len(links) != 1 || links[0].Page != -1 {
		t.Errorf("links after a failed authentication = %+v, want one unresolved link", links)
	}
}
