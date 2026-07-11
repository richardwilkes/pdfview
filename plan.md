# plan.md — pure-Go pdfview port

**Current milestone: M5 complete; M6 (fonts + text) in progress — quad-parity spike + glyph rasterization
DONE (2026-07-11).**

> **Next session start here:** confirm the 4 CI runners went green on the glyph-rasterization commit (fix
> anything that didn't). Text RENDERS now — embedded TrueType (glaive), embedded bare CFF (both IRS forms),
> and Liberation-substituted standard-14, through the pinned code→GID chains, go-text outlines, per-render
> glyph path cache, real accumulated text clips, and user-space stroke pens (see the six new decision-log
> entries). TestTextCorpusPixels enforces text-std14 + hit-quad-split + the six encrypted variants;
> rotate90 is now enforced in TestVectorCorpusPixels; glaive/IRS/std14-styles/subst-metrics/damaged are
> reported unenforced (numbers + why in the M6 status note — AA edge redistribution and substitute
> letterform deltas, NOT layout or mapping bugs; per-file thresholds are an M6-exit decision). The next
> boxes, in plan order: **internal/type1** (PFA/PFB container, eexec, charstrings via go-text psinterpreter
> — also unlocks Type1-FontFile metrics + built-in encodings), **internal/store + maxCacheSize** (glyph
> paths + parsed fonts + decoded images; the per-Run font/image caches and the render device's glyph-path
> cache migrate there; "budget honored under a tiny maxCacheSize" is an exit criterion), **Type0/CID**
> (embedded CMap parsing, Identity-H/V, CIDToGIDMap, CID-keyed CFF charset/FDSelect on cff.go's walkers —
> note go-text refuses sfnts without a cmap table, so CIDFontType2 outlines need a glyf path that does not
> go through otfont.NewFont, or an upstream re-check), **Type3** recursion, **ToUnicode**, FuzzCMap +
> FuzzType1, and the M6-corpus expansion (embedded-Type1/Type0/Type3/CJK + a Tr-mode/text-clip probe file
> with regen'd goldens; also consider a ZapfDingbats-capable bundled face — the ZD line renders blank
> today). The gate const stays "M5" until ALL M6 exit criteria hold.

## Session protocol

1. Read this file top to bottom before touching code. Where it conflicts with `.claude/CLAUDE.md` or README, this
   file wins (those still partly describe the original cgo binding until the M8 rewrite).
2. `./build.sh --all` must be green before you start and when you finish. `main` is the only branch.
3. Do the next unchecked box (or finish the current one). Prefer completing one box well over starting several.
4. Update this file as you go: check boxes, bump `milestone` in `gates_test.go` when a milestone's exit criteria are
   met, append to the decision log (dated) whenever you settle something not already written down, and rewrite the
   "Next session start here" line first thing and last thing.
5. Committing and pushing to THIS repo is authorized: end the session by committing the work with a focused message
   and pushing. Never commit/push the sibling repos (`../pdf`, `../canvas`, `../unison`, `../mupdf`).
6. Oracle regeneration (`oracle/regen.sh`) is local/manual only — CI must stay pure Go and offline.

## Goal

Reimplement the public API of this package as a **pure-Go PDF engine** — COS parsing, encryption, navigation, fonts,
structured text, search — with **all rasterization delegated to `github.com/richardwilkes/canvas`** (locally
`../canvas`), a cgo-free Go port of Skia m142. This eliminates MuPDF (AGPL), ~100 MB of vendored static libs, and all
C-toolchain requirements: the library builds with `CGO_ENABLED=0`.

- `../pdf` (github.com/richardwilkes/pdf, MuPDF 1.27.2 via cgo) stays published as the **behavioral oracle** and
  consumer escape hatch.
- `../mupdf` is AGPL: **run it for behavioral investigation only — never read-and-translate its source.**
- unison's `canvas` branch was evaluated and rejected as the seam: its wrappers are lossy for PDF (affine-only
  matrices, no glyph→path access, GL-coupled surface creation). The engine imports canvas packages directly.
- The prior plan (recoverable via `git show 26de8a9:plan.md`) targeted unison's cgo Skia; its decisions are carried
  forward below except where superseded.

## Invariants (hold in every session)

1. **API frozen**: signatures, exported types, sentinel errors, and `OverallMax*` tunables in `pdf.go` never change.
   Package identifier stays `pdfview`.
2. **Clean-room**: ISO 32000-2 is the spec authority. MuPDF/mutool are run-only. Permissively licensed references OK
   to consult: pdfcpu (Apache-2.0), rsc.io/pdf (BSD), pdf.js (Apache-2.0), x/image (BSD).
3. **Dependency discipline**: only `internal/render` imports `github.com/richardwilkes/canvas/...`; go-text only in
   `internal/font` + `internal/type1`; x/image only in `internal/filter` + `internal/imaging`. Never import
   `canvas/gpu` (keeps purego unlinked). The root package imports only the standard library and `internal/*`.
4. **float32 funnel**: every geometry value the cgo code received as `C.float` (TOC x/y, link rects, dest points,
   search quad corners, page bounds) must round-trip through `float32` before the `float64` scale/floor/ceil math,
   or the exact-value tests will show 1-px off-by-ones. The engine-seam types in `pdf.go` already encode this.
5. **`main` stays green** every session via the gate mechanism; at cutover `pdf_test.go` must be identical to its
   pre-M0 state apart from the fixture path (user-directed, see decision log 2026-07-11): after the M8 gate removal,
   `git diff 26de8a9 -- pdf_test.go` shows only the three `testfiles/corpus/glaive.pdf` path literals.
6. Panics from hostile input never escape the public API: `recover()` in `New`, render, and font loading maps to
   `ErrInternal` / `ErrUnableToOpenPDF` (added when those paths gain engine code; fuzzing enforces it).

## Public API contract (frozen — all in root pdf.go)

The wiring in `pdf.go` is already final: public methods hold the one mutex, check released state, enforce budgets,
and convert coordinates; milestones only fill in the `engineDocument` stub methods at the bottom of the file
(`openEngine`, `needsPassword`, `authenticate`, `pageCount`, `loadPage`, `outline`, `links`, `rasterize`, `bounds`,
`search`) with calls into `internal/*`.

- `New(buffer []byte, maxCacheSize uint64) (*Document, error)` — scans first 1 KB for `%PDF`, else `ErrNotPDFData`;
  unparseable content after the marker → `ErrUnableToOpenPDF`; `maxCacheSize` is the resource-cache budget (0 =
  unlimited, honored from M6).
- Methods: `RequiresAuthentication` · `Authenticate` (result bits must match MuPDF's `fz_authenticate_password`,
  incl. empty-user-password auto-auth: 1=no auth required, 2=user, 4=owner — capture exact semantics from the
  oracle, don't assume) · `PageCount` · `TableOfContents(dpi)` · `RenderPage(page, dpi, maxHits, search)` ·
  `RenderPageForSize(page, maxW, maxH, maxHits, search)` · `Release` (idempotent).
- Behavior pinned by tests: 0-based pages; NRGBA straight alpha, transparent background, stride = 4×w;
  `dpiToScale = min(max(dpi,1)/72, 10)`; hit rects floor(min)/ceil(max) over all four quad corners × scale;
  non-finite coords → 0; external links keep sanitized URI + PageNumber −1; internal links resolve to 0-based page +
  DestPoint ((0,0) when the destination has no explicit coordinate); unresolvable internal links dropped;
  `sanitizeString` on every engine-sourced string; xref-broken PDFs repair-parse (TestInternalLinks: `startxref 0`,
  no xref table); TOC x/y are scaled destination coords on the target page, top-left origin y-down.
- Existing tests that must eventually pass byte-unchanged (gates in parentheses): TestMalformedPDF (M0),
  TestUseAfterRelease (M1), TestInternalLinks (M3), TestRenderPageForSizeLimits (M4), TestPDF (M7 — PageCount 2,
  66 TOC entries with exact spot x/y, 9 exact "GURPS" hit rects, 2 links with exact bounds, stride 3308, 827×1170).

## Gate table

| Test | Gate | Status |
| --- | --- | --- |
| TestMalformedPDF | M0 | passing |
| TestUseAfterRelease | M1 | passing |
| TestInternalLinks | M3 | passing |
| TestRenderPageForSizeLimits | M4 | passing |
| TestPDF | M7 | gated |

`gates_test.go` holds `const milestone`; bump it only when a milestone's full exit criteria are met. All gate lines
and `gates_test.go` itself are deleted at M8.

## Verified building blocks (exact APIs, checked in source — trust these, re-verify only if versions move)

- **canvas** (`../canvas`): `surface.NewRasterN32Premul(w, h int32, props *Props) *Surface` (nil props OK) →
  `.Canvas() *canvas.Canvas`, `.MakeImageSnapshot() *imagecore.Image`;
  `imagecore.Image.ReadPixels(dstInfo, dst, rowBytes, x, y, hint)` — **read back `AlphaTypePremul` (straight copy)
  and keep pdf.go's `unpremultiply` loop**: canvas's own unpremul rounds to nearest-even (imagecore/convert.go:67)
  while pdf.go rounds half-up; letting canvas unpremultiply would break exact parity.
  `canvas.Canvas`: Save/Restore/RestoreToCount, `SaveLayer(bounds, paint)`, `Concat(*geom.Matrix)` (full 3x3),
  `ClipPath(p, raster.ClipIntersect, aa)`, `DrawPath`, `DrawImage`/`DrawImageRect`.
  `path.Path`: MoveTo/LineTo/QuadTo/ConicTo/CubicTo/Close, `SetFillType(FillWinding|FillEvenOdd)`,
  `AddPathMatrix(src, matrix, mode)` (glyph reuse), `Transform`. `Paint` struct fields: Shader, ColorFilter,
  MaskFilter, PathEffect, Color, BlendMode, Style, StrokeWidth, MiterLimit, Cap, Join, AntiAlias.
  `patheffect.MakeDash(intervals, phase)`; `shaders.NewLinearGradient / NewRadialGradient /
  NewTwoPointConicalGradient / NewSweepGradient / NewImage` (TileClamp/Repeat/Mirror/Decal, local matrix,
  SamplingOptions Filter Nearest|Linear); `colorfilter.NewLuma()`; `maskfilter.NewShader`; `raster.BlendMode` has
  all 16 PDF blend modes + `BlendDstIn` (soft masks); `imagecore.NewRasterData` with ColorType RGBA8888 /
  **Alpha8** (alpha-only images tint by paint color = PDF ImageMask stencils) / Gray8, AlphaType
  Opaque/Premul/Unpremul.
- **go-text/typesetting v0.3.4** (already canvas's dep): `font.ParseTTF/ParseTTC` + `Face.GlyphDataOutline(gid)` →
  segments (Move/Line/Quad/Cube) for TrueType/OpenType; `font/cff.Parse([]byte)` + `(*CFF).LoadGlyph(gid)` for bare
  CFF (FontFile3); `font/cff/interpreter` (package `psinterpreter`) is importable and has a `Type1Charstring`
  context — write only the Type1 font-file container parser (PFA/PFB, eexec, /Subrs, /CharStrings) + operator
  handler; the charstring VM is provided.
- **x/image v0.43.0**: `tiff/lzw.NewReader(r, MSB, 8)` implements PDF's LZW EarlyChange=1; EarlyChange=0 uses stdlib
  `compress/lzw` — no in-repo LZW. `ccitt.NewReader` (Group3/Group4, Align, Invert) covers CCITTFaxDecode.
- **stdlib**: compress/flate; encoding/ascii85; image/jpeg incl. CMYK/YCCK/Adobe APP14; crypto/{rc4, aes, cipher,
  md5, sha256, sha512} — everything needed for security handlers R2–R6.
- **Not available pure-Go** (settled scope): JBIG2 → stub (blank + log), minimal generic-region decoder later;
  JPX/JPEG2000 → ship without (blank + log).

## Architecture

### Package layout (`internal/`, dependencies point downward only)

| Package | Responsibility |
| --- | --- |
| `internal/gfx` | Shared geometry: 2x3 affine `Matrix` (float32 elements), `Point`, `Rect`, `Quad`, `Path` (verbs+points, fill rule), `StrokeParams{Width, Cap, Join, MiterLimit, Dash, DashPhase}` |
| `internal/cos` | Lexer, objects (Null/Bool/Int/Real/String/Name/Array/Dict/Stream/Ref), classic+stream+hybrid xref, /Prev chains, object streams, repair scan (rebuild from `N G obj` sweep), trailer, resolver with cycle guard, text-string decode (UTF-16BE/UTF-8 BOM/PDFDocEncoding) |
| `internal/filter` | Flate, LZW (both EarlyChange modes via x/image + stdlib), ASCIIHex, ASCII85, RunLength, PNG/TIFF predictors, chain application with expansion caps |
| `internal/crypt` | Standard security handler R2/R3/R4/R5/R6, RC4 + AES-CBC, crypt filters, per-object keys, auth-status bits matching the oracle |
| `internal/doc` | Page tree (+ inherited Resources/MediaBox/CropBox/Rotate), destinations (all /Fit kinds), named dests (/Dests dict + /Names name tree), outline walk, link annotations, external-URI classification, page-space→device-space transform (MediaBox + Rotate + y-flip) |
| `internal/function` | PDF functions type 0/2/3/4 (sampled, exponential, stitching, PostScript calculator) |
| `internal/color` | DeviceGray/RGB/CMYK, CalGray/CalRGB (approx), Lab, ICCBased (N-component fallback), Indexed, Separation/DeviceN (tint transforms) → RGBA |
| `internal/type1` | Type1 font-file container: PFA/PFB, eexec + charstring decryption (r=4330), /Subrs, /CharStrings, builtin Encoding; charstrings run through go-text `psinterpreter` with our handler (hsbw/seac/flex/othersubrs) |
| `internal/font` | Font semantics: descriptors, FontFile/2/3 dispatch, encodings (Standard/WinAnsi/MacRoman/MacExpert + Differences + AGL names), standard-14 metrics + substitution, Type0/CID (CMaps, Identity-H/V, CIDToGIDMap), ToUnicode, widths (/Widths, /W, /DW, MissingWidth — PDF widths authoritative), Type3 CharProcs + FontMatrix; `Font.GlyphPath(gid)` in glyph space |
| `internal/font/data` | go:embed bundle: Liberation Sans/Serif/Mono ×4 styles (OFL-1.1), Noto symbols subset (OFL-1.1), Adobe Core-14 AFM-derived width tables; license texts committed; compressed, lazily decompressed |
| `internal/device` | The seam: `Device` interface + payloads (`TextRun`, `Glyph`, `Paint`); `Tee(...Device)` |
| `internal/content` | Content-stream tokenizer + interpreter: graphics-state stack, path ops, all text ops, XObjects, inline images, ExtGState, patterns/shadings dispatch, Type3 recursion (depth-limited), marked content ignored; emits Device calls; guarantees balanced clip/group push/pop |
| `internal/imaging` | Image XObject decode: DCT (+Adobe transforms), CCITT, JBIG2 minimal, JPX stub, bpc 1/2/4/8/16 unpack, Decode arrays, Indexed, ImageMask→Alpha8, SMask/Mask/color-key |
| `internal/shading` | Shading types 1–7 parsed to normalized form; 4–7 tessellated (subdivide until ΔRGB < 1/255) |
| `internal/store` | maxCacheSize-budgeted LRU (fz-store analog): glyph paths, decoded images, parsed fonts, with byte-size estimates |
| `internal/stext` | Structured text device: chars with device-space quads, line/word assembly, space inference; `fz_search_stext_page`-compatible search |
| `internal/render` | **Sole canvas importer.** Draw device: fills/strokes/dash/clips, text as outline paths, images, shadings→shaders, tiling patterns, groups/soft masks via SaveLayer, blend mapping, Alpha8 stencils, premultiplied readback |
| `internal/testsupport` | Golden/parity comparison helpers (truth.json loader, perceptual pixel diff) |
| root `pdfview` | Frozen public API, mutex/released, limits, sanitize, float32 funnel, engine seam |
| `oracle/` | Separate module (own go.mod + `replace github.com/richardwilkes/pdf => ../../pdf`); never imported by the library |

### Device seam

One interpreter, N devices; `device.Tee(render, stext)` gives a single content pass per render call:

```go
type Device interface {
    FillPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix, paint Paint)
    StrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix, paint Paint)
    ClipPath(p *gfx.Path, evenOdd bool, ctm gfx.Matrix)                // push
    ClipStrokePath(p *gfx.Path, sp *gfx.StrokeParams, ctm gfx.Matrix)  // push
    FillText(run *TextRun, paint Paint)
    StrokeText(run *TextRun, sp *gfx.StrokeParams, paint Paint)
    ClipText(run *TextRun)   // accumulated across BT..ET, pushed at ET
    IgnoreText(run *TextRun) // Tr 3: stext records, render no-ops
    FillImage(img *imaging.Image, ctm gfx.Matrix, alpha float64)
    FillImageMask(img *imaging.Image, ctm gfx.Matrix, paint Paint)
    ClipImageMask(img *imaging.Image, ctm gfx.Matrix)                  // push
    PopClip()
    BeginGroup(bbox gfx.Rect, isolated, knockout bool, blend Blend, alpha float64)
    EndGroup()
    BeginMask(bbox gfx.Rect, luminosity bool, backdrop color.NRGBA)    // then mask content
    EndMask()                                                          // switch to masked content
    PopMask()                                                          // apply + pop
    FillShading(sh *shading.Shading, ctm gfx.Matrix, alpha float64)
}
```

- `Paint` carries exactly one of: resolved NRGBA color, `*shading.Shading`, or `*Tiling` (cell bbox/matrix/steps +
  replay func), plus folded alpha and a blend enum. The interpreter owns colorspace/function resolution.
- `TextRun{Font, Glyphs, WMode}`; `Glyph{GID, Code, Unicode, Trm gfx.Matrix, Advance}` — Trm is the fully composed
  glyph→device matrix; render fills cached glyph-space outlines via `AddPathMatrix(outline, trm)`; stext builds
  quads from Trm × [0..adv, desc..asc].
- Render maps clip/group pushes to `Save`+`ClipPath` / `SaveLayer` and pops to `RestoreToCount`; stext ignores clips
  (search is unclipped, matching MuPDF).
- Soft masks: `BeginMask` renders mask content to its own offscreen surface; `PopMask` draws the snapshot over the
  masked-content `SaveLayer` with `BlendDstIn` (+ `colorfilter.NewLuma()` when luminosity), then restores.

### Rendering mapping (PDF construct → canvas)

`f`/`f*` → `SetFillType` + `DrawPath(StyleFill)` · `S` + `d` → `StyleStroke` + `MakeDash` (hairline for width 0,
calibrate against the oracle) · `B`/`B*`/`b`/`b*` → fill then stroke (two draws — order matters under transparency)
· `W`/`W*` → `Save` + `ClipPath(Intersect, AA)` after the paint op · text Tr 0–2 → merged outline path per run;
Tr 4–7 accumulate → clip at ET · image XObjects → `NewRasterData` + `DrawImageRect` under `Concat(ctm)`;
/Interpolate → Filter Linear/Nearest · ImageMask → Alpha8 image tinted by paint color · axial/radial →
Linear/TwoPointConical gradients (Function sampled to ≤256 stops; /Extend via TileClamp/TileDecal, mixed extend →
two draws) · function-based (type 1) → evaluate over a domain grid → image shader · mesh types 4–7 → tessellated
flat triangles · tiling patterns → render one cell recursively to an offscreen surface → `shaders.NewImage`
(TileRepeat) · transparency group → `SaveLayer` with mapped blend + alpha · blend names map 1:1
(Normal→SrcOver … Luminosity→BlendLuminosity) · Type3 glyphs → interpreter recursion with FontMatrix∘Trm, d1 forces
the current fill paint · page background: none (the surface starts transparent).

### Font pipeline (the long pole)

| PDF font | Parse | Outlines | Encoding→glyph | Widths |
| --- | --- | --- | --- | --- |
| TrueType (FontFile2) | go-text ParseTTF | GlyphDataOutline | Differences/base → AGL name → cmap(3,1); symbolic: (3,0) with 0xF000 fold, then (1,0); fallback code→GID | /Widths else hmtx |
| Type1 (FontFile) | internal/type1 | psinterpreter Type1 handler | Differences → builtin → StandardEncoding | /Widths else hsbw |
| CFF (FontFile3/Type1C) | go-text cff.Parse | LoadGlyph | name→GID from GlyphName sweep | /Widths else CFF |
| OpenType (FontFile3) | go-text ParseTTF | as TrueType | as TrueType | /Widths |
| Type0/CIDFontType2 | go-text ParseTTF | as TrueType | CMap (embedded parsed; Identity-H/V built in; predefined CMaps corpus-driven from Adobe cmap-resources, BSD-3) → CID → CIDToGIDMap | /W, /DW |
| Type0/CIDFontType0 | go-text cff.Parse | LoadGlyph; CID→GID via our own charset/FDSelect reader (Adobe TN5176) | CMap → CID | /W, /DW |
| Type3 | none | CharProcs via interpreter recursion | Differences (required) | /Widths × FontMatrix |

Non-embedded fonts substitute deterministically from the embedded bundle by descriptor flags + BaseFont name parsing
(`,Bold`, `-Oblique`); PDF /Widths always win, so layout/search/TOC stay oracle-exact and only glyph shapes drift.
Never consult system fonts. Glyph cache key `{fontKey, gid}` → glyph-space path in `internal/store`.

### Search compatibility

Replicate `fz_search_stext_page` black-box (from oracle observation, never its source): Unicode simple case-folding;
needle whitespace matches any run of extracted whitespace (incl. synthesized inter-word spaces and line breaks);
scan stext chars in emission order; one quad per line touched per match (union of the matched chars' quads); budget
`min(maxHits, OverallMaxHits)`. Verify quads pre-scale (tolerance 0.01) and post-scale ints (exact) against oracle
dumps across needles covering case, multi-word, and wrapped matches. Fallback: if a pdf_test.go rect stays ±1
despite the float32 funnel and asc/desc calibration, do a documented one-time re-baseline of that literal (decision
log entry) — never a tolerance in the test.

## Oracle, corpus, testing

- `oracle/` module: `oracle dump -in file.pdf -out testfiles/goldens/<name>/ -dpi 72,100,150 [-password ...]
  [-search needle]...` → one `truth.json` per corpus file (sha256, mupdf version, pageCount, auth attempts×status,
  TOC tree with unscaled page-space coords AND scaled ints per dpi, per-page links/search quads/render PNGs) +
  `regen.sh`. Corpus + goldens are committed; regeneration is local/manual and diffs are reviewed at commit time.
- `testfiles/corpus/`: existing GLAIVE fixture + handcrafted minimal PDFs (vectors, text-std14, embedded
  TTF/CFF/Type1/Type0, shadings 2/3, CCITT, imagemask, rotate90, damaged-xref set) + encrypted variants
  (R2/R3/R4-RC4/R4-AES/R6, generated offline via qpdf/pdfcpu as dev-time tools, outputs committed) + 2–3 vetted
  public-domain real PDFs. ~10–20 MB total.
- Pixel comparison (`internal/testsupport`): exact assert on dims/stride; perceptual on straight-alpha NRGBA —
  initial gate: ≤2% of pixels with Δ>24 AND ≤10% with Δ>8 AND mean Δ≤2; per-file overrides in
  `goldens/<name>/thresholds.json`, only ever tightened; achieved numbers recorded per milestone here.
- Fuzz targets (seeds from corpus): FuzzOpen, FuzzFilters, FuzzCrypt, FuzzContent, FuzzCMap, FuzzType1, FuzzImaging,
  FuzzShading. CI runs a short fuzz smoke; long runs are local.

## Resource limits & robustness

`OverallMax*` semantics preserved exactly (incl. the RenderPageForSize pre-check before allocation). New internal
caps (consts, documented where defined): resolve-chain 64; container nesting 512; page-tree depth 64 + visited set;
q/Q depth 256; XObject/pattern/Type3 recursion 12 + per-page cycle set; filter chain 8; decompressed-stream cap
max(64 MB, 256×compressed); CMap ranges 65536. No timeouts — termination is guaranteed by the caps (document this).
Concurrency contract unchanged: one mutex, `-race` in CI.

## Milestones

Rough total: ~35–45 sessions, ~30–35k LOC (excluding embedded font data). Each milestone ends with
`./build.sh --all` green and this file updated.

### M0 — Scaffolding, oracle, corpus (2–3 sessions)

- [x] Write this plan.md (2026-07-11)
- [x] Strip cgo: delete `lib/`, `include/`, `update_from_release.sh`, `setup-windows.ps1`; rewrite `pdf.go` pure-Go
      with the frozen API wired to a stubbed engine seam (`openEngine` → ErrUnableToOpenPDF until M1) (2026-07-11)
- [x] Gate mechanism: `gates_test.go` + gate lines in `pdf_test.go` (2026-07-11)
- [x] CI rewrite: matrix → ubuntu-22.04, ubuntu-22.04-arm, macos-26, windows-2022; drop CGO_ENABLED + llvm-mingw
      step; add explicit `CGO_ENABLED=0 go build ./...` check (2026-07-11)
- [x] `.gitattributes` trim; `.claude/CLAUDE.md` interim banner pointing here (2026-07-11)
- [x] `oracle/` module (own go.mod + `replace github.com/richardwilkes/pdf => ../../pdf`): `dump` command producing
      `truth.json` + PNGs per corpus file at dpi 72/100/150 with per-password auth attempts and per-needle search
      quads; `regen.sh` (2026-07-11)
- [x] Seed `testfiles/corpus/`: GLAIVE + internalLinkPDF (extracted to a file) + handcrafted minimal PDFs (vectors,
      text-std14, rotate90, damaged-xref set to start; more added per milestone as needed) (2026-07-11)
- [x] Encrypted variants (R2/R3/R4-RC4/R4-AES/R6) generated offline and committed (2026-07-11: qpdf 12.3.2, user/owner
      passwords "user"/"owner", plus an R6 empty-user-password variant for the auto-auth semantics)
- [x] 2–3 vetted public-domain real-world PDFs (license noted in a corpus README) (2026-07-11: IRS f1040 + fw9,
      US-government works, PD under 17 U.S.C. §105)
- [x] Generate + commit first goldens; `internal/testsupport` + a pure-Go parity test skeleton that walks
      `testfiles/goldens/` (2026-07-11: 16 corpus files, 16 goldens — 75 PNGs, ~10.4 MB total; regen verified
      byte-identical across two runs; GLAIVE golden reproduces every exact literal in TestPDF)

Exit: build+lint green on all 4 CI runners; TestMalformedPDF green; goldens regenerate locally via `regen.sh`.

### M1 — COS layer (4–6 sessions; done in 1, 2026-07-11)

- [x] Lexer + object model (Null/Bool/Int/Real/String/Name/Array/Dict/Stream/Ref) (2026-07-11)
- [x] `internal/filter` core now (xref streams need Flate + PNG predictors): Flate, LZW, AHx, A85, RL, predictors
      (2026-07-11: LZW EarlyChange both modes verified against the ISO 32000-2 7.4.4.2 example plus a test-only
      encoder round trip; PNG predictors all five filter types; TIFF predictor 8/16-bit)
- [x] Classic xref + /Prev chains + hybrid; xref streams; object streams (2026-07-11: first-seen-wins merge gives
      incremental-update precedence; hybrid order classic > /XRefStm > /Prev)
- [x] Repair scan (`N G obj` sweep) when startxref/xref is broken or inconsistent (2026-07-11: triggered by
      unreadable xref at open, unusable /Root, or any object-load failure — bad offset, wrong header — once per
      document)
- [x] Resolver with cycle guard; text-string decoding (UTF-16BE/UTF-8 BOM/PDFDocEncoding) (2026-07-11)
- [x] Page-tree walk for `pageCount`; wire `openEngine`/`pageCount`/`loadPage` existence checks (2026-07-11)
- [x] FuzzOpen + FuzzFilters; recover() guards in New (2026-07-11: FuzzOpen found a real zero-length-stream slice
      underflow in `captureRawStream` before the fix; the crasher is committed as a regression seed under
      internal/doc/testdata/fuzz)

Exit: TestUseAfterRelease green (bump gate const to "M1") — done; PageCount parity across corpus incl. damaged set
— done (TestParity at M1 plus internal/doc's TestCorpusPageCounts, which checks all 16 files including the
encrypted set against truth.json).

### M2 — Encryption (3–4 sessions; done in 1, 2026-07-11)

- [x] Standard security handler R2/R3/R4 (RC4 + AESV2)/R5/R6 (AESV3); crypt filters incl. Identity; per-object keys
      (2026-07-11: `internal/crypt`, stdlib crypto only — Algorithms 1/2/2.A/2.B/4/5/6/7; R5 covered by a white-box
      round-trip since the corpus has no R5 file; all R2–R6 corpus content streams decrypt to valid content)
- [x] String/stream decryption hooks in cos; /Encrypt-aware trailer handling (2026-07-11: `cos.Decryptor` seam +
      `SetDecryptor`/`DropCaches`; per-object decrypt keyed by the direct object's number+generation, objects inside
      object streams inherit the container's decryption, /Encrypt dict strings and /XRef streams never decrypted)
- [x] `needsPassword` + `authenticate` with oracle-captured bit semantics (incl. empty-user-password auto-auth)
      (2026-07-11: empty-password probe at open drives `NeedsPassword`; bits match all 43 golden attempts exactly)
- [x] FuzzCrypt (malformed /Encrypt dicts) (2026-07-11: seeded from the encrypted corpus, drives open+auth×4+decode;
      30s smoke clean, as are FuzzOpen and FuzzFilters)

Exit: auth-bits parity table (corpus × {"", user, owner, wrong}) equals oracle exactly (done — TestParity at M2 plus
internal/doc's TestAuthBitsMatchGoldens, 43 attempts across 16 files); encrypted docs parse (done — content streams
of every R2–R6 file decrypt; TestCorpusPageCounts still green).

### M3 — Navigation (3–4 sessions; done in 1, 2026-07-11)

- [x] Destination arrays (all /XYZ,/Fit,/FitH,/FitV,/FitR,/FitB,/FitBH,/FitBV kinds) + named dests (/Dests dict +
      /Names name tree) (2026-07-11: internal/doc dest.go; per-kind coordinate slots, null/absent → NaN, integer
      page indices accepted 0-based, name-tree kids/limits with depth cap + visited set, plus MuPDF's
      "#page=/#nameddest=" URI-fragment forms)
- [x] Outline walk feeding `outline()` (exact `buildTOCEntries` budget semantics already in pdf.go) (2026-07-11:
      /First–/Next chains walked iteratively with visited set, depth cap 64, node cap 65536; /Dest or /A /GoTo;
      items without a resolvable internal dest kept at page -1 like MuPDF)
- [x] Link annotations + external-URI classification (`fz_is_external_link` semantics: URI scheme presence)
      (2026-07-11: /Annots order preserved; unusable annots skipped entirely vs unresolvable dests emitted at
      page -1; schemeless URI actions resolve as intra-document fragments; GoToR/Launch → file spec best-effort)
- [x] Page-space→top-left mapping (MediaBox ∩ CropBox + /Rotate + y-flip), NaN for coordinate-less dests
      (2026-07-11: inherited attributes captured in the page-tree walk; all four rotations pinned against oracle
      probes with offset box origins; out-of-spec /Rotate rounds to nearest 90, ties up, in [0,360))
- [x] `page.bounds()` real; float32 funnel through the seam types (2026-07-11: PageSize on internal/doc; every
      geometry value is float32 end to end; interim blank `rasterize` sizes images with the pinned
      fz_round_rect-compatible rounding so TestInternalLinks and dimension parity run now)

Exit: TestInternalLinks green (gate → "M3") — done; fixture TOC 66 entries with exact spot x/y at dpi 100 — done
(TestParity compares all 66 entries at dpi 72/100/150; the three pdf_test.go spot literals verified directly);
TOC/link JSON parity at dpi 72+100 — done (TestParity: scaled-int TOC + links + render dims/stride at all three
recorded DPIs across all 16 goldens; internal/doc TestCorpusNavigation additionally pins the raw float32
page-space outline coords, link rects, dest points, and page bounds against the goldens).

### M4 — Graphics core + wiring (5–7 sessions; done in 1, 2026-07-11)

- [x] `internal/gfx`; content tokenizer + interpreter (paths, q/Q/cm, ExtGState subset, W/W*, all paint ops)
      (2026-07-11: tokenizer reuses the COS lexer via the exported `cos.Lexer` — content streams share COS's
      lexical rules with no ref lookahead; text objects / `sh` / image `Do` / BI..ID..EI recognized and skipped
      in sync; form XObjects recurse; see the decision log for the robustness semantics)
- [x] `internal/device` (interface + Tee) with a null device for fuzzing (2026-07-11: interface verbatim per the
      seam sketch; `internal/imaging` and `internal/shading` exist as placeholder types so the signatures are
      final now; Tee collapses 0/1-device cases)
- [x] `internal/render` on `surface.NewRasterN32Premul`: fills/strokes/dash/clips; premultiplied readback wired into
      `rasterize` (pdf.go's unpremultiply loop stays) (2026-07-11: sole canvas importer; AA always on; clip paths
      pre-transformed to device space so the canvas matrix stays identity between draws; recover→ErrInternal guard
      in rasterize per invariant 6)
- [x] Page CTM + output dimension rounding pinned empirically against oracle dims across page sizes × dpis
      (2026-07-11: dimension rounding was pinned at M3 (all 78 corpus dims); `doc.PageCTM` expresses the M3-pinned
      toTopLeft mapping as a matrix for all four rotations — unit-pinned in internal/doc/render_test.go and
      pixel-verified by vectors + rotate90 at dpi 72/100/150)
- [x] `internal/color` (Gray/RGB/CMYK/Indexed/ICC-fallback/Separation basics) + `internal/function` (0/2/3/4)
      (2026-07-11: device conversions are behavioral tables captured from the oracle — see the decision log —
      plus CalGray/CalRGB approx, Separation /None and /Pattern never-mark handling, DeviceN; all four function
      types incl. the PostScript calculator)
- [x] RenderPage/RenderPageForSize end-to-end; FuzzContent (2026-07-11: TestVectorCorpusPixels enforces the
      vector corpus; FuzzContent drives the interpreter against a balance-checking device over canned resources,
      14.8M execs/30s clean; FuzzOpen additionally drives PageCTM/PageResources/PageContents)

Exit: TestRenderPageForSizeLimits green (gate → "M4") — done; fixture dims 827×1170 stride 3308 exact — done
(TestParity, real render path); vector corpus within thresholds — done (vectors.pdf at dpi 72/100/150: 0.31%,
0.74%, 0.42% over Δ24 against 2% allowed; mean Δ 0.42–1.07 against 2 allowed); race-clean — done.

Full-corpus pixel status at M4 (dpi 72, enforcement stays per-milestone-scope until M8): vectors PASSES;
internal-links exact (Δ0 — blank pages); rotate90 1.18% over Δ24 / mean Δ2.09 (fails only the mean, entirely its
24pt text → M6); text-std14 + its encrypted variants identically 3.41% over Δ24 (decrypted content interprets
byte-identically); damaged set ~2.7–3.8%; glaive 17.5–18.9%; irs-f1040 10.2–11.7%; irs-fw9 7.8–19.8% — all text
(M6) and images (M5).

### M5 — Images (4–5 sessions; done in 1, 2026-07-11)

- [x] `internal/imaging`: DCT (+CMYK/YCCK/APP14), CCITT, JBIG2 stub, JPX stub, bpc unpack, Decode arrays, Indexed
      (2026-07-11: DCT via stdlib image/jpeg with the CMYK samples reconstructed to libjpeg's stored form — see
      the decision log on the inversion; CCITT via x/image/ccitt, BlackIs1→Invert, K>0 degrades to Group3;
      cos.ImageFilterSplit applies the non-image filter prefix and honors the inline /F /DP abbreviations;
      allocation caps run before any pixel buffer exists)
- [x] ImageMask → Alpha8 stencil tinting; SMask/Mask/color-key; inline images (BI/ID/EI) (2026-07-11: stencil
      coverage planes tint through canvas's alpha-only image lane (paint color); SMask decodes to a raw alpha
      plane — never through the painting gray curve — resampled nearest onto the base and overriding any /Mask;
      inline images decode+draw with /L and EI-scan payload isolation and named /CS resource lookup)
- [x] /Interpolate → sampling mapping calibrated vs oracle; FuzzImaging (2026-07-11: absent→FilterNearest,
      true→FilterLinear, both under the outward gridfit of rectilinear image transforms (decision log) — pinned
      by images-interpolate at 0% over Δ24 at all three DPIs; FuzzImaging 30s clean at ~5.3M execs, plus the
      decode-postcondition checks it enforces)

Exit: image corpus within thresholds — done (TestImageCorpusPixels, ungated from M5 on, enforces ten new image
corpus files at dpi 72/100/150); JBIG2/JPX corpus files render blank-not-error — done (images-jpx matches its
golden — MuPDF also drops the image — and images-jbig2 is compared against the images-jpx golden because MuPDF
pads a failed JBIG2 decode into a black square the blank stub deliberately does not reproduce; see the decision
log).

Image-corpus pixel status at M5 (% over Δ24 at dpi 72/100/150): images-indexed and images-ccitt byte-exact
(mean Δ0, max Δ0 at all DPIs); images-raw 0/0/0.07 (max Δ1 at 72/100); images-inline 0/0/0 (max Δ1);
images-imagemask 0/0/0; images-smask 0/0.51/0; images-dct 0/0/0.18 (JPEG decoder differences mean Δ≈1);
images-interpolate 0/0/0; images-jpx and images-jbig2(-vs-jpx-golden) 0/0.15/0 (the nonzero bits are the blue
rect's AA edges). Full corpus at dpi 72 after M5: vectors/rotate90 unchanged from M4; glaive 17.37/18.89;
irs-f1040 10.22/11.71; irs-fw9 7.77–19.82; text-std14 3.41 — images were a minor slice of the real-world diff,
text (M6) is the rest, exactly as the M4 note predicted.

### M6 — Fonts + text rendering (8–12 sessions, the long pole)

- [x] **Quad-parity spike first**: decode GLAIVE page-0 text, compute char quads from our metrics, diff against
      oracle stext quads before building any rendering (de-risks M7's exact hits) (2026-07-11: TestTextQuadParity,
      a permanent root test, enforces EVERY searchRaw quad of 16 corpus files — both GLAIVE pages, text-std14,
      rotate90, damaged ×3, encrypted ×6, both IRS forms, and the three new probe files — at ≤0.5 pt per corner.
      Achieved: glaive max 0.0022 pt over 138 quads, mean 0.0000; every other enforced file exactly 0.0000,
      244 quads total. See the decision log for the metric rules this pinned.)
- [ ] `internal/type1` (PFA/PFB container, eexec, charstrings via psinterpreter)
- [ ] `internal/font`: descriptors, embedded font dispatch, encodings + Differences + AGL, ToUnicode, widths
      (2026-07-11: all landed for simple fonts — descriptors, FontFile2/FontFile3(OpenType) via go-text,
      FontFile3/Type1C Top DICT metrics via our own TN5176 CFF reader, four generated base encodings +
      /Differences + AGL/uniXXXX/uXXXX; 2026-07-11 later: symbolic-TT cmap fallbacks, the full code→GID
      chains, `Font.GlyphPath` outlines (sfnt via go-text Face, Type1C via cff.LoadGlyph + FontMatrix), and
      the hmtx width fallback all landed with glyph rasterization — still open: ToUnicode CMaps, Type 1
      built-in encodings)
- [ ] Standard-14 + substitution via embedded bundle (Liberation ×12 + Noto symbols subset + AFM width tables;
      license texts committed) (2026-07-11: AFM width tables + Symbol/ZapfDingbats built-in encodings + AGL +
      Liberation ×12 fetched, generated, and committed under internal/font/data with licenses + provenance
      README; deterministic std-14 aliasing + flag/name substitution and the oracle-pinned substitute metrics
      are in; 2026-07-11 later: substitution RENDERS — Liberation faces load/cache/map by AGL name→Unicode
      and unparseable embedded programs fall back to them; the Noto symbols subset (shapes only —
      widths/encodings already work) is still to be fetched, and ZapfDingbats renders blank until a
      dingbat-capable face is bundled)
- [ ] Type0/CID: embedded CMap parsing, Identity-H/V, CIDToGIDMap, CID-keyed CFF charset/FDSelect reader
      (2026-07-11: the CFF header/INDEX/DICT walkers the charset/FDSelect reader will build on are in
      internal/font/cff.go)
- [ ] Type3 CharProcs via interpreter recursion
- [ ] Text operators incl. render modes 0–7 + text clip; glyph cache in `internal/store`; maxCacheSize honored
      (2026-07-11: ALL text operators are live in internal/content — BT/ET, full text state incl. Tz/Ts/Tr,
      Td/TD/Tm/T*, Tj/TJ/'/\" — emitting device.TextRun with fully composed Trm matrices and dispatching
      render modes 0–7 to Fill/Stroke/Ignore/ClipText; the interpreter finalizes accumulated text clips with
      the new Device.EndTextClip at ET and at forced stream-end unwind. 2026-07-11 later: GLYPH RASTERIZATION
      LANDED — render.FillText fills merged per-run outlines (nonzero winding, AA) from a per-render
      {font,gid} path cache, StrokeText strokes them with the user-space pen via the new TextRun.CTM,
      ClipText/EndTextClip accumulate a REAL device-space clip, IgnoreText stays invisible; enforced by
      TestTextCorpusPixels + render unit tests. Still open: internal/store, maxCacheSize)
- [ ] FuzzCMap + FuzzType1 (parsers do not exist yet; FuzzContent now covers font loading + all text operators
      via /Font resources — std-14 and a TrueType with Differences + junk FontFile2 — and new text-op seeds;
      2026-07-11 later: FuzzContent's device now pulls every glyph outline like the raster device would, and
      the new FuzzFontProgram drives parseSFNT + parseCFFGlyphBytes + GID chains + GlyphPath on arbitrary
      bytes, 20s smoke clean)

Exit: text corpus within thresholds; GLAIVE full-page diff within threshold; spike corners <0.5 px; CID/CJK/Type3
corpus per oracle; budget honored under a tiny maxCacheSize.

Text-corpus pixel status at glyph rasterization (2026-07-11; % over Δ24 / mean Δ at dpi 72|100|150,
worst page): ENFORCED (TestTextCorpusPixels + rotate90 in TestVectorCorpusPixels) — rotate90 0.70/0.60 |
0.65/0.70 | 0.70/0.76 (was 1.18/2.09 pre-text, its only failure); text-std14 AND its six encrypted variants
byte-identically 1.84/1.35 | 1.87/1.56 | 1.66/1.64 (was 3.41 over Δ24); hit-quad-split 0.39/0.25 | 0.44/0.35 |
0.30/0.26. REPORTED, not yet enforced — glaive p1 8.40/5.55 | 8.80/5.92 | 6.74/4.58 (was 17.4–18.9 over Δ24);
irs-f1040 p0 5.85/4.48 | 6.61/4.64 | 4.85/3.44 (was 10.2–11.7); irs-fw9 worst page p1 12.14/7.79 | 9.91/6.88 |
7.70/5.36 (was 7.8–19.8); std14-styles 6.36/10.60 | 5.87/10.69 | 5.29/10.56; subst-metrics 2.01/2.75 |
1.85/2.87 | 1.60/2.83; damaged ×3 identically 2.08/1.44 | 2.47/2.22 | 1.45/1.31 (p0). The unenforced misses
are NOT layout or mapping errors (quads stay pinned at ≤0.0022 pt): for the embedded-font files, total ink
matches the oracle within ~1% and the diff is FreeType-scanline-vs-Skia-analytic AA redistributing edge
coverage — at glaive/IRS's 7–9 pt body text nearly every glyph pixel is an edge pixel; for the substituted
files it is Liberation-vs-Nimbus letterform deltas (Liberation Mono is visibly heavier than Nimbus Mono,
dominating std14-styles' mean) plus the blank ZapfDingbats line. Whether these get per-file thresholds (with
this justification) or further work (bundled shapes closer to Nimbus, coverage tuning) is an M6-exit decision.

### M7 — Structured text + search (4–6 sessions)

- [ ] `internal/stext`: chars with quads, line/word assembly, space inference, emission order preserved
- [ ] Search per the compatibility spec above; wire `search()` seam with `min(maxHits, OverallMaxHits)`
- [ ] Tee render+stext in one pass per render call

Exit: **TestPDF green including the 9 exact GURPS rects** (gate → "M7"); search-quad parity across corpus × dpi
{72,100,150} × needle set.

### M8 — Advanced graphics, hardening, cutover (6–8 sessions)

- [ ] `internal/shading` types 1–7 + shading/tiling patterns
- [ ] Transparency groups, soft masks, blend modes; knockout/isolated as SaveLayer allows
- [ ] Annotation appearance streams (/AP) rendered like the oracle does
- [ ] Fuzz/soak (corpus × long local runs), race, perf ≤2× cgo wall time on fixture @150 dpi (record the numbers)
- [ ] `DrawPage(c *canvas.Canvas, pageNumber int, ctm geom.Matrix) error` vector API (documented as canvas-coupled)
- [ ] Remove gates (`gates_test.go` + gate lines): `git diff 26de8a9 -- pdf_test.go` must show only the three
      `testfiles/corpus/glaive.pdf` fixture-path literals (see decision log 2026-07-11)
- [ ] Rewrite README + `.claude/CLAUDE.md` for the pure-Go engine; retire this plan or archive it
- [ ] Tag first release

Exit: full parity suite green at committed thresholds; `CGO_ENABLED=0 go build ./...`; example output matches oracle.

## Decision log (append-only, dated)

- 2026-07-05 (carried from prior plan): clean-room rules; repo-is-the-port on `main` (no cgo coexistence); JBIG2
  stub→minimal generic-region decoder; JPX ship-without; `runtime.AddCleanup` dropped; one-mutex threading contract
  kept; mesh shadings software-tessellated; `DrawPage` added at cutover.
- 2026-07-11: rasterize via `github.com/richardwilkes/canvas` directly, not unison (wrappers lossy: affine-only
  matrices, no glyph→path, GL-coupled surfaces) — restores the `CGO_ENABLED=0` goal the prior plan had dropped.
- 2026-07-11: package identifier stays `pdfview` (rename commit 26de8a9 supersedes the prior plan's "keep `pdf`").
- 2026-07-11: LZW via `x/image/tiff/lzw` (EarlyChange=1) + stdlib `compress/lzw` (EarlyChange=0); no in-repo LZW.
- 2026-07-11: read pixels back premultiplied and keep pdf.go's `unpremultiply` (round-half-up) — canvas's unpremul
  readback rounds to nearest-even and would break exact parity.
- 2026-07-11: single content pass per render via `device.Tee` (no display-list intermediate).
- 2026-07-11: corpus + goldens committed to the repo (user-approved; ~10–20 MB, CI stays offline/pure-Go).
- 2026-07-11: bundled fonts approved: Liberation ×12 (OFL-1.1) + Noto symbols subset (OFL-1.1) + Adobe Core-14 AFM
  width tables; deterministic substitution, never system fonts.
- 2026-07-11: CI matrix shrunk to 4 runners (ubuntu amd64/arm64, macos arm64, windows amd64), user-approved.
- 2026-07-11: float32 funnel rule encoded in the pdf.go engine-seam types (bounds/quads/links/outline coords are
  float32).
- 2026-07-11: committing + pushing this repo at session end is user-authorized (sibling repos never).
- 2026-07-11: the oracle gets the raw unscaled page-space floats (outline x/y, link rects, resolved dest points,
  search quads, page bounds) through a small module-local cgo shim (`oracle/raw.go`) compiled against `../pdf`'s
  vendored MuPDF headers/libs, adapting that binding's own fz_try/fz_catch wrapper pattern; `../pdf` itself is
  never modified. Everything the public API exposes (auth bits, TOC ints, links, hit rects, pixels) is recorded
  from the public API, since that is the contract pdfview must match. Non-finite floats encode as JSON null.
- 2026-07-11: auth truth protocol: every Authenticate attempt runs on its own fresh document; the table always
  includes "" and "invalid-password" plus the per-file passwords. Captured semantics to match at M2: unencrypted
  documents return status 1 for ANY password (even a wrong one); an encrypted document with an empty user password
  reports RequiresAuthentication false yet Authenticate("") returns 2 (user), owner password 4 — not 1.
- 2026-07-11: truth.json/PNG determinism: struct-ordered JSON via MarshalIndent (maps sort keys), float32 fields
  marshal at 32-bit shortest-round-trip precision, no timestamps; PNGs pinned to png.BestCompression. Determinism
  holds per Go release (flate/png output could shift across releases; regen diffs are reviewed at commit time).
  regen.sh wipes testfiles/goldens first so corpus removals cannot leave orphaned goldens.
- 2026-07-11: TestParity gates per capability on gates_test.go's milestone const: M1 open+PageCount, M2 auth bits,
  M3 TOC, M4 render success/dims/stride/links, M7 search rects, M8 pixel thresholds. Pixel comparison deliberately
  waits for M8 (full-parity exit) because M4–M7 render real content incrementally (no text before M6, etc.);
  mid-milestone pixel enforcement would violate the main-stays-green invariant. M4+ sessions verify pixels within
  their milestone scope (vector corpus at M4, images at M5, ...) and may tighten via per-file thresholds.json.
- 2026-07-11: the oracle module is checked with gofmt + go vet, not golangci-lint: the root config's fieldalignment
  rule would dictate schema struct order and thus truth.json field order, hurting golden readability (the root
  module's read-side mirror in internal/testsupport is fieldalignment-clean; LoadTruth rejects unknown fields so
  the two schemas cannot drift silently). Compiled oracle binaries are gitignored; only source, regen.sh, corpus,
  and goldens are committed.
- 2026-07-11: glaive.pdf is a byte-identical copy of the original fixture rather than a symlink (Windows checkout
  safety; git stores one blob for both). internal-links.pdf byte-matches the internalLinkPDF constant in
  pdf_test.go, verified via go/ast extraction.
- 2026-07-11: user-directed: the duplicate `testfiles/GLAIVE_Mini_v2_3_for_GURPS_4e.pdf` was removed;
  `testfiles/corpus/glaive.pdf` is the canonical fixture and `pdf_test.go` now reads it. This relaxes the M8
  "pdf_test.go byte-identical" criterion to "identical apart from the three fixture-path literals".
- 2026-07-11 (M1): references to free or absent objects resolve to Null per ISO 32000-2 7.3.10 and are NOT load
  failures; only actual failures (offset out of range, wrong header at offset, unparseable content) trigger the
  once-per-document repair scan and retry. `cos.Document.Resolve` swallows failures into Null so broken refs
  degrade instead of poisoning callers.
- 2026-07-11 (M1): `pageCount` counts actual page-tree leaves via the walk (depth cap 64 + global visited set,
  skipping duplicate/cyclic kids) rather than trusting `/Count`, for robustness on repaired files. Matches the
  oracle on all 16 corpus files (including pre-auth counting of the encrypted set, whose tree dictionaries are
  not encrypted); if a future corpus file exposes a Count-vs-walk divergence, recalibrate against MuPDF then.
- 2026-07-11 (M1): repair precedence: later-in-file object definitions win (incremental-update semantics); object
  streams found by the sweep register their contents only for numbers not directly swept; xref-stream dictionaries
  found by the sweep join the trailer candidates (they carry /Root for xref-stream-only files, which have no
  `trailer` keyword); the pre-repair trailer, when one was readable, is the final fallback.
- 2026-07-11 (M1): filter fault-tolerance contract: corrupt input that still yields some output returns the
  partial output without an error (the warn-and-continue analog of deployed readers); resource-cap violations
  (chain > 8, output > max(64 MB, 256×input) per stage) are always hard errors. Stream /Length is trusted only
  when direct, plausible, and endstream-confirmed; otherwise the payload is recovered by scanning for
  `endstream` (which also covers indirect /Length during xref bootstrap without resolver recursion).
- 2026-07-11 (M1): `openEngine` copies the caller's buffer (`bytes.Clone`) because the engine holds subslices of
  it for the document's lifetime — callers may reuse their buffer, matching the old implementation's C.CBytes
  copy. The recover() guard of invariant 6 lives in `openEngine` (New's only engine call), keeping the frozen
  public section of pdf.go untouched.
- 2026-07-11 (M1): FuzzOpen lives in `internal/doc` (not internal/cos as once sketched) so one target covers
  doc.Open, the page-tree walk, and — via the `COS()` accessor — resolution of every xref'd object plus stream
  decoding. Corpus files are its seeds. FuzzFilters lives in `internal/filter`.
- 2026-07-11 (M2): decryption seam. `internal/crypt` owns the standard security handler; `internal/cos` gains a
  `Decryptor` interface (`DecryptString`/`DecryptStream`, both keyed by object number+generation) installed via
  `SetDecryptor`. cos does not import crypt (dependency points downward); `internal/doc` wires them in
  `setupEncryption`. Decryption is applied per *directly stored* object right after `parseIndirectAt` (in
  `loadObjectUncached` and `loadObjStm`), so an object stream's payload is decrypted once under its own number and
  the objects parsed out of it are never re-decrypted (ISO 32000-2 7.6.2). `parseIndirectAt` now returns the
  object generation for the per-object key. Two exemptions live in cos: the /Encrypt dictionary object (recorded
  from the trailer ref) and any /Type /XRef stream are never run through the decryptor.
- 2026-07-11 (M2): auth-bit semantics reproduce MuPDF exactly and matched the goldens with no surprises. Not
  encrypted → status 1 (`AuthNoneRequired`) for any password. Encrypted → bit 2 iff the password validates as the
  *user* password (Algorithm 6) and bit 4 iff it validates as the *owner* password (Algorithm 7 recovers the user
  password from /O, then Algorithm 6); the owner check sets only bit 4 even though it authenticates internally as a
  user. The empty password is tried once at open: `NeedsPassword` is false iff it authenticated, which is how the
  R6 empty-user variant reports requiresAuth=false yet `Authenticate("")`=2 (user) and `Authenticate("owner")`=4.
  All 43 golden attempts (16 files × recorded passwords) match bit-for-bit.
- 2026-07-11 (M2): a successful `Authenticate` drops the cos object cache and re-walks the page tree, so page
  dictionaries first parsed pre-authentication (keyless) are recaptured decrypted. `buildPageList` is now
  idempotent (resets its slices first). Harmless for M2 (PageCount is structure-only) but required before M3 reads
  page-dict strings. Unencrypted documents short-circuit before this, so the common path is unchanged.
- 2026-07-11 (M2): all crypto primitives are Go stdlib (crypto/{md5,rc4,aes,cipher,sha256,sha512}). The gosec
  weak-primitive checks (G401 hash, G405 cipher) and blocklisted-import checks (G501 md5, G503 rc4) are added to
  `.golangci.yml`'s existing global gosec exclude list, since the PDF standard security handler mandates MD5 and
  RC4 and there is no alternative; this matches the project's established style of excluding gosec checks globally
  rather than scattering inline directives. `saslPrep` is minimal (UTF-8 passthrough + 127-byte truncation, RFC
  4013 profile step omitted); ASCII passwords — effectively all of them — are unaffected.
- 2026-07-11 (M2): FuzzCrypt (in `internal/doc`, seeded from the encrypted corpus) drives open → authenticate with
  each of {"", user, owner, wrong} → decode every stream, so malformed /Encrypt dicts, truncated /O/U/OE/UE, and
  bogus V/R combinations exercise handler construction, key derivation, and per-object decryption. The decryptor
  hooks are total (no error channel deep in object loading): any shortfall — no key, bad key length, malformed
  ciphertext — returns the input unchanged rather than erroring or panicking. 30s smokes of FuzzCrypt, FuzzOpen,
  and FuzzFilters are clean.
- 2026-07-11 (M3): page geometry model, pinned by running the oracle over scratch probe documents (allowed:
  MuPDF is run-only): effective box = CropBox ∩ MediaBox, both normalized (corners sorted); empty intersection →
  MediaBox; absent/degenerate/non-finite MediaBox → US Letter. /Rotate normalizes into [0,360) then rounds to
  the NEAREST multiple of 90 with ties rounding up (probed: 45→90, 100→90, 315→0, -45→0, -90→270, -100→270,
  -315→90, ±450 likewise) — not snap-down, not reject. The top-left mapping per rotation (u,v from PDF-space
  x,y): 0: (x−x0, y1−y); 90: (y−y0, x−x0); 180: (x1−x, y−y0); 270: (y1−y, x1−x) — pinned with offset-origin
  probes (link rects AND dest points). fz_bound_page always reports [0,0,w,h]; TestCorpusNavigation asserts the
  recorded golden bounds start at the origin so a violation would surface. All probe expectations are embedded
  in internal/doc/nav_unit_test.go so the pins survive without the probe files.
- 2026-07-11 (M3): destination semantics (oracle-probed): coordinate slots per kind are XYZ (x,y), FitH/FitBH
  (y), FitV/FitBV (x), FitR (left, top), Fit/FitB/unknown (none); null, absent, or non-numeric slots are NaN;
  the point is mapped through the TARGET page's geometry (so NaN switches axes under 90/270 — inherent in the
  transform); the array's first element may be a page ref or a 0-based integer index (out-of-range → page -1).
  Named lookup order: old-style catalog /Dests dictionary first, then the /Names → /Dests tree; both stores
  accept name and byte-string keys; tree leaves are scanned linearly and kids are pruned by /Limits only when
  the limits are well-formed strings (lenient beyond MuPDF for broken files, untestable divergence). Chains of
  name → dict(/D) → name indirections are capped at 8.
- 2026-07-11 (M3): links: /Annots order is preserved (matches linksRaw). Annotations that are not /Subtype
  /Link, or carry neither /Dest nor a usable action, produce no link at all — distinct from present-but-
  unresolvable destinations, which are emitted at page -1 and dropped by pdf.go (both behaviors match the
  goldens' linksRaw). URI actions: a scheme (RFC 3986 shape, fz_is_external_link semantics) makes the link
  external with the URI kept verbatim (DecodeTextString-decoded); schemeless URIs resolve like the
  intra-document fragments MuPDF synthesizes — "#page=N&zoom=z,x,y" (1-based N; x,y are ALREADY top-left values
  and get no further mapping; "nan" parses to NaN) and "#nameddest=NAME" (percent-decoded) — else page -1.
  GoToR/Launch degrade to their file specification (/UF over /F) as the URI, external iff it carries a scheme
  (best effort; no corpus coverage). Internal links cross the seam with an empty URI; the oracle's synthesized
  internal URIs are deliberately not reproduced since the public API never exposes them.
- 2026-07-11 (M3): the M3 gate requires TestInternalLinks, which calls RenderPage, so `rasterize` gained an
  interim implementation: a fully transparent image at the final dimensions. Extent rounding is
  ceil(float64(float32(extent) × float32(scale)) − 0.001) per axis — float32 multiply (C float parity), then
  MuPDF's fz_round_rect-style epsilon ceil — verified exactly against all 78 recorded corpus render dimensions
  (26 pages × 3 DPIs), including the 595.2→596 @72 case that rules out round-to-nearest and the 625.0-exact
  @150 case the epsilon exists for. Consequently TestParity now enforces render success, dimensions, stride,
  and links from M3 (supersedes the earlier "M4 render success/dims/stride/links" gating note; search stays M7,
  pixels stay M8). M4 replaces the rasterize body but must keep renderExtent.
- 2026-07-11 (M3): outline items are kept even without a resolvable internal destination (page -1, NaN coords) —
  MuPDF lists items whose action is an external URI or unsupported — and titles cross the seam raw
  (DecodeTextString only; pdf.go sanitizes). Outline caps: depth 64, visited set across the whole walk, 65536
  nodes total; page-link cap 65536 per page; name-tree depth 64 (all documented at their consts, per the
  resource-limits section; the public OverallMax* budgets apply on top in pdf.go). FuzzOpen now also drives
  Outline(), PageSize(i), and Links(i) for every page.

- 2026-07-11 (M4): device color conversions are behavioral lookup tables, not formulas. The oracle's MuPDF build
  routes device colorspaces through ICC (lcms): `0 0 0.8 0 k` renders (255,243,79), not the naive additive
  formula's (255,255,51), and DeviceGray is a curve that is not even perfectly neutral (42/255 → 42,42,41).
  Captured run-only via probe PDFs of flat patches rendered through the oracle: DeviceRGB = trunc(float32(v)×255)
  per channel, independent (verified over 3563 patches: 0.5 g → 127 rules out round-half-up, ramp dips rule out
  everything else); DeviceGray = 1021-sample table (i/1020, linear interp); DeviceCMYK = 17^4 grid + multilinear
  interpolation (mean err 0.25, max 1.7 across 2516 off-grid validation observations; a 9^4 grid was tried first
  and rejected — max err 26 at the R-channel gamut-clamp kink near c≈0.69). Tables live in internal/color/data
  (gray1021.bin 3KB, cmyk17.bin.gz 206KB); `oracle/colorprobe` regenerates them AND fails loudly if the observed
  conversion changes shape (RGB non-trunc or CMYK interp error growth), signalling rework rather than re-commit.
- 2026-07-11 (M4): the content tokenizer is the exported `cos.Lexer` (content streams share ISO 32000-2 7.2
  lexing exactly); operand assembly lives in internal/content because content has no indirect references — "R"
  never triggers lookahead — and dict/array/inline-image forms are content-specific. Lexical errors skip forward
  (the lexer guarantees position progress), so hostile bytes cannot wedge or desync the scan.
- 2026-07-11 (M4): interpreter robustness semantics (all cap consts documented at their definitions): unknown
  operators are skipped with the operand list reset; known operators with missing or mistyped operands are
  skipped the same way; operands are consumed positionally from the list's start and the list is a sliding
  window keeping the newest 64 (floods drop the oldest, which no well-formed operator can notice); q beyond
  depth 256 is ignored with its matching Q ignored via an overflow counter (the counter is only nonzero at full
  depth, so pairing survives); Q at the executing stream's floor is ignored — form content cannot pop its
  caller's states; a per-Run budget of 2^22 executed operators bounds total work across form recursion; stream
  end auto-unwinds states and clips so the device's push/pop pairing always balances (fuzzed via a
  balance-checking device that panics on violation).
- 2026-07-11 (M4): form XObjects: only /Subtype /Form recurses (images no-op until M5); depth cap 12 plus a
  reference cycle set (entries removed on exit, so diamonds replay but cycles cut); execution is
  q + /Matrix concat + /BBox clip + own-/Resources frame (inheriting the caller's when absent) with fresh
  per-stream path/operand/pending-clip state, then Q. Color-space parses are cached per resource frame by name
  (negative results too) so hostile cs loops cannot force repeated stream decodes.
- 2026-07-11 (M4): W/W* semantics: the pending clip is applied after the painting operator, using the complete
  current path and the W-variant's fill rule; an empty path still clips (to nothing), matching viewers. Paints
  whose active space is /Pattern (until M8) or /Separation /None emit no device calls at all; both spaces also
  report a fully transparent color so nothing marks even if something slips through.
- 2026-07-11 (M4): dash handling is split: the interpreter enforces PDF-level validity — arrays truncate at 32
  entries (MuPDF's stroke-state capacity), a negative or non-finite entry invalidates the whole d operator
  (previous dash kept) — while the raster device adapts to the stroker: odd-length arrays are doubled (PDF's
  alternating on/off repetition expressed literally), empty or all-zero arrays render solid, and anything
  MakeDash rejects falls back to solid. Line width 0 maps to the stroker's hairline (1 device pixel), matching
  the oracle within thresholds; ExtGState's LW/LC/LJ/ML/D/CA/ca/BM subset is applied at M4, the rest ignored
  until their milestones.
- 2026-07-11 (M4): rendering is antialiased everywhere (Skia analytic AA vs MuPDF's scanline aa-8); the residual
  vector-corpus diffs are pure edge-coverage differences (vectors.pdf: ≤0.74% of pixels over Δ24, interiors
  byte-exact thanks to the color tables). Rendered dimensions come from renderExtent exactly as pinned at M3;
  the raster surface is created at those dimensions so stride/geometry parity is structural.
- 2026-07-11 (M4): TestVectorCorpusPixels (root) is the milestone-scope pixel gate: vectors.pdf is enforced at
  dpi 72/100/150 ungated — the real render path must keep passing it from M4 on — while rotate90.pdf is reported
  unenforced until M6 supplies its text. TestParity's role is unchanged (dims/stride/links; full pixel
  enforcement waits for M8 per the earlier decision).
- 2026-07-11 (M5): CMYK DCT inversion, oracle-pinned: MuPDF consumes libjpeg's raw output, which leaves Adobe
  CMYK (APP14 transform 0) and YCCK (transform 2) samples in their stored, INVERTED form, and applies no
  inversion of its own — the transform-0 corpus JPEG renders near-black under an identity /Decode, and files
  that intend true ink values carry /Decode [1 0 1 0 1 0 1 0]. Go's image/jpeg undoes the inversion in both
  cases, so internal/imaging reconstructs the stored bytes (255−v per channel, equivalent for both transforms)
  before /Decode, color-key comparison, and the M4 behavioral CMYK table. One accepted divergence: a
  4-component JPEG with no Adobe marker is rejected by Go's decoder (renders blank); libjpeg-based readers
  treat it as non-inverted CMYK. No corpus coverage; revisit only if a real file surfaces.
- 2026-07-11 (M5): image gridfit, pinned behaviorally against golden edge probes (never MuPDF source): a
  rectilinear image transform (axis-aligned in either axis order) has its device extent snapped OUTWARD per
  axis — floor(min edge), ceil(max edge), computed on the float32 matrix values (a 425.0 edge that float32
  arithmetic produces as 424.99997 snaps to 424; a 154.166 max edge to 155; a 187.5 max edge to 188) — before
  drawing, which reproduces MuPDF's hard, pixel-snapped image edges and eliminated the AA-edge seam rows/columns
  that dominated the first-pass diffs at fractional scales. Non-rectilinear (rotated/skewed) image transforms
  are not snapped and keep antialiased edges; no corpus file exercises them yet.
- 2026-07-11 (M5): /Interpolate maps absent/false→FilterNearest, true→FilterLinear (canvas sampling), calibrated
  by images-interpolate.pdf: 0% over Δ24 at all DPIs with this mapping, and the alternative (nearest for both)
  would fail catastrophically on the /Interpolate true half, so the goldens pin it. MuPDF's smoothing formula is
  not bit-identical to canvas bilinear (means ≈1.2, max Δ≈21 on the smoothed half) but sits far inside
  thresholds. Minification-heavy content (where MuPDF box-subsamples regardless of /Interpolate) has no corpus
  coverage yet; the image corpus deliberately magnifies.
- 2026-07-11 (M5): stub-codec semantics: imaging returns ErrUnsupportedCodec for JBIG2Decode/JPXDecode (with an
  slog.Debug note), the interpreter skips the draw, and the page renders blank where the image would be — never
  an error. MuPDF drops a failed JPX image the same way (its golden is the blank page), but PADS a failed JBIG2
  decode into a black square; the blank stub deliberately does not emulate that, so TestImageCorpusPixels pins
  images-jbig2 against the images-jpx golden, whose page content is byte-identical apart from the codec name.
  The corpus payloads are crafted so both decoders fail cleanly in MuPDF too (truncated JBIG2 segment header;
  JPX bytes without a signature box) — a longer arbitrary JBIG2 payload can PARSE and produce junk pixels, which
  is why the truncation matters.
- 2026-07-11 (M5): image resource caps (documented at their consts, per "Resource limits & robustness"):
  decoded pixels ≤ maxImagePixels (2^26, sized for 600-dpi letter scans) AND ≤ max(2^22, 8192×payload bytes) —
  the proportional term stops hostile dictionaries from claiming huge dimensions over a few bytes (truncated
  payloads read as zero samples, so such claims only amplify allocation), while the floor and multiplier
  accommodate CCITT-class extreme compression (an all-white fax row is a few bytes). Both run before any
  allocation, including for /SMask and /Mask sub-decodes and the DCT path (jpeg.DecodeConfig first). Truncated
  CCITT output is completed with white, matching viewer degradation. The interpreter caches decoded images
  per Run keyed by resource ref (failures cached too), capped at 32 entries; the budgeted store arrives at M6.
- 2026-07-11 (M5): image sample conversion rides the M4 behavioral color tables (a CMYK JPEG pixel converts
  exactly like a k operator's operands). Two sub-Δ8 residuals accepted: MuPDF converts gray/CMYK IMAGE pixmaps
  through lcms's 8-bit-optimized transforms, which round ±1 differently from the float fill path the M4 tables
  captured (some gray samples land one off); and DeviceRGB 8-bit samples pass through byte-identical in both
  engines (trunc(float32(s)/255×255) is the identity for every byte, verified exhaustively). Single-component
  spaces at bpc ≤ 8 convert through a precomputed sample→NRGBA LUT; multi-component and 16-bpc images convert
  per pixel.

- 2026-07-11 (M6 spike): methodology — the spike became a permanent root test (TestTextQuadParity) rather than
  throwaway tooling: a capture device records every glyph the interpreter emits (deduplicating runs delivered
  through several verbs by run identity), computes per-char quads as Trm × [0..advance, desc..asc] exactly as
  the M7 stext device will, locates each golden's needles with a small fz-search-compatible matcher (simple
  case folding; needle whitespace matches space-char runs, gaps ≥ 0.2 em, or line breaks detected
  perpendicular to the advance direction so rotated text works), and diffs all corners against searchRaw.
  Results: glaive (10 embedded TrueType subsets) max corner error 0.0022 pt / mean 0.0000 over 138 quads;
  text-std14, rotate90, std14-styles, subst-metrics, hit-quad-split, both IRS forms, damaged ×3, encrypted ×6
  all EXACTLY 0.0000; 244 quads enforced at ≤0.5 pt. The M7 search implementation must graduate this matcher
  into internal/stext against the same goldens.
- 2026-07-11 (M6): quad-metric rules, all oracle-pinned behaviorally (probe corpus files committed):
  EMBEDDED fonts use the font program, FreeType-style — sfnt: hhea ascender/descender, falling back to OS/2
  typo then win metrics when hhea has none, over head's upem (glaive pins this: macOS Helvetica subsets,
  hhea 1577/−471 @2048); bare CFF (FontFile3/Type1C): FontBBox yMax/yMin over the FontMatrix-implied upem,
  read by our own TN5176 Top DICT parser since go-text's cff package does not expose them (irs-f1040 went
  exact only with this rule, proving descriptors do NOT override embedded programs). SUBSTITUTED
  (non-embedded) fonts: when the dictionary has a FontDescriptor, /Ascent and /Descent each apply when
  nonzero with per-slot defaults 0.8/−0.2 — EVEN for standard-14 BaseFonts (subst-metrics.pdf pins all of
  this, including the fw9 HelveticaLTStd-Bold case, zero/absent/one-sided slots); descriptor-less standard-14
  fonts get MuPDF's bundled-substitute FontBBox values, recovered as exact integers from the std14-styles
  probe (Helvetica 1075/−299, ..., Symbol 1010/−293 — see internal/font's nimbusMetrics). ZapfDingbats is
  UNPINNABLE via search (MuPDF extracts no searchable Unicode for it: the AGL proper lacks the aN names);
  its entry carries the Adobe AFM FontBBox 820/−143 as a documented stand-in.
- 2026-07-11 (M6): width rules — PDF /Widths always wins (FirstChar-indexed; unresolvable entries and codes
  outside the array take /MissingWidth, default 0); a font with NO /Widths array at all takes the bundled
  Adobe Core-14 AFM widths by glyph name through its encoding (std14-styles verified every style's advances
  exactly — MuPDF's Nimbus substitutes are AFM-metric-compatible); embedded fonts without /Widths will use
  hmtx once GID mapping lands (no corpus coverage yet). Word spacing applies to single-byte code 32 only;
  TJ numbers kick the text matrix by −n/1000×Tfs×Th; all Trm composition is [Tfs·Th 0, 0 Tfs, 0 Ts]·Tm·CTM
  in float32.
- 2026-07-11 (M6): search-hit quad grouping, pinned by probes (hit-quad-split.pdf commits the three
  behaviors): walking a match's chars in order, a char whose vertical extent stays within a fraction of the
  current quad's height extends the quad horizontally WITHOUT changing its vertical extent (the FIRST char's
  extent wins — a slightly-taller bold inter-word space in fw9 does not stretch the quad), while a char
  diverging further (a 40 pt space amid 20 pt words) closes the quad and starts its own, so one match can
  yield several quads even on one line; a trailing space before a line wrap belongs to the match. The split
  threshold was bisected into (0.101, 0.113) × height (20 pt text: a 22.6 pt space merges, 22.9 splits);
  the matcher uses 1/9. Corpus quads sit far from the bracket; if a real file ever lands near it, re-bisect
  with more probes before trusting the constant.
- 2026-07-11 (M6): the embedded data bundle lives in internal/font/data (README.md there documents exact
  URLs, versions, sha256s): Adobe Core-14 AFM widths + Symbol/ZapfDingbats built-in encodings (afm.txt.gz,
  8 KB), the Adobe Glyph List (agl.txt.gz, 23 KB), and Liberation 2.1.5 ×12 gzipped TTFs (~2.4 MB total,
  OFL-1.1) — committed with license texts (AFM MustRead.html redistribution notice, AGL BSD-3 header, OFL).
  The four Annex D base encodings are GENERATED Go tables (internal/font/encodings_gen.go) derived from
  pdf.js's encodings.js (Apache-2.0) and cross-checked at generation time against the AFM character codes
  (1788 entries agree), so the two independent sources pin each other. Everything regenerates offline via
  `go run ./internal/font/data/gen` from pre-fetched inputs; CI touches none of it. go-text/typesetting is
  now a direct dependency (was indirect via canvas; same v0.3.4, no replace).
- 2026-07-11 (M6): device-seam addition: `EndTextClip()` joined the Device interface — ClipText only
  accumulates, and the interpreter emits exactly one EndTextClip per text object that produced ClipText calls
  (at ET, at a forced text-object close on nested BT, and at stream-end unwind), counting it as one clip
  level toward the balance contract. The render device pushes a no-op clip level until glyph outlines land
  (clipping to nothing would wrongly erase content; clipping to glyphs needs the glyphs).
- 2026-07-11 (M6): text-op robustness (matching the oracle's operator-level recovery): a failed font load
  aborts Tf and KEEPS the previous font and size; show operators without a usable font are skipped entirely
  (no matrix advance — MuPDF cannot advance without widths either); text ops outside BT..ET operate against
  identity matrices; nested BT force-closes the open text object; per-glyph work drains the same maxTotalOps
  budget as operators so huge show strings cannot amplify work; fonts are cached per Run by resource
  reference (failures cached as nil), capped at 64 entries until internal/store lands.
- 2026-07-11 (M6): corpus grew by three generated probe files (std14-styles, subst-metrics, hit-quad-split —
  see testfiles/corpus/README.md) whose goldens pin substitute metrics, descriptor precedence, and hit-quad
  grouping; regen.sh reruns confirmed all pre-existing goldens byte-identical (determinism holds). The
  Symbol probe line searches as αβγδ (MuPDF maps its built-in encoding through the AGL), so Symbol metrics
  and widths are enforced; the ZapfDingbats line yields no hits, leaving ZD metrics behaviorally unpinned.
- 2026-07-11 (M6 glyphs): code→GID chains, pixel-pinned by the corpus (glaive dropped 17.4–18.9% → 7.5–8.8%
  over Δ24 with layout already quad-exact, so the mapping is what the pixels verify). Embedded sfnt,
  non-symbolic (flag 4 unset OR flag 32 set): encoding glyph name → AGL Unicode → the font's Unicode cmap —
  (3,1) preferred, then (3,10), then any platform-0 subtable, FreeType's charmap preference — then glyph
  name → REVERSE Mac Roman code → the (1,0) subtable. glaive's macOS subsets carry ONLY (1,0) format-6
  tables and /Encoding /MacRomanEncoding, so its pixels cannot distinguish name→MacRoman from raw-code
  lookup there; name→MacRoman is implemented first per standard viewer practice (pdf.js consulted,
  Apache-2.0). Symbolic, or when the name path misses: (3,0) with the bare code, then folded into the
  0xF000 symbol page (code ≤ 0xFF), then (1,0) with the bare code, then Unicode-cmap-by-code for symbolic
  fonts, and finally code-as-GID when it is within the glyph count. Bare CFF: encoding name → charset-sweep
  name→GID map (first-wins on duplicates), else code-as-GID. Substituted: AGL-resolved name → Unicode →
  Liberation's own best cmap, and ONLY that — a name the AGL cannot resolve (ZapfDingbats aN, private names)
  maps to .notdef, which substituted fonts deliberately render as NOTHING (drawing the extraction table's
  ASCII fallback would paint the WRONG glyph, and Liberation's .notdef box is ink the original never had);
  embedded fonts keep gid 0 and draw their program's own .notdef like MuPDF. All 256 codes precompute at
  Load into a flat table. Subtable formats 0/4/6/12 are implemented; 2 (legacy CJK) is not consulted.
- 2026-07-11 (M6 glyphs): outlines are em-normalized glyph-space gfx.Paths (y up, advance 1 = one em),
  produced by `Font.GlyphPath(gid)`: sfnt via go-text `Face.GlyphDataOutline` scaled by 1/upem (contours
  implicitly closed, matching TrueType semantics); Type1C via `cff.Parse`+`LoadGlyph` mapped through the Top
  DICT FontMatrix (not a hard-coded /1000 — non-standard matrices ride along); substituted via the
  Liberation face. Fills use the NONZERO winding rule, AA on, one merged canvas path per TextRun (the plan's
  "merged outline path per run"), built by `path.AddPathMatrix(outline, Trm)` per glyph. GlyphPath recovers
  panics from hostile programs into a nil path (missing glyph, never a failed render); go-text rejecting a
  program that parsed for metrics (e.g. an sfnt with no cmap table — NewFont requires one) drops to the
  Liberation substitute for SHAPES while the embedded metrics/widths pins stay (fw9/glaive unaffected; note
  for Type0: CIDFontType2 outlines will need a non-NewFont glyf path).
- 2026-07-11 (M6 glyphs): device-seam addition: `TextRun.CTM` (the gs CTM at emission). Glyph Trms already
  fold it in, but ISO 32000-2 9.3.6 stroke text takes its pen in USER space under the CTM alone — not under
  the text matrix or font size — so render.StrokeText rebuilds the merged outline in user space via
  Trm·CTM⁻¹ and strokes under Concat(CTM) exactly like StrokePath (gfx.Matrix gained Invert; a degenerate
  CTM draws nothing). ClipText accumulates the device-space outline; EndTextClip pushes it as one real
  Save+ClipPath level — a text object whose clip accumulated no outlines now clips EVERYTHING away (correct
  text-clip semantics; the M6-session-1 no-op degrade is gone). No corpus file exercises Tr 1–7 yet — a
  probe file should join the corpus before M6 exits.
- 2026-07-11 (M6 glyphs): caching. Converted glyph paths cache per RENDER in the raster device, keyed
  {*font.Font, gid} (fonts are per-Run cached by resource ref, so the pointer is stable within a page),
  capped at 4096 entries; this migrates to internal/store when it lands. Parsed Liberation programs cache
  globally under a mutex as immutable `*otfont.Font` (verified: the outline/advance paths only read Font
  tables and allocate fresh points) with a fresh `otfont.Face` per pdfview Font, because Face-level caches
  (cmapCache, extentsCache) mutate without locks; substituted cmap lookups go through `Font.Cmap.Lookup`
  (pure) rather than the caching NominalGlyph.
- 2026-07-11 (M6 glyphs): width fallback for /Widths-less fonts, refining the earlier width rules: embedded
  sfnt programs now use their own hmtx advance via the GID chain (the promised "hmtx once GID mapping
  lands"); substituted fonts keep the standard-14 AFM tables (std14-styles pin unchanged); bare CFF without
  /Widths also takes the AFM stand-in until CFF charstring advances land (no corpus coverage). A PRESENT
  /Widths array still never falls through to any of these — gaps mean /MissingWidth.
- 2026-07-11 (M6 glyphs): pixel-enforcement scope: TestTextCorpusPixels enforces the files inside default
  thresholds (text-std14, its six encrypted variants, hit-quad-split) and reports the rest; rotate90 moved
  to enforced in TestVectorCorpusPixels. The recorded numbers and the analysis of the unenforced residuals
  (AA edge redistribution with ~1% ink parity for embedded fonts; Liberation-vs-Nimbus letterforms for
  substituted ones) live in the M6 status note above — per-file thresholds were deliberately NOT set this
  session so the numbers stay honest until the M6-exit decision.

## Verification

- Every session: `./build.sh --all` (build, golangci-lint, `go test -race ./...`).
- Parity: `go test -run Parity ./...` once the harness exists (pure Go, committed goldens).
- Oracle regen (local only): `cd oracle && ./regen.sh` (needs cgo + `../pdf`); review golden diffs before commit.
- End-to-end from M4 on: `go run ./example testfiles/corpus/glaive.pdf GURPS` → compare `page0.png`
  against the oracle's PNG. `mutool` (brew install mupdf-tools) is a secondary investigative tool only.
- Cutover checks are listed under M8.
