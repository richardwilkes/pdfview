// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// This example renders one page of a PDF to a PNG and prints the document's table of contents, the page's links, and
// the bounding boxes of any matches of an optional search term.
//
// Usage:
//
//	go run ./example [-page n] document.pdf [search]
package main

import (
	"errors"
	"flag"
	"fmt"
	"image/png"
	"log"
	"os"

	"github.com/richardwilkes/pdfview"
)

func main() {
	pageNumber := flag.Int("page", 0, "0-based page number to render")
	flag.Parse()
	if flag.NArg() < 1 {
		log.Fatalf("usage: %s [-page n] document.pdf [search]\n", os.Args[0]) //nolint:gosec // We want the executable's name
	}
	search := ""
	if flag.NArg() > 1 {
		search = flag.Arg(1)
	}
	if err := extract(flag.Arg(0), *pageNumber, search); err != nil {
		log.Fatal(err)
	}
}

func extract(path string, pageNumber int, search string) (err error) {
	var data []byte
	if data, err = os.ReadFile(path); err != nil { //nolint:gosec // For the example, we don't care
		return err
	}

	// Pass 0 for maxCacheSize for no limit.
	var doc *pdfview.Document
	if doc, err = pdfview.New(data, 0); err != nil {
		return err
	}
	defer doc.Release()

	if doc.RequiresAuthentication() {
		return errors.New("document requires a password")
	}

	fmt.Printf("%d page(s)\n\n", doc.PageCount())

	fmt.Println("Table of Contents")
	divider := "-----------------"
	fmt.Println(divider)
	for _, entry := range doc.TableOfContents(150) {
		fmt.Printf("Page %d: %q\n", entry.PageNumber, entry.Title)
	}
	fmt.Println(divider)

	// A document whose catalog has no usable page tree opens successfully with zero pages, so there is nothing to
	// render. Report that rather than letting RenderPage fail with "invalid page number".
	if doc.PageCount() == 0 {
		fmt.Println("document has no renderable pages")
		return nil
	}
	if pageNumber < 0 || pageNumber >= doc.PageCount() {
		return fmt.Errorf("page %d out of range: document has %d page(s), numbered 0-%d", pageNumber, doc.PageCount(),
			doc.PageCount()-1)
	}

	// Render the requested page at 150 DPI, reporting up to 10 search matches.
	var page *pdfview.RenderedPage
	if page, err = doc.RenderPage(pageNumber, 150, 10, search); err != nil {
		return err
	}

	if len(page.SearchHits) != 0 {
		fmt.Println("Search Hits")
		fmt.Println(divider)
		for _, hit := range page.SearchHits {
			fmt.Printf("search hit at %v\n", hit)
		}
		fmt.Println(divider)
	}

	if len(page.Links) != 0 {
		fmt.Println("Page Links")
		fmt.Println(divider)
		for _, link := range page.Links {
			if link.PageNumber >= 0 {
				fmt.Printf("link to page %d (destination %v) at %v\n", link.PageNumber, link.DestPoint, link.Bounds)
			} else {
				fmt.Printf("link to %s at %v\n", link.URI, link.Bounds)
			}
		}
		fmt.Println(divider)
	}

	name := fmt.Sprintf("page%d.png", pageNumber)
	var out *os.File
	if out, err = os.Create(name); err != nil { //nolint:gosec // For the example, we don't care
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if err = png.Encode(out, page.Image); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", name)
	return nil
}
