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
