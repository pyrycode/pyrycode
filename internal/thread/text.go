package thread

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type ingestionWorkCounts struct{ work int }

func (f *Fold) countIngestion(n int) {
	if f.ingestCounts != nil {
		f.ingestCounts.work += n
	}
}

// textBuffer owns inline JSON. Only the text suffix and fixed trailing fields
// move during append; geometric growth amortizes copying accumulated content.
type textBuffer struct {
	raw        []byte
	start, end int
	tail       []byte
}

type assistantText struct {
	fields        map[string]json.RawMessage
	base, derived textBuffer
	ending        json.RawMessage
	summary       strings.Builder
	space         bool
}

func (f *Fold) growText(b *textBuffer, suffix []byte) {
	n := b.end + len(suffix) + len(b.tail)
	f.countIngestion(len(suffix) + len(b.tail) + 3)
	if cap(b.raw) < n {
		capacity := max(n, 2*cap(b.raw))
		raw := make([]byte, b.end, capacity)
		copy(raw, b.raw[:b.end])
		f.countIngestion(capacity + b.end + 1)
		b.raw = raw
	}
	b.raw = append(b.raw[:b.end], suffix...)
	b.end = len(b.raw)
	b.raw = append(b.raw, b.tail...)
}

func (f *Fold) textBuffer(fields map[string]json.RawMessage, text []byte) textBuffer {
	keys := make([]string, 0, len(fields)+1)
	for key := range fields {
		if key != "text" {
			keys = append(keys, key)
		}
	}
	keys = append(keys, "text")
	slices.SortFunc(keys, func(a, b string) int { f.countIngestion(len(a) + len(b)); return strings.Compare(a, b) })
	b := textBuffer{raw: []byte{'{'}}
	for i, key := range keys {
		if i != 0 {
			b.raw = append(b.raw, ',')
		}
		name, _ := json.Marshal(key)
		b.raw = append(b.raw, name...)
		b.raw = append(b.raw, ':')
		if key == "text" {
			b.raw = append(b.raw, '"')
			b.start = len(b.raw)
			b.raw = append(b.raw, text...)
			b.end = len(b.raw)
			b.raw = append(b.raw, '"')
		} else {
			value, _ := json.Marshal(fields[key])
			f.countIngestion(len(fields[key]) + len(value))
			b.raw = append(b.raw, value...)
		}
	}
	b.raw = append(b.raw, '}')
	b.tail = append([]byte(nil), b.raw[b.end:]...)
	f.countIngestion(len(b.raw)*3 + cap(b.raw) + len(b.tail)*2 + len(keys))
	return b
}

func (f *Fold) appendSummary(t *assistantText, text string) {
	f.countIngestion(len(text))
	oldLen, oldCap := t.summary.Len(), t.summary.Cap()
	if len(text) != 0 {
		t.summary.Grow(len(text) + 1)
	}
	for _, r := range text {
		if unicode.IsSpace(r) {
			t.space = t.summary.Len() != 0
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if t.space {
			t.summary.WriteByte(' ')
			t.space = false
		}
		t.summary.WriteRune(r)
	}
	f.countIngestion(t.summary.Len() - oldLen)
	if t.summary.Cap() != oldCap {
		f.countIngestion(oldLen + t.summary.Cap())
	}
}

func (f *Fold) appendText(index int, suffix string) {
	f.countIngestion(2)
	t := f.texts[index]
	if t == nil {
		t = &assistantText{}
		content := f.items[index].Content
		var old protocol.AssistantDeltaPayload
		_ = json.Unmarshal(content, &old)
		_ = json.Unmarshal(content, &t.fields)
		encoded, _ := json.Marshal(old.Text)
		f.countIngestion(len(content)*3 + len(encoded) + len(old.Text))
		t.base = f.textBuffer(t.fields, encoded[1:len(encoded)-1])
		f.appendSummary(t, old.Text)
		f.texts[index] = t
	}
	encoded, _ := json.Marshal(suffix)
	f.countIngestion(len(suffix) + len(encoded))
	f.growText(&t.base, encoded[1:len(encoded)-1])
	if t.derived.raw != nil {
		f.growText(&t.derived, encoded[1:len(encoded)-1])
	}
	f.appendSummary(t, suffix)
	item := &f.items[index]
	item.Content, item.Summary = t.base.raw, t.summary.String()
	if item.Summary == "" {
		item.Summary = "assistant message"
	}
	f.markAffected(index)
}

// Enrichment never writes into intrinsic storage. Repeated provisional closure
// reuses the derived buffer, whose text is advanced alongside the intrinsic one.
func (f *Fold) textEnrichment(index int, key string, value any) bool {
	f.countIngestion(1)
	t := f.texts[index]
	if t == nil {
		return false
	}
	raw, _ := json.Marshal(value)
	f.countIngestion(len(raw) * 2)
	if key == "parent_ending" {
		f.countIngestion(min(len(t.ending), len(raw)) + 1)
		if t.derived.raw == nil || !bytes.Equal(t.ending, raw) {
			fields := make(map[string]json.RawMessage, len(t.fields)+1)
			for k, v := range t.fields {
				fields[k] = v
				f.countIngestion(1)
			}
			fields[key] = raw
			t.derived = f.textBuffer(fields, t.base.raw[t.base.start:t.base.end])
			t.ending = raw
		}
		f.items[index].Content = t.derived.raw
	} else {
		t.fields[key] = raw
		t.base = f.textBuffer(t.fields, t.base.raw[t.base.start:t.base.end])
		t.derived, t.ending = textBuffer{}, nil
		f.items[index].Content = t.base.raw
	}
	return true
}
