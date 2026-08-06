package relay

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #491 inbound request_session_settings → run-configuration read fixtures ---

const (
	readSessionID    = "sess-read-001"
	readModel        = "claude-opus-4-8"
	readEffort       = "high"
	readUsedTokens   = 12480
	readWindowTokens = 200000
)

// readSeams is the three primitive read seams the run-configuration verb
// consults, grouped so a test can state one intent ("all wired", "none wired",
// "only the id") instead of threading three closures through every call.
type readSeams struct {
	sessionID   func() string
	settings    func() (string, string, bool)
	usage       func() (int, int)
	knownConv   func(string) bool
	snapshotter ScreenSnapshotter
}

// allReadSeams wires all three seams to the fixture constants above — the
// production-shaped case.
func allReadSeams() readSeams {
	return readSeams{
		sessionID: func() string { return readSessionID },
		settings:  func() (string, string, bool) { return readModel, readEffort, true },
		usage:     func() (int, int) { return readUsedTokens, readWindowTokens },
	}
}

// readManagerFor stands up a v2 manager paired for v2TestToken with the given
// read seams and returns it plus the frames channel, recorder, and responder
// public key. Deliberately a sibling of settingsManagerFor rather than a
// parameter on it: the write path and the read path wire disjoint seams, and
// merging them would make every call site pass nils for the other half.
func readManagerFor(t *testing.T, seams readSeams, logger *slog.Logger) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte) {
	t.Helper()
	var respPriv []byte
	respPriv, respPub = genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 8)
	rec = &v2Recorder{}
	var stop func()
	mgr, stop = startManager(t, V2SessionConfig{
		Frames:             frames,
		Outbound:           rec.outbound,
		StaticPriv:         respPriv,
		Devices:            v2PairedRegistry(t, v2TestToken),
		ServerID:           v2TestServerID,
		Logger:             logger,
		BootstrapSessionID: seams.sessionID,
		SnapshotSettings:   seams.settings,
		SnapshotUsage:      seams.usage,
		KnownConversation:  seams.knownConv,
		Snapshotter:        seams.snapshotter,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

// TestV2Session_RequestSessionSettings_ReportsRunConfig drives a bare inbound
// request_session_settings frame through the real Frames/Run loop and asserts the
// reply carries all six values from the seams, correlated to the request id. A
// non-interactive conn is fully inert: zero outbound frames, so it cannot even
// learn whether a session exists — the same authz posture as the write path.
func TestV2Session_RequestSessionSettings_ReportsRunConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		caps        []string
		wantReply   bool
		wantPayload protocol.SessionSettingsPayload
	}{
		{
			name:      "interactive gets the full run configuration",
			caps:      []string{protocol.CapabilityInteractive},
			wantReply: true,
			wantPayload: protocol.SessionSettingsPayload{
				SessionID:    readSessionID,
				Model:        readModel,
				Effort:       readEffort,
				YOLO:         true,
				UsedTokens:   readUsedTokens,
				WindowTokens: readWindowTokens,
			},
		},
		{
			name:      "non-interactive is inert",
			caps:      nil,
			wantReply: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mgr, frames, rec, respPub := readManagerFor(t, allReadSeams(), silentLogger())

			send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", tc.caps)
			const reqID uint64 = 71
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeRequestSessionSettings,
				TS:   time.Now().UTC(),
			})

			// Barrier: the request is enqueued before this conn's noise_init, so once
			// the barrier conn is open the request has been fully handled.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			msgs := noiseMsgsForConn(t, rec, "c-int")
			if !tc.wantReply {
				if len(msgs) != 0 {
					t.Fatalf("non-interactive conn got %d app frame(s), want 0 (no reply)", len(msgs))
				}
				return
			}
			if len(msgs) != 1 {
				t.Fatalf("got %d app frame(s), want exactly 1 reply", len(msgs))
			}
			reply := decryptAppFrame(t, msgs[0], recv)
			if reply.Type != protocol.TypeSessionSettings {
				t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeSessionSettings)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
				t.Errorf("reply InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
			}
			var got protocol.SessionSettingsPayload
			if err := json.Unmarshal(reply.Payload, &got); err != nil {
				t.Fatalf("decode session_settings payload: %v", err)
			}
			if got != tc.wantPayload {
				t.Errorf("payload = %+v, want %+v", got, tc.wantPayload)
			}
		})
	}
}

// TestV2Session_RequestSessionSettings_AnswersWithoutASnapshotter is the
// anti-regression for the whole change, and it states the defect as a contrast.
//
// Both verbs run against ONE manager with a nil Snapshotter — the stream-json
// runner's production shape, where Session.Supervisor() type-asserts to nil and
// cmd/pyry routes that to a nil seam on purpose (#1077/#1101). Against that
// manager:
//
//   - request_snapshot answers TypeError/server.binary_offline. Correct and
//     unchanged: there is genuinely no terminal screen to photograph.
//   - request_session_settings answers with the run configuration anyway.
//
// Before this verb existed the settings had no route of their own: they rode
// screen_snapshot as a side-load (#848, #857), so the first reply's refusal took
// them down with it and the run-configuration UI got nothing on the runner in
// production. If this test ever fails by the second reply becoming an error, the
// read path has been re-coupled to the terminal and desktop#491 is back.
func TestV2Session_RequestSessionSettings_AnswersWithoutASnapshotter(t *testing.T) {
	t.Parallel()

	seams := allReadSeams()
	// Snapshotter stays nil (the stream-mode shape). KnownConversation must be
	// wired to true so request_snapshot reaches its offline branch rather than
	// being turned away earlier as an unknown conversation — otherwise the
	// contrast would prove nothing about the offline gate.
	seams.knownConv = func(string) bool { return true }

	mgr, frames, rec, respPub := readManagerFor(t, seams, silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})

	const snapReqID uint64 = 81
	snapPayload, err := json.Marshal(protocol.RequestSnapshotPayload{ConversationID: "conv-1"})
	if err != nil {
		t.Fatalf("marshal request_snapshot payload: %v", err)
	}
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:      snapReqID,
		Type:    protocol.TypeRequestSnapshot,
		TS:      time.Now().UTC(),
		Payload: snapPayload,
	})

	const readReqID uint64 = 82
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:   readReqID,
		Type: protocol.TypeRequestSessionSettings,
		TS:   time.Now().UTC(),
	})

	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	msgs := noiseMsgsForConn(t, rec, "c-int")
	if len(msgs) != 2 {
		t.Fatalf("got %d app frame(s), want exactly 2 (one per request)", len(msgs))
	}

	// Reply 1: the screen is genuinely unavailable, and stays so.
	snapReply := decryptAppFrame(t, msgs[0], recv)
	if snapReply.Type != protocol.TypeError {
		t.Fatalf("request_snapshot reply Type = %q, want %q — the screen refusal is deliberate and must not be weakened", snapReply.Type, protocol.TypeError)
	}
	var errPayload protocol.ErrorPayload
	if err := json.Unmarshal(snapReply.Payload, &errPayload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if errPayload.Code != protocol.CodeServerBinaryOffline {
		t.Errorf("request_snapshot error code = %q, want %q", errPayload.Code, protocol.CodeServerBinaryOffline)
	}

	// Reply 2: the run configuration is unaffected by the missing screen.
	readReply := decryptAppFrame(t, msgs[1], recv)
	if readReply.Type != protocol.TypeSessionSettings {
		t.Fatalf("request_session_settings reply Type = %q, want %q — a nil Snapshotter must NOT suppress the run configuration (desktop#491)", readReply.Type, protocol.TypeSessionSettings)
	}
	if readReply.InReplyTo == nil || *readReply.InReplyTo != readReqID {
		t.Errorf("reply InReplyTo = %v, want pointer to %d", readReply.InReplyTo, readReqID)
	}
	var got protocol.SessionSettingsPayload
	if err := json.Unmarshal(readReply.Payload, &got); err != nil {
		t.Fatalf("decode session_settings payload: %v", err)
	}
	if got.SessionID != readSessionID {
		t.Errorf("session_id = %q, want %q — without it the client cannot address a change", got.SessionID, readSessionID)
	}
	if got.Model != readModel || got.Effort != readEffort {
		t.Errorf("model/effort = %q/%q, want %q/%q", got.Model, got.Effort, readModel, readEffort)
	}
	if got.WindowTokens != readWindowTokens {
		t.Errorf("window_tokens = %d, want %d — the context figure rides this reply too (#1214)", got.WindowTokens, readWindowTokens)
	}
}

// TestV2Session_RequestSessionSettings_NilSeamsDegradeToZero pins the unwired
// case (foreground / pre-wire): every seam nil yields a NORMAL reply carrying
// zero values, never an error and never a silent drop.
//
// Zero is a real answer here, which is why it is not an error: session_id "" is
// the wire contract's "no session to address, treat the controls as read-only",
// and window_tokens 0 is "usage not wired". Answering an error instead would
// force a client to parse two reply shapes for one question, and answering
// nothing would hang the sheet — the failure mode that made this bug invisible.
func TestV2Session_RequestSessionSettings_NilSeamsDegradeToZero(t *testing.T) {
	t.Parallel()

	mgr, frames, rec, respPub := readManagerFor(t, readSeams{}, silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})

	const reqID uint64 = 91
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRequestSessionSettings,
		TS:   time.Now().UTC(),
	})
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	msgs := noiseMsgsForConn(t, rec, "c-int")
	if len(msgs) != 1 {
		t.Fatalf("got %d app frame(s), want exactly 1 reply (never a drop)", len(msgs))
	}
	reply := decryptAppFrame(t, msgs[0], recv)
	if reply.Type != protocol.TypeSessionSettings {
		t.Fatalf("reply Type = %q, want %q (never an error for an unwired read)", reply.Type, protocol.TypeSessionSettings)
	}
	var got protocol.SessionSettingsPayload
	if err := json.Unmarshal(reply.Payload, &got); err != nil {
		t.Fatalf("decode session_settings payload: %v", err)
	}
	if (got != protocol.SessionSettingsPayload{}) {
		t.Errorf("payload = %+v, want the zero payload", got)
	}
}

// TestV2Session_RequestSessionSettings_IgnoresAnyPayload pins that the request is
// BARE: a client that attaches a payload — a stale field, a hand-rolled frame, or
// an attacker probing for one — is answered normally and its bytes are never
// read. There is no decode step, so there is no malformed-payload branch and no
// field that could select another session's data.
func TestV2Session_RequestSessionSettings_IgnoresAnyPayload(t *testing.T) {
	t.Parallel()

	mgr, frames, rec, respPub := readManagerFor(t, allReadSeams(), silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})

	const reqID uint64 = 101
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRequestSessionSettings,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"session_id":"../../etc/passwd","not_a_field":true}`),
	})
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	msgs := noiseMsgsForConn(t, rec, "c-int")
	if len(msgs) != 1 {
		t.Fatalf("got %d app frame(s), want exactly 1 reply", len(msgs))
	}
	reply := decryptAppFrame(t, msgs[0], recv)
	if reply.Type != protocol.TypeSessionSettings {
		t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeSessionSettings)
	}
	var got protocol.SessionSettingsPayload
	if err := json.Unmarshal(reply.Payload, &got); err != nil {
		t.Fatalf("decode session_settings payload: %v", err)
	}
	if got.SessionID != readSessionID {
		t.Errorf("session_id = %q, want the seam's %q — the request payload must never influence the reply", got.SessionID, readSessionID)
	}
}
