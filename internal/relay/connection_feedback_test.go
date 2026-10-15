package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/transport"
)

type testWakeLog struct {
	*testHostedPumpGate
	mu   sync.Mutex
	text string
}

func (l *testWakeLog) Handle(ctx context.Context, r slog.Record) error {
	l.mu.Lock()
	l.text += r.Message
	r.Attrs(func(a slog.Attr) bool { l.text += fmt.Sprint(a.Value.Any()); return true })
	l.mu.Unlock()
	return l.testHostedPumpGate.Handle(ctx, r)
}

type testWakeLeg struct {
	t       *testing.T
	c       *Connection
	socket  *websocket.Conn
	sockets chan *websocket.Conn
	wires   chan []byte
	log     *testWakeLog
	cancel  context.CancelFunc
}

func testWakeTake[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("barrier timeout")
		var zero T
		return zero
	}
}
func newWakeLeg(t *testing.T) *testWakeLeg {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	p := &testWakeLeg{t: t, sockets: make(chan *websocket.Conn, 8), wires: make(chan []byte, 256), cancel: cancel}
	p.log = &testWakeLog{testHostedPumpGate: &testHostedPumpGate{testHostedLog: testHostedLog{events: make(chan testHostedEvent, 2048)}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		p.sockets <- ws
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			select {
			case p.wires <- raw:
			case <-ctx.Done():
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	logger := slog.New(p.log)
	client := transport.New(transport.Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Logger: logger, WriteTimeout: 5 * time.Second})
	p.c = connectWithClient(ctx, Config{Logger: logger}, client)
	t.Cleanup(func() { cancel(); _ = p.c.Close(); _ = p.c.Wait() })
	p.socket = testWakeTake(t, p.sockets)
	testWakeTake(t, p.c.Reconnected())
	return p
}
func (p *testWakeLeg) write(raw string) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(p.t.Context(), 5*time.Second)
	defer cancel()
	if err := p.socket.Write(ctx, websocket.MessageText, []byte(raw)); err != nil {
		p.t.Fatal(err)
	}
}
func (p *testWakeLeg) barrier() {
	p.t.Helper()
	p.write(`{"conn_id":"barrier","frame":{"marker":"inner-secret-distinct"}}`)
	if env := testWakeTake(p.t, p.c.Frames()); env.ConnID != "barrier" {
		p.t.Fatal("result entered phone dispatch")
	}
}
func (p *testWakeLeg) submit(ctx context.Context, snapshot string) (<-chan string, string) {
	p.t.Helper()
	ch, err := p.c.SendPushWakeFeedback(ctx, "fcm", "device-secret-distinct", snapshot)
	if err != nil {
		p.t.Fatal(err)
	}
	raw := testWakeTake(p.t, p.wires)
	var e struct {
		Wake struct {
			Platform, Token string
			ID              string `json:"request_id"`
		} `json:"push_wake"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Wake.Platform != "fcm" || e.Wake.Token != "device-secret-distinct" || strings.Contains(string(raw), snapshot) || strings.Contains(string(raw), "conn_id") || strings.Contains(string(raw), "frame") {
		p.t.Fatal("incorrect request wire")
	}
	if e.Wake.ID != "" && !validWakeID(e.Wake.ID) {
		p.t.Fatal("invalid generated ID")
	}
	return ch, e.Wake.ID
}
func testWakeSilent(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected notification")
		}
	default:
	}
}
func testWakeClosed(t *testing.T, ch <-chan string) {
	t.Helper()
	done := make(chan bool, 1)
	go func() { _, ok := <-ch; done <- ok }()
	if testWakeTake(t, done) {
		t.Fatal("retirement notified")
	}
}
func (p *testWakeLeg) result(id string) {
	p.write(`{"push_wake_result":{"request_id":"` + id + `","code":"invalid_token"}}`)
}

func TestWakeFeedbackResults(t *testing.T) {
	p := newWakeLeg(t)
	ch, id := p.submit(t.Context(), "snapshot-secret-distinct")
	for _, raw := range []string{
		`{`, `null`, `[]`, `{"push_wake_result":null}`, `{"push_wake_result":[]}`, `{"push_wake_result":"provider-secret-distinct"}`,
		`{"push_wake_result":{}}`, `{"push_wake_result":{"code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":null,"code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":3,"code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"","code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"` + strings.Repeat("a", 65) + `","code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"é","code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"bad!","code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"unknown","code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":null}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":{}}}`,
		`{"push_wake_result":{"request_id":"` + id + `"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":"success"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":"access-secret-distinct","hash":"hash-secret-distinct"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":"invalid_token","code":5}}`,
		`{"push_wake_result":{"request_id":"` + id + `","request_id":{},"code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","request_id":null,"code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":"invalid_token","code":null}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":"invalid_token"},"malformed-secret-distinct":`,
		`{"conn_id":{},"token":"admission-secret-distinct","frame":"inner-secret-distinct"}`,
	} {
		p.write(raw)
		p.barrier()
		testWakeSilent(t, ch)
	}
	p.write(`{"push_wake_result":{"request_id":"` + id + `","code":"invalid_token","token":"access-secret-distinct","provider":"provider-secret-distinct"},"extra":true}`)
	p.barrier()
	if got := testWakeTake(t, ch); got != "snapshot-secret-distinct" {
		t.Fatal("lost original context")
	}
	p.result(id)
	p.barrier()
	testWakeClosed(t, ch)
	p.log.mu.Lock()
	defer p.log.mu.Unlock()
	for _, marker := range []string{"device-secret-distinct", "snapshot-secret-distinct", "admission-secret-distinct", "access-secret-distinct", "provider-secret-distinct", "inner-secret-distinct", "hash-secret-distinct", "malformed-secret-distinct"} {
		if strings.Contains(p.log.text, marker) {
			t.Fatal("secret in diagnostics")
		}
	}
	for _, id := range []string{"A", strings.Repeat("Z", 64), "a-Z_09"} {
		if !validWakeID(id) {
			t.Fatal("valid syntax rejected")
		}
	}
}

func TestWakeFeedbackWriteOrdering(t *testing.T) {
	for _, outcome := range []string{"success", "cancel", "peer_failure", "expiry"} {
		t.Run(outcome, func(t *testing.T) {
			p := newWakeLeg(t)
			now := time.Now()
			p.c.wakeMu.Lock()
			p.c.wakeNow = func() time.Time { return now }
			p.c.wakeMu.Unlock()
			entered, release := p.log.arm(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type response struct {
				ch  <-chan string
				err error
			}
			returned := make(chan response, 1)
			go func() {
				ch, err := p.c.SendPushWakeFeedback(ctx, "fcm", "device-secret-distinct", "snapshot")
				returned <- response{ch, err}
			}()
			testWakeTake(t, entered)
			p.c.wakeMu.Lock()
			b := p.c.wakes["wake-1"]
			p.c.wakeMu.Unlock()
			if b == nil {
				t.Fatal("binding missing before write")
			}
			p.result("wake-1")
			p.barrier()
			testWakeSilent(t, b.result)
			if outcome == "cancel" {
				cancel()
			}
			if outcome == "peer_failure" {
				_ = p.socket.Close(websocket.StatusPolicyViolation, "provider-secret-distinct")
			}
			if outcome == "expiry" {
				p.c.wakeMu.Lock()
				now = now.Add(60 * time.Second)
				p.c.wakeMu.Unlock()
			}
			release()
			got := testWakeTake(t, returned)
			if outcome == "cancel" || outcome == "peer_failure" {
				if got.err == nil || strings.Contains(got.err.Error(), "secret-distinct") {
					t.Fatal("unsafe or missing send error")
				}
				testWakeClosed(t, b.result)
				p.log.mu.Lock()
				leaked := strings.Contains(p.log.text, "secret-distinct")
				p.log.mu.Unlock()
				if leaked {
					t.Fatal("write failure leaked peer or request content")
				}
			} else if outcome == "expiry" {
				if got.err != nil {
					t.Fatal(got.err)
				}
				testWakeClosed(t, got.ch)
			} else {
				if got.err != nil {
					t.Fatal(got.err)
				}
				if testWakeTake(t, got.ch) != "snapshot" {
					t.Fatal("immediate reply lost")
				}
			}
		})
	}
}

func TestWakeFeedbackGenerations(t *testing.T) {
	a, b := newWakeLeg(t), newWakeLeg(t)
	ca, ia := a.submit(t.Context(), "A")
	cb, ib := b.submit(t.Context(), "B")
	if ia != ib {
		t.Fatal("fixture must exercise equal IDs")
	}
	old := a.c.client.Generation()
	a.c.client.DropConn()
	testWakeClosed(t, ca)
	a.socket = testWakeTake(t, a.sockets)
	testWakeTake(t, a.c.Reconnected())
	ca2, ia2 := a.submit(t.Context(), "A2")
	if ia2 == ia {
		t.Fatal("ID reused")
	}
	a.c.consumeWakeResult(old, json.RawMessage(`{"request_id":"`+ia2+`","code":"invalid_token"}`))
	a.result(ia)
	a.barrier()
	testWakeSilent(t, ca2)
	b.result(ib)
	b.barrier()
	if testWakeTake(t, cb) != "B" {
		t.Fatal("independent B lost")
	}
	a.result(ia2)
	a.barrier()
	if testWakeTake(t, ca2) != "A2" {
		t.Fatal("new generation lost")
	}
}

func TestWakeFeedbackBounds(t *testing.T) {
	p := newWakeLeg(t)
	base := time.Now()
	now := base
	var timers []func()
	p.c.wakeMu.Lock()
	p.c.wakeNow = func() time.Time { return now }
	p.c.wakeAfter = func(d time.Duration, f func()) func() {
		if d != 60*time.Second {
			t.Error("wrong deadline")
		}
		timers = append(timers, f)
		return func() {}
	}
	p.c.wakeMu.Unlock()
	var channels []<-chan string
	var ids []string
	for i := 0; i < 129; i++ {
		ch, id := p.submit(t.Context(), fmt.Sprintf("snapshot-%d", i))
		channels = append(channels, ch)
		ids = append(ids, id)
	}
	if ids[127] == "" || ids[128] != "" {
		t.Fatal("128/129 boundary incorrect")
	}
	testWakeClosed(t, channels[128])
	p.c.wakeMu.Lock()
	count := len(p.c.wakes)
	now = base.Add(60*time.Second - time.Nanosecond)
	p.c.wakeMu.Unlock()
	if count != 128 {
		t.Fatal("overflow evicted binding")
	}
	p.result(ids[0])
	p.barrier()
	if testWakeTake(t, channels[0]) != "snapshot-0" {
		t.Fatal("overflow lost original binding")
	}
	p.c.wakeMu.Lock()
	now = base.Add(60 * time.Second)
	p.c.wakeMu.Unlock()
	p.result(ids[1])
	p.barrier()
	testWakeClosed(t, channels[1])
	p.c.wakeMu.Lock()
	now = base.Add(60*time.Second + time.Nanosecond)
	p.c.wakeMu.Unlock()
	p.result(ids[2])
	p.barrier()
	testWakeClosed(t, channels[2])
	for _, expire := range timers {
		expire()
	}
	p.c.wakeMu.Lock()
	count = len(p.c.wakes)
	p.c.wakeMu.Unlock()
	if count != 0 {
		t.Fatal("idle cleanup retained bindings")
	}
	for _, ch := range channels[3:128] {
		testWakeClosed(t, ch)
	}
	if err := p.c.SendPushWake("fcm", "legacy-secret"); err != nil {
		t.Fatal(err)
	}
	if raw := string(testWakeTake(t, p.wires)); raw != `{"push_wake":{"platform":"fcm","token":"legacy-secret"}}` {
		t.Fatal("legacy wire changed")
	}
	p.barrier()
}

func TestWakeFeedbackRetirement(t *testing.T) {
	for _, kind := range []string{"request", "close", "lifecycle"} {
		t.Run(kind, func(t *testing.T) {
			p := newWakeLeg(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ch, id := p.submit(ctx, "snapshot")
			switch kind {
			case "request":
				cancel()
				p.result(id)
				p.barrier()
			case "close":
				_ = p.c.Close()
			case "lifecycle":
				p.cancel()
			}
			testWakeClosed(t, ch)
		})
	}
}
