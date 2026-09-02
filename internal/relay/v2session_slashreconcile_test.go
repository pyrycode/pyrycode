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

// --- #2006 connect-time slash-command-list reconcile (the fifth Mode B instance,
// after the #877 modal, #878 queue, #1863 model-list and #1979 question sets) ---
//
// TWO BRANCHES HERE ARE UNREACHABLE BY FIXTURE, and neither gap is an oversight:
//
//   - The marshal branch cannot be reddened. SlashCommandListPayload is a closed
//     struct of string / []SlashCommand / int, SlashCommand is three strings and
//     two []string, and both custom MarshalJSONs delegate to json.Marshal over
//     those closed types — no value of the payload can fail to marshal. None of
//     the four analogue test files contains a marshal test for the same reason.
//   - The non-ctx arm of the push branch cannot be reddened either.
//     ErrConnNotFound is Push's only non-ctx error, and the push queue is created
//     a few statements earlier in the same handleNoiseInit on the same goroutine,
//     so it is unreachable from this call site by construction.
//
// That matters for how far TestV2Session_SlashCommandReconcile_ContentFreeLogging
// reaches: on a green run the reconcile never gets the OPPORTUNITY to emit either
// error record, so that test proves the success path logs nothing and no more. The
// never-log guarantee for the two error branches rests on reading them, not on this
// file. Stated here so a passing suite does not read as branch coverage it is not.

// sampleSlashCommandListPayload builds a fully-populated SlashCommandListPayload
// for convID with one SlashCommand per name, standing in for one entry the
// RetainedSlashCommandLists seam enumerates. Every field is distinct and non-zero
// so a mapping that dropped or crossed one cannot survive the reflect.DeepEqual
// round-trip check. SlashCommandListPayload has no time.Time field, so DeepEqual is
// safe here and #878's equalQueued has no counterpart.
func sampleSlashCommandListPayload(convID string, names ...string) protocol.SlashCommandListPayload {
	commands := make([]protocol.SlashCommand, 0, len(names))
	for _, n := range names {
		commands = append(commands, protocol.SlashCommand{
			Name:         n,
			ArgumentHint: fmt.Sprintf("<%s-arg>", n),
			Description:  fmt.Sprintf("Description of %s", n),
			Aliases:      []string{fmt.Sprintf("%s-alias", n)},
		})
	}
	return protocol.SlashCommandListPayload{ConversationID: convID, Commands: commands}
}

// reconciledSlashCommandLists decrypts every noise_msg addressed to connID under
// recv (in recorded, hence AEAD-nonce, order) and returns each slash_command_list
// keyed by conversation_id. In these tests the reconcile is the only outbound app
// traffic, so a noise_msg of any other Type is a bug — that check is where "no turn
// opened" is asserted, since a turn-boundary frame would have to show up here as a
// non-slash_command_list envelope. The nil-EventID check is the other half of AC3:
// it keeps the frame out of the turn-event replay ring and makes forwardEnvelope's
// last_event_id dedup inert for it. A repeated conversation_id is a fan-out/dup bug.
// Decrypt each conn's frames exactly once per test — Decrypt advances the recv
// nonce.
func reconciledSlashCommandLists(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState) map[string]protocol.SlashCommandListPayload {
	t.Helper()
	out := make(map[string]protocol.SlashCommandListPayload)
	for _, env := range noiseMsgsForConn(t, rec, connID) {
		inner := decryptAppFrame(t, env, recv)
		if inner.Type != protocol.TypeSlashCommandList {
			t.Fatalf("conn %q: noise_msg Type = %q, want %q", connID, inner.Type, protocol.TypeSlashCommandList)
		}
		if inner.EventID != nil {
			t.Errorf("conn %q: reconciled slash_command_list carries event_id %d, want none", connID, *inner.EventID)
		}
		var p protocol.SlashCommandListPayload
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			t.Fatalf("conn %q: decode slash_command_list payload: %v", connID, err)
		}
		if _, dup := out[p.ConversationID]; dup {
			t.Errorf("conn %q: conversation_id %q re-sent more than once", connID, p.ConversationID)
		}
		out[p.ConversationID] = p
	}
	return out
}

// TestV2Session_SlashCommandReconcile_Delivery sends exactly the seam's payloads to
// a freshly interactive-open conn, unchanged (AC1). Each row's payloads are compared
// by reflect.DeepEqual against the seam's own values, keyed by conversation_id, so
// the match is order-independent and covers Commands, DroppedCommands and each
// entry's ArgumentHint / Description / Aliases / TruncatedFields.
func TestV2Session_SlashCommandReconcile_Delivery(t *testing.T) {
	t.Parallel()

	// A payload whose bound reporters are both non-zero: DroppedCommands on the
	// aggregate and TruncatedFields on one entry. They are the frame's only record
	// that the producer cut something, so a mapping that silently zeroed them would
	// pass every other row.
	bounded := sampleSlashCommandListPayload("conv-slash-bounded", "clear", "compact")
	bounded.DroppedCommands = 7
	bounded.Commands[0].TruncatedFields = []string{"description", "aliases"}

	tests := []struct {
		name     string
		payloads []protocol.SlashCommandListPayload
	}{
		{
			name:     "one retained list",
			payloads: []protocol.SlashCommandListPayload{sampleSlashCommandListPayload("conv-slash-one", "clear")},
		},
		{
			name: "two conversations",
			payloads: []protocol.SlashCommandListPayload{
				sampleSlashCommandListPayload("conv-slash-1", "clear"),
				sampleSlashCommandListPayload("conv-slash-2", "compact", "__remote-workflow"),
			},
		},
		{
			name:     "dropped commands and truncated fields survive",
			payloads: []protocol.SlashCommandListPayload{bounded},
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
				Frames:                    frames,
				Outbound:                  rec.outbound,
				StaticPriv:                respPriv,
				Devices:                   reg,
				ServerID:                  v2TestServerID,
				Logger:                    silentLogger(),
				RetainedSlashCommandLists: func() []protocol.SlashCommandListPayload { return tt.payloads },
			})
			t.Cleanup(stop)

			_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

			// noise_resp + one slash_command_list per retained list; exactly that many
			// are ever emitted here, so the snapshot is final once they are recorded.
			waitForEnvelopes(t, rec, 1+len(tt.payloads))

			got := reconciledSlashCommandLists(t, rec, connA, aRecv)
			if len(got) != len(tt.payloads) {
				t.Fatalf("conn %q: got %d slash_command_list, want %d", connA, len(got), len(tt.payloads))
			}
			for _, want := range tt.payloads {
				if !reflect.DeepEqual(got[want.ConversationID], want) {
					t.Errorf("conn %q: conv %q = %+v, want %+v", connA, want.ConversationID, got[want.ConversationID], want)
				}
			}
		})
	}
}

// TestV2Session_SlashCommandReconcile_UnicastOnlyOpeningConn proves the send is
// unicast to the just-opened conn, NOT a fan-out: with A already open, opening B
// delivers the slash_command_list to B only — A receives no second one (AC1's
// "unicast to that conn only"). It also pins the seam as a PURE READ: the same
// closure is called once per open and must yield the same set both times, which a
// producer that consumed or retired an entry would break.
func TestV2Session_SlashCommandReconcile_UnicastOnlyOpeningConn(t *testing.T) {
	t.Parallel()

	const (
		connA  = "c-v2-A" // interactive; opened first
		connB  = "c-v2-B" // interactive; opened second
		convID = "conv-slash-unicast"
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
		RetainedSlashCommandLists: func() []protocol.SlashCommandListPayload {
			return []protocol.SlashCommandListPayload{sampleSlashCommandListPayload(convID, "clear")}
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})

	// resp(A) + slash_command_list(A) + resp(B) + slash_command_list(B) = 4 in the
	// correct (unicast) case. A fan-out on B's open would push a 5th (a second
	// slash_command_list to A), which the count plus the helper's per-conn dup check
	// below would catch.
	waitForEnvelopes(t, rec, 4)

	gotA := reconciledSlashCommandLists(t, rec, connA, aRecv)
	gotB := reconciledSlashCommandLists(t, rec, connB, bRecv)
	if len(gotA) != 1 {
		t.Errorf("conn %q (opened first): got %d slash_command_list, want exactly 1 (its own open, none from B's open)", connA, len(gotA))
	}
	if len(gotB) != 1 {
		t.Errorf("conn %q (opened second): got %d slash_command_list, want 1", connB, len(gotB))
	}
}

// TestV2Session_SlashCommandReconcile_NoFrame pins AC2's three arms: no frame AND no
// error — each row asserts zero noise_msg for the conn under test and that the
// session is still enumerable-open, so behaviour is byte-identical to the pre-#2006
// posture.
func TestV2Session_SlashCommandReconcile_NoFrame(t *testing.T) {
	t.Parallel()

	const (
		connA   = "c-v2-A"   // the conn under test; must receive nothing
		connCtl = "c-v2-CTL" // interactive control conn, capability row only
		convID  = "conv-slash-noframe"
	)
	nonEmptySeam := func() []protocol.SlashCommandListPayload {
		return []protocol.SlashCommandListPayload{sampleSlashCommandListPayload(convID, "clear")}
	}

	tests := []struct {
		name string
		seam func() []protocol.SlashCommandListPayload
		caps []string
		// controlConn opens a second, interactive conn AFTER the conn under test.
		// The capability row needs it: without a conn that DOES receive a
		// slash_command_list, A's zero is indistinguishable from an empty seam and
		// the !s.interactive mutant survives green.
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
			// than an untested one. The model-list overview records the same.
			name: "zero payloads",
			seam: func() []protocol.SlashCommandListPayload { return nil },
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

			respPriv, respPub := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:                    frames,
				Outbound:                  rec.outbound,
				StaticPriv:                respPriv,
				Devices:                   reg,
				ServerID:                  v2TestServerID,
				Logger:                    silentLogger(),
				RetainedSlashCommandLists: tt.seam,
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
				wantEnvelopes = 3 // + resp(CTL) + slash_command_list(CTL)
			}
			waitForEnvelopes(t, rec, wantEnvelopes)

			if tt.controlConn {
				if got := reconciledSlashCommandLists(t, rec, connCtl, ctlRecv); len(got) != 1 {
					t.Fatalf("interactive control conn %q: got %d slash_command_list, want 1 (the seam must be enumerating a list for A's zero to mean the gate)", connCtl, len(got))
				}
			}
			if msgs := noiseMsgsForConn(t, rec, connA); len(msgs) != 0 {
				t.Errorf("conn %q: got %d noise_msg, want 0", connA, len(msgs))
			}
			// Still enumerable-open — no frame AND no error.
			waitConnOpen(t, mgr, connA)
		})
	}
}

// TestV2Session_SlashCommandReconcile_ContentFreeLogging pins AC4: no
// workspace-authored string — command name, argument hint, description or alias —
// reaches a log line on this path at any level. Captured at Debug (the lowest level)
// so any leak would surface; the handshake-accept line proves the capture is live
// (non-vacuous). Four distinct sentinels rather than one: the four are separate
// struct members, and a branch echoing just one of them must not pass.
//
// How far this reaches is bounded, and the file header says why: on a green run
// neither error branch is reachable, so what this test proves is that the SUCCESS
// path logs nothing. The two error branches' never-log guarantee is a code-reading
// one.
func TestV2Session_SlashCommandReconcile_ContentFreeLogging(t *testing.T) {
	t.Parallel()

	const (
		connA       = "c-v2-A"
		convID      = "conv-slash-secret"
		name        = "SENTINEL-COMMAND-NAME-do-not-log"
		argumentHnt = "SENTINEL-ARGUMENT-HINT-do-not-log"
		description = "SENTINEL-DESCRIPTION-do-not-log"
		alias       = "SENTINEL-ALIAS-do-not-log"
	)
	payload := protocol.SlashCommandListPayload{
		ConversationID: convID,
		Commands: []protocol.SlashCommand{{
			Name:         name,
			ArgumentHint: argumentHnt,
			Description:  description,
			Aliases:      []string{alias},
		}},
	}

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:                    frames,
		Outbound:                  rec.outbound,
		StaticPriv:                respPriv,
		Devices:                   reg,
		ServerID:                  v2TestServerID,
		Logger:                    logger,
		RetainedSlashCommandLists: func() []protocol.SlashCommandListPayload { return []protocol.SlashCommandListPayload{payload} },
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// noise_resp + the one reconciled slash_command_list: once both are recorded the
	// open path (and any log line it would emit) has run.
	waitForEnvelopes(t, rec, 2)
	if got := reconciledSlashCommandLists(t, rec, connA, aRecv); !reflect.DeepEqual(got[convID], payload) {
		t.Fatalf("conn %q: reconcile did not send the sentinel payload (got %+v)", connA, got)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "handshake") {
		t.Fatalf("log capture appears inert (no handshake line); cannot trust the never-log assertion. logs = %q", logs)
	}
	for _, sentinel := range []string{name, argumentHnt, description, alias} {
		if strings.Contains(logs, sentinel) {
			t.Errorf("workspace-authored string %q leaked into a log record; logs = %q", sentinel, logs)
		}
	}
}

// TestV2Session_SlashCommandReconcile_ContextTeardownStopsBatch pins AC5's first
// arm: a context teardown stops the batch rather than plodding through the
// remaining payloads. The four analogue reconciles ship this branch untested; AC5
// names it, so it gets a test.
//
// Called directly with an already-cancelled context and a hand-built V2Session —
// the package already unit-tests unexported methods, and Push checks ctx.Err()
// before touching any map, so no handshake and no push queue are needed. The
// assertion is on the COUNT of push_err records, not on the (zero) envelope count:
// zero envelopes hold either way, so only the record count distinguishes the early
// return from a continue. Exactly one with the return; one per payload without it.
func TestV2Session_SlashCommandReconcile_ContextTeardownStopsBatch(t *testing.T) {
	t.Parallel()

	const pushErrEvent = "v2.slashcommandlist.reconcile.push_err"

	payloads := []protocol.SlashCommandListPayload{
		sampleSlashCommandListPayload("conv-slash-teardown-1", "clear"),
		sampleSlashCommandListPayload("conv-slash-teardown-2", "compact"),
		sampleSlashCommandListPayload("conv-slash-teardown-3", "review"),
	}

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 1)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:                    frames,
		Outbound:                  rec.outbound,
		StaticPriv:                respPriv,
		Devices:                   reg,
		ServerID:                  v2TestServerID,
		Logger:                    logger,
		RetainedSlashCommandLists: func() []protocol.SlashCommandListPayload { return payloads },
	})
	t.Cleanup(stop)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A session value of this reconcile's own making: it reads connID and
	// interactive and nothing else, and Push resolves connID under pushMu, so no
	// state is shared with the manager's Run goroutine.
	mgr.reconcileSlashCommandLists(ctx, &V2Session{connID: "c-v2-teardown", interactive: true})

	if got := strings.Count(logBuf.String(), pushErrEvent); got != 1 {
		t.Errorf("got %d %s records for %d payloads, want exactly 1 (the batch must stop at the first ctx failure, not continue)", got, pushErrEvent, len(payloads))
	}
	if msgs := noiseMsgsForConn(t, rec, "c-v2-teardown"); len(msgs) != 0 {
		t.Errorf("got %d noise_msg for a torn-down context, want 0", len(msgs))
	}
}
