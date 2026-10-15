package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// feedbackLog also supplies a completion barrier after persistence decisions.
type feedbackLog struct {
	mu     sync.Mutex
	text   string
	events chan string
}

func (l *feedbackLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *feedbackLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *feedbackLog) WithGroup(string) slog.Handler            { return l }
func (l *feedbackLog) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	l.text += r.Message
	r.Attrs(func(a slog.Attr) bool {
		l.text += fmt.Sprint(a.Value.Any())
		if a.Key == "event" {
			l.events <- a.Value.String()
		}
		return true
	})
	l.mu.Unlock()
	return nil
}
func feedbackTake[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("feedback barrier timed out")
		var z T
		return z
	}
}

type feedbackLeg struct {
	t       *testing.T
	ctx     context.Context
	c       *relay.Connection
	socket  *websocket.Conn
	sockets chan *websocket.Conn
	wires   chan []byte
	w       *pushWaker
	reg     *devices.Registry
	path    string
	log     *feedbackLog
}

func feedbackDevice(hash string) devices.Device {
	now := time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)
	return devices.Device{TokenHash: hash, Name: "phone", Platform: "fcm", PushToken: "push-secret-distinct", PairedAt: now, LastSeenAt: now, RedeemBy: now.Add(time.Hour), StaticKey: "key-secret-distinct", ClientVersion: "v1", AllowRemotePermissions: true}
}
func newFeedbackLeg(t *testing.T, hash string, immediate ...bool) *feedbackLeg {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	p := &feedbackLeg{t: t, ctx: ctx, sockets: make(chan *websocket.Conn, 4), wires: make(chan []byte, 256), path: filepath.Join(t.TempDir(), "devices.json"), log: &feedbackLog{events: make(chan string, 4096)}}
	p.reg, _ = devices.Load(p.path)
	p.reg.Add(feedbackDevice(hash))
	u := feedbackDevice("unrelated")
	u.Platform, u.PushToken = "", ""
	p.reg.Add(u)
	if err := p.reg.Save(p.path); err != nil {
		t.Fatal(err)
	}
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
			if len(immediate) > 0 {
				var e struct {
					Wake struct {
						ID string `json:"request_id"`
					} `json:"push_wake"`
				}
				if json.Unmarshal(raw, &e) != nil {
					return
				}
				if err := ws.Write(ctx, websocket.MessageText, []byte(`{"push_wake_result":{"request_id":"`+e.Wake.ID+`","code":"invalid_token"}}`)); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	var err error
	p.c, err = relay.Connect(ctx, relay.Config{ServerID: "synthetic", BinaryVersion: "test", RelayURL: "ws" + strings.TrimPrefix(srv.URL, "http"), AllowInsecureScheme: true, Logger: slog.New(p.log)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = p.c.Close(); _ = p.c.Wait() })
	p.socket = feedbackTake(t, p.sockets)
	feedbackTake(t, p.c.Reconnected())
	p.w = newPushWaker(&fakeWakeConns{}, p.reg, p.c.SendPushWakeFeedback, slog.New(p.log))
	p.w.invalid = func(ctx context.Context, d devices.Device) {
		clearInvalidPushRegistration(ctx, p.reg, p.path, d, slog.New(p.log))
	}
	return p
}
func (p *feedbackLeg) write(raw string) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	if err := p.socket.Write(ctx, websocket.MessageText, []byte(raw)); err != nil {
		p.t.Fatal(err)
	}
}
func (p *feedbackLeg) request() string {
	p.t.Helper()
	p.w.wakeAbsent(p.ctx, testWakeCause)
	var e struct {
		Wake struct {
			ID string `json:"request_id"`
		} `json:"push_wake"`
	}
	if err := json.Unmarshal(feedbackTake(p.t, p.wires), &e); err != nil || e.Wake.ID == "" {
		p.t.Fatal("missing opted-in request")
	}
	return e.Wake.ID
}
func (p *feedbackLeg) result(id string) {
	p.write(`{"push_wake_result":{"request_id":"` + id + `","code":"invalid_token"}}`)
}
func (p *feedbackLeg) barrier() {
	p.write(`{"conn_id":"barrier","frame":{}}`)
	if feedbackTake(p.t, p.c.Frames()).ConnID != "barrier" {
		p.t.Fatal("result escaped routing boundary")
	}
}
func (p *feedbackLeg) completed() string {
	for {
		e := feedbackTake(p.t, p.log.events)
		if strings.HasPrefix(e, "push_wake.registration_") {
			return e
		}
	}
}
func (p *feedbackLeg) disk() []devices.Device {
	p.t.Helper()
	r, err := devices.Load(p.path)
	if err != nil {
		p.t.Fatal(err)
	}
	return r.List()
}
func (p *feedbackLeg) unchanged(before os.FileInfo) {
	p.t.Helper()
	after, err := os.Stat(p.path)
	if err != nil {
		p.t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		p.t.Fatal("unexpected Save")
	}
}

func feedbackState(devs []devices.Device) map[string]devices.Device {
	out := make(map[string]devices.Device, len(devs))
	for _, d := range devs {
		out[d.TokenHash] = d
	}
	return out
}

func feedbackEqual(a, b []devices.Device) bool {
	return reflect.DeepEqual(feedbackState(a), feedbackState(b))
}
func TestPushWakeFeedbackPersistence(t *testing.T) {
	a, b := newFeedbackLeg(t, "hash-A-secret"), newFeedbackLeg(t, "hash-B-secret")
	idA, idB := a.request(), b.request()
	if idA != idB {
		t.Fatal("independent legs should exercise equal request IDs")
	}
	beforeA, beforeB := a.reg.List(), b.reg.List()
	a.reg.UpdatePushRegistration(beforeA[0].TokenHash, "fcm", "replacement-secret", "renamed")
	if err := a.reg.Save(a.path); err != nil {
		t.Fatal(err)
	}
	beforeA = a.reg.List()
	b.reg.UpdatePushRegistration(beforeB[0].TokenHash, "fcm", beforeB[0].PushToken, "current phone name")
	if err := b.reg.Save(b.path); err != nil {
		t.Fatal(err)
	}
	beforeB = b.reg.List()
	info, _ := os.Stat(a.path)
	a.result(idA)
	b.result(idB)
	if a.completed() != "push_wake.registration_unchanged" || b.completed() != "push_wake.registration_cleared" {
		t.Fatal("wrong clear outcomes")
	}
	a.unchanged(info)
	beforeB[0].Platform, beforeB[0].PushToken = "", ""
	for _, check := range []struct {
		p    *feedbackLeg
		want []devices.Device
	}{{a, beforeA}, {b, beforeB}} {
		if !feedbackEqual(check.p.reg.List(), check.want) || !feedbackEqual(check.p.disk(), check.want) {
			t.Fatal("pairing or replacement fields changed")
		}
	}
	info, _ = os.Stat(b.path)
	b.result(idB)
	b.barrier()
	b.unchanged(info)
	b.w.last = make(map[string]time.Time)
	b.w.wakeAbsent(b.ctx, testWakeCause)
	select {
	case <-b.wires:
		t.Fatal("cleared registration woken")
	default:
	}
	b.reg.UpdatePushRegistration("hash-B-secret", "fcm", "new-secret", "phone")
	if err := b.reg.Save(b.path); err != nil {
		t.Fatal(err)
	}
	b.request()
}
func TestPushWakeFeedbackReconcile(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			p := newFeedbackLeg(t, "old-hash-secret")
			applying := make(chan struct{})
			p.w.invalid = func(ctx context.Context, d devices.Device) {
				close(applying)
				clearInvalidPushRegistration(ctx, p.reg, p.path, d, slog.New(p.log))
			}
			id := p.request()
			entered, release := make(chan struct{}), make(chan struct{})
			releaseHolder := sync.OnceFunc(func() { close(release) })
			defer releaseHolder()
			done := make(chan error, 1)
			var pairedInfo os.FileInfo
			go func() {
				done <- devices.WithLock(p.path, time.Second, func() error {
					close(entered)
					<-release
					r, err := devices.Load(p.path)
					if err != nil {
						return err
					}
					if revoke {
						r.Remove("phone")
					}
					d := feedbackDevice("new-hash-secret")
					if !revoke {
						d.Name = "new phone"
					}
					r.Add(d)
					if err := r.Save(p.path); err != nil {
						return err
					}
					pairedInfo, _ = os.Stat(p.path)
					return nil
				})
			}()
			feedbackTake(t, entered)
			p.result(id)
			p.barrier()
			feedbackTake(t, applying)
			releaseHolder()
			if err := feedbackTake(t, done); err != nil {
				t.Fatal(err)
			}
			p.completed()
			if revoke {
				p.unchanged(pairedInfo)
			}
			disk := p.disk()
			if !feedbackEqual(p.reg.List(), disk) {
				t.Fatal("reconcile mismatch")
			}
			found := false
			for _, d := range disk {
				if d.TokenHash == "new-hash-secret" {
					found = true
					if d.PushToken == "" {
						t.Fatal("re-paired identity erased")
					}
				}
				if d.TokenHash == "old-hash-secret" && (revoke || d.PushToken != "") {
					t.Fatal("old identity retained incorrectly")
				}
			}
			if !found {
				t.Fatal("concurrent pairing lost")
			}
		})
	}
}
func TestPushWakeFeedbackRejected(t *testing.T) {
	p := newFeedbackLeg(t, "hash-secret-distinct")
	id := p.request()
	info, _ := os.Stat(p.path)
	for _, raw := range []string{
		`{"push_wake_result":{"request_id":"` + id + `","code":"transient"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":"provider-secret-distinct"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":"invalid_token","code":null}}`,
		`{"push_wake_result":{"request_id":"unknown","code":"invalid_token"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":5,"token":"access-secret-distinct"}}`,
		`{"push_wake_result":{"request_id":"` + id + `","code":"invalid_token"},"malformed-secret-distinct":`,
		`{"conn_id":{},"frame":"inner-secret-distinct"}`,
	} {
		p.write(raw)
		p.barrier()
		p.unchanged(info)
	}
	// Silence from an old relay cannot clear; a later canonical notification can.
	if p.reg.List()[0].PushToken == "" {
		t.Fatal("rejected result cleared")
	}
	p.result(id)
	p.completed()
	p.log.mu.Lock()
	defer p.log.mu.Unlock()
	for _, s := range []string{"hash-secret-distinct", "push-secret-distinct", "key-secret-distinct", "provider-secret-distinct", "access-secret-distinct", "malformed-secret-distinct", "inner-secret-distinct"} {
		if strings.Contains(p.log.text, s) {
			t.Fatal("secret exposed in diagnostics")
		}
	}
}
func TestPushWakeFeedbackReconnect(t *testing.T) {
	p := newFeedbackLeg(t, "hash-secret")
	id := p.request()
	info, _ := os.Stat(p.path)
	_ = p.socket.CloseNow()
	p.socket = feedbackTake(t, p.sockets)
	feedbackTake(t, p.c.Reconnected())
	p.result(id)
	p.barrier()
	p.unchanged(info)
	if p.reg.List()[0].PushToken == "" {
		t.Fatal("stale generation cleared")
	}
	p.w.last = make(map[string]time.Time)
	fresh := p.request()
	p.result(fresh)
	p.completed()
}
func TestPushWakeFeedbackFailures(t *testing.T) {
	for _, mode := range []string{"lock", "cancel", "reload", "save"} {
		t.Run(mode, func(t *testing.T) {
			p := newFeedbackLeg(t, "hash-secret")
			requestCtx, cancel := context.WithCancel(p.ctx)
			defer cancel()
			p.ctx = requestCtx
			original := p.reg.List()
			id := p.request()
			before, err := os.ReadFile(p.path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "lock":
				if err := os.Mkdir(p.path+".lock", 0700); err != nil {
					t.Fatal(err)
				}
			case "reload":
				if err := os.WriteFile(p.path, []byte(`{"devices":[{"paired_at":"decode-secret-distinct"}]}`), 0600); err != nil {
					t.Fatal(err)
				}
				before, _ = os.ReadFile(p.path)
			case "save":
				if err := os.WriteFile(p.path+".lock", nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Dir(p.path), 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(p.path), 0700) })
			case "cancel":
				cancel()
				clearInvalidPushRegistration(requestCtx, p.reg, p.path, original[0], slog.New(p.log))
			}
			if mode != "cancel" {
				p.result(id)
				p.completed()
			}
			want := original
			if mode == "save" {
				want[0].Platform, want[0].PushToken = "", ""
			}
			if !feedbackEqual(p.reg.List(), want) {
				t.Fatal("wrong failure memory state")
			}
			after, _ := os.ReadFile(p.path)
			if string(after) != string(before) {
				t.Fatal("failure changed disk")
			}
			if mode != "cancel" {
				p.result(id)
				p.barrier()
			}
			p.log.mu.Lock()
			text := p.log.text
			p.log.mu.Unlock()
			if strings.Contains(text, "decode-secret-distinct") || strings.Contains(text, "hash-secret") {
				t.Fatal("failure leaked secret")
			}
			if strings.Contains(text, "push_wake.registration_cleared") {
				t.Fatal("false durable success")
			}
		})
	}
}

func TestPushWakeFeedbackImmediate(t *testing.T) {
	p := newFeedbackLeg(t, "immediate-hash", true)
	p.request()
	if p.completed() != "push_wake.registration_cleared" || p.disk()[0].PushToken != "" {
		t.Fatal("immediate reply lost")
	}
}

func TestPushWakeFeedbackCapacity(t *testing.T) {
	p := newFeedbackLeg(t, "first-hash")
	for i := range 128 {
		d := feedbackDevice(fmt.Sprintf("hash-%d", i))
		d.Name = fmt.Sprintf("extra-%03d", i)
		p.reg.Add(d)
	}
	if err := p.reg.Save(p.path); err != nil {
		t.Fatal(err)
	}
	p.w.wakeAbsent(p.ctx, testWakeCause)
	var first string
	for i := range 129 {
		var e struct {
			Wake struct {
				ID string `json:"request_id"`
			} `json:"push_wake"`
		}
		if err := json.Unmarshal(feedbackTake(t, p.wires), &e); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = e.Wake.ID
		}
		if (e.Wake.ID == "") != (i == 128) {
			t.Fatal("incorrect capacity fallback")
		}
	}
	info, _ := os.Stat(p.path)
	p.result("")
	p.barrier()
	p.unchanged(info)
	p.result(first)
	p.completed()
	cleared := 0
	for _, d := range p.disk() {
		if d.TokenHash == "unrelated" {
			continue
		}
		if d.PushToken == "" {
			cleared++
		}
		if d.TokenHash == "hash-127" && d.PushToken == "" {
			t.Fatal("legacy overflow cleared")
		}
	}
	if cleared != 1 {
		t.Fatal("accepted binding lost or overflow cleared")
	}
}

func TestPushWakeFeedbackExpiry(t *testing.T) {
	t.Parallel()
	p := newFeedbackLeg(t, "expiry-hash")
	id := p.request()
	info, _ := os.Stat(p.path)
	// Exercise the public API's real timer; upstream tests pin the exact boundary
	// with a controlled clock without exposing a production clock-setting API.
	select {
	case <-time.After(61 * time.Second):
	case <-t.Context().Done():
		t.Fatal("expiry test cancelled")
	}
	p.result(id)
	p.barrier()
	p.unchanged(info)
	if p.reg.List()[0].PushToken == "" {
		t.Fatal("expired reply cleared registration")
	}
}

func TestPushWakeFeedbackLockBarrier(t *testing.T) {
	for _, mode := range []string{"busy", "cancel", "replace"} {
		t.Run(mode, func(t *testing.T) {
			p := newFeedbackLeg(t, "barrier-hash")
			ctx, cancel := context.WithCancel(p.ctx)
			defer cancel()
			p.ctx = ctx
			applying, applied := make(chan struct{}), make(chan struct{})
			p.w.invalid = func(ctx context.Context, d devices.Device) {
				close(applying)
				clearInvalidPushRegistration(ctx, p.reg, p.path, d, slog.New(p.log))
				close(applied)
			}
			id := p.request()
			original := p.reg.List()
			info, _ := os.Stat(p.path)
			err := devices.WithLock(p.path, time.Second, func() error {
				p.result(id)
				feedbackTake(t, applying)
				switch mode {
				case "busy":
					feedbackTake(t, applied)
				case "cancel":
					cancel()
				case "replace":
					p.reg.UpdatePushRegistration("barrier-hash", "fcm", "replacement-secret", "renamed")
					return p.reg.Save(p.path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			feedbackTake(t, applied)
			if mode == "replace" {
				original[0].PushToken, original[0].Name = "replacement-secret", "renamed"
			} else {
				p.unchanged(info)
			}
			if !feedbackEqual(p.reg.List(), original) || !feedbackEqual(p.disk(), original) {
				t.Fatal("lock barrier lost registration state")
			}
		})
	}
}

func TestPushWakeFeedbackFailedSend(t *testing.T) {
	p := newFeedbackLeg(t, "retry-hash")
	realSend := p.w.send
	p.w.send = func(ctx context.Context, platform, token, snapshot string) (<-chan string, error) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return realSend(cancelled, platform, token, snapshot)
	}
	info, _ := os.Stat(p.path)
	p.w.wakeAbsent(p.ctx, testWakeCause)
	if len(p.w.last) != 0 {
		t.Fatal("failed write consumed coalescing budget")
	}
	p.unchanged(info)
	p.w.send = realSend
	id := p.request()
	p.result(id)
	if p.completed() != "push_wake.registration_cleared" {
		t.Fatal("retry failed")
	}
}
