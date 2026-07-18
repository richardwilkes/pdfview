// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package function

import (
	"errors"

	"github.com/richardwilkes/pdfview/internal/cos"
)

var errBadSampled = errors.New("invalid sampled function")

// sampled is a type 0 (sampled) function: an m-dimensional table of n-component samples, evaluated with multilinear
// interpolation (ISO 32000-2 7.10.3). Only linear interpolation is implemented; /Order 3 (cubic spline) is treated as
// linear, the tolerance every deployed reader extends.
type sampled struct {
	common
	data    []byte
	size    []int
	encode  []float32
	decode  []float32
	strides []int
	bps     int
	n       int
}

func parseSampled(d *cos.Document, stream *cos.Stream, c common) (Func, error) {
	if len(c.rng) == 0 {
		return nil, errBadRange // Range is required for type 0.
	}
	s := &sampled{common: c, n: len(c.rng) / 2}
	m := c.NInputs()
	sizes, ok := numbers(d, stream.Dict, "Size", maxInputs)
	if !ok || len(sizes) != m {
		return nil, errBadSampled
	}
	s.size = make([]int, m)
	for i, v := range sizes {
		s.size[i] = int(v)
		if s.size[i] < 1 || s.size[i] > 1<<20 {
			return nil, errBadSampled
		}
	}
	bps, ok := cos.AsInt(d.Resolve(stream.Dict["BitsPerSample"]))
	if !ok {
		return nil, errBadSampled
	}
	s.bps = int(bps)
	switch s.bps {
	case 1, 2, 4, 8, 12, 16, 24, 32:
	default:
		return nil, errBadSampled
	}
	if s.encode, ok = numbers(d, stream.Dict, "Encode", 2*maxInputs); !ok || len(s.encode) != 2*m {
		s.encode = make([]float32, 2*m)
		for i := range m {
			s.encode[2*i+1] = float32(s.size[i] - 1)
		}
	}
	if s.decode, ok = numbers(d, stream.Dict, "Decode", 2*maxOutputs); !ok || len(s.decode) != 2*s.n {
		s.decode = append([]float32(nil), s.rng...)
	}
	// Total sample bits must fit the decoded data (and a sanity ceiling keeps hostile sizes in check).
	totalSamples := s.n
	for _, sz := range s.size {
		if totalSamples > (1<<28)/sz {
			return nil, errBadSampled
		}
		totalSamples *= sz
	}
	data, err := d.StreamData(stream)
	if err != nil {
		return nil, err
	}
	if int64(totalSamples)*int64(s.bps) > int64(len(data))*8 {
		return nil, errBadSampled
	}
	s.data = data
	s.strides = make([]int, m)
	stride := 1
	for i := range m {
		s.strides[i] = stride
		stride *= s.size[i]
	}
	return s, nil
}

func (s *sampled) NOutputs() int {
	return s.n
}

func (s *sampled) Eval(in []float32) []float32 {
	m := s.NInputs()
	x := s.clampIn(in)
	// Encode each input into sample-grid space and split into integer cell plus fraction.
	lo := make([]int, m)
	fr := make([]float32, m)
	for i := range m {
		e := interpolate(x[i], s.domain[2*i], s.domain[2*i+1], s.encode[2*i], s.encode[2*i+1])
		e = clampF(e, 0, float32(s.size[i]-1))
		l := int(e)
		if l > s.size[i]-2 {
			l = s.size[i] - 2
		}
		if l < 0 {
			l = 0
		}
		lo[i] = l
		fr[i] = e - float32(l)
		if s.size[i] == 1 {
			lo[i], fr[i] = 0, 0
		}
	}
	out := make([]float32, s.n)
	// Multilinear interpolation over the 2^m cell corners. maxInputs caps m, bounding the corner count.
	for corner := range 1 << m {
		w := float32(1)
		flat := 0
		for d := range m {
			if corner>>d&1 == 1 {
				w *= fr[d]
				idx := lo[d] + 1
				if idx > s.size[d]-1 {
					idx = s.size[d] - 1
				}
				flat += idx * s.strides[d]
			} else {
				w *= 1 - fr[d]
				flat += lo[d] * s.strides[d]
			}
		}
		if w == 0 {
			continue
		}
		for j := range s.n {
			out[j] += w * s.sample(flat, j)
		}
	}
	return s.clampOut(out)
}

// sample returns output component j of the sample at flat index flat, decoded to its output value.
func (s *sampled) sample(flat, j int) float32 {
	raw := s.readBits(flat*s.n + j)
	maxV := float64(uint64(1)<<s.bps - 1)
	v := float32(float64(raw) / maxV)
	return interpolate(v, 0, 1, s.decode[2*j], s.decode[2*j+1])
}

// readBits extracts the index-th bps-wide sample from the bit stream.
func (s *sampled) readBits(index int) uint32 {
	bitPos := uint64(index) * uint64(s.bps)
	var v uint32
	for got := 0; got < s.bps; {
		byteIdx := bitPos >> 3
		if byteIdx >= uint64(len(s.data)) {
			return v << (s.bps - got) // Truncated data reads as zero bits (parse validated the size, so only hostile edits hit this).
		}
		bitOff := int(bitPos & 7)
		avail := 8 - bitOff
		take := s.bps - got
		if take > avail {
			take = avail
		}
		bits := uint32(s.data[byteIdx]>>(avail-take)) & (1<<take - 1)
		v = v<<take | bits
		got += take
		bitPos += uint64(take)
	}
	return v
}
