//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const hostedAppID = "22222222-2222-4222-8222-222222222222"

type hostedSnapshot struct {
	ServerID string        `json:"server_id"`
	Revision uint64        `json:"revision"`
	Records  []apps.Record `json:"records"`
}

func hostedRoot(home string) string { return filepath.Join(home, ".pyry", "test", "apps") }
func hostedRead(t *testing.T, home string) (hostedSnapshot, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(hostedRoot(home), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s hostedSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s, b
}
func hostedSeed(t *testing.T, home, title string) hostedSnapshot {
	t.Helper()
	host, err := identity.LoadOrCreate(filepath.Join(home, ".pyry", "test", "server-id"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := apps.Open(hostedRoot(home), string(host))
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 4; i++ {
		id := fmt.Sprintf("%d2222222-2222-4222-8222-222222222222", i)
		manifest := []byte(fmt.Sprintf(`{"contract_version":1,"server_id":%q,"app_id":%q,"title":%q,"description":"Retained manifest metadata","release_version":"2.0.0","runtime":{"node":">=24.15.0 <25.0.0","sqlite":"node:sqlite"},"frontend":{"root":"dist/web","entry":"index.html"},"service":{"entry":"dist/service/main.js","migrate":"dist/service/migrate.js","health_path":"/_pyry/health","migrations":"sql/migrations"},"build":{"package":"package.json","lockfile":"package-lock.json","install":["npm","ci"],"typecheck":["npm","run","typecheck"],"test":["npm","test"],"compile":["npm","run","build"]}}`, host, id, title+" candidate"))
		if _, err := r.Register(manifest); err != nil {
			t.Fatal(err)
		}
		state := apps.Lifecycle{Title: title, Desired: "available", State: "running", ActiveRelease: "1.0.0", PendingRelease: "2.0.0"}
		if i == 3 {
			state.State = "failed"
			state.ActiveRelease = ""
			state.LastError = &apps.LastError{Code: "app.io_failed", Message: "safe failure"}
		}
		if i == 4 {
			state.Desired = "stopped"
			state.State = "stopped"
		}
		if _, err := r.UpdateLifecycle(string(host), id, state); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(hostedRoot(home), hostedAppID, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "app.sqlite"), []byte(title+" saved preferences\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	s, _ := hostedRead(t, home)
	return s
}
func hostedAssertData(t *testing.T, home, title string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(hostedRoot(home), hostedAppID, "data", "app.sqlite"))
	if err != nil || !bytes.Equal(b, []byte(title+" saved preferences\x00")) {
		t.Fatal("saved data changed", err)
	}
}
func TestHostedAppsRestartIsolation(t *testing.T) {
	homes := []string{shortHome(t), shortHome(t)}
	titles := []string{"Host A", "Host B"}
	// First establish stopped real daemons, then transfer exclusive ownership to
	// seeding registries and back. Never Open merely to inspect daemon evidence.
	for _, home := range homes {
		hostedDisableRelay(t, home)
		h := StartInWithEnv(t, home, nil, "-pyry-relay=")
		h.Stop(t)
	}
	before := []hostedSnapshot{hostedSeed(t, homes[0], titles[0]), hostedSeed(t, homes[1], titles[1])}
	if before[0].ServerID == before[1].ServerID {
		t.Fatal("host identities alias")
	}
	hs := make([]*Harness, 2)
	var normalized []byte
	for i, home := range homes {
		hs[i] = StartInWithEnv(t, home, nil, "-pyry-relay=")
		got, raw := hostedRead(t, home)
		want := before[i]
		want.Records = slices.Clone(want.Records)
		want.Revision = 8
		want.Records[0].State, want.Records[0].Revision = "starting", 7
		want.Records[1].State, want.Records[1].Revision = "stopped", 8
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("daemon restart snapshot:\ngot %#v\nwant %#v", got, want)
		}
		hostedAssertData(t, home, titles[i])
		if i == 0 {
			normalized = raw
		}
	}
	_, other := hostedRead(t, homes[1])
	hs[0].Stop(t)
	hs[0] = StartInWithEnv(t, homes[0], nil, "-pyry-relay=")
	_, unchanged := hostedRead(t, homes[0])
	if !bytes.Equal(unchanged, normalized) {
		t.Fatal("unchanged restart added revisions or changed metadata")
	}
	_, otherAfter := hostedRead(t, homes[1])
	if !bytes.Equal(other, otherAfter) {
		t.Fatal("restarting host A changed host B")
	}
	for i, home := range homes {
		hostedAssertData(t, home, titles[i])
		hs[i].Stop(t)
	}
}

func TestHostedAppsChannelIndependence(t *testing.T) {
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	payload, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: fr.URL() + "/v2/server", DeviceName: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	hostedSeed(t, home, "Channel independent")
	const initial = "11111111-1111-4111-8111-111111111111"

	h := StartStreamInteractiveWithRelay(t, home, initial, payload.Relay)
	_, baseline := hostedRead(t, home)
	pub, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	phone, send, recv := dialPairedPhone(t, fr, home, payload.Token, pub, "phone")
	seal, next := noiseWire(t, phone, send, recv)
	conv := createConversationOverWire(t, seal, next, 31389)
	sendAndAwaitEcho(t, seal, next, "initial turn", 31390, conv, "app-before-clear", "before clear", "before clear")
	pre, ok := readEntryByLabel(filepath.Join(home, ".pyry", "test", "sessions.json"), conv)
	if !ok {
		t.Fatal("channel session missing")
	}
	seal(protocol.Envelope{ID: 31391, Type: protocol.TypeSendMessage, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: conv, MessageID: "app-clear", Text: "/clear"})})
	fresh := awaitConversationTransition(t, next, transitionWant{milestone: "clear", convID: conv, previous: pre.ID, because: "typed clear preserves apps", diag: func() string { return mustReadFile(t, filepath.Join(home, ".pyry", "test", "sessions.json")) }})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := control.SessionsRm(ctx, h.SocketPath, fresh, control.JSONLPolicyLeave); err != nil {
		t.Fatal(err)
	}
	_, after := hostedRead(t, home)
	if !bytes.Equal(baseline, after) {
		t.Fatal("channel clear/stop changed apps")
	}
	hostedAssertData(t, home, "Channel independent")
	_ = phone.Close()
	h.Stop(t)
	// Idle eviction has no app owner or discovery detach path.
	hostedDisableRelay(t, home)
	h = StartInWithEnv(t, home, nil, "-pyry-relay=", "-pyry-idle-timeout=100ms")
	entry, ok := readBootstrapIfPresent(filepath.Join(home, ".pyry", "test", "sessions.json"))
	if !ok {
		t.Fatal("bootstrap missing")
	}
	waitForSessionState(t, filepath.Join(home, ".pyry", "test", "sessions.json"), entry.ID, "evicted", 5*time.Second)
	_, after = hostedRead(t, home)
	if !bytes.Equal(baseline, after) {
		t.Fatal("idle eviction changed apps")
	}
	hostedAssertData(t, home, "Channel independent")
	h.Stop(t)
}

func TestHostedAppsProductionUnsupported(t *testing.T) {
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	payload, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: fr.URL() + "/v2/server", DeviceName: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	hostedSeed(t, home, "Not supervised")
	h := StartInWithEnv(t, home, []string{"PYRY_ALLOW_INSECURE_RELAY=1"}, "-pyry-relay="+payload.Relay)
	t.Cleanup(func() { h.Stop(t) })
	waitBinaryHello(t, fr, readPersistedServerID(t, home))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), readPersistedServerID(t, home), payload.Token, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	pub, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	init, err := noise.NewInitiator(fakephone.InstallKey(payload.Token), pub)
	if err != nil {
		t.Fatal(err)
	}
	hello := protocol.HelloClientPayload{Role: "client", DeviceName: "phone", ClientVersion: "test", ProtocolVersions: []string{"v2"}, Token: payload.Token, Capabilities: []string{protocol.CapabilityHostedAppsV1}}
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
	var ackPayload protocol.HelloAckPayload
	if err := json.Unmarshal(ack, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Payload, &ackPayload); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ackPayload.Capabilities, protocol.CapabilityHostedAppsV1) {
		t.Fatal("registration activated hosting")
	}
	seal, next := noiseWire(t, phone, send, recv)
	seal(protocol.Envelope{ID: 2, Type: protocol.TypeListApps, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	var ok bool
	env, ok = next(time.Now().Add(3 * time.Second))
	if !ok {
		t.Fatal("missing unsupported reply")
	}
	var p protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if env.Type != protocol.TypeError || p.Code != "protocol.unsupported" || env.InReplyTo == nil || *env.InReplyTo != 2 {
		t.Fatal(env, p)
	}
}

func hostedDisableRelay(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".pyry")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"relay_url":""}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHostedAppsStorageDegraded(t *testing.T) {
	for _, mode := range []string{"corrupt", "foreign", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			home := shortHome(t)
			hostedDisableRelay(t, home)
			root := hostedRoot(home)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "registry.json")
			switch mode {
			case "corrupt":
				if err := os.WriteFile(path, []byte("ZZ3139PRIVATE-STORAGEZZ"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				hostedSeed(t, home, "Foreign metadata")
				if err := os.WriteFile(filepath.Join(home, ".pyry", "test", "server-id"), []byte(string(identity.NewServerID())+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			h := StartInWithEnv(t, home, nil, "-pyry-relay=")
			result := h.Run(t, "status")
			if result.ExitCode != 0 {
				t.Fatal(string(result.Stderr))
			}
			result = runVerbIn(t, h.SocketPath, home, home, "channel", "new")
			if result.ExitCode != 0 {
				t.Fatal(string(result.Stderr))
			}
			h.Stop(t)
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected storage overwritten")
			}
			if !bytes.Contains(h.Stderr.Bytes(), []byte("apps: unavailable")) || bytes.Contains(h.Stderr.Bytes(), []byte("ZZ3139PRIVATE-STORAGEZZ")) || bytes.Contains(h.Stderr.Bytes(), []byte("Foreign metadata")) {
				t.Fatal("missing or unsafe storage diagnostic")
			}
			if mode == "foreign" {
				hostedAssertData(t, home, "Foreign metadata")
			}
		})
	}
}
