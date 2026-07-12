# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`github.com/richardwilkes/pdfview` is a **pure-Go PDF engine** (no cgo; builds with `CGO_ENABLED=0`) that renders
PDF pages to images and extracts text-search hits, links, and the table of contents, including for
password-protected documents. Rasterization is delegated to `github.com/richardwilkes/canvas` (a pure-Go Skia
port, locally `../canvas`). The package is the successor to `github.com/richardwilkes/pdf` (MuPDF via cgo), which
remains published separately and serves as this repo's **behavioral oracle**: the test suite pins the public API's
output — coordinates exactly, pixels within committed thresholds — to what that binding produces.

The port was built milestone by milestone against [plan.md](plan.md), which is retained as the historical record:
milestone status, the dated decision log (every behavioral pin against the oracle, with how it was measured), and
per-file pixel-parity numbers. **Consult plan.md's decision log before changing rendering, font, color, or search
behavior** — most constants and code paths in those areas are oracle-pinned, and the log explains what pins them.

## Commands

- `./build.sh` — build everything (`go build -v ./...`)
- `./build.sh --all` — build, golangci-lint, and tests with `-race` (the bar every change must pass)
- `./build.sh --lint` / `--test` / `--race` — the individual pieces
- `go test -run TestParity ./...` — golden-parity suite alone
- `go run ./example document.pdf [search]` — end-to-end demonstration
- `cd oracle && ./regen.sh` — regenerate testfiles/goldens from the cgo binding (local/manual only; requires cgo
  and `../pdf`; CI stays pure Go and offline). Review golden diffs before committing them.
- `testfiles/external/fetch-verapdf.sh` — fetch the veraPDF corpus (gitignored) for the optional soak
  (`PDFVIEW_SOAK_DIR=... go test -run TestExternalCorpusSoak .`)

There is no C toolchain, vendored library, or platform-specific setup on any platform. CI
(.github/workflows/build.yml) runs a four-runner matrix (ubuntu-22.04, ubuntu-22.04-arm, macos-26, windows-2022)
plus an explicit `CGO_ENABLED=0 go build ./...` check and short fuzz smokes.

## Architecture

The frozen public API lives in [pdf.go](pdf.go): `New(buffer, maxCacheSize)`, `RequiresAuthentication`,
`Authenticate`, `PageCount`, `TableOfContents(dpi)`, `RenderPage`, `RenderPageForSize`, `Release`, the sentinel
errors, and the `OverallMax*` budget variables. Its methods hold the document's one mutex, check released state,
enforce budgets, and convert coordinates; they call into the engine seam (`engineDocument`, bottom of the file),
which drives the `internal/` packages. [drawpage.go](drawpage.go) adds `DrawPage`, the one canvas-coupled API
(renders onto a caller-owned `*canvas.Canvas`; only file in the root package that imports canvas types).

Layering (dependencies point downward only):

- `internal/cos` — lexer, object model, xref (classic/stream/hybrid, /Prev chains), object streams, repair scan,
  resolver with cycle guard, decryption hooks
- `internal/filter` — Flate, LZW (both EarlyChange modes), ASCIIHex/85, RunLength, PNG/TIFF predictors
- `internal/crypt` — standard security handler R2–R6 (RC4/AES), auth bits matching the oracle
- `internal/doc` — page tree, destinations, outline, links, annotations (/AP selection + placement), page geometry
  (MediaBox∩CropBox, /Rotate, y-flip)
- `internal/function`, `internal/color` — PDF functions 0/2/3/4; color conversions (behavioral tables captured
  from the oracle's ICC-backed output — do not replace with formulas)
- `internal/type1`, `internal/font` (+ `font/data`) — font programs, encodings, widths, glyph outlines; embedded
  Liberation bundle for deterministic substitution (never system fonts)
- `internal/content` — content-stream interpreter (graphics state, all operators, XObjects, patterns, shadings,
  transparency, Type 3 recursion); emits calls on the `internal/device.Device` seam
- `internal/imaging`, `internal/shading` — image decode (JBIG2/JPX are deliberate blank-rendering stubs), shading
  types 1–7
- `internal/render` — **sole canvas importer**; raster device (fills, strokes, clips, text via outlines plus a
  glyph-coverage blit cache, images, gradients, tiling, groups/soft masks); never import `canvas/gpu`
- `internal/stext` — structured-text device and MuPDF-compatible search (its own scale-1 interpreter pass; see
  the M7 decision log for why it cannot share the render pass)
- `internal/store` — the maxCacheSize-budgeted LRU (pure cache: output never depends on budget)
- `oracle/` — separate cgo module (own go.mod, `replace` to `../pdf`); never imported by the library

Key contracts: every geometry value crosses the seam as **float32** (the C-float funnel the exact-value tests were
baselined against — see plan.md invariant 4); pixels are premultiplied until pdf.go's round-half-up
`unpremultiply`; panics from hostile input never escape the public API (`recover()` at the seam boundaries maps to
sentinel errors); one mutex serializes all engine work per document.

## Testing

- `TestParity` (root) replays the public API against committed goldens in `testfiles/goldens/` (produced by
  `oracle/regen.sh` from `testfiles/corpus/`): sha256 pairing, page counts, auth-status tables, TOC/links/search
  rects (exact), and pixels within each golden's gate — the default (≤2% of pixels over Δ24, ≤10% over Δ8, mean
  Δ≤2) unless the golden carries a `thresholds.json` ratchet (justified, only ever tightened).
- `TestPDF` and friends in pdf_test.go assert exact literals against the GLAIVE fixture — these are the original
  cgo binding's tests, byte-unchanged apart from the copyright header and fixture path; treat any needed change
  to them as an API regression.
- Per-area pixel/quad tests: `TestTextQuadParity` (every search quad, positional, ≤0.5 pt),
  `Test{Vector,Text,Image,Shading,Transparency,Annotation}CorpusPixels`, `TestDrawPage`, `TestCacheBudget`
  (byte-identical renders at any budget).
- Ten fuzz targets (FuzzOpen, FuzzCrypt, FuzzFilters, FuzzContent, FuzzCMap, FuzzFontProgram, FuzzType1,
  FuzzImaging, FuzzShading, FuzzStext). CI smokes them; crashers get committed as regression seeds plus a
  unit-level pin.
- The veraPDF soak (2694 files) is env-gated (`PDFVIEW_SOAK_DIR`) and offline-optional; CI never fetches it.

When pixels drift: regenerate nothing until you understand the diff. Ratchets exist for measured, understood
divergences (substitute-font letterforms, AA-model edge redistribution); a new divergence is a bug until proven
otherwise. Golden regeneration is a deliberate, reviewed act (determinism notes in plan.md's decision log).

## Conventions

- Every `.go` file begins with the standard Richard A. Wilkes MPL-2.0 copyright header (goheader-enforced;
  `internal/font/data/gen` emits it into generated files).
- Errors returned by the public API are the predefined sentinels at the top of pdf.go; return those rather than
  constructing new ones.
- Page numbers are 0-based everywhere. All strings from the engine pass through `sanitizeString`.
- Resource caps are named constants documented where they are defined; termination is guaranteed by caps, not
  timeouts.
- MuPDF/mutool are run-only investigative tools (the repo is clean-room: ISO 32000-2 is the spec authority;
  pdfcpu, rsc.io/pdf, pdf.js, x/image are consultable). Never read MuPDF source. Never modify the sibling repos
  (`../pdf`, `../canvas`, `../mupdf`).
