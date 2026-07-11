package store

import (
	"fmt"
	"sync"
	"testing"
)

type (
	keyA struct{ n int }
	keyB struct{ n int }
)

func TestLRUEviction(t *testing.T) {
	s := New(100)
	s.Put(keyA{1}, "a", 40)
	s.Put(keyA{2}, "b", 40)
	if got := s.Used(); got != 80 {
		t.Fatalf("used = %d, want 80", got)
	}
	// Touch 1 so 2 becomes the eviction victim.
	if v, ok := s.Get(keyA{1}); !ok || v != "a" {
		t.Fatalf("Get(1) = %v, %v", v, ok)
	}
	s.Put(keyA{3}, "c", 40)
	if _, ok := s.Get(keyA{2}); ok {
		t.Errorf("entry 2 not evicted")
	}
	if v, ok := s.Get(keyA{1}); !ok || v != "a" {
		t.Errorf("entry 1 wrongly evicted: %v, %v", v, ok)
	}
	if v, ok := s.Get(keyA{3}); !ok || v != "c" {
		t.Errorf("entry 3 missing: %v, %v", v, ok)
	}
	if got := s.Used(); got != 80 {
		t.Errorf("used = %d, want 80", got)
	}
}

func TestBudgetHonored(t *testing.T) {
	s := New(10)
	s.Put(keyA{1}, "big", 11) // Larger than the whole budget: not cached.
	if _, ok := s.Get(keyA{1}); ok {
		t.Errorf("oversized entry cached")
	}
	for i := range 100 {
		s.Put(keyA{i}, i, 3)
		if s.Used() > 10 {
			t.Fatalf("used %d exceeds budget after put %d", s.Used(), i)
		}
	}
	if s.Len() > 3 {
		t.Errorf("len = %d, want <= 3", s.Len())
	}
}

func TestUnlimitedNeverEvicts(t *testing.T) {
	s := New(0)
	for i := range 1000 {
		s.Put(keyA{i}, i, 1<<20)
	}
	if s.Len() != 1000 {
		t.Errorf("len = %d, want 1000", s.Len())
	}
}

func TestNilValueAndKeyKinds(t *testing.T) {
	s := New(0)
	s.Put(keyA{1}, nil, 1) // Negative caching.
	v, ok := s.Get(keyA{1})
	if !ok || v != nil {
		t.Errorf("cached nil: %v, %v", v, ok)
	}
	if _, ok = s.Get(keyB{1}); ok { // Distinct key types never collide.
		t.Errorf("keyB{1} hit keyA{1}")
	}
}

func TestReplace(t *testing.T) {
	s := New(100)
	s.Put(keyA{1}, "old", 30)
	s.Put(keyA{1}, "new", 50)
	if v, _ := s.Get(keyA{1}); v != "new" {
		t.Errorf("value = %v, want new", v)
	}
	if got := s.Used(); got != 50 {
		t.Errorf("used = %d, want 50", got)
	}
	// Replacing beyond budget evicts the entry itself (it is the only one).
	s.Put(keyA{1}, "huge", 200)
	if _, ok := s.Get(keyA{1}); ok {
		t.Errorf("oversized replacement survived")
	}
	if got := s.Used(); got != 0 {
		t.Errorf("used = %d, want 0", got)
	}
}

func TestNilStoreSafe(t *testing.T) {
	var s *Store
	s.Put(keyA{1}, "x", 1)
	if _, ok := s.Get(keyA{1}); ok {
		t.Errorf("nil store returned a value")
	}
	if s.Used() != 0 || s.Max() != 0 || s.Len() != 0 {
		t.Errorf("nil store stats nonzero")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := New(1 << 10)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				k := keyA{i % 61}
				s.Put(k, fmt.Sprintf("%d-%d", g, i), 16)
				s.Get(k)
			}
		}()
	}
	wg.Wait()
	if s.Used() > 1<<10 {
		t.Errorf("budget exceeded: %d", s.Used())
	}
}
