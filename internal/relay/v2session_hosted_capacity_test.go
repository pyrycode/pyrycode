package relay

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testHostedCapacityManager(t *testing.T) (*V2SessionManager, *V2Session, *pushQueue) {
	t.Helper()
	s := &V2Session{connID: "held", state: V2StateOpen, nextID: 2}
	q := &pushQueue{}
	q.enqueue(protocol.Envelope{Type: protocol.TypeModelList, Payload: []byte(`{}`)})
	m := &V2SessionManager{cfg: V2SessionConfig{Logger: testLogger(t), ServerID: v2TestServerID}, sessions: map[string]*V2Session{s.connID: s}, queues: map[string]*pushQueue{s.connID: q}, drainCh: make(chan struct{}, 1), hostedChanges: make(chan struct{}, 1)}
	m.initHosted(t.Context(), s)
	s.hosted.inbox.used = hostedQueueLimit
	return m, s, q
}

func TestHostedPushCapacityHold(t *testing.T) {
	m, s, q := testHostedCapacityManager(t)
	barrier := make(chan struct{})
	q.items = append(q.items, queuedEnv{barrier: barrier})
	other := make(chan struct{})
	m.sessions["other"] = &V2Session{connID: "other", state: V2StateOpen}
	m.queues["other"] = &pushQueue{items: []queuedEnv{{barrier: other}}}
	m.drainOnce(t.Context())
	select {
	case <-other:
	default:
		t.Fatal("other connection did not progress")
	}
	m.drainOnce(t.Context())
	if len(q.items) != 2 || q.bytes != 2 || s.nextID != 2 || s.hosted.inbox.used != hostedQueueLimit {
		t.Fatalf("held head mutated: queue=%+v id=%d used=%d", q, s.nextID, s.hosted.inbox.used)
	}
	select {
	case <-barrier:
		t.Fatal("fence overtook held push")
	default:
	}
	// Consume the previous wake; a capacity-held pass must not re-signal itself.
	select {
	case <-m.drainCh:
	default:
	}
	m.drainOnce(t.Context())
	select {
	case <-m.drainCh:
		t.Fatal("full lane busy-spins")
	default:
	}
}

func TestHostedPushCapacityTeardown(t *testing.T) {
	for _, action := range []string{"disconnect", "cancel"} {
		t.Run(action, func(t *testing.T) {
			m, s, q := testHostedCapacityManager(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- m.FlushPushes(ctx) }()
			testHostedAwait(t, m.drainCh)
			m.drainOnce(t.Context())
			if len(q.items) != 2 {
				t.Fatal("flush lost its held predecessor")
			}
			select {
			case err := <-result:
				t.Fatalf("early flush: %v", err)
			default:
			}
			if action == "disconnect" {
				m.teardown(s)
			} else {
				cancel()
			}
			select {
			case err := <-result:
				if action == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel=%v", err)
				}
				if action == "disconnect" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("held flush stranded")
			}
		})
	}
}

func TestHostedPushReservation(t *testing.T) {
	for _, callbackFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "admission_first", true: "callback_first"}[callbackFirst], func(t *testing.T) {
			m, s, _ := testHostedCapacityManager(t)
			in := s.hosted.inbox
			in.used = hostedQueueLimit - 1
			in.subscribed = true
			change := apps.Change{ServerID: v2TestServerID, AppID: "app", Revision: 1}
			if callbackFirst {
				m.HostedAppsChanged(change)
			}
			admitted := m.reserveHostedPush(s)
			if admitted == callbackFirst {
				t.Fatalf("admitted=%v callbackFirst=%v", admitted, callbackFirst)
			}
			if !callbackFirst {
				// Callback runs concurrently with Run-owned admission and sees its slot.
				finished := make(chan struct{})
				go func() { m.HostedAppsChanged(change); close(finished) }()
				testHostedAwait(t, finished)
				if !in.overflow || in.ctx.Err() == nil {
					t.Fatal("genuine callback overflow did not retire")
				}
			} else if in.overflow || len(in.pending) != 1 {
				t.Fatal("callback reservation lost")
			}
			if in.used != hostedQueueLimit {
				t.Fatalf("used=%d", in.used)
			}
			m.releaseHostedPush(s)
			if !callbackFirst && in.used != hostedQueueLimit-1 {
				t.Fatal("unused reservation not released")
			}
		})
	}
	// Filtered output returns its reservation without sealing or charging twice.
	m, s, _ := testHostedCapacityManager(t)
	s.hosted.inbox.used = 0
	if !m.reserveHostedPush(s) {
		t.Fatal("empty lane refused")
	}
	s.thread = true
	if err := m.forwardEnvelope(t.Context(), s.connID, protocol.Envelope{Type: protocol.TypeAssistantDelta}); err != nil {
		t.Fatal(err)
	}
	m.releaseHostedPush(s)
	if s.hosted.inbox.used != 0 || s.nextID != 2 {
		t.Fatal("withheld output spent capacity or sender ID")
	}
}

func TestHostedPushCapacityPendingFence(t *testing.T) {
	m, s, q := testHostedCapacityManager(t)
	q.popHead()
	s.livePending = &LiveState{}
	barrier := make(chan struct{})
	q.items = append(q.items, queuedEnv{barrier: barrier})
	m.drainOnce(t.Context())
	select {
	case <-barrier:
		t.Fatal("fence overtook capacity-held fresh reading")
	default:
	}
	m.signalLiveDrain()
	select {
	case <-m.drainCh:
		t.Fatal("held fresh reading busy-spins")
	default:
	}
}

func TestHostedPushReservationTransfer(t *testing.T) {
	m, s, _ := testHostedCapacityManager(t)
	in := s.hosted.inbox
	in.used = hostedQueueLimit - 2
	in.subscribed = true
	s.hosted.active = &hostedOutput{} // A held delivery prevents sealing another head.
	if !m.reserveHostedPush(s) {
		t.Fatal("slot refused")
	}
	finished := make(chan struct{})
	go func() {
		m.HostedAppsChanged(apps.Change{ServerID: v2TestServerID, AppID: "app", Revision: 1})
		close(finished)
	}()
	testHostedAwait(t, finished)
	env := protocol.Envelope{Type: protocol.TypeModelList, Payload: []byte(`{}`)}
	m.hostedEnqueue(t.Context(), s, &hostedOutput{env: &env})
	m.releaseHostedPush(s)
	if in.used != hostedQueueLimit || in.overflow || s.hosted.pushReserved || len(s.hosted.queue) != 2 || s.hosted.queue[0].typ != protocol.TypeAppRemoved || s.hosted.queue[1].env != &env || s.nextID != 2 {
		t.Fatalf("reservation transfer: used=%d overflow=%v queue=%+v id=%d", in.used, in.overflow, s.hosted.queue, s.nextID)
	}
}

func TestHostedPushAdmissionRetirement(t *testing.T) {
	for _, cause := range []string{"live_state_gap", "byte_ceiling"} {
		t.Run(cause, func(t *testing.T) {
			m, s, q := testHostedCapacityManager(t)
			m.pushOverflow = make(chan string, 1)
			s.hosted.inbox.used = hostedQueueLimit - 1
			s.hosted.active = &hostedOutput{} // Hold delivery so admission is observable without sealing.
			if cause == "byte_ceiling" {
				for len(q.items) < pushQueueCap {
					q.enqueue(protocol.Envelope{Type: protocol.TypeModelList, Payload: []byte(`{}`)})
				}
			}
			items, retained := len(q.items), q.bytes
			finished := make(chan struct{})
			ctx := t.Context()
			// Holding hostedMu parks admission after selection while producers
			// can still take pushMu before the final queue read.
			m.hostedMu.Lock()
			go func() {
				m.drainOnce(ctx)
				close(finished)
			}()
			t.Cleanup(func() { testHostedAwait(t, finished) })
			func() {
				defer m.hostedMu.Unlock()
				testHostedAwaitAdmission(t)
				var err error
				if cause == "live_state_gap" {
					err = m.RetireLiveState(ctx, s.connID)
				} else {
					err = m.Push(ctx, s.connID, protocol.Envelope{Type: protocol.TypeModelList, Payload: make([]byte, pushQueueByteCeiling)})
				}
				if err != nil {
					t.Fatal(err)
				}
				if id := <-m.pushOverflow; id != s.connID {
					t.Fatalf("retired connection = %q", id)
				}
			}()
			testHostedAwait(t, finished)
			if !q.overflowed || len(q.items) != items || q.bytes != retained {
				t.Fatalf("quarantined head escaped: items=%d/%d bytes=%d/%d overflowed=%v", len(q.items), items, q.bytes, retained, q.overflowed)
			}
			if s.hosted.pushReserved || s.hosted.inbox.used != hostedQueueLimit-1 || len(s.hosted.queue) != 0 || s.nextID != 2 {
				t.Fatalf("retired admission retained capacity or output: reserved=%v used=%d queued=%d id=%d", s.hosted.pushReserved, s.hosted.inbox.used, len(s.hosted.queue), s.nextID)
			}
		})
	}
}

// testHostedAwaitAdmission observes reservation waiting on hostedMu: candidate
// selection has completed and the final pop cannot start.
func testHostedAwaitAdmission(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	stack := make([]byte, 64<<10)
	for {
		n := runtime.Stack(stack, true)
		if n == len(stack) {
			stack = make([]byte, 2*len(stack))
			continue
		}
		for _, goroutine := range strings.Split(string(stack[:n]), "\n\n") {
			if strings.Contains(goroutine, "(*V2SessionManager).reserveHostedPush(") && strings.Contains(goroutine, "sync.(*Mutex).Lock(") {
				return
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("drain did not reach the held hosted reservation")
		case <-tick.C:
		}
	}
}

func TestHostedPushCapacityDelivery(t *testing.T) {
	for _, action := range []string{"resume", "disconnect", "cancel"} {
		t.Run(action, func(t *testing.T) {
			p, entered, exited, release := testHostedBlockedOutput(t, &testAppDiscovery{})
			p.request(10, protocol.TypeAppCancel, `{"request_id":999}`)
			testHostedAwait(t, entered)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			for i := range 4 * hostedQueueLimit {
				env := protocol.Envelope{Type: protocol.TypeWorkspaceUpdated, Payload: []byte(fmt.Sprintf(`{"index":%d}`, i))}
				if err := p.mgr.Push(ctx, p.conn, env); err != nil {
					t.Fatal(err)
				}
			}
			flushed := make(chan error, 1)
			go func() { flushed <- p.mgr.FlushPushes(ctx) }()
			select {
			case err := <-flushed:
				t.Fatalf("early capacity flush: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			// Both inbound work on another connection and manager control remain usable.
			other := p.sibling("capacity-progress")
			testHostedPage(t, other, 20, nil)
			waitConnOpen(t, p.mgr, p.conn)
			p.mgr.pushMu.Lock()
			q := p.mgr.queues[p.conn]
			bounded := q != nil && len(q.items) > 1 && len(q.items) <= pushQueueCap && q.bytes < pushQueueByteCeiling && !q.overflowed
			p.mgr.pushMu.Unlock()
			if !bounded {
				t.Fatal("held ordinary queue is absent or outside its bounds")
			}
			if action == "disconnect" {
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
				testHostedAwait(t, exited)
			} else if action == "cancel" {
				cancel()
			} else {
				release()
			}
			select {
			case err := <-flushed:
				if action == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel=%v", err)
				}
				if action != "cancel" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("capacity flush stranded")
			}
			if action == "disconnect" {
				return
			}
			release()
			env, _ := p.next()
			if env.Type != protocol.TypeAppCancelled {
				t.Fatalf("held predecessor: %+v", env)
			}
			previous := env.ID
			for i := range 4 * hostedQueueLimit {
				env, _ = p.next()
				if env.Type != protocol.TypeWorkspaceUpdated || string(env.Payload) != fmt.Sprintf(`{"index":%d}`, i) || env.ID <= previous {
					t.Fatalf("FIFO output %d: %+v", i, env)
				}
				previous = env.ID
			}
		})
	}
}

func TestHostedPushCapacityInlineEnvelope(t *testing.T) {
	m, s, q := testHostedCapacityManager(t)
	env := protocol.Envelope{Type: protocol.TypeWorkspaceUpdated, Payload: []byte(`{"reply":true}`)}
	m.hostedEnvelope(t.Context(), s, env)
	if s.state != V2StateOpen || len(q.items) != 2 || q.bytes != len(env.Payload)+2 || s.hosted.inbox.used != hostedQueueLimit {
		t.Fatal("ordinary inline output retired the full destination instead of waiting upstream")
	}
}

func TestHostedPushCapacityInlineLiveState(t *testing.T) {
	m, s, q := testHostedCapacityManager(t)
	q.popHead()
	s.thread, s.interactive = true, true
	m.cfg.KnownConversation = func(string) bool { return true }
	r := liveReading(protocol.TypeTurnState, "conversation", "", `"session"`, 1, 1)
	if err := m.forwardLiveState(t.Context(), s, r); err != nil {
		t.Fatal(err)
	}
	if len(q.items) != 2 || !q.items[0].ready || !q.items[0].env.SessionStateCleared || q.items[1].live == nil || s.livePending != nil {
		t.Fatal("deferred fresh reading did not follow its clear upstream")
	}
	s.hosted.active = &hostedOutput{}
	m.releaseHosted(s, hostedQueueLimit)
	m.drainOnce(t.Context())
	m.drainOnce(t.Context())
	out := s.hosted.queue
	if len(out) != 2 || !out[0].env.SessionStateCleared || out[1].env.SessionStateCleared || string(out[1].env.Payload) != string(r.Envelope.Payload) || s.hosted.inbox.used != 2 || s.nextID != 2 {
		t.Fatalf("clear/fresh admission: %+v", out)
	}
}
