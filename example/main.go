// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// This example renders the first page of a PDF to a PNG, highlighting any matches of an optional search term, and
// prints the document's table of contents and the page's links.
//
// Usage:
//
//	go run ./example document.pdf [search]
package main

import (
	"errors"
	"fmt"
	"image/png"
	"log"
	"os"

	"github.com/richardwilkes/pdfview"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s document.pdf [search]\n", os.Args[0]) //nolint:gosec // We want the executable's name
	}
	search := ""
	if len(os.Args) > 2 {
		search = os.Args[2]
	}
	if err := extract(os.Args[1], search); err != nil {
		log.Fatal(err)
	}
}

func extract(path, search string) (err error) {
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

	// Render the first page at 150 DPI, highlighting up to 10 search matches.
	var page *pdfview.RenderedPage
	if page, err = doc.RenderPage(0, 150, 10, search); err != nil {
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

	var out *os.File
	if out, err = os.Create("page0.png"); err != nil { //nolint:gosec // For the example, we don't care
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
	fmt.Println("wrote page0.png")
	return nil
}
