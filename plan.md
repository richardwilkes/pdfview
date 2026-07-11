# plan.md — pure-Go pdfview port

**Current milestone: M2 complete (2026-07-11). M3 (navigation) is next.**

> **Next session start here:** confirm the 4 CI runners went green on the M2 commit (fix anything that didn't),
> then begin M3: destinations (all /XYZ,/Fit,… kinds) + named dests (/Dests dict + /Names name tree) — the first
> M3 box. M3 fills the `outline`, `links`, and `page.bounds` engine-seam stubs in `pdf.go` and adds an
> `internal/doc` navigation layer (page-space→top-left mapping with MediaBox ∩ CropBox + /Rotate + y-flip,
> NaN for coordinate-less dests, the float32 funnel). When `gates_test.go`'s milestone const reaches "M3",
> TestParity starts comparing TableOfContents at every recorded DPI and the gate promotes TestInternalLinks.
> Note the M2 caveat carried forward: encrypted page dictionaries first captured pre-authentication are now
> refreshed by re-walking the page tree after a successful `Authenticate` (see the M2 decision log); M3's
> destination resolution reads through the same freshly-decrypted dicts.

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
| TestInternalLinks | M3 | gated |
| TestRenderPageForSizeLimits | M4 | gated |
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

### M3 — Navigation (3–4 sessions)

- [ ] Destination arrays (all /XYZ,/Fit,/FitH,/FitV,/FitR,/FitB,/FitBH,/FitBV kinds) + named dests (/Dests dict +
      /Names name tree)
- [ ] Outline walk feeding `outline()` (exact `buildTOCEntries` budget semantics already in pdf.go)
- [ ] Link annotations + external-URI classification (`fz_is_external_link` semantics: URI scheme presence)
- [ ] Page-space→top-left mapping (MediaBox ∩ CropBox + /Rotate + y-flip), NaN for coordinate-less dests
- [ ] `page.bounds()` real; float32 funnel through the seam types

Exit: TestInternalLinks green (gate → "M3"); fixture TOC 66 entries with exact spot x/y at dpi 100; TOC/link JSON
parity at dpi 72+100.

### M4 — Graphics core + wiring (5–7 sessions)

- [ ] `internal/gfx`; content tokenizer + interpreter (paths, q/Q/cm, ExtGState subset, W/W*, all paint ops)
- [ ] `internal/device` (interface + Tee) with a null device for fuzzing
- [ ] `internal/render` on `surface.NewRasterN32Premul`: fills/strokes/dash/clips; premultiplied readback wired into
      `rasterize` (pdf.go's unpremultiply loop stays)
- [ ] Page CTM + output dimension rounding pinned empirically against oracle dims across page sizes × dpis
- [ ] `internal/color` (Gray/RGB/CMYK/Indexed/ICC-fallback/Separation basics) + `internal/function` (0/2/3/4)
- [ ] RenderPage/RenderPageForSize end-to-end; FuzzContent

Exit: TestRenderPageForSizeLimits green (gate → "M4"); fixture dims 827×1170 stride 3308 exact; vector corpus within
thresholds; race-clean.

### M5 — Images (4–5 sessions)

- [ ] `internal/imaging`: DCT (+CMYK/YCCK/APP14), CCITT, JBIG2 stub, JPX stub, bpc unpack, Decode arrays, Indexed
- [ ] ImageMask → Alpha8 stencil tinting; SMask/Mask/color-key; inline images (BI/ID/EI)
- [ ] /Interpolate → sampling mapping calibrated vs oracle; FuzzImaging

Exit: image corpus within thresholds; JBIG2/JPX corpus files render blank-not-error.

### M6 — Fonts + text rendering (8–12 sessions, the long pole)

- [ ] **Quad-parity spike first**: decode GLAIVE page-0 text, compute char quads from our metrics, diff against
      oracle stext quads before building any rendering (de-risks M7's exact hits)
- [ ] `internal/type1` (PFA/PFB container, eexec, charstrings via psinterpreter)
- [ ] `internal/font`: descriptors, embedded font dispatch, encodings + Differences + AGL, ToUnicode, widths
- [ ] Standard-14 + substitution via embedded bundle (Liberation ×12 + Noto symbols subset + AFM width tables;
      license texts committed)
- [ ] Type0/CID: embedded CMap parsing, Identity-H/V, CIDToGIDMap, CID-keyed CFF charset/FDSelect reader
- [ ] Type3 CharProcs via interpreter recursion
- [ ] Text operators incl. render modes 0–7 + text clip; glyph cache in `internal/store`; maxCacheSize honored
- [ ] FuzzCMap + FuzzType1

Exit: text corpus within thresholds; GLAIVE full-page diff within threshold; spike corners <0.5 px; CID/CJK/Type3
corpus per oracle; budget honored under a tiny maxCacheSize.

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

## Verification

- Every session: `./build.sh --all` (build, golangci-lint, `go test -race ./...`).
- Parity: `go test -run Parity ./...` once the harness exists (pure Go, committed goldens).
- Oracle regen (local only): `cd oracle && ./regen.sh` (needs cgo + `../pdf`); review golden diffs before commit.
- End-to-end from M4 on: `go run ./example testfiles/corpus/glaive.pdf GURPS` → compare `page0.png`
  against the oracle's PNG. `mutool` (brew install mupdf-tools) is a secondary investigative tool only.
- Cutover checks are listed under M8.
