# Provenance — `internal/jbig2`

## What this is

A vendored, pinned copy of a third-party JBIG2 decoder. It is not pdfview code and does not carry pdfview's MPL-2.0
header; it keeps its upstream Apache-2.0 headers, and `.golangci.yml` excludes this directory from the repository's
header, style, and formatting rules. Correctness and security linters still run over it.

| | |
| --- | --- |
| Upstream | https://github.com/xiaoqidun/jbig2 |
| Commit | `cddd57533c9769765b47f5e4bd9a437c9a789ffe` |
| Commit date | 2026-07-28 |
| Vendored on | 2026-08-02 |
| License | Apache-2.0 (upstream), plus the two derivations below |

## Derivation

Examination of this code before adoption established that it is substantially a translation of PDFium's JBIG2 decoder,
`core/fxcodec/jbig2/` (BSD-3-Clause; Copyright 2014 The PDFium Authors; original code copyright 2014 Foxit Software
Inc.) — the file, type, method, field, and constant names map one-for-one, including PDFium-only artifacts that carry
no meaning outside PDFium, and PDFium's magic-number tables are reproduced verbatim. Separately, `jbig2_mmr_decoder.go`
is a translation of `MMRDecompressor` and `MMRConstants` from the Apache PDFBox JBIG2 ImageIO plugin (Apache-2.0;
Copyright 2017 The Apache Software Foundation, based on code copyright 1995-2017 levigo holding GmbH), reproducing that
project's code tables in its own non-canonical ordering. The upstream repository discloses neither derivation, so this
vendored copy carries all three notice sets; the adoption decision this finding fed — vendor and harden in-tree rather
than depend on the module — is recorded in commit 2f90677.

## Attribution files in this directory

| File | What it is |
| --- | --- |
| `LICENSE` | Upstream's Apache-2.0 license text, verbatim. Also the license covering the PDFBox-derived MMR decoder. |
| `LICENSE-pdfium` | PDFium's `LICENSE` file, verbatim and whole: the BSD-3-Clause grant followed by the full Apache-2.0 text, exactly as PDFium ships it in one file. |
| `NOTICE` | Upstream's NOTICE verbatim, followed by a clearly separated section added by this repository carrying the PDFium BSD-3-Clause notice (including the Foxit line) and the Apache PDFBox / levigo NOTICE text. |

## Local modification policy

The Apache-2.0 grant on this tree requires prominent notice of changes (§4(b)). Any file modified in place therefore
gets a change notice comment at the top of the file, below the upstream copyright header and above the `package`
clause, naming what changed and why. PDFium-derived files additionally get a provenance comment naming PDFium and
Foxit and pointing at `LICENSE-pdfium`, satisfying the BSD-3-Clause requirement that a source redistribution retain the
copyright notice, conditions, and disclaimer. Upstream copyright headers are never removed or replaced: the upstream
author holds copyright in the translation and the packaging even where the underlying expression is someone else's.

Modifications are made in this tree rather than at the glue when the fix cannot be expressed from outside — the
decode-work bounds that depend on arithmetic-decoded values are the motivating case. Everything expressible from
outside stays in `internal/imaging/jbig2.go`.

## Modifications

Every file below carries the change notice this policy requires, stating the same thing at the point of the change.
`embedded.go`, `pdfview_test.go`, and the `simd_*` files (with everything under `testdata/`) are pdfview-authored
MPL-2.0 files, not upstream code, and carry Rich's header instead.

| File | Change | Why |
| --- | --- | --- |
| `embedded.go` (new) | `Limits`, `NewEmbeddedDecoder`, `Decoder.DecodePage`, and the cumulative-area budget the package charges against. | The ISO 32000-2 7.4.7 profile directly, so no synthetic file header is needed, and the one place a caller can bound decode work. |
| `jbig2.go` | Pruned the file-signature probe, the CWS/SWF unwrapping (and the `zlib` import), the `io.Reader` constructors, the `image.RegisterFormat` registration with its `Decode`/`DecodeConfig`, `DecodeAll`, `GetDocument`, and the 8-bit gray expansion. | Everything outside the embedded profile is attack surface pdfview never uses; a caller reading the packed page needs no gray expansion. |
| `jbig2_document.go` | Charges the page bitmap, striped-page growth, and the refinement region's page sub-image against the budget; hands the budget to every proc; restored PDFium's checked arithmetic on the next-segment offset; pruned the grouped organization, the random-access flag, and the endianness flag; dropped an ineffectual index advance. | Bounds allocation before it happens. The offset was summed in int64 and truncated to uint32, so a data length near 2^32 wrapped it backwards and the segment loop re-parsed one header forever — a hang on a few bytes, found by fuzzing. The pruned organizations cannot occur in an embedded stream. |
| `jbig2_grd_proc.go` | Restored PDFium's `&& !USESKIP` on the three nominal-template predicates; charges the region bitmap. | PDFium requires the conjunct because the fast paths cannot honour a skip plane. The charge is the symbol-area DoS fix: every new symbol bitmap is decoded here, and charging before the decode loop bounds total work. |
| `jbig2_grrd_proc.go` | Charges the refinement bitmap; deleted the unreachable `decodeTemplate1Unopt`. | PDFium's gate on that body exists for its raw-pointer fast paths; these bodies read through bounds-checked accessors and are correct at any reference offset or width, which `pdfview_test.go` pins. |
| `jbig2_sdd_proc.go` | Restored PDFium's checked arithmetic in both export-run loops and the `j >= SDNUMEXSYMS` export guard; bounded the height-class loop by the declared new-symbol count; charges the Huffman collective bitmap, each symbol split from it, and each duplicated input symbol; hands the budget to the procs it drives. | The unchecked `EXINDEX + EXRUNLENGTH` wraps on hostile input and the fill loop then indexes out of range — a panic PDFium's `FX_SAFE_UINT32` prevents. Each height class yields at least one symbol, so the loop bound rejects nothing legitimate and stops an empty class from repeating forever against an arithmetic decoder that never reports exhaustion. |
| `jbig2_trd_proc.go` | Charges the text region bitmap; hands the budget to the refinement proc; restored PDFium's hard failure on a nil symbol in the arithmetic path. | Region dimensions are attacker-controlled. Skipping a nil symbol instead of failing left the instance counter unable to advance, and the arithmetic decoder feeds 0xFF forever past the end, so the instance loop spun forever — a hang found by fuzzing. |
| `jbig2_htrd_proc.go` | Charges the skip plane, each MMR gray-code plane, the region bitmap, and the assembly grid; hands the budget to the generic-region proc. | Grid dimensions and plane counts are attacker-controlled, and a one-entry pattern dictionary needs no gray-code plane at all, which would otherwise leave the grid loop running against nothing charged. |
| `jbig2_pdd_proc.go` | Charges the pattern cells; hands the budget to the generic-region proc. | Cell count comes from `GRAYMAX`. |
| `jbig2_bit_stream.go` | Pruned the little-endian mode. | T.88 fields are big-endian; the flag served only the pruned probe and container handling. |
| `jbig2_arith_decoder.go` | Dropped the unused `arithQeStateCount`. | Dead constant. |
| `jbig2_image.go` | Composition routed through the dispatch variables in `simd_dispatch.go`, whose defaults are the scalar code that was already there: the byte-aligned run calls `composeBytesFn`, which *is* `composeBytes`, and the per-byte loop offers the interior of a long enough unaligned run to `composeShiftedRunFn`, which by default consumes nothing. `Fill` and `Expand`'s 0xFF loops call `fillBytes`. | A `GOEXPERIMENT=simd` build on vector hardware repoints both variables at the kernels in `simd_on.go`; every other build is the scalar code it always was, and the scalar bodies still run below the kernels' length gates and for the partial bytes at each end of an unaligned run. The length test in front of the second call is there because a dispatch variable costs a call whatever it holds, and symbol placement composes rows a byte or two wide. Composition is the decoder's hot loop. `fillBytes` fills with a doubling copy instead of a store per byte, which Go's memclr rewrite does not cover for a non-zero value. |
| `jbig2_mmr_decoder.go` | Bounded the `uncompress2D` changing-element index (2026-08-02). `fillBitmap`'s whole-byte black-run loop hands its run to `fillBytes` (2026-08-21). | `currOffsets` is sized width+5, but a mode code that does not advance `bitPos` grows `currIdx` without ending the row, walking the write past the buffer end — a panic on hostile input; this decoder is jbig2dec/pdf.js lineage with no PDFium counterpart, so that is hardening rather than restoration. The fill is the same bits by a memmove instead of a store per byte. |

Unmodified: `jbig2_basics.go`, `jbig2_grd_proc_impl.go`, `jbig2_huffman_decoder.go`, `jbig2_mmr.go`,
`jbig2_pattern_dict.go`, `jbig2_segment.go`, `jbig2_symbol_dict.go`.

The `jbig2_mmr_decoder.go` row's first half records a change made in commit 2443171 whose notice went into the file
but never reached this table; adding the fill brought it to light and it is stated here now.

## SIMD kernels

`simd_dispatch.go` (untagged), `simd_on.go` (`goexperiment.simd`), `simd_prefs_{arm64,amd64,other}.go`, and the
`simd_*_test.go` files are pdfview-authored MPL-2.0 code, self-contained, and import nothing from this repository —
the package keeps its import isolation, so the byte-fill helper and the PRNG are duplicated here rather than shared
with `internal/vecmath`. They add vector forms of the two composition loops, dispatched through function variables
whose defaults are the upstream scalar functions, so a build without the experiment, an architecture that has not
been benchmarked, and a machine with emulated lanes all run the untouched scalar code. `simd_equiv_test.go` proves
every kernel against the scalar function it replaces, byte for byte, at three length-gate settings.

## Vendoring record

This vendoring copied all 19 upstream `.go` files byte-for-byte, with no formatting pass, no import rewriting, and no
pruning; `internal/imaging/jbig2.go` was repointed from the module path to this directory, with no behavior change. The
hardening and pruning above came after that copy, as a separate reviewed step, so every change is a diff against the
pinned upstream commit rather than something folded into the import.
Upstream's `go.mod`, `go.sum`, `README.md`, and `.gitignore` were not copied. The package still imports only the
standard library and `golang.org/x/image/ccitt`, which remains a module dependency. The `*_test.go` files and
`testdata/` in this directory are pdfview-authored (MPL-2.0), not upstream code.

Decisions taken during vendoring, none of which had a stated answer:

- `LICENSE-pdfium` reproduces PDFium's `LICENSE` file whole rather than excerpting its BSD-3-Clause half, because
  PDFium distributes both texts in that single file and excerpting would misrepresent it.
- The NOTICE's added section quotes the PDFium BSD-3-Clause conditions and disclaimer in full rather than referring to
  `LICENSE-pdfium`, so the notice requirement is met by the NOTICE alone.
- The import in `internal/imaging/jbig2.go` is unaliased: the vendored package is named `jbig2` and its path ends in
  `jbig2`, so call sites are unchanged without an alias and an explicit one would be redundant.
