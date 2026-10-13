package relay

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testResourceOp(in *hostedInbox, app string) *hostedResource {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &hostedResource{inbox: in, app: app, ctx: ctx, cancel: cancel}
}
func TestHostedResourceBudgets(t *testing.T) {
	for _, kind := range []string{"connection_count", "app_count", "daemon_count"} {
		t.Run(kind, func(t *testing.T) {
			var b hostedResourceBudget
			limit := 8
			if kind == "app_count" {
				limit = 32
			}
			if kind == "daemon_count" {
				limit = 128
			}
			var admitted []*hostedResource
			in := &hostedInbox{}
			for i := 0; i <= limit; i++ {
				if kind != "connection_count" && i%8 == 0 {
					in = &hostedInbox{}
				}
				app := "a"
				if kind == "daemon_count" {
					app = fmt.Sprintf("app-%d", i/32)
				}
				op := testResourceOp(in, app)
				if got := b.admit(op); got != (i < limit) {
					t.Fatalf("index %d admitted=%v", i, got)
				}
				if i < limit {
					admitted = append(admitted, op)
				} else {
					b.finish(admitted[0])
					if !b.admit(op) {
						t.Fatal("capacity did not recover")
					}
					b.finish(op)
				}
			}
			for _, op := range admitted[1:] {
				b.finish(op)
			}
			if b.count != 0 || len(b.connections) != 0 || len(b.apps) != 0 {
				t.Fatal("leaked counts")
			}
		})
	}
	var b hostedResourceBudget
	ops := make([]*hostedResource, 5)
	for i := range ops {
		ops[i] = testResourceOp(&hostedInbox{}, fmt.Sprint(i))
		if !b.admit(ops[i]) {
			t.Fatal("admission")
		}
	}
	for i := 0; i < 4; i++ {
		if !b.reserve(ops[i], 16<<20) {
			t.Fatal("exact byte limit")
		}
	}
	if b.reserve(ops[0], 1) || b.reserve(ops[4], 1) || b.reserve(ops[4], -1) {
		t.Fatal("byte overflow admitted")
	}
	b.release(ops[0], 32768)
	if !b.reserve(ops[4], 32768) || b.reserve(ops[4], 1) {
		t.Fatal("incremental reservation recovery")
	}
	b.release(ops[4], 32768)
	ops[4].cancel(context.Canceled)
	if b.reserve(ops[4], 1) {
		t.Fatal("retired reservation")
	}
	b.release(ops[0], (16<<20)-32768)
	for i := 1; i < 4; i++ {
		b.release(ops[i], 16<<20)
	}
	for _, op := range ops {
		b.finish(op)
	}
	if b.bytes != 0 || b.count != 0 || len(b.connections) != 0 {
		t.Fatal("leaked bytes")
	}
}
func testResourceIdle(t *testing.T, m *V2SessionManager) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.resourceBudget.mu.Lock()
		count, held := m.resourceBudget.count, m.resourceBudget.bytes
		m.resourceBudget.mu.Unlock()
		if count == 0 && held == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("resource capacity did not recover")
}
func TestHostedAssetAdmission(t *testing.T) {
	entered := make(chan struct{}, 16)
	cleanup := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(cleanup) }) }
	t.Cleanup(release)
	p := testAssetStart(t, nil, 200, func(c *V2SessionConfig) {
		c.hostedPrepareAsset = func(ctx context.Context, _, _, _ string, _ apps.AssetRequest) (hostedResponse, error) {
			entered <- struct{}{}
			<-ctx.Done()
			select {
			case <-cleanup:
			case <-t.Context().Done():
			}
			return nil, ctx.Err()
		}
	})
	for id := uint64(10); id < 18; id++ {
		p.request(id, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
		testHostedAwait(t, entered)
	}
	p.request(18, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	e, _ := p.next()
	testAssetError(t, e, 18, "app.busy", true)
	testAssetCancel(p, 20, 10, testAssetApp)
	e, _ = p.next()
	if e.Type != protocol.TypeAppCancelled {
		t.Fatal(e)
	}
	p.request(21, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	e, _ = p.next()
	testAssetError(t, e, 21, "app.busy", true)
	for id := uint64(11); id < 18; id++ {
		testAssetCancel(p, id+20, id, testAssetApp)
		p.next()
	}
	release()
	testResourceIdle(t, p.mgr)
	p.request(50, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	testHostedAwait(t, entered)
	testAssetCancel(p, 51, 50, testAssetApp)
	e, _ = p.next()
	if e.Type != protocol.TypeAppCancelled {
		t.Fatal(e)
	}
	testResourceIdle(t, p.mgr)
}
func TestHostedAssetByteCapacity(t *testing.T) {
	body := make([]byte, 8<<20)
	p := testAssetStart(t, body, 200, nil)
	p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	testAssetMeta(t, p, 10)
	testAssetChunk(t, p, 10, 0)
	p.request(11, protocol.TypeAppAssetRequest, testAssetPayload("/b"))
	e, _ := p.next()
	testAssetError(t, e, 11, "app.busy", true)
	testAssetCancel(p, 12, 10, testAssetApp)
	p.next()
	testResourceIdle(t, p.mgr)
	p.request(13, protocol.TypeAppAssetRequest, testAssetPayload("/b"))
	testAssetMeta(t, p, 13)
	testAssetChunk(t, p, 13, 0)
	testAssetCancel(p, 14, 13, testAssetApp)
	p.next()
	testResourceIdle(t, p.mgr)
}

func TestHostedAssetControlCapacity(t *testing.T) {
	p, entered, _, release := testHostedBlockedOutput(t, &testAppDiscovery{})
	p.mgr.cfg.hostedPrepareAsset = testAssetPrepare([]byte("x"), 200)
	p.request(10, protocol.TypeAppCancel, `{"request_id":999}`)
	testHostedAwait(t, entered)
	for id := uint64(20); id < 28; id++ {
		p.request(id, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
		p.waitEvent("v2.hosted.queued", protocol.TypeAppResponse)
	}
	for id := uint64(50); id < 65; id++ {
		testAssetCancel(p, id, 999, "")
		p.waitEvent("v2.hosted.queued", protocol.TypeAppCancelled)
	}
	// Eight resource envelopes leave all sixteen control slots independently usable.
	waitConnOpen(t, p.mgr, p.conn)
	release()
	responses, chunks, controls := 0, 0, 0
	for i := 0; i < 32; i++ {
		e, _ := p.next()
		switch e.Type {
		case protocol.TypeAppResponse:
			responses++
		case protocol.TypeAppResponseChunk:
			chunks++
		case protocol.TypeAppCancelled:
			controls++
		default:
			t.Fatalf("capacity/order failure %+v", e)
		}
	}
	if responses != 8 || chunks != 8 || controls != 16 {
		t.Fatal(responses, chunks, controls)
	}
	testResourceIdle(t, p.mgr)
}
