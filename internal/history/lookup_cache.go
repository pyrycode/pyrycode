package history

import (
	"container/list"
	"time"
	"unsafe"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

type segmentCacheKey struct {
	convID  conversations.ConversationID
	segment uint64
	size    int64
}
type cachedSegment struct {
	key     segmentCacheKey
	entries []segEntry
	bytes   int64
}

// segmentCache is one Store-wide LRU. Store.mu guards every operation.
type segmentCache struct {
	limit, bytes int64
	lru          list.List
	items        map[segmentCacheKey]*list.Element
}

func (c *segmentCache) get(key segmentCacheKey) ([]segEntry, bool) {
	e := c.items[key]
	if e == nil {
		return nil, false
	}
	c.lru.MoveToFront(e)
	return e.Value.(cachedSegment).entries, true
}

func (c *segmentCache) put(key segmentCacheKey, entries []segEntry) {
	cost := lookupDecodedBytes(entries)
	if cost > c.limit || cost == 0 {
		return
	}
	if c.items == nil {
		c.items = make(map[segmentCacheKey]*list.Element)
	}
	for c.bytes+cost > c.limit {
		e := c.lru.Back()
		old := e.Value.(cachedSegment)
		delete(c.items, old.key)
		c.bytes -= old.bytes
		c.lru.Remove(e)
	}
	c.items[key] = c.lru.PushFront(cachedSegment{key: key, entries: entries, bytes: cost})
	c.bytes += cost
}

// lookupDecodedBytes counts retained array capacity (including Entry and offset),
// payload capacity, string bytes, provenance structs/strings and visibility bools.
// Non-UTC/non-Local timestamps reserve 256 bytes each for their decoded fixed
// offset location and its zone/transition arrays, conservatively including shared
// fixed zones. Allocator rounding, cache/index bookkeeping and process-global
// UTC/Local locations are excluded. No encoded segment buffer is retained.
func lookupDecodedBytes(entries []segEntry) int64 {
	n := int64(cap(entries)) * int64(unsafe.Sizeof(segEntry{}))
	for _, se := range entries {
		e := se.entry
		n += int64(cap(e.Payload) + len(e.Type))
		if loc := e.TS.Location(); loc != time.UTC && loc != time.Local {
			n += 256
		}
		if e.Session != nil {
			n += int64(unsafe.Sizeof(SessionProvenance{})) + int64(len(e.Session.Kind)+len(e.Session.SessionID))
		}
		if e.Shown != nil {
			n += int64(unsafe.Sizeof(bool(false)))
		}
	}
	return n
}
