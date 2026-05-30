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
	capacity int
	mu       sync.Mutex
}

type memoryNode struct {
	key   string
	entry Entry
}

// NewMemoryCache returns a Cache backed by a bounded LRU map.
// capacity caps the total number of entries; zero or negative
// values default to [DefaultMemoryCapacity].
func NewMemoryCache(capacity int) *MemoryCache {
	if capacity <= 0 {
		capacity = DefaultMemoryCapacity
	}
	return &MemoryCache{
		capacity: capacity,
		now:      time.Now,
		order:    list.New(),
		items:    make(map[string]*list.Element, capacity),
	}
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
		c.order.Remove(elem)
		delete(c.items, key)
		return Entry{}, false, nil
	}
	c.order.MoveToFront(elem)
	return node.entry, true, nil
}

// Set inserts or refreshes the entry for key. Evicts the
// least-recently-used entry when capacity is exceeded.
func (c *MemoryCache) Set(_ context.Context, key string, entry Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.items[key]; ok {
		node, nodeOK := existing.Value.(*memoryNode)
		if !nodeOK {
			return ErrCorruptListEntry
		}
		node.entry = entry
		c.order.MoveToFront(existing)
		return nil
	}
	node := &memoryNode{key: key, entry: entry}
	elem := c.order.PushFront(node)
	c.items[key] = elem
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		oldestNode, ok := oldest.Value.(*memoryNode)
		if !ok {
			return ErrCorruptListEntry
		}
		delete(c.items, oldestNode.key)
	}
	return nil
}

// Delete removes the cache entry for key. No-op when absent.
func (c *MemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		c.order.Remove(elem)
		delete(c.items, key)
	}
	return nil
}

// DeleteMatching removes every entry whose key contains prefix as a
// substring. The transport invokes this with a scoped prefix during
// write-triggered invalidation.
func (c *MemoryCache) DeleteMatching(_ context.Context, prefix string) error {
	if prefix == "" {
		return errors.New("caching: DeleteMatching prefix is empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, elem := range c.items {
		if strings.Contains(key, prefix) {
			c.order.Remove(elem)
			delete(c.items, key)
		}
	}
	return nil
}
