# internal/font/data — embedded font bundle

Everything in this directory (plus `internal/font/encodings_gen.go`) is **generated** by
`go run ./internal/font/data/gen` from locally downloaded upstream inputs (see the generator's package comment
for the input layout). CI never fetches anything: only these committed outputs are used, via `go:embed`, and
the gzipped blobs are decompressed lazily on first use.

## Contents

| File(s) | Contents | Derived from |
| --- | --- | --- |
| `afm.txt.gz` | Glyph-name→width tables for the 14 standard fonts + built-in encodings for Symbol and ZapfDingbats | Adobe Core 14 AFMs |
| `agl.txt.gz` | Adobe Glyph List (glyph name → Unicode) | `glyphlist.txt` |
| `fonts/*.ttf.gz` | Liberation Sans/Serif/Mono, 4 styles each — metric-compatible substitutes for Helvetica/Arial, Times, and Courier | Liberation fonts 2.1.5 |
| `LICENSE-liberation.txt` | SIL Open Font License 1.1 for the Liberation fonts | release archive `LICENSE` |
| `LICENSE-afm.html` | Adobe's redistribution terms for the Core 14 AFMs (must accompany them) | `Core14_AFMs.zip` `MustRead.html` |
| `LICENSE-agl.txt` | BSD-3-style license header of the Adobe Glyph List | `glyphlist.txt` header |

A Noto symbols subset (OFL-1.1) for Symbol/ZapfDingbats glyph *shapes* is planned but not yet bundled; their
widths and built-in encodings (above) are already available, so layout and search work without it.

## Upstream sources (fetched 2026-07-11)

| Input | Version | URL | SHA-256 |
| --- | --- | --- | --- |
| `Core14_AFMs.zip` | 1999-11-10 AFMs (`StartFontMetrics 4.1`) | <https://download.macromedia.com/pub/developer/opentype/tech-notes/Core14_AFMs.zip> | `8c892c3c49553cfd2d2a27c4495b4bb12e2875115be7fd127ed3876df19d8654` |
| `glyphlist.txt` | AGL 2.0 (agl-aglfn @ master) | <https://raw.githubusercontent.com/adobe-type-tools/agl-aglfn/master/glyphlist.txt> | `a3b2f61ced9f3644cc0d4ecde5c59df34ca286c689d9484a43a710a81c466789` |
| `encodings.js` | pdf.js @ master (Apache-2.0) | <https://raw.githubusercontent.com/mozilla/pdf.js/master/src/core/encodings.js> | `eee7b0b49fbf0c27fd1765abeea43621c23da3d1a8a592ad0e5ddfe6743658e3` |
| `liberation-fonts-ttf-2.1.5.tar.gz` | 2.1.5 | <https://github.com/liberationfonts/liberation-fonts/files/7261482/liberation-fonts-ttf-2.1.5.tar.gz> | `7191c669bf38899f73a2094ed00f7b800553364f90e2637010a69c0e268f25d0` |

`encodings.js` is a generation-time *reference* for the four Annex D base-encoding tables (nothing from it is
embedded; the generated `encodings_gen.go` carries the derivation note). The generator cross-checks the derived
StandardEncoding against the AFM character codes — the Core 14 text AFMs are coded in AdobeStandardEncoding —
and fails on any disagreement, so the two independent sources pin each other.
