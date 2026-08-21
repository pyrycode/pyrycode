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
	// The run configuration RunConfigFor reports for readKnownConvID — one
	// conversation's own bound session and its own five values.
	readSessionID    = "sess-read-001"
	readModel        = "claude-opus-4-8"
	readEffort       = "high"
	readUsedTokens   = 12480
	readWindowTokens = 200000
	// The one conversation the fixture RunConfigFor resolves (#1586, re-keyed by
	// #1610). Any other id is a conversation this daemon does not host.
	readKnownConvID = "conv-read-known"

	// The bootstrap-scoped set, deliberately DISJOINT from the conversation-keyed
	// values above. SnapshotSettings / SnapshotUsage stay wired in these fixtures
	// as LEAK DETECTORS: handleRequestSessionSettings must never read them again
	// (#1610), so a re-introduced bootstrap-scoped read shows up as a wrong value
	// in the reply as well as in a counter.
	bootstrapReadModel        = "claude-sonnet-4-0"
	bootstrapReadEffort       = "low"
	bootstrapReadUsedTokens   = 777
	bootstrapReadWindowTokens = 111000
)

// fixtureRunConfig is what RunConfigFor reports for readKnownConvID: the id and
// the five values describing ONE session, which is the whole point of the type
// (#1609).
var fixtureRunConfig = RunConfig{
	SessionID:    readSessionID,
	Model:        readModel,
	Effort:       readEffort,
	YOLO:         true,
	UsedTokens:   readUsedTokens,
	WindowTokens: readWindowTokens,
}

// fixtureReport is fixtureRunConfig as the reply a resolvable request must come
// back with — the six fields cross the handler unchanged.
var fixtureReport = protocol.SessionSettingsPayload{
	SessionID:    fixtureRunConfig.SessionID,
	Model:        fixtureRunConfig.Model,
	Effort:       fixtureRunConfig.Effort,
	YOLO:         fixtureRunConfig.YOLO,
	UsedTokens:   fixtureRunConfig.UsedTokens,
	WindowTokens: fixtureRunConfig.WindowTokens,
}

// poisonedRunConfig is the fixture's REFUSAL return: a non-zero RunConfig handed
// back alongside ok == false. RunConfigFor's doc says a caller MUST NOT read the
// fields when the comma-ok is false, and the production cmd/pyry producer happens
// to zero them — so against a zero-valued refusal a handler that DISCARDED the
// comma-ok would pass every row and prove nothing. Poisoning it makes "fail
// closed on ok == false" a tested property of internal/relay rather than one
// borrowed from cmd/pyry. Ordinary strings: this is a fixture, not a second path
// probe.
var poisonedRunConfig = RunConfig{
	SessionID:    "sess-refused-999",
	Model:        "claude-refused-1-0",
	Effort:       "max",
	YOLO:         true,
	UsedTokens:   4242,
	WindowTokens: 424242,
}

// resolveFixtureConv is the RunConfigFor double every fixture below shares: it
// resolves readKnownConvID and refuses — poisoned — every other id.
func resolveFixtureConv(id string) (RunConfig, bool) {
	if id != readKnownConvID {
		return poisonedRunConfig, false
	}
	return fixtureRunConfig, true
}

// readSeams is the read seams a run-configuration test wires, grouped so it can
// state one intent ("all wired", "none wired") instead of threading four closures
// through every call. runConfig is the one this verb consults; settings and usage
// are the bootstrap-scoped pair it must NOT (they serve handleRequestSnapshot,
// and are wired here only as leak detectors), and knownConv is that handler's
// membership gate.
type readSeams struct {
	runConfig   func(string) (RunConfig, bool)
	settings    func() (string, string, bool)
	usage       func() (int, int)
	knownConv   func(string) bool
	snapshotter ScreenSnapshotter
}

// allReadSeams wires the conversation-keyed seam to the fixture RunConfig and the
// two bootstrap-scoped seams to their own disjoint constants — the
// production-shaped case after #1610.
func allReadSeams() readSeams {
	return readSeams{
		runConfig: resolveFixtureConv,
		settings: func() (string, string, bool) {
			return bootstrapReadModel, bootstrapReadEffort, false
		},
		usage: func() (int, int) { return bootstrapReadUsedTokens, bootstrapReadWindowTokens },
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
	resolves atomic.Int64 // conversation-keyed RunConfigFor resolutions
	settings atomic.Int64
	usage    atomic.Int64
}

// bootstrapReads totals the two bootstrap-scoped seams this verb must never read
// again (#1610). It is wanted at 0 unconditionally, and it goes red under exactly
// ONE mutation class — a re-introduced bootstrap-scoped read. Do not lean on it
// for any other property.
func (c *readCounts) bootstrapReads() int64 {
	return c.settings.Load() + c.usage.Load()
}

// countingReadSeams wires the same NON-ZERO fixture values allReadSeams does and
// counts every consultation.
//
// The non-zero values are load-bearing, not decoration: with the seams unwired
// the zero reply is the same reply whether or not the conversation gate exists,
// so a test built on nil seams would pass with the gate deleted and prove
// nothing. Wiring them is what makes "an unresolvable conversation gets zeros"
// discriminating — and wiring the bootstrap pair to DIFFERENT values is what
// makes "and it got them from nowhere else" discriminating.
func countingReadSeams() (readSeams, *readCounts) {
	c := &readCounts{}
	return readSeams{
		runConfig: func(id string) (RunConfig, bool) {
			c.resolves.Add(1)
			return resolveFixtureConv(id)
		},
		settings: func() (string, string, bool) {
			c.settings.Add(1)
			return bootstrapReadModel, bootstrapReadEffort, false
		},
		usage: func() (int, int) {
			c.usage.Add(1)
			return bootstrapReadUsedTokens, bootstrapReadWindowTokens
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
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           v2PairedRegistry(t, v2TestToken),
		ServerID:          v2TestServerID,
		Logger:            logger,
		RunConfigFor:      seams.runConfig,
		SnapshotSettings:  seams.settings,
		SnapshotUsage:     seams.usage,
		KnownConversation: seams.knownConv,
		Snapshotter:       seams.snapshotter,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

// TestV2Session_RequestSessionSettings_ReportsRunConfig drives an inbound
// request_session_settings frame NAMING a conversation the fixture resolves
// through the real Frames/Run loop, and asserts the reply carries all six of that
// conversation's values, correlated to the request id. A non-interactive conn is
// fully inert: zero outbound frames, so it cannot even learn whether a session
// exists — the same authz posture as the write path.
//
// The reply must carry the CONVERSATION-keyed fixture values, never the disjoint
// bootstrap-scoped ones the same manager still wires for handleRequestSnapshot
// (#1610).
func TestV2Session_RequestSessionSettings_ReportsRunConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		caps        []string
		wantReply   bool
		wantPayload protocol.SessionSettingsPayload
	}{
		{
			name:        "interactive gets the named conversation's run configuration",
			caps:        []string{protocol.CapabilityInteractive},
			wantReply:   true,
			wantPayload: fixtureReport,
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
				ID:      reqID,
				Type:    protocol.TypeRequestSessionSettings,
				TS:      time.Now().UTC(),
				Payload: json.RawMessage(`{"conversation_id":"` + readKnownConvID + `"}`),
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
	// contrast would prove nothing about the offline gate. It is that handler's
	// gate alone now; the read verb resolves through RunConfigFor (#1610), which
	// is why the read frame below must NAME the conversation the fixture resolves.
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
		ID:      readReqID,
		Type:    protocol.TypeRequestSessionSettings,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"` + readKnownConvID + `"}`),
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
//
// The frame MUST name a non-empty conversation. Since #1610 an unnamed request
// short-circuits at the empty-id guard and never reaches a seam at all, so a bare
// frame here would stay green while testing nothing — it would pass on the
// unaddressable path instead of on the nil seam this test exists for.
func TestV2Session_RequestSessionSettings_NilSeamsDegradeToZero(t *testing.T) {
	t.Parallel()

	mgr, frames, rec, respPub := readManagerFor(t, readSeams{}, silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})

	const reqID uint64 = 91
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRequestSessionSettings,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"` + readKnownConvID + `"}`),
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
// conversation_id is the ONE field of this frame that selects a
// session (#1586, re-keyed by #1610), and the probe carries it alongside two
// fields that must not: session_id belongs to the WRITE verb, and not_a_field
// belongs to nothing. The path-shaped value must never reach the reply,
// unrecognised keys stay ignored, and a decode failure is still tolerated rather
// than branching to a malformed reply.
func TestV2Session_RequestSessionSettings_IgnoresAnyPayload(t *testing.T) {
	t.Parallel()

	mgr, frames, rec, respPub := readManagerFor(t, allReadSeams(), silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})

	const reqID uint64 = 101
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRequestSessionSettings,
		TS:   time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"` + readKnownConvID +
			`","session_id":"../../etc/passwd","not_a_field":true}`),
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
// gate the request's conversation_id drives (#1586, re-keyed by #1610). Each row
// drives one frame through the real Frames/Run loop on an interactive conn and
// asserts both the decoded reply AND which seams were consulted to produce it.
//
// The resolves counter is the discriminating half. A reply of all zeros is what
// an unwired daemon answers too, so asserting the payload alone would pass with
// the gate deleted; asserting that the conversation-keyed seam was not consulted
// at all is what proves a request naming nothing short-circuits ahead of it.
//
// The bootstrap-read assertion is the other half, and it is wanted at ZERO on
// every row — so it is asserted unconditionally rather than carried as a column
// of constants. It goes red under exactly one mutation, a re-introduced
// bootstrap-scoped read, which is the defect this ticket closes: before #1610 the
// reported values were the bootstrap session's whichever conversation was named.
//
// Mutation coverage this table buys — do not prune a row as redundant:
//   - dropping the empty-id guard reddens rows 4 and 5 on resolves;
//   - dropping the nil-seam guard panics row 3;
//   - discarding RunConfigFor's comma-ok reddens row 2 on the payload, via
//     poisonedRunConfig;
//   - re-introducing a bootstrap-scoped read reddens rows 2–5 on both the
//     bootstrap-read assertion and the payload.
//
// An unresolvable conversation is answered with the zero payload, never a
// TypeError: session_id "" is already the wire contract's "no session to
// address", so the reply shape stays constant and a client parses one thing.
func TestV2Session_RequestSessionSettings_ConversationGate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		payload      json.RawMessage
		nilRunConfig bool
		wantPayload  protocol.SessionSettingsPayload
		wantResolves int64 // conversation-keyed RunConfigFor calls
	}{
		{
			name:         "a hosted, bound conversation reports its own run configuration",
			payload:      json.RawMessage(`{"conversation_id":"` + readKnownConvID + `"}`),
			wantPayload:  fixtureReport,
			wantResolves: 1,
		},
		{
			// The seam refuses with a POISONED non-zero RunConfig, so this row is also
			// the comma-ok proof: a handler that read the fields anyway would report
			// poisonedRunConfig here instead of zeros.
			name:         "a conversation this daemon does not host gets the zero payload",
			payload:      json.RawMessage(`{"conversation_id":"conv-not-hosted"}`),
			wantPayload:  protocol.SessionSettingsPayload{},
			wantResolves: 1,
		},
		{
			// Unwired seam (foreground / v1): a named id cannot be resolved, so it
			// fails closed, matching handleRequestSnapshot's nil-seam posture.
			name:         "a named id cannot be resolved without the seam",
			payload:      json.RawMessage(`{"conversation_id":"` + readKnownConvID + `"}`),
			nilRunConfig: true,
			wantPayload:  protocol.SessionSettingsPayload{},
			wantResolves: 0,
		},
		{
			// An empty id names no conversation, so there is nothing to describe. The
			// guard lives in internal/relay rather than being left to the producer,
			// which keeps this a property of this package — and it is the relay-side
			// half of the Pool.Lookup("") == bootstrap hazard (#678).
			name:         "an empty conversation_id addresses nothing, without a resolution",
			payload:      json.RawMessage(`{"conversation_id":""}`),
			wantPayload:  protocol.SessionSettingsPayload{},
			wantResolves: 0,
		},
		{
			// An un-updated client: no payload at all. json.Unmarshal(nil, …) errors
			// and leaves the id empty, which reaches the same place — a bare frame
			// names no conversation, so it addresses none.
			name:         "no payload at all addresses nothing, without a resolution",
			payload:      nil,
			wantPayload:  protocol.SessionSettingsPayload{},
			wantResolves: 0,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seams, counts := countingReadSeams()
			if tc.nilRunConfig {
				seams.runConfig = nil
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

			if gotResolves := counts.resolves.Load(); gotResolves != tc.wantResolves {
				t.Errorf("RunConfigFor resolutions = %d, want %d — a request naming no conversation must short-circuit ahead of the seam, and one naming a conversation must resolve exactly once",
					gotResolves, tc.wantResolves)
			}
			if gotBootstrap := counts.bootstrapReads(); gotBootstrap != 0 {
				t.Errorf("bootstrap-scoped seam reads = %d, want 0 — this verb reports the NAMED conversation's session, so no bootstrap-scoped run-configuration source may be read for any request (#1610)",
					gotBootstrap)
			}
		})
	}
}

// TestV2Session_RequestSessionSettings_NonInteractiveMakesNoLookup pins that the
// capability gate sits ahead of BOTH the decode and the conversation resolution:
// a non-interactive conn naming a conversation the daemon really does host gets
// no reply and provokes no RunConfigFor call, so it cannot learn whether a
// conversation exists (nor whether a session does).
//
// _ReportsRunConfig's "non-interactive is inert" row already pins the no-reply
// half. The no-resolution half is the new claim, and it is the half a reordering
// would break silently: moving the gate below the resolution would still produce
// no reply, so only the counter catches it.
//
// The counter MUST be on RunConfigFor — the seam this handler consults since
// #1610 — and not on KnownConversation, which it no longer calls. A counter on a
// seam the handler never reaches reads zero unconditionally, so this test would
// stay green with the gate moved below the resolution: exactly the reordering the
// paragraph above says only the counter catches.
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
	if got := counts.resolves.Load(); got != 0 {
		t.Errorf("RunConfigFor resolutions = %d, want 0 — the capability gate must precede the resolution, or a non-interactive conn learns which conversations exist", got)
	}
	if got := counts.bootstrapReads(); got != 0 {
		t.Errorf("bootstrap-scoped seam reads = %d, want 0 — a non-interactive conn is fully inert on this verb", got)
	}
}
