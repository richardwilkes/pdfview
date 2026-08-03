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
  Post-M2 hardening round 2 (2026-08-02): a systematic loop-termination audit of every decode-driven loop in the
  tree, cross-read against both PDFium reference snapshots, found seven more dropped or missing bounds — restored in
  one commit and pinned by 13 boundary tests (`hardening_test.go`), with all 9 JBIG2 goldens unmoved. Five are
  PDFium guards this translation dropped: `SDNUMEXSYMS`/`SDNUMNEWSYMS` capped at 65535 (uncapped they sized
  `make([]*Image, …)` and the IAID context table from a raw 32-bit field — 32 GB and 4 GB respectively),
  `SBNUMINSTANCES` bounded by 32 per remaining stream byte, `IsValidImageSize` restored once in `ParseRegionInfo`
  (covering text/halftone/generic/refinement in one place, and catching the ≥2^31-reads-negative case), the same
  check on the halftone grid `HGW`/`HGH`, and `TOTWIDTH` capped with the collective-bitmap size taken in uint64
  (it was a wrapping uint32, so a wrapped size slipped under the remaining-bytes guard). Two are beyond PDFium,
  which is unguarded at both: `REFAGGNINST` bounded on the arithmetic *and* Huffman aggregate paths (it becomes
  `SBNUMINSTANCES` inline, bypassing the glue's pre-scan — the audit's sharpest CPU amplifier), and bounds on every
  changing-element write in `jbig2_mmr_decoder.go`'s `uncompress2D`. That last one was a live panic, not a spin:
  the file is jbig2dec/pdf.js lineage with no PDFium counterpart, and a repeated VL1 mode code over a pinned
  reference grows the index without advancing `bitPos`, walking past the `width+5` buffer — confirmed by removing
  the guard and reproducing `index out of range [6] with length 6`, reachable through any halftone region with
  HMMR=1. The audit also corrected two of its own inherited assumptions: the `TOTWIDTH` collective bitmap exists
  only on the Huffman path (the arithmetic path decodes symbols individually), and the dead constants
  `JBig2MaxNewSymbols`/`JBig2MaxExportSymbols` were already in `jbig2_basics.go`, declared and never referenced.
  Post-M2 (2026-08-02): the deferred multi-hour soak found a third pristine-upstream hang nine minutes in — the SDD
  arithmetic export-flag loop spins forever once the decoder exhausts its data and returns zero-length runs, and the
  loop is equally unguarded in PDFium itself (both reference snapshots), so this is hardening beyond upstream, not a
  restored guard. Fixed in-tree with the GRD/GRRD `IsComplete` pattern; the ~90 s CPU spin now errors in
  microseconds, pinned by the committed FuzzJBIG2 seed. The Huffman export loop was audited safe (bit reads fail at
  end of stream); soak relaunched over the fix.
  Post-M2 (2026-08-02, resolved same day): the 2 h soak over the fully-hardened tree (commit 2443171) found a
  fourth hang at ~52 min (`8740de3ce74202c1`, 234 bytes; ~55 M execs clean before it) — the *same* arithmetic
  export-flag loop, which the `IsComplete()` guard from `0c175f3` does not cover: the arithmetic decoder parks on a
  terminal `0xFF` marker (`byteIn`'s `b1 > 0x8f` branch pads 1-bits without advancing the byte position), so
  `IsComplete()` never flips while IAEX returns zero-length export runs forever and `EXINDEX` never advances — a
  pure CPU spin (~110 MB RSS, `test timed out`). Fixed with the progress-based bound the finding called for:
  advancing runs are already bounded by the loop condition, so the guard counts only zero-length runs and errors
  past one per flag slot plus one (a conforming stream needs at most one — the optional leading run; PDFium and
  MuPDF accept nothing this rejects short of a stream no encoder emits). Confirmed beyond-PDFium hardening again:
  the reference snapshot's export loop (`jbig2_sdd_proc.cpp:194`) has checked arithmetic but no progress or
  completeness guard. The prescribed audit of every other `IAx`-driven loop found no further exposure — the
  arithmetic integer decoders exist only in the SDD and TRD files; SDD's height-class, width, and aggregate loops
  are bounded (`HCLASSES` cap, `NSYMSDECODED` guard, REFAGGNINST cap), TRD's strip loop decodes its first instance
  through IAFS, which has no out-of-band path, so every strip advances `NINSTANCES` toward the bounded
  `SBNUMINSTANCES`, and the halftone/pattern loops iterate fixed grid counts. The crasher is committed as FuzzJBIG2
  regression seed `8740de3ce74202c1` — the ~90 s spin now errors in microseconds (the seed run passes in 0.35 s
  where it previously hung past any timeout), `./build.sh -a` green. The 2 h FuzzJBIG2 soak relaunched over the fix
  (commit d48a3d7) then ran clean end to end — 448.8 M execs at ~62 k/s, PASS, no hangs or crashes — the first
  fully clean multi-hour soak over the hardened tree (its predecessors died at ~9 min and ~52 min). JBIG2 hardening
  is closed until M5's extended soak.
- **M3 — JPX integration. DONE (2026-08-02).** Vendored `mububoki/jpeg2000 v1.0.0 @6bfb77fe2e65` into
  `internal/jpeg2000/` (commit d1d5c23): 69 decoder `.go` files byte-identical to upstream modulo the provenance
  header line and import rewrites (independently diff-audited — only `j2k/reader.go` and `jp2/reader.go` differ, for
  the `image.RegisterFormat` init removal), encoder half pruned (37 files/6993 LOC, reverse-dependency-derived),
  MIT `LICENSE` + `PROVENANCE.md`, extended `.golangci.yml` exclusions. No ISO/IEC 15444-4 conformance data
  vendored (upstream keeps it external via `OPJ_DATA_ROOT` — no redistribution question). A test-scoped
  `register_pdfview_test.go` restores the image-registry side effect inside the e2e test binary only. Then (commit
  ebf4b11) the two container entry points the PDF layer needs, authored in `internal/jpeg2000/jp2/pdfview.go` with
  no vendored-file edit: `DecodeComponents` (raw per-component planes, no palette/cdef/colorspace applied — an
  /Indexed override consumes raw indices) and `DecodeInfo` (header-only: SIZ geometry + component precision/sign/
  subsampling, plus the container's colr/pclr/cmap/cdef/CIELab/ICC metadata). `DecodeInfo` parses SIZ itself rather
  than the vendored header pass, which allocates `[]tileState` proportional to the declared tile grid before any
  pixel — the `jpxSizGuard` hazard again; the authored parser is bounded by its input and cross-checked against the
  vendored `DecodeConfig` by test. Finally (this commit) the glue `internal/imaging/jpx.go` was flipped to the
  vendored imports and the external module dropped from `go.mod` (`go.mod` back to three deps). All 13 JPX goldens
  pass uncached through the vendored decoder; `./build.sh -a` green. README row + the now-stale "no pure-Go decoder"
  claim deferred to M5. M4 (PDF semantics) will wire `DecodeInfo`/`DecodeComponents` into the glue for the
  /ColorSpace-precedence, palette, cdef-opacity, and SMaskInData matrix.
  Absolute-cap decision (Rich, 2026-08-02): keep `maxImagePixels` = 2^26 with
  no JPX-specific absolute pixel cap — the sample-count proportional budget (83bdc3d) stays the only JPX-specific
  bound, accepting the measured ~90 B/px worst case (~6 GB peak for a crafted gray payload at the cap, ~2 GB RGB)
  in exchange for zero oracle divergence at large image sizes.
- **M4 — JPX PDF semantics. DONE (2026-08-02).** The three matrix rows M1 left unpinned got corpus files and fresh
  oracle goldens (the milestone's one regeneration; all 65 pre-existing goldens byte-identical), and the glue grew
  the vendored-component path M3 built the entry points for. New pins, each documented in the corpus README:
  `images-jpx-depth` — MuPDF shifts, never rounds or rescales (>8-bit truncates `v>>(p-8)`, <8-bit left-shifts
  `v<<(8-p)`, so 4-bit sample 15 renders 240; the sources carry a deliberate sub-8-bit ramp making truncation,
  shift-rounding, and rescale-rounding mutually distinguishable, and MuPDF matched truncation on every disagreeing
  sample). `images-jpx-smask` — a 3-component JPX `/SMask` reduces by the plain truncated mean `(R+G+B)/3`, not any
  luminance weighting; the arm also exposed that a JPX soft mask's always-smoothed posture must carry onto the
  raster it masks (compositeAlpha folds the mask onto the finer grid, so the combined image samples the way the
  mask alone would have). `images-jpx-stencil` — MuPDF ignores `/ImageMask true` on JPXDecode outright and paints
  the payload as an ordinary opaque image (neither drops nor thresholds); run() now diverts that pairing to
  decodeJPX, while the `/Mask` stencil-stream path keeps declining (no oracle evidence covers it).
  `images-jpx-ixjp2` — a JP2 container's own pclr/cmap palette is suppressed exactly when the PDF declares
  `/Indexed` (raw indices feed the PDF lookup, via jp2.DecodeComponents); under any other space it applies, and the
  post-palette component count then decides the usual override-arity rule (the `/DeviceGray` arm pins that half).
  Glue: `jpxWantsComponents` routes to the component path only for the Indexed-single-component case and for odd
  precisions (≠8/16) on containers carrying no palette/CIELab/sYCC/cdef-opacity — those combinations keep the image
  path (odd-precision ones render dark; no corpus coverage, constraint documented in code). A shared `jpxNorm`
  helper (signed-domain offset, clamp, shift) serves both the bare and container component paths; the bare path's
  missing sub-8-bit left-shift was fixed by it. FuzzJPX now drives both `/Indexed` verdicts per payload. The
  stencil corpus page is 144x144 pt so its full-page fill lands on integer raster rows at all three golden DPIs:
  on a fractional page height, canvas v0.2.1's four-sample vertical coverage diverges from MuPDF's analytic
  coverage on the partial bottom row (alpha 0 vs 16 on a 0.11 px sliver — a pre-existing engine-level divergence
  every golden carries sub-threshold; on a 116 pt page that one row alone spent 1.47 of the 2.0 parity mean).
  Deferred to M5: enumerated-CMYK JPX (no generator path; the library's own conversion covers the no-override
  case), the odd-precision-plus-container-machinery combinations above, and whether the canvas edge-AA divergence
  is worth a canvas-side fix. All corpus checks, TestParity 69/69, and `./build.sh -a` green.
- **M5 — Hardening and closeout. DONE (2026-08-03).** Every gate the milestone names has run; the cap audit
  found real defects in the JPX tree and they are fixed. What landed:

  **Gates.** veraPDF sweep: 2694 files, all opened, all rendered page 0, all searched, zero errors, zero hangs,
  slowest file 1.32 s — the first full sweep with both codecs live. Extended fuzz soaks, 2 h each, all PASS with no
  crashers and no hangs: `FuzzJBIG2` 439.8 M execs (~57 k/s), `FuzzJPX` 269.1 M execs, `FuzzImaging` 86.5 M execs.
  The JBIG2 number is the meaningful one for M2's hardening — its predecessors died at ~9 min and ~52 min, so a
  clean 440 M-exec run closes that thread. Note the JPX soak ran against the *pre-fix* tree (Go compiles the fuzz
  binary at launch and the fixes below landed mid-run), so a re-soak over the fixed tree is required before this
  milestone closes.

  **Cap audit (the milestone's main find).** An independent read-the-code audit of every file-driven allocation in
  both vendored trees, cross-checked against the glue's guards. `internal/jbig2` came back clean: all ten guards M2
  and its two follow-up rounds claim were found, each dominating the site it protects. The imaging glue came back
  clean: every codec path charges its budget before allocating, and the fuzz entry points match production. But
  `internal/jpeg2000` — the tree with far less scrutiny — had three real defects, two of them fatal, all confirmed
  at the code before any fix was written. The common shape is that `jpxSizGuard` charges a budget in *samples*
  (pixels × components), and each defect multiplies a budget-compliant allocation by a *count the budget does not
  model*:
  - **F1 (fatal)** — the COD quality-layer count is a raw `uint16` with only a `0→1` fixup and no cap anywhere, and
    it multiplies the packet-sequence slice (`numLayers·numResolutions·numComps·maxPrec` `[4]int` tuples). An
    **83-byte** codestream declaring `layers=0xFFFF` over a 1024×1024 single-component image demands **2.2 TB**.
  - **F2 (fatal)** — the JP2 `cmap` box's channel count is uncapped (`len(c)/4`), and `applyPalette` allocates one
    full w×h `int32` plane per channel, all retained. A **~41 KB** JP2 with 10240 cmap entries over a 2048×2048
    image demands **~160 GB**, growing 16 MB per 4 payload bytes.
  - **F3 (medium)** — the tile-part buffer is sized from `Psot` against a fixed 256 MiB absolute ceiling rather than
    the input that carries it, so a **78-byte** payload allocates 256 MiB up front before the short read fails.

  Why these mattered enough to fix rather than document: **a Go out-of-memory is an uncatchable `fatal error`**, so
  `recoverCodec` cannot degrade the image to blank. F1 and F2 kill the process, breaking the never-an-error-to-the-
  caller invariant in a way a panic never could. Neither fuzzer found them in ~700 M combined executions — both need
  crafted header structure that mutation from small seeds will not synthesize — which is exactly the argument for
  running a code audit alongside the soaks rather than trusting fuzzing alone.

  **Fixes (in-tree, matching M2's posture on the JBIG2 tree).** `tier2.go` bounds the packet-sequence product
  against a new `maxPackets` (1<<20, sized alongside its existing `maxPrecincts`/`maxCodeBlocks` siblings and
  capping the slice near 32 MB); the guard sits before the progression-order switch, which was verified to dominate
  all three sequence builders (each emits at most one packet per layer/resolution/component/precinct, and all three
  are called only from `tryParseStandardLRCP`, itself single-caller). `box/jp2.go` refuses a `cmap` longer than 32
  channels at parse — refused, never truncated, since a truncated map would mis-render — so the container falls back
  to non-palette handling, with a defensive mirror of the same cap at `applyPalette`'s allocation site. `sod.go`
  bounds `Psot` by the reader's unread length (every call site hands it a `*bytes.Reader`), with an incremental
  `io.LimitReader` fallback for any future reader lacking `Len`. Each is pinned by a crafted-payload test in
  `internal/jpeg2000/codestream/harden_test.go` and by a committed `FuzzJPX` regression seed, with an env-gated
  regenerator so the seeds cannot drift from the builders that produced them. All four edits are documented in
  `internal/jpeg2000/PROVENANCE.md` under a new "Allocation hardening" section; the vendored files keep their
  upstream MIT provenance headers. Verified independently of the worker's claims: vendored tree tests green
  uncached, **all goldens bit-identical** (so the caps reject nothing legitimate), seeds replay clean through the
  production glue.

  **F6 — investigated, hot path deliberately left unchanged.** The audit also flagged a suspected CPU spin in
  `engine/tagtree.go`: `ProgressiveDecodeValue` iterates to its 65536 cap, and because `ResetKnown` is never called
  in the decode path, a zero-bit-plane tag-tree parent saturated by one code-block leaves every later leaf in that
  precinct spinning the full loop while consuming no input. The mechanism was confirmed at the `TagTree` API level
  (a demonstration drove a root's `m` to 65536, after which a second leaf ran 65536 iterations on zero input), but
  no payload-level reproduction was completed, and the obvious guard — terminate when an iteration consumes no
  input — is **provably unsafe**: a legitimate leaf whose shared ancestors were saturated *below* the cap must run
  no-op iterations at low thresholds before reading its own bit at a higher one, so that guard would consume the
  wrong bits and change decoded values. A safe design exists (break only when the minimum path `m` reaches the
  `2^maxBits` cap, which legitimate zero-bit-plane values — always well under 40 — never reach) and is recorded in
  PROVENANCE for a properly-seeded pass. Correctness of the golden decodes outweighs a speculative guard with no
  committed reproduction. Estimated exposure if reachable: ~76 KB payload → ~10–30 s spin, bounded but real.

  **Benchmarks** (`internal/imaging/bench_codec_test.go`, fixtures + provenance under `testdata/bench/`, both
  driving the production entry points): a 2550×3300 300-dpi scanned-text JBIG2 page (symbol dictionary + 3088 text
  placements, jbig2enc 0.32) and 1024×1024 RGB JPX payloads under both wavelets (`opj_compress` 2.5.4, lossless 5/3
  and lossy 9/7). Sources machine-drawn, only encoder outputs committed. JBIG2 symbol mode is essentially free per
  pixel — a full 8.4 Mpx page decodes in ~3 ms at ~180 allocs, the 1.05 MB output plane dominating — because only
  the placements are decoded and the rest is blitting cached symbol bitmaps. JPX is ~100× more expensive per pixel:
  ~0.43 s per megapixel lossless (~110 B/px allocated), ~0.27 s lossy (~162 B/px). One anomaly recorded but not
  pursued: the 9/7 path allocates *more* than 5/3 (169.6 vs 116.3 MB) while running ~40 % faster on a 2.5× smaller
  payload, which looks like an allocation inefficiency in irreversible reconstruction rather than an inherent cost.
  Quiet-machine re-measure (2026-08-03, M4 Max, 8 counts, spread under ±2 %): JBIG2 2.17 ms/op at the same 178
  allocs (the original ~3 ms was load-inflated), JPX 5/3 0.379 s/op and 9/7 0.227 s/op — modestly faster than the
  loaded run, with allocation profiles byte-for-byte identical (116.25 vs 169.60 MB/op), so the 9/7-allocates-more
  anomaly is confirmed real rather than measurement noise. These are the numbers of record.

  **JBIG2 repack decision: moot, closed.** M2's glue already reads the packed bitmap through `Data()`/`Stride()`;
  there is no `image.Gray` intermediate to eliminate, and the benchmark's ~180 allocs/op confirms it.

  **`maxPixelsFor` decision (Rich, 2026-08-03): leave unchanged.** The benchmark fixture is the first real data
  point. At 300 dpi a scanned page uses 22.9 % of its budget (4493 B payload → 36.8 Mpx granted, 8.4 Mpx needed;
  1873 px/byte against the 8192 the cap allows). But a 600-dpi probe of the same page (not committed) yielded 4788 B
  for 33.7 Mpx — 7030 px/byte, headroom factor **1.17** — so a 600-dpi page with fewer distinct symbols or more
  reuse would be rejected outright. The cap stays anyway: no real file fails (the 2694-file sweep and every golden
  render), it is a *shared* cap across CCITT/raw/JBIG2, and it doubles as the JBIG2 decode-*work* bound — the same
  budget that turned M1's 20.4 s DoS into 55 ms. Raising it on synthetic evidence would weaken that for every codec.
  The knob is one line at `internal/imaging/imaging.go:62` if real 600-dpi scans ever demand it. Note the measured
  ratio is conservative in the safe direction: the synthetic source carries no scanner noise, and noise inflates
  payload bytes without adding pixels.

  **Docs and carve-out.** README's Stubs section is gone: the Filters row now states real JBIG2 and JPX coverage,
  the stale "no maintained pure-Go decoder exists for either" claim is removed, an "Out of scope" bullet keeps the
  appearance-synthesis exclusion, both vendored trees have Architecture rows and a license/provenance paragraph, and
  the fuzz-target count is corrected to twelve authored plus the two vendored. The corpus README and the
  parity/render test comments no longer describe the codecs as stubs. **Carve-out resolved as the plan intended:**
  our blank versus MuPDF's black square on the garbage `images-jbig2` payload remains the single documented
  divergence — now because the vendored decoder rejects the payload rather than because a stub declined it. The
  `internal/imaging` package comment needed no change (M4 already updated it).

  **Re-soak find (2026-08-03): the SIZ geometry swap — fixed.** The relaunched `FuzzJPX` soak over the F1–F3-fixed
  tree died at ~5m39s: a fuzz worker was killed while minimizing a 447-byte bare codestream that, replayed in normal
  test mode, decodes for ~25 s at ~77 GB peak RSS. Mechanism (confirmed by marker parse and CPU profile — 91 % of
  samples in `toImageGray` over one 12.9 Gpx plane): the leading SIZ declares an innocent 16×16 image, which is what
  `jpxSizGuard` budgets; a complete 205-byte tile-part (Psot honored) hands the parser back to `sectionMainHeader`
  (`processSOD` reuses that section between tile-parts); a second SIZ then redeclares the image as ~805M×16, and
  EOF-finalization sizes the component and output planes from the swapped geometry — pixels the budget never saw.
  The F-series shape yet again — a post-validation field the samples budget doesn't model — and a hole the cap audit
  missed because it checked allocation sites against SIZ rather than SIZ's own mutability. Fixed in-tree with a
  duplicate-SIZ rejection (`sizSeen` flag on the Decoder, reset per `Decode`): ISO 15444-1 A.5.1 permits exactly one
  SIZ, and OpenJPEG's marker state machine rejects the same stream, so the oracle loses nothing and no legitimate
  file is affected. Pinned by `TestDuplicateSIZRejected` plus two committed `FuzzJPX` regression seeds — the crafted
  `jpx_siz_swap_bomb.seed` (env-gated regenerator extended) and the found crasher `812c06133f20440a` (the full seed
  replay now passes in 0.45 s where that one input alone previously took 25.5 s). PROVENANCE.md gained the guard's
  row. `./build.sh -a` green (note: run it without forcing `CGO_ENABLED=0` in the environment — the oracle-module
  lint pass needs cgo); goldens untouched. The 2 h soak was relaunched over the fixed tree.

  **M4-deferred items resolved (Rich, 2026-08-03).** Odd-precision-plus-container combinations: leave as documented
  — the in-code constraint note stands, no corpus coverage; the 2694-file sweep exercised none, and there is no
  oracle evidence to pin against without speculative fixture engineering. Revisit only if a real file surfaces.
  Canvas edge-AA divergence: accepted as a documented engine-level divergence — sub-threshold on every golden,
  pre-existing, not a JBIG2/JPX regression; any fix belongs in richardwilkes/canvas on its own schedule, outside
  this plan. Enumerated-CMYK JPX: attempt a corpus file (the raw 4-component `opj_compress` route with a
  hand-assembled EnumCS 12 container, mirroring the palette JP2's precedent), falling back to documenting the gap
  if the generator route or MuPDF acceptance fails.

  **Enumerated-CMYK JPX closed (2026-08-03).** The M4 generator gap fell to the palette JP2's precedent: a worker
  encoded 4-component raw input (`opj_compress -F 64,64,4,8,u@1x1:1x1:1x1:1x1 -mct 0` — the `-mct 0` is mandatory,
  since the encoder otherwise applies RGB→YCC to any input of three or more components, and it never writes EnumCS
  12 on its own, labeling 4-component output sRGB by default and sYCC under `-mct 0`) and hand-assembled the
  container (`ihdr` NC 4 BPC 7, `colr` METH 1 EnumCS 12, no `cdef`). `images-jpx-cmyk.pdf` carries the payload in
  two arms — no `/ColorSpace`, and explicit `/DeviceCMYK` — over patches whose K-only, CMY-composite, and rich
  blacks separate the candidate conversion formulas. The oracle renders both arms identically through MuPDF's
  ICC-backed CMYK conversion (pure cyan → the (0,174,239) US-Web-Coated primary; the three blacks stay distinct at
  35/31/32, 54/54/57, and 0/0/0), which is exactly the conversion `internal/color` already captures for DCT CMYK
  and the `k` operator — and not the vendored decoder's naive `255−min` formula, which collapses the blacks. The
  glue therefore routes it away from the vendored rendering: `jpxWantsComponents` grew a third container case
  (EnumCS 12, exactly four components, no palette/CIELab/opacity machinery → raw component planes as ink values,
  reported via a second return so `jpxRasterOf` only accepts four planes on that verdict), `decodeJPX` converts
  ncomp=4 per pixel through the matching space (the dctCMYK shape minus its Adobe re-inversion, which is a DCT
  storage quirk), `jpxSpace` falls back to DeviceCMYK at arity 4 so both arms meet the oracle, and `grayPlane`
  generalizes its truncated mean to the raster's own component count (unpinned for CMYK in a mask role — a bounded
  default, documented as such). Pinned by `TestParity/images-jpx-cmyk` within `DefaultThresholds` (no per-golden
  thresholds.json), three new `TestJPXComponentsGate` rows, a four-component `grayPlane` case, and the `FuzzJPX`
  postcondition widened to admit ncomp 4. The regeneration (this milestone's one) left all 69 pre-existing goldens
  byte-identical; the corpus now counts 70. `./build.sh -a` green. mutool 1.28.1 agreed with the 1.27.2 oracle on
  every sampled patch, so the pin is stable across at least two MuPDF releases.

  **The closing gate (2026-08-03): the `FuzzJPX` re-soak over the final tree — PASS.** Run at `f50a3fb` with the
  whole hardening stack live (F1–F3 caps, the duplicate-SIZ guard, the CMYK component routing) and every corpus
  payload and regression crasher among its 2733 baseline seeds: 2 h clean end to end, 1,182,724,697 execs at
  ~165 k/s — 4.4× the throughput of the invalidated pre-fix soak, on a quiet machine, with an explicit
  `-timeout 150m` per the recorded fuzz gotcha — no crashers, no hangs, coverage corpus grown to 2849. With that,
  every gate this milestone names has run against what ships, and M5 — and the plan — is closed. Outstanding
  beyond the plan's scope: F6 stays documented-not-fixed in PROVENANCE.md pending a payload-level reproduction,
  the odd-precision-plus-container combinations and the canvas edge-AA divergence stay documented per Rich's
  2026-08-03 decisions, and merging `jbig2-jpx` into `main` is Rich's call.

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
