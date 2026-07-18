// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/richardwilkes/pdf"
)

// SoakRecord is what the oracle observed for one corpus file: whether MuPDF opened it, whether it demands a
// password, and its page count. The pure-Go soak test (soak_test.go at the repo root) compares open-success and
// PageCount against these records when PDFVIEW_SOAK_ORACLE points at the emitted JSON.
type SoakRecord struct {
	Open          bool `json:"open"`
	NeedsPassword bool `json:"needsPassword,omitempty"`
	PageCount     int  `json:"pageCount"`
}

// soak walks dir for PDFs and writes a map of relative path to SoakRecord as stable JSON to out.
func soak(dir, out string) error {
	records := make(map[string]*SoakRecord)
	var paths []string
	//nolint:gosec // Local dev tool; dir comes from the developer's command line.
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".pdf") {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(paths)
	for i, path := range paths {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rec := &SoakRecord{}
		if doc, docErr := pdf.New(data, 0); docErr == nil {
			rec.Open = true
			rec.NeedsPassword = doc.RequiresAuthentication()
			rec.PageCount = doc.PageCount()
			doc.Release()
		}
		records[filepath.ToSlash(rel)] = rec
		if (i+1)%250 == 0 {
			log.Printf("soak: %d/%d", i+1, len(paths))
		}
	}
	blob, err := json.MarshalIndent(records, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(out, append(blob, '\n'), 0o644) //nolint:gosec // Local dev tool; out is a command-line arg.
}

func soakMain(args []string) {
	if len(args) != 2 {
		log.Fatalf("usage: %s soak dir out.json", filepath.Base(os.Args[0])) //nolint:gosec // We want the executable's name.
	}
	if err := soak(args[0], args[1]); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", args[1])
}
