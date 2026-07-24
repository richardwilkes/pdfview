// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package function

import (
	"fmt"
	"math"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// docWith parses a minimal PDF whose object 1 is the given body, returning the document; the repair scan handles the
// deliberately missing xref.
func docWith(t *testing.T, body string) *cos.Document {
	t.Helper()
	pdf := "%PDF-1.7\n1 0 obj\n" + body + "\nendobj\n" +
		"2 0 obj\n<< /Type /Catalog >>\nendobj\n" +
		"trailer\n<< /Root 2 0 R /Size 3 >>\nstartxref\n0\n%%EOF\n"
	d, err := cos.Open([]byte(pdf))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func docWithStream(t *testing.T, dict, payload string) *cos.Document {
	t.Helper()
	return docWith(t, fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(payload), payload))
}

func parseObj1(t *testing.T, d *cos.Document) Func {
	t.Helper()
	fn, err := Parse(d, cos.Ref{Num: 1})
	if err != nil {
		t.Fatal(err)
	}
	return fn
}

func evalOne(t *testing.T, fn Func, in ...float32) []float32 {
	t.Helper()
	return fn.Eval(in)
}

func near(t *testing.T, got []float32, want ...float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-4 {
			t.Fatalf("got %v, want %v", got, want)
			return
		}
	}
}

func TestExponential(t *testing.T) {
	d := docWith(t, "<< /FunctionType 2 /Domain [0 1] /C0 [0 0.5] /C1 [1 0] /N 1 >>")
	fn := parseObj1(t, d)
	if fn.NInputs() != 1 || fn.NOutputs() != 2 {
		t.Fatalf("shape %d->%d", fn.NInputs(), fn.NOutputs())
	}
	near(t, evalOne(t, fn, 0), 0, 0.5)
	near(t, evalOne(t, fn, 1), 1, 0)
	near(t, evalOne(t, fn, 0.5), 0.5, 0.25)
	near(t, evalOne(t, fn, 2), 1, 0) // clamped to domain
	near(t, evalOne(t, fn, -1), 0, 0.5)
	near(t, evalOne(t, fn), 0, 0.5) // missing input reads as domain minimum
}

func TestExponentialDefaults(t *testing.T) {
	d := docWith(t, "<< /FunctionType 2 /Domain [0 1] /N 2 >>")
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0.5), 0.25) // default C0=[0], C1=[1]
}

func TestStitching(t *testing.T) {
	d := docWith(t, `<< /FunctionType 3 /Domain [0 1] /Bounds [0.5]
 /Functions [ << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >>
              << /FunctionType 2 /Domain [0 1] /C0 [1] /C1 [0] /N 1 >> ]
 /Encode [0 1 0 1] >>`)
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 0)
	near(t, evalOne(t, fn, 0.25), 0.5) // first half encodes [0,0.5) onto [0,1]
	near(t, evalOne(t, fn, 0.5), 1)    // second subfunction starts at its bound: 1-0=1 at encoded 0... C0=1
	near(t, evalOne(t, fn, 0.75), 0.5)
	near(t, evalOne(t, fn, 1), 0)
}

func TestStitchingBadBounds(t *testing.T) {
	subs := ` /Functions [ << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >>
              << /FunctionType 2 /Domain [0 1] /C0 [1] /C1 [0] /N 1 >>
              << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >> ]
 /Encode [0 1 0 1 0 1] >>`
	for _, bounds := range []string{
		"[0.7 0.3]",  // not nondecreasing (inverted)
		"[0.3 1.5]",  // above Domain max
		"[-0.2 0.6]", // below Domain min
	} {
		d := docWith(t, `<< /FunctionType 3 /Domain [0 1] /Bounds `+bounds+subs)
		if _, err := Parse(d, cos.Ref{Num: 1}); err == nil {
			t.Errorf("bounds %s parsed", bounds)
		}
	}
	// Valid nondecreasing bounds within Domain still parse.
	d := docWith(t, `<< /FunctionType 3 /Domain [0 1] /Bounds [0.3 0.6]`+subs)
	if _, err := Parse(d, cos.Ref{Num: 1}); err != nil {
		t.Errorf("valid bounds rejected: %v", err)
	}
}

func TestStitchingRangeMustMatchSubfunctions(t *testing.T) {
	subs := ` /Functions [ << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >>
              << /FunctionType 2 /Domain [0 1] /C0 [1] /C1 [0] /N 1 >> ]
 /Bounds [0.5] /Encode [0 1 0 1] >>`
	for _, rng := range []string{
		"/Range [0 1 0 1 0 1] ", // wider than the 1-output subfunctions
		"/Range [0 1 0 1] ",     // still wider
	} {
		d := docWith(t, `<< /FunctionType 3 /Domain [0 1] `+rng+subs)
		if _, err := Parse(d, cos.Ref{Num: 1}); err == nil {
			t.Errorf("%s parsed", rng)
		}
	}
	// A /Range that agrees with the subfunctions parses, and NOutputs() matches Eval's result length.
	d := docWith(t, `<< /FunctionType 3 /Domain [0 1] /Range [0 0.5] `+subs)
	fn := parseObj1(t, d)
	if fn.NOutputs() != 1 {
		t.Fatalf("NOutputs %d, want 1", fn.NOutputs())
	}
	got := evalOne(t, fn, 0.25)
	if len(got) != fn.NOutputs() {
		t.Fatalf("Eval returned %d values, NOutputs reports %d", len(got), fn.NOutputs())
	}
	near(t, got, 0.5) // 0.25 encodes to 0.5 in the first subfunction, within the declared range
}

func TestSampled(t *testing.T) {
	// 3-sample 8-bit ramp 0, 128, 255 over domain [0,1]: linear interpolation between samples.
	payload := string([]byte{0, 128, 255})
	d := docWithStream(t, "/FunctionType 0 /Domain [0 1] /Range [0 1] /Size [3] /BitsPerSample 8", payload)
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 0)
	near(t, evalOne(t, fn, 1), 1)
	near(t, evalOne(t, fn, 0.5), 128.0/255)
	near(t, evalOne(t, fn, 0.25), 64.0/255)
}

func TestSampledMultiOut4Bit(t *testing.T) {
	// Two samples × two outputs at 4 bits each: values 0x0F, 0xF0 → sample0 = (0, 15), sample1 = (15, 0), decoded over
	// [0,1] as (0,1) and (1,0).
	payload := string([]byte{0x0F, 0xF0})
	d := docWithStream(t, "/FunctionType 0 /Domain [0 1] /Range [0 1 0 1] /Size [2] /BitsPerSample 4", payload)
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 0, 1)
	near(t, evalOne(t, fn, 1), 1, 0)
	near(t, evalOne(t, fn, 0.5), 0.5, 0.5)
}

func TestSampledTooSmall(t *testing.T) {
	d := docWithStream(t, "/FunctionType 0 /Domain [0 1] /Range [0 1] /Size [300] /BitsPerSample 8", "abc")
	if _, err := Parse(d, cos.Ref{Num: 1}); err == nil {
		t.Fatal("undersized sample data parsed")
	}
}

func TestCalculator(t *testing.T) {
	d := docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 1 0 1]", "{ dup 1 exch sub }")
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0.25), 0.25, 0.75)
}

func TestCalculatorIfElse(t *testing.T) {
	d := docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 2]", "{ dup 0.5 lt { 2 mul } { 0.5 add } ifelse }")
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0.25), 0.5)
	near(t, evalOne(t, fn, 0.75), 1.25)
}

func TestCalculatorStackOps(t *testing.T) {
	d := docWithStream(t, "/FunctionType 4 /Domain [0 1 0 1 0 1] /Range [0 1 0 1 0 1]", "{ 3 1 roll }")
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0.1, 0.2, 0.3), 0.3, 0.1, 0.2)
	d = docWithStream(t, "/FunctionType 4 /Domain [0 1 0 1] /Range [0 1]", "{ add 2 div }")
	fn = parseObj1(t, d)
	near(t, evalOne(t, fn, 0.2, 0.6), 0.4)
}

func TestCalculatorArithmetic(t *testing.T) {
	d := docWithStream(t, "/FunctionType 4 /Domain [0 10] /Range [-100 100]",
		"{ dup dup mul exch 3 add sub }") // x -> x² - (x+3)
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 4), 16-7)
}

func TestCalculatorBitshift(t *testing.T) {
	// Right shift (negative operand) must shift in zeros: -8 (0xFFFFFFF8) >> 1 = 0x7FFFFFFC, not the arithmetic -4.
	d := docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [-2147483648 2147483647]", "{ pop -8 -1 bitshift }")
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 0x7FFFFFFC)
	// Left shift is unchanged: 1 << 4 = 16.
	d = docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 256]", "{ pop 1 4 bitshift }")
	fn = parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 16)
	// Shifts of magnitude >= 32 yield 0.
	d = docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [-1 1]", "{ pop -8 -40 bitshift }")
	fn = parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 0)
}

// TestCalculatorIntConversionClamps pins the float→int conversions the calculator's integer operators depend on. Go
// leaves an out-of-range float→int conversion implementation-defined and the architectures disagree (arm64 saturates,
// amd64 wraps to the sentinel), so the bounds must be applied in float space to keep results identical everywhere.
func TestCalculatorIntConversionClamps(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int32
	}{
		{in: 0, want: 0},
		{in: 2.75, want: 2},
		{in: -2.75, want: -2},
		{in: math.NaN(), want: 0},
		{in: math.Inf(1), want: math.MaxInt32},
		{in: math.Inf(-1), want: math.MinInt32},
		{in: 1e20, want: math.MaxInt32},
		{in: -1e20, want: math.MinInt32},
		{in: math.MaxInt32, want: math.MaxInt32},
		{in: math.MinInt32, want: math.MinInt32},
		{in: math.MaxInt32 + 1, want: math.MaxInt32},
		{in: math.MinInt32 - 1, want: math.MinInt32},
	} {
		if got := psToInt32(tc.in); got != tc.want {
			t.Errorf("psToInt32(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in   float64
		want int
	}{
		{in: 0, want: 0},
		{in: 3.9, want: 3},
		{in: psStackLimit, want: psStackLimit},
		{in: -0.5, want: -1},
		{in: -1, want: -1},
		{in: math.NaN(), want: -1},
		{in: math.Inf(1), want: -1},
		{in: math.Inf(-1), want: -1},
		{in: 1e20, want: -1},
		{in: psStackLimit + 1, want: -1},
	} {
		if got := psToStackCount(tc.in); got != tc.want {
			t.Errorf("psToStackCount(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestCalculatorOutOfRangeOperands checks the clamping end to end: a program that feeds a non-finite or far
// out-of-int32 value to an operator that needs an integer must produce the same output on every architecture.
func TestCalculatorOutOfRangeOperands(t *testing.T) {
	const (
		big      = "10000000000 10000000000 mul" // 1e20, far outside int32
		nan      = "0 0 div"
		small    = "[0 100]"
		signed   = "[-100 100]"
		int32Rng = "[-2147483648 2147483647]"
	)
	for _, tc := range []struct {
		program string
		rng     string
		want    float32
	}{
		// A count operand that is not a usable stack count must abort the program everywhere, rather than reading as 0
		// (a silent no-op that runs the rest of the program) on one architecture and as a huge negative (an abort) on
		// another. Each of these aborts before the trailing 99 is pushed, so the output stays 1.
		{program: "{ pop 1 " + nan + " copy 99 }", rng: small, want: 1},
		{program: "{ pop 1 " + nan + " index 99 }", rng: small, want: 1},
		{program: "{ pop 1 " + nan + " 1 roll 99 }", rng: small, want: 1},
		{program: "{ pop 1 " + big + " copy 99 }", rng: small, want: 1},
		{program: "{ pop 1 " + big + " neg index 99 }", rng: small, want: 1},
		// Integer operands saturate to the int32 bounds and NaN reads as 0.
		{program: "{ pop " + big + " 1 idiv }", rng: int32Rng, want: math.MaxInt32},
		{program: "{ pop " + big + " neg 1 idiv }", rng: int32Rng, want: math.MinInt32},
		{program: "{ pop " + big + " 10 mod }", rng: signed, want: math.MaxInt32 % 10},
		{program: "{ pop " + big + " 255 and }", rng: "[-1 4096]", want: 255},
		{program: "{ pop " + nan + " 255 or }", rng: "[-1 4096]", want: 255},
		{program: "{ pop " + nan + " not }", rng: signed, want: -1},
		{program: "{ pop 1 " + big + " bitshift }", rng: signed, want: 0},
		{program: "{ pop 1 " + big + " neg bitshift }", rng: signed, want: 0},
	} {
		d := docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range "+tc.rng, tc.program)
		fn := parseObj1(t, d)
		if got := evalOne(t, fn, 0); got[0] != tc.want {
			t.Errorf("program %q produced %v, want %v", tc.program, got[0], tc.want)
		}
	}
}

func TestCalculatorEqTypeAware(t *testing.T) {
	// Boolean true is stored as 1.0 but must not compare equal to the number 1.0 (ISO 32000-2 typed equality).
	d := docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 1]", "{ pop true 1 eq { 1 } { 0 } ifelse }")
	fn := parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 0) // true != 1
	// ne is the negation: true ne 1 is true.
	d = docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 1]", "{ pop true 1 ne { 1 } { 0 } ifelse }")
	fn = parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 1)
	// Same-typed operands still compare by value: 1 eq 1 is true, true eq true is true.
	d = docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 1]", "{ pop 1 1 eq { 1 } { 0 } ifelse }")
	fn = parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 1)
	d = docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 1]", "{ pop true true eq { 1 } { 0 } ifelse }")
	fn = parseObj1(t, d)
	near(t, evalOne(t, fn, 0), 1)
}

func TestCalculatorDivZeroClamps(t *testing.T) {
	d := docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 1]", "{ 0 div }")
	fn := parseObj1(t, d)
	got := evalOne(t, fn, 0.5) // 0.5/0 = +Inf, clamped to range top... clamp maps NaN/over to bounds
	if got[0] != 1 && got[0] != 0 {
		t.Fatalf("division by zero produced %v", got)
	}
}

func TestCalculatorMalformed(t *testing.T) {
	for _, program := range []string{
		"{ 1 2 add",                    // unterminated
		"{ { 1 } }",                    // procedure without if
		"{ frobnicate }",               // unknown operator
		"no braces at all",             // not a procedure
		"{ { 1 } { 2 } { 3 } ifelse }", // too many procedures
	} {
		d := docWithStream(t, "/FunctionType 4 /Domain [0 1] /Range [0 1]", program)
		if _, err := Parse(d, cos.Ref{Num: 1}); err == nil {
			t.Errorf("program %q parsed", program)
		}
	}
}

func TestParseRejects(t *testing.T) {
	d := docWith(t, "<< /FunctionType 9 /Domain [0 1] >>")
	if _, err := Parse(d, cos.Ref{Num: 1}); err == nil {
		t.Error("unknown function type parsed")
	}
	d = docWith(t, "<< /FunctionType 2 /N 1 >>")
	if _, err := Parse(d, cos.Ref{Num: 1}); err == nil {
		t.Error("missing domain parsed")
	}
}
