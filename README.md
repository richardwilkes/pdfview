# pdfview

[![Go Reference](https://pkg.go.dev/badge/github.com/richardwilkes/pdfview.svg)](https://pkg.go.dev/github.com/richardwilkes/pdfview)
[![Build](https://github.com/richardwilkes/pdfview/actions/workflows/build.yml/badge.svg)](https://github.com/richardwilkes/pdfview/actions/workflows/build.yml)

A **pure-Go PDF engine** that renders PDF pages to images and extracts text-search hits, links, and the table of
contents. It also handles password-protected documents. There is no cgo, no C toolchain requirement, and no vendored
native library: the package builds with `CGO_ENABLED=0` on every supported platform. Rasterization is delegated to
[github.com/richardwilkes/canvas](https://github.com/richardwilkes/canvas), a pure-Go port of Skia.

This package is the successor to [github.com/richardwilkes/pdf](https://github.com/richardwilkes/pdf), which wraps
MuPDF via cgo. The public API is compatible, and the engine was built against that binding as a behavioral oracle:
the test suite pins page counts, authentication semantics, TOC/link/search-hit coordinates (exactly), and rendered
pixels (within committed perceptual thresholds) to what MuPDF produces.

## Features

- Render any page to an `*image.NRGBA`, either at a fixed DPI (`RenderPage`) or scaled to fit a maximum width and
  height (`RenderPageForSize`).
- Return the bounding boxes of search-text matches on a rendered page, with MuPDF-compatible search semantics
  (case folding, elastic whitespace, per-line quads).
- Extract a page's links (both external URIs and internal page references with destination points).
- Extract the document's table of contents.
- Handle password-protected documents: the standard security handler R2–R6 (RC4 and AES), with authentication
  status bits matching MuPDF's `fz_authenticate_password`.
- Draw a page's vector content directly onto a caller-owned canvas via `DrawPage` (the one canvas-coupled API; see
  below).

All returned coordinates (search hits, link bounds, TOC positions) are in the pixel space of the rendered image, so
they line up directly with what you draw.

Damaged files are handled the way real viewers handle them: broken or missing xref tables trigger a repair scan,
corrupt streams degrade to their recoverable prefix, and hostile input is bounded by documented resource caps —
panics never escape the public API (enforced by fuzzing).

### Format coverage

- **COS layer**: classic, stream, and hybrid xref; /Prev chains; object streams; repair scan for broken files.
- **Filters**: Flate, LZW (both EarlyChange modes), ASCIIHex, ASCII85, RunLength, PNG/TIFF predictors, DCT
  (including CMYK/YCCK with Adobe transforms), CCITT Group 3/4.
- **Color**: DeviceGray/RGB/CMYK (behaviorally matched to MuPDF's ICC-backed conversions), CalGray/CalRGB, Lab,
  ICCBased (N-component fallback), Indexed, Separation/DeviceN with tint transforms.
- **Fonts**: embedded TrueType/OpenType, CFF/Type1C, Type 1 (PFA/PFB), Type 0/CID (CIDFontType0 and 2, embedded
  CMaps, Identity-H/V), Type 3; encodings with /Differences and the AGL; ToUnicode; deterministic substitution
  from an embedded Liberation bundle (OFL-1.1) for non-embedded fonts — system fonts are never consulted.
- **Graphics**: full path/clip/text operator set, form XObjects, images (including inline images, image masks,
  SMasks, color-key masking, /Interpolate), shadings types 1–7, tiling and shading patterns, transparency groups,
  soft masks, all 16 blend modes, annotation appearance streams (/AP /N).
- **Stubs**: JBIG2 and JPX (JPEG 2000) images render blank with a debug log entry rather than erroring — no
  maintained pure-Go decoder exists for either. Appearance-stream *synthesis* (drawing widgets that carry no /AP)
  is likewise out of scope; annotations without a usable appearance draw nothing.

## Usage

```go
doc, err := pdfview.New(pdfBytes, 0) // 0 = unlimited resource cache
if err != nil { ... }
defer doc.Release()
page, err := doc.RenderPage(0, 150, 100, "needle")
if err != nil { ... }
// page.Image, page.SearchHits, page.Links
```

A complete, runnable program lives in [example/main.go](example/main.go). It renders the first page of a PDF to a
PNG and reports the table of contents, search hits, and links:

```sh
go run ./example document.pdf [search]
```

`New(buffer, maxCacheSize)` copies the buffer, so callers may reuse theirs. `maxCacheSize` is a byte budget for the
document's internal resource cache (parsed fonts, decoded images, glyph outlines/coverage); 0 means unlimited. The
budget only affects speed, never output: renders are byte-identical at any budget.

Rendered images carry an alpha channel and most PDF pages do not paint their own background, so areas with no
content are transparent rather than white; composite onto your desired background color if you need an opaque page.

### DrawPage

`DrawPage(c *canvas.Canvas, pageNumber int, ctm geom.Matrix) error` renders a page's content through the same
interpreter and device path onto a caller-owned canvas, without touching the caller's surface lifecycle. Unlike the
rest of the API it exposes [canvas](https://github.com/richardwilkes/canvas) types — using it deliberately couples
you to that module. `ctm` maps the page's top-left, y-down space in PDF points onto the canvas:
`geom.ScaleMatrix(dpi/72, dpi/72)` reproduces `RenderPage`'s layout at that dpi.

### Concurrency

`Document` methods are safe to call from multiple goroutines: one mutex serializes all engine work, so calls
execute one at a time (the same contract as the cgo binding, verified with `-race` in CI). `Release` is idempotent
and optional — the GC reclaims everything — but drops parsed state immediately.

### Resource limits

The package-level `OverallMaxHits`, `OverallMaxLinks`, `OverallMaxTOCEntries`, and `OverallMaxPixels` variables cap
how much work untrusted input can force; renders that would exceed `OverallMaxPixels` fail with `ErrImageTooLarge`
before allocation. Internally, documented constant caps bound resolve chains, container nesting, recursion depths,
filter chains, decompression expansion, and interpreter work, so termination is guaranteed without timeouts.

## Architecture

The public API lives in [pdf.go](pdf.go) (plus [drawpage.go](drawpage.go)); everything else is `internal/`:

| Package | Responsibility |
| --- | --- |
| `internal/cos` | Lexer, object model, xref (classic/stream/hybrid), object streams, repair scan, resolver, decryption hooks |
| `internal/filter` | Stream filters and predictors |
| `internal/crypt` | Standard security handler R2–R6 |
| `internal/doc` | Page tree, destinations, outline, links, annotations, page geometry |
| `internal/function` | PDF functions type 0/2/3/4 |
| `internal/color` | Color spaces → RGBA, behaviorally matched to the oracle |
| `internal/type1` / `internal/font` | Font parsing, encodings, widths, glyph outlines |
| `internal/content` | Content-stream interpreter; emits device calls |
| `internal/imaging` | Image XObject decoding |
| `internal/shading` | Shading types 1–7 parsed and tessellated |
| `internal/device` | The device seam: one interpreter, N devices |
| `internal/render` | **Sole canvas importer.** The raster device |
| `internal/stext` | Structured text and MuPDF-compatible search |
| `internal/store` | The maxCacheSize-budgeted LRU cache |

The content interpreter emits drawing operations through a `Device` interface; the raster device realizes them on a
canvas surface, and the structured-text device assembles characters, lines, and search quads from the same calls.
Search runs its own interpreter pass at scale 1 so hit rectangles reproduce the C-float coordinate funnel of the
original binding exactly.

## Testing scheme

- `oracle/` is a separate cgo module (never imported, never built in CI) that runs the MuPDF-based binding over
  [testfiles/corpus](testfiles/corpus) and records `truth.json` + reference PNGs per file into
  [testfiles/goldens](testfiles/goldens) — page counts, auth-status tables, TOC/link/search coordinates (raw floats
  and scaled ints), and rendered pixels at dpi 72/100/150.
- The pure-Go test suite replays the public API against those committed goldens: coordinates and dimensions must
  match exactly; pixels must pass a perceptual gate (by default ≤2% of pixels over Δ24, ≤10% over Δ8, mean Δ≤2).
  Files whose divergence is measured and understood (substitute-font letterforms, AA-model edge differences) carry
  per-golden `thresholds.json` ratchets that may only tighten; each records its justification.
- Corpus provenance is documented in [testfiles/corpus/README.md](testfiles/corpus/README.md): handcrafted probe
  files, encrypted variants, public-domain IRS forms, the GLAIVE fixture, and one CC BY 4.0 cherry-pick from the
  [veraPDF corpus](https://github.com/veraPDF/veraPDF-corpus). The full veraPDF corpus (2694 files) is used as an
  offline soak (`testfiles/external/fetch-verapdf.sh` + `TestExternalCorpusSoak`), not committed.
- Ten fuzz targets cover the parsing surfaces (open, filters, crypt, content, CMap, Type 1, font programs, imaging,
  shading, stext); CI runs short smokes, and crashers found by longer local runs are committed as regression seeds.

## Building

```sh
./build.sh          # go build ./...
./build.sh --all    # build, golangci-lint, go test -race ./...
```

CI runs the same on a four-runner matrix — ubuntu-22.04 (amd64), ubuntu-22.04-arm, macos-26 (arm64), windows-2022
(amd64) — plus an explicit `CGO_ENABLED=0 go build ./...` check. There are no native dependencies on any platform.

## Performance

On the committed benchmark protocol (`BenchmarkRenderGlaive150`, warm renders of the two-page GLAIVE fixture at
150 dpi, darwin/arm64): 8.6/7.6 ms per page versus the cgo MuPDF binding's 6.5/4.9 ms — about 1.3–1.6× cgo, from a
pure-Go engine. Warm renders are served by a glyph-coverage cache, direct compositing under rectangular clips, and
per-document surface reuse.

## History

The pure-Go engine was built milestone by milestone against the plan in [plan.md](plan.md), which is kept as the
historical record of the port: milestone status, the dated decision log (every behavioral pin against the oracle),
and the measured pixel-parity numbers per corpus file.
