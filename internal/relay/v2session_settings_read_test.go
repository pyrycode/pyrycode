package relay

import (
	"encoding/json"
	"log/slog"
	"sync/atomic"
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
	// The one conversation countingReadSeams' registry knows (#1586). Any other
	// id is a conversation this daemon does not host.
	readKnownConvID = "conv-read-known"
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

// readCounts records how many times each seam was consulted for one manager, so
// a test can assert not merely what the reply said but which seams were read to
// say it — the difference between "the reply is zero" and "the reply is zero
// because nothing was consulted" (#1586).
//
// Every field is atomic, not a plain int: the closures below run on the
// manager's Run dispatch goroutine while the assertions run on the test
// goroutine, so a plain counter is a data race under `go test -race`.
type readCounts struct {
	sessionID atomic.Int64
	settings  atomic.Int64
	usage     atomic.Int64
	lookups   atomic.Int64 // conversations-registry membership checks
}

// runConfig totals the three run-configuration seams — the ones that produce the
// reported values. "No run-configuration seam is consulted at all" is this
// reading zero.
func (c *readCounts) runConfig() int64 {
	return c.sessionID.Load() + c.settings.Load() + c.usage.Load()
}

// countingReadSeams wires the same NON-ZERO fixture constants allReadSeams does,
// plus a registry that knows exactly readKnownConvID, and counts every
// consultation.
//
// The non-zero run-configuration values are load-bearing, not decoration: with
// those seams unwired the zero reply is the same reply whether or not the
// conversation gate exists, so a test built on nil seams would pass with the
// gate deleted and prove nothing. Wiring them is what makes "an unknown
// conversation gets zeros" discriminating.
func countingReadSeams() (readSeams, *readCounts) {
	c := &readCounts{}
	return readSeams{
		sessionID: func() string { c.sessionID.Add(1); return readSessionID },
		settings: func() (string, string, bool) {
			c.settings.Add(1)
			return readModel, readEffort, true
		},
		usage: func() (int, int) { c.usage.Add(1); return readUsedTokens, readWindowTokens },
		knownConv: func(id string) bool {
			c.lookups.Add(1)
			return id == readKnownConvID
		},
	}, c
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

// TestV2Session_RequestSessionSettings_IgnoresAnyPayload pins that a payload
// which is NOT this verb's payload cannot select another session's data: a stale
// field, a hand-rolled frame, or an attacker probing with a path-shaped value is
// answered normally, and the reply carries the seam's values, not the request's.
// The frame does carry one meaningful field now (conversation_id, #1586), but
// this probe names none of it — session_id belongs to the WRITE verb — so it
// decodes to an empty conversation id and takes the answered-as-today path.
// Unrecognised keys stay ignored, and a decode failure is still tolerated rather
// than branching to a malformed reply.
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

// TestV2Session_RequestSessionSettings_ConversationGate pins the conversation
// gate the request's conversation_id drives (#1586). Each row drives one frame
// through the real Frames/Run loop on an interactive conn and asserts both the
// decoded reply AND which seams were consulted to produce it.
//
// The seam counters are the discriminating half. A reply of all zeros is what an
// unwired daemon answers too, so asserting the payload alone would pass with the
// gate deleted; asserting that NO run-configuration seam was read is what proves
// the gate short-circuited ahead of them. Symmetrically, the zero-lookup rows
// prove the empty-id case short-circuits ahead of the registry — that clause is
// what keeps a bare frame from an un-updated client, and a request on a fresh
// daemon that has no conversation id in existence to name, answered as they
// always were (desktop#491).
//
// An unresolvable conversation is answered with the zero payload, never a
// TypeError: session_id "" is already the wire contract's "no session to
// address", so the reply shape stays constant and a client parses one thing.
func TestV2Session_RequestSessionSettings_ConversationGate(t *testing.T) {
	t.Parallel()

	fullReport := protocol.SessionSettingsPayload{
		SessionID:    readSessionID,
		Model:        readModel,
		Effort:       readEffort,
		YOLO:         true,
		UsedTokens:   readUsedTokens,
		WindowTokens: readWindowTokens,
	}

	cases := []struct {
		name          string
		payload       json.RawMessage
		nilKnownConv  bool
		wantPayload   protocol.SessionSettingsPayload
		wantRunConfig bool  // were the three run-configuration seams consulted at all?
		wantLookups   int64 // conversations-registry membership checks
	}{
		{
			name:          "known conversation is answered exactly as today",
			payload:       json.RawMessage(`{"conversation_id":"` + readKnownConvID + `"}`),
			wantPayload:   fullReport,
			wantRunConfig: true,
			wantLookups:   1,
		},
		{
			name:          "unknown conversation gets the zero payload and no run-config read",
			payload:       json.RawMessage(`{"conversation_id":"conv-not-hosted"}`),
			wantPayload:   protocol.SessionSettingsPayload{},
			wantRunConfig: false,
			wantLookups:   1,
		},
		{
			// Unwired seam (foreground / pre-wire): a named id cannot be validated,
			// so it fails closed, matching handleRequestSnapshot's nil-seam posture.
			name:          "a named id cannot be validated without the registry seam",
			payload:       json.RawMessage(`{"conversation_id":"` + readKnownConvID + `"}`),
			nilKnownConv:  true,
			wantPayload:   protocol.SessionSettingsPayload{},
			wantRunConfig: false,
			wantLookups:   0,
		},
		{
			name:          "an empty conversation_id is answered as today, without a lookup",
			payload:       json.RawMessage(`{"conversation_id":""}`),
			wantPayload:   fullReport,
			wantRunConfig: true,
			wantLookups:   0,
		},
		{
			// An un-updated client: no payload at all. json.Unmarshal(nil, …) errors
			// and leaves the id empty, which is the answered-as-today case — the
			// whole backward-compatibility guarantee.
			name:          "no payload at all is answered as today, without a lookup",
			payload:       nil,
			wantPayload:   fullReport,
			wantRunConfig: true,
			wantLookups:   0,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seams, counts := countingReadSeams()
			if tc.nilKnownConv {
				seams.knownConv = nil
			}
			mgr, frames, rec, respPub := readManagerFor(t, seams, silentLogger())
			send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})

			const reqID uint64 = 121
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				ID:      reqID,
				Type:    protocol.TypeRequestSessionSettings,
				TS:      time.Now().UTC(),
				Payload: tc.payload,
			})

			// Barrier: the request is enqueued before this conn's noise_init, so once
			// the barrier conn is open the request has been fully handled.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			msgs := noiseMsgsForConn(t, rec, "c-int")
			if len(msgs) != 1 {
				t.Fatalf("got %d app frame(s), want exactly 1 reply (never a drop)", len(msgs))
			}
			reply := decryptAppFrame(t, msgs[0], recv)
			if reply.Type != protocol.TypeSessionSettings {
				t.Fatalf("reply Type = %q, want %q — an unresolvable conversation degrades to the zero payload, never an error frame", reply.Type, protocol.TypeSessionSettings)
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

			if gotReads := counts.runConfig(); tc.wantRunConfig != (gotReads > 0) {
				t.Errorf("run-configuration seam reads = %d, want %s — a non-addressable request must consult none of them, and an addressable one must report their values",
					gotReads, map[bool]string{true: "> 0", false: "0"}[tc.wantRunConfig])
			}
			if gotLookups := counts.lookups.Load(); gotLookups != tc.wantLookups {
				t.Errorf("conversations-registry lookups = %d, want %d — an unnamed conversation must short-circuit ahead of the registry",
					gotLookups, tc.wantLookups)
			}
		})
	}
}

// TestV2Session_RequestSessionSettings_NonInteractiveMakesNoLookup pins that the
// capability gate sits ahead of BOTH the decode and the conversation lookup: a
// non-interactive conn naming a conversation the daemon really does host gets no
// reply and provokes no registry read, so it cannot learn whether a conversation
// exists (nor whether a session does).
//
// _ReportsRunConfig's "non-interactive is inert" row already pins the no-reply
// half. The no-lookup half is the new claim, and it is the half a reordering
// would break silently: moving the gate below the lookup would still produce no
// reply, so only the counter catches it.
func TestV2Session_RequestSessionSettings_NonInteractiveMakesNoLookup(t *testing.T) {
	t.Parallel()

	seams, counts := countingReadSeams()
	mgr, frames, rec, respPub := readManagerFor(t, seams, silentLogger())
	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-plain", nil)

	const reqID uint64 = 131
	frames <- sealAppFrameConn(t, send, "c-plain", protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRequestSessionSettings,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"` + readKnownConvID + `"}`),
	})
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	if msgs := noiseMsgsForConn(t, rec, "c-plain"); len(msgs) != 0 {
		t.Fatalf("non-interactive conn got %d app frame(s), want 0 (no reply)", len(msgs))
	}
	if got := counts.lookups.Load(); got != 0 {
		t.Errorf("conversations-registry lookups = %d, want 0 — the capability gate must precede the lookup, or a non-interactive conn learns which conversations exist", got)
	}
	if got := counts.runConfig(); got != 0 {
		t.Errorf("run-configuration seam reads = %d, want 0 — a non-interactive conn is fully inert on this verb", got)
	}
}
