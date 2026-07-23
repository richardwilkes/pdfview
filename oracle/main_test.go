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
	"slices"
	"testing"
)

func TestParseDPIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		csv  string
		want []int
	}{
		{name: "single", csv: "72", want: []int{72}},
		{name: "multiple", csv: "72,100,150", want: []int{72, 100, 150}},
		{name: "trims spaces", csv: " 72 , 100 ", want: []int{72, 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDPIs(tc.csv)
			if err != nil {
				t.Fatalf("parseDPIs(%q) unexpected error: %v", tc.csv, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("parseDPIs(%q) = %v, want %v", tc.csv, got, tc.want)
			}
		})
	}
}

func TestParseDPIsRejectsDuplicates(t *testing.T) {
	// A duplicate DPI would collapse under a shared strconv.Itoa key and silently discard one dump; it must be rejected.
	for _, csv := range []string{"72,72", "72,100,72", "150, 150"} {
		if _, err := parseDPIs(csv); err == nil {
			t.Errorf("parseDPIs(%q) = nil error, want duplicate rejection", csv)
		}
	}
}

func TestParseDPIsRejectsInvalid(t *testing.T) {
	for _, csv := range []string{"", "0", "-1", "abc", "72,x"} {
		if _, err := parseDPIs(csv); err == nil {
			t.Errorf("parseDPIs(%q) = nil error, want invalid rejection", csv)
		}
	}
}

func TestValidateNeedles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		needles []string
		wantErr bool
	}{
		{name: "none", needles: nil, wantErr: false},
		{name: "unique", needles: []string{"a", "b", "c"}, wantErr: false},
		{name: "empty allowed once", needles: []string{""}, wantErr: false},
		{name: "adjacent duplicate", needles: []string{"a", "a"}, wantErr: true},
		{name: "separated duplicate", needles: []string{"a", "b", "a"}, wantErr: true},
		{name: "duplicate empty", needles: []string{"", ""}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNeedles(tc.needles)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateNeedles(%v) error = %v, wantErr %v", tc.needles, err, tc.wantErr)
			}
		})
	}
}
