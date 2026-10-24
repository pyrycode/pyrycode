package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type testAppDiscovery struct {
	mu         sync.Mutex
	records    []apps.Record
	revision   uint64
	entered    chan struct{}
	release    chan struct{}
	calls      int
	blockFirst bool
}

func (d *testAppDiscovery) List() ([]apps.Record, uint64) {
	d.mu.Lock()
	d.calls++
	gate, entered := d.release, d.entered
	if d.blockFirst && d.calls > 1 {
		gate, entered = nil, nil
	}
	d.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.records), d.revision
}

type testHostedPeer struct {
	t          *testing.T
	mgr        *V2SessionManager
	frames     chan protocol.RoutingEnvelope
	rec        *v2Recorder
	send, recv *noise.CipherState
	conn       string
	seen       int
	stop       func()
	pub        []byte
	ack        protocol.HelloAckPayload
	events     chan testHostedEvent
	ids        map[uint64]bool
}

type testHostedEvent struct{ event, conn, kind, typ string }
type testHostedLog struct{ events chan testHostedEvent }

func (h testHostedLog) Enabled(context.Context, slog.Level) bool { return true }
func (h testHostedLog) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h testHostedLog) WithGroup(string) slog.Handler            { return h }
func (h testHostedLog) Handle(_ context.Context, r slog.Record) error {
	e := testHostedEvent{}
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "event":
			e.event = a.Value.String()
		case "conn_id":
			e.conn = a.Value.String()
		case "kind":
			e.kind = a.Value.String()
		case "type":
			e.typ = a.Value.String()
		}
		return true
	})
	select {
	case h.events <- e:
	default:
	}
	return nil
}

func (p *testHostedPeer) waitEvent(event, kind string) {
	p.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-p.events:
			if e.event == event && e.conn == p.conn && (kind == "" || e.kind == kind || e.typ == kind) {
				return
			}
		case <-deadline:
			p.t.Fatalf("missing %s %s", event, kind)
		}
	}
}

func testHostedStart(t *testing.T, d HostedAppsDiscovery, tweak func(*V2SessionConfig)) *testHostedPeer {
	return testHostedStartCaps(t, d, []string{protocol.CapabilityHostedAppsV1}, tweak)
}
func testHostedStartCaps(t *testing.T, d HostedAppsDiscovery, caps []string, tweak func(*V2SessionConfig)) *testHostedPeer {
	t.Helper()
	priv, pub := genV2Keypair(t)
	rec := &v2Recorder{}
	frames := make(chan protocol.RoutingEnvelope, 64)
	events := make(chan testHostedEvent, 2048)
	cfg := V2SessionConfig{Frames: frames, Outbound: rec.outbound, StaticPriv: priv, Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID, Logger: silentLogger(), HostedAppsDiscovery: d, HostedAppsReady: func() bool { return true }, HostedAppsSend: func(ctx context.Context, e protocol.RoutingEnvelope) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return rec.outbound(e)
	}}
	cfg.OutboundContext = cfg.HostedAppsSend
	cfg.Logger = slog.New(testHostedLog{events: events})
	if tweak != nil {
		tweak(&cfg)
	}
	mgr, stop := startManager(t, cfg)
	t.Cleanup(stop)
	p := &testHostedPeer{t: t, mgr: mgr, frames: frames, rec: rec, conn: v2TestConnID, stop: stop, pub: pub, events: events}
	p.open(caps)
	return p
}

func (p *testHostedPeer) open(caps []string) {
	p.t.Helper()
	start := len(p.rec.snapshot())
	init, err := noise.NewInitiator(v2TestInstallPriv, p.pub)
	if err != nil {
		p.t.Fatal(err)
	}
	msg, err := init.WriteInit(buildHelloEarlyDataCaps(p.t, v2TestToken, supportedCaps(caps)))
	if err != nil {
		p.t.Fatal(err)
	}
	p.frames <- wrapInnerFrame(p.t, p.conn, protocol.TypeNoiseInit, msg)
	waitConnOpen(p.t, p.mgr, p.conn)
	for _, e := range p.rec.snapshot()[start:] {
		if e.ConnID != p.conn {
			continue
		}
		ack, send, recv, err := init.ReadResp(decodeRespFrame(p.t, e))
		if err != nil {
			p.t.Fatal(err)
		}
		p.send, p.recv = send, recv
		var env protocol.Envelope
		if err := json.Unmarshal(ack, &env); err != nil || env.ID != 1 {
			p.t.Fatalf("hello_ack: %s %v", ack, err)
		}
		if err := json.Unmarshal(env.Payload, &p.ack); err != nil {
			p.t.Fatal(err)
		}
		p.ids = map[uint64]bool{1: true}
		p.seen = len(p.rec.snapshot())
		return
	}
	p.t.Fatal("missing handshake response")
}

func (p *testHostedPeer) sibling(conn string) *testHostedPeer {
	q := &testHostedPeer{t: p.t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: conn, pub: p.pub, events: p.events}
	q.open([]string{protocol.CapabilityHostedAppsV1})
	return q
}
func (p *testHostedPeer) raw(raw string) {
	p.t.Helper()
	b, e := p.send.Encrypt([]byte(raw))
	if e != nil {
		p.t.Fatal(e)
	}
	p.frames <- wrapInnerFrame(p.t, p.conn, protocol.TypeNoiseMsg, b)
}
func (p *testHostedPeer) request(id uint64, typ, payload string) {
	p.raw(fmt.Sprintf(`{"id":%d,"type":%q,"ts":"2026-10-10T18:00:01Z","payload":%s}`, id, typ, payload))
}
func (p *testHostedPeer) next() (protocol.Envelope, map[string]json.RawMessage) {
	p.t.Helper()
	return p.nextBefore(time.Now().Add(3 * time.Second))
}

func (p *testHostedPeer) nextBefore(deadline time.Time) (protocol.Envelope, map[string]json.RawMessage) {
	p.t.Helper()
	remaining := time.Until(deadline)
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	for {
		if !time.Now().Before(deadline) {
			p.t.Fatalf("reply timed out after %s wait budget (consumed %d routing envelopes)", remaining, p.seen)
		}
		snap := p.rec.snapshot()
		for p.seen < len(snap) {
			e := snap[p.seen]
			p.seen++
			if e.ConnID != p.conn {
				continue
			}
			if e.CloseCode != 0 {
				p.t.Fatalf("unexpected close %d", e.CloseCode)
			}
			raw := threadPlain(p.t, e, p.recv)
			var env protocol.Envelope
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &env); err != nil {
				p.t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &obj); err != nil {
				p.t.Fatal(err)
			}
			if p.ids[env.ID] || env.ID > hostedMaxID {
				p.t.Fatalf("reused or unsafe sender ID: %s", raw)
			}
			p.ids[env.ID] = true
			for _, key := range []string{"conversation_id", "session_id", "event_id", "history_entry_id", "session_state_cleared", "payload_encrypted"} {
				if _, ok := obj[key]; ok {
					p.t.Fatalf("forbidden output %s: %s", key, raw)
				}
			}
			return env, obj
		}
		select {
		case <-timer.C:
			p.t.Fatalf("reply timed out after %s wait budget (consumed %d routing envelopes)", remaining, p.seen)
		case <-time.After(time.Millisecond):
		}
	}
}
func testHostedError(t *testing.T, e protocol.Envelope, reply uint64, code string) {
	t.Helper()
	var p map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := json.Unmarshal(p["code"], &got); err != nil {
		t.Fatal(err)
	}
	if e.Type != "error" || e.InReplyTo == nil || *e.InReplyTo != reply || got != code {
		t.Fatalf("error = %+v %s", e, e.Payload)
	}
	if len(p) != 3 && !(code == "app.busy" && len(p) == 4) {
		t.Fatalf("error keys: %s", e.Payload)
	}
	var retry bool
	if err := json.Unmarshal(p["retryable"], &retry); err != nil || retry != (code == "app.busy" || code == "apps.changed") {
		t.Fatalf("retryability: %s", e.Payload)
	}
	if code == "app.busy" && string(p["retry_after_s"]) != "1" {
		t.Fatal(string(e.Payload))
	}
}
func TestHostedDiscoveryPaging(t *testing.T) {
	d := testHostedRecords("Committed", 35)
	for i := range d.records {
		r := &d.records[i]
		r.State = []string{"stopped", "starting", "running", "failed"}[int(r.Revision-1)%4]
		if r.State == "stopped" {
			r.Desired = "stopped"
		}
		if r.Revision == 2 {
			r.ActiveRelease = "1.2.3"
		}
		if r.Revision == 3 {
			r.PendingRelease = "2.3.4"
		}
		if r.Revision == 4 {
			r.LastError = &apps.LastError{Code: "app.io_failed", Message: "safe committed error"}
		}
	}
	p := testHostedStart(t, d, nil)
	p.request(10, "list_apps", `{}`)
	e, _ := p.next()
	var first protocol.AppsPayload
	if err := json.Unmarshal(e.Payload, &first); err != nil {
		t.Fatal(err)
	}
	if e.ID <= 1 || e.InReplyTo == nil || *e.InReplyTo != 10 || len(first.Items) != 16 || first.Revision != 35 || first.NextCursor == nil {
		t.Fatalf("first: %+v %s", e, e.Payload)
	}
	if !strings.Contains(string(e.Payload), `"active_release":null`) || !strings.Contains(string(e.Payload), `"pending_release":null`) || !strings.Contains(string(e.Payload), `"last_error":null`) {
		t.Fatal(string(e.Payload))
	}
	want := protocol.HostedAppRecord{AppID: "00000000-0000-4000-8000-000000000000", Title: "Committed", Desired: "stopped", State: "stopped", Revision: 1}
	if !reflect.DeepEqual(first.Items[0], want) || first.Items[1].State != "starting" || first.Items[1].ActiveRelease == nil || *first.Items[1].ActiveRelease != "1.2.3" || first.Items[2].State != "running" || first.Items[2].PendingRelease == nil || *first.Items[2].PendingRelease != "2.3.4" || first.Items[3].State != "failed" || first.Items[3].LastError == nil || first.Items[3].LastError.Message != "safe committed error" {
		t.Fatalf("whole committed records: %#v", first.Items[:4])
	}
	if len(*first.NextCursor) > 256 || len(*first.NextCursor) == 0 {
		t.Fatal("invalid issued cursor size")
	}
	for _, b := range []byte(*first.NextCursor) {
		if b > 127 {
			t.Fatal("non-ASCII issued cursor")
		}
	}
	p.request(11, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *first.NextCursor))
	e, _ = p.next()
	var second protocol.AppsPayload
	if err := json.Unmarshal(e.Payload, &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 16 || second.NextCursor == nil || e.ID <= 1 || second.Revision != first.Revision {
		t.Fatal(string(e.Payload))
	}
	third := testHostedPage(t, p, 12, second.NextCursor)
	if len(third.Items) != 3 || third.NextCursor != nil || third.Revision != first.Revision {
		t.Fatalf("third page: %#v", third)
	}
	all := append(append(first.Items, second.Items...), third.Items...)
	for i, item := range all {
		if item.AppID != fmt.Sprintf("00000000-0000-4000-8000-%012d", i) || item.Revision != uint64(i+1) {
			t.Fatalf("mixed or unsorted record: %#v", item)
		}
	}
	p.request(13, "list_apps", `{}`)
	e, _ = p.next()
	if e.Type != "apps" {
		t.Fatal(e)
	}
}
func TestHostedDiscoveryValidation(t *testing.T) {
	p := testHostedStart(t, &testAppDiscovery{}, nil)
	bad := []string{`"cursor":null`, `"cursor":""`, `"cursor":1`, `"cursor":"é"`, `"cursor":"` + strings.Repeat("x", 257) + `"`, `"cursor":"a","cursor":"b"`, `"nested":{"x":1,"x":2}`}
	for i, payload := range bad {
		p.request(uint64(10+i), "list_apps", "{"+payload+"}")
		e, _ := p.next()
		testHostedError(t, e, uint64(10+i), "app.invalid_request")
	}
	for i, typ := range []string{"apps", "app_updated", "app_removed", "app_cancelled"} {
		p.request(uint64(30+i), typ, `{}`)
		e, _ := p.next()
		testHostedError(t, e, uint64(30+i), "app.invalid_request")
	}
	p.request(40, "list_apps", `{"CURSOR":null,"unknown":true}`)
	e, _ := p.next()
	if e.Type != "apps" || string(e.Payload) != `{"revision":0,"items":[],"next_cursor":null}` {
		t.Fatal(string(e.Payload))
	}
}
func TestHostedDiscoveryBlockedRead(t *testing.T) {
	d := &testAppDiscovery{entered: make(chan struct{}, 10), release: make(chan struct{})}
	p := testHostedStart(t, d, nil)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(d.release) }) })
	p.request(10, "list_apps", `{}`)
	select {
	case <-d.entered:
	case <-time.After(time.Second):
		t.Fatal("read not started")
	}
	for i := 0; i < 4; i++ {
		p.request(uint64(20+i*2), "app_cancel", `{"request_id":10}`)
		e, _ := p.next()
		if e.Type != "app_cancelled" {
			t.Fatal(e)
		}
		p.request(uint64(21+i*2), "list_apps", `{}`)
		e, _ = p.next()
		testHostedError(t, e, uint64(21+i*2), "app.busy")
	}
	d.mu.Lock()
	calls := d.calls
	d.mu.Unlock()
	if calls != 1 {
		t.Fatalf("readers=%d", calls)
	}
	release.Do(func() { close(d.release) })
	p.waitEvent("v2.hosted.completed", "read")
	p.request(50, "list_apps", `{}`)
	e, _ := p.next()
	if e.Type != "apps" || e.InReplyTo == nil || *e.InReplyTo != 50 {
		t.Fatalf("retired read completed late: %+v", e)
	}
}
