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
	"errors"
	"fmt"
	"strings"
	"testing"
)

// countElements totals the array elements and dictionary entries in an object's whole tree — the quantity
// maxContainerElements bounds.
func countElements(obj Object) int {
	switch v := obj.(type) {
	case Array:
		n := len(v)
		for _, e := range v {
			n += countElements(e)
		}
		return n
	case Dict:
		n := len(v)
		for _, e := range v {
			n += countElements(e)
		}
		return n
	default:
		return 0
	}
}

// TestContainerElementBudget covers the per-object element cap. Nesting depth was bounded but container width was not,
// and the payload an object is built from is not bounded by the file: an object stream decodes through internal/filter's
// max(64 MB, 256x input) allowance, so a 39 KB file could hold a single 20M-element array. That is worse than the same
// flood in a content stream because the result lands in Document.objCache and stays live for the whole Document —
// measured at >1.3 GB RSS with ~364 MB still live after the render returned.
func TestContainerElementBudget(t *testing.T) {
	t.Run("array at the cap parses", func(t *testing.T) {
		p := newParser([]byte("["+strings.Repeat("1 ", maxContainerElements)+"]"), 0)
		obj, err := p.parseObject()
		if err != nil {
			t.Fatalf("an array of exactly %d elements was rejected: %v", maxContainerElements, err)
		}
		if got := countElements(obj); got != maxContainerElements {
			t.Fatalf("parsed %d elements, want %d", got, maxContainerElements)
		}
	})
	t.Run("array past the cap is rejected", func(t *testing.T) {
		p := newParser([]byte("["+strings.Repeat("1 ", maxContainerElements+1)+"]"), 0)
		if _, err := p.parseObject(); !errors.Is(err, errTooLarge) {
			t.Fatalf("err = %v, want %v", err, errTooLarge)
		}
	})
	t.Run("nesting shares the budget", func(t *testing.T) {
		// Two sibling arrays, each small enough on its own, together exceed the cap: the budget is per object, not per
		// container, so nesting cannot multiply the total.
		inner := "[" + strings.Repeat("1 ", maxContainerElements/2+16) + "]"
		p := newParser([]byte("["+inner+inner+"]"), 0)
		if _, err := p.parseObject(); !errors.Is(err, errTooLarge) {
			t.Fatalf("err = %v, want %v", err, errTooLarge)
		}
	})
	t.Run("dictionary entries count", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("<<")
		for i := range maxContainerElements + 1 {
			fmt.Fprintf(&sb, " /K%d %d", i, i)
		}
		sb.WriteString(" >>")
		p := newParser([]byte(sb.String()), 0)
		if _, err := p.parseObject(); !errors.Is(err, errTooLarge) {
			t.Fatalf("err = %v, want %v", err, errTooLarge)
		}
	})
	t.Run("the budget is per object", func(t *testing.T) {
		// A file full of ordinary objects must not exhaust a shared allowance: each object gets its own parser.
		body := "[" + strings.Repeat("1 ", 8) + "]"
		for range 4 {
			p := newParser([]byte(body), 0)
			obj, err := p.parseObject()
			if err != nil {
				t.Fatal(err)
			}
			if got := countElements(obj); got != 8 {
				t.Fatalf("parsed %d elements, want 8", got)
			}
		}
	})
}

// TestRefGenerationIdentityAndBound pins the two things a parsed generation number must satisfy. First, it takes no
// part in a reference's identity: object lookup keys on the number alone, so "4 0 R" and "4 1 R" resolve to the same
// object and their RefKeys have to be equal — anything keyed by reference (the interpreter's form-cycle set, every
// reference-keyed cache) would otherwise treat one object as two, letting a form that alternates generations slip past
// its cycle guard. Second, an absurd generation must not be narrowed into int: the lookahead used to accept exactly
// 1<<31, which wraps negative where int is 32 bits (GOARCH=386/arm). It is clamped instead of rejected, so the object
// the reference names is still reachable.
func TestRefGenerationIdentityAndBound(t *testing.T) {
	for _, tc := range []struct {
		name string
		gen  string
		want int
	}{
		{name: "zero", gen: "0", want: 0},
		{name: "ordinary", gen: "1", want: 1},
		{name: "the largest legal generation", gen: "65535", want: 65535},
		{name: "past the legal maximum", gen: "65536", want: 0},
		{name: "the 32-bit wrap point", gen: "2147483648", want: 0},
		{name: "far past any int32", gen: "99999999999999", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newParser([]byte("4 "+tc.gen+" R"), 0)
			obj, err := p.parseObject()
			if err != nil {
				t.Fatalf("parseObject: %v", err)
			}
			ref, ok := obj.(Ref)
			if !ok {
				t.Fatalf("parsed %T (%v), want a Ref: an out-of-range generation must not cost the reference", obj, obj)
			}
			if ref.Num != 4 || ref.Gen != tc.want {
				t.Errorf("parsed %v, want {Num:4 Gen:%d}", ref, tc.want)
			}
			if ref.Gen < 0 {
				t.Errorf("the generation narrowed to %d: int(second.i) wrapped", ref.Gen)
			}
			if got, want := ref.Key(), (Ref{Num: 4}).Key(); got != want {
				t.Errorf("RefKey = %v, want %v: the generation must not split one object's identity", got, want)
			}
		})
	}
}

// TestOversizedObjectDoesNotLoad verifies the cap at the document level: the object fails to load, so nothing oversized
// reaches objCache, references to it resolve to Null (ISO 32000-2 7.3.10's rule for objects that cannot be loaded), and
// the rest of the document is unaffected.
func TestOversizedObjectDoesNotLoad(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Big 2 0 R /Small 3 0 R >>\nendobj\n2 0 obj\n[")
	sb.WriteString(strings.Repeat("1 ", maxContainerElements+1))
	sb.WriteString("]\nendobj\n3 0 obj\n[7 8 9]\nendobj\ntrailer\n<< /Root 1 0 R /Size 4 >>\nstartxref\n0\n%%EOF\n")
	d, err := Open([]byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	root, ok := AsDict(d.Resolve(d.Trailer()[rootKey]))
	if !ok {
		t.Fatalf("root = %v, want a dictionary", d.Trailer()[rootKey])
	}
	obj := d.Resolve(root["Big"])
	if _, isNull := obj.(Null); !isNull {
		t.Fatalf("the oversized array resolved to %T, want Null", obj)
	}
	// Whatever the load left behind, it must not be the array itself: nothing oversized may sit in the cache for the
	// life of the document. (The repair sweep cannot index an object it fails to parse, so the number reads as absent
	// and caches as Null.)
	if cached, cachedOK := d.objCache[2]; cachedOK && countElements(cached) != 0 {
		t.Errorf("the oversized object was cached with %d elements", countElements(cached))
	}
	small, ok := AsArray(d.Resolve(root["Small"]))
	if !ok || len(small) != 3 {
		t.Fatalf("the ordinary array resolved to %v, want three elements", small)
	}
}
