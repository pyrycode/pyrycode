package relay

// Tests for the two MCP actuation verbs (#2419). Nearly every case runs against BOTH
// verbs from one table, because the pair shares a handler file, a reject vocabulary
// and a seam: a property proven for mcp_reconnect alone would not stop mcp_toggle
// from drifting away from it.
//
// Two proof traps carried forward from #2381's tests, both recorded in the v2 session
// manager's test-surface overview and both live here:
//
//   - A success fixture whose requested conversation_id EQUALS the seam result's
//     ConversationID stays green when the handler wrongly overwrites the
//     daemon-authored answer with the remote-authored lookup key. The fixture below
//     therefore uses deliberately different values.
//   - Searching logs for payload sentinels alone does not prove decoder errors are
//     absent: Go's type-mismatch text can name the target type without repeating the
//     offending contents. The log test asserts the decoder-error shape directly, and
//     drives the refusal arm with a NON-ZERO poisoned payload so "the handler ignores
//     the payload on false" is actually exercised.

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	mcpActuateKnownConv   = "REMOTE_CONVERSATION_2419"
	mcpActuateUnknownConv = "REMOTE_UNKNOWN_2419"
	mcpActuateBlockedConv = "REMOTE_BLOCKED_2419"
	mcpActuateOtherConv   = "REMOTE_OTHER_2419"
	mcpActuateServerName  = "REMOTE_SERVER_2419"
)

// mcpActuateAccepted is the payload an accepting seam hands back. Its ConversationID
// deliberately DIFFERS from mcpActuateKnownConv: the answer is the daemon's own
// post-acknowledgement read, not an echo of the request's lookup key, and equal
// values would hide a handler that echoed.
var mcpActuateAccepted = protocol.MCPStatusPayload{
	ConversationID: "DAEMON_AUTHORED_CONVERSATION_2419",
	Servers: []protocol.MCPServerStatus{
		{Name: "CLAUDE_SERVER_NAME_2419", Status: "connected", Error: "", Scope: "project", Version: "4.1.9"},
		{Name: "second", Status: "failed", Error: "CLAUDE_SERVER_ERROR_2419", Scope: "local", Version: "0.2.4"},
	},
	DroppedServers: 2,
}

// mcpActuatePoisoned is what a REFUSING seam returns, so the refusal arm proves the
// handler never reads the payload on false rather than merely never crashing on a
// zero one.
var mcpActuatePoisoned = protocol.MCPStatusPayload{
	ConversationID: "POISONED_CONVERSATION_2419",
	Servers: []protocol.MCPServerStatus{{
		Name: "POISONED_SERVER_2419", Status: "connected", Scope: "user", Version: "6.6.6",
	}},
	DroppedServers: 66,
}

// fakeMCPActuator records what crossed the seam and answers programmably. block, when
// non-nil, parks the call after signalling entered — the blocked-seam proof.
type fakeMCPActuator struct {
	mu         sync.Mutex
	reconnects []protocol.MCPReconnectPayload
	toggles    []protocol.MCPTogglePayload
	devs       []*devices.Device

	accept  bool
	payload protocol.MCPStatusPayload

	blockOn string
	entered chan struct{}
	release chan struct{}
}

func (f *fakeMCPActuator) Reconnect(ctx context.Context, p protocol.MCPReconnectPayload, dev *devices.Device) (protocol.MCPStatusPayload, bool) {
	f.mu.Lock()
	f.reconnects = append(f.reconnects, p)
	f.devs = append(f.devs, dev)
	f.mu.Unlock()
	return f.answer(ctx, p.ConversationID)
}

func (f *fakeMCPActuator) SetEnabled(ctx context.Context, p protocol.MCPTogglePayload, dev *devices.Device) (protocol.MCPStatusPayload, bool) {
	f.mu.Lock()
	f.toggles = append(f.toggles, p)
	f.devs = append(f.devs, dev)
	f.mu.Unlock()
	return f.answer(ctx, p.ConversationID)
}

func (f *fakeMCPActuator) answer(ctx context.Context, conversationID string) (protocol.MCPStatusPayload, bool) {
	if f.blockOn != "" && conversationID == f.blockOn {
		select {
		case f.entered <- struct{}{}:
		case <-ctx.Done():
			return protocol.MCPStatusPayload{}, false
		}
		select {
		case <-f.release:
		case <-ctx.Done():
			return protocol.MCPStatusPayload{}, false
		}
	}
	return f.payload, f.accept
}

func (f *fakeMCPActuator) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reconnects) + len(f.toggles)
}

func (f *fakeMCPActuator) lastDevice(t *testing.T) *devices.Device {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.devs) == 0 {
		t.Fatal("seam was never called, so no device crossed it")
	}
	return f.devs[len(f.devs)-1]
}

// mcpActuationVerbCase is one row of the both-verbs table every applicable test runs.
// payload builds a well-formed body; malformed holds two bodies that must not decode.
type mcpActuationVerbCase struct {
	name      string
	typ       string
	payload   func(conversationID string) string
	malformed []string
}

func mcpActuationVerbs() []mcpActuationVerbCase {
	return []mcpActuationVerbCase{
		{
			name: "mcp_reconnect",
			typ:  protocol.TypeMCPReconnect,
			payload: func(conversationID string) string {
				return `{"conversation_id":"` + conversationID + `","server_name":"` + mcpActuateServerName + `"}`
			},
			malformed: []string{
				`{"conversation_id":2419,"server_name":"REMOTE_PAYLOAD_2419"}`,
				`"REMOTE_PAYLOAD_2419"`,
			},
		},
		{
			name: "mcp_toggle",
			typ:  protocol.TypeMCPToggle,
			payload: func(conversationID string) string {
				return `{"conversation_id":"` + conversationID + `","server_name":"` + mcpActuateServerName + `","enabled":true}`
			},
			malformed: []string{
				`{"conversation_id":"c","server_name":"s","enabled":"REMOTE_PAYLOAD_2419"}`,
				`["REMOTE_PAYLOAD_2419"]`,
			},
		},
	}
}

func mcpActuateManagerFor(
	t *testing.T,
	known func(string) bool,
	actuator MCPActuator,
	logger *slog.Logger,
) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 16)
	rec = &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           v2PairedRegistry(t, v2TestToken),
		ServerID:          v2TestServerID,
		Logger:            logger,
		KnownConversation: known,
		MCPActuator:       actuator,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

func sendMCPActuation(t *testing.T, frames chan protocol.RoutingEnvelope, send *noise.CipherState, connID, typ string, id uint64, payload string) {
	t.Helper()
	frames <- sealAppFrameConn(t, send, connID, protocol.Envelope{
		ID:      id,
		Type:    typ,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

// nilActuator is a typed nil used only to keep the config field's interface nil in the
// inert tests below; assigning it would DEFEAT the gate, so the tests pass a literal
// nil instead and this helper exists purely to make that intent unmissable.
func nilActuator() MCPActuator { return nil }

func TestV2Session_MCPActuation_NilSeamConsumesWithoutPayloadDecode(t *testing.T) {
	t.Parallel()
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()
			var knownCalls atomic.Int64
			mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(string) bool {
				knownCalls.Add(1)
				return true
			}, nilActuator(), silentLogger())
			conn := verb.name + "-inert"
			send, _ := openModalConn(t, mgr, frames, rec, respPub, conn, []string{protocol.CapabilityInteractive})

			// An array-valued conversation_id: if anything decoded this payload, the
			// malformed reject would fire and a reply would appear.
			sendMCPActuation(t, frames, send, conn, verb.typ, 24190, `{"conversation_id":["REMOTE_PAYLOAD_2419"]}`)
			openModalConn(t, mgr, frames, rec, respPub, conn+"-barrier", []string{protocol.CapabilityInteractive})

			if got := noiseMsgsForConn(t, rec, conn); len(got) != 0 {
				t.Fatalf("nil seam produced %d application replies, want none", len(got))
			}
			if got := knownCalls.Load(); got != 0 {
				t.Errorf("KnownConversation consulted %d times, want 0", got)
			}
		})
	}
}

func TestV2Session_MCPActuation_NonInteractiveIsInert(t *testing.T) {
	t.Parallel()
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()
			var knownCalls atomic.Int64
			actuator := &fakeMCPActuator{accept: true, payload: mcpActuateAccepted}
			mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(string) bool {
				knownCalls.Add(1)
				return true
			}, actuator, silentLogger())
			conn := verb.name + "-no-cap"
			send, _ := openModalConn(t, mgr, frames, rec, respPub, conn, nil)

			sendMCPActuation(t, frames, send, conn, verb.typ, 24191, verb.payload(mcpActuateKnownConv))
			openModalConn(t, mgr, frames, rec, respPub, conn+"-barrier", []string{protocol.CapabilityInteractive})

			if got := noiseMsgsForConn(t, rec, conn); len(got) != 0 {
				t.Fatalf("non-interactive conn produced %d application replies, want none", len(got))
			}
			if got := knownCalls.Load(); got != 0 {
				t.Errorf("KnownConversation consulted %d times, want 0", got)
			}
			if got := actuator.calls(); got != 0 {
				t.Errorf("seam consulted %d times, want 0", got)
			}
		})
	}
}

func TestV2Session_MCPActuation_MalformedStopsBeforeDependencies(t *testing.T) {
	t.Parallel()
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		for i, payload := range verb.malformed {
			i, payload := i, payload
			t.Run(verb.name+"/"+payload, func(t *testing.T) {
				t.Parallel()
				var knownCalls atomic.Int64
				actuator := &fakeMCPActuator{accept: true, payload: mcpActuateAccepted}
				mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(string) bool {
					knownCalls.Add(1)
					return true
				}, actuator, silentLogger())
				conn := verb.name + "-malformed-" + string(rune('a'+i))
				send, recv := openModalConn(t, mgr, frames, rec, respPub, conn, []string{protocol.CapabilityInteractive})

				sendMCPActuation(t, frames, send, conn, verb.typ, 24192, payload)
				reply := waitMCPStatusReply(t, rec, conn, recv, 0)
				assertMCPStatusError(t, reply, 24192, protocol.CodeProtocolMalformed, false)
				if got := len(noiseMsgsForConn(t, rec, conn)); got != 1 {
					t.Fatalf("malformed request produced %d replies, want exactly 1", got)
				}
				if got := knownCalls.Load(); got != 0 {
					t.Errorf("KnownConversation consulted %d times, want 0", got)
				}
				if got := actuator.calls(); got != 0 {
					t.Errorf("seam consulted %d times, want 0", got)
				}
			})
		}
	}
}

func TestV2Session_MCPActuation_UnknownConversationRejected(t *testing.T) {
	t.Parallel()
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()
			actuator := &fakeMCPActuator{accept: true, payload: mcpActuateAccepted}
			mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(id string) bool {
				return id == mcpActuateKnownConv
			}, actuator, silentLogger())
			conn := verb.name + "-unknown"
			send, recv := openModalConn(t, mgr, frames, rec, respPub, conn, []string{protocol.CapabilityInteractive})

			sendMCPActuation(t, frames, send, conn, verb.typ, 24193, verb.payload(mcpActuateUnknownConv))
			reply := waitMCPStatusReply(t, rec, conn, recv, 0)
			assertMCPStatusError(t, reply, 24193, protocol.CodeConversationNotFound, false)
			if got := len(noiseMsgsForConn(t, rec, conn)); got != 1 {
				t.Fatalf("unknown conversation produced %d replies, want exactly 1", got)
			}
			if got := actuator.calls(); got != 0 {
				t.Errorf("seam consulted %d times, want 0 — membership must precede it", got)
			}
		})
	}
}

// TestV2Session_MCPActuation_RefusalIsOneMergedCode is the fourth acceptance
// criterion's proof. The seam refuses while returning a POISONED payload, so the test
// covers both halves of the contract: one merged non-retryable code, and no byte of a
// refused seam's payload on the wire.
func TestV2Session_MCPActuation_RefusalIsOneMergedCode(t *testing.T) {
	t.Parallel()
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()
			actuator := &fakeMCPActuator{accept: false, payload: mcpActuatePoisoned}
			mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(id string) bool {
				return id == mcpActuateKnownConv
			}, actuator, silentLogger())
			conn := verb.name + "-refused"
			send, recv := openModalConn(t, mgr, frames, rec, respPub, conn, []string{protocol.CapabilityInteractive})

			sendMCPActuation(t, frames, send, conn, verb.typ, 24194, verb.payload(mcpActuateKnownConv))
			reply := waitMCPStatusReply(t, rec, conn, recv, 0)
			assertMCPStatusError(t, reply, 24194, protocol.CodeMCPActuationRefused, false)
			if got := len(noiseMsgsForConn(t, rec, conn)); got != 1 {
				t.Fatalf("refused actuation produced %d replies, want exactly 1", got)
			}
			if got := actuator.calls(); got != 1 {
				t.Errorf("seam consulted %d times, want exactly 1", got)
			}
			if reply.Type == protocol.TypeMCPStatus {
				t.Fatal("refusal sent mcp_status instead of an error")
			}

			// The merged code must not be undermined by the body: no server name the
			// client asked about, and nothing from the refusing seam's payload.
			body := string(reply.Payload)
			for _, secret := range []string{
				mcpActuateServerName,
				"POISONED_CONVERSATION_2419",
				"POISONED_SERVER_2419",
			} {
				if strings.Contains(body, secret) {
					t.Errorf("reject body contains %q: %s", secret, body)
				}
			}

			var got protocol.ErrorPayload
			if err := json.Unmarshal(reply.Payload, &got); err != nil {
				t.Fatalf("decode error payload: %v", err)
			}
			if got.Code == protocol.CodeMCPStatusUnavailable {
				t.Error("refusal reused the read path's mcp_status.unavailable; it needs its own code")
			}
		})
	}
}

func TestV2Session_MCPActuation_AcceptedAnswersCorrelatedStatus(t *testing.T) {
	t.Parallel()
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()
			actuator := &fakeMCPActuator{accept: true, payload: mcpActuateAccepted}
			mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(id string) bool {
				return id == mcpActuateKnownConv
			}, actuator, silentLogger())
			conn := verb.name + "-accepted"
			send, recv := openModalConn(t, mgr, frames, rec, respPub, conn, []string{protocol.CapabilityInteractive})
			_, _ = openModalConn(t, mgr, frames, rec, respPub, conn+"-observer", []string{protocol.CapabilityInteractive})

			sendMCPActuation(t, frames, send, conn, verb.typ, 24195, verb.payload(mcpActuateKnownConv))
			reply := waitMCPStatusReply(t, rec, conn, recv, 0)
			if got := len(noiseMsgsForConn(t, rec, conn)); got != 1 {
				t.Fatalf("accepted actuation produced %d replies, want exactly 1", got)
			}

			if reply.Type != protocol.TypeMCPStatus {
				t.Fatalf("reply type = %q, want existing %q — there is no ack type", reply.Type, protocol.TypeMCPStatus)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != 24195 {
				t.Fatalf("in_reply_to = %v, want pointer to 24195", reply.InReplyTo)
			}
			if reply.EventID != nil {
				t.Errorf("event_id = %v, want nil so the reply never enters the event ring", reply.EventID)
			}

			var got protocol.MCPStatusPayload
			if err := json.Unmarshal(reply.Payload, &got); err != nil {
				t.Fatalf("decode mcp_status payload: %v", err)
			}
			if !reflect.DeepEqual(got, mcpActuateAccepted) {
				t.Errorf("payload = %#v, want the seam's result %#v", got, mcpActuateAccepted)
			}
			// The trap this fixture exists for: the answer is the daemon-authored read,
			// never the remote-authored lookup key.
			if got.ConversationID == mcpActuateKnownConv {
				t.Error("reply echoed the requested conversation_id instead of the seam's own")
			}
			if observer := noiseMsgsForConn(t, rec, conn+"-observer"); len(observer) != 0 {
				t.Fatalf("observer received %d application frames, want none — the reply is unicast", len(observer))
			}
		})
	}
}

// TestV2Session_MCPActuation_PayloadCrossesSeamVerbatim pins that the handler is a
// courier: every decoded field reaches the seam unchanged, alongside the conn's
// authenticated device, and the toggle's flag is carried in both directions including
// the absent-key reading.
func TestV2Session_MCPActuation_PayloadCrossesSeamVerbatim(t *testing.T) {
	t.Parallel()

	t.Run("mcp_reconnect", func(t *testing.T) {
		t.Parallel()
		actuator := &fakeMCPActuator{accept: true, payload: mcpActuateAccepted}
		mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(id string) bool {
			return id == mcpActuateKnownConv
		}, actuator, silentLogger())
		send, recv := openModalConn(t, mgr, frames, rec, respPub, "reconnect-verbatim", []string{protocol.CapabilityInteractive})

		sendMCPActuation(t, frames, send, "reconnect-verbatim", protocol.TypeMCPReconnect, 24196,
			`{"conversation_id":"`+mcpActuateKnownConv+`","server_name":"`+mcpActuateServerName+`"}`)
		_ = waitMCPStatusReply(t, rec, "reconnect-verbatim", recv, 0)

		actuator.mu.Lock()
		got := append([]protocol.MCPReconnectPayload(nil), actuator.reconnects...)
		actuator.mu.Unlock()
		want := protocol.MCPReconnectPayload{ConversationID: mcpActuateKnownConv, ServerName: mcpActuateServerName}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("seam received %#v, want exactly one %#v", got, want)
		}
		if actuator.lastDevice(t) == nil {
			t.Error("seam received a nil device; the conn authenticated one")
		}
	})

	for _, tc := range []struct {
		name    string
		body    string
		enabled bool
	}{
		{"enabled true", `{"conversation_id":"` + mcpActuateKnownConv + `","server_name":"` + mcpActuateServerName + `","enabled":true}`, true},
		{"enabled false", `{"conversation_id":"` + mcpActuateKnownConv + `","server_name":"` + mcpActuateServerName + `","enabled":false}`, false},
		// An omitted key reads as false — "disable", the non-escalating direction.
		{"enabled omitted", `{"conversation_id":"` + mcpActuateKnownConv + `","server_name":"` + mcpActuateServerName + `"}`, false},
	} {
		tc := tc
		t.Run("mcp_toggle/"+tc.name, func(t *testing.T) {
			t.Parallel()
			actuator := &fakeMCPActuator{accept: true, payload: mcpActuateAccepted}
			mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(id string) bool {
				return id == mcpActuateKnownConv
			}, actuator, silentLogger())
			conn := "toggle-verbatim-" + strings.ReplaceAll(tc.name, " ", "-")
			send, recv := openModalConn(t, mgr, frames, rec, respPub, conn, []string{protocol.CapabilityInteractive})

			sendMCPActuation(t, frames, send, conn, protocol.TypeMCPToggle, 24197, tc.body)
			_ = waitMCPStatusReply(t, rec, conn, recv, 0)

			actuator.mu.Lock()
			got := append([]protocol.MCPTogglePayload(nil), actuator.toggles...)
			actuator.mu.Unlock()
			want := protocol.MCPTogglePayload{
				ConversationID: mcpActuateKnownConv,
				ServerName:     mcpActuateServerName,
				Enabled:        tc.enabled,
			}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("seam received %#v, want exactly one %#v", got, want)
			}
			if actuator.lastDevice(t) == nil {
				t.Error("seam received a nil device; the conn authenticated one")
			}
		})
	}
}

// TestV2Session_MCPActuation_BlockedSeamDoesNotStallOtherConnection proves BOTH halves
// of the ownership boundary, per the concurrency overview: a second connection is
// answered while the first is parked in the seam, AND the first connection's eventual
// reply decrypts after release. Either assertion alone misses one side — the first
// without the second would pass if the parked reply were simply lost.
func TestV2Session_MCPActuation_BlockedSeamDoesNotStallOtherConnection(t *testing.T) {
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		t.Run(verb.name, func(t *testing.T) {
			actuator := &fakeMCPActuator{
				accept:  true,
				payload: mcpActuateAccepted,
				blockOn: mcpActuateBlockedConv,
				entered: make(chan struct{}, 1),
				release: make(chan struct{}),
			}
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(actuator.release) }) })

			mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(id string) bool {
				return id == mcpActuateBlockedConv || id == mcpActuateOtherConv
			}, actuator, silentLogger())
			blocked := verb.name + "-blocked"
			free := verb.name + "-free"
			sendA, recvA := openModalConn(t, mgr, frames, rec, respPub, blocked, []string{protocol.CapabilityInteractive})
			sendB, recvB := openModalConn(t, mgr, frames, rec, respPub, free, []string{protocol.CapabilityInteractive})

			sendMCPActuation(t, frames, sendA, blocked, verb.typ, 24198, verb.payload(mcpActuateBlockedConv))
			select {
			case <-actuator.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("blocked seam was not entered")
			}

			sendMCPActuation(t, frames, sendB, free, verb.typ, 24199, verb.payload(mcpActuateOtherConv))
			freeReply := waitMCPStatusReply(t, rec, free, recvB, 0)
			if freeReply.Type != protocol.TypeMCPStatus || freeReply.InReplyTo == nil || *freeReply.InReplyTo != 24199 {
				t.Fatalf("free connection reply = %#v, want correlated mcp_status", freeReply)
			}
			if got := noiseMsgsForConn(t, rec, blocked); len(got) != 0 {
				t.Fatalf("blocked connection replied before release: %d frames", len(got))
			}

			releaseOnce.Do(func() { close(actuator.release) })
			blockedReply := waitMCPStatusReply(t, rec, blocked, recvA, 0)
			if blockedReply.Type != protocol.TypeMCPStatus || blockedReply.InReplyTo == nil || *blockedReply.InReplyTo != 24198 {
				t.Fatalf("blocked connection eventual reply = %#v, want correlated mcp_status", blockedReply)
			}
			if got := len(noiseMsgsForConn(t, rec, blocked)); got != 1 {
				t.Fatalf("blocked connection produced %d replies, want exactly 1", got)
			}
		})
	}
}

// TestV2Session_MCPActuation_InterceptedNotRouted pins that the switch consumes both
// verbs BEFORE dispatch.Route, so neither draws its unknown-type reply: a sentinel
// handler registered under each type must never fire.
func TestV2Session_MCPActuation_InterceptedNotRouted(t *testing.T) {
	t.Parallel()
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()
			var routedToTable atomic.Bool
			respPriv, respPub := genV2Keypair(t)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			sentinel := func(_ context.Context, _ *dispatch.Conn, _ protocol.Envelope) error {
				routedToTable.Store(true)
				return nil
			}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:            frames,
				Outbound:          rec.outbound,
				StaticPriv:        respPriv,
				Devices:           v2PairedRegistry(t, v2TestToken),
				ServerID:          v2TestServerID,
				Logger:            silentLogger(),
				KnownConversation: func(id string) bool { return id == mcpActuateKnownConv },
				MCPActuator:       &fakeMCPActuator{accept: true, payload: mcpActuateAccepted},
				Handlers: map[string]dispatch.Handler{
					protocol.TypeMCPReconnect: sentinel,
					protocol.TypeMCPToggle:    sentinel,
				},
			})
			t.Cleanup(stop)

			conn := verb.name + "-intercept"
			send, recv := openModalConn(t, mgr, frames, rec, respPub, conn, []string{protocol.CapabilityInteractive})
			sendMCPActuation(t, frames, send, conn, verb.typ, 24200, verb.payload(mcpActuateKnownConv))
			reply := waitMCPStatusReply(t, rec, conn, recv, 0)

			if reply.Type != protocol.TypeMCPStatus {
				t.Fatalf("reply type = %q, want %q", reply.Type, protocol.TypeMCPStatus)
			}
			if routedToTable.Load() {
				t.Error("frame reached dispatch.Route; interception must precede it")
			}
		})
	}
}

// TestV2Session_MCPActuation_LogsContainNoRemoteValues drives all four arms — success,
// malformed, unknown conversation, seam refusal — and asserts two separate things.
// The sentinel sweep catches a field echoed verbatim. The decoder-error assertion
// catches what that sweep cannot: encoding/json's type-mismatch text can name the
// target type without repeating the offending value, so a handler that logged err
// would still pass a sentinel-only search.
func TestV2Session_MCPActuation_LogsContainNoRemoteValues(t *testing.T) {
	for _, verb := range mcpActuationVerbs() {
		verb := verb
		t.Run(verb.name, func(t *testing.T) {
			logger, logs := bufferLogger()
			actuator := &fakeMCPActuator{accept: true, payload: mcpActuateAccepted}
			mgr, frames, rec, respPub := mcpActuateManagerFor(t, func(id string) bool {
				return id == mcpActuateKnownConv
			}, actuator, logger)
			conn := verb.name + "-logs"
			send, recv := openModalConn(t, mgr, frames, rec, respPub, conn, []string{protocol.CapabilityInteractive})

			sendMCPActuation(t, frames, send, conn, verb.typ, 24201, verb.payload(mcpActuateKnownConv))
			_ = waitMCPStatusReply(t, rec, conn, recv, 0)
			sendMCPActuation(t, frames, send, conn, verb.typ, 24202, verb.malformed[0])
			_ = waitMCPStatusReply(t, rec, conn, recv, 1)
			sendMCPActuation(t, frames, send, conn, verb.typ, 24203, verb.payload(mcpActuateUnknownConv))
			_ = waitMCPStatusReply(t, rec, conn, recv, 2)

			// The refusal arm needs its own manager: the seam's verdict is fixed per
			// instance, and a refusal returning a poisoned payload is the only way to
			// prove that arm logs none of it.
			refusingLogger, refusingLogs := bufferLogger()
			refusing := &fakeMCPActuator{accept: false, payload: mcpActuatePoisoned}
			mgrR, framesR, recR, respPubR := mcpActuateManagerFor(t, func(id string) bool {
				return id == mcpActuateKnownConv
			}, refusing, refusingLogger)
			connR := verb.name + "-logs-refused"
			sendR, recvR := openModalConn(t, mgrR, framesR, recR, respPubR, connR, []string{protocol.CapabilityInteractive})
			sendMCPActuation(t, framesR, sendR, connR, verb.typ, 24204, verb.payload(mcpActuateKnownConv))
			_ = waitMCPStatusReply(t, recR, connR, recvR, 0)

			gotLogs := logs.String() + refusingLogs.String()
			for _, secret := range []string{
				mcpActuateKnownConv,
				mcpActuateUnknownConv,
				mcpActuateServerName,
				"REMOTE_PAYLOAD_2419",
				"CLAUDE_SERVER_NAME_2419",
				"CLAUDE_SERVER_ERROR_2419",
				"POISONED_CONVERSATION_2419",
				"POISONED_SERVER_2419",
			} {
				if strings.Contains(gotLogs, secret) {
					t.Errorf("logs contain remote- or claude-authored value %q: %s", secret, gotLogs)
				}
			}

			// The decoder-error proof a sentinel sweep cannot give. encoding/json's
			// messages carry these fragments whatever the offending bytes were.
			for _, decoderText := range []string{
				"cannot unmarshal",
				"json:",
				"protocol.MCP",
			} {
				if strings.Contains(gotLogs, decoderText) {
					t.Errorf("logs contain decoder-error text %q: %s", decoderText, gotLogs)
				}
			}
		})
	}
}
