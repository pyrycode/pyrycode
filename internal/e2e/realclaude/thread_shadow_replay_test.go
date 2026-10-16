package realclaude

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func shadowReplay(t *testing.T, h shadowHistory, e shadowExpected, generate bool) shadowExpected {
	t.Helper()
	for i, cp := range e.Checkpoints {
		id := conversations.ConversationID(h.Conversation)
		log := history.New(t.TempDir())
		full := thread.New(h.Conversation)
		for _, entry := range h.Entries {
			if entry.ID > cp.Version {
				break
			}
			if err := full.Feed([]history.Entry{entry}); err != nil {
				t.Fatal(err)
			}
			got, err := log.AppendWithMetadata(id, entry.Type, entry.Payload, entry.TS, history.Metadata{Session: entry.Session, Shown: entry.Shown})
			if err != nil || got != entry.ID {
				t.Fatal("cannot restore retained history")
			}
		}
		store := thread.NewStore(log)
		if err := store.Load(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		var snapshot thread.Snapshot
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			snapshot = store.Snapshot(id)
			if snapshot.State == thread.StateUsable && snapshot.Version == cp.Version {
				break
			}
			if snapshot.State == thread.StateUnavailable {
				break
			}
			time.Sleep(time.Millisecond)
		}
		shutdownErr := store.Shutdown()
		if snapshot.State != thread.StateUsable || snapshot.Version != cp.Version || shutdownErr != nil {
			t.Fatal("store failed retained checkpoint")
		}
		if !reflect.DeepEqual(snapshot.Items, full.Items()) {
			t.Fatal("store and fresh replay differ")
		}
		if generate {
			e.Checkpoints[i].Items = snapshot.Items
		} else {
			current := shadowCurrentSendHandles(t, h, cp)
			current = shadowCurrentMessageTimes(t, h, current)
			if !reflect.DeepEqual(snapshot.Items, current.Items) {
				t.Fatal("retained expected rows or revisions differ")
			}
		}
	}
	return e
}

// Captures predating recorded bubble times remain immutable evidence. Add only
// the creating entry's saved timestamp when comparing their rows to today's fold.
func shadowCurrentMessageTimes(t *testing.T, h shadowHistory, cp shadowCheckpoint) shadowCheckpoint {
	t.Helper()
	cp.Items = slices.Clone(cp.Items)
	for i, item := range cp.Items {
		if item.Kind != "assistant_message" || item.ID == 0 || item.ID > uint64(len(h.Entries)) {
			continue
		}
		source := h.Entries[item.ID-1]
		if source.TS.IsZero() {
			continue
		}
		var content map[string]json.RawMessage
		if err := json.Unmarshal(item.Content, &content); err != nil {
			t.Fatal(err)
		}
		if _, present := content["ts"]; present {
			continue
		}
		stamp, err := json.Marshal(source.TS.UTC())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(item.Content, source.Payload) {
			// An untouched first delta retains its original JSON field order.
			body := bytes.TrimSpace(item.Content)
			raw := append([]byte(nil), body[:len(body)-1]...)
			raw = append(raw, `,"ts":`...)
			raw = append(raw, stamp...)
			cp.Items[i].Content = append(raw, '}')
		} else {
			content["ts"] = stamp
			cp.Items[i].Content, err = json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return cp
}

// shadowCurrentSendHandles projects historical rows to the acceptance-owned
// handle contract without rewriting the captured observations or provenance.
func shadowCurrentSendHandles(t *testing.T, h shadowHistory, cp shadowCheckpoint) shadowCheckpoint {
	t.Helper()
	cp.Items = slices.Clone(cp.Items)
	for i, item := range cp.Items {
		if item.ID == 0 || item.ID > uint64(len(h.Entries)) {
			continue
		}
		source := h.Entries[item.ID-1]
		if source.Type != "send_accepted" {
			continue
		}
		var accepted, content map[string]json.RawMessage
		if json.Unmarshal(source.Payload, &accepted) != nil || json.Unmarshal(item.Content, &content) != nil {
			t.Fatal("invalid captured send content")
		}
		hasHandle := false
		for key := range accepted {
			hasHandle = hasHandle || strings.EqualFold(key, "queued_msg_id")
		}
		if hasHandle {
			continue
		}
		for key := range content {
			if strings.EqualFold(key, "queued_msg_id") {
				delete(content, key)
				var err error
				cp.Items[i].Content, err = json.Marshal(content)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return cp
}
