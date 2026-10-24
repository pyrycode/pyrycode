package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestHostedDiscoveryUnauthorized(t *testing.T) {
	priv, pub := genV2Keypair(t)
	frames, rec := make(chan protocol.RoutingEnvelope, 4), &v2Recorder{}
	d := &testAppDiscovery{}
	mgr, stop := startManager(t, V2SessionConfig{Frames: frames, Outbound: rec.outbound,
		StaticPriv: priv, Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID,
		Logger: silentLogger(), HostedAppsDiscovery: d, HostedAppsReady: func() bool { return true },
		HostedAppsSend: func(_ context.Context, e protocol.RoutingEnvelope) error { return rec.outbound(e) }})
	t.Cleanup(stop)
	init, err := noise.NewInitiator(v2TestInstallPriv, pub)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(buildHelloEarlyDataCaps(t, "unpaired-token", []string{"hosted_apps_v1"}))
	if err != nil {
		t.Fatal(err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, msg)
	envs := waitForEnvelopes(t, rec, 2)
	_, send, recv, err := init.ReadResp(decodeRespFrame(t, envs[0]))
	if err != nil {
		t.Fatal(err)
	}
	var e protocol.Envelope
	if err := json.Unmarshal(threadPlain(t, envs[1], recv), &e); err != nil || e.Type != "error" {
		t.Fatalf("unauthenticated output: %+v %v", e, err)
	}
	frames <- sealAppFrame(t, send, protocol.Envelope{ID: 10, Type: "list_apps", TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	waitForEnvelopes(t, rec, 3)
	_ = mgr.ActiveConns(t.Context())
	for _, e := range rec.snapshot()[2:] {
		if len(e.Frame) != 0 {
			t.Fatal("unpaired discovery emitted data")
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls != 0 {
		t.Fatal("unpaired discovery read the registry")
	}
}

func TestHostedDiscoveryCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps []string
		set  func(*V2SessionConfig)
		want bool
	}{
		{"ready", []string{"hosted_apps_v1", "hosted_apps_v1"}, nil, true},
		{"not offered", nil, nil, false},
		{"no discovery", []string{"hosted_apps_v1"}, func(c *V2SessionConfig) { c.HostedAppsDiscovery = nil }, false},
		{"no readiness", []string{"hosted_apps_v1"}, func(c *V2SessionConfig) { c.HostedAppsReady = nil }, false},
		{"false readiness", []string{"hosted_apps_v1"}, func(c *V2SessionConfig) { c.HostedAppsReady = func() bool { return false } }, false},
		{"no cancellable delivery", []string{"hosted_apps_v1"}, func(c *V2SessionConfig) { c.HostedAppsSend = nil }, false},
		{"production defaults", []string{"hosted_apps_v1"}, func(c *V2SessionConfig) {
			c.HostedAppsDiscovery, c.HostedAppsReady, c.HostedAppsSend = nil, nil, nil
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &testAppDiscovery{}
			p := testHostedStartCaps(t, d, tc.caps, tc.set)
			if slices.Contains(p.ack.Capabilities, "hosted_apps_v1") != tc.want || len(p.ack.Capabilities) > 2 {
				t.Fatalf("capabilities: %v", p.ack.Capabilities)
			}
			p.request(0, "list_apps", `{}`)
			e, _ := p.next()
			if tc.want {
				if e.Type != "apps" || string(e.Payload) != `{"revision":0,"items":[],"next_cursor":null}` || e.InReplyTo == nil || *e.InReplyTo != 0 {
					t.Fatalf("empty discovery: %+v", e)
				}
			} else {
				testHostedError(t, e, 0, "protocol.unsupported")
				d.mu.Lock()
				calls := d.calls
				d.mu.Unlock()
				if calls != 0 {
					t.Fatalf("disabled discovery calls=%d", calls)
				}
			}
		})
	}
}

func TestHostedDiscoveryEnvelopeValidation(t *testing.T) {
	p := testHostedStart(t, &testAppDiscovery{}, nil)
	base := `{"id":41,"type":"list_apps","ts":"2026-10-10T18:00:01Z","payload":{}}`
	for _, tc := range []struct{ name, raw string }{
		{"missing ts", strings.Replace(base, `"ts":"2026-10-10T18:00:01Z",`, "", 1)},
		{"alias ts", strings.Replace(base, `"ts"`, `"TS"`, 1)},
		{"null ts", strings.Replace(base, `"2026-10-10T18:00:01Z"`, "null", 1)},
		{"invalid ts", strings.Replace(base, `"2026-10-10T18:00:01Z"`, `"tomorrow"`, 1)},
		{"offset ts", strings.Replace(base, `18:00:01Z`, `18:00:01+02:00`, 1)},
		{"one digit hour", strings.Replace(base, `18:00:01Z`, `8:00:01Z`, 1)},
		{"comma fraction", strings.Replace(base, `18:00:01Z`, `18:00:01,5Z`, 1)},
		{"empty fraction", strings.Replace(base, `18:00:01Z`, `18:00:01.Z`, 1)},
		{"missing payload", strings.Replace(base, `,"payload":{}`, "", 1)},
		{"alias payload", strings.Replace(base, `"payload"`, `"PAYLOAD"`, 1)},
		{"array payload", strings.Replace(base, `"payload":{}`, `"payload":[]`, 1)},
		{"null payload", strings.Replace(base, `"payload":{}`, `"payload":null`, 1)},
		{"alias type", strings.Replace(base, `"type"`, `"TYPE"`, 1)},
		{"duplicate type", strings.Replace(base, `"type":"list_apps"`, `"type":"list_apps","type":"unknown"`, 1)},
		{"reverse duplicate type", strings.Replace(base, `"type":"list_apps"`, `"type":"unknown","type":"list_apps"`, 1)},
		{"trailing JSON", base + `{}`},
		{"invalid UTF8", strings.Replace(base, `"payload":{}`, "\"payload\":{\"unknown\":\"\xff\"}", 1)},
		{"nested duplicate array", strings.Replace(base, `"payload":{}`, `"payload":{"extra":[{"x":1,"x":2}]}`, 1)},
		{"cancel missing target", strings.Replace(base, "list_apps", "app_cancel", 1)},
		{"cancel null target", strings.Replace(strings.Replace(base, "list_apps", "app_cancel", 1), `"payload":{}`, `"payload":{"request_id":null}`, 1)},
		{"cancel null app", strings.Replace(strings.Replace(base, "list_apps", "app_cancel", 1), `"payload":{}`, `"payload":{"request_id":1,"app_id":null}`, 1)},
		{"cancel invalid app", strings.Replace(strings.Replace(base, "list_apps", "app_cancel", 1), `"payload":{}`, `"payload":{"request_id":1,"app_id":"../host"}`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.raw(tc.raw)
			e, _ := p.next()
			testHostedError(t, e, 41, "app.invalid_request")
		})
	}
	for _, key := range []string{"conversation_id", "session_id", "event_id", "history_entry_id", "session_state_cleared", "payload_encrypted", "in_reply_to"} {
		for _, value := range []string{"null", "false"} {
			p.raw(strings.TrimSuffix(base, "}") + fmt.Sprintf(`,%q:%s}`, key, value))
			e, _ := p.next()
			testHostedError(t, e, 41, "app.invalid_request")
		}
	}
	for _, target := range []string{`true`, `"1"`, `-1`, `1.5`, `9007199254740992`} {
		p.request(42, "app_cancel", `{"request_id":`+target+`}`)
		e, _ := p.next()
		testHostedError(t, e, 42, "app.invalid_request")
	}
	p.raw(`{"id":9007199254740991,"ID":5,"type":"list_apps","TYPE":"app_removed","ts":"2026-10-10T18:00:01Z","TS":null,"payload":{},"PAYLOAD":{"cursor":null}}`)
	e, _ := p.next()
	if e.Type != "apps" || e.InReplyTo == nil || *e.InReplyTo != hostedMaxID {
		t.Fatalf("alias overrode canonical authority: %+v", e)
	}
	for _, ts := range []string{"2026-10-10T08:00:01Z", "2026-10-10T18:00:01.5Z", "2026-10-10T18:00:01.123456789Z"} {
		p.raw(strings.Replace(base, "2026-10-10T18:00:01Z", ts, 1))
		e, _ := p.next()
		if e.Type != "apps" || e.InReplyTo == nil || *e.InReplyTo != 41 {
			t.Fatalf("valid UTC timestamp %q: %+v", ts, e)
		}
	}
}

func TestHostedDiscoveryUncorrelatable(t *testing.T) {
	for _, id := range []string{`null`, `true`, `-1`, `1.5`, `"1"`, `9007199254740992`, `1,"id":2`} {
		t.Run(id, func(t *testing.T) {
			p := testHostedStart(t, &testAppDiscovery{}, nil)
			p.raw(`{"id":` + id + `,"type":"list_apps","ts":"2026-10-10T18:00:01Z","payload":{}}`)
			testHostedClose(t, p)
		})
	}
}

func testHostedRecords(title string, count int) *testAppDiscovery {
	d := &testAppDiscovery{revision: uint64(count)}
	for i := count - 1; i >= 0; i-- {
		d.records = append(d.records, apps.Record{AppID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), Title: title, Desired: "available", State: "starting", Revision: uint64(i + 1)})
	}
	return d
}

func testHostedPage(t *testing.T, p *testHostedPeer, id uint64, cursor *string) protocol.AppsPayload {
	t.Helper()
	payload := `{}`
	if cursor != nil {
		payload = fmt.Sprintf(`{"cursor":%q}`, *cursor)
	}
	p.request(id, "list_apps", payload)
	e, _ := p.next()
	var page protocol.AppsPayload
	if err := json.Unmarshal(e.Payload, &page); err != nil || e.Type != "apps" || e.InReplyTo == nil || *e.InReplyTo != id {
		t.Fatalf("page: %+v %v", e, err)
	}
	return page
}

func TestHostedDiscoveryCursors(t *testing.T) {
	t.Run("host binding and drift", func(t *testing.T) {
		da, db := testHostedRecords("Host A", 17), testHostedRecords("Host B", 17)
		a := testHostedStart(t, da, func(c *V2SessionConfig) { c.ServerID = "host-a" })
		b := testHostedStart(t, db, func(c *V2SessionConfig) { c.ServerID = "host-b" })
		pa, pb := testHostedPage(t, a, 10, nil), testHostedPage(t, b, 10, nil)
		if pa.Items[0].AppID != pb.Items[0].AppID || pa.Items[0].Title != "Host A" || pb.Items[0].Title != "Host B" {
			t.Fatalf("host isolation: %+v %+v", pa, pb)
		}
		for _, tc := range []struct {
			p      *testHostedPeer
			cursor string
		}{{a, *pb.NextCursor}, {b, *pa.NextCursor}, {a, *pa.NextCursor + "x"}} {
			tc.p.request(11, "list_apps", fmt.Sprintf(`{"cursor":%q}`, tc.cursor))
			e, _ := tc.p.next()
			testHostedError(t, e, 11, "app.invalid_request")
		}
		testHostedPage(t, b, 12, pb.NextCursor)
		da.mu.Lock()
		da.revision++
		da.mu.Unlock()
		a.request(12, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *pa.NextCursor))
		e, _ := a.next()
		testHostedError(t, e, 12, "apps.changed")
		a.request(13, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *pa.NextCursor))
		e, _ = a.next()
		testHostedError(t, e, 13, "app.invalid_request")
		if got := testHostedPage(t, a, 14, nil); got.Revision != 18 {
			t.Fatal(got.Revision)
		}
	})
	t.Run("expiration and fresh replacement", func(t *testing.T) {
		var seconds atomic.Int64
		p := testHostedStart(t, testHostedRecords("Host", 17), func(c *V2SessionConfig) {
			c.hostedNow = func() time.Time { return time.Unix(seconds.Load(), 0) }
		})
		first := testHostedPage(t, p, 10, nil)
		seconds.Store(29)
		testHostedPage(t, p, 11, first.NextCursor)
		first = testHostedPage(t, p, 12, nil)
		seconds.Store(59)
		p.request(13, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *first.NextCursor))
		e, _ := p.next()
		testHostedError(t, e, 13, "apps.changed")
		first = testHostedPage(t, p, 14, nil)
		testHostedPage(t, p, 15, nil)
		p.request(16, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *first.NextCursor))
		e, _ = p.next()
		testHostedError(t, e, 16, "app.invalid_request")
	})
	t.Run("reconnect", func(t *testing.T) {
		p := testHostedStart(t, testHostedRecords("Host", 17), nil)
		first := testHostedPage(t, p, 10, nil)
		p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
		testHostedGone(t, p)
		p.open([]string{"hosted_apps_v1"})
		p.request(11, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *first.NextCursor))
		e, _ := p.next()
		testHostedError(t, e, 11, "app.invalid_request")
		testHostedPage(t, p, 12, nil)
	})
}

func TestHostedDiscoveryCursorBlockedRead(t *testing.T) {
	for _, action := range []string{"expiry", "drift", "cancel"} {
		t.Run(action, func(t *testing.T) {
			var seconds atomic.Int64
			d := testHostedRecords("Host", 17)
			p := testHostedStart(t, d, func(c *V2SessionConfig) {
				c.hostedNow = func() time.Time { return time.Unix(seconds.Load(), 0) }
			})
			first := testHostedPage(t, p, 10, nil)
			p.waitEvent("v2.hosted.reader_exited", "")
			d.mu.Lock()
			d.release, d.entered = make(chan struct{}), make(chan struct{}, 1)
			d.mu.Unlock()
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(d.release) }) })
			p.request(11, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *first.NextCursor))
			testHostedAwait(t, d.entered)
			switch action {
			case "expiry":
				seconds.Store(30)
			case "drift":
				d.mu.Lock()
				d.revision++
				d.mu.Unlock()
			case "cancel":
				p.request(12, "app_cancel", `{"request_id":11}`)
				e, _ := p.next()
				if e.Type != "app_cancelled" || e.InReplyTo == nil || *e.InReplyTo != 12 {
					t.Fatal(e)
				}
			}
			release.Do(func() { close(d.release) })
			p.waitEvent("v2.hosted.reader_exited", "")
			if action != "cancel" {
				e, _ := p.next()
				testHostedError(t, e, 11, "apps.changed")
			}
			p.request(13, "list_apps", fmt.Sprintf(`{"cursor":%q}`, *first.NextCursor))
			e, _ := p.next()
			testHostedError(t, e, 13, "app.invalid_request")
			testHostedPage(t, p, 14, nil)
		})
	}
}

func TestHostedDiscoveryDefaultSenderIDs(t *testing.T) {
	p := testHostedStart(t, nil, func(c *V2SessionConfig) { c.HostedAppsReady, c.HostedAppsSend = nil, nil })
	p.request(10, "unknown_legacy_request", `{}`)
	e, _ := p.next()
	if e.ID != 2 {
		t.Fatalf("legacy first sender ID: %+v", e)
	}
	p.request(11, "list_apps", `{}`)
	e, _ = p.next()
	testHostedError(t, e, 11, "protocol.unsupported")
	if e.ID != 3 {
		t.Fatalf("hosted reply reused legacy sender ID: %+v", e)
	}
	p.request(12, "unknown_legacy_request", `{}`)
	e, _ = p.next()
	if e.ID != 4 || e.Type != "error" {
		t.Fatalf("shared sender IDs: %+v", e)
	}
}

func TestHostedDiscoveryThreadSenderIgnoresProducerID(t *testing.T) {
	p := testHostedStartCaps(t, nil, nil, func(c *V2SessionConfig) { c.HostedAppsReady, c.HostedAppsSend = nil, nil })
	if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{ID: hostedMaxID, Type: "workspace_updated", TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	e, _ := p.next()
	if e.ID != 2 {
		t.Fatal(e)
	}
	p.request(10, "list_apps", `{}`)
	e, _ = p.next()
	testHostedError(t, e, 10, protocol.CodeProtocolUnsupported)
	if e.ID != 3 {
		t.Fatal("producer ID exhausted the thread sender")
	}
}
