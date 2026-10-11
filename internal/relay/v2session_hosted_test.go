package relay

import (
	"context"
	"encoding/json"
	"fmt"
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
	mu       sync.Mutex
	records  []apps.Record
	revision uint64
	entered  chan struct{}
	release  chan struct{}
	calls    int
}

func (d *testAppDiscovery) List() ([]apps.Record, uint64) {
	d.mu.Lock()
	d.calls++
	gate, entered := d.release, d.entered
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
}

func testHostedStart(t *testing.T, d HostedAppsDiscovery, tweak func(*V2SessionConfig)) *testHostedPeer {
	t.Helper()
	priv, pub := genV2Keypair(t)
	rec := &v2Recorder{}
	frames := make(chan protocol.RoutingEnvelope, 64)
	cfg := V2SessionConfig{Frames: frames, Outbound: rec.outbound, StaticPriv: priv, Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID, Logger: silentLogger(), HostedAppsDiscovery: d, HostedAppsReady: func() bool { return true }, HostedAppsSend: func(ctx context.Context, e protocol.RoutingEnvelope) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return rec.outbound(e)
	}}
	if tweak != nil {
		tweak(&cfg)
	}
	mgr, stop := startManager(t, cfg)
	t.Cleanup(stop)
	p := &testHostedPeer{t: t, mgr: mgr, frames: frames, rec: rec, conn: v2TestConnID, stop: stop, pub: pub}
	p.send, p.recv = openModalConn(t, mgr, frames, rec, pub, p.conn, []string{protocol.CapabilityHostedAppsV1})
	p.seen = len(rec.snapshot())
	return p
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
	deadline := time.After(3 * time.Second)
	for {
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
			for _, key := range []string{"conversation_id", "session_id", "event_id", "history_entry_id", "session_state_cleared", "payload_encrypted"} {
				if _, ok := obj[key]; ok {
					p.t.Fatalf("forbidden output %s: %s", key, raw)
				}
			}
			return env, obj
		}
		select {
		case <-deadline:
			p.t.Fatal("reply timed out")
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
	json.Unmarshal(p["code"], &got)
	if e.Type != "error" || e.InReplyTo == nil || *e.InReplyTo != reply || got != code {
		t.Fatalf("error = %+v %s", e, e.Payload)
	}
	if len(p) != 3 && !(code == "app.busy" && len(p) == 4) {
		t.Fatalf("error keys: %s", e.Payload)
	}
}
func TestHostedDiscoveryPaging(t *testing.T) {
	d := &testAppDiscovery{revision: 20}
	for i := 19; i >= 0; i-- {
		d.records = append(d.records, apps.Record{AppID: fmt.Sprintf("%032d", i), Title: "Committed", Desired: "available", State: "starting", Revision: 20})
	}
	p := testHostedStart(t, d, nil)
	p.request(10, "list_apps", `{}`)
	e, _ := p.next()
	var first protocol.AppsPayload
	if err := json.Unmarshal(e.Payload, &first); err != nil {
		t.Fatal(err)
	}
	if e.ID <= 1 || e.InReplyTo == nil || *e.InReplyTo != 10 || len(first.Items) != 16 || first.Revision != 20 || first.NextCursor == nil {
		t.Fatalf("first: %+v %s", e, e.Payload)
	}
	if !strings.Contains(string(e.Payload), `"active_release":null`) || !strings.Contains(string(e.Payload), `"last_error":null`) {
		t.Fatal(string(e.Payload))
	}
	p.request(11, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *first.NextCursor))
	e, _ = p.next()
	var second protocol.AppsPayload
	json.Unmarshal(e.Payload, &second)
	if len(second.Items) != 4 || second.NextCursor != nil || e.ID <= 1 {
		t.Fatal(string(e.Payload))
	}
	p.request(12, "list_apps", `{}`)
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
	defer close(d.release)
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
}
