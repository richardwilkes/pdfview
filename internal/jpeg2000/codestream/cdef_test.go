// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/engine"
)

// TestReorderByChannelDefs covers the JP2 cdef plane reordering: a non-standard
// channel order is permuted into colour order, while a standard or absent definition
// (and any malformed one) leaves the planes in stream order.
func TestReorderByChannelDefs(t *testing.T) {
	// Planes tagged by their component index so the order is observable.
	planes := func(comps ...int) []engine.ComponentPlane {
		ps := make([]engine.ComponentPlane, len(comps))
		for i, c := range comps {
			ps[i] = engine.ComponentPlane{Comp: c}
		}
		return ps
	}
	order := func(ps []engine.ComponentPlane) []int {
		out := make([]int, len(ps))
		for i, p := range ps {
			out[i] = p.Comp
		}
		return out
	}
	eq := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	cases := []struct {
		name string
		defs []ChannelDef
		in   []int
		want []int
	}{
		{
			// issue236: components stored reversed (comp0=Cr, comp1=Cb, comp2=Y).
			name: "reverse YCbCr",
			defs: []ChannelDef{{Comp: 0, Typ: 0, Asoc: 3}, {Comp: 1, Typ: 0, Asoc: 2}, {Comp: 2, Typ: 0, Asoc: 1}},
			in:   []int{0, 1, 2},
			want: []int{2, 1, 0},
		},
		{
			name: "standard order is identity",
			defs: []ChannelDef{{Comp: 0, Typ: 0, Asoc: 1}, {Comp: 1, Typ: 0, Asoc: 2}, {Comp: 2, Typ: 0, Asoc: 3}},
			in:   []int{0, 1, 2},
			want: []int{0, 1, 2},
		},
		{
			// RGB + alpha: opacity channel keeps its trailing position.
			name: "colour then opacity",
			defs: []ChannelDef{{Comp: 0, Typ: 0, Asoc: 1}, {Comp: 1, Typ: 0, Asoc: 2}, {Comp: 2, Typ: 0, Asoc: 3}, {Comp: 3, Typ: 1, Asoc: 0}},
			in:   []int{0, 1, 2, 3},
			want: []int{0, 1, 2, 3},
		},
		{
			name: "no cdef leaves order",
			defs: nil,
			in:   []int{0, 1, 2},
			want: []int{0, 1, 2},
		},
		{
			// Malformed: a colour position is missing → safe fallback to stream order.
			name: "missing colour falls back",
			defs: []ChannelDef{{Comp: 0, Typ: 0, Asoc: 1}, {Comp: 1, Typ: 0, Asoc: 3}, {Comp: 2, Typ: 0, Asoc: 3}},
			in:   []int{0, 1, 2},
			want: []int{0, 1, 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Decoder{channelDefs: tc.defs}
			got := order(d.reorderByChannelDefs(planes(tc.in...)))
			if !eq(got, tc.want) {
				t.Fatalf("reorder %v: got %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
