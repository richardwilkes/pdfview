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
vendored copy carries all three notice sets; see the "M1 evidence" section of `plan.md` at the repository root for the
adoption decision this finding fed.

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
`embedded.go` and `pdfview_test.go` (with everything under `testdata/`) are pdfview-authored MPL-2.0 files, not
upstream code, and carry Rich's header instead.

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

Unmodified: `jbig2_basics.go`, `jbig2_grd_proc_impl.go`, `jbig2_huffman_decoder.go`, `jbig2_image.go`, `jbig2_mmr.go`,
`jbig2_mmr_decoder.go`, `jbig2_pattern_dict.go`, `jbig2_segment.go`, `jbig2_symbol_dict.go`.

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
