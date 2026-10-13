package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestHostedAssetReconnect(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	p := testAssetStart(t, []byte("fresh"), 200, func(c *V2SessionConfig) {
		base := c.hostedPrepareAsset
		c.hostedPrepareAsset = func(ctx context.Context, a, b, r string, in apps.AssetRequest) (hostedResponse, error) {
			if calls.Add(1) == 1 {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil, ctx.Err()
			}
			return base(ctx, a, b, r, in)
		}
	})
	p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	testHostedAwait(t, entered)
	p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
	testHostedGone(t, p)
	p.open([]string{protocol.CapabilityHostedAppsV1})
	close(release)
	p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	meta := testAssetMeta(t, p, 10)
	if meta.SizeBytes != 5 || string(testAssetChunk(t, p, 10, 0)) != "fresh" {
		t.Fatal("rebound completion")
	}
	p.request(20, protocol.TypeListApps, `{}`)
	e, _ := p.next()
	if e.Type != protocol.TypeApps {
		t.Fatal(e)
	}
	testResourceIdle(t, p.mgr)
}
func TestHostedAssetRetirement(t *testing.T) {
	for _, action := range []string{"backend", "release", "process", "revocation", "disconnect", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			prepared := make(chan *testAssetResponse, 1)
			p := testAssetStart(t, bytes.Repeat([]byte("x"), 70000), 200, func(c *V2SessionConfig) {
				base := c.hostedPrepareAsset
				c.hostedPrepareAsset = func(ctx context.Context, a, b, r string, in apps.AssetRequest) (hostedResponse, error) {
					q, err := base(ctx, a, b, r, in)
					if err == nil {
						prepared <- q.(*testAssetResponse)
					}
					return q, err
				}
			})
			p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
			testAssetMeta(t, p, 10)
			testAssetChunk(t, p, 10, 0)
			response := <-prepared
			switch action {
			case "backend", "release", "process":
				response.Close()
				e, _ := p.next()
				testAssetError(t, e, 10, "app.unavailable", true)
				// Retired credit cannot read/revive the response, including duplicate credit.
				testAssetCredit(p, 10, 1, testAssetApp)
				p.request(20, protocol.TypeListApps, `{}`)
				e, _ = p.next()
				if e.Type != protocol.TypeApps {
					t.Fatal(e)
				}
			case "revocation":
				p.mgr.cfg.Devices.Remove(v2TestDevName)
				testHostedClose(t, p)
			case "disconnect":
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
				testHostedGone(t, p)
			case "shutdown":
				p.stop()
			}
			testResourceIdle(t, p.mgr)
		})
	}
}
func TestHostedAssetBlockedWriter(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	p := testAssetStart(t, []byte("blocked"), 200, func(c *V2SessionConfig) {
		out := c.Outbound
		c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
			if e.ConnID == v2TestConnID && e.CloseCode == 0 && first.CompareAndSwap(false, true) {
				close(entered)
				defer close(exited)
				<-ctx.Done()
				return ctx.Err()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return out(e)
		}
	})
	p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	testHostedAwait(t, entered)
	other := p.sibling("unrelated")
	testHostedPage(t, other, 30, nil)
	testAssetCancel(p, 11, 10, testAssetApp)
	testHostedClose(t, p)
	testHostedAwait(t, exited)
	testResourceIdle(t, p.mgr)
}
func TestHostedAssetQueuedCancellation(t *testing.T) {
	p, entered, _, release := testHostedBlockedOutput(t, &testAppDiscovery{})
	// Configure the provider before starting this request; Run never reads it earlier.
	p.mgr.cfg.hostedPrepareAsset = testAssetPrepare([]byte("queued"), 200)
	p.request(10, protocol.TypeAppCancel, `{"request_id":999}`)
	testHostedAwait(t, entered)
	p.request(11, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	p.waitEvent("v2.hosted.queued", protocol.TypeAppResponse)
	testAssetCancel(p, 12, 11, testAssetApp)
	p.waitEvent("v2.hosted.queued", protocol.TypeAppCancelled)
	release()
	for _, id := range []uint64{10, 12} {
		e, _ := p.next()
		if e.Type != protocol.TypeAppCancelled || e.InReplyTo == nil || *e.InReplyTo != id {
			t.Fatal(e)
		}
	}
	testResourceIdle(t, p.mgr)
}
func TestHostedAssetBackendErrors(t *testing.T) {
	for _, tc := range []struct {
		err   error
		code  string
		retry bool
	}{{apps.ErrAssetPath, "app.invalid_request", false}, {apps.ErrNotFound, "app.not_found", false}, {apps.ErrReleaseChanged, "app.release_changed", false}, {apps.ErrUnavailable, "app.unavailable", true}, {apps.ErrResponseLimit, "app.limit_exceeded", false}, {apps.ErrResponseCapacity, "app.busy", true}, {apps.ErrInvalidStorage, "app.io_failed", true}, {apps.ErrResponseIO, "app.io_failed", true}} {
		t.Run(tc.code, func(t *testing.T) {
			p := testAssetStart(t, nil, 200, func(c *V2SessionConfig) {
				c.hostedPrepareAsset = func(context.Context, string, string, string, apps.AssetRequest) (hostedResponse, error) {
					return nil, tc.err
				}
			})
			p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
			e, _ := p.next()
			testAssetError(t, e, 10, tc.code, tc.retry)
			testResourceIdle(t, p.mgr)
		})
	}
}
func TestHostedAssetRealTransport(t *testing.T) {
	p := testHostedRealTransport(t, true)
	p.mgr.cfg.hostedPrepareAsset = testAssetPrepare(bytes.Repeat([]byte{0, 255, 1}, 22000), 200)
	init := p.hello([]string{protocol.CapabilityHostedAppsV1})
	_, send, recv, err := init.ReadResp(decodeRespFrame(t, p.next()))
	if err != nil {
		t.Fatal(err)
	}
	request := func(id uint64, typ, payload string) {
		t.Helper()
		msg, err := send.Encrypt([]byte(testAssetEnvelope(id, typ, payload)))
		if err != nil {
			t.Fatal(err)
		}
		p.write(wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, msg))
	}
	request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	e := decryptAppFrame(t, p.next(), recv)
	var meta protocol.AppResponsePayload
	if e.Type != protocol.TypeAppResponse || json.Unmarshal(e.Payload, &meta) != nil || meta.SizeBytes != 66000 || meta.TotalChunks != 3 {
		t.Fatal(e)
	}
	var body []byte
	for i := uint64(0); i < 3; i++ {
		if i > 0 {
			request(20+i, protocol.TypeAppResponseCredit, testAssetCreditPayload(10, i, testAssetApp))
		}
		raw := threadPlain(t, p.next(), recv)
		if len(raw) > protocol.MaxThreadEnvelopeBytes {
			t.Fatal("plaintext limit", len(raw))
		}
		if json.Unmarshal(raw, &e) != nil {
			t.Fatal("invalid plaintext")
		}
		var chunk protocol.AppResponseChunkPayload
		if e.Type != protocol.TypeAppResponseChunk || json.Unmarshal(e.Payload, &chunk) != nil || chunk.Index != i || e.ID != i+3 || e.InReplyTo == nil || *e.InReplyTo != 10 {
			t.Fatal(e)
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(chunk.Data)
		if err != nil || len(decoded) == 0 || len(decoded) > 32768 {
			t.Fatal("chunk", err, len(decoded))
		}
		body = append(body, decoded...)
	}
	if !bytes.Equal(body, bytes.Repeat([]byte{0, 255, 1}, 22000)) || meta.SHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) {
		t.Fatal("transport bytes/digest mismatch")
	}
	testResourceIdle(t, p.mgr)
}

func TestHostedAssetRealTransportBlockedWriter(t *testing.T) {
	p := testHostedRealTransport(t, true)
	p.mgr.cfg.hostedPrepareAsset = testAssetPrepare([]byte("blocked"), 200)
	init := p.hello([]string{protocol.CapabilityHostedAppsV1})
	_, send, _, err := init.ReadResp(decodeRespFrame(t, p.next()))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := p.gate.arm(t)
	request := func(id uint64, typ, payload string) {
		t.Helper()
		msg, err := send.Encrypt([]byte(testAssetEnvelope(id, typ, payload)))
		if err != nil {
			t.Fatal(err)
		}
		p.write(wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, msg))
	}
	request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
	testHostedAwait(t, entered)
	request(11, protocol.TypeAppCancel, fmt.Sprintf(`{"request_id":10,"app_id":%q}`, testAssetApp))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for {
		found := false
		for _, c := range p.mgr.ActiveConns(ctx) {
			found = found || c.ConnID == v2TestConnID
		}
		if !found {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("cancellation stalled behind pump")
		}
		time.Sleep(time.Millisecond)
	}
	testResourceIdle(t, p.mgr)
	release()
	testHostedAwait(t, p.socketDone)
	// Socket retirement fences every prior publication from this transport generation.
	select {
	case e := <-p.wires:
		if len(e.Frame) != 0 {
			t.Fatal("late sealed asset metadata")
		}
	default:
	}
}

type testAssetSlowResponse struct {
	*testAssetResponse
	stalled bool
}

func (p *testAssetSlowResponse) Read(b []byte) (int, error) {
	select {
	case <-p.ctx.Done():
		return 0, p.ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}
	if p.stalled {
		return 0, nil
	}
	return p.testAssetResponse.Read(b[:min(len(b), 1)])
}
func TestHostedAssetReadProgress(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(fmt.Sprint(stalled), func(t *testing.T) {
			p := testAssetStart(t, []byte("abcd"), 200, func(c *V2SessionConfig) {
				c.hostedInactivity = 250 * time.Millisecond
				c.hostedDeadline = 2 * time.Second
				base := c.hostedPrepareAsset
				c.hostedPrepareAsset = func(ctx context.Context, a, b, r string, in apps.AssetRequest) (hostedResponse, error) {
					q, err := base(ctx, a, b, r, in)
					if err != nil {
						return nil, err
					}
					return &testAssetSlowResponse{testAssetResponse: q.(*testAssetResponse), stalled: stalled}, nil
				}
			})
			p.request(10, protocol.TypeAppAssetRequest, testAssetPayload("/a"))
			testAssetMeta(t, p, 10)
			if stalled {
				e, _ := p.next()
				testAssetError(t, e, 10, "app.timeout", true)
			} else {
				if string(testAssetChunk(t, p, 10, 0)) != "abcd" {
					t.Fatal("progressive body")
				}
			}
			testResourceIdle(t, p.mgr)
		})
	}
}
