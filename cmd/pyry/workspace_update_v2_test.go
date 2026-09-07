package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// The record one announcement carries. Both strings are deliberately distinctive
// so the never-log assertions cannot pass against a buffer that merely never said
// "cwd" or "label" — a substring like "project" appears in half the daemon's
// vocabulary, and neither of these does.
const (
	wsUpdatePath  = "/home/op/pyry-2209-announced-workspace"
	wsUpdateLabel = "pyry-2209-announced-label"
	// wsUpdateRequester is the conn that sent the rename_workspace: it already
	// holds the correlated reply, so it must receive no push.
	wsUpdateRequester = "c-requester"
)

// wsUpdatePayload is the announced record. A function rather than a package var:
// Label is a pointer, and a shared one would let a mutating test reach every
// other case.
func wsUpdatePayload() protocol.WorkspaceUpdatedPayload {
	label := wsUpdateLabel
	return protocol.WorkspaceUpdatedPayload{Path: wsUpdatePath, Label: &label}
}

// decodeWsUpdate decodes one recorded push as a workspace_updated payload,
// failing the test if it is any other frame.
func decodeWsUpdate(t *testing.T, p recordedPush) protocol.WorkspaceUpdatedPayload {
	t.Helper()
	if p.env.Type != protocol.TypeWorkspaceUpdated {
		t.Fatalf("pushed envelope type = %q, want %q", p.env.Type, protocol.TypeWorkspaceUpdated)
	}
	var got protocol.WorkspaceUpdatedPayload
	if err := json.Unmarshal(p.env.Payload, &got); err != nil {
		t.Fatalf("decode workspace_updated payload: %v", err)
	}
	return got
}

// pushedConnIDs lists the conns one fan-out reached, in fan-out order.
func pushedConnIDs(pushes []recordedPush) []string {
	var out []string
	for _, p := range pushes {
		out = append(out, p.connID)
	}
	return out
}

// AC-1 and AC-2: the requester's conn is excluded and every other
// interactive-capable conn is reached. The #607 capability gate and the
// conn-id exclusion are the whole of this table — a skipped conn is attached
// and authenticated, so nothing but the flag or the id separates it from a
// recipient.
func TestWorkspaceUpdateEmitterV2_Announce_ExcludesRequesterAndGatesOnInteractive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		exclude string
		conns   []relay.ActiveConn
		want    []string // conn ids that must receive the record, in fan-out order
	}{
		{
			name:    "the requester's own conn receives no push",
			exclude: wsUpdateRequester,
			conns:   []relay.ActiveConn{{ConnID: wsUpdateRequester, Interactive: true}},
			want:    nil,
		},
		{
			name:    "every other interactive conn is reached",
			exclude: wsUpdateRequester,
			conns: []relay.ActiveConn{
				{ConnID: "c1", Interactive: true},
				{ConnID: wsUpdateRequester, Interactive: true},
				{ConnID: "c2", Interactive: true},
			},
			want: []string{"c1", "c2"},
		},
		{
			name:    "a non-interactive conn receives nothing",
			exclude: wsUpdateRequester,
			conns:   []relay.ActiveConn{{ConnID: "c1", Interactive: false}},
			want:    nil,
		},
		{
			name:    "mixed set delivers only to the interactive non-requesters",
			exclude: wsUpdateRequester,
			conns: []relay.ActiveConn{
				{ConnID: "c1", Interactive: true},
				{ConnID: "c2", Interactive: false},
				{ConnID: wsUpdateRequester, Interactive: true},
				{ConnID: "c3", Interactive: true},
			},
			want: []string{"c1", "c3"},
		},
		{
			// Exclusion is keyed on the CONN, not on the device or the operator
			// behind it. A requester with a second client open sees the rename
			// there, because that conn asked for nothing and holds no reply.
			name:    "a requester holding a second conn is pushed on that second conn",
			exclude: wsUpdateRequester,
			conns: []relay.ActiveConn{
				{ConnID: wsUpdateRequester, Interactive: true},
				{ConnID: "c-requester-second-window", Interactive: true},
			},
			want: []string{"c-requester-second-window"},
		},
		{
			// An empty key excludes nothing rather than matching a conn: no
			// ActiveConn carries an empty ConnID, so this is the shape a
			// hypothetical requester-less producer would take.
			name:    "an empty exclusion key skips nobody",
			exclude: "",
			conns: []relay.ActiveConn{
				{ConnID: "c1", Interactive: true},
				{ConnID: "c2", Interactive: true},
			},
			want: []string{"c1", "c2"},
		},
		{
			name:    "no conns at all",
			exclude: wsUpdateRequester,
			conns:   nil,
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{tc.conns}}
			e := newWorkspaceUpdateEmitterV2(bcast, context.Background(), discardLogger())

			e.announce(wsUpdatePayload(), tc.exclude)

			got := pushedConnIDs(bcast.pushes)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("record reached conns %v, want %v", got, tc.want)
			}
			want := wsUpdatePayload()
			for _, p := range bcast.pushes {
				payload := decodeWsUpdate(t, p)
				if payload.Path != want.Path {
					t.Errorf("path = %q, want %q", payload.Path, want.Path)
				}
				if payload.Label == nil || *payload.Label != *want.Label {
					t.Errorf("label = %v, want %q", payload.Label, *want.Label)
				}
			}
		})
	}
}

// The pushed envelope carries no in_reply_to, which is the whole of what makes
// this an unsolicited push rather than a second reply — nothing solicited it, so
// there is no request envelope for it to name. AC-5 records the same fact in the
// two places that classify the type; this is the assertion behind it.
func TestWorkspaceUpdateEmitterV2_Announce_CarriesNoCorrelation(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "c1", Interactive: true}}}}
	e := newWorkspaceUpdateEmitterV2(bcast, context.Background(), discardLogger())

	e.announce(wsUpdatePayload(), wsUpdateRequester)

	if len(bcast.pushes) != 1 {
		t.Fatalf("recorded %d pushes, want 1", len(bcast.pushes))
	}
	if got := bcast.pushes[0].env.InReplyTo; got != nil {
		t.Errorf("in_reply_to = %d, want unset — the requester's copy is the correlated one, and "+
			"nothing solicited this frame", *got)
	}
}

// AC-3: clearing a label fans out the same way, carrying a JSON null. Asserted on
// the RAW pushed bytes as well as the decoded value: a decoded nil cannot tell a
// present null from an absent key, so an omitempty regression that drops the key
// would pass the typed check while changing what the wire says.
func TestWorkspaceUpdateEmitterV2_Announce_ClearedLabelFansOutAsNull(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "c1", Interactive: true}}}}
	e := newWorkspaceUpdateEmitterV2(bcast, context.Background(), discardLogger())

	e.announce(protocol.WorkspaceUpdatedPayload{Path: wsUpdatePath, Label: nil}, wsUpdateRequester)

	if len(bcast.pushes) != 1 {
		t.Fatalf("recorded %d pushes, want 1 — a clear fans out like a set", len(bcast.pushes))
	}
	if got := decodeWsUpdate(t, bcast.pushes[0]); got.Label != nil {
		t.Errorf("label = %q, want nil", *got.Label)
	}
	if raw := string(bcast.pushes[0].env.Payload); !strings.Contains(raw, `"label":null`) {
		t.Errorf("pushed payload = %s; want a present `\"label\":null` key — a client cannot read a "+
			"clear from an omitted key", raw)
	}
}

// One timestamp for the whole fan-out, and strictly increasing envelope ids
// across announcements. The shared timestamp is what makes two clients' copies of
// one announcement the same event; the monotonic id is what a client orders on.
// Both are properties of the loop rather than of any one push, so neither is
// visible from a single-conn case.
func TestWorkspaceUpdateEmitterV2_Announce_SharedTimestampMonotonicIDs(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "c1", Interactive: true},
		{ConnID: "c2", Interactive: true},
	}}}
	e := newWorkspaceUpdateEmitterV2(bcast, context.Background(), discardLogger())

	e.announce(wsUpdatePayload(), wsUpdateRequester)
	second := wsUpdatePayload()
	second.Path = "/home/op/pyry-2209-announced-workspace-two"
	e.announce(second, wsUpdateRequester)

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

// AC-4: a conn torn down between the snapshot and its push is logged and skipped,
// never fatal, and the remaining conns still get their copy. relay.ErrConnNotFound
// is exactly what Push answers for that conn, which is the reachable race rather
// than a hypothetical.
func TestWorkspaceUpdateEmitterV2_Announce_TornDownConnDoesNotStopTheFanOut(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "gone", Interactive: true},
			{ConnID: "live", Interactive: true},
		}},
		pushErr: map[string]error{"gone": relay.ErrConnNotFound},
	}
	var buf bytes.Buffer
	e := newWorkspaceUpdateEmitterV2(bcast, context.Background(), debugLogger(&buf))

	e.announce(wsUpdatePayload(), wsUpdateRequester)

	if len(bcast.pushes) != 2 {
		t.Fatalf("recorded %d pushes, want 2 — one client's failure must not affect another's "+
			"delivery", len(bcast.pushes))
	}
	if bcast.pushes[1].connID != "live" {
		t.Errorf("second push went to %q, want the conn after the failing one", bcast.pushes[1].connID)
	}
	logs := buf.String()
	if !strings.Contains(logs, "workspace_update.push_err") {
		t.Errorf("a dropped push logged no workspace_update.push_err line; got:\n%s", logs)
	}
	if !strings.Contains(logs, relay.ErrConnNotFound.Error()) {
		t.Errorf("the push-error line names no transport sentinel; got:\n%s", logs)
	}
	assertWsUpdateLogsWorkspaceFree(t, logs)
}

// AC-4: the loop returns early on daemon teardown rather than logging once per
// conn for a manager that is going away. The distinguishing assertion is the
// SECOND conn getting no push at all — a loop that merely skipped the error would
// push twice.
func TestWorkspaceUpdateEmitterV2_Announce_TeardownReturnsEarly(t *testing.T) {
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
	e := newWorkspaceUpdateEmitterV2(bcast, ctx, debugLogger(&buf))

	e.announce(wsUpdatePayload(), wsUpdateRequester)

	if len(bcast.pushes) > 1 {
		t.Errorf("recorded %d pushes after teardown, want at most 1 — the loop returns on the first "+
			"ctx error rather than walking every conn", len(bcast.pushes))
	}
	if strings.Contains(buf.String(), "workspace_update.push_err") {
		t.Errorf("teardown logged a push-error line; a daemon going away is not a dropped push:\n%s",
			buf.String())
	}
}

// The two strings this record carries must not reach a log line on any branch,
// and unlike the conversation announcement there is NO daemon-minted identifier
// here to log in their place: the path is a host filesystem location and the
// label is operator content. Checked on all four push outcomes because the rule
// binds each separately, and a future edit is most likely to reintroduce the path
// into an error branch where it looks like useful diagnostics.
func TestWorkspaceUpdateEmitterV2_Announce_NoBranchLogsTheWorkspace(t *testing.T) {
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
			e := newWorkspaceUpdateEmitterV2(bcast, context.Background(), debugLogger(&buf))

			e.announce(wsUpdatePayload(), wsUpdateRequester)

			assertWsUpdateLogsWorkspaceFree(t, buf.String())
		})
	}
}

// assertWsUpdateLogsWorkspaceFree is the never-log rule as an assertion. Conn ids
// stay loggable — they are daemon-minted and name no host location — which is why
// only the workspace path and its operator-chosen label are banned here.
func assertWsUpdateLogsWorkspaceFree(t *testing.T, logs string) {
	t.Helper()
	if strings.Contains(logs, wsUpdatePath) {
		t.Errorf("a log line carries the announced workspace path, a host filesystem location:\n%s", logs)
	}
	if strings.Contains(logs, wsUpdateLabel) {
		t.Errorf("a log line carries the announced label, which is operator-supplied text arriving "+
			"from a network-paired party:\n%s", logs)
	}
	// A fragment, in case a future edit logs part of either rather than the whole.
	if strings.Contains(logs, "announced-") {
		t.Errorf("a log line carries part of the announced workspace or label:\n%s", logs)
	}
}
