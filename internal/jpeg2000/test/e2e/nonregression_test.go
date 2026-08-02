// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package e2e_test

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/j2k"
	"github.com/richardwilkes/pdfview/internal/jpeg2000/jp2"
)

// Opt-in robustness sweep over the OpenJPEG test-data "nonregression" corpus — a
// large grab-bag of real-world JPEG 2000 files (DICOM, digital cinema, PDF-embedded,
// various encoders) AND deliberately malformed inputs (fuzzer crashers named
// *.SIGSEGV* / *.SIGFPE* / *.asan* / heap-oob, truncated streams, illegal markers).
// Like the conformance test it needs OPJ_DATA_ROOT (or a sibling openjpeg-data
// checkout) and skips when absent; the data is not vendored (restrictive licence).
//
// The contract is the hardening contract: for EVERY file, decoding must either
// return an image or a clean error — never panic, never hang. This is what makes the
// decoder safe to point at untrusted input. The corpus is the strongest available
// adversarial input set, complementing the Go fuzz harnesses.
//
//	OPJ_DATA_ROOT=/path/to/openjpeg-data go test ./test/e2e/ -run NonRegression
func TestNonRegressionNoPanic(t *testing.T) {
	root := conformanceRoot(t)
	if root == "" {
		t.Skip("ISO conformance data not found; set OPJ_DATA_ROOT (see test doc)")
	}
	dir := filepath.Join(root, "input", "nonregression")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("nonregression dir not found: %v", err)
	}

	// The decoder imposes no arbitrary image-size cap (it follows the standard
	// library: allocate proportional to the declared dimensions). So before fully
	// decoding an untrusted corpus file we read its dimensions cheaply with
	// DecodeConfig and skip ones whose declared size would exhaust test memory — the
	// idiomatic gopher pattern for untrusted input. (A couple of corpus files declare
	// hundreds of millions to billions of pixels.)
	const maxTestPixels = 1 << 26 // ~67M px ≈ 268 MiB for a single int32 plane

	tooLarge := func(name string, data []byte) bool {
		var cfg image.Config
		var err error
		if strings.HasSuffix(strings.ToLower(name), ".jp2") {
			cfg, err = jp2.DecodeConfig(bytes.NewReader(data))
		} else {
			cfg, err = j2k.DecodeConfig(bytes.NewReader(data))
		}
		if err != nil { // unreadable header → let the full decode report the error
			return false
		}
		return int64(cfg.Width)*int64(cfg.Height) > maxTestPixels
	}

	decode := func(name string, data []byte) (err error) {
		// A panic here is a test failure, not a recovered error: the public API must
		// not panic on any input. Recover so one bad file does not abort the sweep,
		// and report it as a hard failure below.
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC decoding %s: %v", name, r)
			}
		}()
		if strings.HasSuffix(strings.ToLower(name), ".jp2") {
			_, err = jp2.Decode(bytes.NewReader(data))
		} else {
			// Raw codestream (.j2k/.j2c/.jpc): the generic per-component path accepts
			// any component count, so a clean decode is not gated on the 1/2/3/4 image
			// shapes.
			_, err = j2k.DecodeComponents(bytes.NewReader(data), j2k.Options{})
		}
		return err
	}

	var total, decoded, errored, skipped int
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if !(strings.HasSuffix(n, ".jp2") || strings.HasSuffix(n, ".j2k") ||
			strings.HasSuffix(n, ".j2c") || strings.HasSuffix(n, ".jpc")) {
			continue
		}
		total++
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Errorf("read %s: %v", e.Name(), rerr)
			continue
		}
		if tooLarge(e.Name(), data) {
			skipped++
			continue
		}
		if err := decode(e.Name(), data); err != nil {
			errored++
		} else {
			decoded++
		}
	}
	t.Logf("nonregression: %d files, %d decoded, %d clean errors, %d skipped (oversized), 0 panics", total, decoded, errored, skipped)
	if total == 0 {
		t.Skip("no nonregression files found")
	}
}

// TestNonRegressionDecodes pins a set of real-world files that this decoder is
// expected to decode without error — they exercise features that were once rejected
// but are valid: Psot=0 (tile-part runs to EOC/end), a missing EOC marker, an
// inconsistent TPsot/TNsot, and 2-component (greyscale+alpha) images. Guarding them
// here prevents a regression from silently re-breaking real files.
func TestNonRegressionDecodes(t *testing.T) {
	root := conformanceRoot(t)
	if root == "" {
		t.Skip("ISO conformance data not found; set OPJ_DATA_ROOT (see test doc)")
	}
	dir := filepath.Join(root, "input", "nonregression")

	cases := []struct {
		file string
		why  string
	}{
		{"issue228.j2k", "Psot=0 (tile-part to end of codestream)"},
		{"tnsot_zero_missing_eoc.jp2", "codestream without an EOC marker"},
		{"issue254.jp2", "inconsistent TPsot/TNsot tolerated"},
		{"issue208.jp2", "inconsistent TPsot/TNsot, 4-component"},
		{"basn4a08.jp2", "2-component greyscale+alpha"},
		{"Marrin.jp2", "2-component greyscale+alpha"},
		{"db11217111510058.jp2", "truncated jp2c codestream box"},
		{"issue726.j2k", "tile grid declares 97 tiles but only tile 0 is coded (absent tiles → blank)"},
		{"dwt_interleave_h.gsr105.jp2", "degenerate coarse subbands (clipped tile-component)"},
		{"issue399.j2k", "Kakadu 3x3 tiles; centre tile-part COD overrides the layer count"},
		{"issue236-ESYCC-CDEF.jp2", "e-sYCC colour (enum 24) + cdef reversing the channel order"},
		{"issue205.jp2", "CMYK colour (enum 12)"},
		{"issue559-eci-090-CIELab.jp2", "CIELab colour (enum 14) -> sRGB via the D50 PCS"},
		{"issue171.jp2", "embedded matrix/TRC ICC profile (colr METH=2) -> sRGB"},
		{"relax.jp2", "ICC profile with a degenerate matrix falls back to raw rendering"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, tc.file))
			if err != nil {
				t.Skipf("file absent: %v", err)
			}
			if strings.HasSuffix(strings.ToLower(tc.file), ".jp2") {
				_, err = jp2.Decode(bytes.NewReader(data))
			} else {
				_, err = j2k.DecodeComponents(bytes.NewReader(data), j2k.Options{})
			}
			if err != nil {
				t.Fatalf("expected %s to decode (%s), got error: %v", tc.file, tc.why, err)
			}
		})
	}
}

// TestNonRegressionCMYK checks that a CMYK JP2 (colr enum 12) is converted to RGB
// rather than mis-read as RGBA: the K channel must drive the colour, not become a
// variable alpha. The converted image is therefore fully opaque (alpha 255
// everywhere). issue205.jp2 is a Kakadu-produced CMYK image in the corpus.
func TestNonRegressionCMYK(t *testing.T) {
	root := conformanceRoot(t)
	if root == "" {
		t.Skip("ISO conformance data not found; set OPJ_DATA_ROOT (see test doc)")
	}
	data, err := os.ReadFile(filepath.Join(root, "input", "nonregression", "issue205.jp2"))
	if err != nil {
		t.Skipf("file absent: %v", err)
	}
	img, err := jp2.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CMYK decode failed: %v", err)
	}
	b := img.Bounds()
	if b.Empty() {
		t.Fatal("CMYK decode produced an empty image")
	}
	// Sample a grid: every pixel must be opaque (the CMYK→RGB path sets A=255; the
	// RGBA path would put the decoded K channel here, which varies).
	for y := b.Min.Y; y < b.Max.Y; y += 17 {
		for x := b.Min.X; x < b.Max.X; x += 17 {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xFFFF {
				t.Fatalf("CMYK pixel (%d,%d) not opaque: alpha=%d (CMYK→RGB not applied?)", x, y, a>>8)
			}
		}
	}
}
