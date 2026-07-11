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
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
)

// aesSalt is the four bytes ("sAlT") appended to the per-object key material for AESV2 (ISO 32000-2 7.6.3).
var aesSalt = []byte{0x73, 0x41, 0x6C, 0x54}

// objectKey derives the per-object decryption key for object (num, gen) from the file encryption key
// (Algorithm 1, ISO 32000-2 7.6.3.2). aesV2 appends the "sAlT" bytes required for AES-128 crypt filters. The
// key is min(fileKeyLen+5, 16) bytes. This applies to R2-R4; R5/R6 (AESV3) use the file key directly.
func (h *Handler) objectKey(num, gen int, aesV2 bool) []byte {
	sum := md5.New()
	sum.Write(h.fileKey)
	sum.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16)})
	sum.Write([]byte{byte(gen), byte(gen >> 8)})
	if aesV2 {
		sum.Write(aesSalt)
	}
	digest := sum.Sum(nil)
	n := len(h.fileKey) + 5
	if n > 16 {
		n = 16
	}
	return digest[:n]
}

// rc4Apply runs the RC4 stream cipher over data with key (RC4 is its own inverse, so this both encrypts and
// decrypts). It reports false when the key length is unacceptable, so callers fall back to the input unchanged.
func rc4Apply(key, data []byte) ([]byte, bool) {
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil, false
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out, true
}

// aesCBCDecrypt decrypts data as AES-CBC where the first block is the IV, then strips PKCS#7 padding
// (ISO 32000-2 7.6.3.2 for AESV2, 7.6.4.3 for AESV3). It reports false — leaving the caller to pass the data
// through unchanged — when the key or ciphertext is unusable, and never panics on malformed input.
func aesCBCDecrypt(key, data []byte) ([]byte, bool) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	if len(data) < 2*aes.BlockSize {
		return nil, false // Need at least the IV plus one ciphertext block.
	}
	iv := data[:aes.BlockSize]
	ct := data[aes.BlockSize:]
	if remainder := len(ct) % aes.BlockSize; remainder != 0 {
		ct = ct[:len(ct)-remainder] // Tolerate a truncated final block by dropping it.
		if len(ct) == 0 {
			return nil, false
		}
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	return stripPKCS7(out), true
}

// aesNoPadCBCDecrypt decrypts data as AES-CBC with an all-zero IV and no padding, used to unwrap the file
// encryption key from /UE and /OE (Algorithm 2.A). data must be a whole number of blocks.
func aesNoPadCBCDecrypt(key, data []byte) ([]byte, bool) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, false
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, data)
	return out, true
}

// aesCBCEncryptNoPad encrypts data as AES-CBC with the given key and IV and no padding, used by the R6 hash
// (Algorithm 2.B). data must be a whole number of blocks (Algorithm 2.B guarantees this).
func aesCBCEncryptNoPad(key, iv, data []byte) ([]byte, bool) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 || len(iv) != aes.BlockSize {
		return nil, false
	}
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	return out, true
}

// stripPKCS7 removes PKCS#7 padding. Malformed padding (a length of 0, one exceeding the data or the block
// size) is left untouched rather than treated as an error, matching the leniency deployed readers extend to
// slightly damaged streams.
func stripPKCS7(data []byte) []byte {
	n := int(data[len(data)-1])
	if n == 0 || n > len(data) || n > aes.BlockSize {
		return data
	}
	return data[:len(data)-n]
}
