// Code from github.com/mububoki/jpeg2000 v1.0.0 (MIT); see internal/jpeg2000/LICENSE and PROVENANCE.md.

package codestream

import (
	"fmt"

	"github.com/richardwilkes/pdfview/internal/jpeg2000/binutil"
)

// Quantization styles (Sqcd & 0x1F), ISO/IEC 15444-1 Table A.28.
const (
	qStyleNone      = 0 // no quantization (reversible 5/3)
	qStyleDerived   = 1 // scalar derived: one step size, the rest derived
	qStyleExpounded = 2 // scalar expounded: one step size per subband
)

// QCDStepSize holds a single quantization step size entry.
// For reversible (style 0) only Expn is meaningful (Mant is 0). For scalar
// quantization the pair encodes Δ_b = 2^(R_b-Expn) * (1 + Mant/2^11).
type QCDStepSize struct {
	Expn int // exponent (epsilon_b), 5 bits
	Mant int // mantissa (mu_b), 11 bits (0 for reversible)
}

// QCD holds default quantization parameters.
type QCD struct {
	Reversible bool // true when Style == qStyleNone (lossless 5/3 path)
	Style      int  // quantization style (qStyleNone / Derived / Expounded)
	GuardBits  int  // number of guard bits (Sqcd bits 7-5)
	StepSizes  []QCDStepSize
}

// processQCD parses a QCD marker segment (default quantization), supporting the
// reversible "no quantization" style and the scalar derived/expounded styles.
func (d *Decoder) processQCD() error {
	Lqcd, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	if Lqcd < 3 {
		return fmt.Errorf("QCD: invalid length %d", Lqcd)
	}
	Sqcd, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	body := int(Lqcd) - 3
	qcd, err := d.parseQuant(Sqcd, body, "QCD")
	if err != nil {
		return err
	}

	switch d.section {
	case sectionMainHeader:
		d.header.qcd = qcd
	case sectionTilePartHeader:
		d.cur.qcd = qcd
		q := qcd
		d.tiles[d.cur.tileIndex].tileQCD = &q
		// Count this marker and its marker segment toward the current tile-part size.
		d.cur.tilePartConsumed += 2 + int(Lqcd)
	default:
		return fmt.Errorf("QCD: unexpected in section %v", d.section)
	}

	return nil
}

// parseQuant reads the SQcd/SQcc style byte (already consumed by the caller) plus
// the following `body` bytes of step sizes, shared by QCD and QCC (their payloads
// are identical). `marker` names the caller for error messages.
func (d *Decoder) parseQuant(sq byte, body int, marker string) (QCD, error) {
	style := int(sq & 0x1F)
	guardBits := int((sq >> 5) & 0x07)

	var steps []QCDStepSize
	switch style {
	case qStyleNone:
		// One byte per subband: epsilon_b in bits 7-3.
		steps = make([]QCDStepSize, body)
		for i := 0; i < body; i++ {
			b, err := binutil.ReadU8(d.r)
			if err != nil {
				return QCD{}, err
			}
			steps[i] = QCDStepSize{Expn: int(b >> 3)}
		}
	case qStyleDerived, qStyleExpounded:
		// Two bytes per entry: epsilon_b in bits 15-11, mu_b in bits 10-0.
		// Derived signals exactly one entry (sizes for other subbands are
		// derived from it); expounded signals one entry per subband.
		if body < 2 || body%2 != 0 {
			return QCD{}, fmt.Errorf("%s: invalid scalar body length %d for style %d", marker, body, style)
		}
		n := body / 2
		steps = make([]QCDStepSize, n)
		for i := 0; i < n; i++ {
			v, err := binutil.ReadU16(d.r)
			if err != nil {
				return QCD{}, err
			}
			steps[i] = QCDStepSize{Expn: int(v >> 11), Mant: int(v & 0x7FF)}
		}
	default:
		if body > 0 {
			_ = binutil.DiscardN(d.r, int64(body))
		}
		return QCD{}, fmt.Errorf("%s: unsupported quantization style %d", marker, style)
	}

	return QCD{
		Reversible: style == qStyleNone,
		Style:      style,
		GuardBits:  guardBits,
		StepSizes:  steps,
	}, nil
}

// processQCC parses a QCC marker segment: a per-component override of the default
// quantization (QCD). Its SQcc/SPqcc payload is identical to QCD; only a leading
// component index (Cqcc) is added.
func (d *Decoder) processQCC() error {
	Lqcc, err := binutil.ReadU16(d.r)
	if err != nil {
		return err
	}
	// Cqcc is 1 byte when the image has fewer than 257 components, else 2 bytes.
	cqccBytes := 1
	if len(d.header.siz.Components) >= 257 {
		cqccBytes = 2
	}
	if int(Lqcc) < 3+cqccBytes {
		return fmt.Errorf("QCC: invalid length %d", Lqcc)
	}
	var comp int
	if cqccBytes == 1 {
		b, err := binutil.ReadU8(d.r)
		if err != nil {
			return err
		}
		comp = int(b)
	} else {
		v, err := binutil.ReadU16(d.r)
		if err != nil {
			return err
		}
		comp = int(v)
	}
	if comp < 0 || comp >= len(d.header.siz.Components) {
		return fmt.Errorf("QCC: component index %d out of range", comp)
	}
	Sqcc, err := binutil.ReadU8(d.r)
	if err != nil {
		return err
	}
	body := int(Lqcc) - 3 - cqccBytes
	qcd, err := d.parseQuant(Sqcc, body, "QCC")
	if err != nil {
		return err
	}

	switch d.section {
	case sectionMainHeader:
		if d.header.qcc == nil {
			d.header.qcc = make(map[int]QCD)
		}
		d.header.qcc[comp] = qcd
	case sectionTilePartHeader:
		t := &d.tiles[d.cur.tileIndex]
		if t.tileQCC == nil {
			t.tileQCC = make(map[int]QCD)
		}
		t.tileQCC[comp] = qcd
		d.cur.tilePartConsumed += 2 + int(Lqcc)
	default:
		return fmt.Errorf("QCC: unexpected in section %v", d.section)
	}

	return nil
}
