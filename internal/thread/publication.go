package thread

import (
	"cmp"
	"encoding/json"
	"slices"
)

// publicationCapture belongs to the fold worker. Slots have stable fold indexes;
// nil slots are private. Published pointers and their content never change.
type publicationCapture struct {
	slots    []*Item
	recipes  map[uint64]contentRecipe
	active   map[int]bool
	version  uint64
	affected []int
	headers  map[uint64]childHeader // only this capture's affected public rows
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
	before, err := renderSharedLookup(before, c.recipes, f.source, func(id uint64) (Item, bool) {
		i, ok := slices.BinarySearchFunc(f.items, id, func(item Item, id uint64) int { return cmp.Compare(item.ID, id) })
		if !ok || i >= len(c.slots) || c.slots[i] == nil {
			return Item{}, false
		}
		return *c.slots[i], true
	})
	if err != nil {
		return nil, Observation{}, err
	}
	after, err = f.renderShared(after)
	if err != nil {
		return nil, Observation{}, err
	}
	c.headers = make(map[uint64]childHeader, len(after))
	for _, item := range after {
		c.headers[item.ID] = prepareChildHeader(item)
	}
	slots := make([]*Item, len(f.items))
	copy(slots, c.slots)
	if c.copied != nil {
		c.copied(len(c.slots))
	}
	if c.active == nil {
		c.active = make(map[int]bool)
	}
	if c.recipes == nil {
		c.recipes = make(map[uint64]contentRecipe)
	}
	for _, i := range indexes {
		delete(c.active, i)
		if f.items[i].Active {
			c.active[i] = true
		}
		slots[i] = nil
		delete(c.recipes, f.items[i].ID)
		if f.publicVisible[f.items[i].ID] {
			// Both buffers can survive provisional closure and reopening. Freeze
			// them before exposing content; the producer detaches on next append.
			f.countIngestion(1)
			if text := f.texts[i]; text != nil {
				text.base.shared, text.derived.shared = true, true
				f.countIngestion(2)
			}
			item := f.items[i]
			slots[i] = &item
			if r := f.recipes[item.ID]; r.creating != 0 || len(r.fields) != 0 {
				c.recipes[item.ID] = r
			}
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
