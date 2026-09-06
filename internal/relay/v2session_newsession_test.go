package relay

import (
	"errors"
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
