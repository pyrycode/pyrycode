package thread

import (
	"bytes"
	"encoding/json"
	"reflect"
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
	return Observation{Snapshot: Snapshot{State: StateUsable, Items: f.Items(), Version: f.version, LastShownVersion: f.lastShown}}
}

// FeedChanges consumes entries with Feed's partial-error semantics and returns
// detached progress from the pre-feed baseline, after private joins resolve.
func (f *Fold) FeedChanges(entries []history.Entry) (Observation, error) {
	before := f.Observe().Snapshot
	err := f.Feed(entries)
	return difference(before, f.Observe().Snapshot), err
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

// recordShown observes resolved public identities, never provisional child state.
func (f *Fold) recordShown(first, appended int) {
	var visible map[uint64]bool
	if len(f.children) > 0 {
		visible = make(map[uint64]bool)
	}
	for index, item := range f.items {
		if visible == nil && index < first && index != appended {
			continue
		}
		if f.children[index] != nil && (item.Parent == 0 || !visible[item.Parent]) {
			continue
		}
		if claimed, ok := f.messages[item.ID]; ok && claimed < 0 {
			continue
		}
		if visible != nil {
			visible[item.ID] = true
		}
		if item.Shown && (!f.published[item.ID] || index == appended) {
			f.lastShown = f.version
		}
		f.published[item.ID] = true
	}
}

func difference(before, after Snapshot) Observation {
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
			copy.Content = append(json.RawMessage(nil), item.Content...)
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
						c.TextAppend = text[len(previous):]
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
