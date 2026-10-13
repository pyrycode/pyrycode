package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testAPIPrepare(body []byte, status int) func(context.Context, string, string, string, apps.APIRequest) (hostedResponse, error) {
	return func(ctx context.Context, a, b, r string, in apps.APIRequest) (hostedResponse, error) {
		return testAssetPrepare(body, status)(ctx, a, b, r, apps.AssetRequest{Reserve: in.Reserve, Release: in.Release, Progress: in.Progress})
	}
}

func TestHostedAPIBodyAdmission(t *testing.T) {
	var calls atomic.Int32
	received := make(chan []byte, 4)
	p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
		c.hostedForwardAPI = func(ctx context.Context, host, app, release string, in apps.APIRequest) (hostedResponse, error) {
			calls.Add(1)
			if host != v2TestServerID || app != testAssetApp || release != "1.2.3" || in.Path != "/api/%41" || in.Query != "x=%2f&x=&x=%2F" || in.Method != "POST" || in.Headers["content-type"] != "application/octet-stream" {
				t.Error("forwarding changed request")
			}
			received <- bytes.Clone(in.Body)
			return testAPIPrepare(nil, 204)(ctx, host, app, release, in)
		}
	})
	for i, body := range [][]byte{nil, bytes.Repeat([]byte{0, 255}, 16384)} {
		id := uint64(i + 10)
		p.request(id, protocol.TypeAppAPIRequest, testAPIPayload(body))
		testAssetMeta(t, p, id)
		if !bytes.Equal(<-received, body) {
			t.Fatal("binary request changed")
		}
		testResourceIdle(t, p.mgr)
	}
	for i, bad := range []struct{ body, code string }{
		{"AA", "app.invalid_request"}, {"AA==\n", "app.invalid_request"}, {"AB==", "app.invalid_request"}, {"AAF=", "app.invalid_request"}, {"____", "app.invalid_request"}, {"A===", "app.invalid_request"},
		{strings.Repeat("AAAA", 10923), "app.limit_exceeded"},
	} {
		id := uint64(i + 100)
		payload := strings.Replace(testAPIPayload(nil), `"body":""`, fmt.Sprintf(`"body":%q`, bad.body), 1)
		p.request(id, protocol.TypeAppAPIRequest, payload)
		e, _ := p.next()
		testAssetError(t, e, id, bad.code, false)
	}
	if calls.Load() != 2 {
		t.Fatal("rejected body dispatched", calls.Load())
	}
}

func TestHostedAPITransfer(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   []byte
	}{
		{200, bytes.Repeat([]byte{0, 255, 1}, 22000)}, {302, []byte("redirect")}, {404, []byte("missing")}, {409, []byte("conflict")}, {500, []byte("failure")}, {200, nil}, {204, nil}, {205, nil}, {304, nil},
	} {
		t.Run(fmt.Sprint(tc.status, "_", len(tc.body)), func(t *testing.T) {
			p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) { c.hostedForwardAPI = testAPIPrepare(tc.body, tc.status) })
			p.request(10, protocol.TypeAppAPIRequest, testAPIPayload(nil))
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
				t.Fatal("body changed")
			}
			testResourceIdle(t, p.mgr)
			p.request(50, protocol.TypeListApps, `{}`)
			e, _ := p.next()
			if e.Type != protocol.TypeApps {
				t.Fatal("unexpected terminal output", e)
			}
		})
	}
}

func TestHostedAPILimitsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		err   error
		code  string
		retry bool
	}{
		{apps.ErrAPIRequest, "app.invalid_request", false}, {apps.ErrAPILimit, "app.limit_exceeded", false}, {apps.ErrResponseCapacity, "app.busy", true}, {apps.ErrResponseIO, "app.io_failed", true},
	} {
		p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
			c.hostedForwardAPI = func(context.Context, string, string, string, apps.APIRequest) (hostedResponse, error) {
				return nil, tc.err
			}
		})
		p.request(10, protocol.TypeAppAPIRequest, testAPIPayload([]byte("mutation")))
		e, _ := p.next()
		testAssetError(t, e, 10, tc.code, tc.retry)
		testResourceIdle(t, p.mgr)
		p.stop()
	}
	p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) { c.hostedForwardAPI = testAPIPrepare(make([]byte, (1<<20)+1), 200) })
	p.request(10, protocol.TypeAppAPIRequest, testAPIPayload(nil))
	e, _ := p.next()
	testAssetError(t, e, 10, "app.limit_exceeded", false)
	testResourceIdle(t, p.mgr)
}

func TestHostedAPIProgress(t *testing.T) {
	for _, kind := range []string{"progressive", "stalled", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
				c.hostedInactivity, c.hostedDeadline = 250*time.Millisecond, time.Second
				c.hostedForwardAPI = func(ctx context.Context, a, b, r string, in apps.APIRequest) (hostedResponse, error) {
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
							return testAPIPrepare([]byte("ok"), 200)(ctx, a, b, r, in)
						case <-tick.C:
							if kind != "stalled" {
								in.Progress()
							}
						}
					}
				}
			})
			p.request(10, protocol.TypeAppAPIRequest, testAPIPayload([]byte("write")))
			if kind == "progressive" {
				testAssetMeta(t, p, 10)
				if string(testAssetChunk(t, p, 10, 0)) != "ok" {
					t.Fatal("progressive response")
				}
			} else {
				e, _ := p.next()
				testAssetError(t, e, 10, "app.timeout", true)
			}
			testResourceIdle(t, p.mgr)
		})
	}
}

func TestHostedAPICapability(t *testing.T) {
	var calls atomic.Int32
	p := testHostedStartCaps(t, &testAppDiscovery{}, nil, func(c *V2SessionConfig) {
		c.hostedForwardAPI = func(context.Context, string, string, string, apps.APIRequest) (hostedResponse, error) {
			calls.Add(1)
			return nil, apps.ErrUnavailable
		}
	})
	p.request(10, protocol.TypeAppAPIRequest, testAPIPayload(nil))
	e, _ := p.next()
	testAssetError(t, e, 10, protocol.CodeProtocolUnsupported, false)
	if calls.Load() != 0 {
		t.Fatal("unnegotiated forwarding")
	}
}
