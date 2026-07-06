# Plan: Porting `github.com/richardwilkes/pdf` off MuPDF (as `github.com/richardwilkes/pdfview`)

## 1. Goal and constraints

Reimplement the public API of `github.com/richardwilkes/pdf` in this repository: a pure-Go PDF *engine* (parsing,
encryption, navigation, fonts, structured text, search) with all rasterization delegated to
`github.com/richardwilkes/unison`'s Skia-backed drawing layer (locally `../unison`). This repo was seeded
from the pdf repo and becomes module `github.com/richardwilkes/pdfview`; the package name stays `pdf`, so consumers
migrate by changing only the import path. Development happens directly on `main` — the cgo build does not need to
keep working here. The original cgo repo stays published at <https://github.com/richardwilkes/pdf> (locally
`../pdf`) as the behavioral oracle and consumer escape hatch; the MuPDF C source at
<https://github.com/richardwilkes/mupdf> (locally `../mupdf`) is available for behavioral investigation only — run,
never translated (see §3).

**Revised goal (2026-07-05):** the original "no cgo anywhere" ambition is consciously dropped — unison links Skia
via cgo (vendored static libs on macOS/Linux; a runtime-loaded embedded DLL on Windows, which therefore needs no C
toolchain at all). What the port still eliminates: MuPDF and its AGPL license (Skia is BSD-3-Clause), the
`richardwilkes/mupdf` artifact pipeline, and the riskiest hand-rolled component (the rasterizer). What it accepts:
consumers share unison's dependency graph, and rendering fidelity rides on Skia. The engine/rendering split stays
strict — only the draw device imports unison (§5) — so the core remains UI-free, headless-testable, and open to a
different backend later.
The port implements *only* what the exposed API requires, not all of MuPDF.

The public API to preserve, byte-for-byte in signature and documented behavior:

- `New(buffer []byte, maxCacheSize uint64) (*Document, error)` — including the "scan first 1KB for `%PDF`" tolerance.
- `Document.RequiresAuthentication() bool`
- `Document.Authenticate(password string) AuthenticationStatus` — same bitmask semantics
  (`NoAuthenticationRequiredMask`, `UserAuthenticatedMask`, `OwnerAuthenticatedMask`).
- `Document.PageCount() int`
- `Document.TableOfContents(dpi int) []*TOCEntry`
- `Document.RenderPage(pageNumber, dpi, maxHits int, search string) (*RenderedPage, error)`
- `Document.RenderPageForSize(pageNumber, maxWidth, maxHeight, maxHits int, search string) (*RenderedPage, error)`
- `Document.Release()` — after release, methods return zero values / `ErrDocumentReleased`. ("Automatic cleanup"
  becomes moot: all resources are GC-managed Go memory — see M1 item 7.)
- Types `TOCEntry`, `PageLink`, `RenderedPage`, `AuthenticationStatus`; all `Err*` sentinels; all `OverallMax*` limits.

A vector-native `DrawPage(*unison.Canvas, ...)` entry point will be *added* (not substituted) at cutover; the
`*image.NRGBA` API above remains the parity contract and the basis for differential testing.

Behavioral contract to preserve (these are asserted by the existing tests):

- 0-based page numbers everywhere.
- Rendered output is `*image.NRGBA`, straight (non-premultiplied) alpha, transparent background where the page
  paints nothing, stride = 4×width.
- `dpiToScale` = `min(max(dpi,1)/72, 10)`; the same scale applied to search quads, link rects, dest points, TOC x/y.
- Search-hit rects: axis-aligned bbox of all four quad corners, min floored, max ceiled (`quadToRect`/`scaleRect`).
- Non-finite destination coordinates (e.g. `/Fit`) map to 0 (`scaledFloor`).
- External links keep URI, `PageNumber == -1`; internal links resolve to a 0-based page + `DestPoint`; unresolvable
  internal links are dropped.
- All strings from the document pass through `sanitizeString` (strip control/non-printable/U+FFFD, trim space).
- `OverallMaxHits` / `OverallMaxLinks` / `OverallMaxTOCEntries` / `OverallMaxPixels` enforced exactly as today
  (`RenderPageForSize` rejects up front; both paths reject centrally before allocating).
- Tolerant parsing: `TestInternalLinks` uses a document with `startxref 0` and **no xref table** — the port must
  include xref reconstruction (MuPDF-style "repair mode"), not just spec-conformant parsing.
- Garbage after a valid `%PDF` header → `ErrUnableToOpenPDF`, never a panic.
- Thread safety: one mutex serializing all operations on a `Document`; safe concurrent use from any goroutine.
  Rendering therefore only ever touches CPU raster surfaces — never windows, GL contexts, or unison UI state.

## 2. What the API actually requires (functional inventory)

Working backward from the wrapped `fz_*` calls, the port needs these subsystems. This is the real scope — it is a
large project, dominated by rendering and fonts:

| Wrapped MuPDF call | Pure-Go subsystem needed |
|---|---|
| `fz_open_document_with_stream` | PDF object/lexer/xref layer, repair, object & xref streams, stream filters |
| `fz_needs_password` / `fz_authenticate_password` | Standard security handler (RC4, AES-128/256, revisions 2–6) |
| `fz_count_pages`, `fz_load_page`, `fz_bound_page` | Page tree walk, inherited attributes, MediaBox/CropBox/Rotate |
| `fz_load_outline` | Outline tree, named destinations, name trees, UTF-16BE/PDFDoc string decoding |
| `fz_load_links`, `fz_resolve_link`, `fz_is_external_link` | Link annotations, /Dest & /A GoTo/URI actions, dest resolution |
| `fz_new_display_list_from_page` + `fz_new_pixmap_from_display_list` | Content-stream interpreter + a draw device rendering through unison/Skia: paths, clipping, images, fonts/glyphs, shadings, patterns, transparency, blend modes, colorspaces, functions; annotation appearance streams |
| `fz_search_display_list` | Structured-text extraction (per-glyph quads) + MuPDF-compatible search |

Notably *not* needed (and explicitly out of scope): PDF writing/editing, forms/JS, non-PDF formats (XPS, EPUB,
images-as-documents), printing, PostScript output, digital signatures, redaction, accessibility/tagged-PDF.

## 3. Licensing ground rules (decided)

- MuPDF is AGPL. A line-by-line translation of its C source is a derivative work and would force AGPL on the Go
  port. **Decided: (a) clean-room** — implement from the PDF 2.0 spec (ISO 32000-2) using MuPDF only as a
  behavioral oracle (run it, compare outputs — safe). AGPL was rejected because it would infect every consumer of
  this library. MuPDF — including the local `../mupdf` checkout — is consulted for *behavior* (what does it do
  with this malformed file?) but never read-and-translated into Go.
- Permissively licensed references that *can* be read/adapted: `pdfcpu` (Apache-2.0: xref, filters, crypto),
  `rsc.io/pdf` (BSD: lexer/xref shape), `golang.org/x/image` (BSD: CCITT, sfnt, raster), pdf.js (Apache-2.0:
  excellent reference for repair heuristics, font edge cases, search).
- Bundled data needs vetting: Base-14 substitute fonts (recommend Liberation family, SIL OFL — metric-compatible
  with Arial/Times New Roman/Courier New, close matches for Helvetica/Times/Courier; plus an OFL symbol/dingbat
  source), Adobe Core-14 AFM metrics (freely redistributable), Adobe `cmap-resources` for predefined CJK CMaps
  (BSD-3-Clause), Adobe Glyph List (BSD-3-Clause).
- unison is MPL-2.0 (same author) and Skia is BSD-3-Clause — both fine to link against. The clean-room rule exists
  solely because of MuPDF's AGPL.

## 4. Dependency decisions

Drawing is delegated wholesale to `github.com/richardwilkes/unison` (Skia-backed; the one large dependency — §5
describes the seam that contains it). For the engine itself: stdlib and `golang.org/x/*` preferred, with no other
third-party packages.

| Need | Decision |
|---|---|
| FlateDecode | `compress/zlib`/`compress/flate` wrapped in a lenient reader (bad zlib headers, truncated streams, garbage trailing bytes are common in the wild) |
| LZWDecode | **Decided:** in-repo implementation adapted from stdlib `compress/lzw` (BSD; PDF needs the EarlyChange semantics stdlib lacks), fuzzed like every other filter — keeps engine dependencies to stdlib + `golang.org/x/*` only |
| ASCIIHex / ASCII85 / RunLength | write in-repo (trivial) |
| PNG/TIFF predictors | write in-repo |
| DCTDecode | `image/jpeg` + in-repo handling of CMYK/YCCK + Adobe APP14 transform and Decode-array inversion |
| CCITTFaxDecode | `golang.org/x/image/ccitt` |
| JBIG2Decode | **Decided:** blank + debug log through cutover, then an in-repo generic-region-only ITU T.88 decoder post-M8 — see §8 |
| JPXDecode (JPEG 2000) | **Decided:** ship without — blank + debug log, documented; revisit on demand — see §8 |
| RC4 / AES / SHA-2 / MD5 | stdlib `crypto/*` |
| TrueType/OpenType | **Decided:** glyph rendering via Skia (`unison.RegisterFont` loads embedded TTF/OTF/CFF from bytes); in-repo parsing still required for widths, symbolic-cmap selection quirks, `CIDToGIDMap`, and repairing subset fonts Skia/FreeType reject |
| Type1 / bare CFF parsing | write in-repo (eexec + Type1/Type2 charstring interpreters — no suitable permissive Go library exists); Skia has no Type1 loader, so those glyphs become Go-generated paths drawn through the device |
| Rasterizer | **Superseded (2026-07-05):** all rasterization/compositing via `github.com/richardwilkes/unison` (Skia) — fills (nonzero + even-odd), strokes, clip stacks, layers, blend modes. The in-repo scanline rasterizer is no longer planned |
| Text shaping | **not needed** — PDF content streams carry pre-positioned glyphs; no HarfBuzz equivalent required |
| ICC profiles | not initially: map ICCBased spaces by component count to Device{Gray,RGB,CMYK}; revisit only if fidelity demands it |

## 5. Proposed package layout

Public API stays in the root `pdf` package; everything else is `internal/`:

```
pdf.go                    — same exported surface, no cgo; orchestrates the internals
internal/syntax/          — lexer, object model (Null/Bool/Int/Real/String/Name/Array/Dict/Ref/Stream),
                            xref tables + xref streams + object streams, trailer, incremental updates,
                            repair/reconstruction, lazy object resolution, recursion/loop guards
internal/filter/          — filter chain plumbing, flate (lenient), lzw, asciihex, ascii85, runlength,
                            predictors, dct, ccitt, (jbig2, jpx stubs)
internal/crypt/           — standard security handler R2–R6, crypt filters, per-object keys
internal/model/           — document catalog, page tree, inherited attributes, resources, name trees,
                            destinations, outline, link annotations
internal/geom/            — matrix, point, rect, quad, MuPDF-compatible rect→int rounding
internal/function/        — PDF function types 0, 2, 3, 4 (incl. PostScript calculator)
internal/color/           — colorspace model: device spaces, Indexed, Separation/DeviceN, Lab, Cal*, ICC-fallback
internal/font/            — font model; type1/, cff/, truetype/ (or sfnt shim), type3, cid;
                            encodings (Standard/WinAnsi/MacRoman/Symbol/ZapfDingbats, /Differences, AGL),
                            CMaps (embedded + predefined), ToUnicode, widths/metrics, glyph cache
internal/font/data/       — embedded substitute fonts (OFL), Core-14 AFMs, predefined CMaps (embedded compressed,
                            decompressed lazily — see the binary-size default in §8)
internal/content/         — content-stream tokenizer, operator dispatch, graphics state, resource stack,
                            Form XObjects, inline images, device interface (the fz_device analogue)
internal/render/          — the unison/Skia canvas draw device (the ONLY package that imports unison): paths,
                            glyphs (glyph-ID runs via Skia; Type1/Type3 as paths/procedures), images, shadings,
                            tiling patterns, transparency groups + soft masks (save-layer, luma color filter),
                            blend modes; headless raster surface + unpremultiplied-RGBA readback
internal/stext/           — structured-text device (per-glyph quads, line/word assembly) + search
oracle/                   — separate Go module (own go.mod) importing the old cgo package
                            github.com/richardwilkes/pdf to dump reference outputs; never a dependency of the
                            root module
```

The `fz_display_list` intermediate is unnecessary in Go: `render()` runs the interpreter over the page once per
device (draw device, stext device), or once through a tee device. The device interface is the seam that keeps
rendering and text extraction sharing one interpreter, exactly as MuPDF's `fz_device` does — and (decided
2026-07-05) it is also the unison boundary: the engine emits device calls, and `internal/render` translates them
into unison Canvas operations. Everything outside `internal/render` stays UI-free and headless-testable.

## 6. Milestones (each ends green: builds, tests, fuzz corpus clean)

### M0 — Scaffolding and oracle harness (~1–2 weeks)

1. Repo setup (**decided** — supersedes the earlier branch-vs-build-tag options): this new repo *is* the port and
   `main` is the development branch; no cgo coexistence, no build tags, no `purego` branch. Rename the module to
   `github.com/richardwilkes/pdfview` (package name stays `pdf`). Strip the cgo remnants inherited from the seed
   commit — the `pdf.go` cgo preamble and wrappers, `lib/`, `include/mupdf`, `update_from_release.sh`,
   `setup-windows.ps1`, and the Windows-toolchain docs — keeping the pure-Go parts of `pdf.go` (exported types,
   `Err*` sentinels, `OverallMax*`, `dpiToScale`, `quadToRect`, `scaleRect`, `scaledFloor`, `sanitizeString`),
   `pdf_test.go`, `testfiles/`, and `example/`. Stub the entry points so the tree compiles; tests stay red until
   their milestone wires them. CI builds and tests on all three OSes: macOS/Linux use the stock system C
   toolchain (unison links Skia statically via cgo); Windows needs no C toolchain at all (unison loads Skia from
   an embedded DLL).
2. Build an oracle tool as a separate module (`oracle/`, own `go.mod`) that imports the old cgo package
   `github.com/richardwilkes/pdf` (locally `../pdf`) and, for a corpus of PDFs, dumps JSON of: page count,
   auth status, full TOC (titles/pages/x/y), per-page links, per-page search quads for sample needles, page
   bounds — plus rendered PNGs at fixed DPIs. Check the tool in; commit outputs per corpus file, so only
   regenerating oracle data ever needs a C toolchain — the root module and its CI never do.
3. Assemble the corpus: the existing fixture, the inline `internalLinkPDF`, plus a curated set (encrypted docs
   R2–R6, CJK, Type1/Type3/CID fonts, shadings, patterns, transparency, CCITT/JBIG2/JPX images, damaged files).
   Public suites to draw from: pdf-association test corpus, veraPDF corpus, Mozilla pdf.js test files.
4. Write the differential test runner: pure-Go output vs oracle JSON (exact) and PNGs (perceptual metric with a
   per-milestone threshold that tightens over time).
5. Unison headless-rendering prerequisite (lands in `../unison`; needed before M4, can proceed in parallel with
   M1–M3): make CPU raster-surface drawing work without `Start()`, a window, or a GL context on every platform
   (lazy `skiaColorspace` init; drop the GL context from the raster path, including Windows); add a
   persistent-surface offscreen render entry point (`NewImageFromDrawing` is per-call and self-described as
   inefficient); document and test in unison's own suite (with `-race`) that raster-only drawing is safe off the
   UI thread, one goroutine per surface — pdfview's any-goroutine API contract depends on this guarantee.

### M1 — COS layer: objects, xref, filters, page tree (~3–4 weeks)

1. Lexer per ISO 32000-2 §7.2–7.3 with real-world tolerances (comments anywhere, `#`-escaped names, octal and
   line-continuation string escapes, malformed numbers like `--5` and `1.2.3`).
2. Object model + lazy indirect-reference resolution with cycle detection and depth limits.
3. Xref: classic tables (multi-section, `/Prev` chains, hybrid `/XRefStm`), xref streams (`/W`, `/Index`),
   object streams (`/ObjStm`), incremental updates (later sections win), free-list tolerance.
4. **Repair mode**: when startxref/xref is missing or broken, scan the whole buffer for `N G obj` patterns,
   rebuild the table, locate trailer(s)/`/Root`. Required by `TestInternalLinks`. Also: recover from wrong
   `/Length` by scanning for `endstream`.
5. Filter chain: Flate (lenient), ASCIIHex, ASCII85, RunLength, LZW, predictors; `DecodeParms` arrays; decoded
   output size caps (defense in depth for the `OverallMax*` philosophy).
6. Page tree walk with `/Count` short-cuts, inherited `MediaBox`/`CropBox`/`Rotate`/`Resources`, loop guards.
7. Wire up: `New` (header scan → parse → `ErrUnableToOpenPDF` on failure), `PageCount`, `Release`
   (drops references, marks released; **decided:** `runtime.AddCleanup` is dropped — no non-memory resource
   remains in pure Go — but the released-state semantics stay because tests assert them).
8. Fuzz targets: lexer, xref loader, each filter. Run continuously from here on.

Exit: page counts match the oracle across the corpus; `TestMalformedPDF` and `TestUseAfterRelease` pass;
`TestInternalLinks` document *parses* (links come in M3).

### M2 — Encryption (~1–2 weeks)

1. Standard security handler: revisions 2/3/4 (RC4 40–128, AES-128 `/AESV2`) and 6 (AES-256 `/AESV3`,
   ISO 32000-2 Algorithm 2.A/2.B hashing); crypt filters (`/StmF`, `/StrF`, `/Identity`), `/EncryptMetadata`,
   per-object key derivation; empty-user-password auto-auth at open time (MuPDF does this; it's why most
   encrypted-but-passwordless files "just open").
2. `RequiresAuthentication` and `Authenticate` with MuPDF's exact result bits: bit 0 = no password was needed,
   bit 1 = user password matched, bit 2 = owner password matched; 0 = failure.
3. Encrypted fixtures for every revision in the corpus; differential tests against the oracle.

### M3 — Navigation: TOC, links, destinations (~2 weeks)

1. Destination model: explicit arrays (`/XYZ`, `/Fit`, `/FitH`, `/FitV`, `/FitR`, `/FitB*`), page-ref → page
   index resolution, named destinations via `/Dests` dict (PDF 1.1) and `/Names` name tree (binary search +
   broken-tree linear fallback), `/A` GoTo and URI actions.
2. Coordinate mapping: dest points and TOC x/y converted from PDF bottom-up page space to the top-left/y-down
   space MuPDF reports (account for `MediaBox` origin and `/Rotate`), then the existing `scaledFloor` applies.
   Missing coordinates surface as NaN so `scaledFloor` maps them to 0, matching today's behavior exactly.
3. Outline: `/Outlines` walk (`/First`/`/Next`/`/Down` analogue via `/First`/`/Last`/`/Next`/`/Count`), cycle
   guards, `OverallMaxTOCEntries` cap with the same traversal order as `buildTOCEntries`, PDFDocEncoding and
   UTF-16BE (and UTF-8, PDF 2.0) title decoding → `sanitizeString`.
4. Links: page `/Annots` `/Link` subtype, rect → top-left space, external-vs-internal split identical to
   `fz_is_external_link` (URI has a scheme → external), unresolvable internal links dropped, `OverallMaxLinks`.

Exit: `TestInternalLinks` passes fully; TOC entry counts/titles/coords and links match the oracle corpus-wide
(the fixture's 66 TOC entries with exact x/y values are the acceptance test).

### M4 — Graphics core: interpreter, unison draw device, color, functions (~3–4 weeks)

1. `internal/geom`: matrix/rect/quad ops; replicate MuPDF's rect→integer-rect rounding used when sizing the
   output pixmap, so rendered dimensions (827×1170 for the fixture at 100 DPI) match exactly.
2. Content tokenizer + interpreter: full operator set; graphics state stack (`q`/`Q`, `gs` ExtGState); resource
   dictionary scoping; Form XObjects (with `/Matrix`, `/BBox` clip); inline images (`BI`/`ID`/`EI`, including
   the abbreviated keys and the "EI inside image data" scan hazard); operand-count and type tolerance (skip bad
   operators, don't abort the page — MuPDF renders what it can).
3. Device interface mirroring the shape of `fz_device` (fillPath, strokePath, clipPath, fillText, clipText,
   fillImage, fillImageMask, clipImageMask, fillShade, begin/endGroup, begin/endMask, pop clip...).
4. Unison canvas draw device (`internal/render`): render into a headless CPU raster surface; map fills
   (nonzero/even-odd), strokes (butt/round/square caps, miter/round/bevel joins, miter limit, dash arrays/phase —
   Skia path effects), and the clip stack (canvas save/restore + clip paths) onto unison Canvas calls; read back
   once as unpremultiplied RGBA8888 into the `*image.NRGBA` result (Skia's `AlphaTypeUnPreMul` readback replaces
   today's `unpremultiply` loop with the same result).
5. Colorspaces: DeviceGray/RGB/CMYK (match MuPDF's CMYK→RGB conversion formula so flat colors diff-test clean),
   Indexed, Separation/DeviceN (tint transform via functions), Lab, CalGray/CalRGB (approximate as device),
   ICCBased → component-count fallback.
6. PDF functions: sampled (0), exponential (2), stitching (3), PostScript calculator (4) with an allowlisted
   operator set and execution limits.
7. Wire `RenderPage`/`RenderPageForSize`/`render()` end-to-end with the existing scale/limit logic (already
   pure Go — moves over unchanged).

Exit: vector-only corpus pages render; image diff vs oracle under a loose threshold; output dimensions exact.

### M5 — Images (~2–3 weeks)

1. Image XObjects: 1/2/4/8/16 BPC unpacking, `/Decode` arrays, `/ImageMask` stencils, `/SMask` (incl. `/Matte`
   awareness), color-key masking (`/Mask` array), `/Mask` stencil form; DCT (incl. CMYK/YCCK + Adobe transform),
   CCITT (x/image/ccitt), raw Flate/LZW paths.
2. Image transform + sampling in the draw device via Skia sampling options (bilinear/mipmapped when
   `/Interpolate` or downscaling; MuPDF-comparable quality is the bar, not identical resampling).
3. JBIG2/JPX: land the stub behavior (blank fill + counter) and the interface a future decoder plugs into.

### M6 — Fonts and text rendering (~5–6 weeks; still the largest single chunk)

1. Simple font model: `/Font` dicts, `/Widths`+`/FirstChar`/`/MissingWidth`, `/FontDescriptor`.
2. Embedded font parsing (in-repo parsing stays regardless of who rasterizes — widths, encodings, and CID
   mapping feed search geometry):
   - `FontFile` — Type1: PFA/PFB, eexec decryption, Type1 charstring interpreter → outlines drawn as paths
     through the device (Skia has no Type1 loader); `/Encoding` from the font program.
   - `FontFile2` — TrueType: register the bytes with Skia (`unison.RegisterFont`) for glyph rendering; parse
     in-repo for `hmtx` widths and symbolic-font cmap selection quirks ((3,0) MS Symbol, (1,0) Mac, no-cmap →
     glyph index by code), and repair subset fonts Skia/FreeType reject before registering.
   - `FontFile3` — bare CFF (Type1C) and CIDFontType0C: CFF INDEX/DICT parsing, Type2 charstrings, charset,
     FDArray/FDSelect for CID-keyed CFF (needed for CID→glyph mapping); rendering via Skia where it accepts
     the font, otherwise Type2 charstrings → paths.
3. Encodings: Standard/WinAnsi/MacRoman/MacExpert + `/Differences`; AGL glyph-name→Unicode; Symbol and
   ZapfDingbats tables.
4. Composite fonts: Type0, CIDFontType0/2, `CIDToGIDMap`, embedded CMap streams, `Identity-H/V`, `/W` widths;
   predefined CJK CMaps from bundled Adobe data (default: embed compressed — confirm against measured sizes at
   M6 start; see the binary-size row in §8).
5. Type3 fonts: glyph procedures executed through the interpreter with the font matrix.
6. Non-embedded fonts: Base-14 name normalization (incl. the standard aliases), bundled OFL substitutes for
   glyphs, Core-14 AFM metrics for widths — widths drive search/text geometry, so metrics fidelity matters more
   than glyph-shape fidelity.
7. Text operators: `Tf Tj TJ ' " Td TD Tm T* TL Tc Tw Tz Ts Tr`; render modes 0–7 including clip modes;
   char/word spacing incl. the CID-vs-simple-font word-spacing rule; text rise; horizontal scaling.
8. Glyph pipeline: embedded TTF/OTF/CFF render as glyph-ID runs through Skia (`SK_TEXT_ENCODING_GLYPH_ID`; Skia's
   internal glyph caches apply); Type1 and Skia-rejected fonts render as Go-generated paths; Type3 through the
   interpreter. `maxCacheSize` becomes the budget for decoded-image and parsed-font caches (0 = unlimited, as
   today).

Exit: text-heavy corpus pages pass perceptual diff; fixture page 0 renders correctly at 827×1170.

### M7 — Structured text and search (~2–3 weeks)

1. Stext device: capture each shown glyph's Unicode (via encoding/ToUnicode/AGL fallback chain), advance, and
   quad in device space; assemble chars → lines → blocks with MuPDF-comparable ordering and space insertion.
2. Search: replicate `fz_search_stext_page` semantics — case-insensitive canonical matching, whitespace runs
   match any whitespace, matches can span line breaks, hit quads per matched char run merged per line.
   Validate against the oracle: the fixture must yield the same 9 "GURPS" hits with identical rects (the quads
   derive from font metrics, not rasterization, so exactness is achievable given correct widths).
3. Enforce `maxHits`/`OverallMaxHits`; hook into `render()` alongside the draw device.

Exit: `TestPDF` search assertions pass unmodified — or, where a residual off-by-one-pixel difference proves
intractable, re-baseline the test values *once*, with the diff documented and reviewed.

### M8 — Advanced graphics, hardening, cutover (~3–4 weeks)

1. Shadings types 1–7: axial/radial (2/3) map to Skia gradients by sampling the PDF function into color stops;
   function-based (1) and mesh types 4–7 (with stream decoding) are software-rendered to images and drawn
   (`sk_capi` exposes no draw-vertices — extend it only if quality/perf demands); `sh` operator and shading
   patterns; tiling patterns (pattern matrix vs page matrix, XStep/YStep, resource scoping, tile cache).
2. Transparency via Skia layers: `/Group` transparency groups (isolated/knockout) with `save_layer`, soft masks
   (`/SMask` in ExtGState — alpha + luminosity, the latter via Skia's luma color filter), constant alpha (CA/ca),
   all 16 blend modes (native in Skia).
3. Annotation appearance streams: render each annotation's `/AP` `/N` form with `/Rect`↔`/BBox` mapping —
   `fz_run_page` includes annotations, so parity requires this (Link annotations usually have no appearance,
   but Widget/Stamp/FreeText etc. do).
4. Hardening: recursion/size/time limits audit; fuzz all new parsers; corpus soak run comparing every metric;
   `-race` suite; memory profiling with `maxCacheSize` budgets enforced; performance target within ~2× of the
   cgo build on the corpus — rasterization is Skia either way; the overhead is the Go engine plus one pixel
   readback per render (document actuals in the README).
5. Cutover (the MuPDF scrub already happened in M0): CI matrix across the platforms unison supports
   (macOS/Linux with stock C toolchains, Windows toolchain-free); rewrite `CLAUDE.md`/README architecture
   sections; update `pdf_test.go` expected values only where the re-baseline in M7/M8 was accepted; tag the
   first `pdfview` release, documenting behavior deltas vs the cgo package and the import-path-only migration;
   point the old repo's README here; ship the vector-native `DrawPage(*unison.Canvas, ...)` addition.

## 7. Testing strategy (continuous, not a phase)

- **Differential oracle testing** is the backbone: every milestone compares against cgo-MuPDF outputs captured in
  M0. Exact match for counts, TOC, links, auth status; perceptual threshold for pixels, tightening per milestone.
  Pixel tests render through unison's headless raster mode (M0 item 5) in ordinary `go test` — no display server.
- **Existing tests stay authoritative** for API behavior (release semantics, limits, error sentinels) and run
  unchanged throughout; the exact-value assertions (search rects, TOC coords, stride/bounds) are the final
  fidelity gate.
- **Fuzzing** (native Go fuzzing) for: lexer, xref/repair, every filter, every font parser, content tokenizer,
  function evaluator, CMap parser. Seed with corpus slices. Crashes and hangs are release blockers — this package
  advertises untrusted-input safety via the `OverallMax*` knobs. The Go engine removes the memory-unsafety class
  for everything it parses, but not resource exhaustion — and font bytes handed to Skia remain native-parsed
  (see §8).
- **Golden-image regression** within the pure-Go implementation itself once it stabilizes (so later refactors
  can't silently shift pixels).

## 8. Risks and open decisions

| Risk / decision | Assessment & mitigation |
|---|---|
| **Scope is very large** | This is a 6–9 month effort for one experienced developer (~40–70k LOC). M6 (fonts) is the long pole. Mitigate by strict scoping to the API and by the milestone order above, which delivers verifiable value early (parse/auth/nav before pixels). |
| **JBIG2 decoder** | No permissive pure-Go implementation exists (unipdf's is AGPL; jbig2dec is AGPL). **Decided: (b)** — stub (blank regions + debug log) through cutover, then a generic-region-only decoder from ITU T.88 post-M8 (~2–3 weeks, covers most scanned PDFs). Rejected: (a) ship-without leaves scanned docs blank forever; (c) full-spec symbol/text/halftone support costs weeks more for rare cases. |
| **JPX (JPEG 2000)** | Same situation. Rarer in the wild than JBIG2. **Decided:** ship without — blank + debug log, documented limitation; revisit on demand. |
| **Exact-value test parity** | Search quads and TOC coordinates depend on font *metrics* (not rasterization), so exactness is achievable — but only if widths, MediaBox handling, and rounding all match MuPDF. Budget for a documented one-time re-baseline of `pdf_test.go` if a few values shift by ±1px. |
| **AGPL derivation** | Enforce the clean-room rule in review: MuPDF is run, never read-and-translated. Keep a `NOTICES` file for every bundled asset and adapted permissive source. |
| **Malformed-PDF tolerance** | MuPDF's real moat is 20 years of quirk handling. The oracle corpus + fuzzing is the countermeasure; expect a steady trickle of tolerance patches after cutover. The old cgo module (`github.com/richardwilkes/pdf`) remains published as the escape hatch — consumers switch back by reverting one import line. |
| **Performance** | Rasterization is Skia — C++ speed. The overhead is the Go engine (interpreter, fonts, filters) plus one pixel readback per render; target within ~2× of the cgo build. Profile-guided optimization in M8, and the per-document mutex already serializes work, so no concurrency regression. |
| **Unison/Skia coupling** | Rendering rides on unison: cgo + vendored Skia libs on macOS/Linux (Windows: embedded DLL, no C toolchain), a GUI toolkit in the module graph, and hard prerequisites on new unison headless-raster support (M0 item 5) and an off-UI-thread raster guarantee. Mitigations: unison is same-author, so changes land quickly and the guarantees are tested in unison's own suite; the device seam keeps the engine UI-free so another backend remains possible. |
| **Untrusted fonts reach native code** | Embedded font bytes registered with Skia are parsed by FreeType/CoreText — memory-unsafety surface the pure-Go engine otherwise eliminates. Mitigations: pdfview parses/validates the same tables Go-side first (it needs them for metrics anyway) and can reject or rebuild malformed programs before registration; Skia's font path is heavily fuzzed upstream; fuzz the combined register-and-render path here. |
| **Binary size** | Embedded fonts + CJK CMaps could add several MB. **Default decided, confirm at M6 with measured sizes:** embed everything compressed, decompress lazily on first use; add opt-out build tags or a companion data module only if the measured cost exceeds a few MB. |
| **Interim alternative (WASM/wazero)** | **Decided: skipped.** No consumer needs a pure-Go build before the port matures, and the old cgo module remains the working fallback throughout, so compiling MuPDF to WASM under `wazero` would split effort and add an AGPL artifact only to delete it later. Revisit only if schedule pressure appears. |

## 9. Milestone summary

| Milestone | Deliverable | Est. |
|---|---|---|
| M0 | Repo scrub (MuPDF removal, module rename), oracle harness, corpus, differential runner, CI; unison headless-render mode | 1–2 wks |
| M1 | Objects, xref (+repair), filters, page tree; `New`/`PageCount`/`Release` | 3–4 wks |
| M2 | Encryption; `RequiresAuthentication`/`Authenticate` | 1–2 wks |
| M3 | TOC, links, destinations; `TableOfContents`, link loading | 2 wks |
| M4 | Interpreter, unison draw device, color, functions; vector rendering | 3–4 wks |
| M5 | Image formats and image drawing | 2–3 wks |
| M6 | All font types, encodings, CMaps, glyph rendering | 5–6 wks |
| M7 | Structured text + search parity | 2–3 wks |
| M8 | Shadings/patterns/transparency, annotations, hardening, cutover | 3–4 wks |

Total: roughly 4.5–7 months of focused work, with the old cgo module (`github.com/richardwilkes/pdf`) remaining
the shippable option for consumers until the M8 cutover.

## 10. Decision log (2026-07-05)

| Decision | Outcome |
|---|---|
| Licensing (§3) | Clean-room from ISO 32000-2; MuPDF (incl. `../mupdf`) is run as a behavioral oracle, never read-and-translated. |
| Repo strategy (M0) | This new repo *is* the port; develop on `main`; no cgo coexistence (no build tags, no `purego` branch). The old cgo repo stays published at `github.com/richardwilkes/pdf` (locally `../pdf`) as oracle + escape hatch. |
| Module path (M0) | Rename to `github.com/richardwilkes/pdfview`; package name stays `pdf`, so consumers change only the import path. |
| cgo scrub timing | Moved from M8 to M0: strip the preamble, `lib/`, `include/mupdf`, and Windows tooling now; keep the pure-Go helpers, tests, and fixtures. |
| LZWDecode (§4) | In-repo implementation adapted from stdlib `compress/lzw` (BSD) with EarlyChange; engine deps stay stdlib + `golang.org/x/*` only. |
| JBIG2 (§8) | Stub through cutover; generic-region-only ITU T.88 decoder post-M8. |
| JPX (§8) | Ship without; blank + debug log; revisit on demand. |
| WASM/wazero interim (§8) | Skipped — no schedule pressure; the old cgo module is the fallback. |
| Binary size / CJK CMaps (§8, M6) | Default: embed compressed, decompress lazily; confirm at M6 with measured sizes before adding build tags or a data module. |
| `runtime.AddCleanup` (M1) | Dropped — no non-memory resources in pure Go; `Release()` / `ErrDocumentReleased` semantics preserved because tests assert them. |
| Rendering backend (§4, §5, M4) | unison/Skia (`github.com/richardwilkes/unison`, locally `../unison`) behind the device seam — only `internal/render` imports unison; the in-repo rasterizer is dropped. Accepted consequence: cgo returns via Skia (BSD-3), so the `CGO_ENABLED=0` goal is dropped while the AGPL/MuPDF elimination stands. |
| Unison headless mode (M0 item 5) | Prerequisite work in `../unison`: CPU raster surfaces without `Start()`/window/GL on all platforms, lazy colorspace init, a persistent-surface offscreen API, and a `-race`-tested off-UI-thread raster guarantee. |
| Threading contract (§1) | Any-goroutine API contract kept: rendering touches CPU raster surfaces only — never windows, GL, or unison UI state. |
| Render API (§1, M8) | `*image.NRGBA` API preserved as the parity contract; vector-native `DrawPage(*unison.Canvas, ...)` added at cutover, not substituted. |
| Mesh/function shadings (M8) | Types 1 and 4–7 software-rendered to images; extend `sk_capi` (draw-vertices) only if quality/perf demands. |
