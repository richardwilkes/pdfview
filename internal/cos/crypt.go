// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos

// Decryptor decrypts the strings and stream payload of objects stored directly in the file, keyed by the object's
// number and generation. Objects parsed out of an object stream are never passed to it: the object stream's payload was
// already decrypted as a whole under the stream's own number, and ISO 32000-2 7.6.2 does not separately encrypt the
// objects it contains. The document installs one via SetDecryptor once its security handler (internal/crypt) has been
// built from the /Encrypt dictionary.
type Decryptor interface {
	// DecryptString returns the decrypted bytes of a string belonging to object (num, gen). It must return the input
	// unchanged when decryption is not possible (for example, before authentication), and must never panic on malformed
	// input.
	DecryptString(num, gen int, data []byte) []byte
	// DecryptStream returns the decrypted raw payload of a stream belonging to object (num, gen), applied before the
	// stream's /Filter chain. The same no-panic and pass-through contract as DecryptString applies.
	DecryptStream(num, gen int, data []byte) []byte
}

// SetDecryptor installs dec and drops every cached object, so objects parsed before the security handler existed — the
// catalog probed while validating the root, and the /Encrypt dictionary itself — are reparsed and decrypted on next
// use. The /Encrypt object number is recorded from the trailer so that its own strings (the /O, /U, and related entries
// the handler was built from) are never themselves run through the decryptor (ISO 32000-2 7.6.2).
func (d *Document) SetDecryptor(dec Decryptor) {
	d.decryptor = dec
	d.encryptNum = 0
	if ref, ok := d.trailer["Encrypt"].(Ref); ok {
		d.encryptNum = ref.Num
	}
	d.clearCaches()
}

// DropCaches drops every parsed-object cache. The security handler calls it after a successful authentication so
// objects cached under the pre-authentication (keyless) state are reparsed and decrypted with the file encryption key.
func (d *Document) DropCaches() {
	d.clearCaches()
}

// decryptDirect decrypts, in place, the strings and stream payload of an object that was stored directly at a file
// offset under object number num and generation gen, and returns it. It is a no-op when no decryptor is installed or
// the object is the encryption dictionary itself. It never follows indirect references: every indirect object is
// decrypted under its own key when it is itself loaded.
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
		for k, e := range v.Dict {
			v.Dict[k] = d.decryptValue(num, gen, e)
		}
		// Cross-reference streams are never encrypted (ISO 32000-2 7.5.8.2); everything else — including object streams
		// — is.
		if typ, _ := AsName(v.Dict["Type"]); typ != typeXRef {
			v.Raw = d.decryptor.DecryptStream(num, gen, v.Raw)
		}
		return v
	default:
		return obj
	}
}
