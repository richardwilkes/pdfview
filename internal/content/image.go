package content

import (
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/imaging"
)

// maxCachedImages caps the per-Run decoded-image cache used when no budgeted store is wired. A page drawing
// more distinct images than this still renders them all; the extras just decode again on reuse. With a store,
// its byte budget replaces this cap.
const maxCachedImages = 32

// drawImageXObject implements Do for /Subtype /Image: decode (cached by the resource's reference — in the
// document's budgeted store when wired, else per Run; failures are cached too, so a broken image is not
// re-decoded per draw) and hand the result to the device. Every failure path simply draws nothing: the page
// keeps rendering, blank where the image would be.
func (in *interp) drawImageXObject(raw cos.Object, stream *cos.Stream) {
	var img *imaging.Image
	resources := in.res[len(in.res)-1]
	if ref, isRef := raw.(cos.Ref); isRef {
		img = in.cachedImage(ref, stream, resources)
	} else {
		img, _ = imaging.DecodeXObject(in.doc, stream, resources) //nolint:errcheck // Failures draw nothing.
	}
	in.drawImage(img)
}

// cachedImage decodes an image XObject through the active cache layer.
func (in *interp) cachedImage(ref cos.Ref, stream *cos.Stream, resources cos.Dict) *imaging.Image {
	if in.st != nil {
		if v, hit := in.st.Get(imageKey{ref: ref}); hit {
			if img, isImg := v.(*imaging.Image); isImg {
				return img
			}
			return nil // Cached failure (negative entry).
		}
		img, _ := imaging.DecodeXObject(in.doc, stream, resources) //nolint:errcheck // Failures draw nothing.
		in.st.Put(imageKey{ref: ref}, img, imageSize(img))
		return img
	}
	cached, seen := in.images[ref]
	if !seen {
		cached, _ = imaging.DecodeXObject(in.doc, stream, resources) //nolint:errcheck // Failures draw nothing.
		if len(in.images) < maxCachedImages {
			in.images[ref] = cached
		}
	}
	return cached
}

// imageSize estimates a decoded image's cache footprint.
func imageSize(img *imaging.Image) uint64 {
	if img == nil {
		return 64
	}
	return uint64(len(img.Pix)) + 64
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
