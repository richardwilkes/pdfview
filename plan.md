# Plan: Porting `github.com/richardwilkes/pdf` to Pure Go

## 1. Goal and constraints

Replace the cgo/MuPDF implementation behind the existing public API in `pdf.go` with a pure-Go implementation.
No cgo unless truly unavoidable (current assessment: it is avoidable — everything needed can be done in Go).
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
- `Document.Release()` — plus automatic cleanup; after release, methods return zero values / `ErrDocumentReleased`.
- Types `TOCEntry`, `PageLink`, `RenderedPage`, `AuthenticationStatus`; all `Err*` sentinels; all `OverallMax*` limits.

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
- Thread safety: one mutex serializing all operations on a `Document`; safe concurrent use.

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
| `fz_new_display_list_from_page` + `fz_new_pixmap_from_display_list` | Content-stream interpreter + full raster pipeline: paths, clipping, images, fonts/glyphs, shadings, patterns, transparency, blend modes, colorspaces, functions; annotation appearance streams |
| `fz_search_display_list` | Structured-text extraction (per-glyph quads) + MuPDF-compatible search |

Notably *not* needed (and explicitly out of scope): PDF writing/editing, forms/JS, non-PDF formats (XPS, EPUB,
images-as-documents), printing, PostScript output, digital signatures, redaction, accessibility/tagged-PDF.

## 3. Licensing ground rules (decide before writing code)

- MuPDF is AGPL. A line-by-line translation of its C source is a derivative work and would force AGPL on the Go
  port. Decide up front: (a) clean-room implementation from the PDF 2.0 spec (ISO 32000-2) using MuPDF only as a
  behavioral oracle (run it, compare outputs — safe), or (b) accept AGPL for the port. This plan assumes (a):
  MuPDF is consulted for *behavior* (what does it do with this malformed file?) but not for code.
- Permissively licensed references that *can* be read/adapted: `pdfcpu` (Apache-2.0: xref, filters, crypto),
  `rsc.io/pdf` (BSD: lexer/xref shape), `golang.org/x/image` (BSD: CCITT, sfnt, raster), pdf.js (Apache-2.0:
  excellent reference for repair heuristics, font edge cases, search).
- Bundled data needs vetting: Base-14 substitute fonts (recommend Liberation family, SIL OFL — metric-compatible
  with Arial/Times New Roman/Courier New, close matches for Helvetica/Times/Courier; plus an OFL symbol/dingbat
  source), Adobe Core-14 AFM metrics (freely redistributable), Adobe `cmap-resources` for predefined CJK CMaps
  (BSD-3-Clause), Adobe Glyph List (BSD-3-Clause).

## 4. Dependency decisions

Stdlib and `golang.org/x/*` preferred; a small number of permissive third-party packages where they save real work.

| Need | Decision |
|---|---|
| FlateDecode | `compress/zlib`/`compress/flate` wrapped in a lenient reader (bad zlib headers, truncated streams, garbage trailing bytes are common in the wild) |
| LZWDecode | `github.com/hhrutter/lzw` (handles PDF's EarlyChange; stdlib `compress/lzw` does not) or a small in-repo implementation |
| ASCIIHex / ASCII85 / RunLength | write in-repo (trivial) |
| PNG/TIFF predictors | write in-repo |
| DCTDecode | `image/jpeg` + in-repo handling of CMYK/YCCK + Adobe APP14 transform and Decode-array inversion |
| CCITTFaxDecode | `golang.org/x/image/ccitt` |
| JBIG2Decode | no permissive pure-Go decoder exists — see Risks §8; initially render as blank + debug log |
| JPXDecode (JPEG 2000) | same — see Risks §8; initially blank + debug log |
| RC4 / AES / SHA-2 / MD5 | stdlib `crypto/*` |
| TrueType/OpenType parsing & outlines | `golang.org/x/image/font/sfnt` where sufficient; expect to grow an in-repo parser for broken embedded fonts (missing tables, bad cmaps) — sfnt is strict, PDFs are not |
| Type1 / bare CFF parsing | write in-repo (eexec + Type1/Type2 charstring interpreters). No suitable permissive Go library exists |
| Rasterizer | write in-repo: scanline anti-aliased coverage rasterizer (active-edge list, 0–255 coverage), both nonzero and even-odd fill rules, span-based clipping. `x/image/vector` is nonzero-only and can't do clip stacks, so it is a reference, not the engine |
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
internal/font/data/       — embedded substitute fonts (OFL), Core-14 AFMs, predefined CMaps (build-tag or
                            sub-module if size becomes a concern)
internal/content/         — content-stream tokenizer, operator dispatch, graphics state, resource stack,
                            Form XObjects, inline images, device interface (the fz_device analogue)
internal/raster/          — scanline rasterizer, Bezier flattening, stroking (caps/joins/miter/dashes),
                            clip masks, pixmap, blending/compositing, blend modes, soft masks
internal/render/          — draw device: paths, glyphs, images, shadings (types 1–7), tiling patterns,
                            transparency groups; image scaler
internal/stext/           — structured-text device (per-glyph quads, line/word assembly) + search
```

The `fz_display_list` intermediate is unnecessary in Go: `render()` runs the interpreter over the page once per
device (draw device, stext device), or once through a tee device. The device interface is the seam that keeps
rendering and text extraction sharing one interpreter, exactly as MuPDF's `fz_device` does.

## 6. Milestones (each ends green: builds, tests, fuzz corpus clean)

### M0 — Scaffolding and oracle harness (~1 week)

1. Branch strategy: keep the cgo implementation working on `main`; develop on a `purego` branch. Alternatively
   gate the cgo backend behind a build tag (`//go:build mupdf_cgo`) so both backends coexist during the port and
   differential tests can run in one tree. Decide and set up CI for both.
2. Build an oracle tool (using the current cgo build) that, for a corpus of PDFs, dumps JSON of: page count,
   auth status, full TOC (titles/pages/x/y), per-page links, per-page search quads for sample needles, page
   bounds — plus rendered PNGs at fixed DPIs. Check the tool in; store outputs per corpus file.
3. Assemble the corpus: the existing fixture, the inline `internalLinkPDF`, plus a curated set (encrypted docs
   R2–R6, CJK, Type1/Type3/CID fonts, shadings, patterns, transparency, CCITT/JBIG2/JPX images, damaged files).
   Public suites to draw from: pdf-association test corpus, veraPDF corpus, Mozilla pdf.js test files.
4. Write the differential test runner: pure-Go output vs oracle JSON (exact) and PNGs (perceptual metric with a
   per-milestone threshold that tightens over time).

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
   (drops references, marks released; keep `runtime.AddCleanup` only if any non-memory resource remains — likely
   none, but the released-state semantics stay because tests assert them).
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

### M4 — Graphics core: interpreter, rasterizer, color, functions (~4–6 weeks)

1. `internal/geom`: matrix/rect/quad ops; replicate MuPDF's rect→integer-rect rounding used when sizing the
   output pixmap, so rendered dimensions (827×1170 for the fixture at 100 DPI) match exactly.
2. Content tokenizer + interpreter: full operator set; graphics state stack (`q`/`Q`, `gs` ExtGState); resource
   dictionary scoping; Form XObjects (with `/Matrix`, `/BBox` clip); inline images (`BI`/`ID`/`EI`, including
   the abbreviated keys and the "EI inside image data" scan hazard); operand-count and type tolerance (skip bad
   operators, don't abort the page — MuPDF renders what it can).
3. Device interface mirroring the shape of `fz_device` (fillPath, strokePath, clipPath, fillText, clipText,
   fillImage, fillImageMask, clipImageMask, fillShade, begin/endGroup, begin/endMask, pop clip...).
4. Rasterizer: adaptive Bezier flattening; stroking (butt/round/square caps, miter/round/bevel joins, miter
   limit, dash arrays/phase); nonzero + even-odd scanline AA fill; clip stack as accumulated coverage masks;
   RGBA8 premultiplied compositing internally, converted once to straight-alpha NRGBA at the end (replaces
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
2. Image transform + sampling in the draw device (bilinear when `/Interpolate` or downscaling; MuPDF-comparable
   quality is the bar, not identical resampling).
3. JBIG2/JPX: land the stub behavior (blank fill + counter) and the interface a future decoder plugs into.

### M6 — Fonts and text rendering (~6–8 weeks; the largest single chunk)

1. Simple font model: `/Font` dicts, `/Widths`+`/FirstChar`/`/MissingWidth`, `/FontDescriptor`.
2. Embedded font parsing:
   - `FontFile` — Type1: PFA/PFB, eexec decryption, Type1 charstring interpreter → outlines; `/Encoding` from
     the font program.
   - `FontFile2` — TrueType: `glyf`/`loca`/`cmap`/`hmtx` via sfnt or in-repo tolerant parser; symbolic-font
     cmap selection quirks ((3,0) MS Symbol, (1,0) Mac, no-cmap → glyph index by code).
   - `FontFile3` — bare CFF (Type1C) and CIDFontType0C: CFF INDEX/DICT parsing, Type2 charstrings, charset,
     FDArray/FDSelect for CID-keyed CFF.
3. Encodings: Standard/WinAnsi/MacRoman/MacExpert + `/Differences`; AGL glyph-name→Unicode; Symbol and
   ZapfDingbats tables.
4. Composite fonts: Type0, CIDFontType0/2, `CIDToGIDMap`, embedded CMap streams, `Identity-H/V`, `/W` widths;
   predefined CJK CMaps from bundled Adobe data (can ship in a later minor release if size is a concern —
   decide at M6 start).
5. Type3 fonts: glyph procedures executed through the interpreter with the font matrix.
6. Non-embedded fonts: Base-14 name normalization (incl. the standard aliases), bundled OFL substitutes for
   glyphs, Core-14 AFM metrics for widths — widths drive search/text geometry, so metrics fidelity matters more
   than glyph-shape fidelity.
7. Text operators: `Tf Tj TJ ' " Td TD Tm T* TL Tc Tw Tz Ts Tr`; render modes 0–7 including clip modes;
   char/word spacing incl. the CID-vs-simple-font word-spacing rule; text rise; horizontal scaling.
8. Glyph pipeline: charstring/glyf → outline → rasterizer, with an LRU glyph-bitmap cache. `maxCacheSize`
   becomes the budget for this cache plus decoded-image and parsed-font caches (0 = unlimited, as today).

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

### M8 — Advanced graphics, hardening, cutover (~4–6 weeks)

1. Shadings types 1–7 (function-based, axial, radial, mesh types 4–7 with stream decoding), `sh` operator and
   shading patterns; tiling patterns (pattern matrix vs page matrix, XStep/YStep, resource scoping, tile cache).
2. Transparency: `/Group` transparency groups (isolated/knockout), soft masks (`/SMask` in ExtGState, alpha +
   luminosity), constant alpha (CA/ca), all 16 blend modes.
3. Annotation appearance streams: render each annotation's `/AP` `/N` form with `/Rect`↔`/BBox` mapping —
   `fz_run_page` includes annotations, so parity requires this (Link annotations usually have no appearance,
   but Widget/Stamp/FreeText etc. do).
4. Hardening: recursion/size/time limits audit; fuzz all new parsers; corpus soak run comparing every metric;
   `-race` suite; memory profiling with `maxCacheSize` budgets enforced; performance target within ~2–4× of the
   cgo build on the corpus (document actuals in the README).
5. Cutover: delete the cgo preamble, `lib/` (~6 static libs), `include/mupdf`, `update_from_release.sh`,
   `setup-windows.ps1`, and the Windows-toolchain docs; simplify `build.sh` and CI (no C toolchain, and
   cross-compiling becomes trivial — add a CI matrix proving `GOOS`/`GOARCH` builds with `CGO_ENABLED=0`);
   rewrite `CLAUDE.md`/README architecture sections; update `pdf_test.go` expected values only where the
   re-baseline in M7/M8 was accepted; tag a new major-ish release noting behavior deltas.

## 7. Testing strategy (continuous, not a phase)

- **Differential oracle testing** is the backbone: every milestone compares against cgo-MuPDF outputs captured in
  M0. Exact match for counts, TOC, links, auth status; perceptual threshold for pixels, tightening per milestone.
- **Existing tests stay authoritative** for API behavior (release semantics, limits, error sentinels) and run
  unchanged throughout; the exact-value assertions (search rects, TOC coords, stride/bounds) are the final
  fidelity gate.
- **Fuzzing** (native Go fuzzing) for: lexer, xref/repair, every filter, every font parser, content tokenizer,
  function evaluator, CMap parser. Seed with corpus slices. Crashes and hangs are release blockers — this package
  advertises untrusted-input safety via the `OverallMax*` knobs, and pure Go removes the memory-unsafety class
  but not resource exhaustion.
- **Golden-image regression** within the pure-Go implementation itself once it stabilizes (so later refactors
  can't silently shift pixels).

## 8. Risks and open decisions

| Risk / decision | Assessment & mitigation |
|---|---|
| **Scope is very large** | This is a 6–9 month effort for one experienced developer (~40–70k LOC). M6 (fonts) is the long pole. Mitigate by strict scoping to the API and by the milestone order above, which delivers verifiable value early (parse/auth/nav before pixels). |
| **JBIG2 decoder** | No permissive pure-Go implementation exists (unipdf's is AGPL; jbig2dec is AGPL). Options: (a) ship without it (blank regions, documented), (b) write a generic-region-only decoder from ITU T.88 (~2–3 weeks, covers most scanned PDFs), (c) full spec implementation later. Recommend (b) post-M8. |
| **JPX (JPEG 2000)** | Same situation. Rarer in the wild than JBIG2. Recommend ship without, document, revisit on demand. |
| **Exact-value test parity** | Search quads and TOC coordinates depend on font *metrics* (not rasterization), so exactness is achievable — but only if widths, MediaBox handling, and rounding all match MuPDF. Budget for a documented one-time re-baseline of `pdf_test.go` if a few values shift by ±1px. |
| **AGPL derivation** | Enforce the clean-room rule in review: MuPDF is run, never read-and-translated. Keep a `NOTICES` file for every bundled asset and adapted permissive source. |
| **Malformed-PDF tolerance** | MuPDF's real moat is 20 years of quirk handling. The oracle corpus + fuzzing is the countermeasure; expect a steady trickle of tolerance patches after cutover. Keep the cgo backend available behind its build tag for one release as an escape hatch. |
| **Performance** | Pure-Go rasterizer + charstring interpreters will be slower than optimized C. 2–4× slower is acceptable for the primary use case (interactive page rendering); profile-guided optimization in M8, and the per-document mutex already serializes work, so no concurrency regression. |
| **Binary size** | Embedded fonts + CJK CMaps could add several MB. Decide at M6: embed always, build tags, or a companion data module. |
| **Interim alternative (flagged, not chosen)** | If a usable pure-Go build is needed *before* the port matures: compile MuPDF to WASM and run it under `wazero` (pure Go, `CGO_ENABLED=0`, near-perfect fidelity, ~2–5× slower, keeps AGPL + adds a few MB). This can ship in weeks and later be swapped for the native port behind the same API. Worth a spike in parallel with M1 if there is schedule pressure. |

## 9. Milestone summary

| Milestone | Deliverable | Est. |
|---|---|---|
| M0 | Oracle harness, corpus, differential runner, branch/CI setup | 1 wk |
| M1 | Objects, xref (+repair), filters, page tree; `New`/`PageCount`/`Release` | 3–4 wks |
| M2 | Encryption; `RequiresAuthentication`/`Authenticate` | 1–2 wks |
| M3 | TOC, links, destinations; `TableOfContents`, link loading | 2 wks |
| M4 | Interpreter, rasterizer, color, functions; vector rendering | 4–6 wks |
| M5 | Image formats and image drawing | 2–3 wks |
| M6 | All font types, encodings, CMaps, glyph rendering | 6–8 wks |
| M7 | Structured text + search parity | 2–3 wks |
| M8 | Shadings/patterns/transparency, annotations, hardening, cgo removal | 4–6 wks |

Total: roughly 6–9 months of focused work, with the package remaining shippable on the cgo backend until the
M8 cutover.
