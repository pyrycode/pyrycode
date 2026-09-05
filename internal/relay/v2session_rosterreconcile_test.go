package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2078 connect-time background-task-roster reconcile (the sixth Mode B
// instance, after the #877 modal, #878 queue, #1863 model-list, #1979 question and
// #2006 slash-command sets) ---
//
// TWO BRANCHES HERE ARE UNREACHABLE BY FIXTURE, and neither gap is an oversight:
//
//   - The marshal branch cannot be reddened. BackgroundTaskRosterPayload is a closed
//     struct of string / []BackgroundTask / int, BackgroundTask is three strings and
//     a []string, and the custom MarshalJSON delegates to json.Marshal over that
//     closed alias — no value of the payload can fail to marshal. None of the five
//     analogue test files contains a marshal test for the same reason.
//   - The non-ctx arm of the push branch cannot be reddened either. ErrConnNotFound
//     is Push's only non-ctx error, and the push queue is created a few statements
//     earlier in the same handleNoiseInit on the same goroutine, so it is
//     unreachable from this call site by construction.
//
// That bounds how far TestV2Session_BackgroundTaskRosterReconcile_ContentFreeLogging
// reaches: on a green run the reconcile never gets the OPPORTUNITY to emit either
// error record, so that test proves the success path logs nothing and no more. The
// never-log guarantee for the two error branches rests on reading them, not on this
// file. Stated here so a passing suite does not read as branch coverage it is not.
//
// THE ROUND-TRIP ASYMMETRY this family has and the five twins do not: a payload with
// a nil Tasks marshals to "tasks":[] (BackgroundTaskRosterPayload.MarshalJSON) and
// decodes back to an EMPTY, NON-NIL slice, so reflect.DeepEqual against the seam's
// own value is false for an empty roster. wantRoster normalises the want side rather
// than the got side, so the empty case is compared honestly instead of excused.

// sampleBackgroundTaskRosterPayload builds a fully-populated
// BackgroundTaskRosterPayload for convID with one BackgroundTask per id, standing in
// for one entry the RetainedBackgroundTaskRosters seam enumerates. Every field is
// distinct and non-zero so a mapping that dropped or crossed one cannot survive the
// reflect.DeepEqual round-trip check. The payload has no time.Time field, so
// DeepEqual is safe here and #878's equalQueued has no counterpart.
func sampleBackgroundTaskRosterPayload(convID string, ids ...string) protocol.BackgroundTaskRosterPayload {
	tasks := make([]protocol.BackgroundTask, 0, len(ids))
	for _, id := range ids {
		tasks = append(tasks, protocol.BackgroundTask{
			TaskID:      id,
			TaskType:    fmt.Sprintf("%s-type", id),
			Description: fmt.Sprintf("description of %s", id),
		})
	}
	return protocol.BackgroundTaskRosterPayload{ConversationID: convID, Tasks: tasks}
}

// wantRoster is p as it comes back off the wire: a nil Tasks becomes an empty
// non-nil slice, because that is what MarshalJSON emits and what a decoder produces
// for "tasks":[]. Only the WANT side is normalised — normalising the got side would
// make an accidental "tasks":null indistinguishable from the empty array this frame
// is contractually required to send.
func wantRoster(p protocol.BackgroundTaskRosterPayload) protocol.BackgroundTaskRosterPayload {
	if p.Tasks == nil {
		p.Tasks = []protocol.BackgroundTask{}
	}
	return p
}

// reconciledBackgroundTaskRosters decrypts every noise_msg addressed to connID under
// recv (in recorded, hence AEAD-nonce, order) and returns each background_task_roster
// keyed by conversation_id, alongside the raw payload bytes so a caller can assert on
// the wire form rather than on what a decoder makes of it. In these tests the
// reconcile is the only outbound app traffic, so a noise_msg of any other Type is a
// bug — that check is where "no turn opened" is asserted, since a turn-boundary frame
// would have to show up here as a non-background_task_roster envelope. The nil-EventID
// check is the other half of AC4: it keeps the frame out of the turn-event replay ring
// and makes forwardEnvelope's last_event_id dedup inert for it. A repeated
// conversation_id is a fan-out/dup bug. Decrypt each conn's frames exactly once per
// test — Decrypt advances the recv nonce.
func reconciledBackgroundTaskRosters(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState) (map[string]protocol.BackgroundTaskRosterPayload, map[string][]byte) {
	t.Helper()
	out := make(map[string]protocol.BackgroundTaskRosterPayload)
	raw := make(map[string][]byte)
	for _, env := range noiseMsgsForConn(t, rec, connID) {
		inner := decryptAppFrame(t, env, recv)
		if inner.Type != protocol.TypeBackgroundTaskRoster {
			t.Fatalf("conn %q: noise_msg Type = %q, want %q", connID, inner.Type, protocol.TypeBackgroundTaskRoster)
		}
		if inner.EventID != nil {
			t.Errorf("conn %q: reconciled background_task_roster carries event_id %d, want none", connID, *inner.EventID)
		}
		var p protocol.BackgroundTaskRosterPayload
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			t.Fatalf("conn %q: decode background_task_roster payload: %v", connID, err)
		}
		if _, dup := out[p.ConversationID]; dup {
			t.Errorf("conn %q: conversation_id %q re-sent more than once", connID, p.ConversationID)
		}
		out[p.ConversationID] = p
		raw[p.ConversationID] = inner.Payload
	}
	return out, raw
}

// TestV2Session_BackgroundTaskRosterReconcile_Delivery sends exactly the seam's
// payloads to a freshly interactive-open conn, unchanged (AC1, AC3's first half).
// Each row's payloads are compared by reflect.DeepEqual against the seam's own
// values, keyed by conversation_id, so the match is order-independent and covers
// Tasks, DroppedTasks and each row's TaskType / Description / TruncatedFields.
func TestV2Session_BackgroundTaskRosterReconcile_Delivery(t *testing.T) {
	t.Parallel()

	// A payload whose bound reporters are both non-zero: DroppedTasks on the
	// aggregate and TruncatedFields on one row. They are the frame's only record that
	// the producer cut something, so a mapping that silently zeroed them — presenting
	// a capped roster as a whole one, which AC3 forbids — would pass every other row.
	bounded := sampleBackgroundTaskRosterPayload("conv-roster-bounded", "task-a", "task-b")
	bounded.DroppedTasks = 7
	bounded.Tasks[0].TruncatedFields = []string{"description", "task_type"}

	tests := []struct {
		name     string
		payloads []protocol.BackgroundTaskRosterPayload
	}{
		{
			name:     "one retained roster",
			payloads: []protocol.BackgroundTaskRosterPayload{sampleBackgroundTaskRosterPayload("conv-roster-one", "task-1")},
		},
		{
			name: "two conversations",
			payloads: []protocol.BackgroundTaskRosterPayload{
				sampleBackgroundTaskRosterPayload("conv-roster-1", "task-1"),
				sampleBackgroundTaskRosterPayload("conv-roster-2", "task-2", "task-3"),
			},
		},
		{
			name:     "dropped tasks and truncated fields survive",
			payloads: []protocol.BackgroundTaskRosterPayload{bounded},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const connA = "c-v2-A"

			respPriv, respPub := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:                        frames,
				Outbound:                      rec.outbound,
				StaticPriv:                    respPriv,
				Devices:                       reg,
				ServerID:                      v2TestServerID,
				Logger:                        silentLogger(),
				RetainedBackgroundTaskRosters: func() []protocol.BackgroundTaskRosterPayload { return tt.payloads },
			})
			t.Cleanup(stop)

			_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

			// noise_resp + one background_task_roster per retained payload; exactly that
			// many are ever emitted here, so the snapshot is final once recorded.
			waitForEnvelopes(t, rec, 1+len(tt.payloads))

			got, _ := reconciledBackgroundTaskRosters(t, rec, connA, aRecv)
			if len(got) != len(tt.payloads) {
				t.Fatalf("conn %q: got %d background_task_roster, want %d", connA, len(got), len(tt.payloads))
			}
			for _, p := range tt.payloads {
				want := wantRoster(p)
				if !reflect.DeepEqual(got[want.ConversationID], want) {
					t.Errorf("conn %q: conv %q = %+v, want %+v", connA, want.ConversationID, got[want.ConversationID], want)
				}
			}
		})
	}
}

// TestV2Session_BackgroundTaskRosterReconcile_EmptyRosterIsSent pins AC3's second
// half, the one place this family's answer differs from its five twins: an empty
// roster is a POSITIVE statement that nothing is alive, so a payload carrying no
// tasks is sent as a frame carrying an empty list rather than being filtered out.
//
// Two payloads, one empty and one populated, because the two failure modes need
// different witnesses: a `len(p.Tasks) == 0 { continue }` filter drops exactly one
// frame, which only the COUNT catches, while a nil Tasks reaching the wire as
// "tasks":null decodes to a zero-length slice just as "tasks":[] does, so only the
// RAW BYTES catch it. The populated payload also keeps the count assertion honest —
// with the empty one alone, a total of 0 and a filter bug would be the same number.
func TestV2Session_BackgroundTaskRosterReconcile_EmptyRosterIsSent(t *testing.T) {
	t.Parallel()

	const (
		connA      = "c-v2-A"
		emptyConv  = "conv-roster-empty"
		activeConv = "conv-roster-active"
	)
	// Tasks left nil, which is what a producer hands over for a roster claude
	// reported as empty AND for one whose key claude omitted — MarshalJSON is what
	// collapses both to the empty array.
	empty := protocol.BackgroundTaskRosterPayload{ConversationID: emptyConv}
	active := sampleBackgroundTaskRosterPayload(activeConv, "task-live")

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		RetainedBackgroundTaskRosters: func() []protocol.BackgroundTaskRosterPayload {
			return []protocol.BackgroundTaskRosterPayload{empty, active}
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	waitForEnvelopes(t, rec, 3) // noise_resp + both rosters

	got, raw := reconciledBackgroundTaskRosters(t, rec, connA, aRecv)
	if len(got) != 2 {
		t.Fatalf("conn %q: got %d background_task_roster, want 2 (the empty roster must not be filtered out)", connA, len(got))
	}
	if !reflect.DeepEqual(got[emptyConv], wantRoster(empty)) {
		t.Errorf("conn %q: empty roster = %+v, want %+v", connA, got[emptyConv], wantRoster(empty))
	}
	if !reflect.DeepEqual(got[activeConv], wantRoster(active)) {
		t.Errorf("conn %q: active roster = %+v, want %+v", connA, got[activeConv], wantRoster(active))
	}
	// The wire form is the contract: "tasks":[] reads as an empty list where
	// "tasks":null reads as absent/unknown, and both decode to a zero-length slice.
	if body := string(raw[emptyConv]); !strings.Contains(body, `"tasks":[]`) {
		t.Errorf("conn %q: empty roster serialised as %s, want a payload carrying \"tasks\":[]", connA, body)
	}
}

// TestV2Session_BackgroundTaskRosterReconcile_UnicastOnlyOpeningConn proves the send
// is unicast to the just-opened conn, NOT a fan-out: with A already open, opening B
// delivers the background_task_roster to B only — A receives no second one (AC1's
// "to that connection only and to no other"). It also pins the seam as a PURE READ:
// the same closure is called once per open and must yield the same set both times,
// which a producer that consumed or retired an entry would break.
func TestV2Session_BackgroundTaskRosterReconcile_UnicastOnlyOpeningConn(t *testing.T) {
	t.Parallel()

	const (
		connA  = "c-v2-A" // interactive; opened first
		connB  = "c-v2-B" // interactive; opened second
		convID = "conv-roster-unicast"
	)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		RetainedBackgroundTaskRosters: func() []protocol.BackgroundTaskRosterPayload {
			return []protocol.BackgroundTaskRosterPayload{sampleBackgroundTaskRosterPayload(convID, "task-1")}
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})

	// resp(A) + roster(A) + resp(B) + roster(B) = 4 in the correct (unicast) case. A
	// fan-out on B's open would push a 5th (a second roster to A), which the count
	// plus the helper's per-conn dup check below would catch.
	waitForEnvelopes(t, rec, 4)

	gotA, _ := reconciledBackgroundTaskRosters(t, rec, connA, aRecv)
	gotB, _ := reconciledBackgroundTaskRosters(t, rec, connB, bRecv)
	if len(gotA) != 1 {
		t.Errorf("conn %q (opened first): got %d background_task_roster, want exactly 1 (its own open, none from B's open)", connA, len(gotA))
	}
	if len(gotB) != 1 {
		t.Errorf("conn %q (opened second): got %d background_task_roster, want 1", connB, len(gotB))
	}
}

// TestV2Session_BackgroundTaskRosterReconcile_NoFrame pins AC2's three arms: no
// frame AND no log record AND no error — each row asserts zero noise_msg for the
// conn under test, zero records carrying this reconcile's event prefix, and that the
// session is still enumerable-open, so behaviour is byte-identical to the pre-#2078
// posture.
func TestV2Session_BackgroundTaskRosterReconcile_NoFrame(t *testing.T) {
	t.Parallel()

	const (
		connA      = "c-v2-A"   // the conn under test; must receive nothing
		connCtl    = "c-v2-CTL" // interactive control conn, capability row only
		convID     = "conv-roster-noframe"
		eventScope = "v2.backgroundtaskroster." // both error events share this prefix
	)
	nonEmptySeam := func() []protocol.BackgroundTaskRosterPayload {
		return []protocol.BackgroundTaskRosterPayload{sampleBackgroundTaskRosterPayload(convID, "task-1")}
	}

	tests := []struct {
		name string
		seam func() []protocol.BackgroundTaskRosterPayload
		caps []string
		// controlConn opens a second, interactive conn AFTER the conn under test. The
		// capability row needs it: without a conn that DOES receive a roster, A's zero
		// is indistinguishable from an empty seam and the !s.interactive mutant
		// survives green.
		controlConn bool
	}{
		{
			name: "nil seam",
			seam: nil, // unwired / foreground — this slice's shipped posture
			caps: []string{protocol.CapabilityInteractive},
		},
		{
			// Pins the CONTRACT (a real producer returning nil must not error), not a
			// reachable branch: with a zero-length slice the loop body never executes,
			// so deleting the len(retained) == 0 check is an equivalent mutant rather
			// than an untested one. Note this is the EMPTY SEAM, not an empty roster —
			// the empty ROSTER is a payload that must be sent, and
			// TestV2Session_BackgroundTaskRosterReconcile_EmptyRosterIsSent pins it.
			name: "zero payloads",
			seam: func() []protocol.BackgroundTaskRosterPayload { return nil },
			caps: []string{protocol.CapabilityInteractive},
		},
		{
			name:        "capability not negotiated",
			seam:        nonEmptySeam,
			caps:        nil,
			controlConn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Debug is the lowest level, so a record at any level would surface here.
			logBuf := &lockedBuffer{}
			logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			respPriv, respPub := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:                        frames,
				Outbound:                      rec.outbound,
				StaticPriv:                    respPriv,
				Devices:                       reg,
				ServerID:                      v2TestServerID,
				Logger:                        logger,
				RetainedBackgroundTaskRosters: tt.seam,
			})
			t.Cleanup(stop)

			// The conn under test opens first and fully settles (its reconcile runs
			// synchronously in handleNoiseInit and pushes nothing) before the control
			// conn's multi-step handshake pumps the Run loop.
			openModalConn(t, mgr, frames, rec, respPub, connA, tt.caps)

			wantEnvelopes := 1 // resp(A) only
			var ctlRecv *noise.CipherState
			if tt.controlConn {
				_, ctlRecv = openModalConn(t, mgr, frames, rec, respPub, connCtl, []string{protocol.CapabilityInteractive})
				wantEnvelopes = 3 // + resp(CTL) + roster(CTL)
			}
			waitForEnvelopes(t, rec, wantEnvelopes)

			if tt.controlConn {
				if got, _ := reconciledBackgroundTaskRosters(t, rec, connCtl, ctlRecv); len(got) != 1 {
					t.Fatalf("interactive control conn %q: got %d background_task_roster, want 1 (the seam must be enumerating a roster for A's zero to mean the gate)", connCtl, len(got))
				}
			}
			if msgs := noiseMsgsForConn(t, rec, connA); len(msgs) != 0 {
				t.Errorf("conn %q: got %d noise_msg, want 0", connA, len(msgs))
			}
			// No log record either. The handshake line proves the capture is live, so
			// the absence below is a measurement rather than an inert buffer.
			logs := logBuf.String()
			if !strings.Contains(logs, "handshake") {
				t.Fatalf("log capture appears inert (no handshake line); cannot trust the no-record assertion. logs = %q", logs)
			}
			if strings.Contains(logs, eventScope) {
				t.Errorf("conn %q: reconcile wrote a log record on a no-frame path; logs = %q", connA, logs)
			}
			// Still enumerable-open — no frame AND no error.
			waitConnOpen(t, mgr, connA)
		})
	}
}

// TestV2Session_BackgroundTaskRosterReconcile_ContentFreeLogging pins AC5: no
// claude-authored task text — task id, task type, description or a truncated-fields
// entry — reaches a log record on this path at any level. Captured at Debug (the
// lowest level) so any leak would surface; the handshake-accept line proves the
// capture is live (non-vacuous). Four distinct sentinels rather than one: the four
// are separate struct members, and a branch echoing just one of them must not pass.
//
// How far this reaches is bounded, and the file header says why: on a green run
// neither error branch is reachable, so what this test proves is that the SUCCESS
// path logs nothing. The two error branches' never-log guarantee — including the
// marshal branch's refusal to echo err, which would quote a description straight into
// the record — is a code-reading one.
func TestV2Session_BackgroundTaskRosterReconcile_ContentFreeLogging(t *testing.T) {
	t.Parallel()

	const (
		connA       = "c-v2-A"
		convID      = "conv-roster-secret"
		taskID      = "SENTINEL-TASK-ID-do-not-log"
		taskType    = "SENTINEL-TASK-TYPE-do-not-log"
		description = "SENTINEL-DESCRIPTION-do-not-log"
		truncated   = "SENTINEL-TRUNCATED-FIELD-do-not-log"
	)
	payload := protocol.BackgroundTaskRosterPayload{
		ConversationID: convID,
		Tasks: []protocol.BackgroundTask{{
			TaskID:          taskID,
			TaskType:        taskType,
			Description:     description,
			TruncatedFields: []string{truncated},
		}},
	}

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:                        frames,
		Outbound:                      rec.outbound,
		StaticPriv:                    respPriv,
		Devices:                       reg,
		ServerID:                      v2TestServerID,
		Logger:                        logger,
		RetainedBackgroundTaskRosters: func() []protocol.BackgroundTaskRosterPayload { return []protocol.BackgroundTaskRosterPayload{payload} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + the one reconciled roster: once both are recorded the open path
	// (and any log line it would emit) has run.
	waitForEnvelopes(t, rec, 2)
	if got, _ := reconciledBackgroundTaskRosters(t, rec, connA, aRecv); !reflect.DeepEqual(got[convID], wantRoster(payload)) {
		t.Fatalf("conn %q: reconcile did not send the sentinel payload (got %+v)", connA, got)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "handshake") {
		t.Fatalf("log capture appears inert (no handshake line); cannot trust the never-log assertion. logs = %q", logs)
	}
	for _, sentinel := range []string{taskID, taskType, description, truncated} {
		if strings.Contains(logs, sentinel) {
			t.Errorf("claude-authored string %q leaked into a log record; logs = %q", sentinel, logs)
		}
	}
}

// TestV2Session_BackgroundTaskRosterReconcile_ContextTeardownStopsBatch pins the
// send loop's inherited error posture: a context teardown stops the batch rather than
// plodding through the remaining payloads, because the session is going away and they
// have nowhere to land. Four of the five analogue reconciles ship this branch
// untested; the slash-command twin does not, and its overview records why the
// obvious assertion fails.
//
// Called directly with an already-cancelled context and a hand-built V2Session — the
// package already unit-tests unexported methods, and Push checks ctx.Err() before
// touching any map, so no handshake and no push queue are needed. The assertion is on
// the COUNT of push_err records, not on the (zero) envelope count: zero envelopes hold
// either way, so only the record count distinguishes the early return from a continue.
// Exactly one with the return; one per payload without it.
func TestV2Session_BackgroundTaskRosterReconcile_ContextTeardownStopsBatch(t *testing.T) {
	t.Parallel()

	const pushErrEvent = "v2.backgroundtaskroster.reconcile.push_err"

	payloads := []protocol.BackgroundTaskRosterPayload{
		sampleBackgroundTaskRosterPayload("conv-roster-teardown-1", "task-1"),
		sampleBackgroundTaskRosterPayload("conv-roster-teardown-2", "task-2"),
		sampleBackgroundTaskRosterPayload("conv-roster-teardown-3", "task-3"),
	}

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 1)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:                        frames,
		Outbound:                      rec.outbound,
		StaticPriv:                    respPriv,
		Devices:                       reg,
		ServerID:                      v2TestServerID,
		Logger:                        logger,
		RetainedBackgroundTaskRosters: func() []protocol.BackgroundTaskRosterPayload { return payloads },
	})
	t.Cleanup(stop)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A session value of this reconcile's own making: it reads connID and interactive
	// and nothing else, and Push resolves connID under pushMu, so no state is shared
	// with the manager's Run goroutine.
	mgr.reconcileBackgroundTaskRosters(ctx, &V2Session{connID: "c-v2-teardown", interactive: true})

	if got := strings.Count(logBuf.String(), pushErrEvent); got != 1 {
		t.Errorf("got %d %s records for %d payloads, want exactly 1 (the batch must stop at the first ctx failure, not continue)", got, pushErrEvent, len(payloads))
	}
	if msgs := noiseMsgsForConn(t, rec, "c-v2-teardown"); len(msgs) != 0 {
		t.Errorf("got %d noise_msg for a torn-down context, want 0", len(msgs))
	}
}
