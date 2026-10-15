package thread

import (
	"encoding/json"
	"slices"
)

// publicationCapture belongs to the fold worker. Slots have stable fold indexes;
// nil slots are private. Published pointers and their content never change.
type publicationCapture struct {
	slots    []*Item
	active   map[int]bool
	version  uint64
	affected []int
	copied   func(int)
}

func (c *publicationCapture) capture(f *Fold) ([]*Item, Observation, error) {
	indexes := f.captureAffected
	c.affected = append(c.affected[:0], indexes...)
	slices.Sort(indexes)
	var before, after []Item
	for _, i := range indexes {
		if i < len(c.slots) && c.slots[i] != nil {
			before = append(before, *c.slots[i])
		}
		if f.publicVisible[f.items[i].ID] {
			after = append(after, f.items[i])
		}
	}
	after, err := f.renderShared(after)
	if err != nil {
		return nil, Observation{}, err
	}
	slots := make([]*Item, len(f.items))
	copy(slots, c.slots)
	if c.copied != nil {
		c.copied(len(c.slots))
	}
	if c.active == nil {
		c.active = make(map[int]bool)
	}
	next := 0
	for _, i := range indexes {
		delete(c.active, i)
		if f.items[i].Active {
			c.active[i] = true
		}
		slots[i] = nil
		if f.publicVisible[f.items[i].ID] {
			// Both buffers can survive provisional closure and reopening. Freeze
			// them before exposing content; the producer detaches on next append.
			f.countIngestion(1)
			if text := f.texts[i]; text != nil {
				text.base.shared, text.derived.shared = true, true
				f.countIngestion(2)
			}
			item := after[next]
			slots[i] = &item
			next++
		}
	}
	snapshot := Snapshot{State: StateUsable, Version: f.version, LastShownVersion: f.lastShown, Active: len(c.active) != 0}
	old, current := snapshot, snapshot
	old.Version, old.Items, current.Items = c.version, before, after
	changes := itemDifference(old, current, true)
	c.slots, c.version = slots, f.version
	for _, i := range indexes {
		delete(f.captureSet, i)
	}
	f.captureAffected = f.captureAffected[:0]
	return slots, changes, nil
}

func detachPublication(items []*Item) []Item {
	var result []Item
	for _, item := range items {
		if item == nil {
			continue
		}
		if result == nil {
			result = make([]Item, 0, len(items))
		}
		copy := *item
		copy.Content = append(json.RawMessage(nil), item.Content...)
		result = append(result, copy)
	}
	return result
}
