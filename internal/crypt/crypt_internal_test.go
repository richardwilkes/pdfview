// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package crypt

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// TestConfigureCapsKeyLen checks that a hostile /Length beyond 128 bits is clamped to a 16-byte key for R<=4. The
// RC4/AESV2 file key derives from a 16-byte MD5 digest, so an uncapped keyLen (up to 32 for /Length 256) would slice
// that digest out of range and panic during the empty-password probe at document open.
func TestConfigureCapsKeyLen(t *testing.T) {
	for _, length := range []int64{256, 192, 136} {
		h := &Handler{r: 4, o: make([]byte, 32), u: make([]byte, 32)}
		encDict := cos.Dict{"Length": cos.Integer(length)}
		if err := h.configure(&cos.Document{}, encDict, 2); err != nil {
			t.Fatalf("configure(/Length %d): %v", length, err)
		}
		if h.keyLen != 16 {
			t.Errorf("/Length %d yields keyLen %d, want 16 (capped)", length, h.keyLen)
		}
		// Before the cap these derivations sliced the 16-byte digest with an out-of-range index and panicked.
		h.deriveRC4([]byte(""))
	}

	// A normal /Length 128 still maps to a 16-byte key, and the R2 default of 40 bits to 5 bytes.
	h := &Handler{r: 4, o: make([]byte, 32), u: make([]byte, 32)}
	if err := h.configure(&cos.Document{}, cos.Dict{"Length": cos.Integer(128)}, 2); err != nil {
		t.Fatalf("configure(/Length 128): %v", err)
	}
	if h.keyLen != 16 {
		t.Errorf("/Length 128 yields keyLen %d, want 16", h.keyLen)
	}
	h2 := &Handler{r: 2, o: make([]byte, 32), u: make([]byte, 32)}
	if err := h2.configure(&cos.Document{}, cos.Dict{}, 1); err != nil {
		t.Fatalf("configure(default): %v", err)
	}
	if h2.keyLen != 5 {
		t.Errorf("default /Length yields keyLen %d, want 5", h2.keyLen)
	}
}

// TestEncryptsMetadata checks that /EncryptMetadata reaches the COS layer, which consults it to leave a /Type
// /Metadata stream undecrypted (ISO 32000-2 7.6.2). The entry defaults to true when absent or malformed.
func TestEncryptsMetadata(t *testing.T) {
	for _, tc := range []struct {
		entry cos.Object
		name  string
		want  bool
	}{
		{name: "absent", entry: nil, want: true},
		{name: "true", entry: cos.Boolean(true), want: true},
		{name: "false", entry: cos.Boolean(false), want: false},
		{name: "not a boolean", entry: cos.Integer(0), want: true},
	} {
		encDict := cos.Dict{
			"Filter": cos.Name("Standard"),
			"V":      cos.Integer(2),
			"R":      cos.Integer(4),
			"O":      cos.String(make([]byte, 32)),
			"U":      cos.String(make([]byte, 32)),
		}
		if tc.entry != nil {
			encDict["EncryptMetadata"] = tc.entry
		}
		h, err := New(&cos.Document{}, encDict)
		if err != nil {
			t.Fatalf("%s: New: %v", tc.name, err)
		}
		if got := h.EncryptsMetadata(); got != tc.want {
			t.Errorf("%s: EncryptsMetadata() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPadPassword checks the password padding of Algorithm 2: an empty password is exactly the padding string, and a
// full-length password is truncated to 32 bytes with no padding appended.
func TestPadPassword(t *testing.T) {
	if got := padPassword(nil); !bytes.Equal(got, padding) {
		t.Errorf("padPassword(nil) = %x, want the padding string", got)
	}
	long := bytes.Repeat([]byte{'a'}, 40)
	got := padPassword(long)
	if len(got) != 32 || !bytes.Equal(got, long[:32]) {
		t.Errorf("padPassword of a 40-byte password = %x, want its first 32 bytes", got)
	}
}

// TestDeriveAES256R5RoundTrip exercises the R5 path (plain SHA-256, no hardened hash), which the corpus — all R6 — does
// not reach. It hand-builds a valid /U and /UE for a known file key and password, then confirms deriveAES256 recovers
// the key for the right password as the user and rejects the wrong one. The owner slot is left as zeros, so the owner
// check simply fails.
func TestDeriveAES256R5RoundTrip(t *testing.T) {
	fileKey := make([]byte, 32)
	for i := range fileKey {
		fileKey[i] = byte(i)
	}
	pw := []byte("secret")
	validationSalt := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	keySalt := []byte{9, 10, 11, 12, 13, 14, 15, 16}

	uHash := sha256.Sum256(append(append([]byte(nil), pw...), validationSalt...))
	u := append(append(append([]byte(nil), uHash[:]...), validationSalt...), keySalt...)

	ik := sha256.Sum256(append(append([]byte(nil), pw...), keySalt...))
	ue, ok := aesCBCEncryptNoPad(ik[:], make([]byte, 16), fileKey)
	if !ok {
		t.Fatal("failed to build test /UE")
	}

	h := &Handler{
		r:       5,
		keyLen:  32,
		strM:    methodAESV3,
		stmM:    methodAESV3,
		metaEnc: true,
		u:       u,
		ue:      ue,
		o:       make([]byte, 48),
		oe:      make([]byte, 32),
	}

	key, user, owner := h.deriveAES256(pw)
	if !user || owner {
		t.Errorf("deriveAES256(correct) user=%v owner=%v, want user=true owner=false", user, owner)
	}
	if !bytes.Equal(key, fileKey) {
		t.Errorf("recovered file key = %x, want %x", key, fileKey)
	}

	if wrongKey, wrongUser, _ := h.deriveAES256([]byte("wrong")); wrongUser || wrongKey != nil {
		t.Errorf("deriveAES256(wrong) user=%v key=%x, want user=false key=nil", wrongUser, wrongKey)
	}
}

// TestObjectKeyLength checks the per-object key length rule of Algorithm 1: min(fileKeyLen+5, 16) bytes, and that the
// "sAlT" suffix for AESV2 changes the key.
func TestObjectKeyLength(t *testing.T) {
	h := &Handler{fileKey: bytes.Repeat([]byte{0xAB}, 16)}
	rc4Key := h.objectKey(3, 0, false)
	if len(rc4Key) != 16 {
		t.Errorf("16-byte file key yields a %d-byte object key, want 16", len(rc4Key))
	}
	if aesKey := h.objectKey(3, 0, true); bytes.Equal(aesKey, rc4Key) {
		t.Error("the AESV2 salt did not change the object key")
	}

	short := &Handler{fileKey: bytes.Repeat([]byte{0xAB}, 5)}
	if got := len(short.objectKey(3, 0, false)); got != 10 {
		t.Errorf("5-byte file key yields a %d-byte object key, want 10", got)
	}
}
