package msgqueue

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turncommit"
)

type lifecycleFact struct {
	kind    string
	conv    string
	msg     QueuedMessage
	outcome TerminalOutcome
}

type lifecycleRecorder struct {
	mu    sync.Mutex
	facts []lifecycleFact
	fired chan lifecycleFact
}

func testLifecycleRecorder(cfg *Config) *lifecycleRecorder {
	r := &lifecycleRecorder{fired: make(chan lifecycleFact, 32)}
	record := func(f lifecycleFact) {
		r.mu.Lock()
		r.facts = append(r.facts, f)
		r.mu.Unlock()
		r.fired <- f
	}
	cfg.OnAccepted = func(c string, m QueuedMessage) { record(lifecycleFact{kind: "accepted", conv: c, msg: m}) }
	cfg.OnTerminal = func(c string, m QueuedMessage, o TerminalOutcome) {
		record(lifecycleFact{kind: "terminal", conv: c, msg: m, outcome: o})
	}
	cfg.OnDelivered = func(c string, m QueuedMessage) { record(lifecycleFact{kind: "delivered", conv: c, msg: m}) }
	return r
}

func (r *lifecycleRecorder) all() []lifecycleFact {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]lifecycleFact(nil), r.facts...)
}

func testLifecycleRun(t *testing.T, q *Queue) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); recvWithin(t, done, "Run to join drains") }) }
	t.Cleanup(stop)
	return stop
}

func TestQueue_Lifecycle_LegacyAcceptanceAndCapacity(t *testing.T) {
	t.Parallel()
	methods := []struct {
		name    string
		enqueue func(*Queue) uint64
	}{
		{"plain", func(q *Queue) uint64 { return q.Enqueue("c", "text") }},
		{"delivery", func(q *Queue) uint64 { return q.EnqueueDelivery("c", "app", "text", "payload") }},
		{"attached", func(q *Queue) uint64 { return q.EnqueueAttached("c", "app", "text", "payload", []string{"att"}) }},
		{"sent", func(q *Queue) uint64 {
			return q.EnqueueSent("c", "app", "text", "payload", nil, "name", "version", time.Time{})
		}},
	}
	for _, tc := range methods {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Deliver: newFakeDeliver().deliver, MaxQueuedPerConversation: 1}
			r := testLifecycleRecorder(&cfg)
			q, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if id := tc.enqueue(q); id != 1 {
				t.Fatalf("id=%d", id)
			}
			if id := tc.enqueue(q); id != 0 {
				t.Fatalf("capacity rejection id=%d", id)
			}
			facts := r.all()
			if len(facts) != 1 || facts[0].kind != "accepted" || facts[0].msg.DeviceID != "" || facts[0].msg.TS.IsZero() {
				t.Fatalf("acceptance=%+v", facts)
			}
			if !q.Remove("c", 1) {
				t.Fatal("Remove failed")
			}
			if q.Remove("c", 1) {
				t.Fatal("second Remove succeeded")
			}
			if id := tc.enqueue(q); id != 2 {
				t.Fatalf("rejection consumed id: next=%d", id)
			}
			facts = r.all()
			if len(facts) != 3 || facts[1].outcome != TerminalRemoved || facts[2].kind != "accepted" {
				t.Fatalf("facts=%+v", facts)
			}
			if !reflect.DeepEqual(facts[0].msg, facts[1].msg) {
				t.Fatal("removal changed acceptance metadata")
			}
		})
	}
}

func TestQueue_Lifecycle_AcceptanceCompletesBeforeObservers(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"immediate", "before Run", "remove", "send now", "give up"} {
		t.Run(mode, func(t *testing.T) {
			accepted := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var acceptanceDone atomic.Bool
			entered := make(chan struct{}, 8)
			cfg := Config{RetryInterval: time.Millisecond, GiveUpAfter: time.Millisecond,
				Deliver: func(context.Context, string, []byte) error {
					entered <- struct{}{}
					if mode == "give up" {
						return errWedged
					}
					return nil
				},
				SendNow: func(context.Context, string, uint64, []byte) error { return nil },
			}
			r := testLifecycleRecorder(&cfg)
			recordAcceptance := cfg.OnAccepted
			cfg.OnAccepted = func(c string, m QueuedMessage) {
				recordAcceptance(c, m)
				close(accepted)
				<-release
				acceptanceDone.Store(true)
			}
			var q *Queue
			recordTerminal, recordDelivered := cfg.OnTerminal, cfg.OnDelivered
			cfg.OnTerminal = func(c string, m QueuedMessage, o TerminalOutcome) {
				if !acceptanceDone.Load() {
					t.Error("terminal began before acceptance finished")
				}
				recordTerminal(c, m, o)
			}
			cfg.OnDelivered = func(c string, m QueuedMessage) {
				if !acceptanceDone.Load() {
					t.Error("OnDelivered began before acceptance finished")
				}
				for _, waiting := range q.Snapshot(c) {
					if waiting.ID == m.ID {
						t.Error("OnDelivered still in backlog")
					}
				}
				recordDelivered(c, m)
			}
			var err error
			q, err = New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "immediate" || mode == "give up" {
				testLifecycleRun(t, q)
			}
			t.Cleanup(unblock)
			enqueueDone := make(chan uint64, 1)
			go func() { enqueueDone <- q.Enqueue("c", "text") }()
			recvWithin(t, accepted, "acceptance callback")
			switch mode {
			case "before Run":
				testLifecycleRun(t, q)
			case "remove":
				if !q.Remove("c", 1) {
					t.Fatal("Remove failed")
				}
			case "send now":
				if !q.SendNow("c", 1) {
					t.Fatal("SendNow failed")
				}
			}
			if mode == "immediate" || mode == "before Run" || mode == "give up" {
				recvWithin(t, entered, "delivery attempt")
				waitDrainExited(t, q, "c")
			}
			if got := r.all(); len(got) != 1 {
				t.Fatalf("observers ran during acceptance: %+v", got)
			}
			unblock()
			if id := recvWithin(t, enqueueDone, "enqueue completion"); id != 1 {
				t.Fatalf("id=%d", id)
			}
			facts := r.all()
			want := TerminalDelivered
			if mode == "remove" {
				want = TerminalRemoved
			}
			if mode == "give up" {
				want = TerminalGiveUp
			}
			last := facts[len(facts)-1]
			if last.kind != "terminal" || last.outcome != want {
				t.Fatalf("facts=%+v", facts)
			}
			if want == TerminalDelivered && (len(facts) != 3 || facts[1].kind != "delivered") {
				t.Fatalf("legacy delivered missing: %+v", facts)
			}
		})
	}
}

func TestQueue_Lifecycle_IdentityAndCopyIsolation(t *testing.T) {
	t.Parallel()
	sent := time.Date(2026, 10, 8, 1, 2, 3, 0, time.UTC)
	var q *Queue
	var accepted, delivered, terminal QueuedMessage
	done := make(chan struct{}, 1)
	check := func(m QueuedMessage) {
		if m.DeviceID != "stable-device" || m.DeviceName != "display name" || m.ClientVersion != "app/1" || m.ClientSentAt != sent || m.ID != 1 || m.MessageID != "app-id" || m.Text != deliveredText || !reflect.DeepEqual(m.AttachmentIDs, []string{"att"}) {
			t.Errorf("projection=%+v", m)
		}
	}
	reenter := func() { q.Snapshot("c"); q.SnapshotAll() }
	cfg := Config{
		Deliver: func(ctx context.Context, _ string, p []byte) error {
			if string(p) != deliveredPayload {
				t.Errorf("delivery=%q", p)
			}
			m, ok := DeliveryMessage(ctx)
			if !ok {
				t.Error("missing delivery projection")
			}
			check(m)
			m.AttachmentIDs[0] = "attempt mutation"
			m, _ = DeliveryMessage(ctx)
			check(m)
			return nil
		},
		OnAccepted: func(c string, m QueuedMessage) {
			reenter()
			if c != "c" {
				t.Errorf("conversation=%q", c)
			}
			check(m)
			accepted = m
			m.AttachmentIDs[0] = "acceptance mutation"
		},
		OnDelivered: func(_ string, m QueuedMessage) {
			reenter()
			check(m)
			delivered = m
			m.AttachmentIDs[0] = "delivered mutation"
		},
		OnTerminal: func(_ string, m QueuedMessage, o TerminalOutcome) {
			reenter()
			check(m)
			if o != TerminalDelivered {
				t.Errorf("outcome=%s", o)
			}
			terminal = m
			m.AttachmentIDs[0] = "terminal mutation"
			done <- struct{}{}
		},
	}
	var err error
	q, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"att"}
	q.EnqueueIdentified("c", "app-id", deliveredText, deliveredPayload, ids, "stable-device", "display name", "app/1", sent)
	ids[0] = "caller mutation"
	if q.Snapshot("c")[0].DeviceID != "stable-device" || q.SnapshotAll()["c"][0].DeviceID != "stable-device" {
		t.Fatal("snapshot lost identity")
	}
	stop := testLifecycleRun(t, q)
	recvWithin(t, done, "terminal projection")
	stop()
	if accepted.TS != delivered.TS || accepted.TS != terminal.TS || accepted.TS.IsZero() {
		t.Fatal("acceptance time changed")
	}
}

func TestQueue_Lifecycle_RemoveAttemptArbitration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		claim bool
		err   error
		want  TerminalOutcome
	}{
		{"confirmed without claim", false, nil, TerminalDelivered},
		{"cancelled", false, context.Canceled, TerminalRemoved},
		{"claimed refuses removal", true, nil, TerminalDelivered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			cfg := Config{SendNow: func(context.Context, string, uint64, []byte) error { return nil }, Deliver: func(ctx context.Context, _ string, _ []byte) error {
				if tc.claim && !turncommit.From(ctx)() {
					t.Error("claim refused")
				}
				entered <- struct{}{}
				<-release
				return tc.err
			}}
			r := testLifecycleRecorder(&cfg)
			q, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			q.Enqueue("c", "text")
			stop := testLifecycleRun(t, q)
			t.Cleanup(unblock)
			recvWithin(t, entered, "outstanding attempt")
			if removed := q.Remove("c", 1); removed == tc.claim {
				t.Fatalf("Remove=%v, claimed=%v", removed, tc.claim)
			}
			if tc.claim && q.SendNow("c", 1) {
				t.Fatal("SendNow took a committing head")
			}
			if facts := r.all(); len(facts) != 1 {
				t.Fatalf("terminal before attempt resolved: %+v", facts)
			}
			unblock()
			waitDrainExited(t, q, "c")
			stop()
			facts := r.all()
			wantCount := 2
			if tc.want == TerminalDelivered {
				wantCount = 3
			}
			if len(facts) != wantCount || facts[len(facts)-1].outcome != tc.want {
				t.Fatalf("facts=%+v", facts)
			}
		})
	}
}

func TestQueue_Lifecycle_RetryGiveUpAndShutdown(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"retry success", "give up", "stale give up", "shutdown", "confirmed shutdown gap"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{}, 8)
			calls := 0
			var q *Queue
			cfg := Config{RetryInterval: 2 * time.Millisecond, GiveUpAfter: time.Millisecond}
			ctx, cancel := context.WithCancel(context.Background())
			cfg.Deliver = func(ctx context.Context, _ string, _ []byte) error {
				calls++
				entered <- struct{}{}
				switch mode {
				case "retry success":
					if calls < 3 {
						return errWedged
					}
					return nil
				case "shutdown":
					<-ctx.Done()
					return ctx.Err()
				case "confirmed shutdown gap":
					cancel()
					return nil
				default:
					return errWedged
				}
			}
			if mode == "retry success" {
				cfg.GiveUpAfter = time.Second
			}
			if mode == "stale give up" {
				cfg.Pending = func(error) bool {
					if calls == 2 {
						q.Remove("c", 1)
					}
					return false
				}
			}
			r := testLifecycleRecorder(&cfg)
			var err error
			q, err = New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			q.Enqueue("c", "text")
			done := make(chan error, 1)
			go func() { done <- q.Run(ctx) }()
			var once sync.Once
			stop := func() { once.Do(func() { cancel(); recvWithin(t, done, "Run join") }) }
			t.Cleanup(stop)
			recvWithin(t, entered, "delivery attempt")
			if mode != "shutdown" {
				waitDrainExited(t, q, "c")
			}
			stop()
			facts := r.all()
			switch mode {
			case "shutdown", "confirmed shutdown gap":
				if len(facts) != 1 || len(q.Snapshot("c")) != 1 {
					t.Fatalf("shutdown resolved acceptance: %+v", facts)
				}
			default:
				want := TerminalGiveUp
				n := 2
				if mode == "retry success" {
					want = TerminalDelivered
					n = 3
				}
				if mode == "stale give up" {
					want = TerminalRemoved
				}
				if len(facts) != n || facts[n-1].outcome != want {
					t.Fatalf("facts=%+v", facts)
				}
			}
		})
	}
}

func TestQueue_Lifecycle_SendNow(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "failed", "nil seam", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			cfg := Config{Deliver: newFakeDeliver().deliver,
				SendNow: func(ctx context.Context, _ string, _ uint64, _ []byte) error {
					m, ok := DeliveryMessage(ctx)
					if !ok || !m.SentNow || m.DeviceID != "device" {
						t.Errorf("send-now projection=%+v", m)
					}
					if mode == "failed" {
						return errors.New("refused")
					}
					return nil
				},
			}
			if mode == "nil seam" {
				cfg.SendNow = nil
			}
			r := testLifecycleRecorder(&cfg)
			q, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			q.EnqueueIdentified("c", "app", "text", "payload", nil, "device", "name", "version", time.Time{})
			id := uint64(1)
			if mode == "unknown" {
				id = 99
			}
			if got := q.SendNow("c", id); got != (mode == "success") {
				t.Fatalf("SendNow=%v", got)
			}
			facts := r.all()
			if mode == "success" {
				if len(facts) != 3 || facts[2].outcome != TerminalDelivered || !facts[2].msg.SentNow {
					t.Fatalf("facts=%+v", facts)
				}
			} else {
				if len(facts) != 1 || len(q.Snapshot("c")) != 1 {
					t.Fatalf("refused send resolved acceptance: %+v", facts)
				}
				q.Remove("c", 1)
				if facts = r.all(); len(facts) != 2 || facts[1].outcome != TerminalRemoved {
					t.Fatalf("later removal=%+v", facts)
				}
			}
		})
	}
}

func TestQueue_Lifecycle_SendNowOutstandingHead(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			f := newFakeDeliver()
			gate := make(chan struct{})
			f.gates["c"] = gate
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			cfg := Config{Deliver: f.deliver, RetryInterval: time.Millisecond,
				SendNow: func(context.Context, string, uint64, []byte) error {
					if fail {
						return errWedged
					}
					return nil
				},
			}
			r := testLifecycleRecorder(&cfg)
			q, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			// Repeated device/app IDs are metadata, not a deduplication key.
			for want := uint64(1); want <= 2; want++ {
				if id := q.EnqueueIdentified("c", "same-app-id", "text", "payload", nil, "same-device", "name", "v", time.Time{}); id != want {
					t.Fatalf("id=%d, want %d", id, want)
				}
			}
			stop := testLifecycleRun(t, q)
			t.Cleanup(release)
			recvWithin(t, f.entered, "outstanding head")
			if got := q.SendNow("c", 1); got == fail {
				t.Fatalf("SendNow=%v, failure=%v", got, fail)
			}
			release()
			waitDrainExited(t, q, "c")
			stop()
			facts := r.all()
			if len(facts) != 6 {
				t.Fatalf("facts=%+v", facts)
			}
			terminals := map[uint64]QueuedMessage{}
			for _, fact := range facts {
				if fact.kind != "terminal" {
					continue
				}
				if _, exists := terminals[fact.msg.ID]; exists {
					t.Fatalf("duplicate terminal: %+v", facts)
				}
				if fact.outcome != TerminalDelivered {
					t.Fatalf("cancelled send-now attempt resolved as %s", fact.outcome)
				}
				terminals[fact.msg.ID] = fact.msg
			}
			if len(terminals) != 2 || terminals[1].SentNow == fail || terminals[2].SentNow {
				t.Fatalf("terminal projections=%+v", terminals)
			}
		})
	}
}
