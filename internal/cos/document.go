// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package cos implements the COS layer of a PDF document: the lexer, the object model (null, boolean, integer, real,
// string, name, array, dictionary, stream, and indirect reference), classic and stream cross-reference parsing with
// /Prev chains and hybrid files, object streams, a repair scan for files whose cross-reference data is broken or
// inconsistent, an indirect-reference resolver with a cycle guard, and text-string decoding.
//
// Everything is bounded so hostile input cannot force unbounded work: reference chains are capped at maxResolveDepth,
// container nesting at maxNestingDepth, and stream decoding inherits internal/filter's chain and expansion caps.
// Termination is guaranteed by these caps; there are no timeouts.
package cos

import (
	"errors"
	"fmt"

	"github.com/richardwilkes/pdfview/internal/filter"
)

// maxResolveDepth caps how many indirect references Resolve follows before giving up, terminating reference cycles.
const maxResolveDepth = 64

var (
	errNoRoot        = errors.New("document has no usable root object")
	errNotObjStm     = errors.New("object is not an object stream")
	errCryptFilter   = errors.New("a /Crypt filter naming a crypt filter other than /Identity is not supported")
	errBadFilterName = errors.New("filter name is not a name object")

	errFilterChainTooLong = fmt.Errorf("filter chain is longer than the %d filters allowed", filter.MaxChainLength)
)

// Document is one open PDF file at the COS level. It is not safe for concurrent use; the public API package serializes
// access with its document mutex.
type Document struct {
	xref     map[int]xrefEntry
	objCache map[int]Object
	// objFailed records the numbers whose load failed, and why, so a broken reference is parsed at most once. A failed
	// load walks the object graph and may scan for a stream terminator, and nothing above this layer charges for it, so
	// a content stream naming a broken reference once per operator would otherwise cost O(operators × file size).
	objFailed map[int]error
	objStms   map[int]*objStm
	// objStmLoading is the set of object streams currently being parsed (see loadObjStm).
	objStmLoading map[int]bool
	trailer       Dict
	decryptor     Decryptor
	data          []byte
	// encryptNum is the object number of the /Encrypt dictionary, whose own strings are never decrypted. It is
	// meaningful only while decryptor is non-nil.
	encryptNum int
	// decodeWork is the running total DecodeWork reports.
	decodeWork uint64
	// xrefStreamRows counts cross-reference stream rows processed across the whole chain (see maxXrefStreamRows).
	xrefStreamRows int
	repaired       bool
	// xrefLoading reports whether loadXref is assembling the cross-reference table. A load attempted from there resolves
	// against an incomplete table, so its failure says nothing about the object and must not be cached — and must not
	// trigger the repair scan, which would replace d.xref and d.trailer under loadXref: it would keep writing entries
	// into the replaced map, then overwrite the repaired trailer with one merged from the broken chain, while d.repaired
	// (now set) makes Open skip the retry.
	xrefLoading bool
}

// Open parses the cross-reference data of the PDF file in data (which the Document retains and slices into) and checks
// that a usable root exists, running the repair scan when the file's own cross-reference data is broken, inconsistent,
// or missing. It fails only when even repair cannot produce a root.
//
// For a trailer naming an /Encrypt dictionary the root check is deferred. The security handler is built by the layer
// above and installed with SetDecryptor only after Open returns, so a catalog stored in an object stream — what every
// modern producer emits — is still ciphertext that neither Resolve nor the repair sweep can see through. Call
// ValidateRoot once the decryptor is installed.
func Open(data []byte) (*Document, error) {
	d := &Document{
		data:          data,
		xref:          make(map[int]xrefEntry),
		objCache:      make(map[int]Object),
		objFailed:     make(map[int]error),
		objStms:       make(map[int]*objStm),
		objStmLoading: make(map[int]bool),
	}
	if err := d.loadXref(); err != nil {
		if rerr := d.repair(); rerr != nil {
			return nil, fmt.Errorf("cannot read cross-reference data (%w) and repair failed: %w", err, rerr)
		}
	}
	if d.Encrypted() {
		return d, nil
	}
	if err := d.ValidateRoot(); err != nil {
		return nil, err
	}
	return d, nil
}

// Encrypted reports whether the trailer names an /Encrypt dictionary, i.e. whether this document's strings and stream
// payloads are ciphertext until a Decryptor is installed.
func (d *Document) Encrypted() bool {
	switch d.trailer["Encrypt"].(type) {
	case nil, Null:
		return false
	default:
		return true
	}
}

// ValidateRoot checks that the trailer names a /Root resolving to a dictionary, running the repair scan once if it does
// not. Open applies it itself for an unencrypted document; for an encrypted one it is the deferred check the caller
// runs once a Decryptor is installed, and may run again after authentication supplies the file key. Repeating it costs
// nothing once the root resolves.
func (d *Document) ValidateRoot() error {
	if d.rootUsable() {
		return nil
	}
	if !d.repaired {
		if err := d.repair(); err != nil {
			return fmt.Errorf("%w: %w", errNoRoot, err)
		}
	}
	if !d.rootUsable() {
		return errNoRoot
	}
	return nil
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

// Resolve follows obj through indirect references until a direct object is reached and returns it. References to free
// or absent objects resolve to Null per ISO 32000-2 7.3.10, as do reference cycles (terminated by maxResolveDepth) and
// objects that cannot be loaded even after repair.
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

// loadObject returns the top-level object with the given number, parsing and caching it on first use. A load failure
// (bad offset, mismatched header, unparseable content) triggers the document-wide repair scan once, then retries;
// absent and free entries are not failures — they read as Null. Failures are cached too, so a broken reference costs
// one parse attempt for the life of the document (or until a cache drop).
func (d *Document) loadObject(num int) (Object, error) {
	if obj, ok := d.objCache[num]; ok {
		return obj, nil
	}
	if err, ok := d.objFailed[num]; ok {
		return nil, err
	}
	obj, err := d.loadObjectUncached(num)
	// Repair is deferred while an object-stream load or the cross-reference load is in flight: it would replace the
	// table and drop every cache under frames still parsing against the old one, and its loadObjStm sweep would
	// re-enter a stream whose parse is suspended further down this stack (see xrefLoading). d.repaired stays false, so
	// the next failing load reached from neither state still triggers it.
	if err != nil && !d.repaired && !d.xrefLoading && len(d.objStmLoading) == 0 {
		if rerr := d.repair(); rerr == nil {
			obj, err = d.loadObjectUncached(num)
		}
	}
	if err != nil {
		// The object-stream guards fire because of where this load sits in the call stack, not because of num: the
		// same object loads cleanly from the top level, and both refuse before parsing anything. A failure during the
		// cross-reference load was decided against an incomplete table, so it is not cached either.
		if !d.xrefLoading && !errors.Is(err, errObjStmCycle) && !errors.Is(err, errObjStmDepth) {
			d.objFailed[num] = err
		}
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
		obj, gen, err := d.parseIndirectObjectAt(entry.offset, num)
		if err != nil {
			return nil, err
		}
		// Stored directly in the file, so its strings and stream payload are encrypted under its own number and
		// generation. Objects reached through objFromStm are not: their container was decrypted as a whole.
		return d.decryptDirect(num, gen, obj), nil
	}
	return d.objFromStm(entry.stmNum, entry.stmIdx, num)
}

// ObjectNums returns the object numbers present in the cross-reference data, in no particular order. It exists for
// exhaustive sweeps (tests and fuzzing).
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

// StreamData applies s's /Filter chain to its raw bytes and returns the decoded data. Filter chain length and output
// size are capped by internal/filter. Document-level encryption is already undone at parse time by the installed
// Decryptor (see crypt.go); a non-Identity /Crypt filter in the chain is an error.
func (d *Document) StreamData(s *Stream) ([]byte, error) {
	specs, err := d.filterSpecs(s.Dict)
	if err != nil {
		return nil, err
	}
	return d.decodeChain(specs, s.Raw)
}

// decodeChain runs specs over raw and meters the bytes the chain produced, including those a chain that ultimately
// failed had already produced (see DecodeWork).
func (d *Document) decodeChain(specs []filter.Spec, raw []byte) ([]byte, error) {
	out, decoded, err := filter.DecodeChain(specs, raw)
	d.decodeWork += uint64(decoded)
	return out, err
}

// DecodeWork returns the running total of bytes every filter chain this document has run has produced, including chains
// that failed. A caller charging a work budget cannot measure that itself: a failed decode returns no bytes, yet
// internal/filter lets one stage inflate to max(64 MB, 256x input) before reporting ErrTooLarge, so pricing a failure
// by its input values a 64 KB zip bomb at a thousandth of the work it forced. Bracket a call with two reads and charge
// the difference.
func (d *Document) DecodeWork() uint64 {
	return d.decodeWork
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

// ImageFilterSplit applies the leading non-image filters of an image XObject to raw and stops at the first image codec
// (DCTDecode, CCITTFaxDecode, JBIG2Decode, JPXDecode, or an abbreviation), returning the processed data, the codec's
// name, and its resolved decode-parms dictionary (possibly nil). With no image codec in the chain, codec is empty and
// data holds fully decoded samples. Filters listed after a codec cannot be applied and are ignored, as deployed viewers
// do. Only /Filter and /DecodeParms are consulted: on an ordinary stream /F is a file specification, not a filter
// abbreviation. Use InlineImageFilterSplit for inline images.
func (d *Document) ImageFilterSplit(dict Dict, raw []byte) (data []byte, codec Name, parms Dict, err error) {
	return d.imageFilterSplit(Dict{"Filter": dict["Filter"], "DecodeParms": dict["DecodeParms"]}, raw)
}

// InlineImageFilterSplit is ImageFilterSplit for an inline image's data between ID and EI, where the abbreviations /F
// and /DP stand in for /Filter and /DecodeParms (ISO 32000-2 table 92). The unabbreviated keys win when both appear.
func (d *Document) InlineImageFilterSplit(dict Dict, raw []byte) (data []byte, codec Name, parms Dict, err error) {
	lookup := Dict{"Filter": dict["Filter"], "DecodeParms": dict["DecodeParms"]}
	if lookup["Filter"] == nil {
		lookup["Filter"] = dict["F"]
	}
	if lookup["DecodeParms"] == nil {
		lookup["DecodeParms"] = dict["DP"]
	}
	return d.imageFilterSplit(lookup, raw)
}

// imageFilterSplit is the shared body of ImageFilterSplit and InlineImageFilterSplit; lookup holds the /Filter and
// /DecodeParms entries to use, under those keys.
func (d *Document) imageFilterSplit(lookup Dict, raw []byte) (data []byte, codec Name, parms Dict, err error) {
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
			data, err = d.decodeChain(specs, raw)
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
	data, err = d.decodeChain(specs, raw)
	if err != nil {
		return nil, "", nil, err
	}
	return data, "", nil, nil
}

// filterSpecs converts a stream dictionary's /Filter and /DecodeParms entries into filter.Specs. A /Crypt filter whose
// /Name is /Identity (or absent, the default) is dropped from the chain, since Identity is a no-op; any other (named)
// crypt filter is unsupported and is an error.
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

// filterNamesAndParms normalizes /Filter (name or array of names) and /DecodeParms (dictionary or array, possibly
// containing nulls) into parallel slices. A chain longer than filter.MaxChainLength is rejected here, before the names
// are resolved, rather than by filter.DecodeChain: the array length is file-supplied, an object stream can carry a
// million-element one in a few megabytes, and PageContents calls StreamData once per /Contents entry (up to
// maxContentStreams of them), so building the []Name and []filter.Spec first cost tens of milliseconds and tens of
// megabytes per call.
func (d *Document) filterNamesAndParms(dict Dict) (names []Name, parms Array, err error) {
	switch f := d.Resolve(dict["Filter"]).(type) {
	case nil, Null:
	case Name:
		names = []Name{f}
	case Array:
		if len(f) > filter.MaxChainLength {
			return nil, nil, errFilterChainTooLong
		}
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

// filterParams builds filter.Params from one /DecodeParms dictionary (which may be nil). Every value passes through
// exactly as the file declared it: internal/filter validates each one against what it accepts (64 colors, 16 bits per
// component, 2^24 columns, predictor 15), so an out-of-range value is rejected there rather than acted on here.
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

// clearCaches drops every parsed-object cache. objStmLoading is deliberately left alone: it is not a cache but the set
// of loadObjStm frames on the stack, each of which deletes its own entry on the way out. Replacing the map would strand
// their markers and disarm the re-entrancy guard for the rest of the recursion.
func (d *Document) clearCaches() {
	d.objCache = make(map[int]Object)
	d.objFailed = make(map[int]error)
	d.objStms = make(map[int]*objStm)
}
