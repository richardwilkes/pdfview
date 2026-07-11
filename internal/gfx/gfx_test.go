package gfx

import (
	"math"
	"testing"
)

func TestMatrixMulApply(t *testing.T) {
	// [x y 1]·M1·M2 must equal applying M1 then M2.
	m1 := Matrix{A: 2, B: 0.5, C: -1, D: 3, E: 10, F: -20}
	m2 := Matrix{A: 0, B: 1, C: -1, D: 0, E: 5, F: 7}
	combined := m1.Mul(m2)
	pts := []Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}, {X: -3.5, Y: 12.25}}
	for _, p := range pts {
		step := m2.Apply(m1.Apply(p))
		direct := combined.Apply(p)
		if math.Abs(float64(step.X-direct.X)) > 1e-4 || math.Abs(float64(step.Y-direct.Y)) > 1e-4 {
			t.Errorf("point %v: stepwise %v != combined %v", p, step, direct)
		}
	}
}

func TestMatrixIdentityTranslateScale(t *testing.T) {
	p := Point{X: 3, Y: 4}
	if got := Identity().Apply(p); got != p {
		t.Errorf("identity moved %v to %v", p, got)
	}
	if got := Translate(2, -1).Apply(p); got != (Point{X: 5, Y: 3}) {
		t.Errorf("translate: %v", got)
	}
	if got := Scale(2, 3).Apply(p); got != (Point{X: 6, Y: 12}) {
		t.Errorf("scale: %v", got)
	}
	// PDF cm composition: translate then scale must scale the translation.
	m := Translate(2, 0).Mul(Scale(10, 10))
	if got := m.Apply(Point{}); got != (Point{X: 20, Y: 0}) {
		t.Errorf("translate-then-scale: %v", got)
	}
}

func TestMatrixIsFinite(t *testing.T) {
	if !Identity().IsFinite() {
		t.Error("identity reported non-finite")
	}
	bad := Matrix{A: 1, D: float32(math.NaN())}
	if bad.IsFinite() {
		t.Error("NaN matrix reported finite")
	}
	bad = Matrix{A: float32(math.Inf(1)), D: 1}
	if bad.IsFinite() {
		t.Error("Inf matrix reported finite")
	}
}

func TestPathBuilding(t *testing.T) {
	var p Path
	p.MoveTo(1, 2)
	p.LineTo(3, 4)
	p.QuadTo(5, 6, 7, 8)
	p.CubicTo(9, 10, 11, 12, 13, 14)
	p.Close()
	wantVerbs := []PathVerb{MoveTo, LineTo, QuadTo, CubicTo, ClosePath}
	if len(p.Verbs) != len(wantVerbs) {
		t.Fatalf("got %d verbs", len(p.Verbs))
	}
	for i, v := range wantVerbs {
		if p.Verbs[i] != v {
			t.Errorf("verb %d = %d, want %d", i, p.Verbs[i], v)
		}
	}
	if len(p.Points) != 7 { // 1 (move) + 1 (line) + 2 (quad) + 3 (cubic)
		t.Fatalf("got %d points", len(p.Points))
	}
	if p.IsEmpty() {
		t.Error("built path reported empty")
	}
}

func TestPathRect(t *testing.T) {
	var p Path
	p.Rect(10, 20, 30, 40)
	wantVerbs := []PathVerb{MoveTo, LineTo, LineTo, LineTo, ClosePath}
	if len(p.Verbs) != len(wantVerbs) {
		t.Fatalf("got %d verbs", len(p.Verbs))
	}
	wantPts := []Point{{X: 10, Y: 20}, {X: 40, Y: 20}, {X: 40, Y: 60}, {X: 10, Y: 60}}
	for i, want := range wantPts {
		if p.Points[i] != want {
			t.Errorf("point %d = %v, want %v", i, p.Points[i], want)
		}
	}
}

func TestPathCloneAndTransform(t *testing.T) {
	var p Path
	p.MoveTo(1, 1)
	p.LineTo(2, 2)
	clone := p.Clone()
	clone.Transform(Scale(10, 10))
	if p.Points[1] != (Point{X: 2, Y: 2}) {
		t.Error("transforming the clone mutated the original")
	}
	if clone.Points[1] != (Point{X: 20, Y: 20}) {
		t.Errorf("transform: %v", clone.Points[1])
	}
}

func TestRectNormalizeEmpty(t *testing.T) {
	r := Rect{X0: 5, Y0: 9, X1: 1, Y1: 2}.Normalize()
	if r != (Rect{X0: 1, Y0: 2, X1: 5, Y1: 9}) {
		t.Errorf("normalize: %v", r)
	}
	if r.IsEmpty() {
		t.Error("non-empty rect reported empty")
	}
	if !(Rect{X0: 1, Y0: 1, X1: 1, Y1: 5}).IsEmpty() {
		t.Error("zero-width rect reported non-empty")
	}
}

func TestStrokeParamsClone(t *testing.T) {
	sp := StrokeParams{Width: 2, Dash: []float32{6, 3}, DashPhase: 1}
	clone := sp.Clone()
	clone.Dash[0] = 99
	if sp.Dash[0] != 6 {
		t.Error("clone shares the dash slice")
	}
}
