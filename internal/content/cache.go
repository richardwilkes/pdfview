// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package content

import "container/list"

// lruCache is the count-bounded LRU backing the per-Run image and font caches when no budgeted store is wired. It keeps
// the most-recently-used cap entries (negative entries included — a nil value caches a decode/load failure), so a
// resource used once stays cached until cap newer distinct resources displace it.
type lruCache[K comparable, V any] struct {
	entries map[K]*list.Element
	order   *list.List // Front = most recently used.
	cap     int
}

// lruEntry is one cached value plus its key (needed to delete the map slot on eviction).
type lruEntry[K comparable, V any] struct {
	key K
	val V
}

// newLRUCache returns an lruCache holding at most capacity entries.
func newLRUCache[K comparable, V any](capacity int) *lruCache[K, V] {
	return &lruCache[K, V]{
		entries: make(map[K]*list.Element),
		order:   list.New(),
		cap:     capacity,
	}
}

// get returns the cached value for key and marks it most recently used. The bool distinguishes a cached value
// (including a nil negative entry) from a miss.
func (c *lruCache[K, V]) get(key K) (V, bool) {
	if el, ok := c.entries[key]; ok {
		if e, isEntry := el.Value.(*lruEntry[K, V]); isEntry { // Always true: only put creates elements.
			c.order.MoveToFront(el)
			return e.val, true
		}
	}
	var zero V
	return zero, false
}

// put inserts or updates key, evicting the least-recently-used entry when the capacity would be exceeded.
func (c *lruCache[K, V]) put(key K, val V) {
	if el, ok := c.entries[key]; ok {
		if e, isEntry := el.Value.(*lruEntry[K, V]); isEntry {
			e.val = val
			c.order.MoveToFront(el)
			return
		}
	}
	c.entries[key] = c.order.PushFront(&lruEntry[K, V]{key: key, val: val})
	for c.order.Len() > c.cap {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.order.Remove(back)
		if e, isEntry := back.Value.(*lruEntry[K, V]); isEntry {
			delete(c.entries, e.key)
		}
	}
}
