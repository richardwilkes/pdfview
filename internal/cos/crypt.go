// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos

// Decryptor decrypts the strings and stream payload of objects stored directly in the file, keyed by object number and
// generation. Objects parsed out of an object stream are never passed to it: the stream's payload was decrypted as a
// whole under its own number, and ISO 32000-2 7.6.2 does not separately encrypt the objects inside. The document
// installs one via SetDecryptor once the security handler (internal/crypt) is built from the /Encrypt dictionary.
type Decryptor interface {
	// DecryptString returns the decrypted bytes of a string belonging to object (num, gen). It must return the input
	// unchanged when decryption is not possible (for example, before authentication), and must never panic on malformed
	// input.
	DecryptString(num, gen int, data []byte) []byte
	// DecryptStream returns the decrypted raw payload of a stream belonging to object (num, gen), applied before the
	// stream's /Filter chain. The same no-panic and pass-through contract as DecryptString applies.
	DecryptStream(num, gen int, data []byte) []byte
	// EncryptsMetadata reports whether metadata streams are encrypted along with the rest of the document. It is false
	// only when the encryption dictionary carries /EncryptMetadata false, in which case ISO 32000-2 7.6.2 stores every
	// /Type /Metadata stream in the clear and it must not be run through DecryptStream.
	EncryptsMetadata() bool
}

const typeMetadata Name = "Metadata"

// SetDecryptor installs dec and drops every cached object, so objects parsed before the security handler existed are
// reparsed and decrypted on next use. The /Encrypt object's number is recorded so that its own strings (/O, /U, and
// related entries) are never run through the decryptor (ISO 32000-2 7.6.2).
func (d *Document) SetDecryptor(dec Decryptor) {
	d.decryptor = dec
	d.encryptNum = 0
	if ref, ok := d.trailer["Encrypt"].(Ref); ok {
		d.encryptNum = ref.Num
	}
	d.clearCaches()
	d.rearmRepair()
}

// DropCaches drops every parsed-object cache. The security handler calls it after a successful authentication so
// objects cached under the pre-authentication (keyless) state are reparsed and decrypted with the file encryption key.
func (d *Document) DropCaches() {
	d.clearCaches()
	d.rearmRepair()
}

// rearmRepair re-enables the once-per-document repair scan. A sweep run before the current decryption state could not
// decode any object stream (their payloads were still ciphertext), so it recovered neither the objects inside them nor
// a catalog stored there. Nothing runs until a load actually fails, so a document that needs no repair pays nothing.
func (d *Document) rearmRepair() {
	d.repaired = false
}

// decryptDirect decrypts, in place, the strings and stream payload of an object stored directly in the file as (num,
// gen), and returns it. It is a no-op when no decryptor is installed or the object is the encryption dictionary. It
// never follows indirect references: each indirect object is decrypted under its own key when loaded.
func (d *Document) decryptDirect(num, gen int, obj Object) Object {
	if d.decryptor == nil || num == d.encryptNum {
		return obj
	}
	return d.decryptValue(num, gen, obj)
}

// decryptValue recursively decrypts the strings (and, for a stream, the raw payload) reachable within obj without
// crossing an indirect reference. Containers are mutated in place; a bare String is replaced by value, so callers
// substitute the returned object.
func (d *Document) decryptValue(num, gen int, obj Object) Object {
	switch v := obj.(type) {
	case String:
		return String(d.decryptor.DecryptString(num, gen, v))
	case Array:
		for i, e := range v {
			v[i] = d.decryptValue(num, gen, e)
		}
		return v
	case Dict:
		for k, e := range v {
			v[k] = d.decryptValue(num, gen, e)
		}
		return v
	case *Stream:
		typ, _ := AsName(v.Dict["Type"])
		// Cross-reference streams are never encrypted at all (ISO 32000-2 7.5.8.2) — this includes the strings in their
		// own dictionary (e.g. a direct /ID), so the whole object passes through untouched.
		if typ == typeXRef {
			return v
		}
		for k, e := range v.Dict {
			v.Dict[k] = d.decryptValue(num, gen, e)
		}
		// A metadata stream's payload is stored in the clear when /EncryptMetadata is false (ISO 32000-2 7.6.2), but
		// the strings in its dictionary are still encrypted; every other stream, object streams included, has its
		// payload encrypted too.
		if typ != typeMetadata || d.decryptor.EncryptsMetadata() {
			v.Raw = d.decryptor.DecryptStream(num, gen, v.Raw)
		}
		return v
	default:
		return obj
	}
}
