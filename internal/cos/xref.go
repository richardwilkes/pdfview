// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package cos

import (
	"bytes"
	"errors"
	"fmt"
)

// xrefKind classifies a cross-reference entry.
type xrefKind uint8

const (
	// xrefFree marks a free (deleted) object. Free entries are stored, not skipped, so a deletion recorded in a
	// newer increment shadows the object's definition in an older one.
	xrefFree xrefKind = iota
	// xrefInFile locates an object at a byte offset in the file.
	xrefInFile
	// xrefInStream locates an object inside an object stream.
	xrefInStream
)

// xrefEntry is one cross-reference entry.
type xrefEntry struct {
	// offset is the byte offset of the object for xrefInFile entries.
	offset int64
	// stmNum is the object number of the containing object stream for xrefInStream entries.
	stmNum int
	// stmIdx is the index of the object within that stream.
	stmIdx int
	kind   xrefKind
}

var (
	errNoStartXref   = errors.New("startxref not found")
	errBadXref       = errors.New("cannot parse cross-reference section")
	errBadXrefStream = errors.New("cannot parse cross-reference stream")
)

// typeXRef is the /Type value of a cross-reference stream. Such streams are never encrypted (ISO 32000-2
// 7.5.8.2), so the decryptor skips them.
const typeXRef Name = "XRef"

// startXrefWindow is how far from the end of the file the startxref keyword is searched for. The spec says the
// last line holds the offset, but real files carry trailing junk; this matches the tolerance of deployed readers.
const startXrefWindow = 2048

// findStartXref locates the last startxref keyword near the end of the file and returns the offset it names.
func (d *Document) findStartXref() (int64, error) {
	tail := d.data
	base := 0
	if len(tail) > startXrefWindow {
		base = len(tail) - startXrefWindow
		tail = tail[base:]
	}
	idx := bytes.LastIndex(tail, []byte("startxref"))
	if idx < 0 {
		return 0, errNoStartXref
	}
	p := newParser(d.data, base+idx+len("startxref"))
	off, err := p.expectInt()
	if err != nil {
		return 0, fmt.Errorf("%w: no offset after startxref", errNoStartXref)
	}
	return off, nil
}

// loadXref reads the cross-reference chain starting from the startxref offset, following /Prev (and hybrid-file
// /XRefStm) links. Sections are processed newest first and the first entry seen for an object number wins,
// implementing incremental-update precedence. The trailer is merged the same way.
func (d *Document) loadXref() error {
	start, err := d.findStartXref()
	if err != nil {
		return err
	}
	visited := make(map[int64]bool)
	var trailers []Dict
	offset := start
	for {
		if offset < 0 || offset >= int64(len(d.data)) {
			return fmt.Errorf("%w: offset %d out of range", errBadXref, offset)
		}
		if visited[offset] {
			break // A cycle in the /Prev chain; keep what has been read so far.
		}
		visited[offset] = true
		trailer, serr := d.readXrefSection(offset)
		if serr != nil {
			return serr
		}
		trailers = append(trailers, trailer)
		prev, ok := AsInt(trailer["Prev"])
		if !ok {
			break
		}
		offset = prev
	}
	d.trailer = mergeTrailers(trailers)
	return nil
}

// mergeTrailers combines the trailer dictionaries of a cross-reference chain, newest first: the newest trailer
// wins, with the document-level keys filled in from older trailers when the newer ones lack them.
func mergeTrailers(trailers []Dict) Dict {
	if len(trailers) == 0 {
		return Dict{}
	}
	merged := trailers[0]
	for _, older := range trailers[1:] {
		for _, key := range []Name{"Root", "Info", "Encrypt", "ID"} {
			if _, ok := merged[key]; !ok {
				if v, ok2 := older[key]; ok2 {
					merged[key] = v
				}
			}
		}
	}
	return merged
}

// readXrefSection reads the classic table or cross-reference stream at offset, adds its entries (first seen
// wins), and returns its trailer dictionary. For a classic section in a hybrid file, the /XRefStm stream is
// processed after the table itself, giving the table precedence over the stream and both precedence over /Prev,
// per ISO 32000-2 7.5.8.4.
func (d *Document) readXrefSection(offset int64) (Dict, error) {
	p := newParser(d.data, int(offset))
	tok, err := p.next()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadXref, err)
	}
	if tok.kind == tkKeyword && bytes.Equal(tok.s, []byte("xref")) {
		trailer, terr := d.readClassicXref(p)
		if terr != nil {
			return nil, terr
		}
		if stmOff, ok := AsInt(trailer["XRefStm"]); ok {
			// Failure to read the hybrid stream is not fatal: the classic table is complete for pre-1.5 readers.
			d.readXrefStream(stmOff) //nolint:errcheck // See above.
		}
		return trailer, nil
	}
	if tok.kind == tkInt {
		return d.readXrefStream(offset)
	}
	return nil, fmt.Errorf("%w: neither xref table nor xref stream at offset %d", errBadXref, offset)
}

// readClassicXref reads the subsections of a classic xref table (the "xref" keyword has been consumed) and the
// trailer dictionary that follows.
func (d *Document) readClassicXref(p *parser) (Dict, error) {
	for {
		tok, err := p.next()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errBadXref, err)
		}
		switch {
		case tok.kind == tkKeyword && bytes.Equal(tok.s, []byte("trailer")):
			obj, derr := p.parseObject()
			if derr != nil {
				return nil, fmt.Errorf("%w: bad trailer: %w", errBadXref, derr)
			}
			trailer, ok := obj.(Dict)
			if !ok {
				return nil, fmt.Errorf("%w: trailer is not a dictionary", errBadXref)
			}
			return trailer, nil
		case tok.kind == tkInt:
			if err = d.readClassicSubsection(p, tok.i); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: unexpected token in xref table", errBadXref)
		}
	}
}

// readClassicSubsection reads one "start count" subsection whose start value has been consumed.
func (d *Document) readClassicSubsection(p *parser, start int64) error {
	count, err := p.expectInt()
	if err != nil {
		return fmt.Errorf("%w: %w", errBadXref, err)
	}
	if start < 0 || count < 0 {
		return fmt.Errorf("%w: negative subsection header", errBadXref)
	}
	for i := int64(0); i < count; i++ {
		offset, oerr := p.expectInt()
		if oerr != nil {
			return fmt.Errorf("%w: %w", errBadXref, oerr)
		}
		gen, gerr := p.expectInt()
		if gerr != nil {
			return fmt.Errorf("%w: %w", errBadXref, gerr)
		}
		tok, terr := p.next()
		if terr != nil || tok.kind != tkKeyword || len(tok.s) != 1 {
			return fmt.Errorf("%w: bad entry type", errBadXref)
		}
		num := start + i
		switch tok.s[0] {
		case 'n':
			d.setEntry(num, xrefEntry{kind: xrefInFile, offset: offset})
		case 'f':
			d.setEntry(num, xrefEntry{kind: xrefFree})
		default:
			return fmt.Errorf("%w: bad entry type %q", errBadXref, tok.s)
		}
		_ = gen // Generation numbers are recorded in the file but object lookup is keyed by number alone.
	}
	return nil
}

// setEntry records an entry for an object number unless one is already present (the first entry seen comes from
// the newest increment and wins). Object numbers outside the supported range are ignored.
func (d *Document) setEntry(num int64, entry xrefEntry) {
	if num <= 0 || num > maxObjectNumber {
		return
	}
	if _, exists := d.xref[int(num)]; !exists {
		d.xref[int(num)] = entry
	}
}

// readXrefStream reads the cross-reference stream at offset and returns its dictionary, which doubles as the
// trailer, per ISO 32000-2 7.5.8.
func (d *Document) readXrefStream(offset int64) (Dict, error) {
	obj, _, _, err := parseIndirectAt(d.data, offset, -1)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadXrefStream, err)
	}
	stream, ok := obj.(*Stream)
	if !ok {
		return nil, fmt.Errorf("%w: not a stream", errBadXrefStream)
	}
	if typ, _ := AsName(stream.Dict["Type"]); typ != typeXRef {
		return nil, fmt.Errorf("%w: stream /Type is not /XRef", errBadXrefStream)
	}
	if err = d.readXrefStreamEntries(stream); err != nil {
		return nil, err
	}
	return stream.Dict, nil
}

func (d *Document) readXrefStreamEntries(stream *Stream) error {
	// Cross-reference streams are never encrypted and use only direct values in /W and /Index, so decoding here
	// cannot recurse into object loading.
	data, err := d.StreamData(stream)
	if err != nil {
		return fmt.Errorf("%w: %w", errBadXrefStream, err)
	}
	widths, ok := AsArray(stream.Dict["W"])
	if !ok || len(widths) < 3 {
		return fmt.Errorf("%w: missing or short /W", errBadXrefStream)
	}
	var w [3]int
	rowLen := 0
	for i := range 3 {
		v, wok := AsInt(widths[i])
		if !wok || v < 0 || v > 8 {
			return fmt.Errorf("%w: bad /W value", errBadXrefStream)
		}
		w[i] = int(v)
		rowLen += int(v)
	}
	if rowLen == 0 {
		return fmt.Errorf("%w: zero-width /W", errBadXrefStream)
	}
	index := d.xrefStreamIndex(stream.Dict)
	pos := 0
	for i := 0; i+1 < len(index); i += 2 {
		start := index[i]
		count := index[i+1]
		for j := int64(0); j < count; j++ {
			if pos+rowLen > len(data) {
				return nil // Truncated stream data; keep the entries read so far (leniency).
			}
			f1 := readField(data[pos:], w[0], 1) // A zero-width type field defaults to type 1.
			f2 := readField(data[pos+w[0]:], w[1], 0)
			f3 := readField(data[pos+w[0]+w[1]:], w[2], 0)
			pos += rowLen
			num := start + j
			switch f1 {
			case 0:
				d.setEntry(num, xrefEntry{kind: xrefFree})
			case 1:
				d.setEntry(num, xrefEntry{kind: xrefInFile, offset: int64(f2)})
			case 2:
				if f2 <= maxObjectNumber {
					d.setEntry(num, xrefEntry{kind: xrefInStream, stmNum: int(f2), stmIdx: int(f3)})
				}
			default:
				// Unknown entry types are ignored, as the spec directs for forward compatibility.
			}
		}
	}
	return nil
}

// xrefStreamIndex returns the /Index pairs, defaulting to [0 Size].
func (d *Document) xrefStreamIndex(dict Dict) []int64 {
	if arr, ok := AsArray(dict["Index"]); ok {
		index := make([]int64, 0, len(arr))
		for _, entry := range arr {
			v, vok := AsInt(entry)
			if !vok || v < 0 {
				return nil
			}
			index = append(index, v)
		}
		return index
	}
	size, ok := AsInt(dict["Size"])
	if !ok || size < 0 {
		return nil
	}
	return []int64{0, size}
}

// readField reads an n-byte big-endian unsigned value, returning def when n is zero. Values that would exceed
// 63 bits saturate rather than wrap.
func readField(data []byte, n int, def uint64) uint64 {
	if n == 0 {
		return def
	}
	var v uint64
	for i := range n {
		v = v<<8 | uint64(data[i])
	}
	if v > 1<<62 {
		v = 1 << 62
	}
	return v
}
