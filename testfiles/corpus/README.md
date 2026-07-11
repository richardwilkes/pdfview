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
- `std14-styles.pdf` — one 612×792 page (generated 2026-07-11 by a dev-time script, same convention as the
  rest) with one 50-pt line per standard-14 font, none embedded and none carrying /Widths, each line tagged
  with a unique search token (the Symbol line spells αβγδ through its built-in encoding, the ZapfDingbats line
  ✁✂✃). Its recorded search quads pin, per style, the substitute-font metrics MuPDF uses for non-embedded
  standard-14 text: the AFM-compatible advances and — via the quad tops/bottoms — each substitute's
  ascender/descender (see internal/font's nimbusMetrics).
- `subst-metrics.pdf` — one 612×792 page (generated 2026-07-11, same convention): twelve 50-pt lines of
  non-embedded fonts that DO carry font descriptors, covering the irs-fw9 HelveticaLTStd-Bold case, unknown
  sans/serif/mono/bold/italic names across descriptor flags, comma-style names ("NoSuchSans,BoldItalic"),
  descriptors with no /Ascent//Descent, zeroed ones, and one-sided ones, plus standard-14 names with
  descriptors. Its quads pin the substituted-font quad-metric precedence: descriptor /Ascent//Descent when
  nonzero, per-slot defaults 800/-200 otherwise — even for standard-14 BaseFonts (see internal/font's
  substituteMetrics).
- `hit-quad-split.pdf` — one 612×792 page (generated 2026-07-11, same convention) pinning MuPDF's search-hit
  quad grouping: a match whose interior space sits in a slightly-taller bold font keeps ONE quad with the
  FIRST character's vertical extent (the irs-fw9 case, distilled); a trailing space before a line wrap is
  included in the first segment's quad; and a match crossing a hugely-taller character (a 40-pt space between
  20-pt words) splits into three quads, the divergent character carrying its own extent.
- `damaged-startxref-zero.pdf` — trailer present, no xref table, `startxref 0`; MuPDF repairs by scanning.
- `damaged-bad-offsets.pdf` — two pages; structurally complete, but every xref offset is shifted by +7 bytes, so
  the table is present yet wrong and MuPDF repairs.
- `damaged-no-trailer.pdf` — objects and `%%EOF` only: no xref, no trailer, no startxref; MuPDF reconstructs
  everything, finding the catalog by scanning.

## M6 font probes (generated 2026-07-11)

Hand-assembled like the rest (a dev-time script; only outputs committed), but with the embedded font programs
themselves built from scratch — a Type 1 program (clear text, eexec- and charstring-encryption applied by the
script), a TrueType program deliberately carrying no `cmap` table, and a CID-keyed CFF with its own
charset/FDSelect — so every byte is license-free and the exercised operator set is exactly known. Search
needles pin layout (quads) and, for the Type0 files, the /ToUnicode extraction chain.

- `text-type1.pdf` — an embedded Type 1 font (raw FontFile) used by two font dictionaries: F1 with
  /Widths+/Differences, F2 without /Widths (hsbw advances drive layout). The glyphs cover hsbw/sbw, all the
  lineto/moveto forms, rrcurveto/vhcurveto/hvcurveto, closepath, callsubr, div, hstem/vstem/dotsection, the
  full othersubr flex protocol, a nonzero-winding counter (hole), and a seac composite (é = e + acute,
  reachable through the built-in encoding and through /Differences).
- `text-type0-cid2.pdf` — Type0/CIDFontType2 over the cmap-less TrueType: font A uses Identity-H with a
  /CIDToGIDMap stream (CIDs 100–103) and /W overrides plus a /ToUnicode mapping to CJK codepoints (the
  extraction text is 你佡世界 — the bfrange increment is deliberate); font B uses /CIDToGIDMap /Identity
  (ToUnicode WXYZ); font C shows the same descendant through Identity-V (vertical advances/origins, no
  needle — vertical quads are pinned at M7). The TrueType glyphs include quadratic on/off-curve contours, a
  fully off-curve contour, and a composite glyph with scaled components.
- `text-type0-cid0.pdf` — Type0/CIDFontType0 over a CID-keyed CFF (ROS, charset format 0, FDSelect format 3,
  one FD) addressed through an EMBEDDED CMap stream with a one-byte codespace (codes A–D map to CIDs
  10/11/30/20; CID 11 is deliberately absent from the charset — MuPDF/FreeType render nothing for it and the
  empty .notdef matches). /W mixes both entry forms; /ToUnicode maps to PQRS.
- `text-type3.pdf` — two Type 3 fonts sharing three CharProcs: a d1 (shape) glyph whose own color operator
  must be ignored, a d0 (colored) glyph painting its own fill and stroke, and a d1 glyph with Bézier subpaths;
  the second font differs only in a wider FontMatrix, pinning matrix-scaled advances. No needles (Type 3 quad
  metrics are pinned at M7).
- `text-trmodes.pdf` — standard-14 text across every text render mode: Tr 0/1/2 fill/stroke/both, Tr 3
  invisible (its needle pins extraction of invisible text), Tr 7 clip-only and Tr 4/5 fill+clip / stroke+clip
  with colored bands painted through the live clip, plus rise (Ts) and horizontal scaling (Tz) lines.

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

## Image corpus (M5)

Ten single-page files generated 2026-07-11 by a throwaway dev-time Go program (same convention as the other
handcrafted files: only the outputs are committed; classic xref tables with mechanically computed offsets;
uncompressed content streams). Raw sample payloads are ASCIIHexDecode-encoded for readability; the codec payloads
are binary with exact /Length values. Payload provenance:

- JPEG payloads: grayscale and RGB baseline JPEGs encoded with Go's `image/jpeg` at quality 100. The CMYK JPEG
  was produced by macOS `sips --matchTo '/System/Library/ColorSync/Profiles/Generic CMYK Profile.icc' -s format
  jpeg` from an RGB PNG (Adobe APP14, transform 0, inverted CMYK storage), then its APP1/APP2/APP13 metadata
  segments were stripped so no embedded ICC profile overrides the PDF-declared /DeviceCMYK in any reader.
- CCITT payload: a handwritten uncompressed bilevel TIFF (a 2-pixel border box plus 4×4 diagonal stripes,
  PhotometricInterpretation MinIsWhite) re-encoded with libtiff's `tiffcp -c g4` into a single strip, whose raw
  MMR bytes are exactly a CCITTFaxDecode K<0 payload.
- The JBIG2 payload is a deliberately truncated segment header and the JPX payload lacks the JP2 signature box,
  so every decoder rejects them cleanly; MuPDF warns and the JPX page renders with the image absent. MuPDF's
  jbig2 path instead pads the failed decode into a black square, which the pure-Go stub intentionally does not
  reproduce (blank per plan.md) — see TestImageCorpusPixels for how that file is pinned.

| File | Contents |
| --- | --- |
| `images-dct.pdf` | Gray, RGB, and CMYK (Adobe APP14) DCTDecode XObjects |
| `images-raw.pdf` | Uncompressed samples: gray at 1/2/4/8/16 bpc, RGB and CMYK at 8 bpc, /Decode [1 0], and a 3×3 4-bpc RGB image whose rows need bit padding |
| `images-indexed.pdf` | Indexed palettes over DeviceRGB/DeviceGray at 1/2/4/8 bpc, including out-of-range indices that clamp to hival |
| `images-imagemask.pdf` | ImageMask stencils over a colored background: default /Decode, /Decode [1 0], and an inline (BI/ID/EI) mask |
| `images-inline.pdf` | Inline images: raw binary with /L, ASCIIHexDecode, a named colorspace from page resources, FlateDecode with /L, and /D [1 0] |
| `images-smask.pdf` | /SMask alpha (mask dimensions differ from the base image), a stencil /Mask stream, and a color-key /Mask array |
| `images-ccitt.pdf` | The G4 payload twice: default decoding and /BlackIs1 true |
| `images-jbig2.pdf` | JBIG2Decode stub coverage (truncated payload; see above) |
| `images-jpx.pdf` | JPXDecode stub coverage (invalid payload; see above) |
| `images-interpolate.pdf` | The same checkerboard drawn magnified with and without /Interpolate, pinning the sampling-filter mapping |

## Shading and pattern probes (M8, generated 2026-07-11)

Five single-page files generated by a throwaway dev-time Go program (same convention as the image corpus:
computed classic xref, only outputs committed). Content streams are uncompressed; the mesh payloads and the
sampled function use ASCIIHexDecode so every committed byte stays printable. Colors are DeviceRGB/DeviceGray so
the M4 behavioral color tables keep interiors exact.

| File | Contents |
| --- | --- |
| `shading-axial.pdf` | Type 2 axial shadings via `sh` under clips: /Extend [true true], [false false] with a non-default /Domain and an N=2 exponential function, and MIXED extend [true false] over a type 3 stitching function; a shading-pattern (`scn`) triangle fill with a pattern /Matrix; a shading-pattern stroked Bézier (`SCN`) over a DeviceGray axial |
| `shading-radial.pdf` | Type 3 radials: an offset-center "sphere" (extend both), a no-extend annulus colored by a type 0 sampled function (painted as a pattern fill), a shrinking-radius mixed extend that runs the radius to 0 (the cone-tip cutoff), and an extend-outward/hole-inward combination |
| `shading-function.pdf` | Type 1 function-based shading over a PostScript-calculator function: `sh` under a clip, a pattern fill with a /BBox and a non-uniform pattern /Matrix, and a scaled translucent copy under `gs` ca 0.5 |
| `shading-mesh.pdf` | All four mesh types via `sh`, one per quadrant: type 4 free-form triangles chained through edge flags 0/1/2, a type 5 3x3 lattice, a type 6 Coons patch with curved edges plus a flag-1 continuation sharing its top edge, and a type 7 tensor patch with displaced interior points |
| `pattern-tiling.pdf` | Tiling patterns: a colored cell with gaps (XStep > BBox) under a pattern /Matrix, an uncolored (PaintType 2) cell painted twice with different `scn` colors, an overlapping cell (BBox > steps), a pattern-painted stroke, and a pattern fill inside a rotated CTM (pinning that pattern space anchors to the page, not the current CTM) |

## Transparency probes (M8, generated 2026-07-11)

Four single-page files generated by a throwaway dev-time Go program (same convention as the shading corpus:
computed classic xref, uncompressed content streams, only outputs committed; the sampled transfer function uses
ASCIIHexDecode). Colors are DeviceRGB/DeviceGray so the M4 behavioral color tables keep interiors exact.

| File | Contents |
| --- | --- |
| `transparency-blend.pdf` | All 16 PDF blend modes as a 4x4 grid of squares straddling saturated/neutral backdrop bands, with the constant fill alpha alternating 1.0 / 0.7 by column so every mode composites both opaque and translucent sources |
| `transparency-group.pdf` | Separate stroke (/CA) vs fill (/ca) constant alphas on one `B` paint; a transparency-group form painted at ca 0.5 whose interior alphas reset (overlap composites once); a NON-isolated group whose interior Multiply sees the page backdrop, holding a nested isolated Screen group painted at ca 0.6; and knockout (/K true) vs non-knockout forms with identical interiors (overlap replaced vs composited) |
| `transparency-smask-lum.pdf` | Luminosity soft masks: an axial-gradient mask over a solid fill; a small-BBox mask with /BC 0.6 (the outside-BBox area keeps the backdrop luminosity); a sampled (type 0) inverting /TR, painted once wholly outside the gs-time-anchored mask (must vanish) and once with the gs issued under the cm (must show); and masked standard-14 text |
| `transparency-smask-alpha.pdf` | Alpha-subtype soft masks: graded constant-alpha mask content in unrelated hues (only the alpha may matter), /SMask /None resetting the mask mid-path, and an alpha mask combined with /BM /Multiply and ca 0.8 over a backdrop band |

## Public-domain real-world PDFs

Both are works of the United States federal government and therefore in the public domain in the United States
under 17 U.S.C. § 105. Retrieved 2026-07-11. They share a producer lineage (modern Adobe toolchains); more diverse
real-world files can be added in later milestones as engine coverage grows.

- `irs-f1040.pdf` — IRS Form 1040 (2 pages, AcroForm), from <https://www.irs.gov/pub/irs-pdf/f1040.pdf>.
- `irs-fw9.pdf` — IRS Form W-9 (6 pages, AcroForm), from <https://www.irs.gov/pub/irs-pdf/fw9.pdf>.
