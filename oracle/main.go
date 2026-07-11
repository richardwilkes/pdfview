// Command oracle dumps the observable behavior of github.com/richardwilkes/pdf (MuPDF via cgo) for a corpus PDF
// into a golden directory that the pure-Go pdfview engine is tested against (see plan.md). It is a development
// tool: it requires cgo and a checkout of the binding at ../../pdf, is never imported by the library, and never
// runs in CI — the goldens it produces are committed. Run regen.sh to regenerate every golden from the corpus.
//
// Usage:
//
//	oracle dump -in file.pdf -out dir [-dpi 72,100,150] [-password pw]... [-search needle]...
//
// The output directory is wiped and recreated with one truth.json (see schema.go) plus one losslessly encoded PNG
// per page per DPI. Output is deterministic for a given corpus file, MuPDF build, and Go release: truth.json is
// stable-marshaled (sorted map keys, shortest float32 round-trip formatting) and PNGs are encoded with a pinned
// compression level, so a re-run that produces any diff signals a real behavior change to review.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/richardwilkes/pdf"
)

// invalidPassword is deliberately attempted against every corpus file so the goldens pin how authentication
// failure is reported.
const invalidPassword = "invalid-password"

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 || os.Args[1] != "dump" {
		log.Fatalf("usage: %s dump -in file.pdf -out dir [-dpi 72,100,150] [-password pw]... [-search needle]...", filepath.Base(os.Args[0]))
	}
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	in := fs.String("in", "", "input PDF file (required)")
	out := fs.String("out", "", "output golden directory, wiped and recreated (required)")
	dpiList := fs.String("dpi", "72,100,150", "comma-separated list of DPIs to dump at")
	var passwords, needles stringList
	fs.Var(&passwords, "password", "password to attempt; repeatable")
	fs.Var(&needles, "search", "search needle to dump quads and hit rectangles for; repeatable")
	if err := fs.Parse(os.Args[2:]); err != nil {
		log.Fatal(err)
	}
	if *in == "" || *out == "" {
		log.Fatal("both -in and -out are required")
	}
	dpis, err := parseDPIs(*dpiList)
	if err != nil {
		log.Fatal(err)
	}
	if err = dump(*in, *out, dpis, passwords, needles); err != nil {
		log.Fatalf("%s: %v", *in, err)
	}
}

func parseDPIs(csv string) ([]int, error) {
	var dpis []int
	for part := range strings.SplitSeq(csv, ",") {
		dpi, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || dpi < 1 {
			return nil, fmt.Errorf("invalid dpi %q", part)
		}
		dpis = append(dpis, dpi)
	}
	if len(dpis) == 0 {
		return nil, fmt.Errorf("no dpis given")
	}
	return dpis, nil
}

func dump(in, out string, dpis []int, passwords, needles []string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	truth := &Truth{
		File:    filepath.Base(in),
		SHA256:  hex.EncodeToString(sum[:]),
		MuPDF:   mupdfVersion(),
		DPIs:    dpis,
		Needles: needles,
	}

	// Record the authentication table: every attempt runs against its own fresh document so no attempt can
	// influence another (a successful authentication changes document state).
	for _, password := range authAttemptPasswords(passwords) {
		doc, docErr := pdf.New(data, 0)
		if docErr != nil {
			return fmt.Errorf("open for auth attempt: %w", docErr)
		}
		truth.Auth = append(truth.Auth, AuthAttempt{Password: password, Status: int(doc.Authenticate(password))})
		doc.Release()
	}

	// The main document everything else is dumped from.
	doc, err := pdf.New(data, 0)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer doc.Release()
	truth.RequiresAuth = doc.RequiresAuthentication()
	if truth.RequiresAuth {
		for _, password := range passwords {
			if doc.Authenticate(password) != 0 {
				truth.AuthPassword = password
				break
			}
		}
		if truth.AuthPassword == "" {
			return fmt.Errorf("document requires authentication and no -password succeeded")
		}
	}
	truth.PageCount = doc.PageCount()
	if truth.PageCount < 1 {
		return fmt.Errorf("document reports no pages")
	}
	toc := make(map[string][]*TOCEntry, len(dpis))
	for _, dpi := range dpis {
		if entries := convertTOC(doc.TableOfContents(dpi)); entries != nil {
			toc[strconv.Itoa(dpi)] = entries
		}
	}
	if len(toc) > 0 {
		truth.TOC = toc
	}

	// The raw MuPDF view: unscaled page-space floats the public API never exposes.
	raw, err := openRaw(data, truth.AuthPassword)
	if err != nil {
		return err
	}
	defer raw.close()
	truth.TOCRaw = raw.outlineTree()

	if err = os.RemoveAll(out); err != nil {
		return err
	}
	if err = os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for pageNumber := range truth.PageCount {
		page, pageErr := dumpPage(doc, raw, out, pageNumber, dpis, needles)
		if pageErr != nil {
			return pageErr
		}
		truth.Pages = append(truth.Pages, page)
	}

	encoded, err := json.MarshalIndent(truth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "truth.json"), append(encoded, '\n'), 0o644)
}

// authAttemptPasswords is the auth table's password list: the empty password, every provided password, and a
// deliberately invalid one, deduplicated preserving order.
func authAttemptPasswords(passwords []string) []string {
	attempts := make([]string, 0, len(passwords)+2)
	for _, password := range slices.Concat([]string{""}, passwords, []string{invalidPassword}) {
		if !slices.Contains(attempts, password) {
			attempts = append(attempts, password)
		}
	}
	return attempts
}

func convertTOC(entries []*pdf.TOCEntry) []*TOCEntry {
	if len(entries) == 0 {
		return nil
	}
	converted := make([]*TOCEntry, 0, len(entries))
	for _, entry := range entries {
		converted = append(converted, &TOCEntry{
			Title:    entry.Title,
			Page:     entry.PageNumber,
			X:        entry.PageX,
			Y:        entry.PageY,
			Children: convertTOC(entry.Children),
		})
	}
	return converted
}

func dumpPage(doc *pdf.Document, raw *rawDoc, out string, pageNumber int, dpis []int, needles []string) (*Page, error) {
	bounds, linksRaw, searchRaw, err := raw.pageRaw(pageNumber, needles)
	if err != nil {
		return nil, err
	}
	page := &Page{
		Page:      pageNumber,
		Bounds:    bounds,
		LinksRaw:  linksRaw,
		SearchRaw: searchRaw,
		Renders:   make(map[string]*Render, len(dpis)),
	}
	for _, dpi := range dpis {
		render, renderErr := dumpRender(doc, out, pageNumber, dpi, needles)
		if renderErr != nil {
			return nil, renderErr
		}
		page.Renders[strconv.Itoa(dpi)] = render
	}
	return page, nil
}

// dumpRender renders one page at one DPI through the public API, writing the PNG and collecting the scaled links
// and per-needle hit rectangles. The image and links come from the first call; each additional needle costs one
// more render whose image is discarded (MuPDF is deterministic, so it is identical).
func dumpRender(doc *pdf.Document, out string, pageNumber, dpi int, needles []string) (*Render, error) {
	firstNeedle := ""
	if len(needles) > 0 {
		firstNeedle = needles[0]
	}
	rendered, err := doc.RenderPage(pageNumber, dpi, pdf.OverallMaxHits, firstNeedle)
	if err != nil {
		return nil, fmt.Errorf("render page %d at dpi %d: %w", pageNumber, dpi, err)
	}
	render := &Render{
		PNG:    fmt.Sprintf("page%d-%d.png", pageNumber, dpi),
		Width:  rendered.Image.Rect.Dx(),
		Height: rendered.Image.Rect.Dy(),
		Stride: rendered.Image.Stride,
		Links:  make([]*Link, 0, len(rendered.Links)),
	}
	for _, link := range rendered.Links {
		render.Links = append(render.Links, &Link{
			URI:    link.URI,
			Page:   link.PageNumber,
			Bounds: [4]int{link.Bounds.Min.X, link.Bounds.Min.Y, link.Bounds.Max.X, link.Bounds.Max.Y},
			DestX:  link.DestPoint.X,
			DestY:  link.DestPoint.Y,
		})
	}
	if len(needles) > 0 {
		render.Search = make(map[string][][4]int, len(needles))
		render.Search[firstNeedle] = convertHits(rendered.SearchHits)
		for _, needle := range needles[1:] {
			again, searchErr := doc.RenderPage(pageNumber, dpi, pdf.OverallMaxHits, needle)
			if searchErr != nil {
				return nil, fmt.Errorf("search page %d at dpi %d for %q: %w", pageNumber, dpi, needle, searchErr)
			}
			render.Search[needle] = convertHits(again.SearchHits)
		}
	}
	// A pinned compression level keeps the encoded bytes deterministic for a given Go release; the pixel data
	// itself is lossless either way (image.NRGBA round-trips exactly through 8-bit RGBA PNG).
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	var buffer bytes.Buffer
	if err = encoder.Encode(&buffer, rendered.Image); err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(out, render.PNG), buffer.Bytes(), 0o644); err != nil {
		return nil, err
	}
	return render, nil
}

func convertHits(hits []image.Rectangle) [][4]int {
	converted := make([][4]int, 0, len(hits))
	for _, hit := range hits {
		converted = append(converted, [4]int{hit.Min.X, hit.Min.Y, hit.Max.X, hit.Max.Y})
	}
	return converted
}
