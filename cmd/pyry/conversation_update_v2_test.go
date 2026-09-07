package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// The row one announcement carries, shared by every case below. The path and
// the name are deliberately distinctive so the never-log assertions cannot pass
// against a buffer that merely never said "cwd" — a substring like "project"
// appears in half the daemon's vocabulary, and these two do not.
const (
	updateConvID = "77777777-7777-4777-8777-777777777777"
	updateCwd    = "/home/op/pyry-2156-announced-workspace"
	updateName   = "pyry-2156-announced-label"
)

// updatePayload is the announced record. A function rather than a package var:
// Name is a pointer, and a shared one would let a mutating test reach every
// other case.
func updatePayload() protocol.ConversationUpdatedPayload {
	name := updateName
	return protocol.ConversationUpdatedPayload{
		ID:         updateConvID,
		IsPromoted: true,
		IsArchived: false,
		Name:       &name,
		Cwd:        updateCwd,
		LastUsedAt: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
	}
}

// decodeUpdate decodes one recorded push as a conversation_updated payload,
// failing the test if it is any other frame.
//
// The conversation_created check is AC-2 and is not redundant with the equality
// below it: that is the frame desktop treats as the reply to its OWN create, so
// it navigates into the thread on receipt. A host-side `pyry channel new` that
// minted one would steal the screen of every client that happened to be paired.
// Spelled out here so a future edit that reaches for the neighbouring constant
// gets told why, rather than just "want conversation_updated".
func decodeUpdate(t *testing.T, p recordedPush) protocol.ConversationUpdatedPayload {
	t.Helper()
	if p.env.Type == protocol.TypeConversationCreated {
		t.Fatalf("pushed envelope type = %q; a host-side create must never mint that frame — "+
			"desktop reads it as the reply to its own create and navigates into the thread, "+
			"so this would steal the screen of every paired client", p.env.Type)
	}
	if p.env.Type != protocol.TypeConversationUpdated {
		t.Fatalf("pushed envelope type = %q, want %q", p.env.Type, protocol.TypeConversationUpdated)
	}
	var got protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(p.env.Payload, &got); err != nil {
		t.Fatalf("decode conversation_updated payload: %v", err)
	}
	return got
}

// AC-1: an announcement reaches every interactive-capable conn and no other.
// The #607 capability gate is the whole of the table's second and third rows —
// a non-interactive conn is attached and authenticated, so nothing but the flag
// separates it from a recipient.
func TestConversationUpdateEmitterV2_Announce_GatesOnInteractive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		conns []relay.ActiveConn
		want  []string // conn ids that must receive the record, in fan-out order
	}{
		{
			name:  "one interactive conn",
			conns: []relay.ActiveConn{{ConnID: "c1", Interactive: true}},
			want:  []string{"c1"},
		},
		{
			name:  "a non-interactive conn receives nothing",
			conns: []relay.ActiveConn{{ConnID: "c1", Interactive: false}},
			want:  nil,
		},
		{
			name: "mixed set delivers only to the interactive members",
			conns: []relay.ActiveConn{
				{ConnID: "c1", Interactive: true},
				{ConnID: "c2", Interactive: false},
				{ConnID: "c3", Interactive: true},
			},
			want: []string{"c1", "c3"},
		},
		{
			name:  "no conns at all",
			conns: nil,
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{tc.conns}}
			var buf bytes.Buffer
			e := newConversationUpdateEmitterV2(bcast, context.Background(), debugLogger(&buf))

			e.announce(updatePayload())

			var got []string
			for _, p := range bcast.pushes {
				got = append(got, p.connID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("record reached conns %v, want %v", got, tc.want)
			}
			want := updatePayload()
			for _, p := range bcast.pushes {
				payload := decodeUpdate(t, p)
				if payload.ID != want.ID {
					t.Errorf("id = %q, want %q", payload.ID, want.ID)
				}
				if payload.IsPromoted != want.IsPromoted {
					t.Errorf("is_promoted = %v, want %v", payload.IsPromoted, want.IsPromoted)
				}
				if payload.IsArchived != want.IsArchived {
					t.Errorf("is_archived = %v, want %v", payload.IsArchived, want.IsArchived)
				}
				if payload.Name == nil || *payload.Name != *want.Name {
					t.Errorf("name = %v, want %q", payload.Name, *want.Name)
				}
				if payload.Cwd != want.Cwd {
					t.Errorf("cwd = %q, want %q", payload.Cwd, want.Cwd)
				}
				// Equal, never ==: the monotonic clock reading strips on JSON
				// marshal, so the round-tripped value is never == the original.
				if !payload.LastUsedAt.Equal(want.LastUsedAt) {
					t.Errorf("last_used_at = %v, want %v", payload.LastUsedAt, want.LastUsedAt)
				}
			}
		})
	}
}

// The pushed envelope carries no in_reply_to, which is the whole of what makes
// this an unsolicited push rather than a fifth reply — there is no request
// envelope for it to name. AC-4 records the same fact in the two places that
// classify the type; this is the assertion behind it.
func TestConversationUpdateEmitterV2_Announce_CarriesNoCorrelation(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "c1", Interactive: true}}}}
	e := newConversationUpdateEmitterV2(bcast, context.Background(), discardLogger())

	e.announce(updatePayload())

	if len(bcast.pushes) != 1 {
		t.Fatalf("recorded %d pushes, want 1", len(bcast.pushes))
	}
	if got := bcast.pushes[0].env.InReplyTo; got != nil {
		t.Errorf("in_reply_to = %d, want unset — nothing solicited this frame, so there is no "+
			"request envelope for it to correlate to", *got)
	}
}

// One timestamp for the whole fan-out, and strictly increasing envelope ids
// across announcements. The shared timestamp is what makes two clients' copies
// of one announcement the same event; the monotonic id is what a client orders
// on. Both are properties of the loop rather than of any one push, so neither is
// visible from a single-conn case.
func TestConversationUpdateEmitterV2_Announce_SharedTimestampMonotonicIDs(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "c1", Interactive: true},
		{ConnID: "c2", Interactive: true},
	}}}
	e := newConversationUpdateEmitterV2(bcast, context.Background(), discardLogger())

	e.announce(updatePayload())
	second := updatePayload()
	second.ID = "88888888-8888-4888-8888-888888888888"
	e.announce(second)

	if len(bcast.pushes) != 4 {
		t.Fatalf("recorded %d pushes, want 4 (two conns × two announcements)", len(bcast.pushes))
	}
	if !bcast.pushes[0].env.TS.Equal(bcast.pushes[1].env.TS) {
		t.Errorf("the two conns' copies of one announcement carry different timestamps (%v, %v); "+
			"the timestamp is taken once per announcement, outside the per-conn loop",
			bcast.pushes[0].env.TS, bcast.pushes[1].env.TS)
	}
	for i := 1; i < len(bcast.pushes); i++ {
		if bcast.pushes[i].env.ID <= bcast.pushes[i-1].env.ID {
			t.Errorf("envelope id %d (push %d) does not exceed %d (push %d); ids must be strictly "+
				"increasing across the whole emitter",
				bcast.pushes[i].env.ID, i, bcast.pushes[i-1].env.ID, i-1)
		}
	}
}

// AC-3: a torn-down conn is logged and skipped, never fatal. relay.ErrConnNotFound
// is exactly what Push answers for a conn that closed between the ActiveConns
// snapshot and the push, which is the reachable race rather than a hypothetical.
// The second conn must still receive its copy.
func TestConversationUpdateEmitterV2_Announce_TornDownConnDoesNotStopTheFanOut(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "gone", Interactive: true},
			{ConnID: "live", Interactive: true},
		}},
		pushErr: map[string]error{"gone": relay.ErrConnNotFound},
	}
	var buf bytes.Buffer
	e := newConversationUpdateEmitterV2(bcast, context.Background(), debugLogger(&buf))

	e.announce(updatePayload())

	if len(bcast.pushes) != 2 {
		t.Fatalf("recorded %d pushes, want 2 — a failing conn must not end the fan-out", len(bcast.pushes))
	}
	if bcast.pushes[1].connID != "live" {
		t.Errorf("second push went to %q, want the conn after the failing one", bcast.pushes[1].connID)
	}
	logs := buf.String()
	if !strings.Contains(logs, "conversation_update.push_err") {
		t.Errorf("a dropped push logged no conversation_update.push_err line; got:\n%s", logs)
	}
	if !strings.Contains(logs, relay.ErrConnNotFound.Error()) {
		t.Errorf("the push-error line names no transport sentinel; got:\n%s", logs)
	}
	assertUpdateLogsPathFree(t, logs)
}

// AC-3: the loop returns early on daemon teardown rather than logging once per
// conn for a manager that is going away. The distinguishing assertion is the
// SECOND conn getting no push at all — a loop that merely skipped the error
// would push twice.
func TestConversationUpdateEmitterV2_Announce_TeardownReturnsEarly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "c1", Interactive: true},
			{ConnID: "c2", Interactive: true},
		}},
		pushErr: map[string]error{"c1": context.Canceled, "c2": context.Canceled},
	}
	var buf bytes.Buffer
	e := newConversationUpdateEmitterV2(bcast, ctx, debugLogger(&buf))

	e.announce(updatePayload())

	if len(bcast.pushes) > 1 {
		t.Errorf("recorded %d pushes after teardown, want at most 1 — the loop returns on the "+
			"first ctx error rather than walking every conn", len(bcast.pushes))
	}
	if strings.Contains(buf.String(), "conversation_update.push_err") {
		t.Errorf("teardown logged a push-error line; a daemon going away is not a dropped push:\n%s",
			buf.String())
	}
}

// The two host filesystem strings this record carries — the workspace path and
// the label derived from it — must not reach a log line on any branch. Checked
// on all four because the rule binds each separately, and a future edit is most
// likely to reintroduce the path into an error branch where it looks like
// useful diagnostics.
func TestConversationUpdateEmitterV2_Announce_NoBranchLogsTheWorkspace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pushErr error
	}{
		{name: "successful fan-out"},
		{name: "dropped push", pushErr: relay.ErrConnNotFound},
		{name: "push refused by a ctx error", pushErr: context.Canceled},
		{name: "push refused by an unexpected error", pushErr: errors.New("boom")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bcast := &fakeInteractiveBcast{
				snapshots: [][]relay.ActiveConn{{{ConnID: "c1", Interactive: true}}},
			}
			if tc.pushErr != nil {
				bcast.pushErr = map[string]error{"c1": tc.pushErr}
			}
			var buf bytes.Buffer
			e := newConversationUpdateEmitterV2(bcast, context.Background(), debugLogger(&buf))

			e.announce(updatePayload())

			assertUpdateLogsPathFree(t, buf.String())
		})
	}
}

// assertUpdateLogsPathFree is the never-log rule as an assertion. The
// conversation id stays loggable — it is daemon-minted and names no host
// location — which is why only the workspace and its label are banned here.
func assertUpdateLogsPathFree(t *testing.T, logs string) {
	t.Helper()
	if strings.Contains(logs, updateCwd) {
		t.Errorf("a log line carries the announced workspace path, a host filesystem location:\n%s", logs)
	}
	if strings.Contains(logs, updateName) {
		t.Errorf("a log line carries the announced conversation name, which is derived from a "+
			"host directory:\n%s", logs)
	}
	// A fragment, in case a future edit logs part of either rather than the whole.
	if strings.Contains(logs, "announced-") {
		t.Errorf("a log line carries part of the announced workspace or label:\n%s", logs)
	}
}
