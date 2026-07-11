package render

import (
	"image/color"
	"testing"

	"github.com/richardwilkes/pdfview/internal/device"
	"github.com/richardwilkes/pdfview/internal/gfx"
)

func newDevice(t *testing.T, w, h int) *Device {
	t.Helper()
	d, err := New(w, h)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func pixelAt(t *testing.T, pix []byte, stride, x, y int) [4]uint8 {
	t.Helper()
	i := y*stride + x*4
	return [4]uint8{pix[i], pix[i+1], pix[i+2], pix[i+3]}
}

func redPaint() device.Paint {
	return device.Paint{Color: color.NRGBA{R: 255, A: 255}, Alpha: 1}
}

func TestNewRejectsBadSizes(t *testing.T) {
	for _, size := range [][2]int{{0, 10}, {10, 0}, {-1, 5}} {
		if _, err := New(size[0], size[1]); err == nil {
			t.Errorf("size %v accepted", size)
		}
	}
}

func TestFillPathPixels(t *testing.T) {
	d := newDevice(t, 20, 20)
	var p gfx.Path
	p.Rect(5, 5, 10, 10)
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 10, 10); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("interior = %v", got)
	}
	if got := pixelAt(t, pix, stride, 2, 2); got != [4]uint8{0, 0, 0, 0} {
		t.Errorf("outside = %v (surface must start transparent)", got)
	}
}

func TestFillRespectsCTM(t *testing.T) {
	d := newDevice(t, 20, 20)
	var p gfx.Path
	p.Rect(0, 0, 5, 5)
	d.FillPath(&p, false, gfx.Translate(10, 10), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 12, 12); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("translated interior = %v", got)
	}
	if got := pixelAt(t, pix, stride, 2, 2); got[3] != 0 {
		t.Errorf("origin painted despite translation: %v", got)
	}
}

func TestAlphaPremultiplied(t *testing.T) {
	d := newDevice(t, 8, 8)
	var p gfx.Path
	p.Rect(0, 0, 8, 8)
	paint := redPaint()
	paint.Alpha = 0.5 // folded constant alpha: premul bytes must be scaled by coverage×alpha
	d.FillPath(&p, false, gfx.Identity(), paint)
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	got := pixelAt(t, pix, stride, 4, 4)
	if got[3] != 128 || got[0] != 128 || got[1] != 0 {
		t.Errorf("half-alpha premul pixel = %v", got)
	}
}

func TestClipRestrictsAndPops(t *testing.T) {
	d := newDevice(t, 20, 20)
	var clip gfx.Path
	clip.Rect(0, 0, 8, 20)
	d.ClipPath(&clip, false, gfx.Identity())
	var p gfx.Path
	p.Rect(0, 0, 20, 20)
	d.FillPath(&p, false, gfx.Identity(), redPaint())
	d.PopClip()
	// After the pop, fills reach the whole surface again.
	var p2 gfx.Path
	p2.Rect(0, 12, 20, 8)
	d.FillPath(&p2, false, gfx.Identity(), device.Paint{Color: color.NRGBA{G: 255, A: 255}, Alpha: 1})
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 4, 4); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("inside clip = %v", got)
	}
	if got := pixelAt(t, pix, stride, 15, 4); got[3] != 0 {
		t.Errorf("outside clip painted: %v", got)
	}
	if got := pixelAt(t, pix, stride, 15, 15); got != [4]uint8{0, 255, 0, 255} {
		t.Errorf("after PopClip = %v", got)
	}
}

func TestStrokeAndDash(t *testing.T) {
	d := newDevice(t, 21, 40)
	var p gfx.Path
	p.MoveTo(10.5, 0)
	p.LineTo(10.5, 40)
	sp := gfx.StrokeParams{Width: 3, MiterLimit: 10}
	d.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 10, 20); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("stroke center = %v", got)
	}
	if got := pixelAt(t, pix, stride, 2, 20); got[3] != 0 {
		t.Errorf("far from stroke painted: %v", got)
	}

	// Dashed: on for 8, off for 8 — y=4 is on, y=12 is off.
	d2 := newDevice(t, 21, 40)
	sp.Dash = []float32{8, 8}
	d2.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err = d2.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 10, 4); got[3] == 0 {
		t.Error("dash 'on' segment missing")
	}
	if got := pixelAt(t, pix, stride, 10, 12); got[3] != 0 {
		t.Errorf("dash 'off' segment painted: %v", got)
	}
}

func TestOddDashDoubles(t *testing.T) {
	// A single-entry array [4] means on 4, off 4 (PDF's odd-count repetition).
	d := newDevice(t, 5, 32)
	var p gfx.Path
	p.MoveTo(2.5, 0)
	p.LineTo(2.5, 32)
	sp := gfx.StrokeParams{Width: 2, MiterLimit: 10, Dash: []float32{4}}
	d.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 2, 2); got[3] == 0 {
		t.Error("on segment missing")
	}
	if got := pixelAt(t, pix, stride, 2, 6); got[3] != 0 {
		t.Errorf("off segment painted: %v", got)
	}
}

func TestAllZeroDashIsSolid(t *testing.T) {
	d := newDevice(t, 5, 16)
	var p gfx.Path
	p.MoveTo(2.5, 0)
	p.LineTo(2.5, 16)
	sp := gfx.StrokeParams{Width: 2, MiterLimit: 10, Dash: []float32{0, 0}}
	d.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	for _, y := range []int{2, 8, 14} {
		if got := pixelAt(t, pix, stride, 2, y); got[3] == 0 {
			t.Errorf("all-zero dash gap at y=%d", y)
		}
	}
}

func TestHairline(t *testing.T) {
	d := newDevice(t, 9, 9)
	var p gfx.Path
	p.MoveTo(0, 4.5)
	p.LineTo(9, 4.5)
	sp := gfx.StrokeParams{Width: 0, MiterLimit: 10}
	d.StrokePath(&p, &sp, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 4, 4); got[3] == 0 {
		t.Error("hairline drew nothing")
	}
}

func TestEvenOddFill(t *testing.T) {
	d := newDevice(t, 20, 20)
	var p gfx.Path
	p.Rect(0, 0, 20, 20)
	p.Rect(5, 5, 10, 10)
	d.FillPath(&p, true, gfx.Identity(), redPaint())
	pix, stride, err := d.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(t, pix, stride, 2, 2); got[3] == 0 {
		t.Error("outer ring missing")
	}
	if got := pixelAt(t, pix, stride, 10, 10); got[3] != 0 {
		t.Errorf("even-odd hole painted: %v", got)
	}
}
