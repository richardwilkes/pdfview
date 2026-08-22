# Provenance — `internal/jpeg2000`

## What this is

A vendored, pinned copy of a third-party JPEG 2000 (ISO/IEC 15444-1 Part 1) decoder. It is not pdfview code and does
not carry pdfview's MPL-2.0 header; every file keeps an upstream-provenance line instead, and `.golangci.yml` excludes
this directory from the repository's header, style, and formatting rules. Correctness and security linters still run
over it.

| | |
| --- | --- |
| Upstream | https://github.com/mububoki/jpeg2000 |
| Version | v1.0.0 |
| Commit | `6bfb77fe2e65a591d93918eb336c4dc3b615faaf` |
| Commit date | 2026-06-19 |
| Vendored on | 2026-08-02 |
| License | MIT |

Upstream lays the packages out as `internal/{binutil,box,codestream,engine,wavelet}` plus top-level `j2k`, `jp2`, and
`test`. That `internal/` level is flattened away here — this whole tree is already inside pdfview's `internal/` — so
upstream `internal/codestream` is `internal/jpeg2000/codestream`, and so on. The import paths were rewritten to match;
nothing else about the layout changed.

## What was pruned and why

pdfview decodes JPEG 2000; it never writes it. The encoder half is unreachable code that parses no input, so it is
attack surface with no payoff and maintenance weight on every future rebase. It was removed at vendoring time rather
than left to `unused`.

| Pruned | Files | Lines |
| --- | --- | --- |
| Encoder implementation | 9 | 3823 |
| Encoder tests (round-trip encode→decode, OpenJPEG cross-checks, encoder benchmarks) | 28 | 3170 |
| Total | 37 | 6993 |

Implementation files removed: `box/writer.go`, `codestream/encoder.go`, `codestream/pcrd.go` (post-compression
rate-distortion allocation), `engine/mqenc.go`, `engine/tier1enc.go`, `engine/tier1rc.go`, `wavelet/forward.go`,
`j2k/writer.go`, `jp2/writer.go`. The set was established by reverse dependency from the `j2k`/`jp2` writer entry
points: each candidate was deleted and the tree rebuilt until nothing surviving referenced it.

`engine/bitwrite.go` is the one encoder-side file that survives. `engine/tagtree.go` — which the packet-header decoder
needs — carries the tag-tree encoder in the same file (`ProgressiveEncodeBool`, `ProgressiveEncodeValue`), and those
take a `*BitWriter`. Vendored files are not split, so `BitWriter` stays; `engine/bitwrite_test.go` stays with it,
because it now tests surviving code.

Upstream's `example/`, `tools/gen-testdata/` (a C driver for the OpenJPEG encoder that regenerates test vectors),
`.github/`, `README.md`, `ROADMAP.md`, `.gitignore`, and `go.mod` were not copied. The package tree imports only the
standard library, so no module dependency was added.

The decoder tests were kept whole: nothing under `test/e2e` used the encoder, so the entire end-to-end suite and both
fuzz targets (`j2k.FuzzDecode`, `jp2.FuzzDecode`, with their seed corpus) survive unchanged.

### Coverage left behind by the pruned round-trip tests

The deleted encoder tests proved a feature by encoding it and decoding it back. Where the same feature also has a
decode-only vector in `test/testdata`, the surviving end-to-end test still covers the decoder side. Where it does not,
the decoder path is now covered only by the opt-in ISO conformance suite (`TestConformance`, which needs
`OPJ_DATA_ROOT` and is skipped by default), or not at all:

| Axis | Surviving decode coverage |
| --- | --- |
| PPM / PPT packed packet headers | Opt-in conformance only (`p1_02`, `p1_03`, `p1_05`, `p1_06`). No vendored vector. |
| SOP / EPH / COM markers | None vendored, and not asserted by any conformance case. |
| Per-component wavelet, per-component decomposition levels | Opt-in conformance only (`p0_05`, `p1_03`). |
| 4:2:2 (horizontal-only) subsampling | Opt-in conformance only (`p0_02`, `p1_01`, both `dx=2`); `sub420_n2.j2k` covers 4:2:0 only. |
| Everything else the pruned tests exercised — 5/3 and 9/7, all five progression orders, POC, precincts (tiled and untiled), tiles and tile-parts, quality layers, rate control, code-block styles (bypass, termall, reset, vertically causal, segmentation symbols), COC, QCC, RGN, TLM/PLT, palette, `cdef`, sYCC, 8/12/16-bit and signed samples, JP2 container | Vendored decode vectors under `test/testdata`. |

## Local modifications

Four mechanical changes, four allocation-hardening guards, and five SIMD dispatch conversions were made. A diff of every
vendored `.go` file against the pinned upstream commit, with the provenance line stripped and the import rewrites
reversed, is empty except for the two files in the third row of the first table, the four files in the hardening table,
and the five files in the SIMD dispatch table below.

| Change | Where | Why |
| --- | --- | --- |
| Import rewrites | Every file that imports a sibling package | `github.com/mububoki/jpeg2000/...` → `github.com/richardwilkes/pdfview/internal/jpeg2000/...`, with upstream's `internal/` level flattened away. |
| Provenance line | Every vendored `.go` file, first line, above any package doc comment | Names the upstream, version, and license at the point of use, and keeps pdfview's MPL header off someone else's code. |
| `image.RegisterFormat` removal | `j2k/reader.go`, `jp2/reader.go` | A library must not mutate the process-global image registry on import; `internal/jbig2` set this precedent. The `init` functions are gone, along with the package-doc sentences that advertised them. Nothing else in those comments was reworded. |
| Encoder prune | See above | Dead, unreachable attack surface. |

The upstream package doc comments for `j2k` still describe `Encode`, `EncodeWithOptions`, `EncodeComponents`, and
`EncodeOptions`, which the prune removed. They were left alone: rewriting them is a judgement call, not a mechanical
change, and keeping the diff against upstream to the four items above is worth more than the stale sentences cost.

### Allocation hardening

The same in-tree hardening posture applied to `internal/jbig2` (see its `PROVENANCE.md`): the PDF image glue
(`internal/imaging/jpx.go`) charges a pixels×components budget from the SIZ marker before this tree parses, but three
allocation sites are sized by file fields that budget never models, so they blow past it. Two are fatal — they demand
terabytes from a sub-100-byte payload, and a Go out-of-memory is an uncatchable `fatal error`, so the glue's `recover`
cannot degrade the image to blank. Each guard bounds the work against the input that carries it, matching the existing
`maxPrecincts`/`maxCodeBlocks`/`maxTilePartBytes` limits already in the tree. Vendored files keep their upstream
provenance headers; the edits are documented here, not in code comments.

| File | Site | Bound added | Attack closed |
| --- | --- | --- | --- |
| `codestream/tier2.go` | `tryParseStandardLRCP`, before the progression-order switch | Reject when `numLayers·numResolutions·numComps·maxPrec > maxPackets` (new const `1<<20`, alongside `maxPrecincts`/`maxCodeBlocks`). Every builder — `packetOrder`, `pocSequence`, `packetSeqPosition` — emits at most one packet per (layer, resolution, component, precinct), so the product bounds the returned `[][4]int` slice and each builder's iteration count. | A COD with `layers=0xFFFF` (and/or a large decomposition depth) over a single-precinct image makes the builders enumerate millions of packets, sizing a multi-megabyte sequence slice from a few payload bytes. Fatal (uncatchable OOM). |
| `codestream/sod.go` | `processSOD`, before the tile-part payload buffer | A tile-part cannot exceed the input carrying it, so reject `remaining` greater than the reader's unread length when it is knowable (`d.r` is a `*bytes.Reader`, which exposes `Len`); a reader without a known length is read incrementally via `io.ReadAll(io.LimitReader(...))`. `maxTilePartBytes` stays the absolute ceiling. | An SOT with `Psot=0x0FFFFFFF` (just under `maxTilePartBytes`) drove `make([]byte, ~256 MiB)` up front from a sub-100-byte payload the read then fails to fill. Fatal (uncatchable OOM). |
| `box/jp2.go` | `parseCmap` | Refuse a `cmap` describing more than `maxCMapChannels` output channels (new const `32`); the container then falls back to its non-palette handling rather than mis-rendering a truncated map. | A `cmap` listing thousands of output channels — each of which becomes a full w×h `int32` plane in `applyPalette` — allocates that many image-sized planes (~160 GB for 10240 channels over a 2048×2048 image). Fatal (uncatchable OOM). |
| `codestream/palette.go` | `applyPalette`, before the per-channel plane loop | Reject `len(d.cmap) > maxOutputChannels` (new const `32`, mirroring `box.maxCMapChannels`). A defensive backstop to the `parseCmap` cap that holds regardless of how the cmap reached the decoder, and also protects upstream's own `image.Decode` path. | Same over-long-`cmap` plane blow-up as above, bounded at the allocation site itself. |
| `codestream/siz.go` + `codestream/decoder.go` | `processSIZ` (new `sizSeen` flag on `Decoder`, reset per `Decode`) | Reject a second SIZ marker segment. ISO 15444-1 A.5.1 permits exactly one SIZ, in the main header; the parser reuses `sectionMainHeader` between tile-parts (`processSOD` returns there), so the existing section check alone admitted a mid-stream SIZ. OpenJPEG's marker state machine rejects the same stream, so nothing legitimate is lost. | A leading SIZ declares an innocent image that passes the caller's pre-decode budget check, a complete tile-part hands the parser back to the main-header section, and a second SIZ swaps in huge dimensions the budget never saw; `finalizeImage` then sizes component and output planes from the swapped geometry. Found by the 2026-08-03 `FuzzJPX` soak (447-byte payload → ~77 GB RSS, ~25 s CPU per decode; workers died minimizing it). Fatal (uncatchable OOM) at larger declared sizes. |

A fourth, lower-severity site was investigated and left unchanged: `engine/tagtree.go` `ProgressiveDecodeValue` spins
its full 65536-iteration value loop per code-block once a shared zero-bit-plane tag-tree parent is driven to the
`2^maxBits` cap (a later leaf then reads no input yet still iterates to the cap). The mechanism was confirmed at the
`TagTree` API level, but a payload-level reproduction was not completed within the available effort, and no local guard
that terminates the loop is provably neutral to the golden decodes (a legitimate later leaf whose shared ancestors were
saturated below the cap by an earlier leaf must run no-op iterations before reading its own bit, so a naive
"terminate when no input is consumed" bound would change decoded values). The hot path was therefore left untouched.

### SIMD dispatch variables

pdfview ships a `GOEXPERIMENT=simd` build in which a handful of the repository's hottest per-sample loops run as
vector kernels. The kernels are pdfview-authored and live in the files listed in the next table; the change to
upstream code is that five loops moved into named functions in the file they already lived in, and their call sites
now call a package-level function variable instead of running the loop inline. Nothing upstream was deleted and no
loop left its file.

Each variable defaults to the scalar function, so the default build calls exactly the code it always did through one
indirect call per plane or per sweep — never per sample. `simd_on.go`'s `init`, compiled only under the experiment,
repoints a variable at its kernel when the architecture's `simd_prefs_*.go` constant selects it and the `simd` package
is not emulating vectors in software. A kernel that loses its benchmark is therefore switched off by a constant rather
than deleted, and `simd_wiring_test.go` (default build) and `TestSIMDWiring` (experiment build) assert that each
variable points where its build says it should.

Every kernel is bit-identical to the loop it replaces — proven directly by `simd_equiv_test.go` in each package, which
runs the same input through both functions and compares element for element (`math.Float64bits` for the float64
sweeps, not an epsilon) across a length sweep that crosses each width gate in both directions, and indirectly by the
whole decode suite, which runs a second time under the experiment.

| File | Site | Dispatch |
| --- | --- | --- |
| `codestream/reconstruction.go` | `ApplyRCT`'s per-sample loop, now `applyRCTScalar` | `applyRCTFn` over the three planes, bounded to the shortest as before. |
| `codestream/decoder.go` | `finalizeImage`'s componentsOnly range clamp, now `clampPlaneScalar` | `clampPlaneFn` per component plane. The kernel hands unordered bounds back to the scalar loop: a component precision above 32 overflows the `int32` bound computation and yields `lo > hi`, which Max-then-Min and the scalar chain disagree about. |
| `codestream/palette.go` | `applyPalette`'s type 0 (direct copy) channel, now `addClampPlaneScalar` | `addClampPlaneFn`. The kernel hands back unordered bounds (same overflow case) and a source shorter than the destination, so the scalar loop's out-of-range panic still happens instead of a partial vector load quietly substituting zeros. |
| `wavelet/wavelet.go` | `inverse53VerticalCas0`'s two sweeps, now `sub53SweepScalar` and `add53SweepScalar` | `sub53SweepFn` / `add53SweepFn`, one call per sweep. Dispatch is per sweep, not per row: a per-row indirect call cost the scalar path about 16% at row widths below the gate, because a call in the row-loop body is enough to change how that loop is compiled. |
| `wavelet/wavelet97.go` | `inverse97VerticalCas0`'s pair of scaling sweeps, now `scale97SweepScalar` | `scale97SweepFn`, one call for both sweeps, for the same reason. The four lifting sweeps below it are deliberately left scalar; `liftRow97SIMD`'s doc comment in `wavelet/simd_on.go` records why (the compiler contracts `dst -= c*(a+b)` into a fused multiply-add on arm64, and a vector kernel that rounds twice would not match it). |

### pdfview-authored files inside this tree

These carry Rich's MPL-2.0 header, not the upstream provenance line, and are excluded from the diff audit above. They
add to the vendored packages from the outside; no vendored file was edited to accommodate them.

| File | What it is |
| --- | --- |
| `test/e2e/register_pdfview_test.go` | The upstream end-to-end tests reach the decoders through `image.Decode`, which the registry removal above breaks. This restores the two registrations inside the test binary, where the side effect is scoped to the test process. It exists so no upstream test had to be edited or dropped. |
| `jp2/pdfview.go` | `DecodeComponents` and `DecodeInfo`, the two container-level entry points the PDF image pipeline needs and upstream never exposed: raw per-component planes from a JP2, and a header-only report of the container metadata alongside the codestream's own component geometry. |
| `jp2/pdfview_test.go` | Pins both against the vendored vectors and their ground-truth planes, including a corruption sweep over every `.jp2` vector. |
| `codestream/simd_dispatch.go`, `wavelet/simd_dispatch.go` | Untagged: the dispatch variables, defaulted to the scalar functions, and the width gates each kernel falls back below. |
| `codestream/simd_on.go`, `wavelet/simd_on.go` | The vector kernels and the `init` that repoints the variables at them, built only under `GOEXPERIMENT=simd`. |
| `codestream/simd_prefs_{arm64,amd64,other}.go` and the same three in `wavelet/` | Per-architecture constants saying which kernels that architecture prefers, settled from `simd-bench.sh` results. Unmeasured architectures get `_other`, where every constant is false. |
| `codestream/simd_equiv_test.go`, `wavelet/simd_equiv_test.go` | Scalar-versus-vector equivalence, swept over every tail alignment and both sides of every gate, plus the experiment build's wiring assertions. Tagged for the experiment build. |
| `codestream/simd_wiring_test.go`, `wavelet/simd_wiring_test.go` | The default build's mirror: every dispatch variable must still be its scalar function. Tagged `!goexperiment.simd`. |
| `codestream/simd_bench_test.go`, `wavelet/simd_bench_test.go` | Untagged benchmarks through the dispatch variables, so one benchmark body measures both builds; `simd-bench.sh` benchstats the pair. |

`jp2/pdfview.go` reads the SIZ marker segment itself rather than calling the codestream decoder's header pass. That
pass runs `initTiles` immediately after SIZ even when asked for the configuration only, allocating one `tileState` per
declared tile — bounded at 2^20 tiles, so a few dozen header bytes can cost hundreds of megabytes before any pixel is
requested. Reading SIZ directly keeps `DecodeInfo` bounded by its input. `jp2/pdfview_test.go` cross-checks the two
readings against each other on every vendored container so they cannot drift.

## Attribution files in this directory

| File | What it is |
| --- | --- |
| `LICENSE` | Upstream's MIT license text, verbatim. |

## Test data

`test/testdata` (99 files) and `j2k/testdata/fuzz/FuzzDecode` (5 files) are copied byte-for-byte from upstream. No
ISO/IEC 15444-4 conformance bitstream is vendored: upstream deliberately keeps that data out of the repository because
of its ITU/ISO copyright, and both `TestConformance` and `TestNonRegressionNoPanic` read it from an external
`openjpeg-data` checkout named by `OPJ_DATA_ROOT`, skipping when it is absent. The vendored vectors are upstream's own,
produced by `opj_compress`, by the OpenJPEG library encoder API through upstream's `tools/gen-testdata`, by a patched
OpenJPEG for the COC/QCC cases, by hand for the palette container, and by Grok's `grk_compress` for the TLM/PLT case;
the `.pgm`/`.ppm`/`.pgx`/`.raw` companions are the matching ground-truth planes, and the `*_opjref.*` files are
OpenJPEG's own decode output.
