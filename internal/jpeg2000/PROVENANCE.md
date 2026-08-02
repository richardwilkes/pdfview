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

Only four mechanical changes were made. A diff of every vendored `.go` file against the pinned upstream commit, with
the provenance line stripped and the import rewrites reversed, is empty except for the two files in the third row.

| Change | Where | Why |
| --- | --- | --- |
| Import rewrites | Every file that imports a sibling package | `github.com/mububoki/jpeg2000/...` → `github.com/richardwilkes/pdfview/internal/jpeg2000/...`, with upstream's `internal/` level flattened away. |
| Provenance line | Every vendored `.go` file, first line, above any package doc comment | Names the upstream, version, and license at the point of use, and keeps pdfview's MPL header off someone else's code. |
| `image.RegisterFormat` removal | `j2k/reader.go`, `jp2/reader.go` | A library must not mutate the process-global image registry on import; `internal/jbig2` set this precedent. The `init` functions are gone, along with the package-doc sentences that advertised them. Nothing else in those comments was reworded. |
| Encoder prune | See above | Dead, unreachable attack surface. |

The upstream package doc comments for `j2k` still describe `Encode`, `EncodeWithOptions`, `EncodeComponents`, and
`EncodeOptions`, which the prune removed. They were left alone: rewriting them is a judgement call, not a mechanical
change, and keeping the diff against upstream to the four items above is worth more than the stale sentences cost.

`test/e2e/register_pdfview_test.go` is pdfview-authored (MPL-2.0), not upstream code. The upstream end-to-end tests
reach the decoders through `image.Decode`, which the registry removal above breaks; that file restores the two
registrations inside the test binary, where the side effect is scoped to the test process. It exists so no upstream
test had to be edited or dropped.

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
