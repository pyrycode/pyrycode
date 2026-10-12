package relay

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestThreadDeliverySuppliedLiveState(t *testing.T) {
	for _, source := range []string{"push", "reply"} {
		t.Run(source, func(t *testing.T) {
			p, _, _, release := testThreadHeld(t, func(c *V2SessionConfig) {
				c.ThreadLiveState = func() func() (LiveState, bool) {
					return func() (LiveState, bool) { return LiveState{}, false }
				}
			})
			// The provider suppresses unsourced legacy state, while supplied state
			// must retain its source-authorized clear/fresh delivery contract.
			if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{"conversation_id":"known"}`)}); err != nil {
				t.Fatal(err)
			}
			var readings []LiveState
			for _, typ := range []string{protocol.TypeTurnState, protocol.TypeModalShown, protocol.TypeQuestionShown, protocol.TypeSessionSettings} {
				r := liveReading(typ, "known", "reading", `"source-session"`, 1, 1)
				if source == "reply" {
					reply := uint64(77)
					r.Envelope.InReplyTo = &reply
				}
				if err := p.mgr.PushLiveState(t.Context(), p.conn, r); err != nil {
					t.Fatal(err)
				}
				readings = append(readings, r)
			}
			testThreadQueued(t, p, 1+2*len(readings))
			if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeWorkspaceUpdated, Payload: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			testThreadQueued(t, p, 2+2*len(readings))
			release()
			frames := waitForEnvelopes(t, p.rec, 3+2*len(readings))
			if len(frames) != 3+2*len(readings) {
				t.Fatalf("unexpected output count: %d", len(frames))
			}
			var got []protocol.Envelope
			for i, frame := range frames[1:] {
				var env protocol.Envelope
				if err := json.Unmarshal(threadPlain(t, frame, p.recv), &env); err != nil {
					t.Fatal(err)
				}
				if env.ID != uint64(i+2) {
					t.Fatalf("sender order: %+v", env)
				}
				got = append(got, env)
			}
			if got[0].Type != protocol.TypeAppCancelled || got[len(got)-1].Type != protocol.TypeWorkspaceUpdated {
				t.Fatal("held output or trailing barrier reordered")
			}
			for i, r := range readings {
				clear, fresh := got[1+2*i], got[2+2*i]
				if clear.Type != r.Envelope.Type || !clear.SessionStateCleared || string(clear.Payload) != `{}` || clear.InReplyTo != nil ||
					fresh.Type != r.Envelope.Type || fresh.SessionStateCleared || string(fresh.Payload) != string(r.Envelope.Payload) || !reflect.DeepEqual(fresh.InReplyTo, r.Envelope.InReplyTo) {
					t.Fatalf("clear/fresh changed for %s: %+v %+v", r.Envelope.Type, clear, fresh)
				}
				for _, env := range []protocol.Envelope{clear, fresh} {
					if string(env.ConversationID) != `"known"` || string(env.SessionID) != `"source-session"` || env.EventID != nil || env.HistoryEntryID != nil {
						t.Fatalf("source metadata changed: %+v", env)
					}
				}
			}
		})
	}
}
