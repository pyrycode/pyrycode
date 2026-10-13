package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type testAssetResponse struct {
	mu   sync.Mutex
	body *bytes.Reader
	meta apps.ResponseMetadata
	ctx  context.Context
	in   apps.AssetRequest
	done chan struct{}
	once sync.Once
}

func testAssetPrepare(body []byte, status int) func(context.Context, string, string, string, apps.AssetRequest) (hostedResponse, error) {
	return func(ctx context.Context, _, _, _ string, in apps.AssetRequest) (hostedResponse, error) {
		if !in.Reserve(int64(len(body)) + 1) {
			return nil, apps.ErrResponseCapacity
		}
		in.Release(1) // EOF probe is released while the stable body remains reserved.
		if len(body) > 0 {
			in.Progress()
		}
		return &testAssetResponse{body: bytes.NewReader(body), ctx: ctx, in: in, done: make(chan struct{}), meta: apps.ResponseMetadata{Status: status, Headers: map[string]string{"content-type": "application/octet-stream"}, Size: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body))}}, nil
	}
}
func (p *testAssetResponse) Metadata() (apps.ResponseMetadata, error) {
	if err := p.ctx.Err(); err != nil {
		return apps.ResponseMetadata{}, err
	}
	select {
	case <-p.done:
		return apps.ResponseMetadata{}, apps.ErrUnavailable
	default:
	}
	return p.meta, nil
}
func (p *testAssetResponse) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.Metadata(); err != nil {
		return 0, err
	}
	return p.body.Read(b)
}
func (p *testAssetResponse) Done() <-chan struct{} { return p.done }
func (p *testAssetResponse) Close() error {
	p.once.Do(func() { close(p.done); p.in.Release(p.meta.Size) })
	return nil
}
func (p *testAssetResponse) Complete(error) error { return p.Close() }
func testAssetStart(t *testing.T, body []byte, status int, tweak func(*V2SessionConfig)) *testHostedPeer {
	return testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
		c.hostedPrepareAsset = testAssetPrepare(body, status)
		if tweak != nil {
			tweak(c)
		}
	})
}
func testAssetMeta(t *testing.T, p *testHostedPeer, id uint64) protocol.AppResponsePayload {
	t.Helper()
	e, obj := p.next()
	var meta protocol.AppResponsePayload
	if e.Type != protocol.TypeAppResponse || e.InReplyTo == nil || *e.InReplyTo != id || json.Unmarshal(e.Payload, &meta) != nil {
		t.Fatalf("metadata %+v", e)
	}
	for _, k := range []string{"conversation_id", "session_id", "event_id", "history_entry_id", "payload_encrypted", "session_state_cleared"} {
		if _, ok := obj[k]; ok {
			t.Fatal("forbidden", k)
		}
	}
	return meta
}
func testAssetChunk(t *testing.T, p *testHostedPeer, id, index uint64) []byte {
	t.Helper()
	e, _ := p.next()
	var c protocol.AppResponseChunkPayload
	if e.Type != protocol.TypeAppResponseChunk || e.InReplyTo == nil || *e.InReplyTo != id || json.Unmarshal(e.Payload, &c) != nil || c.AppID != testAssetApp || c.Index != index {
		t.Fatalf("chunk %+v %#+v", e, c)
	}
	b, err := base64.StdEncoding.Strict().DecodeString(c.Data)
	if err != nil || len(b) == 0 || len(b) > 32768 {
		t.Fatal("chunk bytes", len(b), err)
	}
	return b
}
func testAssetCredit(p *testHostedPeer, id, index uint64, app string) {
	p.request(id+1000+index, protocol.TypeAppResponseCredit, fmt.Sprintf(`{"app_id":%q,"request_id":%d,"next_index":%d}`, app, id, index))
}
func testAssetCancel(p *testHostedPeer, id, target uint64, app string) {
	scope := ""
	if app != "" {
		scope = fmt.Sprintf(`,"app_id":%q`, app)
	}
	p.request(id, protocol.TypeAppCancel, fmt.Sprintf(`{"request_id":%d%s}`, target, scope))
}
func TestHostedAssetTransfer(t *testing.T) {
	for _, tc := range []struct {
		body   []byte
		status int
	}{{bytes.Repeat([]byte{0, 255, 1, 2, 3}, 15000), 200}, {nil, 200}, {nil, 404}} {
		p := testAssetStart(t, tc.body, tc.status, nil)
		p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/index.html"))
		meta := testAssetMeta(t, p, 10)
		if meta.AppID != testAssetApp || meta.ReleaseVersion != "1.2.3" || meta.Status != tc.status || meta.SizeBytes != uint64(len(tc.body)) || meta.TotalChunks != uint64((len(tc.body)+32767)/32768) || meta.SHA256 != fmt.Sprintf("%x", sha256.Sum256(tc.body)) {
			t.Fatalf("metadata %#v", meta)
		}
		var got []byte
		for i := uint64(0); i < meta.TotalChunks; i++ {
			if i > 0 {
				testAssetCredit(p, 10, i, testAssetApp)
			}
			got = append(got, testAssetChunk(t, p, 10, i)...)
		}
		if !bytes.Equal(got, tc.body) {
			t.Fatal("body mismatch")
		}
		p.request(500, protocol.TypeListApps, `{}`)
		e, _ := p.next()
		if e.Type != protocol.TypeApps {
			t.Fatalf("unexpected terminal output: %+v", e)
		}
		p.stop()
	}
}
func TestHostedAssetCancel(t *testing.T) {
	p := testAssetStart(t, bytes.Repeat([]byte("x"), 70000), 200, nil)
	p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	testAssetMeta(t, p, 10)
	testAssetChunk(t, p, 10, 0)
	for _, scope := range []string{"", "33333333-3333-4333-8333-333333333333", testAssetApp, testAssetApp} {
		testAssetCancel(p, 20, 10, scope)
		e, _ := p.next()
		var c protocol.AppCancelPayload
		if e.Type != protocol.TypeAppCancelled || e.InReplyTo == nil || *e.InReplyTo != 20 || json.Unmarshal(e.Payload, &c) != nil || c.RequestID != 10 || c.AppID != scope {
			t.Fatal(e)
		}
	}
	testAssetCredit(p, 10, 1, testAssetApp)
	p.request(30, protocol.TypeListApps, `{}`)
	e, _ := p.next()
	if e.Type != protocol.TypeApps {
		t.Fatal(e)
	}
}
func TestHostedAssetCredit(t *testing.T) {
	for _, bad := range []struct {
		index uint64
		app   string
	}{{2, testAssetApp}, {1, "33333333-3333-4333-8333-333333333333"}} {
		p := testAssetStart(t, bytes.Repeat([]byte("x"), 70000), 200, nil)
		p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
		testAssetMeta(t, p, 10)
		testAssetChunk(t, p, 10, 0)
		testAssetCredit(p, 10, 0, testAssetApp) // Duplicate initial credit grants nothing.
		testAssetCredit(p, 10, bad.index, bad.app)
		e, _ := p.next()
		testHostedError(t, e, 10, "app.invalid_request")
		testAssetCredit(p, 10, 1, testAssetApp)
		p.request(30, protocol.TypeListApps, `{}`)
		e, _ = p.next()
		if e.Type != protocol.TypeApps {
			t.Fatal(e)
		}
		p.stop()
	}
}
func TestHostedAssetTimeouts(t *testing.T) {
	for _, kind := range []string{"held_credit", "stalled_prepare", "progressive_prepare", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			p := testAssetStart(t, bytes.Repeat([]byte("x"), 70000), 200, func(c *V2SessionConfig) {
				c.hostedInactivity = 250 * time.Millisecond
				c.hostedDeadline = time.Second
				if kind != "held_credit" {
					base := c.hostedPrepareAsset
					c.hostedPrepareAsset = func(ctx context.Context, a, b, r string, in apps.AssetRequest) (hostedResponse, error) {
						tick := time.NewTicker(50 * time.Millisecond)
						defer tick.Stop()
						finish := time.NewTimer(600 * time.Millisecond)
						defer finish.Stop()
						if kind == "deadline" {
							finish.Reset(2 * time.Second)
						}
						for {
							select {
							case <-ctx.Done():
								return nil, ctx.Err()
							case <-finish.C:
								return base(ctx, a, b, r, in)
							case <-tick.C:
								if kind != "stalled_prepare" {
									in.Progress()
								}
							}
						}
					}
				}
			})
			p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
			if kind == "held_credit" || kind == "progressive_prepare" {
				testAssetMeta(t, p, 10)
				testAssetChunk(t, p, 10, 0)
			}
			e, _ := p.next()
			testAssetError(t, e, 10, "app.timeout", true)
		})
	}
}

var _ io.Reader = (*testAssetResponse)(nil)

func testAssetError(t *testing.T, e protocol.Envelope, id uint64, code string, retry bool) {
	t.Helper()
	var p map[string]json.RawMessage
	if json.Unmarshal(e.Payload, &p) != nil || e.Type != protocol.TypeError || e.InReplyTo == nil || *e.InReplyTo != id || string(p["code"]) != fmt.Sprintf("%q", code) || string(p["retryable"]) != fmt.Sprintf("%v", retry) {
		t.Fatalf("error %+v", e)
	}
	if len(p) < 3 || len(p) > 4 {
		t.Fatal("error fields", p)
	}
	if code == "app.busy" && string(p["retry_after_s"]) != "1" {
		t.Fatal("retry_after", p)
	}
}
func testAssetEnvelope(id uint64, typ, payload string) string {
	return fmt.Sprintf(`{"id":%d,"type":%q,"ts":"2026-10-10T18:00:01Z","payload":%s}`, id, typ, payload)
}
func testAssetCreditPayload(id, index uint64, app string) string {
	return fmt.Sprintf(`{"app_id":%q,"request_id":%d,"next_index":%d}`, app, id, index)
}

func TestHostedAssetDuplicateCredit(t *testing.T) {
	p := testAssetStart(t, bytes.Repeat([]byte("x"), 70000), 200, nil)
	p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	testAssetMeta(t, p, 10)
	testAssetChunk(t, p, 10, 0)
	testAssetCredit(p, 10, 1, testAssetApp)
	testAssetChunk(t, p, 10, 1)
	testAssetCredit(p, 10, 1, testAssetApp)
	p.request(50, protocol.TypeListApps, `{}`)
	e, _ := p.next()
	if e.Type != protocol.TypeApps {
		t.Fatalf("duplicate credit advanced stream: %+v", e)
	}
	testAssetCredit(p, 10, 2, testAssetApp)
	testAssetChunk(t, p, 10, 2)
	testResourceIdle(t, p.mgr)
}
func TestHostedAssetMalformedCredit(t *testing.T) {
	p := testAssetStart(t, bytes.Repeat([]byte("x"), 70000), 200, nil)
	p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	testAssetMeta(t, p, 10)
	testAssetChunk(t, p, 10, 0)
	p.request(20, protocol.TypeAppResponseCredit, fmt.Sprintf(`{"app_id":%q,"request_id":10,"next_index":"1"}`, testAssetApp))
	e, _ := p.next()
	testAssetError(t, e, 10, "app.invalid_request", false)
	// Repeating malformed credit for a retired request is silent too.
	p.request(21, protocol.TypeAppResponseCredit, fmt.Sprintf(`{"app_id":%q,"request_id":10,"next_index":256}`, testAssetApp))
	p.request(50, protocol.TypeListApps, `{}`)
	e, _ = p.next()
	if e.Type != protocol.TypeApps {
		t.Fatal(e)
	}
}
func TestHostedAssetCapability(t *testing.T) {
	called := make(chan struct{}, 1)
	p := testHostedStartCaps(t, &testAppDiscovery{}, nil, func(c *V2SessionConfig) {
		c.hostedPrepareAsset = func(context.Context, string, string, string, apps.AssetRequest) (hostedResponse, error) {
			called <- struct{}{}
			return nil, apps.ErrUnavailable
		}
	})
	p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	e, _ := p.next()
	testAssetError(t, e, 10, protocol.CodeProtocolUnsupported, false)
	select {
	case <-called:
		t.Fatal("unauthorized provider call")
	default:
	}
}
