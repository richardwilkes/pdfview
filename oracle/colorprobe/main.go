// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Command colorprobe regenerates internal/color's behavioral conversion tables by rendering flat-patch probe
// PDFs through github.com/richardwilkes/pdf (MuPDF via cgo) and sampling the resulting pixels — run-only
// behavioral observation (the clean-room rule: observe rendered output, never read MuPDF source). Like the
// golden dumps, it is a local development tool: rerun it (and review the diffs) when the oracle's MuPDF build
// moves.
//
// Usage:
//
//	go run ./colorprobe [-out ../internal/color/data]
//
// It writes gray1021.bin (DeviceGray sampled at i/1020 → RGB bytes) and cmyk17.bin.gz (DeviceCMYK on the 17^4
// grid → RGB bytes, gzipped), then validates that internal/color's evaluation strategies reproduce fresh
// off-grid observations: DeviceRGB as trunc(v×255) per channel exactly, and the CMYK grid under multilinear
// interpolation within a small tolerance. Validation failure means MuPDF's conversion behavior changed shape,
// not just values — internal/color then needs rework, not just new tables.
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"image"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/richardwilkes/pdf"
)

const cell = 8 // patch size in points == pixels at 72 dpi

type patch struct {
	comps []float64
	x, y  int
}

func main() {
	log.SetFlags(0)
	out := flag.String("out", filepath.Join("..", "internal", "color", "data"), "output directory for the table files")
	flag.Parse()

	grayTable := probeGray()
	if err := os.WriteFile(filepath.Join(*out, "gray1021.bin"), grayTable, 0o644); err != nil {
		log.Fatal(err)
	}
	verifyRGB()
	cmykTable := probeCMYK()
	var gz bytes.Buffer
	w, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		log.Fatal(err)
	}
	if _, err = w.Write(cmykTable); err != nil {
		log.Fatal(err)
	}
	if err = w.Close(); err != nil {
		log.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(*out, "cmyk17.bin.gz"), gz.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	validateCMYK(cmykTable)
	log.Printf("wrote %s (%d bytes) and %s (%d bytes gzipped)",
		filepath.Join(*out, "gray1021.bin"), len(grayTable),
		filepath.Join(*out, "cmyk17.bin.gz"), gz.Len())
}

// render builds a single-page PDF filling one patch per comps entry with the given operator, renders it at
// 72 dpi, and returns the patches (with sample coordinates) plus the image.
func render(op string, pats [][]float64) ([]patch, *image.NRGBA) {
	const cols = 96
	rows := (len(pats) + cols - 1) / cols
	width, height := cols*cell, rows*cell
	var content strings.Builder
	out := make([]patch, 0, len(pats))
	for i, comps := range pats {
		col, row := i%cols, i/cols
		x, y := col*cell, row*cell
		vals := make([]string, len(comps))
		for j, c := range comps {
			// Shortest float32 round-trip: MuPDF lexes content reals as C float.
			vals[j] = strconv.FormatFloat(float64(float32(c)), 'f', -1, 32)
		}
		fmt.Fprintf(&content, "%s %s\n%d %d %d %d re\nf\n", strings.Join(vals, " "), op, x, y, cell, cell)
		out = append(out, patch{comps: comps, x: x + cell/2, y: height - 1 - (y + cell/2)})
	}
	var b strings.Builder
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	fmt.Fprintf(&b, "3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R >>\nendobj\n", width, height)
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", content.Len(), content.String())
	b.WriteString("trailer\n<< /Root 1 0 R /Size 5 >>\nstartxref\n0\n%%EOF\n")
	doc, err := pdf.New([]byte(b.String()), 0)
	if err != nil {
		log.Fatal(err)
	}
	defer doc.Release()
	page, err := doc.RenderPage(0, 72, 0, "")
	if err != nil {
		log.Fatal(err)
	}
	return out, page.Image
}

func at(img *image.NRGBA, x, y int) (r, g, b uint8) {
	i := img.PixOffset(x, y)
	return img.Pix[i], img.Pix[i+1], img.Pix[i+2]
}

func probeGray() []byte {
	pats := make([][]float64, 0, 1021)
	for i := 0; i <= 1020; i++ {
		pats = append(pats, []float64{float64(i) / 1020})
	}
	patches, img := render("g", pats)
	table := make([]byte, 1021*3)
	for i, p := range patches {
		table[i*3], table[i*3+1], table[i*3+2] = at(img, p.x, p.y)
	}
	return table
}

// verifyRGB asserts the DeviceRGB model internal/color hard-codes: each channel is trunc(float32(v)×255),
// independent of the others.
func verifyRGB() {
	rng := rand.New(rand.NewSource(7))
	pats := make([][]float64, 0, 3*1021+500)
	for ch := range 3 {
		for i := 0; i <= 1020; i++ {
			comps := []float64{0, 0, 0}
			comps[ch] = float64(i) / 1020
			pats = append(pats, comps)
		}
	}
	for range 500 {
		pats = append(pats, []float64{rng.Float64(), rng.Float64(), rng.Float64()})
	}
	patches, img := render("rg", pats)
	for _, p := range patches {
		r, g, b := at(img, p.x, p.y)
		for ch, got := range []uint8{r, g, b} {
			if want := uint8(float32(p.comps[ch]) * 255); got != want {
				log.Fatalf("DeviceRGB model broke: rgb%v channel %d rendered %d, trunc model says %d",
					p.comps, ch, got, want)
			}
		}
	}
	log.Printf("DeviceRGB trunc model verified over %d patches", len(patches))
}

func probeCMYK() []byte {
	pats := make([][]float64, 0, 17*17*17*17)
	for c := 0; c <= 16; c++ {
		for m := 0; m <= 16; m++ {
			for y := 0; y <= 16; y++ {
				for k := 0; k <= 16; k++ {
					pats = append(pats, []float64{float64(c) / 16, float64(m) / 16, float64(y) / 16, float64(k) / 16})
				}
			}
		}
	}
	patches, img := render("k", pats)
	table := make([]byte, len(pats)*3)
	for i, p := range patches {
		table[i*3], table[i*3+1], table[i*3+2] = at(img, p.x, p.y)
	}
	return table
}

// validateCMYK renders off-grid colors and checks multilinear interpolation of the freshly captured grid
// against them, mirroring internal/color's evaluation.
func validateCMYK(table []byte) {
	rng := rand.New(rand.NewSource(42))
	pats := make([][]float64, 0, 2000)
	for range 2000 {
		pats = append(pats, []float64{rng.Float64(), rng.Float64(), rng.Float64(), rng.Float64()})
	}
	patches, img := render("k", pats)
	var maxErr, sumErr float64
	for _, p := range patches {
		r, g, b := at(img, p.x, p.y)
		est := interp(table, p.comps)
		for ch, got := range []uint8{r, g, b} {
			e := math.Abs(est[ch] - float64(got))
			sumErr += e
			if e > maxErr {
				maxErr = e
			}
		}
	}
	mean := sumErr / float64(3*len(patches))
	log.Printf("CMYK interpolation vs %d off-grid observations: mean %.3f, max %.1f", len(patches), mean, maxErr)
	if maxErr > 4 || mean > 0.75 {
		log.Fatalf("CMYK grid interpolation error grew beyond expectations (mean %.3f, max %.1f); "+
			"the conversion's shape changed — rework internal/color, don't just commit the tables", mean, maxErr)
	}
}

func interp(table []byte, comps []float64) [3]float64 {
	var lo [4]int
	var fr [4]float64
	for i, v := range comps {
		v = math.Min(1, math.Max(0, v)) * 16
		lo[i] = int(v)
		if lo[i] > 15 {
			lo[i] = 15
		}
		fr[i] = v - float64(lo[i])
	}
	var out [3]float64
	for corner := range 16 {
		w := 1.0
		var ix [4]int
		for d := range 4 {
			if corner>>d&1 == 1 {
				w *= fr[d]
				ix[d] = lo[d] + 1
			} else {
				w *= 1 - fr[d]
				ix[d] = lo[d]
			}
		}
		if w == 0 {
			continue
		}
		base := (((ix[0]*17+ix[1])*17+ix[2])*17 + ix[3]) * 3
		for ch := range 3 {
			out[ch] += w * float64(table[base+ch])
		}
	}
	return out
}
