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
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
)

// padding is the 32-byte password-padding string from ISO 32000-2 7.6.4.3, Algorithm 2.
var padding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// padPassword pads (or truncates) a password to exactly 32 bytes with the padding string (Algorithm 2, step a).
func padPassword(pw []byte) []byte {
	out := make([]byte, 32)
	n := copy(out, pw)
	copy(out[n:], padding)
	return out
}

// deriveRC4 authenticates password against an R2-R4 document, returning the file encryption key on success.
func (h *Handler) deriveRC4(pw []byte) (fileKey []byte, user, owner bool) {
	if key := h.keyFromUserPassword(pw); h.userKeyValid(key) {
		user = true
		fileKey = key
	}
	// The owner password decrypts /O to recover the padded user password, which is then validated as a user password
	// (Algorithm 7).
	recovered := h.userPasswordFromOwner(pw)
	if key := h.keyFromUserPassword(recovered); h.userKeyValid(key) {
		owner = true
		if fileKey == nil {
			fileKey = key
		}
	}
	return fileKey, user, owner
}

// keyFromUserPassword computes the file encryption key from a user password (Algorithm 2).
func (h *Handler) keyFromUserPassword(pw []byte) []byte {
	sum := md5.New()
	sum.Write(padPassword(pw))
	sum.Write(h.o[:32])
	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], h.perm)
	sum.Write(p[:])
	sum.Write(h.id0)
	if h.r >= 4 && !h.metaEnc {
		sum.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}
	key := sum.Sum(nil)
	if h.r >= 3 {
		for range 50 {
			s := md5.Sum(key[:h.keyLen])
			key = s[:]
		}
	}
	return key[:h.keyLen]
}

// userKeyValid reports whether key reproduces the stored /U entry (Algorithm 6): all 32 bytes for R2, the first 16 for
// R3+ (the remaining bytes of /U are arbitrary padding).
func (h *Handler) userKeyValid(key []byte) bool {
	computed := h.computeU(key)
	if h.r == 2 {
		return bytes.Equal(computed, h.u[:32])
	}
	return len(computed) >= 16 && bytes.Equal(computed[:16], h.u[:16])
}

// computeU computes the /U value for a file encryption key (Algorithm 4 for R2, Algorithm 5 for R3+).
func (h *Handler) computeU(key []byte) []byte {
	if h.r == 2 {
		out, _ := rc4Apply(key, padding)
		return out
	}
	sum := md5.New()
	sum.Write(padding)
	sum.Write(h.id0)
	x, _ := rc4Apply(key, sum.Sum(nil))
	for i := 1; i <= 19; i++ {
		x, _ = rc4Apply(xorByte(key, byte(i)), x)
	}
	return x
}

// userPasswordFromOwner recovers the padded user password from /O using the owner password (Algorithm 7, reversing the
// RC4 rounds Algorithm 3 applied).
func (h *Handler) userPasswordFromOwner(ownerPw []byte) []byte {
	sum := md5.Sum(padPassword(ownerPw))
	key := sum[:]
	if h.r >= 3 {
		for range 50 {
			s := md5.Sum(key[:h.keyLen])
			key = s[:]
		}
	}
	rc4Key := key[:h.keyLen]
	recovered := append([]byte(nil), h.o[:32]...)
	if h.r == 2 {
		recovered, _ = rc4Apply(rc4Key, recovered)
		return recovered
	}
	for i := 19; i >= 0; i-- {
		recovered, _ = rc4Apply(xorByte(rc4Key, byte(i)), recovered)
	}
	return recovered
}

// xorByte returns key with every byte XORed by b (the per-round key transform of Algorithms 3, 5, and 7).
func xorByte(key []byte, b byte) []byte {
	out := make([]byte, len(key))
	for i, k := range key {
		out[i] = k ^ b
	}
	return out
}

// deriveAES256 authenticates password against an R5/R6 document (Algorithm 2.A), returning the file encryption key on
// success.
func (h *Handler) deriveAES256(pw []byte) (fileKey []byte, user, owner bool) {
	pw = saslPrep(pw)
	// User password: hash(pw + user validation salt) must equal the first 32 bytes of /U.
	if bytes.Equal(h.hash256(pw, h.u[32:40], nil), h.u[:32]) {
		user = true
		ik := h.hash256(pw, h.u[40:48], nil)
		if key, ok := aesNoPadCBCDecrypt(ik, h.ue[:32]); ok {
			fileKey = key
		}
	}
	// Owner password: hash(pw + owner validation salt + /U) must equal the first 32 bytes of /O.
	if bytes.Equal(h.hash256(pw, h.o[32:40], h.u[:48]), h.o[:32]) {
		owner = true
		if fileKey == nil {
			ik := h.hash256(pw, h.o[40:48], h.u[:48])
			if key, ok := aesNoPadCBCDecrypt(ik, h.oe[:32]); ok {
				fileKey = key
			}
		}
	}
	return fileKey, user, owner
}

// hash256 is the R5/R6 password hash: a plain SHA-256 for R5, and the hardened iterated hash of Algorithm 2.B for R6.
// salt is the 8-byte validation or key salt; udata is empty for the user password and the 48-byte /U for the owner
// password.
func (h *Handler) hash256(pw, salt, udata []byte) []byte {
	first := sha256.New()
	first.Write(pw)
	first.Write(salt)
	first.Write(udata)
	k := first.Sum(nil)
	if h.r == 5 {
		return k
	}
	return hash2B(pw, udata, k)
}

// hash2B is Algorithm 2.B (ISO 32000-2 7.6.4.3.4), the R6 hardened hash. k is the initial SHA-256 of (pw + salt +
// udata); the round loop then repeatedly AES-encrypts and re-hashes until the termination condition is met, returning
// the first 32 bytes.
func hash2B(pw, udata, k []byte) []byte {
	for round := 1; ; round++ {
		block := make([]byte, 0, len(pw)+len(k)+len(udata))
		block = append(block, pw...)
		block = append(block, k...)
		block = append(block, udata...)
		k1 := bytes.Repeat(block, 64)
		e, ok := aesCBCEncryptNoPad(k[:16], k[16:32], k1)
		if !ok {
			return k[:32]
		}
		mod := 0
		for _, b := range e[:16] {
			mod = (mod*256 + int(b)) % 3
		}
		switch mod {
		case 0:
			s := sha256.Sum256(e)
			k = s[:]
		case 1:
			s := sha512.Sum384(e)
			k = s[:]
		default:
			s := sha512.Sum512(e)
			k = s[:]
		}
		if round >= 64 && int(e[len(e)-1]) <= round-32 {
			return k[:32]
		}
	}
}

// saslPrep applies the parts of the SASLprep profile (RFC 4013) that matter for the PDF passwords seen in practice: the
// UTF-8 bytes are truncated to 127 bytes (ISO 32000-2 7.6.4.3.3). Full stringprep normalization is not performed; ASCII
// passwords — effectively all of them — are unaffected.
func saslPrep(pw []byte) []byte {
	if len(pw) > 127 {
		return pw[:127]
	}
	return pw
}
