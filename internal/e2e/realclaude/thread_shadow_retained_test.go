//go:build !e2e_realclaude

package realclaude

import (
	"encoding/json"
	"reflect"
	"testing"

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
