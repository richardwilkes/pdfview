// Package imaging will decode PDF image XObjects and inline images (plan.md milestone M5): DCT, CCITT, the
// JBIG2/JPX stubs, bits-per-component unpacking, Decode arrays, Indexed expansion, image masks, and soft
// masks. The Image type is defined now so the device seam (internal/device) has its final method signatures
// from M4 on; the decoder and the type's fields land with M5.
package imaging

// Image is one decoded image resource. Until M5 lands the decoder, no producer exists and the type carries
// nothing; the device seam's image methods are declared against it so their signatures never change.
type Image struct {
	_ struct{}
}
