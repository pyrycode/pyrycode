package thread

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"unsafe"

	"github.com/pyrycode/pyrycode/internal/history"
)

type entrySource interface {
	Lookup([]uint64) ([]history.Entry, error)
}

// Recipe fields are copy-on-write; text IDs are append-only with snapshot lengths.
// Only parent_ending may carry a copied payload until its lifecycle conversion.
type contentRecipe struct {
	creating uint64
	text     []uint64
	fields   map[string]recipeField
}

type recipeField struct {
	entry uint64
	value json.RawMessage
}

func (f *Fold) referenceItem(index int, e history.Entry) {
	item := &f.items[index]
	r := contentRecipe{creating: e.ID}
	if item.Kind == "assistant_message" {
		r.text = []uint64{e.ID}
		item.Summary = ""
	}
	f.recipes[item.ID], item.Content = r, nil
}

func (f *Fold) referenceField(index int, key string, value recipeField) {
	f.markAffected(index)
	id := f.items[index].ID
	r := f.recipes[id]
	r.fields = maps.Clone(r.fields)
	if r.fields == nil {
		r.fields = make(map[string]recipeField)
	}
	r.fields[key] = value
	f.recipes[id] = r
}

func (f *Fold) setOutcome(index int, outcome *mainOutcome) {
	if f.recipes[f.items[index].ID].creating != 0 {
		f.referenceField(index, outcome.field, recipeField{entry: outcome.id})
	} else {
		f.setContent(index, outcome.field, outcome.raw)
	}
}

func newReferenceFold(conversationID string, source entrySource) *Fold {
	f := New(conversationID)
	f.source, f.recipes = source, make(map[uint64]contentRecipe)
	return f
}

func referencedKind(kind string) bool {
	switch kind {
	case "user_message", "assistant_message", "turn_end", "notice", "compaction", "session_divider":
		return true
	}
	return false
}

// Render returns detached public items. A missing or unreadable creating entry
// withholds the entire result. The owner must serialize access with Feed.
func (f *Fold) Render() ([]Item, error) {
	var items []Item
	visible := make(map[uint64]bool)
	for index, item := range f.items {
		if f.children[index] != nil && (item.Parent == 0 || !visible[item.Parent]) {
			continue
		}
		if index, ok := f.messages[item.ID]; ok && index < 0 {
			continue
		}
		items = append(items, item)
		visible[item.ID] = true
	}
	return f.render(items)
}

func (f *Fold) render(items []Item) ([]Item, error) {
	var ids []uint64
	for _, item := range items {
		if r := f.recipes[item.ID]; r.creating != 0 {
			ids = append(ids, r.creating)
			ids = append(ids, r.text...)
			for _, field := range r.fields {
				if field.entry != 0 {
					ids = append(ids, field.entry)
				}
			}
		}
	}
	entries := make(map[uint64]history.Entry, len(ids))
	if len(ids) > 0 {
		resolved, err := f.source.Lookup(ids)
		if err != nil {
			return nil, ErrUnavailable
		}
		for _, e := range resolved {
			entries[e.ID] = e
		}
	}
	for i := range items {
		item := &items[i]
		if r := f.recipes[item.ID]; r.creating != 0 {
			e, ok := entries[r.creating]
			if !ok {
				return nil, ErrUnavailable
			}
			item.Content = e.Payload
			var text strings.Builder
			for _, id := range r.text {
				e, ok := entries[id]
				var p struct {
					Text string `json:"text"`
				}
				if !ok || json.Unmarshal(e.Payload, &p) != nil {
					return nil, ErrUnavailable
				}
				text.WriteString(p.Text)
			}
			if len(r.text) > 1 || len(r.fields) > 0 {
				var fields map[string]json.RawMessage
				if json.Unmarshal(e.Payload, &fields) != nil || fields == nil {
					return nil, ErrUnavailable
				}
				if len(r.text) > 1 {
					fields["text"], _ = json.Marshal(text.String())
				}
				for key, field := range r.fields {
					raw := field.value
					if field.entry != 0 {
						e, ok := entries[field.entry]
						if !ok {
							return nil, ErrUnavailable
						}
						raw = e.Payload
					}
					if !json.Valid(raw) {
						return nil, ErrUnavailable
					}
					fields[key] = raw
				}
				var err error
				item.Content, err = json.Marshal(fields)
				if err != nil {
					return nil, ErrUnavailable
				}
			}
			if item.Kind == "assistant_message" {
				item.Summary = plainSummary(text.String())
				if item.Summary == "" {
					item.Summary = "assistant message"
				}
			} else if item.Kind == "user_message" {
				var p struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					return nil, ErrUnavailable
				}
				item.Summary = plainSummary(p.Text)
				if item.Summary == "" {
					item.Summary = "user message"
				}
			}
		}
		item.Content = append(json.RawMessage(nil), item.Content...)
	}
	return items, nil
}

const recentEntryBudget = 1 << 20

type recentEntry struct {
	entry history.Entry
	bytes int
}

// recentSource borrows only the current, increasing-ID chunk. Its fixed FIFO
// and detached entry storage fit recentEntryBudget; oversized entries are not copied.
type recentSource struct {
	lookup             func([]uint64) ([]history.Entry, error)
	chunk              []history.Entry
	recent             [256]recentEntry
	head, count, bytes int
}

func newRecentSource(lookup func([]uint64) ([]history.Entry, error)) *recentSource {
	s := &recentSource{lookup: lookup}
	s.bytes = int(unsafe.Sizeof(*s))
	return s
}

func (s *recentSource) Lookup(ids []uint64) ([]history.Entry, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	out := make([]history.Entry, 0, len(ids))
	var missing []uint64
	chunkIndex := 0
	for _, id := range ids {
		var found history.Entry
		for chunkIndex < len(s.chunk) && s.chunk[chunkIndex].ID < id {
			chunkIndex++
		}
		if chunkIndex < len(s.chunk) && s.chunk[chunkIndex].ID == id {
			found = s.chunk[chunkIndex]
		}
		if found.ID == 0 {
			for i := 0; i < s.count; i++ {
				e := s.recent[(s.head+i)%len(s.recent)].entry
				if e.ID == id {
					found = e
					break
				}
			}
		}
		if found.ID != 0 {
			out = append(out, found)
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		entries, err := s.lookup(missing)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}

func (s *recentSource) remember(entries []history.Entry) {
	for _, e := range entries {
		cost := len(e.Payload) + len(e.Type)
		if e.Session != nil {
			cost += int(unsafe.Sizeof(*e.Session)) + len(e.Session.Kind) + len(e.Session.SessionID)
		}
		if e.Shown != nil {
			cost += int(unsafe.Sizeof(*e.Shown))
		}
		if cost+int(unsafe.Sizeof(*s)) > recentEntryBudget {
			continue
		}
		for s.count == len(s.recent) || s.bytes+cost > recentEntryBudget {
			s.bytes -= s.recent[s.head].bytes
			s.recent[s.head] = recentEntry{}
			s.head = (s.head + 1) % len(s.recent)
			s.count--
		}
		payload := make(json.RawMessage, len(e.Payload))
		copy(payload, e.Payload)
		e.Payload, e.Type, e.TS = payload, strings.Clone(e.Type), e.TS.UTC()
		if e.Session != nil {
			v := *e.Session
			v.Kind, v.SessionID = strings.Clone(v.Kind), strings.Clone(v.SessionID)
			e.Session = &v
		}
		if e.Shown != nil {
			v := *e.Shown
			e.Shown = &v
		}
		s.recent[(s.head+s.count)%len(s.recent)] = recentEntry{entry: e, bytes: cost}
		s.count++
		s.bytes += cost
	}
}
