# Plan: render JBIG2Decode and JPXDecode images

Goal: replace the deliberate render-blank stubs for `JBIG2Decode` (ISO/IEC 14492 / ITU-T T.88) and `JPXDecode`
(JPEG 2000, ISO/IEC 15444-1 / ITU-T T.800) with real decoders, so pages carrying them render their images.

Hard constraints:

- **CGO-free.** No cgo, no C toolchain, no vendored native library, and no WASM-blob workaround (a wazero-hosted
  OpenJPEG/jbig2dec build would technically satisfy `CGO_ENABLED=0` but violates the README's "no vendored native
  library" promise; rejected). Everything is pure Go.
- **Public API unchanged.** All work lands under `internal/`. `apicontract_test.go` is the gate; it must not change.
  The only externally visible difference is behavioral and allowed: pages with JBIG2/JPX images stop rendering blank
  where those images sit, and the README's "Stubs" section shrinks.
- Existing invariants hold: panics never escape (fuzz-enforced), hostile input is bounded by documented caps before
  allocation, decode failures degrade to a skipped image (never an error to the caller), 64-bit-only arithmetic
  assumptions stay valid, every new `.go` file authored here carries the standard MPL-2.0 header (goheader-enforced),
  gofumpt formatting, 120-column comments.

## Current state (verified)

- `internal/cos.imageFilterSplit` already stops the filter chain at `JBIG2Decode`/`JPXDecode` and hands
  `(data, codec, parms)` to `internal/imaging` — no plumbing changes needed there.
- `internal/imaging` declines both codecs in three places: `run()` (imaging.go:191), `stencilPlane()` (decode.go:232),
  and `alphaPlane()` (masks.go:82), each returning `ErrUnsupportedCodec` so the interpreter skips the draw.
- CCITT shows the integration shape for a bilevel codec (`ccitt.go`: decode to packed 1-bpc rows at the codec's own
  column count, white-fill shortfall, `validCols` cropping); DCT shows it for a self-describing continuous-tone codec
  (`dct.go`: codec dimensions authoritative via `DecodeConfig`-then-budget-then-`Decode`, `dctSpace`
  component-count-mismatch fallback, direct RGBA emission).
- The corpus has `images-jbig2.pdf` / `images-jpx.pdf`, but their payloads are garbage bytes: their goldens pin
  MuPDF's *failure* behavior (JBIG2 padded to a black square; parity_test.go:176 carves this out). They become the
  damaged-payload leniency pins; real coverage needs new corpus files.
- `oracle/regen.sh` regenerates goldens from the sibling MuPDF binding (`../../pdf`, MuPDF 1.27.2). MuPDF bundles
  jbig2dec and OpenJPEG, so the oracle can produce real goldens for well-formed payloads.
- `FuzzImaging` seeds itself from `testfiles/corpus/images-*.pdf` by glob, so new corpus files are picked up
  automatically.

## Third-party candidates (evaluated 2026-08-02)

Two pure-Go decoders appeared in mid-2026 (both postdate the README's "no maintained pure-Go decoder exists for
either" claim, which is now stale either way). Findings from cloning and probing both:

**github.com/mububoki/jpeg2000** — MIT, zero dependencies, ~18.7k LOC, 66 test files, CI, decoder *and* encoder.
Decoder claims all 23 ISO/IEC 15444-4 conformance files, validated against OpenJPEG (the same library MuPDF renders
through — strong oracle alignment). Covers the full Part-1 feature set the plan needs: both wavelets, all progression
orders + POC, multi-tile, precincts, every code-block style, PPM/PPT, RGN, `pclr`/`cmap`/`cdef`, sYCC/ICC, 1–38-bit
signed/unsigned depths, and a `j2k.DecodeComponents` API returning native per-component planes
(`W/H/Precision/Signed/XRsiz/YRsiz/Samples []int32`) — exactly the shape the PDF glue needs. Verified locally:
builds and passes its suite with `CGO_ENABLED=0`; lossless encode→decode round-trip bit-exact; a forged SIZ claiming
1 Gpx is rejected instantly with ~2 MB allocated (real allocation caps in the SIZ/tile-part parsers); no `panic(` in
the codebase; has its own fuzz targets. Gaps: brand-new (June 2026), effectively zero users, single pseudonymous
author; the `jp2` package exposes only `image.Image` decoding — `DecodeComponents` and the colr/pclr/cdef metadata
are not reachable for JP2-wrapped payloads (only for bare codestreams), which PDF integration needs (an in-tree
change after vendoring, or an upstream PR).

**github.com/xiaoqidun/jbig2** — Apache-2.0, ~5.9k LOC, depends only on `x/image` (uses `x/image/ccitt` for MMR —
the same choice this plan made). Covers generic, symbol dictionary, text, halftone, pattern, and refinement regions,
arithmetic + Huffman, and has a PDF-shaped `NewDecoderWithGlobals(r, globals)` entry point. Verified locally: builds
and vets clean CGO-free, no `panic(`, garbage and 2.1M random-mutation inputs error cleanly without panics or
runaway allocation (weak evidence — mostly header rejects; real fuzzing still needed). Output is an 8-bit
`image.Gray` (0 = black) via `ToGoImage`; the packed 1-bpc bitmap is unexported. Gaps: **zero test files**; 22
commits, 1 star; and a provenance concern — the file-per-procedure layout, type names (`GRDProc`, `TRDProc`,
`SDDProc`), and the distinctive `ProgressiveArithDecodeState` design strongly resemble a translation of PDFium's
JBIG2 decoder (BSD-3), yet the NOTICE claims sole copyright with no PDFium attribution. Not necessarily fatal (we
could carry a PDFium BSD-3 notice ourselves as insurance), but it needs resolving — ideally an upstream question —
before this repo depends on it.

**Adoption posture (either codec): vendor into `internal/`, not a fork.** Copy the needed pieces into
`internal/jpeg2000/` and `internal/jbig2/` subtrees with imports rewritten (mechanical — their `internal/` layers
flatten under ours; the only external import is `x/image/ccitt`, already a dependency). This beats a fork here:
`go.mod` stays at three modules, the copy is immune to upstream force-pushes or tag moves, the API surgery we need
(see below) happens in-tree without maintaining a public fork's stability, and everything runs under this repo's
own review/lint/fuzz/CI. Both licenses permit it:

- MIT (jpeg2000): upstream has no per-file headers, so each vendored file gets a short provenance header
  ("Code from github.com/mububoki/jpeg2000, MIT — see LICENSE-jpeg2000") and the subtree carries the MIT text.
- Apache-2.0 (jbig2): existing per-file headers are retained verbatim, the subtree carries the Apache-2.0 text and
  the upstream NOTICE contents, and any file we modify gets the prominent change notice the license requires.

The repo's MPL-2.0 is file-scoped, so vendored files simply remain under their own licenses (the embedded
Liberation OFL-1.1 bundle is the precedent); the README's third-party section documents both. Each subtree gets a
PROVENANCE note recording the upstream commit hash vendored, so later upstream fixes can be diffed and
cherry-picked by hand. `.golangci.yml` excludes the vendored paths from goheader (Rich's MPL header applies to code
authored here, and must not be stamped onto someone else's copyright) and from style-level linters; vet-level
checks stay on. Prune what PDF rendering never calls — the jpeg2000 encoder (~3k LOC: `encoder.go`, `pcrd.go`, the
writers and their tests), `tools/`, `example/` — after confirming the surviving decoder tests (their testdata is
OpenJPEG-generated, ~3 MB, redistributable and provenance-documentable) still cover what the deleted round-trip
tests did. Behavioral leniencies (truncation handling, MuPDF parity) are enforced by our corpus/goldens/fuzz layer
regardless of who wrote the decoder.

## Decision and recommended path

The corpus files, goldens, imaging glue, PDF-semantics work (`/ColorSpace` precedence, `SMaskInData`, `/Decode`
rules, stencil/SMask paths), fuzzing, and hardening are needed under **every** path — the only variable is who
produces the decoded samples. So the plan front-loads an evaluation spike (M1) that builds the shared
corpus+goldens+glue scaffolding against the third-party decoders, and only falls back to from-spec ports (the
appendix below) for whichever codec fails its gate.

- **JPX: adopt `mububoki/jpeg2000` (vendored), pending the M1 gate.** A from-spec Tier-1/EBCOT + DWT implementation
  was this plan's long pole (months); the library covers it with conformance-file validation against the very
  implementation our oracle renders through. Vendoring needs: expose `DecodeComponents` + colr/pclr/cdef metadata
  at the `jp2` level (small, mechanical — the internals already carry it).
- **JBIG2: lean adopt (vendored), but genuinely conditional.** The library is untested upstream, so our corpus/fuzz
  layer carries all verification weight, and the provenance question must be settled first (ask upstream; failing
  clarity, treat it as PDFium-derived and carry the BSD-3 notice, or drop it). The from-spec fallback is credible
  here — JBIG2 is the smaller codec (~5–6k LOC, matching the library's own size) and the repo already has the CCITT
  integration pattern — so the gate can afford to be strict.

M1 gate, per codec: renders the new real-payload corpus within parity thresholds against fresh MuPDF goldens
(including truncation leniency — MuPDF keeps partial JBIG2 pages; a decoder that hard-errors where MuPDF degrades
fails the gate unless the glue can recover equivalent behavior), survives a multi-hour fuzz soak under our harness
postconditions (no panic, no hang, allocations bounded by the budget the glue enforces), and — for JBIG2 —
provenance resolved. Pass → vendor + integrate. Fail → port from spec per the appendix.

## Integration design (common to both paths)

```
internal/imaging/jbig2.go   glue mirroring ccitt.go
internal/imaging/jpx.go     glue mirroring dct.go
```

**JBIG2 (the CCITT shape).** Entry: `decodeJBIG2(h) (data []byte, cols int, err error)` producing packed 1-bpc
byte-aligned rows, **0 = black** (MuPDF and pdf.js both invert JBIG2's internal 1=black so samples read correctly as
DeviceGray, and an ImageMask stencil with default `/Decode [0 1]` paints where the ink is). Wired into the same
three switch sites as CCITT (`decodeSamples`, `stencilPlane`, `alphaPlane`); page-bitmap width is `cols` (dict
`/Width` crops or zero-extends via `validCols`, `/Height` shortfall white-fills); `bitsPerComponent` not consulted.
`/JBIG2Globals` comes from `dec.parms`; its parsed form is worth caching per stream ref only if profiling says so.
If the adopted library's `image.Gray` output is kept, the glue repacks or thresholds — a copy per image, acceptable
until benchmarks say otherwise (the vendored tree can export the packed bitmap directly later).

**JPX (the DCT shape).** Codestream dimensions authoritative (dict `/Width`/`/Height` only position the unit
square); `/BitsPerComponent` ignored; `DecodeConfig`-equivalent header parse feeds `maxPixelsFor` *before* full
decode, exactly like `dctImage`. Emits RGBA directly from component planes with LUTs for 1-/3-component device
paths. PDF-specific semantics, each pinned against the oracle:

- **/ColorSpace precedence**: when present it overrides the embedded colorspec *if* its component count matches the
  codestream (payload wins on mismatch — the `dctSpace` rule). When absent, map the embedded spec: sRGB/e-sRGB/ROMM
  → DeviceRGB, greyscale → DeviceGray, sYCC → convert, enumerated CMYK → DeviceCMYK, ICC → N-component device
  fallback (the ICCBased posture), palettized → expand.
- **/Decode**: honored only when the space is Indexed (the spec's rule); confirm the oracle agrees and pin either way.
- **/SMaskInData**: 0 → ignore embedded opacity, `/SMask` applies; 1 → `cdef` opacity channel becomes straight
  alpha, `/SMask` ignored; 2 → premultiplied, un-premultiplied into the straight-alpha `Image.Pix` contract.
- **Masks**: JPX as an `/SMask` payload decodes via a gray-plane path analogous to `dctGrayPlane`; JPX as an
  ImageMask stencil is spec-illegal — keep declining unless the oracle shows MuPDF thresholding.
- **Depth normalization**: components >8 bits shift down to 8 (MuPDF renders through 8-bit pixmaps; the oracle is
  the arbiter of rounding). Depths >16 may decline to blank if the oracle path never exercises them.

**What deliberately does not change:** no `DecodeWork` charging for codec expansion (the DCT/CCITT precedent —
pixel caps bound the work); `imageFilterName`, `ImageFilterSplit`, the decoded-image cache, and `NeedsResources`
untouched; `ErrUnsupportedCodec` survives for genuinely out-of-scope payloads so the degrade-to-blank contract and
its tests keep their shape.

## Testing

**Corpus + goldens (oracle parity):** new minimal PDFs, one dump line each in `regen.sh`, provenance in
`testfiles/corpus/README.md`:

- `images-jbig2-generic` (arith + MMR), `images-jbig2-text` (symbol/text), `images-jbig2-globals`,
  `images-jbig2-huffman`, `images-jbig2-halftone`, `images-jbig2-refine`, `images-jbig2-stencil` (ImageMask +
  `/Mask` stencil usage), `images-jbig2-truncated` (leniency).
- `images-jpx-53` (lossless RGB), `images-jpx-97` (lossy, ICT), `images-jpx-gray`, `images-jpx-ycc` (subsampled),
  `images-jpx-palette`, `images-jpx-alpha1` / `images-jpx-alpha2` (SMaskInData), `images-jpx-tiles`,
  `images-jpx-bypass` (mode switches/precincts), `images-jpx-raw` (bare codestream), `images-jpx-csoverride`
  (`/ColorSpace` + Indexed/`/Decode` matrix), `images-jpx-truncated`.
- Payloads generated dev-time with jbig2enc and `opj_compress` from our own source images (outputs are ours; only
  outputs committed). Note `mububoki/jpeg2000`'s encoder can generate JPX payloads too, but corpus payloads should
  come from an *independent* encoder (OpenJPEG) so decoder and test data don't share ancestry.
- Regeneration wipes and rebuilds *all* goldens; the diff on unrelated goldens must be empty (same MuPDF build).
- Existing `images-jbig2`/`images-jpx` stay as damaged-payload pins. Once the real decoder also renders their
  garbage payloads blank, revisit the parity_test.go:176 carve-out — ideally our blank + MuPDF's black square
  remains the one documented divergence.
- Thresholds: JBIG2 is deterministic bilevel — `DefaultThresholds` should pass with near-zero deltas; 9/7 JPX may
  need per-golden `thresholds.json`, hand-tuned and justified in the commit.

**Unit fixtures:** on the adopt path these live mostly in the vendored trees — jpeg2000's decoder tests and
testdata come along in the copy; the JBIG2 tree *needs* a test suite built for it (jbig2enc payloads + committed jbig2dec
`.pbm` output, compared bit-exact — the same fixtures the port path would use, and the M1 gate's evidence). On a
port path, add T.88 Annex H.2 MQ vectors and OpenJPEG-comparison fixtures per the appendix.

**Fuzzing:** `FuzzJBIG2` and `FuzzJPX` harnesses over raw payloads through the *glue* entry points (so our budget
enforcement wraps the library exactly as production does), seeded from the fixtures; `FuzzImaging` picks up the new
corpus PDFs automatically. Postconditions: no panic, no hang, output consistent with claimed dimensions, allocation
bounded. Multi-hour local soak before each codec ships. The veraPDF corpus feeds `soak_test.go` with real-world JPX
(PDF/A-2+); cherry-picks into the corpus allowed with attribution.

**Benchmarks:** JBIG2 (text-region-heavy scanned page) and JPX (megapixel 5/3 and 9/7) cases alongside
`bench_test.go`; on the adopt path this also decides whether the JBIG2 `image.Gray` repack or a vendored-tree
packed export is warranted.

## Milestones

Each ends green on `./build.sh -a` (build + tests + race + lint) with `CGO_ENABLED=0`. Goldens regenerate at most
once per milestone.

- **M1 — Corpus, goldens, and the adoption gate.** Build the full corpus above + regenerate goldens; wire both
  libraries behind prototype glue on a branch; run parity, truncation-leniency, and fuzz-soak gates; settle the
  JBIG2 provenance question. Exit: adopt/port decision per codec, recorded here with the evidence.
- **M2 — JBIG2 integration. DONE (2026-08-02).** Vendored `xiaoqidun/jbig2 @cddd575` into `internal/jbig2/`
  byte-identical (unit 1), pinned by a mutation-proofed fixture suite (unit 2), then hardened in-tree (unit 3):
  a cumulative `Limits{MaxPixels}` charged before every stream-sized bitmap allocation (the M1 DoS drops 20.4 s →
  55 ms), all five dropped PDFium guards restored (the dossier's three plus two more hangs the M2 fuzz found in
  pristine upstream — a TRD nil-symbol spin and a segment-offset backward wrap), a big-endian embedded-profile
  entry point (`NewEmbeddedDecoder`/`DecodePage`, no synthetic file header, no CWS/SWF unwrap, no endian sniff),
  the `image.RegisterFormat` global side effect and container/gray-expansion layers pruned, and triple attribution
  (upstream Apache-2.0 + NOTICE, PDFium BSD-3, PDFBox/ASF+levigo) in `NOTICE`/`LICENSE-pdfium`/`PROVENANCE.md`.
  The refinement-gating divergence from PDFium was investigated and found correct (the Go bodies use bounds-checked
  accessors, not PDFium's raw-pointer fast paths), pinned by two tests. Glue reads the packed page via
  `Data()`/`Stride()` directly. All 9 JBIG2 goldens pass; the external module dependency is gone. Truncation still
  degrades to the oracle's white page via the glue (the decoder keeps no partial output). README row deferred to M5.
  Post-M2 (2026-08-02): the deferred multi-hour soak found a third pristine-upstream hang nine minutes in — the SDD
  arithmetic export-flag loop spins forever once the decoder exhausts its data and returns zero-length runs, and the
  loop is equally unguarded in PDFium itself (both reference snapshots), so this is hardening beyond upstream, not a
  restored guard. Fixed in-tree with the GRD/GRRD `IsComplete` pattern; the ~90 s CPU spin now errors in
  microseconds, pinned by the committed FuzzJBIG2 seed. The Huffman export loop was audited safe (bit reads fail at
  end of stream); soak relaunched over the fix.
- **M3 — JPX integration.** Vendor into `internal/jpeg2000/` with the `jp2` metadata/`DecodeComponents` exposure
  and encoder prune (or start the port per appendix B), production glue, budget pre-check from the header parse,
  component→RGBA pipeline, README row. Absolute-cap decision (Rich, 2026-08-02): keep `maxImagePixels` = 2^26 with
  no JPX-specific absolute pixel cap — the sample-count proportional budget (83bdc3d) stays the only JPX-specific
  bound, accepting the measured ~90 B/px worst case (~6 GB peak for a crafted gray payload at the cap, ~2 GB RGB)
  in exchange for zero oracle divergence at large image sizes.
- **M4 — JPX PDF semantics.** `/ColorSpace` precedence matrix, `/Decode`-with-Indexed, SMaskInData 0/1/2,
  `/SMask`-as-JPX gray path, stencil posture, depth normalization — each pinned to oracle goldens.
- **M5 — Hardening and closeout.** Cap audit, extended fuzz soak, veraPDF sweep, benchmarks (and the JBIG2 repack
  decision), `maxPixelsFor` tuning if real scans demand it (JBIG2's symbol reuse compresses better than the CCITT
  8192×-payload rationale assumes), docs cleanup: imaging package comment, README "Stubs" section and the
  now-stale "no maintained pure-Go decoder exists" claim, parity carve-out resolution, corpus README provenance.

Port fallbacks (appendices) insert 2–4 additional milestones for the affected codec, matching the original
from-spec plan's phasing (JBIG2: generic → symbol/text → completeness; JPX: core 5/3 path → breadth).

## M1 evidence (2026-08-02) — gates run; verdict is Rich's call

- **Corpus and goldens.** All 20 real-payload files landed (commit 84c213c) with regen.sh registration and README
  provenance; `oracle/regen.sh` then rebuilt every golden — the 45 pre-existing ones byte-identical, so the corpus
  and oracle build are exactly reproducible. Notable oracle pins the corpus established: MuPDF ignores
  `/SMaskInData` entirely (embedded JPX alpha always applies; an explicit /SMask *multiplies* with it), ignores
  `/Decode` for JPX even under Indexed, silently paints a truncated-mid-region JBIG2 image as a full-size white
  plane, and hard-drops truncated JPX (strict OpenJPEG).
- **Prototype glue** (this commit) wires both pinned libraries (`mububoki/jpeg2000 v1.0.0`,
  `xiaoqidun/jbig2 @cddd575` — temporary go.mod deps, replaced by vendored `internal/` trees at M2/M3) at the
  planned switch sites, with pre-decode budget enforcement (a JBIG2 segment-header scan bounds every
  allocation-sizing field including the globals stream; JPX header parse feeds `maxPixelsFor`), panic-to-error
  recovery, and the oracle-pinned semantics above. `FuzzJBIG2`/`FuzzJPX` targets drive the production entry points.
- **Parity gate: 65/65 goldens pass** — all 13 JPX within `DefaultThresholds` (no per-golden thresholds.json, 9/7
  included) and all 8 JBIG2. Getting there exposed two pre-existing engine divergences, fixed here and pinned by
  unit tests plus the goldens: images magnified below 2× must blend samples (`blendsSamples`, taken from MuPDF's
  `fz_paint_image_imp` rule and probe-verified at 1.83×/1.92× vs 2.00×/2.04×), and a /Mask finer than its base
  image composites on the finer grid instead of being decimated. Fixing the latter surfaced a canvas v0.2.0 bug
  (sprite blit lane composites `AlphaTypeUnpremul` sources as premultiplied — reported as richardwilkes/canvas#1);
  `rasterImage` now premultiplies at hand-off, correct for both canvas lanes.
- **Fuzz gate.** JPX: the 90 s smoke was clean, but a 2 h soak found one real defect at ~47 min: an 80-byte
  codestream declaring a budget-compliant 16x256048 image cut into 128024 two-row tiles decoded successfully in
  ~4 s over ~300 MB of per-tile bookkeeping (concurrent fuzz workers then OOMed). Header-declared, so — unlike the
  JBIG2 case — it is boundable outside the library: `jpxSizGuard` now validates the SIZ marker (component count,
  zero subsampling, image area, and declared tile count against what the payload could physically carry, one SOT
  per tile) before the library parses, the crasher is a committed FuzzJPX regression seed rejecting in
  microseconds, and the soak was relaunched clean over the guard. JBIG2:
  **fails as an external dependency** — beyond the allocation-sizing fields the glue can bound externally, symbol
  bitmap dimensions are arithmetic-decoded, so no wrapper can bound decode *work*: a ~100-byte payload+globals
  pair decodes for ~20 s (live DoS; the library's only guard is 65535 per side per symbol with no cumulative area
  cap — an in-decoder fix). It also rejects PDF-embedded streams outright unless the glue prepends a synthetic
  9-byte T.88 file header, and upstream has zero tests.
- **Provenance (dossier in the 2026-08-02 session records; summary in the project memory).** `xiaoqidun/jbig2` is
  a translation of PDFium's JBIG2 decoder (≥98% confidence — e.g. PDFium's PDF-object-number cache key surviving
  as an unused constructor argument, Chromium-local `num_ex_syms` reproduced verbatim) plus a second undisclosed
  upstream: its MMR decoder is PDFBox's `MMRDecompressor` row-for-row. The translation *dropped* PDFium hardening
  (checked arithmetic in the SDD export loop, the export-bounds guard, the `!USESKIP` conjunct). Vendoring means
  three notice sets (xiaoqidun Apache-2.0, PDFium BSD-3 incl. Foxit, ASF/levigo Apache-2.0 + NOTICE).
- **Verdict (decided by Rich, 2026-08-02): JPX — adopt `mububoki/jpeg2000`**; vendor at M3 with the three-export
  jp2 surface (`DecodeComponents`, metadata, header-only config), the encoder prune, and the `jpxSizGuard`-style
  bounds kept at the glue. **JBIG2 — do not adopt `xiaoqidun/jbig2` as-is; take option (a): vendor + harden.** M2
  therefore vendors it into `internal/jbig2/` and, in-tree: fixes the symbol-area DoS (cumulative area cap fed
  from the glue's budget), restores the three dropped PDFium guards (SDD export-loop checked arithmetic, the
  export-bounds guard, the `!USESKIP` conjunct), adds an embedded-profile entry point (removing the glue's
  synthetic file header), builds the missing test suite (jbig2enc payloads + committed reference output per the
  Testing section), and carries the three notice sets (xiaoqidun Apache-2.0 + NOTICE, PDFium BSD-3 incl. Foxit,
  ASF/levigo Apache-2.0 + NOTICE) with per-file provenance headers and goheader exclusions. The M1 empirical
  gates passed for JBIG2 correctness (all 8 corpus files decode; bit-exact vs oracle where the renderer agrees),
  so option (a) buys hardening and attribution, not decode capability.

## Risks and open questions

- **Single-author, zero-user dependencies.** Both libraries are weeks old with no ecosystem soak. The vendoring
  posture (a pinned in-tree copy under our review, lint, and fuzzing — never a mutable remote) and our own
  fuzz/parity layers are the mitigation; the M1 gate is deliberately empirical, not README-trusting.
- **JBIG2 provenance** (likely PDFium translation, unattributed) must resolve before adoption; the port path is the
  clean fallback and is sized accordingly.
- **Truncation-leniency parity** is the most likely adopt-path failure mode: this engine and MuPDF both keep partial
  output where a library may prefer hard errors. The gate tests it explicitly; glue-level recovery (decode what
  returned before the error) may need vendored-tree support.
- **9/7 float divergence** vs OpenJPEG at the LSB level is expected regardless of path; per-golden thresholds need a
  first real data point in M3/M4 before the policy is final.
- **Oracle nuances to pin, not assume**: MuPDF's JBIG2 bit polarity through ImageMask, JPX depth normalization and
  sYCC handling, `/Decode`-with-JPX, SMaskInData=2 un-premultiply rounding. Where MuPDF's behavior is a bug we
  choose not to reproduce, the divergence gets the images-jbig2-style documented carve-out.
- **Tooling presence**: jbig2enc and OpenJPEG CLIs (brew) needed dev-time for fixtures; the oracle needs the usual
  `../../pdf` checkout. CI needs neither.

---

## Appendix A — from-spec JBIG2 port (fallback)

Scope: the PDF embedded-stream profile of T.88 — no file header, sequential segment headers, all segments
implicitly page 1, plus `/JBIG2Globals` parsed first into a shared segment context. Packages: `internal/mq` (MQ
arithmetic decoder, T.88 Annex E ≡ T.800 Annex C — shared with a JPX port if both fall back) and `internal/jbig2`.

Components in dependency order: segment headers (short/long referred-to forms, retain bits, 1-/4-byte page
association, the 0xFFFFFFFF unknown-length generic-region case); MQ + the T.88 arithmetic integer procedures (IADH,
IADW, IAEX, IAAI, IADT, IAFS, IADS, IAIT, IARI, IARDW/H/X/Y, IAID); generic regions (templates 0–3, nominal and
arbitrary AT pixels, TPGDON with per-template SLTP contexts, MMR via `x/image/ccitt` with an in-package T.6 fallback
if its contract can't express embedded MMR); symbol dictionaries (height classes, export runs, arithmetic + Huffman,
SDREFAGG); text regions (strips, REFCORNER, transposed, SBCOMBOP, instance refinement, runcode-coded symbol ID
tables, standard tables B.1–B.15 + custom table segments); pattern/halftone (gray-code bitplanes over generic
decoding); refinement regions (templates 0–1, TPGRON); page assembly (striping, end-of-stripe height resolution,
default pixel value, combination operators). Unknown segments are skipped (bounded header parse); a region failing
mid-decode keeps what composed, like the CCITT truncation behavior. Caps: page/region dims and cumulative
symbol-bitmap area against `maxImagePixels`/`maxPixelsFor` before allocation; placement offsets validated;
text-region instance counts bounded by region area; Huffman run lengths clamped; referred-segment graphs
size-checked. Licensing: implemented from T.88; jbig2dec (AGPL) is a behavioral reference only; pdf.js and
jbig2-imageio (Apache-2.0) may be consulted with attribution if any routine ends up a recognizable port.

## Appendix B — from-spec JPX port (fallback)

Scope: 15444-1, bare and JP2-wrapped codestreams; Part-2 extensions decline to blank. Package: `internal/jpx` (+
`internal/mq`). Components: JP2 box walk (`jp2h`: `ihdr`, `bpcc`, `colr` precedence, `pclr`, `cmap`, `cdef`;
`jp2c`); codestream headers (SIZ, COD/COC, QCD/QCC, POC, tile-parts, PPM/PPT, SOP/EPH; TLM/PLT/COM skipped);
Tier-2 packet decoding (inclusion/zero-bitplane tag trees, precincts, Lblock, all five progression orders + POC);
Tier-1 EBCOT (three passes with T.800 context formation, run-length/UNIFORM contexts, sign decoding, and every COD
mode switch: bypass, RESET, RESTART, vertically causal, segmentation symbols, predictable termination; pass counts
spec-bounded, bit-plane counts clamped); inverse quantization (all three styles, guard bits, RGN maxshift);
inverse DWT (5/3 integer and 9/7 float lifting, arbitrary NL, symmetric extension); component pipeline (RCT/ICT,
DC shift, signed handling, subsampling upsample, palette expansion, `cdef` roles, depth normalization). Unit
fixtures: `opj_compress`-generated files — 5/3 must reproduce sources exactly, 9/7 compares against committed
`opj_decompress` output within a documented per-sample tolerance; MQ gets the T.88 Annex H.2 vectors. Phasing:
core lossless path (container/T2/T1/5/3/RCT, single-tile 8-bit gray/RGB) first, then breadth (9/7 + ICT,
quantization styles, ROI, subsampling, multi-tile, 12/16-bit + signed, palette/cdef, sYCC/ICC/CMYK, PPM/PPT).
