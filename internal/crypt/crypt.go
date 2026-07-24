// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package crypt implements the PDF standard security handler (ISO 32000-2 7.6): revisions 2, 3, and 4 (RC4 and AESV2),
// revision 5, and revision 6 (AESV3). It authenticates the user and owner passwords, reproducing the status bits
// MuPDF's fz_authenticate_password returns, and decrypts strings and streams with per-object keys.
//
// It is installed into the COS layer as a cos.Decryptor: the handler is built from the /Encrypt dictionary, the empty
// password is tried immediately (unlocking documents that need no password), and once a password authenticates the file
// encryption key lets every subsequently loaded object be decrypted. All cryptographic primitives come from the Go
// standard library. Hostile input yields errors or pass-through data, never a panic.
package crypt

import (
	"errors"

	"github.com/richardwilkes/pdfview/internal/cos"
)

var (
	errNotStandard = errors.New("only the standard security handler is supported")
	errBadRevision = errors.New("unsupported or missing encryption revision")
	errBadKeyEntry = errors.New("encryption dictionary /O or /U entry has the wrong length")
)

// method identifies how a string or stream is encrypted.
type method uint8

const (
	methodIdentity method = iota // not encrypted
	methodRC4                    // RC4 with a per-object key (V1, V2, and V4 /CFM /V2)
	methodAESV2                  // AES-128-CBC with a per-object key (V4 /CFM /AESV2)
	methodAESV3                  // AES-256-CBC with the file key (V5 /CFM /AESV3)
)

// Handler is the standard security handler for one open document. It is safe to use only under the COS document's own
// serialization (the public API's single mutex); it is not independently concurrency-safe.
type Handler struct {
	id0     []byte // first element of the trailer /ID (empty when absent); folded into the R2-R4 key
	o       []byte // /O entry
	u       []byte // /U entry
	oe      []byte // /OE entry (R5/R6)
	ue      []byte // /UE entry (R5/R6)
	fileKey []byte // the file encryption key, nil until a password authenticates
	r       int    // revision
	keyLen  int    // file key length in bytes (R2-R4)
	perm    uint32 // /P as an unsigned 32-bit value
	strM    method // string encryption method
	stmM    method // stream encryption method
	metaEnc bool   // /EncryptMetadata
	authed  bool   // whether a password has authenticated (including the empty password tried at open)
}

// New builds the standard security handler from encDict (the resolved /Encrypt dictionary) using c for the trailer /ID
// and any indirectly stored entries, then tries the empty password so documents that need none are immediately usable.
// It returns an error for encryption schemes it does not implement; the caller then treats the document as
// encrypted-but-locked.
func New(c *cos.Document, encDict cos.Dict) (*Handler, error) {
	if filter, _ := c.GetName(encDict, "Filter"); filter != "Standard" {
		return nil, errNotStandard
	}
	v, _ := c.GetInt(encDict, "V")
	r, ok := c.GetInt(encDict, "R")
	// The standard security handler defines revisions 2 through 6 (ISO 32000-2 7.6.4); there is no revision 1 and
	// nothing above 6, so both ends are rejected, as is a missing or non-integer /R.
	if !ok || r < 2 || r > 6 {
		return nil, errBadRevision
	}
	h := &Handler{r: int(r), metaEnc: true}
	if o, sok := c.GetString(encDict, "O"); sok {
		h.o = o
	}
	if u, sok := c.GetString(encDict, "U"); sok {
		h.u = u
	}
	if me, meok := cos.AsBool(c.Resolve(encDict["EncryptMetadata"])); meok {
		h.metaEnc = me
	}
	if p, pok := c.GetInt(encDict, "P"); pok {
		h.perm = uint32(p)
	}
	if id0, idok := firstID(c); idok {
		h.id0 = id0
	}
	if err := h.configure(c, encDict, int(v)); err != nil {
		return nil, err
	}
	h.tryPassword("")
	return h, nil
}

// firstID returns the first element of the trailer /ID array as raw bytes.
func firstID(c *cos.Document) ([]byte, bool) {
	arr, ok := cos.AsArray(c.Resolve(c.Trailer()["ID"]))
	if !ok || len(arr) == 0 {
		return nil, false
	}
	s, ok := cos.AsString(c.Resolve(arr[0]))
	if !ok {
		return nil, false
	}
	return s, true
}

// configure sets the key length and the string/stream methods from V, R, /Length, and (for V4/V5) the crypt filter
// dictionary, and validates the /O and /U lengths for the revision.
func (h *Handler) configure(c *cos.Document, encDict cos.Dict, v int) error {
	switch {
	case h.r <= 4:
		length := 40
		if l, ok := c.GetInt(encDict, "Length"); ok && l >= 40 && l <= 256 && l%8 == 0 {
			length = int(l)
		}
		// The RC4/AESV2 file key derives from a 16-byte MD5 digest, so a hostile /Length up to 256 (keyLen 32) would
		// slice that digest out of range and panic. Cap at 16 (ISO 32000-2 keys never exceed 128 bits for R<=4).
		h.keyLen = min(length/8, 16)
		if len(h.o) < 32 || len(h.u) < 32 {
			return errBadKeyEntry
		}
		if v >= 4 {
			h.strM = h.cryptFilterMethod(c, encDict, "StrF")
			h.stmM = h.cryptFilterMethod(c, encDict, "StmF")
		} else {
			h.strM = methodRC4
			h.stmM = methodRC4
		}
	default: // R5, R6
		h.keyLen = 32
		if len(h.o) < 48 || len(h.u) < 48 {
			return errBadKeyEntry
		}
		if oe, ok := c.GetString(encDict, "OE"); ok {
			h.oe = oe
		}
		if ue, ok := c.GetString(encDict, "UE"); ok {
			h.ue = ue
		}
		if len(h.oe) < 32 || len(h.ue) < 32 {
			return errBadKeyEntry
		}
		h.strM = methodAESV3
		h.stmM = methodAESV3
	}
	return nil
}

// cryptFilterMethod resolves the method named by the /StmF or /StrF entry through the /CF dictionary. The default
// filter name is /Identity (no encryption) per ISO 32000-2 7.6.5.
func (h *Handler) cryptFilterMethod(c *cos.Document, encDict cos.Dict, which cos.Name) method {
	name, ok := c.GetName(encDict, which)
	if !ok || name == "Identity" {
		return methodIdentity
	}
	cf, ok := c.GetDict(encDict, "CF")
	if !ok {
		return methodIdentity
	}
	filter, ok := c.GetDict(cf, name)
	if !ok {
		return methodIdentity
	}
	switch cfm, _ := c.GetName(filter, "CFM"); cfm {
	case "V2":
		return methodRC4
	case "AESV2":
		return methodAESV2
	case "AESV3":
		return methodAESV3
	default: // /None, /Identity, or absent
		return methodIdentity
	}
}

// NeedsPassword reports whether a password is required to use the document: true unless the empty password (tried at
// open) already authenticated.
func (h *Handler) NeedsPassword() bool {
	return !h.authed
}

// Authenticate tries password as both the user and the owner password, returning which matched. On success it records
// the file encryption key so subsequent decryption can proceed. The two booleans map directly onto the public API's
// UserAuthenticatedMask and OwnerAuthenticatedMask bits.
func (h *Handler) Authenticate(password string) (user, owner bool) {
	key, user, owner := h.derive(password)
	if key != nil {
		h.fileKey = key
		h.authed = true
	}
	return user, owner
}

// tryPassword authenticates without reporting the result; used for the empty-password probe at open.
func (h *Handler) tryPassword(password string) {
	h.Authenticate(password)
}

// derive tests password as user then owner, returning the file encryption key it yields (nil if neither matched) and
// which checks passed. Testing both is required to set both status bits when a password serves as both.
func (h *Handler) derive(password string) (fileKey []byte, user, owner bool) {
	pw := []byte(password)
	if h.r <= 4 {
		return h.deriveRC4(pw)
	}
	return h.deriveAES256(pw)
}

// DecryptString decrypts a string belonging to object (num, gen), returning it unchanged when no key is yet available
// or the strings are not encrypted.
func (h *Handler) DecryptString(num, gen int, data []byte) []byte {
	return h.apply(h.strM, num, gen, data)
}

// DecryptStream decrypts a stream's raw payload (before its /Filter chain) belonging to object (num, gen), returning it
// unchanged when no key is yet available or the streams are not encrypted.
func (h *Handler) DecryptStream(num, gen int, data []byte) []byte {
	return h.apply(h.stmM, num, gen, data)
}

// EncryptsMetadata reports whether metadata streams are encrypted along with everything else, i.e. whether the
// encryption dictionary's /EncryptMetadata entry (default true) is set.
func (h *Handler) EncryptsMetadata() bool {
	return h.metaEnc
}

// apply performs the actual decryption for one string or stream. It is total: any shortfall (no key, bad key length,
// malformed ciphertext) yields the input unchanged rather than an error or panic, because these hooks run deep inside
// object loading where there is no error channel and hostile input must not crash.
func (h *Handler) apply(m method, num, gen int, data []byte) []byte {
	if h.fileKey == nil || m == methodIdentity || len(data) == 0 {
		return data
	}
	switch m {
	case methodRC4:
		if out, ok := rc4Apply(h.objectKey(num, gen, false), data); ok {
			return out
		}
	case methodAESV2:
		if out, ok := aesCBCDecrypt(h.objectKey(num, gen, true), data); ok {
			return out
		}
	case methodAESV3:
		if out, ok := aesCBCDecrypt(h.fileKey, data); ok {
			return out
		}
	}
	return data
}
