//go:build permission_observe

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/pyrycode/pyrycode/internal/noise"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func permissionEnvelope(typ string) protocol.Envelope {
	return protocol.Envelope{ID: 7, Type: typ, SessionID: json.RawMessage(`"source-session"`), Payload: json.RawMessage(`{"class":"permission","modal_id":"opaque-modal","conversation_id":"source-conversation","prompt":"HOSTILE prompt command credentials tool input"}`)}
}

func permissionRecords(o *permbridge.ObservationRecorder) []permbridge.ObservationRecord {
	var records []permbridge.ObservationRecord
	for _, r := range o.Snapshot().Records {
		if strings.HasPrefix(r.Stage, "queue_") {
			records = append(records, r)
		}
	}
	return records
}

func assertPermissionReasons(t *testing.T, o *permbridge.ObservationRecorder, want ...string) []permbridge.ObservationRecord {
	t.Helper()
	records := permissionRecords(o)
	got := make([]string, len(records))
	for i, r := range records {
		got[i] = r.Reason
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons %v, want %v", got, want)
	}
	return records
}

func TestPermissionQueueDisposition(t *testing.T) {
	for _, mode := range []string{"admission", "eviction", "ceiling", "latched-admission", "latched-rejection", "cancelled", "absent"} {
		t.Run(mode, func(t *testing.T) {
			m, s, _, _ := liveFixture(t, []string{protocol.CapabilityInteractive}, nil)
			o := permbridge.NewObservationRecorder()
			m.ObservePermissions(o)
			q := m.queues[s.connID]
			env := permissionEnvelope(protocol.TypeModalShown)
			ctx := t.Context()
			switch mode {
			case "eviction":
				fillDeltas(q, 0, pushQueueCap)
			case "ceiling", "latched-rejection":
				payload := json.RawMessage(strings.Repeat("x", pushQueueByteCeiling/pushQueueCap))
				for i := 0; i < pushQueueCap; i++ {
					q.enqueue(protocol.Envelope{Type: protocol.TypeTurnState, Payload: payload})
				}
				if mode == "latched-rejection" {
					q.overflowed = true
				}
			case "latched-admission":
				q.overflowed = true
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "absent":
				m.teardown(s)
			}
			before := len(q.items)
			err := m.Push(ctx, s.connID, env)
			switch mode {
			case "cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				assertPermissionReasons(t, o, "cancelled")
			case "absent":
				if !errors.Is(err, ErrConnNotFound) {
					t.Fatal(err)
				}
				assertPermissionReasons(t, o, "absent")
			case "ceiling", "latched-rejection":
				if err != nil || len(q.items) != before || !q.overflowed {
					t.Fatal("ceiling policy changed", err)
				}
				assertPermissionReasons(t, o, "byte_ceiling")
			default:
				if err != nil || q.items[len(q.items)-1].env.ID != env.ID {
					t.Fatal("not retained", err)
				}
				if mode == "eviction" && (q.dropped != 1 || len(q.items) != before) {
					t.Fatal("delta eviction changed")
				}
				if mode == "latched-admission" {
					m.drainOnce(ctx)
					m.teardown(s)
					rs := assertPermissionReasons(t, o, "admitted", "quarantined", "quarantined", "discarded")
					for _, r := range rs {
						if r.Identity != rs[0].Identity {
							t.Fatal("discard changed join")
						}
					}
				} else {
					assertPermissionReasons(t, o, "admitted")
				}
			}
		})
	}
}

func TestPermissionQueueHolds(t *testing.T) {
	for _, hold := range []string{"transport", "replay"} {
		t.Run(hold, func(t *testing.T) {
			connected := true
			m, s, wire, recv := liveFixture(t, []string{protocol.CapabilityInteractive}, func(c *V2SessionConfig) { c.Connected = func() bool { return connected } })
			o := permbridge.NewObservationRecorder()
			m.ObservePermissions(o)
			r := LiveState{Envelope: permissionEnvelope(protocol.TypeModalDismissed), ConversationID: "source-conversation", SessionGeneration: 1, ReadingID: "opaque-modal", Revision: 1}
			if err := m.PushLiveState(t.Context(), s.connID, r); err != nil {
				t.Fatal(err)
			}
			if hold == "transport" {
				connected = false
			} else {
				s.replayQueue = []eventring.Event{{}}
			}
			m.drainOnce(t.Context())
			rs := assertPermissionReasons(t, o, "admitted", "hold_"+hold)
			if len(m.queues[s.connID].items) != 1 || len(wire.snapshot()) != 1 {
				t.Fatal("hold sent or popped")
			}
			origin := rs[0].Identity
			if origin.Envelope == "unknown" {
				t.Fatal("known source lost")
			}
			o.Reset()
			connected = true
			s.replayQueue = nil
			m.drainOnce(t.Context())
			rs = assertPermissionReasons(t, o, "dequeued")
			if rs[0].Stage != "queue_modal_dismissed" || rs[0].Identity != origin || rs[0].Epoch == origin.Epoch {
				t.Fatal("origin lost")
			}
			seen := 1
			e, _ := suppliedWire(t, wire, recv, &seen)
			if e.Type != protocol.TypeModalDismissed || len(m.queues[s.connID].items) != 0 {
				t.Fatal("delivery changed")
			}
		})
	}
}

func TestPermissionQueueIdentity(t *testing.T) {
	o := permbridge.NewObservationRecorder()
	var identities []permbridge.ObservationIdentity
	priv, pub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	wire := &v2Recorder{}
	m, err := NewV2SessionManager(threadConfig(t, make(chan protocol.RoutingEnvelope), wire, priv))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, s := range m.sessions {
			m.teardown(s)
		}
	}()
	m.ObservePermissions(o)
	for i, connID := range []string{"client-a", "client-b", "client-a"} {
		init, err := noise.NewInitiator(initPriv, pub)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := init.WriteInit(buildHelloEarlyDataCaps(t, v2TestToken, []string{protocol.CapabilityInteractive}))
		if err != nil {
			t.Fatal(err)
		}
		m.handleFrame(t.Context(), wrapInnerFrame(t, connID, protocol.TypeNoiseInit, msg))
		session := m.sessions[connID]
		if session == nil || session.state != V2StateOpen {
			t.Fatal("connection not open")
		}
		r := LiveState{Envelope: permissionEnvelope(protocol.TypeModalShown), ConversationID: "source-conversation", SessionGeneration: 1, ReadingID: "opaque-modal", Revision: 1}
		if i == 2 {
			r.SessionGeneration = 2
		}
		if err := m.PushLiveState(t.Context(), connID, r); err != nil {
			t.Fatal(err)
		}
		rs := permissionRecords(o)
		identities = append(identities, rs[len(rs)-1].Identity)
		if i == 0 {
			m.teardown(session)
			rs = permissionRecords(o)
			if rs[len(rs)-1].Identity != identities[len(identities)-1] {
				t.Fatal("discard lost source identity")
			}
		}
	}
	a, b, c := identities[0], identities[1], identities[2]
	if a.Connection == b.Connection || b.Connection == c.Connection || a.Connection == "unknown" {
		t.Fatal("connection lifetime reused")
	}
	if a.Envelope == "unknown" || a.Envelope != b.Envelope || a.Envelope == c.Envelope {
		t.Fatal("source lifetime conflated")
	}
	if a.Session == a.Conversation || a.Modal != b.Modal {
		t.Fatal("identity scope changed")
	}
}

func TestPermissionQueueUnknown(t *testing.T) {
	m, s, _, _ := liveFixture(t, []string{protocol.CapabilityInteractive}, nil)
	o := permbridge.NewObservationRecorder()
	m.ObservePermissions(o)
	env := permissionEnvelope(protocol.TypeModalShown)
	env.SessionID = nil
	if err := m.Push(t.Context(), s.connID, env); err != nil {
		t.Fatal(err)
	}
	rs := assertPermissionReasons(t, o, "admitted")
	if rs[0].Identity.Session != "unknown" || rs[0].Identity.Envelope != "unknown" || rs[0].Identity.Conversation == "unknown" {
		t.Fatal("inferred source/session")
	}
	for _, payload := range []string{`{"class":"trust","modal_id":"other"}`, `{"class":42}`, `not-json`} {
		env.Payload = json.RawMessage(payload)
		if err := m.Push(t.Context(), s.connID, env); err != nil {
			t.Fatal(err)
		}
	}
	assertPermissionReasons(t, o, "admitted")
	for _, payload := range []string{`{"class":"trust","modal_id":"other"}`, `{"class":"permission","modal_id":42}`, `{"class":"permission","modal_id":"HOSTILE"}`} {
		env.Payload = json.RawMessage(payload)
		env.SessionStateCleared = true
		if err := m.Push(t.Context(), s.connID, env); err != nil {
			t.Fatal(err)
		}
	}
	assertPermissionReasons(t, o, "admitted")
	env.SessionStateCleared = false
	env.Payload = json.RawMessage(`{"class":"permission","modal_id":"HOSTILE","conversation_id":null}`)
	if err := m.Push(t.Context(), s.connID, env); err != nil {
		t.Fatal(err)
	}
	rs = assertPermissionReasons(t, o, "admitted", "admitted")
	if rs[1].Identity.Conversation != "unknown" {
		t.Fatal("current binding inferred")
	}
	raw, _ := json.Marshal(o.Snapshot())
	for _, secret := range []string{"HOSTILE", "source-session", "opaque-modal", "source-conversation", "credentials"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("content leaked", secret)
		}
	}
}

func TestPermissionQueueLoss(t *testing.T) {
	m, s, wire, recv := liveFixture(t, []string{protocol.CapabilityInteractive}, nil)
	o := permbridge.NewObservationRecorder()
	m.ObservePermissions(o)
	for i := 0; i < permbridge.ObservationMaxRecords; i++ {
		o.Submit("relay", "seen", permbridge.ObservationIdentity{})
	}
	if err := m.Push(t.Context(), s.connID, permissionEnvelope(protocol.TypeModalShown)); err != nil {
		t.Fatal(err)
	}
	m.drainOnce(t.Context())
	seen := 1
	suppliedWire(t, wire, recv, &seen)
	if !o.Snapshot().Loss["record_capacity"] {
		t.Fatal("silent capacity loss")
	}
	if !m.teardown(s) {
		t.Fatal("teardown stalled")
	}
}

func TestPermissionQueueAttachment(t *testing.T) {
	m, s, _, _ := liveFixture(t, []string{protocol.CapabilityInteractive}, nil)
	first, second := permbridge.NewObservationRecorder(), permbridge.NewObservationRecorder()
	m.ObservePermissions(first)
	if err := m.Push(t.Context(), s.connID, permissionEnvelope(protocol.TypeModalShown)); err != nil {
		t.Fatal(err)
	}
	m.ObservePermissions(second)
	m.ObservePermissions(nil)
	m.teardown(s)
	rs := assertPermissionReasons(t, first, "admitted", "discarded")
	if rs[0].Identity != rs[1].Identity || !first.Snapshot().Loss["incomplete_coverage"] || !second.Snapshot().Loss["incomplete_coverage"] {
		t.Fatal("attachment lost originating evidence")
	}
	// The cancellation observation must not wait for a stalled queue lock.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.ObservePermissions(second)
	m.pushMu.Lock()
	err := m.Push(ctx, "absent", permissionEnvelope(protocol.TypeModalShown))
	m.pushMu.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPermissionQueueDisabled(t *testing.T) {
	m, s, wire, recv := liveFixture(t, []string{protocol.CapabilityInteractive}, nil)
	env := permissionEnvelope(protocol.TypeModalShown)
	if err := m.Push(t.Context(), s.connID, env); err != nil {
		t.Fatal(err)
	}
	observation := m.queues[s.connID].items[0].permissionObservation
	if observation.recorder != nil || observation.identity != (permbridge.ObservationIdentity{}) {
		t.Fatal("disabled retained identity")
	}
	m.drainOnce(t.Context())
	seen := 1
	e, _ := suppliedWire(t, wire, recv, &seen)
	if !reflect.DeepEqual(e.Payload, env.Payload) {
		t.Fatal("disabled altered payload")
	}
}

func TestPermissionQueueContention(t *testing.T) {
	m, s, wire, recv := liveFixture(t, []string{protocol.CapabilityInteractive}, nil)
	o := permbridge.NewObservationRecorder()
	m.ObservePermissions(o)
	id := o.Identity("source-conversation", "source-session", "", "opaque-modal", "")
	for i := 0; i < 400; i++ {
		o.Identity("", "", string(rune(i+1)), "", "")
	}
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			default:
				o.Submit("relay", "seen", id)
			}
		}
	}()
	defer func() { close(quit); <-done }()
	seen := 1
	for i := 0; i < 100; i++ {
		if err := m.Push(t.Context(), s.connID, permissionEnvelope(protocol.TypeModalShown)); err != nil {
			t.Fatal(err)
		}
		m.drainOnce(t.Context())
		suppliedWire(t, wire, recv, &seen)
	}
	if !m.teardown(s) {
		t.Fatal("teardown stalled")
	}
	snapshot := o.Snapshot()
	if !snapshot.Loss["contention"] {
		t.Fatal("contention silently lost")
	}
	if len(snapshot.Records) > permbridge.ObservationMaxRecords || snapshot.IdentityCount > permbridge.ObservationMaxIdentities {
		t.Fatal("unbounded observer")
	}
}
