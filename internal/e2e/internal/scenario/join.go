package scenario

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// JoinConversations completes named catch-up without consuming unrelated live
// observations. Empty conv lists discover the currently listed conversations.
// Saved envelopes are returned in receive order for the scenario's next reader.
func JoinConversations(t *testing.T, w Wire, restore func([]protocol.Envelope), convs ...string) {
	t.Helper()
	var saved []protocol.Envelope
	defer func() { restore(saved) }()
	deadline := time.Now().Add(30 * time.Second)
	wait := func(kind string, request uint64) protocol.Envelope {
		t.Helper()
		for {
			if !time.Now().Before(deadline) {
				t.Fatal("named catch-up deadline expired")
			}
			env := w.Next(deadline)
			if env.Type == "" {
				t.Fatal("named catch-up failed: ", env.Type)
			}
			if env.Type == protocol.TypeError {
				if env.InReplyTo != nil && *env.InReplyTo == request {
					return env
				}
				t.Fatal("uncorrelated error during catch-up")
			}
			if env.Type == kind && env.InReplyTo != nil && *env.InReplyTo == request {
				return env
			}
			saved = append(saved, env)
		}
	}
	if len(convs) == 0 {
		id := w.Send(protocol.TypeListConversations, json.RawMessage(`{}`))
		env := wait(protocol.TypeConversations, id)
		if env.Type == protocol.TypeError {
			t.Fatal("conversation discovery failed")
		}
		var rows protocol.ConversationsPayload
		if err := json.Unmarshal(env.Payload, &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows.Conversations {
			convs = append(convs, row.ID)
		}
	}
	for _, conv := range convs {
		id := w.Send(protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: conv, Limit: 100})
		var logical []byte
		var update string
		var index uint64
		for {
			env := wait(protocol.TypeThreadItems, id)
			if env.Type == protocol.TypeError {
				var failure protocol.ErrorPayload
				if json.Unmarshal(env.Payload, &failure) != nil || failure.Code != "thread.query_failed" || !failure.Retryable || !time.Now().Before(deadline) {
					t.Fatal("named catch-up rejected")
				}
				time.Sleep(20 * time.Millisecond)
				id = w.Send(protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: conv, Limit: 100})
				logical, index = nil, 0
				continue
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(env.Payload, &fields); err != nil {
				t.Fatal(err)
			}
			if raw := fields["continuation"]; len(raw) > 0 && raw[0] == '{' {
				var part protocol.ThreadUpdatePart
				if json.Unmarshal(env.Payload, &part) != nil || part.Continuation == nil || part.ConversationID != conv {
					t.Fatal("invalid catch-up fragment")
				}
				c := part.Continuation
				if index == 0 {
					update = c.UpdateID
				}
				if c.Index != index || c.Offset != uint64(len(logical)) || c.UpdateID != update {
					t.Fatal("catch-up fragment lost order or identity")
				}
				logical = append(logical, part.Data...)
				index++
				if !c.Final {
					continue
				}
				if c.TotalBytes != uint64(len(logical)) || fmt.Sprintf("%x", sha256.Sum256(append([]byte(protocol.TypeThreadItems+"\x00"), logical...))) != update {
					t.Fatal("catch-up fragment length or digest changed")
				}
				env.Payload, logical, index = logical, nil, 0
			} else if index != 0 {
				t.Fatal("incomplete catch-up assembly")
			}
			var p protocol.ThreadItemsPayload
			if json.Unmarshal(env.Payload, &p) != nil || p.ConversationID != conv || p.Epoch == "" {
				t.Fatal("invalid named catch-up reply")
			}
			if p.Final {
				break
			}
		}
	}
}
