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
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/identity"
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
	for i := 2; i <= 5; i++ {
		id := fmt.Sprintf("%d2222222-2222-4222-8222-222222222222", i)
		manifest := []byte(fmt.Sprintf(`{"contract_version":1,"server_id":%q,"app_id":%q,"title":%q,"description":"Retained manifest metadata","release_version":"2.0.0","runtime":{"node":">=24.15.0 <25.0.0","sqlite":"node:sqlite"},"frontend":{"root":"dist/web","entry":"index.html"},"service":{"entry":"dist/service/main.js","migrate":"dist/service/migrate.js","health_path":"/_pyry/health","migrations":"sql/migrations"},"build":{"package":"package.json","lockfile":"package-lock.json","install":["npm","ci"],"typecheck":["npm","run","typecheck"],"test":["npm","test"],"compile":["npm","run","build"]}}`, host, id, title+" candidate"))
		build := filepath.Join(hostedRoot(home), id, "builds", strings.Repeat("a", 64))
		if err := os.MkdirAll(filepath.Join(build, "dist/service"), 0700); err != nil {
			t.Fatal(err)
		}
		committed := bytes.ReplaceAll(manifest, []byte(`"release_version":"2.0.0"`), []byte(`"release_version":"1.0.0"`))
		for path, data := range map[string][]byte{"app.json": committed, "dist/service/main.js": []byte("fixture")} {
			if err := os.WriteFile(filepath.Join(build, path), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
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
	var previous hostedSnapshot
	for i, home := range homes {
		hs[i] = StartInWithEnv(t, home, hostedNodeEnv(t), "-pyry-relay=")
		got := hostedRunning(t, home)
		want := before[i]
		want.Records = slices.Clone(want.Records)
		want.Revision = got.Revision
		for j := range want.Records {
			if got.Records[j].Revision < want.Records[j].Revision {
				t.Fatal("revision regressed")
			}
			want.Records[j].Revision = got.Records[j].Revision
		}
		want.Records[0].State, want.Records[3].State = "running", "running"
		want.Records[1].State = "stopped"
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("daemon restart snapshot:\ngot %#v\nwant %#v", got, want)
		}
		hostedAssertData(t, home, titles[i])
		if i == 0 {
			previous = got
		}
	}
	_, other := hostedRead(t, homes[1])
	hs[0].Stop(t)
	hs[0] = StartInWithEnv(t, homes[0], hostedNodeEnv(t), "-pyry-relay=")
	got := hostedRunning(t, homes[0])
	if got.ServerID != previous.ServerID || got.Revision <= previous.Revision {
		t.Fatal("restart identity/revision", got, previous)
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

	nodeEnv := hostedNodeEnv(t)
	h := StartStreamInteractiveWithRelay(t, home, initial, payload.Relay, nodeEnv...)
	hostedRunning(t, home)
	proof := hostedProof(t, home, hostedAppID)
	_, baseline := hostedRead(t, home)
	pub, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	phone, send, recv := dialPairedPhone(t, fr, home, payload.Token, pub, "phone")
	seal, next := noiseWire(t, phone, send, recv)
	conv := createConversationOverWire(t, seal, next, 31389)
	sendAndAwaitEcho(t, home, seal, next, "initial turn", 31390, conv, "app-before-clear", "before clear", "before clear")
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
		t.Fatalf("channel clear/stop changed apps:\nbefore %s\nafter %s", baseline, after)
	}
	hostedAssertData(t, home, "Channel independent")
	_ = phone.Close()
	if !fr.ForceCloseBinary(readPersistedServerID(t, home)) {
		t.Fatal("relay disconnect failed")
	}
	waitBinaryHello(t, fr, readPersistedServerID(t, home))
	hostedHealthy(t, proof)
	h.Stop(t)
	hostedGone(t, proof)
	// Idle eviction leaves the independent app owner running.
	hostedDisableRelay(t, home)
	h = StartInWithEnv(t, home, nodeEnv, "-pyry-relay=", "-pyry-idle-timeout=100ms")
	hostedRunning(t, home)
	_, baseline = hostedRead(t, home)
	proof = hostedProof(t, home, hostedAppID)
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
	hostedHealthy(t, proof)
	h.Stop(t)
	hostedGone(t, proof)
}

func TestHostedAppsProductionReadiness(t *testing.T) {
	for _, mode := range []string{"empty", "stopped", "unhealthy", "missing", "unsupported", "storage"} {
		t.Run(mode, func(t *testing.T) {
			home := shortHome(t)
			fr := fakerelay.New(relayTestLogger())
			t.Cleanup(func() { _ = fr.Close() })
			pair, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: fr.URL() + "/v2/server", DeviceName: "phone"})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "empty" {
				seed := hostedSeed(t, home, "Readiness")
				if mode == "stopped" {
					r, err := apps.Open(hostedRoot(home), seed.ServerID)
					if err != nil {
						t.Fatal(err)
					}
					for _, record := range seed.Records {
						if _, err := r.UpdateLifecycle(seed.ServerID, record.AppID, apps.Lifecycle{Title: record.Title, Desired: "stopped", State: "stopped", ActiveRelease: record.ActiveRelease}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if mode == "unhealthy" {
					if err := os.WriteFile(filepath.Join(hostedRoot(home), hostedAppID, "builds", strings.Repeat("a", 64), "dist/service/main.js"), []byte("bad-health"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "storage" {
					if err := os.WriteFile(filepath.Join(hostedRoot(home), "registry.json"), []byte("invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			env := hostedNodeEnv(t)
			node := strings.TrimPrefix(strings.Split(env[0], ":")[0], "PATH=") + "/node"
			if mode == "missing" {
				env[0] = "PATH=" + filepath.Dir(node)
				if err := os.Remove(node); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unsupported" {
				if err := os.WriteFile(node, []byte("#!/bin/sh\necho v25.0.0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			env = append(env, "PYRY_ALLOW_INSECURE_RELAY=1")
			h := StartInWithEnv(t, home, env, "-pyry-relay="+pair.Relay)
			waitBinaryHello(t, fr, readPersistedServerID(t, home))
			ready := mode == "empty" || mode == "stopped" || mode == "unhealthy"
			p := hostedConnect(t, fr, home, pair.Token, pair.ServerStaticPubkey, true, ready)
			p.request(2, protocol.TypeListApps, protocol.ListAppsPayload{})
			if ready {
				e := p.reply(2, protocol.TypeApps)
				var list protocol.AppsPayload
				if err := json.Unmarshal(e.Payload, &list); err != nil || (mode == "empty" && len(list.Items) != 0) || (mode != "empty" && len(list.Items) != 4) {
					t.Fatal(string(e.Payload), err)
				}
				if mode != "empty" {
					p.request(3, protocol.TypeAppAssetRequest, protocol.AppAssetRequestPayload{AppID: hostedAppID, ReleaseVersion: "1.0.0", Path: "/index.html"})
					e = p.reply(3, protocol.TypeError)
					var failure protocol.ErrorPayload
					if err := json.Unmarshal(e.Payload, &failure); err != nil || failure.Code != "app.unavailable" {
						t.Fatal(string(e.Payload), err)
					}
				}
			} else {
				e := p.reply(2, protocol.TypeError)
				var failure protocol.ErrorPayload
				if err := json.Unmarshal(e.Payload, &failure); err != nil || failure.Code != "protocol.unsupported" {
					t.Fatal(string(e.Payload), err)
				}
			}
			if result := h.Run(t, "status"); result.ExitCode != 0 {
				t.Fatal(string(result.Stderr))
			}
			h.Stop(t)
		})
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
