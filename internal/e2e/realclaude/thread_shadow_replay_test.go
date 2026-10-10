package realclaude

import (
	"context"
	"reflect"
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
		} else if !reflect.DeepEqual(snapshot.Items, cp.Items) {
			t.Fatal("retained expected rows or revisions differ")
		}
	}
	return e
}
