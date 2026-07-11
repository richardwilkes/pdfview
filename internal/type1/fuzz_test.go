package type1

import (
	"testing"
)

// FuzzType1 drives the full Type 1 surface with arbitrary bytes: container splitting (raw/PFA/PFB), eexec
// decryption, clear-text and private-dict scanning, and charstring interpretation (including seac, flex, and
// the othersubr protocol) for every parsed glyph. Nothing may panic and nothing may loop: the scanner always
// advances and the interpreter's stacks and counts are capped (plan.md invariant 6).
func FuzzType1(f *testing.F) {
	f.Add(buildTestFont(false, false, false))
	f.Add(buildTestFont(true, false, false))
	f.Add(buildTestFont(false, true, true))
	f.Add([]byte("%!PS-AdobeFont-1.0\n/Encoding StandardEncoding def\neexec\nJUNKJUNKJUNKJUNK"))
	f.Add([]byte{0x80, 0x01, 4, 0, 0, 0, 'e', 'e', 'x', 'e', 0x80, 0x03})
	f.Fuzz(func(_ *testing.T, raw []byte) {
		fnt, err := Parse(raw)
		if err != nil {
			return
		}
		fnt.StdEnc = stdEncForSeac()
		n := 0
		for name := range fnt.CharStrings {
			if _, _, glyphErr := fnt.Glyph(name); glyphErr != nil {
				// A hostile charstring degrading to an error (never a panic) is exactly the contract.
				continue
			}
			fnt.Advance(name)
			n++
			if n >= 64 { // Glyph count is capped far higher; bound per-input work instead.
				break
			}
		}
	})
}
