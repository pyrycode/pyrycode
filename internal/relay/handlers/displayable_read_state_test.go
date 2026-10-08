package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestConversationReadDisplayableRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		held    uint64
		content bool
		upTo    string
	}{
		{"exact displayed ID", 0, true, "1"},
		{"overshoot", 0, true, "18446744073709551615"},
		{"higher held mark", 3, true, "18446744073709551615"},
		{"status only", 0, false, "18446744073709551615"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, path := newMarkReadReg(t, tc.held)
			hist := history.New(filepath.Dir(path))
			appendEntry := func(typ string) uint64 {
				t.Helper()
				id, err := hist.Append(markReadTargetID, typ, []byte(`{}`), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			var latest uint64
			if tc.content {
				latest = appendEntry("turn_end")
			}
			for _, typ := range []string{"turn_state", "stall", "api_retry", "compacting", "session_transition"} {
				appendEntry(typ)
			}
			if err := reg.Save(path); err != nil {
				t.Fatal(err)
			}
			ann := &recordingAnnouncer{}
			h := MarkConversationRead(reg, hist, path, ann.announce, testLogger(t))
			env, _ := runMarkRead(t, h, markReadPayload(tc.upTo))
			var updated protocol.ConversationUpdatedPayload
			if err := json.Unmarshal(env.Payload, &updated); err != nil {
				t.Fatal(err)
			}
			wantHeld := max(tc.held, latest)
			if env.Type != protocol.TypeConversationUpdated || updated.ReadUpTo != wantHeld || storedMark(t, reg) != wantHeld {
				t.Fatalf("mark reply = %+v, stored = %d; want %d", env, storedMark(t, reg), wantHeld)
			}
			wantPushes := 0
			if wantHeld > tc.held {
				wantPushes = 1
			}
			if got := ann.records(); len(got) != wantPushes || (len(got) > 0 && got[0].ReadUpTo != wantHeld) {
				t.Fatalf("pushes = %+v; want %d with mark %d", got, wantPushes, wantHeld)
			}
			var err error
			reg, err = conversations.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			hist = history.New(filepath.Dir(path))
			list := func(wantLatest uint64, unread bool) {
				t.Helper()
				c, recv := newListConvConn(t)
				if err := ListConversations(reg, hist)(context.Background(), c, makeListConversationsRequest(t, 91)); err != nil {
					t.Fatal(err)
				}
				env, rows := decodeConversationsResponse(t, recv())
				if env.InReplyTo == nil || *env.InReplyTo != 91 || len(rows.Conversations) != 1 {
					t.Fatalf("list reply = %+v, %+v", env, rows)
				}
				row := rows.Conversations[0]
				if row.ReadUpTo != wantHeld || row.LatestEntryID != wantLatest || (row.LatestEntryID > row.ReadUpTo) != unread {
					t.Fatalf("list row = %+v; want held %d, latest %d, unread %v", row, wantHeld, wantLatest, unread)
				}
			}
			list(latest, false)
			fresh := appendEntry("unknown_future_type")
			appendEntry("turn_state")
			list(fresh, true)
			h = MarkConversationRead(reg, hist, path, ann.announce, testLogger(t))
			runMarkRead(t, h, markReadPayload(fmt.Sprint(fresh)))
			wantHeld = fresh
			list(fresh, false)
		})
	}
}
