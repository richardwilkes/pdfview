// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

// Package store implements the document-scoped, byte-budgeted resource cache (the fz-store analog): parsed fonts,
// decoded images, and converted glyph paths register here with byte-size estimates, and least-recently-used entries
// evict when a New(maxCacheSize) budget would be exceeded. A zero budget means unlimited, matching the public API's
// maxCacheSize semantics.
//
// The store is a pure cache: eviction drops only the store's reference, never invalidates values callers still hold,
// and a budget of any size, even one too small to hold anything, changes only the amount of re-parsing work, never
// rendering output. It carries its own mutex; in the engine it additionally sits behind the document's public-API
// mutex, which serializes all rendering work per document.
package store

import (
	"container/list"
	"sync"
)

// Store is a budgeted LRU cache. Keys are arbitrary comparable values; use a dedicated key struct type per resource
// kind so kinds cannot collide (e.g. one type for font-dictionary refs, another for {font, gid} glyph paths).
type Store struct {
	entries map[any]*list.Element
	lru     *list.List // Front = most recently used.
	max     uint64     // Immutable after New, so Max reads it without mu.
	used    uint64
	mu      sync.Mutex
}

type entry struct {
	key  any
	val  any
	size uint64
}

// New returns a store with the given byte budget; 0 means unlimited.
func New(maxBytes uint64) *Store {
	return &Store{
		entries: make(map[any]*list.Element),
		lru:     list.New(),
		max:     maxBytes,
	}
}

// Get returns the cached value for key, asserted to V, and marks it most recently used. A cached nil, typed or untyped,
// is a hit that returns V's zero value (negative caching: parse failures are cacheable). A cached value that is not a V
// reports a miss so the caller re-derives and re-puts; keys are dedicated per resource kind (see Store), so a
// consistently instantiated Get never sees one.
func (s *Store) Get[V any](key any) (V, bool) {
	var zero V
	if s == nil {
		return zero, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.entries[key]
	if !ok {
		return zero, false
	}
	e, ok := el.Value.(*entry)
	if !ok { // Only Put creates elements; miss rather than panic if that ever changes.
		return zero, false
	}
	if e.val == nil { // An untyped-nil negative entry would fail the assertion below, yet it is a hit.
		s.lru.MoveToFront(el)
		return zero, true
	}
	v, ok := e.val.(V)
	if !ok { // Miss rather than panic, and leave the LRU order alone.
		return zero, false
	}
	s.lru.MoveToFront(el)
	return v, true
}

// Put caches val under key with the given byte-size estimate, evicting least-recently-used entries as needed to fit the
// budget. A value larger than the whole budget is not cached at all (matching fz-store: the caller keeps using its
// value; it just is not retained). Re-putting an existing key replaces its value and size.
func (s *Store) Put(key, val any, size uint64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.max != 0 && size > s.max {
		// Drop any existing entry for key here: the re-put branch below would evict every other entry before finally
		// evicting this oversized one.
		if el, ok := s.entries[key]; ok {
			s.lru.Remove(el)
			if e, isEntry := el.Value.(*entry); isEntry {
				s.used -= e.size
			}
			delete(s.entries, key)
		}
		return
	}
	if el, ok := s.entries[key]; ok {
		if e, isEntry := el.Value.(*entry); isEntry {
			s.used -= e.size
			e.val, e.size = val, size
			s.used += size
			s.lru.MoveToFront(el)
			s.evict()
			return
		}
	}
	el := s.lru.PushFront(&entry{key: key, val: val, size: size})
	s.entries[key] = el
	s.used += size
	s.evict()
}

// evict drops least-recently-used entries until the budget holds. Called with the lock held.
func (s *Store) evict() {
	if s.max == 0 {
		return
	}
	for s.used > s.max {
		back := s.lru.Back()
		if back == nil {
			return
		}
		s.lru.Remove(back)
		if e, isEntry := back.Value.(*entry); isEntry {
			delete(s.entries, e.key)
			s.used -= e.size
		}
	}
}

// Used returns the current total of cached byte estimates (for budget verification in tests).
func (s *Store) Used() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

// Max returns the configured budget (0 = unlimited).
func (s *Store) Max() uint64 {
	if s == nil {
		return 0
	}
	return s.max
}

// Len returns the number of cached entries (for tests).
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
