package thread

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/pyrycode/pyrycode/internal/history"
)

// Change applies to ID at PreviousRev, producing Rev. Fields use Item field
// names and replace values, including zero/null. Apply Fields, then TextAppend
// to Content.text, then Rev. Addition instead supplies the complete new item.
// Version is the consumed publication boundary, which may exceed Rev.
type Change struct {
	ID, PreviousRev, Rev, Version uint64
	Addition                      *Item
	Fields                        map[string]json.RawMessage
	TextAppend                    string
	// ResultContent is the complete resulting content for a combined field/text
	// change. It lets consumers publish one atomic replacement without changing
	// the Fields-then-TextAppend application contract. Suffix-only changes omit it.
	ResultContent json.RawMessage
}

// Observation is either a complete baseline (Items) or progress from FromVersion
// through Version. BaselineRequired carries no applicable changes: acquire a new
// baseline. Only usable observations carry epoch, versions or watermark.
type Observation struct {
	Snapshot
	FromVersion      uint64
	Changes          []Change
	BaselineRequired bool
}

// Observe returns a detached complete baseline. Fold access must be serialized.
func (f *Fold) Observe() Observation {
	items, err := f.Render()
	if err != nil {
		return Observation{Snapshot: Snapshot{State: StateUnavailable, Err: ErrUnavailable}}
	}
	return Observation{Snapshot: Snapshot{State: StateUsable, Items: items, Version: f.version, LastShownVersion: f.lastShown}}
}

// FeedChanges consumes entries with Feed's partial-error semantics and returns
// detached progress from the pre-feed baseline, after private joins resolve.
func (f *Fold) FeedChanges(entries []history.Entry) (Observation, error) {
	before := f.Observe().Snapshot
	err := f.Feed(entries)
	after := f.Observe().Snapshot
	if after.State != StateUsable {
		return Observation{Snapshot: after}, ErrUnavailable
	}
	if before.State != StateUsable {
		return Observation{Snapshot: after, BaselineRequired: true}, err
	}
	return difference(before, after), err
}

func messageText(item Item) string {
	if item.Kind != "assistant_message" && item.Kind != "user_message" {
		return ""
	}
	var content struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(item.Content, &content) // recorded content is inert; absent/non-text has no suffix
	return content.Text
}

// publicWorkCounts counts candidate discovery, ordering, lookups and updates.
// Only package tests enable it on their own serialized fold.
type publicWorkCounts struct{ work int }

func (f *Fold) countPublicWork(n int) {
	if f.publicCounts != nil {
		f.publicCounts.work += n
	}
}

// markAffected retains stable indexes once per entry for post-join consumers.
func (f *Fold) markAffected(index int) {
	f.countPublicWork(2) // registration and deduplication lookup, including duplicates
	if _, exists := f.affectedSet[index]; exists {
		return
	}
	f.countPublicWork(2) // membership insertion and dense candidate append
	f.affectedSet[index] = struct{}{}
	if len(f.affected) == cap(f.affected) {
		f.countPublicWork(len(f.affected)) // copying candidates when slice storage grows
	}
	f.affected = append(f.affected, index)
}

// recordShown observes public touches and shown appearances after private joins.
// Child repair includes affected descendants. Older-only parent edges let this
// walk use retained reachability for unrelated parents without visiting them.
func (f *Fold) recordShown() {
	slices.SortFunc(f.affected, func(a, b int) int {
		f.countPublicWork(2)
		return a - b
	})
	for _, index := range f.affected {
		f.countPublicWork(3) // item, delivery claim and child-state lookup
		item := f.items[index]
		claimed, exists := f.messages[item.ID]
		visible := true
		if f.children[index] != nil {
			f.countPublicWork(1) // parent dependency, including unresolved parents
			visible = item.Parent != 0 && f.publicVisible[item.Parent]
		}
		f.countPublicWork(2) // reachability update and retained-touch lookup
		if !visible || (exists && claimed < 0) {
			delete(f.publicVisible, item.ID)
			if f.touches[item.ID] != 0 {
				f.countPublicWork(1)
				delete(f.touches, item.ID)
				f.suppressed = f.version
			}
			continue
		}
		f.publicVisible[item.ID] = true
		if f.touches[item.ID] == 0 || item.Rev == f.version {
			f.countPublicWork(1)
			f.touches[item.ID] = f.version
		}
		if item.Shown {
			f.countPublicWork(1)
		}
		if item.Shown && (!f.published[item.ID] || index == f.appendedText) {
			f.lastShown = f.version
		}
		f.published[item.ID] = true
		f.countPublicWork(1)
	}
	// Visit only current candidates; ranging or clearing the membership map
	// would also scan storage retained from previously affected large trees.
	for _, index := range f.affected {
		f.countPublicWork(2) // candidate inspection and membership removal
		if f.captureSet != nil {
			if _, exists := f.captureSet[index]; !exists {
				f.captureSet[index] = struct{}{}
				f.captureAffected = append(f.captureAffected, index)
			}
		}
		delete(f.affectedSet, index)
	}
	f.affected = f.affected[:0]
}

func difference(before, after Snapshot) Observation {
	return itemDifference(before, after, false)
}

// Shared additions are internal immutable values; public Fold results detach them.
func itemDifference(before, after Snapshot, shared bool) Observation {
	o := Observation{Snapshot: after, FromVersion: before.Version}
	o.Items = nil
	old := make(map[uint64]Item, len(before.Items))
	for _, item := range before.Items {
		old[item.ID] = item
	}
	for _, item := range after.Items {
		prior, exists := old[item.ID]
		delete(old, item.ID)
		c := Change{ID: item.ID, PreviousRev: prior.Rev, Rev: item.Rev, Version: after.Version}
		if !exists {
			copy := item
			if !shared {
				copy.Content = append(json.RawMessage(nil), item.Content...)
			}
			c.Addition = &copy
		} else {
			left, right := reflect.ValueOf(prior), reflect.ValueOf(item)
			for i := 0; i < right.NumField(); i++ {
				name := right.Type().Field(i).Name
				if name == "ID" || name == "Rev" || reflect.DeepEqual(left.Field(i).Interface(), right.Field(i).Interface()) {
					continue
				}
				if c.Fields == nil {
					c.Fields = make(map[string]json.RawMessage)
				}
				if name == "Content" {
					previous, text := messageText(prior), messageText(item)
					if strings.HasPrefix(text, previous) && len(text) > len(previous) {
						// Retained suffixes must not pin the accumulated message allocation.
						c.TextAppend = strings.Clone(text[len(previous):])
						var fields map[string]json.RawMessage
						_ = json.Unmarshal(item.Content, &fields)
						fields["text"], _ = json.Marshal(previous)
						raw, _ := json.Marshal(fields)
						// Keep replacement only when fields other than text also changed.
						var priorFields map[string]json.RawMessage
						_ = json.Unmarshal(prior.Content, &priorFields)
						normalized, _ := json.Marshal(priorFields)
						if !bytes.Equal(raw, normalized) {
							c.Fields[name] = raw
						}
						continue
					}
				}
				c.Fields[name], _ = json.Marshal(right.Field(i).Interface()) // Item fields are JSON-encodable
			}
			if item.Rev == prior.Rev && len(c.Fields) == 0 && c.TextAppend == "" {
				continue
			}
			if len(c.Fields) > 0 && c.TextAppend != "" {
				c.ResultContent = item.Content
				if !shared {
					c.ResultContent = append(json.RawMessage(nil), item.Content...)
				}
			}
		}
		o.Changes = append(o.Changes, c)
	}
	if len(old) > 0 {
		o.BaselineRequired = true
		o.Changes = nil
	}
	return o
}

func copyObservation(o Observation) Observation {
	o.Items = copyItems(o.Items)
	changes := make([]Change, len(o.Changes))
	copy(changes, o.Changes)
	for i := range changes {
		c := &changes[i]
		c.ResultContent = append(json.RawMessage(nil), c.ResultContent...)
		if c.Addition != nil {
			item := *c.Addition
			item.Content = append(json.RawMessage(nil), item.Content...)
			c.Addition = &item
		}
		if c.Fields != nil {
			fields := make(map[string]json.RawMessage, len(c.Fields))
			for k, v := range c.Fields {
				fields[k] = append(json.RawMessage(nil), v...)
			}
			c.Fields = fields
		}
	}
	o.Changes = changes
	return o
}

func copyItems(items []Item) []Item {
	if items == nil {
		return nil
	}
	result := append([]Item(nil), items...)
	for i := range result {
		result[i].Content = append(json.RawMessage(nil), result[i].Content...)
	}
	return result
}
