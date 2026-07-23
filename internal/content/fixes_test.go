// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package content

import (
	"fmt"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
	"github.com/richardwilkes/pdfview/internal/store"
)

// TestCMNonFiniteRejected verifies the cm operator rejects a CTM concat that overflows to a non-finite matrix (finite
// operands can still multiply to Inf), leaving the prior CTM in effect so the device never sees NaN/Inf coordinates.
func TestCMNonFiniteRejected(t *testing.T) {
	// The first cm scales by 1e20 (finite in float32); the second squares it to ~1e40, which overflows float32 to +Inf
	// and must be dropped. The paint then carries the first, still-finite, CTM. (PDF reals have no exponent syntax, so
	// the magnitude is written out as a plain decimal.)
	big := "1" + strings.Repeat("0", 20) // 1e20
	rec := run(t, nil, nil, fmt.Sprintf("%[1]s 0 0 %[1]s 0 0 cm %[1]s 0 0 %[1]s 0 0 cm 0 0 m 1 1 l S", big))
	wantOps(t, rec, opStroke)
	ctm := rec.calls[0].ctm
	if !ctm.IsFinite() {
		t.Fatalf("non-finite CTM reached the device: %+v", ctm)
	}
	if ctm.A != 1e20 {
		t.Fatalf("second cm was not rejected: ctm.A = %v, want 1e20", ctm.A)
	}
}

// TestMiterLimitGuarded verifies the M operator rejects non-positive and non-finite miter limits (like the line-width
// guard), keeping the default of 10.
func TestMiterLimitGuarded(t *testing.T) {
	huge := "1" + strings.Repeat("0", 39) // 1e39, which overflows float32 to +Inf
	for _, tc := range []struct {
		name    string
		content string
		want    float32
	}{
		{"valid", "3.5 M 0 0 m 1 1 l S", 3.5},
		{"zero", "0 M 0 0 m 1 1 l S", 10},
		{"negative", "-4 M 0 0 m 1 1 l S", 10},
		{"overflow", huge + " M 0 0 m 1 1 l S", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := run(t, nil, nil, tc.content)
			wantOps(t, rec, opStroke)
			if got := rec.calls[0].sp.MiterLimit; got != tc.want {
				t.Fatalf("MiterLimit = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExtGStateMiterLimitGuarded verifies the ExtGState /ML entry applies the same finiteness/positivity guard.
func TestExtGStateMiterLimitGuarded(t *testing.T) {
	for _, tc := range []struct {
		name string
		ml   string
		want float32
	}{
		{"valid", "2.5", 2.5},
		{"negative", "-1", 10},
		{"overflow", "1" + strings.Repeat("0", 39), 10}, // 1e39 -> +Inf in float32
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := minimalPDF(fmt.Sprintf("<< /Type /ExtGState /ML %s >>", tc.ml))
			d, err := cos.Open([]byte(pdf))
			if err != nil {
				t.Fatal(err)
			}
			res := cos.Dict{catExtGState: cos.Dict{resGSName: cos.Ref{Num: 1}}}
			rec := run(t, d, res, "/GS0 gs 0 0 m 1 1 l S")
			wantOps(t, rec, opStroke)
			if got := rec.calls[0].sp.MiterLimit; got != tc.want {
				t.Fatalf("MiterLimit = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestImageCacheRetainsAfterCapReached verifies the no-store fallback image cache keeps a resource cached once decoded
// even after the cap is reached, so a repeatedly drawn image is not re-decoded on every Do. Before the LRU, the
// (maxCachedImages+1)-th distinct ref was never cached and each Do handed the device a freshly decoded image.
func TestImageCacheRetainsAfterCapReached(t *testing.T) {
	const extra = maxCachedImages + 1
	bodies := make([]string, extra)
	xobjects := cos.Dict{}
	var content strings.Builder
	for i := range extra {
		// A distinct 1x1 DeviceGray image per slot; the sample byte varies so the streams differ.
		bodies[i] = fmt.Sprintf(
			"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray /Length 1 >>\nstream\n%c\nendstream",
			byte(i+1))
		name := cos.Name(fmt.Sprintf("Im%d", i))
		xobjects[name] = cos.Ref{Num: i + 1}
		fmt.Fprintf(&content, "/%s Do ", name)
	}
	// Draw the last (cap-overflowing) image a second time; the LRU must return the same decoded image both times.
	last := cos.Name(fmt.Sprintf("Im%d", extra-1))
	fmt.Fprintf(&content, "/%s Do", last)

	d, err := cos.Open([]byte(minimalPDF(bodies...)))
	if err != nil {
		t.Fatal(err)
	}
	rec := run(t, d, cos.Dict{catXObject: xobjects}, content.String())
	if len(rec.calls) != extra+1 {
		t.Fatalf("recorded %d draws, want %d", len(rec.calls), extra+1)
	}
	first, second := rec.calls[extra-1], rec.calls[extra]
	if first.img == nil || second.img == nil {
		t.Fatalf("image decode failed: %+v / %+v", first.img, second.img)
	}
	if first.img != second.img {
		t.Fatal("repeated draw of a cap-overflowing image re-decoded instead of reusing the cached image")
	}
}

// TestLoadFontCachedFailureReportsMiss verifies that a font whose load fails, when cached as a negative entry in the
// budgeted store, still reports a miss on subsequent lookups (like the no-store LRU path). Before the fix, the store
// path returned the typed-nil *font.Font boxed in the cache as a success, so a repeated Tf would clear the current font
// instead of aborting the operator and preserving the previous font.
func TestLoadFontCachedFailureReportsMiss(t *testing.T) {
	// Object 1 is a plain integer, so it is not a font dictionary and font.Load never runs — loadFont fails.
	d, err := cos.Open([]byte(minimalPDF("42")))
	if err != nil {
		t.Fatal(err)
	}
	res := cos.Dict{cos.Name("Font"): cos.Dict{cos.Name("F1"): cos.Ref{Num: 1}}}
	in := newInterp(d, res, gfx.Identity(), device.Device(nil), store.New(1<<20))

	if f, ok := in.loadFont(cos.Name("F1")); ok || f != nil {
		t.Fatalf("first load of an unloadable font: got (%v, %v), want (nil, false)", f, ok)
	}
	// The second lookup hits the cached negative entry; it must also report a miss.
	if f, ok := in.loadFont(cos.Name("F1")); ok || f != nil {
		t.Fatalf("cached-failure load of an unloadable font: got (%v, %v), want (nil, false)", f, ok)
	}
}

// TestLRUCache exercises the count-bounded LRU directly: recency-ordered eviction, MRU retention across a full cycle,
// and negative (nil-value) entries surviving as cache hits.
func TestLRUCache(t *testing.T) {
	c := newLRUCache[int, *int](2)
	one, two, three := 1, 2, 3
	c.put(1, &one)
	c.put(2, &two)
	// Touch 1 so 2 becomes least-recently-used, then insert 3: 2 must be the one evicted.
	if _, ok := c.get(1); !ok {
		t.Fatal("key 1 missing before eviction")
	}
	c.put(3, &three)
	if _, ok := c.get(2); ok {
		t.Fatal("key 2 should have been evicted as least-recently-used")
	}
	if v, ok := c.get(1); !ok || v != &one {
		t.Fatal("key 1 should have survived as recently used")
	}
	if v, ok := c.get(3); !ok || v != &three {
		t.Fatal("key 3 missing after insertion")
	}
	// A negative entry (nil value) is a hit, not a miss — a cached failure must not be re-attempted.
	c.put(3, nil)
	if v, ok := c.get(3); !ok || v != nil {
		t.Fatalf("negative entry not retained: v=%v ok=%v", v, ok)
	}
}
