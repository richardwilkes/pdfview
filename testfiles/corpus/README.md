# Test corpus

These PDFs are the inputs for the golden-based parity tests. For each `<name>.pdf` here, `../goldens/<name>/`
holds the recorded MuPDF behavior (`truth.json` plus rendered PNGs), produced by `oracle/regen.sh` — see plan.md.
The per-file dump parameters (search needles, passwords) are registered in `oracle/regen.sh`; when adding a corpus
file, add a dump line there and document the file's provenance below. Corpus files and goldens are committed;
nothing here is fetched or regenerated in CI.

## Pre-existing fixtures

- `glaive.pdf` — the repository's long-standing test fixture, formerly at `../GLAIVE_Mini_v2_3_for_GURPS_4e.pdf`
  (moved here 2026-07-11; `pdf_test.go` reads this path). "GLAIVE Mini v2.3 for GURPS 4e" by T Bone (Games Diner,
  <https://www.gamesdiner.com/glaive_mini>), distributed as a free download; it has shipped as this repository's
  test fixture since before the pure-Go port. The exact-value expectations in `pdf_test.go` (page count, TOC
  spots, GURPS search hits, link bounds, image dimensions) come from this file.
- `internal-links.pdf` — byte-for-byte extraction of the `internalLinkPDF` constant in `pdf_test.go` (which stays,
  since that test must remain byte-identical through the port): a minimal two-page document with an explicit /XYZ
  destination link and a named /Fit destination link, no xref table, and `startxref 0`, so opening it requires a
  repair parse.

## Handcrafted minimal PDFs

Hand-assembled uncompressed PDF 1.7 files with classic xref tables whose byte offsets were computed mechanically
at generation time (a throwaway dev-time script; only these outputs are committed). All text uses standard-14
fonts, so nothing is embedded, and all content streams are stored uncompressed for readability in any text editor.

- `vectors.pdf` — one 200×200 page of pure vector art, no text: nonzero-winding and even-odd fills, a dashed
  stroked Bézier with round caps/joins, a rectangular clip, a fill+stroke (`B`) path, and DeviceRGB/DeviceGray/
  DeviceCMYK colors.
- `text-std14.pdf` — one 400×300 page exercising Helvetica, Times-Roman, Courier, and Helvetica-Bold: mixed-case
  duplicate lines ("Hello World"/"hello world") for case-folded search, a phrase wrapped across two lines
  ("...brown" / "fox...") for line-spanning matches, a word-spaced line (`Tw`), and a `TJ` array with a large
  kerning gap ("Kerned"/-500/"Text") whose synthesized inter-word space the goldens pin down.
- `rotate90.pdf` — one 300×200 page with `/Rotate 90` containing text and vector marks; rendered output is
  200×300 and the recorded search quads for "Rotated" are non-axis-aligned, pinning the all-four-corners
  quad-to-rectangle behavior.
- `damaged-startxref-zero.pdf` — trailer present, no xref table, `startxref 0`; MuPDF repairs by scanning.
- `damaged-bad-offsets.pdf` — two pages; structurally complete, but every xref offset is shifted by +7 bytes, so
  the table is present yet wrong and MuPDF repairs.
- `damaged-no-trailer.pdf` — objects and `%%EOF` only: no xref, no trailer, no startxref; MuPDF reconstructs
  everything, finding the catalog by scanning.

## Encrypted variants

Generated from `text-std14.pdf` with qpdf 12.3.2 (`brew install qpdf`); only the outputs are committed. The user
password is `user` and the owner password is `owner`, except `encrypted-r6-empty-user.pdf`, which has an empty
user password (it opens without authentication) and owner password `owner`.

| File | qpdf arguments (after `qpdf`) | Handler |
| --- | --- | --- |
| `encrypted-r2-rc4.pdf` | `--allow-weak-crypto --encrypt user owner 40 --` | R2, 40-bit RC4 |
| `encrypted-r3-rc4.pdf` | `--allow-weak-crypto --encrypt user owner 128 --` | R3, 128-bit RC4 |
| `encrypted-r4-rc4.pdf` | `--allow-weak-crypto --encrypt user owner 128 --force-V4 --` | R4, RC4 crypt filter |
| `encrypted-r4-aes.pdf` | `--allow-weak-crypto --encrypt user owner 128 --use-aes=y --` | R4, AESV2 |
| `encrypted-r6-aes.pdf` | `--encrypt user owner 256 --` | R6, AESV3 |
| `encrypted-r6-empty-user.pdf` | `--encrypt "" owner 256 --` | R6, AESV3, empty user password |

## Public-domain real-world PDFs

Both are works of the United States federal government and therefore in the public domain in the United States
under 17 U.S.C. § 105. Retrieved 2026-07-11. They share a producer lineage (modern Adobe toolchains); more diverse
real-world files can be added in later milestones as engine coverage grows.

- `irs-f1040.pdf` — IRS Form 1040 (2 pages, AcroForm), from <https://www.irs.gov/pub/irs-pdf/f1040.pdf>.
- `irs-fw9.pdf` — IRS Form W-9 (6 pages, AcroForm), from <https://www.irs.gov/pub/irs-pdf/fw9.pdf>.
