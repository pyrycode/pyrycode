//go:build e2e_liverelay

package liverelay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/pair"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const hostedAppID = "22222222-2222-4222-8222-222222222222"

func hostedJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hostedWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func hostedWait(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("hosted observation deadline")
}

// All registry and release preparation precedes daemon ownership.
func hostedSeed(t *testing.T, home, server string) (build, data string) {
	t.Helper()
	root := filepath.Join(home, ".pyry/test/apps")
	build = filepath.Join(root, hostedAppID, "builds", strings.Repeat("a", 64))
	data = filepath.Join(root, hostedAppID, "data")
	manifest := []byte(fmt.Sprintf(`{"contract_version":1,"server_id":%q,"app_id":%q,"title":"Relay proof","release_version":"1.0.0","runtime":{"node":">=24.15.0 <25.0.0","sqlite":"node:sqlite"},"frontend":{"root":"dist/web","entry":"index.html"},"service":{"entry":"dist/service/main.js","migrate":"dist/service/migrate.js","health_path":"/_pyry/health","migrations":"sql/migrations"},"build":{"package":"package.json","lockfile":"package-lock.json","install":["npm","ci"],"typecheck":["npm","run","typecheck"],"test":["npm","test"],"compile":["npm","run","build"]}}`, server, hostedAppID))
	hostedWrite(t, filepath.Join(build, "app.json"), manifest)
	hostedWrite(t, filepath.Join(build, "dist/service/main.js"), []byte("fixture"))
	hostedWrite(t, filepath.Join(data, "app.sqlite"), nil)
	r, err := apps.Open(root, server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpdateLifecycle(server, hostedAppID, apps.Lifecycle{Title: "Relay proof", Desired: "available", State: "starting", ActiveRelease: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	return build, data
}

func hostedHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func hostedRunning(t *testing.T, home string, d *daemonProc) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon diagnostics:\n%s", d.stderr.String())
		}
	})
	hostedWait(t, func() bool {
		b, err := os.ReadFile(filepath.Join(home, ".pyry/test/apps/registry.json"))
		var s struct {
			Records []apps.Record `json:"records"`
		}
		return err == nil && json.Unmarshal(b, &s) == nil && len(s.Records) == 1 && s.Records[0].State == "running"
	})
}

type hostedWire struct {
	t          *testing.T
	phone      *fakephone.Client
	send, recv *noise.CipherState
	frames     chan struct {
		raw []byte
		err error
	}
}

func hostedConnect(t *testing.T, r *relayProc, home string, pair pair.Payload) *hostedWire {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, r.baseWS, readPersistedServerID(t, home), pair.Token, "hosted")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	pub, err := base64.StdEncoding.DecodeString(pair.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	init, err := noise.NewInitiator(fakephone.InstallKey(pair.Token), pub)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(buildHello(t, pair.Token, protocol.CapabilityHostedAppsV1))
	if err != nil {
		t.Fatal(err)
	}
	sendNoiseInit(t, phone, msg)
	frame := readInnerFrame(t, phone, 3*time.Second)
	if frame.Type != protocol.TypeNoiseResp {
		t.Fatal("missing Noise response")
	}
	raw, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	ack, send, recv, err := init.ReadResp(raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope protocol.Envelope
	var hello protocol.HelloAckPayload
	if json.Unmarshal(ack, &envelope) != nil || envelope.Type != protocol.TypeHelloAck || json.Unmarshal(envelope.Payload, &hello) != nil || !slices.Contains(hello.Capabilities, protocol.CapabilityHostedAppsV1) {
		t.Fatal("hosted capability not acknowledged")
	}
	p := &hostedWire{t: t, phone: phone, send: send, recv: recv, frames: make(chan struct {
		raw []byte
		err error
	}, 8)}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			raw, err := phone.ReceiveBytes(time.Minute)
			select {
			case p.frames <- struct {
				raw []byte
				err error
			}{raw, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop); _ = phone.Close(); <-done })
	p.request(2, protocol.TypeListApps, protocol.ListAppsPayload{})
	e := p.reply(2, protocol.TypeApps)
	var list protocol.AppsPayload
	if json.Unmarshal(e.Payload, &list) != nil || len(list.Items) != 1 || list.Items[0].AppID != hostedAppID || list.Items[0].ActiveRelease == nil || *list.Items[0].ActiveRelease != "1.0.0" {
		t.Fatal("discovery", string(e.Payload))
	}
	return p
}

func (p *hostedWire) request(id uint64, typ string, value any) {
	p.t.Helper()
	b, err := p.send.Encrypt(hostedJSON(p.t, protocol.Envelope{ID: id, Type: typ, TS: time.Now().UTC(), Payload: hostedJSON(p.t, value)}))
	if err != nil {
		p.t.Fatal(err)
	}
	sendNoiseMsg(p.t, p.phone, b)
}

func (p *hostedWire) next(wait time.Duration) (protocol.Envelope, bool) {
	p.t.Helper()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		select {
		case f := <-p.frames:
			if f.err != nil {
				p.t.Fatal(f.err)
			}
			var inner protocol.InnerFrameV2
			if json.Unmarshal(f.raw, &inner) != nil || inner.Type != protocol.TypeNoiseMsg {
				p.t.Fatal("unencrypted resource frame")
			}
			cipher, err := base64.StdEncoding.DecodeString(inner.Data)
			if err != nil {
				p.t.Fatal(err)
			}
			plain, err := p.recv.Decrypt(cipher)
			if err != nil {
				p.t.Fatal(err)
			}
			var env protocol.Envelope
			var keys map[string]json.RawMessage
			if len(plain) > 65519 || json.Unmarshal(plain, &env) != nil || json.Unmarshal(plain, &keys) != nil {
				p.t.Fatal("invalid envelope")
			}
			for _, key := range []string{"conversation_id", "session_id", "event_id", "history_entry_id", "session_state_cleared", "payload_encrypted"} {
				if _, ok := keys[key]; ok {
					p.t.Fatal("conversation metadata key", key)
				}
			}
			if env.Type == protocol.TypeAppUpdated {
				continue
			}
			return env, true
		case <-deadline.C:
			return protocol.Envelope{}, false
		}
	}
}

func (p *hostedWire) quiet() {
	p.t.Helper()
	if e, ok := p.next(100 * time.Millisecond); ok {
		p.t.Fatalf("unexpected unsolicited frame: %s reply=%v", e.Type, e.InReplyTo)
	}
}

func (p *hostedWire) reply(id uint64, typ string) protocol.Envelope {
	p.t.Helper()
	e, ok := p.next(5 * time.Second)
	if !ok || e.Type != typ || e.InReplyTo == nil || *e.InReplyTo != id {
		p.t.Fatalf("expected %s reply to %d, got %+v %s", typ, id, e, e.Payload)
	}
	return e
}

func (p *hostedWire) response(id uint64, status int, want []byte, partial bool) {
	p.t.Helper()
	e := p.reply(id, protocol.TypeAppResponse)
	var meta protocol.AppResponsePayload
	if json.Unmarshal(e.Payload, &meta) != nil || meta.AppID != hostedAppID || meta.ReleaseVersion != "1.0.0" || meta.Status != status || meta.SizeBytes != uint64(len(want)) || meta.TotalChunks != uint64((len(want)+32767)/32768) || meta.SHA256 != fmt.Sprintf("%x", sha256.Sum256(want)) || meta.Headers["content-type"] == "" {
		p.t.Fatal("resource metadata", string(e.Payload))
	}
	for name := range meta.Headers {
		if !slices.Contains([]string{"content-type", "cache-control", "etag", "last-modified"}, name) {
			p.t.Fatal("forbidden header", name)
		}
	}
	var body []byte
	for i := uint64(0); i < meta.TotalChunks; i++ {
		if i > 0 {
			p.quiet()
			p.request(9000+i, protocol.TypeAppResponseCredit, protocol.AppResponseCreditPayload{AppID: hostedAppID, RequestID: id, NextIndex: i})
		}
		e = p.reply(id, protocol.TypeAppResponseChunk)
		var chunk protocol.AppResponseChunkPayload
		if json.Unmarshal(e.Payload, &chunk) != nil {
			p.t.Fatal("chunk JSON")
		}
		b, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil || chunk.AppID != hostedAppID || chunk.Index != i || len(b) == 0 || len(b) > 32768 {
			p.t.Fatal("chunk identity/order/size", err)
		}
		body = append(body, b...)
		if partial {
			if !bytes.Equal(body, want[:len(body)]) {
				p.t.Fatal("partial bytes")
			}
			p.quiet()
			return
		}
	}
	if !bytes.Equal(body, want) || fmt.Sprintf("%x", sha256.Sum256(body)) != meta.SHA256 {
		p.t.Fatal("resource bytes/digest")
	}
	p.quiet()
}

func hostedPair(t *testing.T, home string, relay *relayProc) pair.Payload {
	t.Helper()
	pair, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relay.baseWS, DeviceName: "hosted"})
	if err != nil {
		t.Fatal(err)
	}
	return pair
}
