//go:build !e2e_realclaude

package realclaude

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// TestThreadShadowRetainedEvidence requires the committed observed pair, pinned
// to its counted live-gate report, and replays every checkpoint through a fresh
// store and fold. It needs no credential.
func TestThreadShadowRetainedEvidence(t *testing.T) {
	h, e, err := shadowReadPair("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if err := shadowValidatePair(h, e, true); err != nil {
		t.Fatal(err)
	}
	shadowReplay(t, h, e, false)
}

func TestThreadShadowMessageTimeProjection(t *testing.T) {
	at := time.Date(2026, 10, 16, 6, 30, 0, 0, time.UTC)
	raw := json.RawMessage(`{"turn_id":"t","text":"first"}`)
	h := shadowHistory{Entries: []history.Entry{{ID: 1, TS: at, Payload: raw}}}
	for _, content := range []json.RawMessage{raw, json.RawMessage(`{"text":"first second","turn_id":"t"}`),
		json.RawMessage(`{"text":"captured date","ts":"2026-10-15T00:00:00Z"}`)} {
		item := thread.Item{ID: 1, Kind: "assistant_message", Content: content}
		cp := shadowCheckpoint{Items: []thread.Item{item}}
		got := shadowCurrentMessageTimes(t, h, cp)
		var original, projected map[string]json.RawMessage
		if err := json.Unmarshal(content, &original); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(got.Items[0].Content, &projected); err != nil {
			t.Fatal(err)
		}
		if _, present := original["ts"]; present {
			if !reflect.DeepEqual(got.Items[0], item) {
				t.Fatal("rewrote an observed date")
			}
		} else if string(projected["ts"]) != `"2026-10-16T06:30:00Z"` {
			t.Fatal("projection lost the recorded time")
		}
		delete(original, "ts")
		delete(projected, "ts")
		if !reflect.DeepEqual(original, projected) || !reflect.DeepEqual(cp.Items[0], item) {
			t.Fatal("projection changed historical content or captured evidence")
		}
	}
}

func TestThreadShadowQueueHandleProjection(t *testing.T) {
	for _, tc := range []struct {
		acceptance string
		present    bool
	}{
		{`{"text":"legacy"}`, false},
		{`{"text":"queued","queued_msg_id":7}`, true},
	} {
		t.Run(tc.acceptance, func(t *testing.T) {
			h := shadowHistory{Entries: []history.Entry{{ID: 1, Type: "send_accepted", Payload: json.RawMessage(tc.acceptance)}}}
			item := thread.Item{ID: 1, Status: "delivered", Content: json.RawMessage(`{"text":"delivered","queued_msg_id":7}`)}
			cp := shadowCheckpoint{Items: []thread.Item{item}}
			got := shadowCurrentSendHandles(t, h, cp)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(got.Items[0].Content, &fields); err != nil {
				t.Fatal(err)
			}
			_, present := fields["queued_msg_id"]
			if present != tc.present || !reflect.DeepEqual(cp.Items[0], item) {
				t.Fatal("projection changed historical evidence or handle ownership")
			}
		})
	}
}
