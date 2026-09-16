package relay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #831 inbound new_session → /clear routing fixtures ---

// fakeSessionStarter is a relay-side test double for SessionStarter: it counts
// StartNewSession calls and returns an injectable error. The mutex guards the
// cross-goroutine access (the Run goroutine writes via StartNewSession, the test
// goroutine reads via startCount), mirroring fakeInterrupter.
type fakeSessionStarter struct {
	mu         sync.Mutex
	startCalls int
	convIDs    []string
	err        error
}

func (f *fakeSessionStarter) StartNewSession(conversationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	f.convIDs = append(f.convIDs, conversationID)
	return f.err
}

func (f *fakeSessionStarter) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCalls
}

// startedConvIDs returns the ids handed to StartNewSession, in call order. The
// relay is a courier for this string — it validates nothing and rewrites nothing —
// so what the seam received IS the whole of the relay-side contract (#2099).
func (f *fakeSessionStarter) startedConvIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.convIDs...)
}

// TestV2Session_NewSession_RoutesClearByCapability drives an inbound `new_session`
// frame through the real Frames/Run loop and asserts the manager routes exactly
// one /clear for an interactive conn and zero for a non-interactive one — the
// inbound capability gate (AC #3, AC #4). After the new_session frame, a second
// conn is opened as a barrier: because Frames is a single FIFO channel drained by
// the one Run goroutine, the new_session (enqueued first) is fully handled before
// the barrier conn opens, so startCount is final regardless of the gate's verdict.
func TestV2Session_NewSession_RoutesClearByCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		caps      []string
		wantStart int
	}{
		{"interactive routes one /clear", []string{protocol.CapabilityInteractive}, 1},
		{"non-interactive is inert", nil, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			fake := &fakeSessionStarter{}
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:         frames,
				Outbound:       rec.outbound,
				StaticPriv:     respPriv,
				Devices:        v2PairedRegistry(t, v2TestToken),
				ServerID:       v2TestServerID,
				Logger:         silentLogger(),
				SessionStarter: fake,
			})
			t.Cleanup(stop)

			send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", tc.caps)
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				Type: protocol.TypeNewSession,
				TS:   time.Now().UTC(),
			})

			// Barrier: the new_session is enqueued before this conn's noise_init, so
			// once the barrier conn is open the new_session has been handled.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			if got := fake.startCount(); got != tc.wantStart {
				t.Errorf("startCalls = %d, want %d", got, tc.wantStart)
			}
		})
	}
}

// TestV2Session_NewSession_NilStarterInert proves a nil SessionStarter (foreground
// / pre-wire) makes an interactive new_session inert: no /clear, no panic, the
// manager keeps serving. If handleNewSession mishandled the nil seam, the Run
// goroutine would be dead and the barrier conn would never open (waitConnOpen
// would fail).
func TestV2Session_NewSession_NilStarterInert(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		// SessionStarter intentionally nil.
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})

	// Barrier: opening succeeds only if Run processed the new_session without
	// crashing or hanging on the nil seam.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})
}

// TestV2Session_NewSession_StartErrorTolerated proves a StartNewSession error (no
// live session / mid-teardown) is best-effort: the keystroke still counts as one
// attempted call, and the manager neither crashes nor closes the conn (the
// barrier conn opens afterward).
func TestV2Session_NewSession_StartErrorTolerated(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := &fakeSessionStarter{err: errors.New("no live session")}
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:         frames,
		Outbound:       rec.outbound,
		StaticPriv:     respPriv,
		Devices:        v2PairedRegistry(t, v2TestToken),
		ServerID:       v2TestServerID,
		Logger:         silentLogger(),
		SessionStarter: fake,
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})

	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	if got := fake.startCount(); got != 1 {
		t.Errorf("startCalls = %d, want 1 (attempted despite error)", got)
	}
}

// --- #2099 new_session names the conversation it restarts ---

// TestV2Session_NewSession_HandsNamedConversationToSeam is the relay half of
// #2099: whatever conversation_id the frame carries reaches SessionStarter
// verbatim, and every shape that means "nothing named" reaches it as the empty
// string. The relay deliberately does NO validation — internal/relay imports
// neither internal/conversations nor internal/sessions, so the shape check and the
// registry lookup live in cmd/pyry. That makes "what the seam received" the entire
// relay-side contract, and an id the relay rewrote or dropped is the only way this
// half can fail.
//
// The hostile shapes are here on purpose: a body that is not an object at all, and
// an id that is not a UUID. Both must arrive at the seam unchanged (or as "" when
// they do not decode) rather than being sanitised in transit — sanitising here
// would move the trust boundary into a package that cannot see the registry.
func TestV2Session_NewSession_HandsNamedConversationToSeam(t *testing.T) {
	t.Parallel()

	const namedConv = "22222222-2222-4222-8222-222222222222"

	cases := []struct {
		name    string
		payload []byte
		want    string
	}{
		{"named conversation reaches the seam verbatim",
			mustMarshal(t, protocol.NewSessionPayload{ConversationID: namedConv}), namedConv},
		{"absent payload is the cursor path",
			nil, ""},
		{"empty object is the cursor path",
			[]byte(`{}`), ""},
		{"explicit empty string is the cursor path",
			[]byte(`{"conversation_id":""}`), ""},
		// Wrong-typed rather than truncated: Envelope.Payload is a json.RawMessage,
		// so a syntactically broken body cannot be marshalled into a frame by this
		// harness at all. A well-formed body whose field has the wrong type is the
		// reachable undecodable shape, and it exercises the same tolerated-error arm.
		{"undecodable body is the cursor path, not a dropped frame",
			[]byte(`{"conversation_id":7}`), ""},
		{"body that is not an object is the cursor path",
			[]byte(`["` + namedConv + `"]`), ""},
		{"a non-UUID id is NOT sanitised by the relay",
			[]byte(`{"conversation_id":"../../etc/passwd"}`), "../../etc/passwd"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			fake := &fakeSessionStarter{}
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:         frames,
				Outbound:       rec.outbound,
				StaticPriv:     respPriv,
				Devices:        v2PairedRegistry(t, v2TestToken),
				ServerID:       v2TestServerID,
				Logger:         silentLogger(),
				SessionStarter: fake,
			})
			t.Cleanup(stop)

			send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				Type:    protocol.TypeNewSession,
				TS:      time.Now().UTC(),
				Payload: tc.payload,
			})

			// Barrier: Frames is one FIFO drained by the single Run goroutine, so once
			// this conn is open the new_session above has been fully handled.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			got := fake.startedConvIDs()
			if len(got) != 1 {
				t.Fatalf("StartNewSession calls = %d (%q), want exactly 1 — a payload shape must never "+
					"suppress the call, only change the id it carries", len(got), got)
			}
			if got[0] != tc.want {
				t.Errorf("StartNewSession(%q), want %q", got[0], tc.want)
			}
		})
	}
}

// TestV2Session_NewSession_NamedConversationStillCapabilityGated pins that the
// capability gate keeps running BEFORE the decode. Naming a conversation is not a
// way around the gate: a non-interactive conn's new_session stays inert whether it
// names one or not, and the manager parses none of its bytes.
func TestV2Session_NewSession_NamedConversationStillCapabilityGated(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := &fakeSessionStarter{}
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:         frames,
		Outbound:       rec.outbound,
		StaticPriv:     respPriv,
		Devices:        v2PairedRegistry(t, v2TestToken),
		ServerID:       v2TestServerID,
		Logger:         silentLogger(),
		SessionStarter: fake,
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-plain", nil)
	frames <- sealAppFrameConn(t, send, "c-plain", protocol.Envelope{
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
		Payload: mustMarshal(t, protocol.NewSessionPayload{
			ConversationID: "22222222-2222-4222-8222-222222222222",
		}),
	})

	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	if got := fake.startedConvIDs(); len(got) != 0 {
		t.Errorf("StartNewSession calls = %q, want none: naming a conversation must not bypass the "+
			"interactive capability gate", got)
	}
}

// --- #2443 a refused recorded workspace is reported back to the requester ---

// rotatedConvID is the conversation the seam reports as rotated. It is what the
// reply must carry, and — per handleNewSession's logging contract — what the
// relay's own log record must NOT.
const rotatedConvID = "33333333-3333-4333-8333-333333333333"

// TestV2Session_NewSession_RefusedWorkspaceReplies is #2443's whole relay-side
// contract: a *RotatedWithoutWorkspaceError from the seam becomes exactly one
// coded error frame, correlated to the frame that asked, carrying the conversation
// the seam named and nothing else.
//
// The BARE frame is the one driven here rather than a named one, deliberately.
// That is the path the correlation cannot cover on its own — the client named no
// conversation, so in_reply_to alone cannot tell it which one stayed put — and it
// is why the id is in the payload at all. A named frame reaches the identical code
// with less to prove.
func TestV2Session_NewSession_RefusedWorkspaceReplies(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := &fakeSessionStarter{err: &RotatedWithoutWorkspaceError{ConversationID: rotatedConvID}}
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	logger, logBuf := bufferLogger()
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:         frames,
		Outbound:       rec.outbound,
		StaticPriv:     respPriv,
		Devices:        v2PairedRegistry(t, v2TestToken),
		ServerID:       v2TestServerID,
		Logger:         logger,
		SessionStarter: fake,
	})
	t.Cleanup(stop)

	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	const reqID uint64 = 77
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})

	// Barrier: the reply is forwarded synchronously on the Run goroutine, so once a
	// later conn is open the reply is already recorded.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	msgs := noiseMsgsForConn(t, rec, "c-int")
	if len(msgs) != 1 {
		t.Fatalf("got %d app frame(s) for the requester, want exactly 1 error reply", len(msgs))
	}
	reply := decryptAppFrame(t, msgs[0], recv)
	if reply.Type != protocol.TypeError {
		t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeError)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Fatalf("reply InReplyTo = %v, want a pointer to %d — the client correlates on this and a bare "+
			"frame has nothing else to correlate on", reply.InReplyTo, reqID)
	}
	var p protocol.ErrorPayload
	if err := json.Unmarshal(reply.Payload, &p); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if p.Code != protocol.CodeNewSessionWorkspaceRefused {
		t.Errorf("error Code = %q, want %q", p.Code, protocol.CodeNewSessionWorkspaceRefused)
	}
	if p.Message != msgNewSessionWorkspaceRefused {
		t.Errorf("error Message = %q, want the static %q", p.Message, msgNewSessionWorkspaceRefused)
	}
	if p.Retryable {
		t.Errorf("error Retryable = true, want false: the same stored workspace fails identically " +
			"until the operator repairs it, which a retry does not accomplish")
	}
	if p.ConversationID != rotatedConvID {
		t.Errorf("error ConversationID = %q, want %q — the bare path names no conversation, so the "+
			"reply is the only place the client learns which one rotated", p.ConversationID, rotatedConvID)
	}

	// The reply is UNICAST to the requester. A second interactive conn is a live
	// observer of the same daemon and must see nothing: this is a reply, not a
	// broadcast, and the barrier conn above is that observer.
	if got := noiseMsgsForConn(t, rec, "c-barrier"); len(got) != 0 {
		t.Errorf("the barrier conn received %d app frame(s), want 0 — the refusal answers the conn "+
			"that asked and no other", len(got))
	}

	// handleNewSession does not log this verb's conversation id (its own doc says
	// so); the seam records it below the boundary under its own bound. The id
	// reaches the wire and not the log.
	if s := logBuf.String(); strings.Contains(s, rotatedConvID) {
		t.Errorf("the relay logged the conversation id; only the seam records it:\n%s", s)
	}
}

// TestV2Session_NewSession_SilentOutcomesReplyNothing is AC-3's negative space at
// the relay layer: exactly one seam answer produces a frame, and every other
// outcome leaves the verb as silent as it was before #2443. A client that ignores
// the new frame must see precisely today's behaviour.
//
// The plain-error case carries a path-shaped marker. It proves the discrimination
// is on the TYPE and not on "the error mentions a workspace": a look-alike error
// must fall through to the pre-#2443 best-effort log arm with no reply at all.
func TestV2Session_NewSession_SilentOutcomesReplyNothing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		nilSeam bool
		caps    []string
		seamErr error
	}{
		{name: "a clean rotation owes nothing", caps: []string{protocol.CapabilityInteractive}},
		{
			name:    "a look-alike error is still the best-effort arm",
			caps:    []string{protocol.CapabilityInteractive},
			seamErr: errors.New("recorded workspace /Users/someone/secret-project refused"),
		},
		{name: "a non-interactive conn is inert", caps: nil},
		{name: "a nil seam is inert", nilSeam: true, caps: []string{protocol.CapabilityInteractive}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			var starter SessionStarter
			if !tc.nilSeam {
				starter = &fakeSessionStarter{err: tc.seamErr}
			}
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:         frames,
				Outbound:       rec.outbound,
				StaticPriv:     respPriv,
				Devices:        v2PairedRegistry(t, v2TestToken),
				ServerID:       v2TestServerID,
				Logger:         silentLogger(),
				SessionStarter: starter,
			})
			t.Cleanup(stop)

			send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", tc.caps)
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				ID:   42,
				Type: protocol.TypeNewSession,
				TS:   time.Now().UTC(),
			})

			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			if got := noiseMsgsForConn(t, rec, "c-int"); len(got) != 0 {
				t.Errorf("got %d app frame(s), want 0 — new_session stays fire-and-forget in every "+
					"outcome but the refused-workspace one", len(got))
			}
		})
	}
}

// --- #2477 LateSessionStarter: an outcome that arrives after the handler returned ---

// fakeLateSessionStarter is a SessionStarter that ALSO implements
// LateSessionStarter: it parks the callback instead of answering, so a test can
// report the outcome from its own goroutine at a moment of its choosing — which is
// the whole shape the production starter has, where the callback fires from the
// reset goroutine up to ninety seconds after dispatch.
//
// fakeSessionStarter above deliberately stays plain, so every pre-#2477 test in
// this file keeps exercising handleNewSession's fallback path unchanged.
type fakeLateSessionStarter struct {
	mu      sync.Mutex
	convIDs []string
	armed   chan func(error)
}

func newFakeLateSessionStarter() *fakeLateSessionStarter {
	return &fakeLateSessionStarter{armed: make(chan func(error), 4)}
}

// StartNewSession satisfies SessionStarter so the config field accepts the fake.
// handleNewSession prefers the late form, so reaching this method in a test that
// wired this type means the capability assertion did not fire.
func (f *fakeLateSessionStarter) StartNewSession(conversationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convIDs = append(f.convIDs, "SYNC:"+conversationID)
	return nil
}

func (f *fakeLateSessionStarter) StartNewSessionLate(conversationID string, outcome func(error)) {
	f.mu.Lock()
	f.convIDs = append(f.convIDs, conversationID)
	f.mu.Unlock()
	f.armed <- outcome
}

func (f *fakeLateSessionStarter) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.convIDs...)
}

// awaitCallback returns the parked outcome callback, failing rather than hanging
// when the manager never reached the late seam.
func (f *fakeLateSessionStarter) awaitCallback(t *testing.T) func(error) {
	t.Helper()
	select {
	case fn := <-f.armed:
		return fn
	case <-time.After(3 * time.Second):
		t.Fatalf("handleNewSession never called StartNewSessionLate")
		return nil
	}
}

// awaitReplyForConn polls until connID has received at least one app frame, which
// is how a reply sealed from Run's m.newSessionDone arm is observed. A Frames
// barrier cannot serve: the outcome arrives on a DIFFERENT channel, and Run's
// select picks among ready cases at random, so a later frame being handled proves
// nothing about the outcome having been.
func awaitReplyForConn(t *testing.T, rec *v2Recorder, connID string) []protocol.RoutingEnvelope {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if msgs := noiseMsgsForConn(t, rec, connID); len(msgs) > 0 {
			return msgs
		}
		select {
		case <-deadline:
			t.Fatalf("no app frame reached %s; the deferred outcome never produced its reply", connID)
			return nil
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestV2Session_NewSession_LateRefusalRepliesToRequester is the relay half of the
// fix for the regression PR #2482 shipped: #2443's coded reply survives a starter
// whose rotation outlives the call, because the outcome is carried back to Run and
// answered there rather than being answered early or dropped.
//
// THE TWO HALVES ARE ORDERED, and the order is the assertion. Nothing may be sent
// while the callback is still parked — a reply then would be exactly the "lie the
// client cannot check" RotatedWithoutWorkspaceError forbids — and the reply must
// appear once it fires, correlated to the frame that asked.
func TestV2Session_NewSession_LateRefusalRepliesToRequester(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := newFakeLateSessionStarter()
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	logger, logBuf := bufferLogger()
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:         frames,
		Outbound:       rec.outbound,
		StaticPriv:     respPriv,
		Devices:        v2PairedRegistry(t, v2TestToken),
		ServerID:       v2TestServerID,
		Logger:         logger,
		SessionStarter: fake,
	})
	t.Cleanup(stop)

	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	const reqID uint64 = 2477
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})

	outcome := fake.awaitCallback(t)
	if got := fake.seen(); len(got) != 1 || got[0] != "" {
		t.Fatalf("seam received %v, want exactly one bare (cursor) call — reaching the SYNC: form "+
			"would mean handleNewSession did not assert LateSessionStarter", got)
	}

	// Barrier on Frames: once a later conn is open, Run has finished the new_session
	// frame. The callback is still parked, so no reply may exist yet.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})
	if got := noiseMsgsForConn(t, rec, "c-int"); len(got) != 0 {
		t.Fatalf("the requester got %d app frame(s) while the rotation was still pending, want 0", len(got))
	}

	outcome(&RotatedWithoutWorkspaceError{ConversationID: rotatedConvID})

	msgs := awaitReplyForConn(t, rec, "c-int")
	if len(msgs) != 1 {
		t.Fatalf("got %d app frame(s) for the requester, want exactly 1 error reply", len(msgs))
	}
	reply := decryptAppFrame(t, msgs[0], recv)
	if reply.Type != protocol.TypeError {
		t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeError)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Fatalf("reply InReplyTo = %v, want a pointer to %d — the frame id is captured at dispatch and "+
			"must survive the deferral", reply.InReplyTo, reqID)
	}
	var p protocol.ErrorPayload
	if err := json.Unmarshal(reply.Payload, &p); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if p.Code != protocol.CodeNewSessionWorkspaceRefused {
		t.Errorf("error Code = %q, want %q", p.Code, protocol.CodeNewSessionWorkspaceRefused)
	}
	if p.ConversationID != rotatedConvID {
		t.Errorf("error ConversationID = %q, want %q", p.ConversationID, rotatedConvID)
	}
	// The reply is unicast, on the deferred path exactly as on the inline one.
	if got := noiseMsgsForConn(t, rec, "c-barrier"); len(got) != 0 {
		t.Errorf("the barrier conn received %d app frame(s), want 0", len(got))
	}
	// handleNewSession's no-conversation-id logging rule is not relaxed by the
	// deferral: the id still reaches the wire and not the log.
	if s := logBuf.String(); strings.Contains(s, rotatedConvID) {
		t.Errorf("the conversation id reached a log record; logs are:\n%s", s)
	}
}

// TestV2Session_NewSession_LateCleanOutcomeRepliesNothing pins the nil arm of the
// same path. A deferred rotation that went cleanly owes the client nothing, and
// "nothing" must mean no frame — not an error frame carrying a nil error.
func TestV2Session_NewSession_LateCleanOutcomeRepliesNothing(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := newFakeLateSessionStarter()
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	logger, _ := bufferLogger()
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:         frames,
		Outbound:       rec.outbound,
		StaticPriv:     respPriv,
		Devices:        v2PairedRegistry(t, v2TestToken),
		ServerID:       v2TestServerID,
		Logger:         logger,
		SessionStarter: fake,
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:   9,
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})

	fake.awaitCallback(t)(nil)

	// Two Frames barriers after the outcome: the first may race the newSessionDone
	// arm, the second cannot — Run drains a buffered channel it is already selecting
	// on before it can service two later frames.
	openModalConn(t, mgr, frames, rec, respPub, "c-b1", []string{protocol.CapabilityInteractive})
	openModalConn(t, mgr, frames, rec, respPub, "c-b2", []string{protocol.CapabilityInteractive})
	if got := noiseMsgsForConn(t, rec, "c-int"); len(got) != 0 {
		t.Errorf("a clean deferred rotation produced %d app frame(s), want 0", len(got))
	}
}

// TestV2Session_NewSession_LateOutcomeOnAClosedSessionIsDropped pins
// handleNewSessionDone's staleness guard: a conn torn down while its rotation ran
// must not be sealed against, because that burns a Noise send-nonce for a frame no
// live peer awaits — handleBundleReady's rule, reached by the same off-Run route.
//
// A DIRECT CALL rather than a driven teardown, the posture
// TestV2Session_AppReply_NotOpenPrecedesTransportDown already takes here: driving
// it would mean closing the conn between the dispatch and the outcome, and with
// s.done closed the producer's select picks among ready cases at random, so the
// result might never reach the arm under test. Run is never started, so no
// single-owner invariant is touched, and the guard returns before anything would
// dereference the hand-built session's nil s.send.
func TestV2Session_NewSession_LateOutcomeOnAClosedSessionIsDropped(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	var sent int
	logger, logBuf := bufferLogger()
	mgr, err := NewV2SessionManager(V2SessionConfig{
		Frames:     make(chan protocol.RoutingEnvelope),
		Outbound:   func(protocol.RoutingEnvelope) error { sent++; return nil },
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     logger,
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}

	s := &V2Session{connID: v2TestConnID, state: V2StateClosed}
	mgr.handleNewSessionDone(context.Background(), newSessionResult{
		s:         s,
		inReplyTo: 1,
		err:       &RotatedWithoutWorkspaceError{ConversationID: rotatedConvID},
	})

	if sent != 0 {
		t.Errorf("Outbound sends = %d, want 0 — a torn-down conn must not be sealed against", sent)
	}
	if out := logBuf.String(); !strings.Contains(out, "v2.new_session.stale") {
		t.Errorf("the dropped outcome left no record; logs are:\n%s", out)
	}
}
