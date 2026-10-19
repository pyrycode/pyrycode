package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

type testThreadGapStore struct {
	status  thread.Snapshot
	changes func(string, uint64) thread.Observation
	compact thread.CompactQueryResult
}

func (s *testThreadGapStore) Status(conversations.ConversationID) thread.Snapshot {
	return s.status
}

func (s *testThreadGapStore) Changes(_ context.Context, _ conversations.ConversationID, epoch string, version uint64) (thread.Observation, error) {
	return s.changes(epoch, version), nil
}

func (s *testThreadGapStore) CompactChanges(context.Context, conversations.ConversationID, string, uint64) (thread.CompactQueryResult, error) {
	return s.compact, nil
}

func TestDaemonThreadGapReasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source, reason string
		unusable             bool
	}{
		{"status", "status", "state_not_usable", true},
		{"changes unavailable", "changes", "baseline_required", true},
		{"changes baseline", "changes", "baseline_required", false},
		{"compact unavailable", "compact", "compact_reset_required", true},
		{"compact reset", "compact", "compact_reset_required", false},
		{"encoding", "encode", "encode_failure", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const private = "/private/history credential=secret payload-content"
			usable := thread.Snapshot{State: thread.StateUsable, Epoch: private, Version: 2,
				Items: []thread.Item{{ID: 2, Rev: 2, Content: json.RawMessage(`{"text":"private content"}`)}}}
			w := &shadowConversation{threadEpoch: private, threadVersion: 1,
				threadPeers: map[string]bool{"tracked": true, "offline": true}}
			retired := map[string]int{}
			acquired := 0
			store := &testThreadGapStore{status: usable}
			store.changes = func(epoch string, version uint64) thread.Observation {
				if epoch == "" {
					if version != 0 || w.threadEpoch != "" || w.threadVersion != 0 || w.threadPeers != nil || len(retired) == 0 {
						t.Fatal("replacement acquired before retirement and cursor reset")
					}
					acquired++
					return thread.Observation{Snapshot: usable}
				}
				if epoch != private || version != 1 {
					t.Fatal("existing publication cursor changed")
				}
				o := thread.Observation{Snapshot: usable}
				if tc.source == "changes" {
					o.BaselineRequired = !tc.unusable
					if tc.unusable {
						o.State, o.Err = thread.StateUnavailable, errors.New(private)
					}
				}
				return o
			}
			if tc.source == "status" {
				store.status.State, store.status.Err = thread.StateUnavailable, errors.New(private)
			}
			store.compact = thread.CompactQueryResult{State: thread.StateUsable, ResetRequired: true}
			if tc.unusable {
				store.compact.State, store.compact.Err = thread.StateUnavailable, errors.New(private)
			}
			var logs bytes.Buffer
			p := &daemonThreadPublisher{store: store, log: slog.New(slog.NewJSONHandler(&logs, nil)),
				active: func(_ context.Context, id string) []relay.ActiveConn {
					if id != private {
						t.Fatal("recipient selection changed conversation")
					}
					return []relay.ActiveConn{
						{ConnID: "tracked", Interactive: true, Thread: true, View: relay.ThreadViewCompact},
						{ConnID: "query-only", Interactive: true, Thread: true, View: relay.ThreadViewCompact},
						{ConnID: "noninteractive", Thread: true}, {ConnID: "legacy", Interactive: true},
					}
				},
				retire: func(_ context.Context, peer string) error { retired[peer]++; return errors.New(private) },
				push:   func(context.Context, string, protocol.Envelope) error { t.Fatal("unexpected history push"); return nil },
			}
			if tc.source == "encode" {
				update := protocol.ThreadItemAddedPayload{ConversationID: private, Epoch: private,
					Item: protocol.ThreadItem{Content: json.RawMessage(private)}}
				if p.deliver(t.Context(), private, w, []any{update}, []string{"tracked"}) {
					t.Fatal("encoding failure did not stop delivery")
				}
			} else {
				p.publish(t.Context(), private, w)
			}
			wantRetired := map[string]int{"tracked": 1, "query-only": 1, "offline": 1}
			if tc.source == "compact" {
				// Publication prunes disconnected recipients before compact capture.
				delete(wantRetired, "offline")
			}
			if !reflect.DeepEqual(retired, wantRetired) {
				t.Fatalf("retired=%v want %v", retired, wantRetired)
			}
			if tc.source == "changes" {
				if acquired != 1 || w.threadEpoch != private || w.threadVersion != 2 {
					t.Fatal("baseline gap did not reacquire immediately")
				}
			} else {
				if w.threadEpoch != "" || w.threadVersion != 0 || w.threadPeers != nil {
					t.Fatal("gap retained cursor or recipients")
				}
				store.status = usable
				p.publish(t.Context(), private, w)
			}
			if acquired != 1 || w.threadEpoch != private || w.threadVersion != 2 || len(w.threadPeers) != 0 {
				t.Fatal("metadata-only cursor recovery changed")
			}
			decoder := json.NewDecoder(&logs)
			var record map[string]any
			if err := decoder.Decode(&record); err != nil {
				t.Fatalf("gap record missing: %v", err)
			}
			stamp, ok := record["time"].(string)
			if _, err := time.Parse(time.RFC3339Nano, stamp); !ok || err != nil {
				t.Fatal("missing normal timestamp metadata")
			}
			delete(record, "time")
			want := map[string]any{"level": "WARN", "msg": "relay: thread gap", "event": "daemon.thread.gap", "reason": tc.reason}
			if !reflect.DeepEqual(record, want) {
				t.Fatalf("gap record violates fixed-field allowlist: %v", record)
			}
			if err := decoder.Decode(&record); err != io.EOF {
				t.Fatalf("unexpected additional log record: %v", err)
			}
		})
	}
}

func TestDaemonThreadGapNoCursor(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	p := &daemonThreadPublisher{store: &testThreadGapStore{status: thread.Snapshot{State: thread.StateUnavailable}},
		log: slog.New(slog.NewJSONHandler(&logs, nil))}
	p.publish(t.Context(), testConvID, &shadowConversation{})
	if logs.Len() != 0 {
		t.Fatal("uninitialized unusable state logged a gap")
	}
}
