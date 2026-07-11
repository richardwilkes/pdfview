package function

import (
	"fmt"
	"math"
	"testing"

	"github.com/richardwilkes/pdfview/internal/cos"
)

// docWith parses a minimal PDF whose object 1 is the given body, returning the document; the repair scan
// handles the deliberately missing xref.
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
	// Two samples × two outputs at 4 bits each: values 0x0F, 0xF0 → sample0 = (0, 15), sample1 = (15, 0),
	// decoded over [0,1] as (0,1) and (1,0).
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
