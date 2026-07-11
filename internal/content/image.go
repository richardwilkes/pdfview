package content

import (
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/imaging"
)

// maxCachedImages caps the per-Run decoded-image cache. A page drawing more distinct images than this still
// renders them all; the extras just decode again on reuse. The real budgeted store arrives at M6 (plan.md
// internal/store); this cap only stops a hostile page from pinning unbounded decoded pixels for one Run.
const maxCachedImages = 32

// drawImageXObject implements Do for /Subtype /Image: decode (with a per-Run cache keyed by the resource's
// reference — failures are cached too, so a broken image is not re-decoded per draw) and hand the result to the
// device. Every failure path simply draws nothing: the page keeps rendering, blank where the image would be.
func (in *interp) drawImageXObject(raw cos.Object, stream *cos.Stream) {
	var img *imaging.Image
	resources := in.res[len(in.res)-1]
	if ref, isRef := raw.(cos.Ref); isRef {
		cached, seen := in.images[ref]
		if !seen {
			cached, _ = imaging.DecodeXObject(in.doc, stream, resources) //nolint:errcheck // Failures draw nothing.
			if len(in.images) < maxCachedImages {
				in.images[ref] = cached
			}
		}
		img = cached
	} else {
		img, _ = imaging.DecodeXObject(in.doc, stream, resources) //nolint:errcheck // Failures draw nothing.
	}
	in.drawImage(img)
}

// decodeInline decodes one inline image against the resource frame in scope (named /CS entries resolve through
// it).
func (in *interp) decodeInline(dict cos.Dict, payload []byte) (*imaging.Image, error) {
	return imaging.DecodeInline(in.doc, dict, payload, in.res[len(in.res)-1])
}

// drawImage emits one decoded image to the device under the current CTM: stencils tint with the fill paint
// (skipped entirely when the fill space never marks), ordinary images carry the constant fill alpha.
func (in *interp) drawImage(img *imaging.Image) {
	if img == nil || !in.gs.ctm.IsFinite() {
		return
	}
	if img.Stencil {
		if !in.marks(in.gs.fillSpace) {
			return
		}
		in.dev.FillImageMask(img, in.gs.ctm, in.fillPaint())
		return
	}
	in.dev.FillImage(img, in.gs.ctm, in.gs.fillAlpha)
}
