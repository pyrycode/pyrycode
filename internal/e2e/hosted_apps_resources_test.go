//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type hostedWire struct {
	t     *testing.T
	phone *fakephone.Client
	seal  func(protocol.Envelope)
	next  func(time.Time) (protocol.Envelope, bool)
}

func hostedConnect(t *testing.T, fr *fakerelay.Server, home, token, public string, offer, ready bool) *hostedWire {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), readPersistedServerID(t, home), token, "hosted")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	pub, err := base64.StdEncoding.DecodeString(public)
	if err != nil {
		t.Fatal(err)
	}
	init, err := noise.NewInitiator(fakephone.InstallKey(token), pub)
	if err != nil {
		t.Fatal(err)
	}
	hello := protocol.HelloClientPayload{Role: "client", DeviceName: "hosted", ClientVersion: "test", ProtocolVersions: []string{"v2"}, Token: token}
	if offer {
		hello.Capabilities = []string{protocol.CapabilityHostedAppsV1}
	}
	msg, err := init.WriteInit(mustJSON(t, protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: mustJSON(t, hello)}))
	if err != nil {
		t.Fatal(err)
	}
	sendNoiseInit(t, phone, msg)
	frame := readInnerFrame(t, phone, 3*time.Second)
	raw, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	ack, send, recv, err := init.ReadResp(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	var payload protocol.HelloAckPayload
	if err := json.Unmarshal(ack, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(payload.Capabilities, protocol.CapabilityHostedAppsV1) != ready {
		t.Fatalf("capabilities: %v", payload.Capabilities)
	}
	seal, next := noiseWire(t, phone, send, recv)
	return &hostedWire{t, phone, seal, next}
}

func (p *hostedWire) request(id uint64, typ string, payload any) {
	p.t.Helper()
	p.seal(protocol.Envelope{ID: id, Type: typ, TS: time.Now().UTC(), Payload: mustJSON(p.t, payload)})
}

func (p *hostedWire) reply(id uint64, typ string) protocol.Envelope {
	p.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		e, ok := p.next(deadline)
		if !ok {
			p.t.Fatal("missing hosted reply", id, typ)
		}
		if e.Type == protocol.TypeAppUpdated {
			continue
		}
		if e.InReplyTo == nil || *e.InReplyTo != id || e.Type != typ {
			p.t.Fatalf("unexpected reply to %d: %+v %s", id, e, e.Payload)
		}
		if e.ConversationID != nil || e.SessionID != nil || e.EventID != nil || e.HistoryEntryID != nil || e.SessionStateCleared || e.PayloadEncrypted {
			p.t.Fatal("conversation metadata on hosted frame", e)
		}
		return e
	}
}

func (p *hostedWire) discovery(id uint64) {
	p.t.Helper()
	p.request(id, protocol.TypeListApps, protocol.ListAppsPayload{})
	e := p.reply(id, protocol.TypeApps)
	var list protocol.AppsPayload
	if err := json.Unmarshal(e.Payload, &list); err != nil || len(list.Items) != 4 || list.Items[0].AppID != hostedAppID || list.Items[0].ActiveRelease == nil || *list.Items[0].ActiveRelease != "1.0.0" {
		p.t.Fatal("discovery", string(e.Payload), err)
	}
}

func (p *hostedWire) response(id uint64, status int, want []byte, partial bool) {
	p.t.Helper()
	e := p.reply(id, protocol.TypeAppResponse)
	var meta protocol.AppResponsePayload
	if err := json.Unmarshal(e.Payload, &meta); err != nil {
		p.t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(want))
	if meta.AppID != hostedAppID || meta.ReleaseVersion != "1.0.0" || meta.Status != status || meta.SizeBytes != uint64(len(want)) || meta.SHA256 != digest || meta.TotalChunks != uint64((len(want)+32767)/32768) {
		p.t.Fatal("metadata", meta)
	}
	for name := range meta.Headers {
		if !slices.Contains([]string{"content-type", "cache-control", "etag", "last-modified"}, name) {
			p.t.Fatal("forbidden response header", name)
		}
	}
	var body []byte
	for i := uint64(0); i < meta.TotalChunks; i++ {
		if i > 0 {
			p.request(9000+i, protocol.TypeAppResponseCredit, protocol.AppResponseCreditPayload{AppID: hostedAppID, RequestID: id, NextIndex: i})
		}
		e = p.reply(id, protocol.TypeAppResponseChunk)
		var chunk protocol.AppResponseChunkPayload
		if err := json.Unmarshal(e.Payload, &chunk); err != nil {
			p.t.Fatal(err)
		}
		b, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil || chunk.AppID != hostedAppID || chunk.Index != i || len(b) == 0 || len(b) > 32768 {
			p.t.Fatal("chunk", chunk, err)
		}
		body = append(body, b...)
		if partial {
			if !bytes.Equal(body, want[:len(body)]) {
				p.t.Fatal("partial bytes")
			}
			return
		}
	}
	if !bytes.Equal(body, want) || fmt.Sprintf("%x", sha256.Sum256(body)) != meta.SHA256 {
		p.t.Fatal("body/digest mismatch")
	}
}

func hostedResourceHandler(data string) http.Handler {
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Set-Cookie", "secret=forbidden")
		w.Header().Set("Location", "http://forbidden.invalid")
		if r.URL.Path == "/api/query" {
			_, _ = io.WriteString(w, r.URL.RawQuery)
			return
		}
		if r.Method == "POST" {
			body, err := io.ReadAll(io.LimitReader(r.Body, 32769))
			if err != nil {
				w.WriteHeader(500)
				return
			}
			mu.Lock()
			err = os.WriteFile(filepath.Join(data, "api-state"), body, 0600)
			f, e := os.OpenFile(filepath.Join(data, "api-invocations"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if e == nil {
				_, e = f.WriteString("mutation\n")
				_ = f.Close()
			}
			mu.Unlock()
			if err != nil || e != nil {
				w.WriteHeader(500)
				return
			}
			if r.URL.RawQuery == "barrier=1" {
				_ = os.WriteFile(filepath.Join(data, "committed"), []byte("yes"), 0600)
				done := r.Context().Done()
				for {
					select {
					case <-done:
						_ = os.WriteFile(filepath.Join(data, "response-retired"), []byte("yes"), 0600)
						done = nil
					case <-time.After(5 * time.Millisecond):
						if _, err := os.Stat(filepath.Join(data, "release-response")); err == nil {
							w.WriteHeader(204)
							_ = os.WriteFile(filepath.Join(data, "old-response-released"), []byte("yes"), 0600)
							return
						}
					}
				}
			}
			w.WriteHeader(204)
			return
		}
		mu.Lock()
		b, _ := os.ReadFile(filepath.Join(data, "api-state"))
		mu.Unlock()
		_, _ = w.Write(b)
	})
}

func TestHostedAppsPairedResources(t *testing.T) {
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	pair, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: fr.URL() + "/v2/server", DeviceName: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	seed := hostedSeed(t, home, "Resources")
	web := filepath.Join(hostedRoot(home), hostedAppID, "builds", strings.Repeat("a", 64), "dist/web")
	if err := os.MkdirAll(web, 0700); err != nil {
		t.Fatal(err)
	}
	html := []byte(`<html><script type="module" src="./main.js"></script></html>`)
	js := []byte(`export const paired = true;`)
	binary := bytes.Repeat([]byte{0, 255, 3, 128, 7}, 14000)
	for name, b := range map[string][]byte{"index.html": html, "main.js": js, "image.bin": binary} {
		if err := os.WriteFile(filepath.Join(web, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := StartStreamInteractiveWithRelay(t, home, "11111111-1111-4111-8111-111111111111", pair.Relay, hostedNodeEnv(t)...)
	hostedRunning(t, home)
	process := hostedProof(t, home, hostedAppID)
	p := hostedConnect(t, fr, home, pair.Token, pair.ServerStaticPubkey, true, true)
	p.discovery(2)
	asset := func(path string) protocol.AppAssetRequestPayload {
		return protocol.AppAssetRequestPayload{AppID: hostedAppID, ReleaseVersion: "1.0.0", Path: path}
	}
	api := func() protocol.AppAPIRequestPayload {
		return protocol.AppAPIRequestPayload{AppID: hostedAppID, ReleaseVersion: "1.0.0", Path: "/api/state", Method: "GET"}
	}
	for i, tc := range []struct {
		path       string
		navigation bool
		body       []byte
	}{{"/index.html", false, html}, {"/main.js", false, js}, {"/image.bin", false, binary}, {"/preferences/view", true, html}} {
		r := asset(tc.path)
		r.Navigation = tc.navigation
		id := uint64(10 + i)
		p.request(id, protocol.TypeAppAssetRequest, r)
		p.response(id, 200, tc.body, false)
	}
	query := "encoded=%2F%20%2b&duplicate=one&empty=&duplicate=two&bare"
	r := api()
	r.Path = "/api/query"
	r.Query = query
	p.request(20, protocol.TypeAppAPIRequest, r)
	p.response(20, 200, []byte(query), false)
	r = api()
	r.Method = "POST"
	r.Body = base64.StdEncoding.EncodeToString([]byte("saved\x00body"))
	r.Headers = map[string]string{"content-type": "application/octet-stream"}
	p.request(21, protocol.TypeAppAPIRequest, r)
	p.response(21, 204, nil, false)
	p.request(22, protocol.TypeAppAPIRequest, api())
	p.response(22, 200, []byte("saved\x00body"), false)
	for i, tc := range []struct {
		req       any
		typ, code string
	}{
		{protocol.AppAssetRequestPayload{AppID: seed.Records[2].AppID, ReleaseVersion: "1.0.0", Path: "/index.html"}, protocol.TypeAppAssetRequest, "app.unavailable"},
		{protocol.AppAssetRequestPayload{AppID: hostedAppID, ReleaseVersion: "0.9.0", Path: "/index.html"}, protocol.TypeAppAssetRequest, "app.release_changed"},
		{asset("/../data/app.sqlite"), protocol.TypeAppAssetRequest, "app.invalid_request"},
		{asset("/%2e%2e/secret"), protocol.TypeAppAssetRequest, "app.invalid_request"},
		{asset("//127.0.0.1/api/state"), protocol.TypeAppAssetRequest, "app.invalid_request"},
		{protocol.AppAPIRequestPayload{AppID: hostedAppID, ReleaseVersion: "1.0.0", Path: "http://127.0.0.1/api/state", Method: "GET"}, protocol.TypeAppAPIRequest, "app.invalid_request"},
		{protocol.AppAPIRequestPayload{AppID: hostedAppID, ReleaseVersion: "1.0.0", Path: "/api/state", Method: "GET", Headers: map[string]string{"host": "127.0.0.1"}}, protocol.TypeAppAPIRequest, "app.invalid_request"},
		{protocol.AppAPIRequestPayload{AppID: hostedAppID, ReleaseVersion: "1.0.0", Path: "/api/state", Method: "GET", Headers: map[string]string{"authorization": "secret"}}, protocol.TypeAppAPIRequest, "app.invalid_request"},
	} {
		id := uint64(30 + i)
		p.request(id, tc.typ, tc.req)
		e := p.reply(id, protocol.TypeError)
		var failure protocol.ErrorPayload
		if err := json.Unmarshal(e.Payload, &failure); err != nil || failure.Code != tc.code {
			t.Fatal(string(e.Payload), err)
		}
	}
	// Only the hosted capability is negotiated; this connection has no channel.
	p.request(50, protocol.TypeAppAssetRequest, asset("/image.bin"))
	p.response(50, 200, binary, true)
	pub, _ := base64.StdEncoding.DecodeString(pair.ServerStaticPubkey)
	phone, send, recv := dialPairedPhone(t, fr, home, pair.Token, pub, "channel")
	defer phone.Close()
	seal, next := noiseWire(t, phone, send, recv)
	conv := createConversationOverWire(t, seal, next, 500)
	sendAndAwaitEcho(t, home, seal, next, "credit wait", 501, conv, "credit-wait", "still usable", "still usable")
	p.request(51, protocol.TypeAppCancel, protocol.AppCancelPayload{AppID: hostedAppID, RequestID: 50})
	p.reply(51, protocol.TypeAppCancelled)
	sendAndAwaitEcho(t, home, seal, next, "cancelled transfer", 502, conv, "cancelled-transfer", "usable again", "usable again")
	// A partial response is discarded; a new Noise generation reuses its ID.
	p.request(60, protocol.TypeAppAssetRequest, asset("/image.bin"))
	p.response(60, 200, binary, true)
	_ = p.phone.Close()
	p = hostedConnect(t, fr, home, pair.Token, pair.ServerStaticPubkey, true, true)
	p.discovery(2)
	p.request(60, protocol.TypeAppAssetRequest, asset("/main.js"))
	p.response(60, 200, js, false)
	// Commit is proven before disconnect, while the handler withholds all response bytes.
	data := filepath.Join(hostedRoot(home), hostedAppID, "data")
	r = api()
	r.Method = "POST"
	r.Query = "barrier=1"
	r.Body = base64.StdEncoding.EncodeToString([]byte("uncertain committed"))
	p.request(70, protocol.TypeAppAPIRequest, r)
	hostedWait(t, func() bool { _, err := os.Stat(filepath.Join(data, "committed")); return err == nil })
	_ = p.phone.Close()
	hostedWait(t, func() bool { _, err := os.Stat(filepath.Join(data, "response-retired")); return err == nil })
	p = hostedConnect(t, fr, home, pair.Token, pair.ServerStaticPubkey, true, true)
	p.discovery(2)
	p.request(70, protocol.TypeAppAPIRequest, api())
	if err := os.WriteFile(filepath.Join(data, "release-response"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	p.response(70, 200, []byte("uncertain committed"), false)
	hostedWait(t, func() bool { _, err := os.Stat(filepath.Join(data, "old-response-released")); return err == nil })
	count, err := os.ReadFile(filepath.Join(data, "api-invocations"))
	if err != nil || string(count) != "mutation\nmutation\n" {
		t.Fatal("mutation replayed", string(count), err)
	}
	if got := hostedProof(t, home, hostedAppID); got != process {
		t.Fatal("transfer retirement replaced service", got, process)
	}
	if readPersistedServerID(t, home) != seed.ServerID {
		t.Fatal("host identity changed")
	}
	hostedAssertData(t, home, "Resources")
	// A peer without negotiation receives neither app updates nor resource access.
	u := hostedConnect(t, fr, home, pair.Token, pair.ServerStaticPubkey, false, false)
	u.request(80, protocol.TypeAppAssetRequest, asset("/index.html"))
	e := u.reply(80, protocol.TypeError)
	var unsupported protocol.ErrorPayload
	if err := json.Unmarshal(e.Payload, &unsupported); err != nil || unsupported.Code != "protocol.unsupported" {
		t.Fatal(string(e.Payload), err)
	}
	before, _ := hostedRead(t, home)
	if err := syscall.Kill(process.PID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	hostedWait(t, func() bool { after, _ := hostedRead(t, home); return after.Revision > before.Revision })
	if e, ok := u.next(time.Now().Add(100 * time.Millisecond)); ok {
		t.Fatal("unnegotiated lifecycle push", e)
	}
	h.Stop(t)
}
