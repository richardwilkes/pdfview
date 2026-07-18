// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package pdfview_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/richardwilkes/pdfview"
)

// The veraPDF corpus soak: opens and renders EVERY file of an external corpus, asserting that the engine never panics,
// never hangs (each file must complete within soakFileTimeout — the internal caps guarantee termination, so a timeout
// is a bug), and fails only with the public sentinel errors. It is local-only: CI never has the files. Fetch the corpus
// with testfiles/external/fetch-verapdf.sh and run
//
//	PDFVIEW_SOAK_DIR=testfiles/external/veraPDF-corpus go test -run TestExternalCorpusSoak -v -timeout 60m .
//
// Optionally set PDFVIEW_SOAK_ORACLE to a JSON file produced by `oracle soak` to also compare open-success and
// PageCount against the cgo/MuPDF oracle (mismatches are reported and counted, and only fail the test when a file MuPDF
// opens fails to open here).
const (
	// soakFileTimeout bounds one file's full pipeline (open + auth probe + TOC + render page 0 + search). The internal
	// operator/recursion/allocation caps mean every file terminates; this exists to turn a would-be hang into a test
	// failure naming the file.
	soakFileTimeout = 60 * time.Second
	// soakCacheSize is the per-document maxCacheSize used during the soak, exercising the budgeted store.
	soakCacheSize = 32 << 20
)

type soakOracleRecord struct {
	Open          bool `json:"open"`
	NeedsPassword bool `json:"needsPassword"`
	PageCount     int  `json:"pageCount"`
}

type soakResult struct {
	openErr   error
	renderErr error
	file      string
	elapsed   time.Duration
	pageCount int
	opened    bool
	rendered  bool
	searched  bool
	needsAuth bool
}

func TestExternalCorpusSoak(t *testing.T) {
	dir := os.Getenv("PDFVIEW_SOAK_DIR")
	if dir == "" {
		t.Skip("set PDFVIEW_SOAK_DIR to an external corpus directory (see testfiles/external/fetch-verapdf.sh)")
	}
	var files []string
	//nolint:gosec // Local-only soak; dir comes from the developer's own environment variable.
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".pdf") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no PDFs under %s", dir)
	}
	sort.Strings(files)

	var oracle map[string]*soakOracleRecord
	if oraclePath := os.Getenv("PDFVIEW_SOAK_ORACLE"); oraclePath != "" {
		blob, err := os.ReadFile(oraclePath) //nolint:gosec // Local-only soak; path from the developer's env.
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(blob, &oracle); err != nil {
			t.Fatal(err)
		}
	}

	jobs := make(chan string)
	results := make(chan *soakResult)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				results <- soakOne(t, dir, path)
			}
		}()
	}
	go func() {
		for _, path := range files {
			jobs <- path
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	tally := struct {
		openErrs   map[string]int
		renderErrs map[string]int
		total      int
		opened     int
		rendered   int
		searched   int
		needsAuth  int
		oracleOpen int
		pageAgree  int
		pageDiff   int
		slowest    time.Duration
	}{openErrs: make(map[string]int), renderErrs: make(map[string]int)}
	var pageDiffs, openDiffs []string
	for res := range results {
		tally.total++
		if res.elapsed > tally.slowest {
			tally.slowest = res.elapsed
		}
		if res.opened {
			tally.opened++
		} else {
			tally.openErrs[res.openErr.Error()]++
		}
		if res.rendered {
			tally.rendered++
		} else if res.renderErr != nil {
			tally.renderErrs[res.renderErr.Error()]++
		}
		if res.searched {
			tally.searched++
		}
		if res.needsAuth {
			tally.needsAuth++
		}
		if oracle != nil {
			rec, ok := oracle[res.file]
			switch {
			case !ok:
			case rec.Open && !res.opened:
				openDiffs = append(openDiffs, fmt.Sprintf("%s: MuPDF opens, we fail with %v", res.file, res.openErr))
			case rec.Open && res.opened:
				tally.oracleOpen++
				if rec.PageCount == res.pageCount {
					tally.pageAgree++
				} else {
					tally.pageDiff++
					pageDiffs = append(pageDiffs, fmt.Sprintf("%s: pageCount %d vs oracle %d", res.file, res.pageCount, rec.PageCount))
				}
			}
		}
	}

	t.Logf("soak: %d files, %d opened, %d rendered page 0, %d searched, %d need auth, slowest file %v",
		tally.total, tally.opened, tally.rendered, tally.searched, tally.needsAuth, tally.slowest.Round(time.Millisecond))
	for msg, n := range tally.openErrs {
		t.Logf("  open error %q: %d", msg, n)
	}
	for msg, n := range tally.renderErrs {
		t.Logf("  render error %q: %d", msg, n)
	}
	if oracle != nil {
		t.Logf("  oracle: %d both-open, pageCount agree %d / differ %d; MuPDF-opens-we-fail %d",
			tally.oracleOpen, tally.pageAgree, tally.pageDiff, len(openDiffs))
		sort.Strings(pageDiffs)
		for _, msg := range pageDiffs {
			t.Logf("  pagecount mismatch: %s", msg)
		}
		sort.Strings(openDiffs)
		for _, msg := range openDiffs {
			t.Errorf("open mismatch: %s", msg)
		}
	}
}

// soakOne runs the full pipeline for one file under the per-file timeout. Sentinel-error validation happens inside; a
// non-sentinel error or a timeout fails the test immediately.
func soakOne(t *testing.T, dir, path string) *soakResult {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		t.Error(err)
		return &soakResult{file: path}
	}
	res := &soakResult{file: filepath.ToSlash(rel)}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Error(err)
		return res
	}
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		soakPipeline(t, res, data)
	}()
	select {
	case <-done:
		res.elapsed = time.Since(start)
	case <-time.After(soakFileTimeout):
		res.elapsed = time.Since(start)
		t.Errorf("%s: HANG — did not complete within %v", res.file, soakFileTimeout)
	}
	return res
}

func soakPipeline(t *testing.T, res *soakResult, data []byte) {
	doc, err := pdfview.New(data, soakCacheSize)
	if err != nil {
		res.openErr = err
		if !errors.Is(err, pdfview.ErrNotPDFData) && !errors.Is(err, pdfview.ErrUnableToOpenPDF) &&
			!errors.Is(err, pdfview.ErrUnableToCreatePDFContext) && !errors.Is(err, pdfview.ErrInternal) {
			t.Errorf("%s: open returned a non-sentinel error: %v", res.file, err)
		}
		return
	}
	defer doc.Release()
	res.opened = true
	if doc.RequiresAuthentication() {
		res.needsAuth = true
		doc.Authenticate("") // must not panic; most corpus files with encryption use an empty user password
	}
	res.pageCount = doc.PageCount()
	doc.TableOfContents(72)
	if res.pageCount <= 0 {
		return
	}
	rendered, err := doc.RenderPage(0, 72, 16, "a")
	if err != nil {
		res.renderErr = err
		if !errors.Is(err, pdfview.ErrInvalidPageNumber) && !errors.Is(err, pdfview.ErrUnableToLoadPage) &&
			!errors.Is(err, pdfview.ErrUnableToCreateImage) && !errors.Is(err, pdfview.ErrImageTooLarge) &&
			!errors.Is(err, pdfview.ErrInvalidPageSize) && !errors.Is(err, pdfview.ErrInternal) {
			t.Errorf("%s: render returned a non-sentinel error: %v", res.file, err)
		}
		return
	}
	res.rendered = true
	res.searched = true
	_ = rendered
}
