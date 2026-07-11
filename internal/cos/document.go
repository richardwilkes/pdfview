// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package cos implements the COS layer of a PDF document: the lexer, the object model (null, boolean, integer,
// real, string, name, array, dictionary, stream, and indirect reference), classic and stream cross-reference
// parsing with /Prev chains and hybrid files, object streams, a repair scan for files whose cross-reference data
// is broken or inconsistent, an indirect-reference resolver with a cycle guard, and text-string decoding.
//
// Everything is bounded so hostile input cannot force unbounded work (see plan.md "Resource limits &
// robustness"): reference chains are capped at maxResolveDepth, container nesting at maxNestingDepth, and stream
// decoding inherits internal/filter's chain and expansion caps. Termination is guaranteed by these caps; there
// are no timeouts.
package cos

import (
	"errors"
	"fmt"

	"github.com/richardwilkes/pdfview/internal/filter"
)

// maxResolveDepth caps how many indirect references Resolve follows before giving up, terminating reference
// cycles (see plan.md "Resource limits & robustness").
const maxResolveDepth = 64

var (
	errNoRoot        = errors.New("document has no usable root object")
	errNotObjStm     = errors.New("object is not an object stream")
	errCryptFilter   = errors.New("encrypted streams are not supported yet")
	errBadFilterName = errors.New("filter name is not a name object")
)

// Document is one open PDF file at the COS level. It is not safe for concurrent use; the public API package
// serializes access with its document mutex.
type Document struct {
	xref      map[int]xrefEntry
	objCache  map[int]Object
	objStms   map[int]*objStm
	trailer   Dict
	decryptor Decryptor
	data      []byte
	// encryptNum is the object number of the /Encrypt dictionary, whose own strings are never decrypted; it is
	// meaningful only once decryptor is non-nil.
	encryptNum int
	repaired   bool
}

// Open parses the cross-reference data of the PDF file in data (which the Document retains and slices into) and
// validates that a usable document root exists, running the repair scan when the file's own cross-reference
// information is broken, inconsistent, or missing. It fails only when even repair cannot produce a root.
func Open(data []byte) (*Document, error) {
	d := &Document{
		data:     data,
		xref:     make(map[int]xrefEntry),
		objCache: make(map[int]Object),
		objStms:  make(map[int]*objStm),
	}
	if err := d.loadXref(); err != nil {
		if rerr := d.repair(); rerr != nil {
			return nil, fmt.Errorf("cannot read cross-reference data (%w) and repair failed: %w", err, rerr)
		}
	}
	if !d.rootUsable() {
		if !d.repaired {
			if err := d.repair(); err != nil {
				return nil, fmt.Errorf("%w: %w", errNoRoot, err)
			}
		}
		if !d.rootUsable() {
			return nil, errNoRoot
		}
	}
	return d, nil
}

// rootUsable reports whether the trailer names a /Root that resolves to a dictionary.
func (d *Document) rootUsable() bool {
	_, ok := AsDict(d.Resolve(d.trailer["Root"]))
	return ok
}

// Trailer returns the document trailer dictionary (for cross-reference streams, the stream dictionary).
func (d *Document) Trailer() Dict {
	return d.trailer
}

// Resolve follows obj through indirect references until a direct object is reached and returns it. References to
// free or absent objects resolve to Null per ISO 32000-2 7.3.10, as do reference cycles (terminated by
// maxResolveDepth) and objects that cannot be loaded even after repair.
func (d *Document) Resolve(obj Object) Object {
	for range maxResolveDepth {
		ref, ok := obj.(Ref)
		if !ok {
			if obj == nil {
				return Null{}
			}
			return obj
		}
		loaded, err := d.loadObject(ref.Num)
		if err != nil {
			return Null{}
		}
		obj = loaded
	}
	return Null{}
}

// loadObject returns the top-level object with the given number, parsing and caching it on first use. A load
// failure (bad offset, mismatched header, unparseable content) triggers the document-wide repair scan once, then
// retries; absent and free entries are not failures — they read as Null.
func (d *Document) loadObject(num int) (Object, error) {
	if obj, ok := d.objCache[num]; ok {
		return obj, nil
	}
	obj, err := d.loadObjectUncached(num)
	if err != nil && !d.repaired {
		if rerr := d.repair(); rerr == nil {
			obj, err = d.loadObjectUncached(num)
		}
	}
	if err != nil {
		return nil, err
	}
	d.objCache[num] = obj
	return obj, nil
}

func (d *Document) loadObjectUncached(num int) (Object, error) {
	entry, ok := d.xref[num]
	if !ok || entry.kind == xrefFree {
		return Null{}, nil
	}
	if entry.kind == xrefInFile {
		obj, gen, _, err := parseIndirectAt(d.data, entry.offset, num)
		if err != nil {
			return nil, err
		}
		// The object was stored directly in the file, so its strings and stream payload are encrypted under
		// its own number and generation. Objects reached through objFromStm are not: their container was
		// decrypted as a whole (ISO 32000-2 7.6.2).
		return d.decryptDirect(num, gen, obj), nil
	}
	return d.objFromStm(entry.stmNum, entry.stmIdx, num)
}

// ObjectNums returns the object numbers present in the cross-reference data, in no particular order. It exists
// for exhaustive sweeps (tests and fuzzing).
func (d *Document) ObjectNums() []int {
	nums := make([]int, 0, len(d.xref))
	for num := range d.xref {
		nums = append(nums, num)
	}
	return nums
}

// LoadObject returns the top-level object with the given number, or Null when it is free, absent, or unloadable.
func (d *Document) LoadObject(num int) Object {
	obj, err := d.loadObject(num)
	if err != nil {
		return Null{}
	}
	return obj
}

// GetInt resolves dict[key] and returns it as an integer.
func (d *Document) GetInt(dict Dict, key Name) (int64, bool) {
	return AsInt(d.Resolve(dict[key]))
}

// GetName resolves dict[key] and returns it as a Name.
func (d *Document) GetName(dict Dict, key Name) (Name, bool) {
	return AsName(d.Resolve(dict[key]))
}

// GetDict resolves dict[key] and returns it as a Dict (a Stream's dictionary qualifies).
func (d *Document) GetDict(dict Dict, key Name) (Dict, bool) {
	return AsDict(d.Resolve(dict[key]))
}

// GetArray resolves dict[key] and returns it as an Array.
func (d *Document) GetArray(dict Dict, key Name) (Array, bool) {
	return AsArray(d.Resolve(dict[key]))
}

// GetStream resolves dict[key] and returns it as a *Stream.
func (d *Document) GetStream(dict Dict, key Name) (*Stream, bool) {
	return AsStream(d.Resolve(dict[key]))
}

// GetString resolves dict[key] and returns it as a String.
func (d *Document) GetString(dict Dict, key Name) (String, bool) {
	return AsString(d.Resolve(dict[key]))
}

// StreamData applies s's /Filter chain to its raw bytes and returns the decoded data. Filter chain length and
// output size are capped by internal/filter. Encrypted streams are rejected until the standard security handler
// lands (M2).
func (d *Document) StreamData(s *Stream) ([]byte, error) {
	specs, err := d.filterSpecs(s.Dict)
	if err != nil {
		return nil, err
	}
	return filter.DecodeChain(specs, s.Raw)
}

// imageFilterName reports whether name is one of the image-codec filters that internal/filter rejects and
// internal/imaging decodes at rasterization time, including the abbreviated inline-image forms.
func imageFilterName(name Name) bool {
	switch name {
	case "DCTDecode", "DCT", "CCITTFaxDecode", "CCF", "JBIG2Decode", "JPXDecode":
		return true
	default:
		return false
	}
}

// ImageFilterSplit applies dict's leading non-image filters to raw — an image XObject's raw stream payload or
// an inline image's data between ID and EI — and stops at the first image-codec filter (DCTDecode,
// CCITTFaxDecode, JBIG2Decode, JPXDecode, or an abbreviated form), returning the processed data, the codec's
// name, and its resolved decode-parms dictionary (possibly nil). The inline-image abbreviations /F and /DP are
// honored alongside /Filter and /DecodeParms (only here — on ordinary streams /F means an external file). When
// the chain contains no image codec, the returned codec is empty and data holds fully decoded sample bytes.
// Filters listed after an image codec are impossible to apply (the codec ends the byte-stream pipeline) and are
// ignored, matching deployed viewers.
func (d *Document) ImageFilterSplit(dict Dict, raw []byte) (data []byte, codec Name, parms Dict, err error) {
	lookup := Dict{"Filter": dict["Filter"], "DecodeParms": dict["DecodeParms"]}
	if lookup["Filter"] == nil {
		lookup["Filter"] = dict["F"]
	}
	if lookup["DecodeParms"] == nil {
		lookup["DecodeParms"] = dict["DP"]
	}
	names, parmsArr, err := d.filterNamesAndParms(lookup)
	if err != nil {
		return nil, "", nil, err
	}
	specs := make([]filter.Spec, 0, len(names))
	for i, name := range names {
		var parmDict Dict
		if i < len(parmsArr) {
			parmDict, _ = AsDict(d.Resolve(parmsArr[i]))
		}
		if imageFilterName(name) {
			data, err = filter.DecodeChain(specs, raw)
			if err != nil {
				return nil, "", nil, err
			}
			return data, name, parmDict, nil
		}
		if name == "Crypt" {
			cryptName, ok := d.GetName(parmDict, "Name")
			if !ok || cryptName == "Identity" {
				continue
			}
			return nil, "", nil, errCryptFilter
		}
		specs = append(specs, filter.Spec{Name: string(name), Params: d.filterParams(parmDict)})
	}
	data, err = filter.DecodeChain(specs, raw)
	if err != nil {
		return nil, "", nil, err
	}
	return data, "", nil, nil
}

// filterSpecs converts a stream dictionary's /Filter and /DecodeParms entries into filter.Specs. A /Crypt filter
// whose /Name is /Identity (or absent, the default) is dropped from the chain, since Identity is a no-op; any
// other crypt filter is an error until M2.
func (d *Document) filterSpecs(dict Dict) ([]filter.Spec, error) {
	names, parms, err := d.filterNamesAndParms(dict)
	if err != nil {
		return nil, err
	}
	specs := make([]filter.Spec, 0, len(names))
	for i, name := range names {
		var parmDict Dict
		if i < len(parms) {
			parmDict, _ = AsDict(d.Resolve(parms[i]))
		}
		if name == "Crypt" {
			cryptName, ok := d.GetName(parmDict, "Name")
			if !ok || cryptName == "Identity" {
				continue
			}
			return nil, errCryptFilter
		}
		specs = append(specs, filter.Spec{Name: string(name), Params: d.filterParams(parmDict)})
	}
	return specs, nil
}

// filterNamesAndParms normalizes /Filter (name or array of names) and /DecodeParms (dictionary or array,
// possibly containing nulls) into parallel slices.
func (d *Document) filterNamesAndParms(dict Dict) (names []Name, parms Array, err error) {
	switch f := d.Resolve(dict["Filter"]).(type) {
	case nil, Null:
	case Name:
		names = []Name{f}
	case Array:
		names = make([]Name, 0, len(f))
		for _, entry := range f {
			name, ok := AsName(d.Resolve(entry))
			if !ok {
				return nil, nil, errBadFilterName
			}
			names = append(names, name)
		}
	default:
		return nil, nil, errBadFilterName
	}
	switch p := d.Resolve(dict["DecodeParms"]).(type) {
	case Dict:
		parms = Array{p}
	case Array:
		parms = p
	default:
	}
	return names, parms, nil
}

// filterParams builds filter.Params from one /DecodeParms dictionary (which may be nil).
func (d *Document) filterParams(parmDict Dict) filter.Params {
	params := filter.DefaultParams()
	if parmDict == nil {
		return params
	}
	if v, ok := d.GetInt(parmDict, "Predictor"); ok {
		params.Predictor = int(v)
	}
	if v, ok := d.GetInt(parmDict, "Colors"); ok {
		params.Colors = int(v)
	}
	if v, ok := d.GetInt(parmDict, "BitsPerComponent"); ok {
		params.BitsPerComponent = int(v)
	}
	if v, ok := d.GetInt(parmDict, "Columns"); ok {
		params.Columns = int(v)
	}
	if v, ok := d.GetInt(parmDict, "EarlyChange"); ok {
		params.EarlyChange = int(v)
	}
	return params
}

// clearCaches drops every parsed-object cache. The repair scan calls this because entries parsed through the old
// cross-reference data may be wrong.
func (d *Document) clearCaches() {
	d.objCache = make(map[int]Object)
	d.objStms = make(map[int]*objStm)
}
