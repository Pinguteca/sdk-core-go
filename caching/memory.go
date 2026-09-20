package caching

import (
	"container/list"
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// DefaultMemoryCapacity is the maximum number of entries the
// in-memory cache holds before LRU eviction kicks in. Realistic SDK
// consumers typically size in the hundreds of distinct request/
// method/tenant combinations; 1024 covers that comfortably.
const DefaultMemoryCapacity = 1024

// DefaultMemoryMaxBytes bounds the total payload the in-memory cache
// retains. An entry-count limit alone is not a memory bound: with only
// a count, the ceiling is capacity multiplied by whatever the upstream
// server chose to send, which leaves the server deciding how much
// memory the client holds.
const DefaultMemoryMaxBytes int64 = 64 << 20 // 64 MiB

// ErrCorruptListEntry is returned defensively when the internal LRU
// list holds a node that is not a *memoryNode. The map and list are
// populated in tandem; if this fires something else corrupted the
// cache state.
var ErrCorruptListEntry = errors.New("caching: corrupt list entry")

// MemoryCache is an in-process LRU+TTL [Cache]. Suitable for
// single-replica deployments and tests. Multi-replica deployments
// should use a shared cache implementation (Redis adapter, etc.)
// because in-memory write-triggered invalidation only clears the
// writing replica.
type MemoryCache struct {
	now      func() time.Time
	order    *list.List
	items    map[string]*list.Element
	maxBytes int64
	bytes    int64
	capacity int
	mu       sync.Mutex
}

type memoryNode struct {
	key   string
	entry Entry
}

// NewMemoryCache returns a Cache backed by a bounded LRU map.
// capacity caps the total number of entries; zero or negative
// values default to [DefaultMemoryCapacity]. The retained payload is
// bounded by [DefaultMemoryMaxBytes]; use [NewMemoryCacheWithLimits]
// to choose a different byte budget.
func NewMemoryCache(capacity int) *MemoryCache {
	return NewMemoryCacheWithLimits(capacity, DefaultMemoryMaxBytes)
}

// NewMemoryCacheWithLimits returns a Cache bounded by both an entry
// count and a total payload size. Eviction runs until both hold, so
// neither a flood of small entries nor a handful of large ones can
// grow the cache past its budget. Zero or negative arguments select
// [DefaultMemoryCapacity] and [DefaultMemoryMaxBytes].
func NewMemoryCacheWithLimits(capacity int, maxBytes int64) *MemoryCache {
	if capacity <= 0 {
		capacity = DefaultMemoryCapacity
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMemoryMaxBytes
	}
	return &MemoryCache{
		capacity: capacity,
		maxBytes: maxBytes,
		now:      time.Now,
		order:    list.New(),
		items:    make(map[string]*list.Element, capacity),
	}
}

// entrySize is the accounted cost of holding key and entry. Body bytes
// dominate; the key and status/timestamp fields are counted so that a
// flood of empty-bodied entries still registers against the budget.
func entrySize(key string, entry Entry) int64 {
	size := int64(len(key)) + int64(len(entry.Body)) + int64(len(entry.ETag))
	for name, values := range entry.Headers {
		size += int64(len(name))
		for _, v := range values {
			size += int64(len(v))
		}
	}
	return size
}

// Get returns the cached entry for key if present and within the
// SWR hard deadline. Past-deadline entries are evicted inline.
func (c *MemoryCache) Get(_ context.Context, key string) (Entry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return Entry{}, false, nil
	}
	node, ok := elem.Value.(*memoryNode)
	if !ok {
		return Entry{}, false, ErrCorruptListEntry
	}
	now := c.now()
	hardDeadline := node.entry.Created.Add(node.entry.TTL + node.entry.SWR)
	if now.After(hardDeadline) {
		c.evictElement(elem, key, node.entry)
		return Entry{}, false, nil
	}
	c.order.MoveToFront(elem)
	return node.entry, true, nil
}

// Set inserts or refreshes the entry for key. Evicts
// least-recently-used entries until both the entry count and the byte
// budget are satisfied.
func (c *MemoryCache) Set(_ context.Context, key string, entry Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.items[key]; ok {
		node, nodeOK := existing.Value.(*memoryNode)
		if !nodeOK {
			return ErrCorruptListEntry
		}
		c.bytes += entrySize(key, entry) - entrySize(key, node.entry)
		node.entry = entry
		c.order.MoveToFront(existing)
		return c.evictToBudget()
	}
	node := &memoryNode{key: key, entry: entry}
	elem := c.order.PushFront(node)
	c.items[key] = elem
	c.bytes += entrySize(key, entry)
	return c.evictToBudget()
}

// evictToBudget drops least-recently-used entries until the cache is
// within both bounds. The last remaining entry is kept even when it
// alone exceeds maxBytes: evicting it would leave the cache unable to
// serve the entry the caller just stored, and the per-response cap in
// the transport already bounds a single body.
func (c *MemoryCache) evictToBudget() error {
	for c.order.Len() > c.capacity || (c.bytes > c.maxBytes && c.order.Len() > 1) {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		oldestNode, ok := oldest.Value.(*memoryNode)
		if !ok {
			return ErrCorruptListEntry
		}
		c.evictElement(oldest, oldestNode.key, oldestNode.entry)
	}
	return nil
}

// evictElement removes one element from both the list and the map and
// releases its accounted bytes. Callers hold c.mu.
func (c *MemoryCache) evictElement(elem *list.Element, key string, entry Entry) {
	c.order.Remove(elem)
	delete(c.items, key)
	c.bytes -= entrySize(key, entry)
	if c.bytes < 0 {
		c.bytes = 0
	}
}

// Delete removes the cache entry for key. No-op when absent.
func (c *MemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return nil
	}
	node, ok := elem.Value.(*memoryNode)
	if !ok {
		return ErrCorruptListEntry
	}
	c.evictElement(elem, key, node.entry)
	return nil
}

// DeleteMatching removes every entry whose key begins with prefix. The
// transport invokes this with a scoped prefix during write-triggered
// invalidation.
//
// The match is anchored at the start of the key. An unanchored match
// would evict across tenants whenever one scope is a suffix of another
// (`42:` occurs inside `142:...`).
func (c *MemoryCache) DeleteMatching(_ context.Context, prefix string) error {
	if prefix == "" {
		return errors.New("caching: DeleteMatching prefix is empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, elem := range c.items {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		node, ok := elem.Value.(*memoryNode)
		if !ok {
			return ErrCorruptListEntry
		}
		c.evictElement(elem, key, node.entry)
	}
	return nil
}
