# Codec benchmark fixtures

Payloads for `BenchmarkJBIG2Decode` and `BenchmarkJPXDecode` (`../../bench_codec_test.go`). They are raw codec
payloads, not PDFs: each is exactly the bytes `cos.ImageFilterSplit` hands the codec glue, so the benchmarks drive
`decodeJBIG2Plane` and `jpxRasterFor` — the same entry points the three switch sites and the fuzz targets reach.

Both sources were drawn by a throwaway dev-time Go program (the convention the rest of this repository's fixtures
follow — see `../../../../testfiles/corpus/README.md`): every mark is machine-drawn from primitive rectangles,
strokes, discs, gradients, and a seeded LCG, so no third-party image data and no rasterized font is involved. Only
the encoder outputs are committed; the sources and the generator are not.

| File | Bytes | Pixels | Codec shape |
| --- | --- | --- | --- |
| `scanned-text.jbig2` | 4493 | 2550x3300 (8,415,000) | Arithmetic symbol dictionary (72 symbols) + text region (3088 instances) |
| `rgb-53.jp2` | 972468 | 1024x1024 (1,048,576) | 3-component lossless: 5/3 reversible wavelet, RCT, 1 tile, 6 resolutions |
| `rgb-97.jp2` | 393125 | 1024x1024 (1,048,576) | 3-component lossy: 9/7 irreversible wavelet, ICT, scalar-expounded quantization at 8x |

## `scanned-text.jbig2`

Encoded with jbig2enc 0.32 (leptonica 1.87.0, `brew install jbig2enc`; `jbig2 --version`):

    jbig2 -s -p -b bench page.png
    cat bench.sym bench.0000 > scanned-text.jbig2

`-p` selects the PDF embedded profile — no file header, sequential segment headers, everything on page 1 — and `-s`
puts the marks in a symbol dictionary plus a text region rather than a generic region. `-b` splits the result into a
`.sym` dictionary segment (page association 0) and a `.0000` page information + text region pair; concatenating them
is the same self-contained single-stream convention `testfiles/corpus/images-jbig2-text.pdf` uses, so the fixture
needs no `/JBIG2Globals`. The page information segment declares 0 dpi (the source PNG carries no resolution and PDF
ignores the field regardless); 2550x3300 is the pixel geometry of US Letter at 300 dpi.

`page.png` is a 1-bit paletted PNG of a synthetic text page: an inventory of 72 distinct pseudo-glyphs, each a
subset of 5 to 9 primitive bars laid on a 3x5 lattice of stroke endpoints (4 px thick, 24x36 px cell), kept only if
its ink forms a single 8-connected component — what a JBIG2 classifier treats as one symbol. The glyphs are placed
in 50 lines of 2-to-9-glyph words with paragraph indents and short paragraph-final lines, 3088 placements in all,
selected with a cubic skew so a few glyphs recur far more often than the rest, as letter frequencies do. Ink covers
9.87% of the page. The source carries no scanner noise, which is the conservative direction for the budget
arithmetic below: noise inflates a real payload's byte count without changing its pixel count.

The encoding is exact for this source — jbig2enc's classifier kept all 72 symbols rather than substituting — and the
committed payload decoded through `decodeJBIG2Plane` matches the source page bit for bit, 0 of 8,415,000 pixels
differing.

### Budget headroom (the `maxPixelsFor` evidence)

`decodeJBIG2Plane` bounds the decode by `maxPixelsFor(len(payload))`, which for a payload this size is
`4493 x 8192 = 36,806,656` pixels (under `maxImagePixels`, so no clamp). The page needs 8,415,000 of that, 22.9% —
a factor of 4.37 in hand. Put the other way, the fixture decodes 1873 pixels per payload byte against the 8192 the
cap allows, so a realistic scanned page sits within a factor of 4.4 of a budget whose stated rationale is CCITT G4
compression. A payload below 1028 bytes could not carry this page at all.

Resolution is what consumes that margin, since a symbol-coded payload grows with the number of marks, not with the
pixels they cover. A dev-time probe (not committed) re-encoded this same page at 600 dpi — 5100x6600, 33,660,000
pixels, still inside `maxImagePixels` — and jbig2enc produced 4788 bytes: 7030 pixels per payload byte, a headroom
factor of 1.17. A page with fewer distinct symbols, or more reuse of them, would be rejected outright at that
resolution.

## `rgb-53.jp2` and `rgb-97.jp2`

Encoded with OpenJPEG 2.5.4's `opj_compress` (`brew install openjpeg`; the banner of `opj_compress -h` reports the
library version):

    opj_compress -i src.ppm -o rgb-53.jp2
    opj_compress -i src.ppm -o rgb-97.jp2 -I -r 8

The first row is `opj_compress`'s documented defaults: lossless, 1 tile, RGB->YCC (reversible), 64x64 code-blocks,
6 resolutions, LRCP, no mode switches. `-I` selects the irreversible 9/7 wavelet and `-r 8` an 8:1 compression
ratio against the 3,145,728-byte raw source. The `.jp2` extension selects the JP2 container in both cases, the
shape PDF payloads overwhelmingly take.

`src.ppm` is a 1024x1024 P6 RGB image built in layers so no wavelet subband is trivial: a smooth 2-D gradient (red
horizontal, green vertical, blue radial) modulated by a chirped ripple whose spatial frequency climbs left to
right; 22 hard-edged discs and rectangles in seeded colors; 60 one-pixel diagonal hairlines; a 320x256 patch of
seeded per-channel noise; and three checkerboards at 1, 2, and 4 px cells, the Nyquist-adjacent content.

Both payloads were verified through `jpxRasterFor`, and against OpenJPEG itself:

- `rgb-53.jp2` round-trips bit-exact through `opj_decompress`, and the raster `jpxRasterFor` returns for it is
  bit-identical to `src.ppm` — all 3,145,728 samples.
- `rgb-97.jp2` is lossy by construction (PSNR 42.72 dB against the source). The raster `jpxRasterFor` returns
  differs from `opj_decompress`'s own output on 4617 of 3,145,728 samples, every one of them by 1, which is the
  floating-point rounding spread expected of an independent 9/7 implementation.

Neither payload comes near its pixel budget. `jpxSizGuard` charges the budget in samples rather than pixels, so
these need `1024 x 1024 x 3 = 3,145,728` against a `maxPixelsFor` result that both payload sizes drive past
`maxImagePixels` and therefore clamp to 67,108,864 — a factor of 21.3 in hand. The smallest payload that could
still carry this image is 384 bytes.
