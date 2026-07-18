// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package font

import (
	"bytes"
	"fmt"
	"testing"
)

// buildT1Program assembles a minimal but conformant Type 1 program (compare internal/type1's full test builder; this
// compact variant keeps the font package's fixture self-contained): three glyphs — .notdef, a 600-wide box "A", and a
// 400-wide wedge "T" bound to a non-ASCII code by the built-in encoding.
func buildT1Program() []byte {
	encrypt := func(plain []byte, r uint16, lead int) []byte {
		const c1, c2 = 52845, 22719
		full := append(bytes.Repeat([]byte{0x55}, lead), plain...)
		out := make([]byte, len(full))
		for i, p := range full {
			c := p ^ byte(r>>8)
			r = (uint16(c)+r)*c1 + c2
			out[i] = c
		}
		return out
	}
	num := func(v int) []byte {
		if v >= -107 && v <= 107 {
			return []byte{byte(v + 139)}
		}
		if v >= 108 && v <= 1131 {
			v -= 108
			return []byte{byte(247 + v>>8), byte(v)}
		}
		return []byte{255, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
	cs := func(parts ...any) []byte {
		var out []byte
		for _, p := range parts {
			switch v := p.(type) {
			case int:
				out = append(out, num(v)...)
			case byte:
				out = append(out, v)
			}
		}
		return out
	}
	const (
		opRlineto   = byte(5)
		opHlineto   = byte(6)
		opClosepath = byte(9)
		opHsbw      = byte(13)
		opEndchar   = byte(14)
		opRmoveto   = byte(21)
	)
	glyphs := []struct {
		name string
		prog []byte
	}{
		{".notdef", cs(0, 500, opHsbw, opEndchar)},
		{"A", cs(50, 600, opHsbw, 0, 0, opRmoveto, 400, opHlineto, 0, 700, opRlineto,
			-400, 0, opRlineto, opClosepath, opEndchar)},
		{"T", cs(0, 400, opHsbw, 0, 0, opRmoveto, 300, opHlineto, -150, 500, opRlineto,
			opClosepath, opEndchar)},
	}
	var clearBuf bytes.Buffer
	clearBuf.WriteString("%!PS-AdobeFont-1.0: FontT1 001.000\n/FontName /FontT1 def\n")
	clearBuf.WriteString("/FontMatrix [0.001 0 0 0.001 0 0] readonly def\n")
	clearBuf.WriteString("/FontBBox {0 -250 1000 750} readonly def\n")
	clearBuf.WriteString("/Encoding 256 array\n0 1 255 {1 index exch /.notdef put} for\n")
	clearBuf.WriteString("dup 65 /A put\ndup 200 /T put\nreadonly def\ncurrentdict end\ncurrentfile eexec\n")
	var priv bytes.Buffer
	priv.WriteString("dup /Private 10 dict dup begin\n/lenIV 4 def\n")
	fmt.Fprintf(&priv, "/CharStrings %d dict dup begin\n", len(glyphs))
	for _, g := range glyphs {
		enc := encrypt(g.prog, 4330, 4)
		fmt.Fprintf(&priv, "/%s %d RD ", g.name, len(enc))
		priv.Write(enc)
		priv.WriteString(" ND\n")
	}
	priv.WriteString("end\nend\n")
	out := clearBuf.Bytes()
	out = append(out, encrypt(priv.Bytes(), 55665, 4)...)
	out = append(out, []byte("\n0000000000000000\ncleartomark\n")...)
	return out
}

// loadT1TestFont loads a font dictionary carrying the test program, with or without /Widths.
func loadT1TestFont(t *testing.T, withWidths bool) *Font {
	t.Helper()
	prog := buildT1Program()
	widths := ""
	if withWidths {
		widths = "/FirstChar 65 /Widths [650]"
	}
	f, err := loadFromDict(
		t,
		fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /FontT1 %s /FontDescriptor 2 0 R >>", widths),
		"<< /Type /FontDescriptor /FontName /FontT1 /Flags 4 /FontFile 3 0 R >>",
		fmt.Sprintf("<< /Length %d /Length1 1 /Length2 1 /Length3 0 >>\nstream\n%s\nendstream", len(prog), prog),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return f
}

func pathBounds(t *testing.T, f *Font, gid uint32) (x0, y0, x1, y1 float32) {
	t.Helper()
	p := f.GlyphPath(gid)
	if p == nil || p.IsEmpty() {
		t.Fatalf("GlyphPath(%d) empty", gid)
	}
	x0, y0 = p.Points[0].X, p.Points[0].Y
	x1, y1 = x0, y0
	for _, pt := range p.Points {
		x0, y0 = min(x0, pt.X), min(y0, pt.Y)
		x1, y1 = max(x1, pt.X), max(y1, pt.Y)
	}
	return x0, y0, x1, y1
}

func TestType1Embedded(t *testing.T) {
	f := loadT1TestFont(t, false)
	if f.t1 == nil {
		t.Fatalf("embedded Type 1 program not parsed (substituted instead)")
	}
	if f.sub != nil {
		t.Errorf("substitute loaded despite embedded program")
	}
	// Quad metrics from the FontBBox over the FontMatrix-implied upem (FreeType rule; the float32 matrix reciprocal is
	// inexact, exactly as in the pinned bare-CFF path).
	if a, d := f.Ascender(), f.Descender(); a < 0.7499 || a > 0.7501 || d > -0.2499 || d < -0.2501 {
		t.Errorf("metrics = %v/%v, want ~0.75/-0.25", a, d)
	}
	// Built-in encoding is the base without /Encoding. Only explicit dup entries fill the table (the "0 1 255 {...}
	// for" idiom is not executed — an unmapped code means .notdef, same net effect).
	if f.GlyphName(65) != "A" || f.GlyphName(200) != "T" || f.GlyphName(66) != "" {
		t.Errorf("built-in encoding not applied: %q %q %q", f.GlyphName(65), f.GlyphName(200), f.GlyphName(66))
	}
	// GIDs are synthetic (.notdef=0, then sorted names: A=1, T=2).
	if f.GID(65) != 1 || f.GID(200) != 2 || f.GID(66) != 0 {
		t.Errorf("GIDs = %d %d %d, want 1 2 0", f.GID(65), f.GID(200), f.GID(66))
	}
	// Outlines come from the program, em-normalized via the FontMatrix.
	x0, y0, x1, y1 := pathBounds(t, f, f.GID(65))
	for _, chk := range []struct {
		name      string
		got, want float32
	}{{"x0", x0, 0.05}, {"y0", y0, 0}, {"x1", x1, 0.45}, {"y1", y1, 0.7}} {
		if diff := chk.got - chk.want; diff > 1e-6 || diff < -1e-6 {
			t.Errorf("A bounds %s = %v, want %v", chk.name, chk.got, chk.want)
		}
	}
	// Without /Widths, hsbw advances win (600/1000 for A, 400/1000 for T, unmapped codes → /MissingWidth 0).
	if w := f.Width(65); w != 0.6 {
		t.Errorf("Width(A) = %v, want 0.6", w)
	}
	if w := f.Width(200); w != 0.4 {
		t.Errorf("Width(T) = %v, want 0.4", w)
	}
}

func TestType1WidthsPrecedence(t *testing.T) {
	f := loadT1TestFont(t, true)
	if w := f.Width(65); w != 0.65 { // /Widths beats hsbw.
		t.Errorf("Width(A) = %v, want 0.65", w)
	}
	if w := f.Width(200); w != 0 { // Present /Widths: gaps mean /MissingWidth, never hsbw.
		t.Errorf("Width(T) = %v, want 0 (MissingWidth)", w)
	}
}

func TestType1EncodingOverride(t *testing.T) {
	// An explicit /Encoding dictionary applies its Differences over the built-in base.
	prog := buildT1Program()
	f, err := loadFromDict(
		t,
		"<< /Type /Font /Subtype /Type1 /BaseFont /FontT1 /Encoding << /Differences [65 /T] >> /FontDescriptor 2 0 R >>",
		"<< /Type /FontDescriptor /FontName /FontT1 /Flags 4 /FontFile 3 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(prog), prog),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.GlyphName(65) != "T" || f.GID(65) != 2 {
		t.Errorf("Differences not applied over built-in base: %q gid %d", f.GlyphName(65), f.GID(65))
	}
	if f.GlyphName(200) != "T" { // The built-in base still shows through where Differences are silent.
		t.Errorf("built-in base lost under /Encoding dict: %q", f.GlyphName(200))
	}
}
