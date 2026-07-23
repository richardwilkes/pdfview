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
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// fakeSteps returns steps that skip MuPDF entirely, recording which stages ran.
func fakeSteps(ran *[]string, rgbErr, cmykErr error) steps {
	return steps{
		probeGray: func() []byte {
			*ran = append(*ran, "probeGray")
			return []byte{1, 2, 3}
		},
		verifyRGB: func() error {
			*ran = append(*ran, "verifyRGB")
			return rgbErr
		},
		probeCMYK: func() []byte {
			*ran = append(*ran, "probeCMYK")
			return []byte{4, 5, 6, 7}
		},
		validateCMYK: func(_ []byte) error {
			*ran = append(*ran, "validateCMYK")
			return cmykErr
		},
	}
}

// seed drops sentinel table files into dir, standing in for the tables already committed to the tree.
func seed(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"gray1021.bin", "cmyk17.bin.gz"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("previous"), 0o644); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
}

func checkUntouched(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"gray1021.bin", "cmyk17.bin.gz"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(data) != "previous" {
			t.Errorf("%s = %q, want the pre-existing table left untouched", name, data)
		}
	}
}

func TestGenerateWritesTablesOnlyAfterValidation(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	var ran []string
	if err := generate(dir, fakeSteps(&ran, nil, nil)); err != nil {
		t.Fatalf("generate() unexpected error: %v", err)
	}
	want := []string{"probeGray", "verifyRGB", "probeCMYK", "validateCMYK"}
	if len(ran) != len(want) {
		t.Fatalf("stages ran = %v, want %v", ran, want)
	}
	for i, stage := range want {
		if ran[i] != stage {
			t.Fatalf("stages ran = %v, want %v", ran, want)
		}
	}
	gray, err := os.ReadFile(filepath.Join(dir, "gray1021.bin"))
	if err != nil {
		t.Fatalf("reading gray1021.bin: %v", err)
	}
	if !bytes.Equal(gray, []byte{1, 2, 3}) {
		t.Errorf("gray1021.bin = %v, want the probed table", gray)
	}
	gz, err := os.ReadFile(filepath.Join(dir, "cmyk17.bin.gz"))
	if err != nil {
		t.Fatalf("reading cmyk17.bin.gz: %v", err)
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("cmyk17.bin.gz is not gzipped: %v", err)
	}
	cmyk, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decompressing cmyk17.bin.gz: %v", err)
	}
	if !bytes.Equal(cmyk, []byte{4, 5, 6, 7}) {
		t.Errorf("cmyk17.bin.gz decompressed to %v, want the probed table", cmyk)
	}
}

// A failed DeviceRGB verification must abort before anything is written; the gray table used to land on disk first.
func TestGenerateLeavesTablesAloneWhenRGBVerificationFails(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	seed(t, dir)
	boom := errors.New("DeviceRGB model broke")
	var ran []string
	err := generate(dir, fakeSteps(&ran, boom, nil))
	if !errors.Is(err, boom) {
		t.Fatalf("generate() error = %v, want %v", err, boom)
	}
	checkUntouched(t, dir)
}

// Likewise for the CMYK grid: the tables are only worth committing once interpolation still matches observation.
func TestGenerateLeavesTablesAloneWhenCMYKValidationFails(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	seed(t, dir)
	boom := errors.New("CMYK grid interpolation error grew beyond expectations")
	var ran []string
	err := generate(dir, fakeSteps(&ran, nil, boom))
	if !errors.Is(err, boom) {
		t.Fatalf("generate() error = %v, want %v", err, boom)
	}
	checkUntouched(t, dir)
}
