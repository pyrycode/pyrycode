package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

const daemonAppID = "22222222-2222-4222-8222-222222222222"

func daemonAppManifest(host string) []byte {
	return []byte(fmt.Sprintf(`{"contract_version":1,"server_id":%q,"app_id":%q,"title":"Candidate","release_version":"1.0.0","runtime":{"node":">=24.15.0 <25.0.0","sqlite":"node:sqlite"},"frontend":{"root":"dist/web","entry":"index.html"},"service":{"entry":"dist/service/main.js","migrate":"dist/service/migrate.js","health_path":"/_pyry/health","migrations":"sql/migrations"},"build":{"package":"package.json","lockfile":"package-lock.json","install":["npm","ci"],"typecheck":["npm","run","typecheck"],"test":["npm","test"],"compile":["npm","run","build"]}}`, host, daemonAppID))
}
func TestDaemonAppsStorage(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "foreign", "unreadable", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", testRealTempDir(t))
			const name = "test/instance"
			root := filepath.Join(resolveInstanceDirPath(name), "apps")
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			host, err := identity.LoadOrCreate(resolveServerIDPath(name))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "registry.json")
			switch mode {
			case "corrupt":
				if err := os.WriteFile(path, []byte("ZZ3139SECRET-STORAGEZZ"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				foreign := string(identity.NewServerID())
				r, err := apps.Open(root, foreign)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.Register(daemonAppManifest(foreign)); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			var logs bytes.Buffer
			r := openDaemonApps(name, slog.New(slog.NewTextHandler(&logs, nil)))
			if mode != "missing" {
				if r != nil {
					t.Fatal("rejected storage exposed")
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("rejected bytes changed")
				}
				if !strings.Contains(logs.String(), "apps: unavailable") || strings.Contains(logs.String(), "ZZ3139SECRET-STORAGEZZ") || strings.Contains(logs.String(), root) {
					t.Fatalf("unsafe/missing reason: %s", logs.String())
				}
				return
			}
			if r == nil {
				t.Fatal(logs.String())
			}
			if records, rev := r.List(); len(records) != 0 || rev != 0 {
				t.Fatal(records, rev)
			}
			if _, err := r.Register(daemonAppManifest(string(host))); err != nil {
				t.Fatal(err)
			}
			for p, want := range map[string]os.FileMode{root: 0700, path: 0600} {
				info, err := os.Stat(p)
				if err != nil || info.Mode().Perm() != want {
					t.Fatalf("mode %s: %v %v", p, info, err)
				}
			}
			if !filepath.IsAbs(root) || filepath.Base(filepath.Dir(root)) != sanitizeName(name) {
				t.Fatal(root)
			}
		})
	}
}

func TestDaemonAppsComposition(t *testing.T) {
	for _, mode := range []string{"supported", "owner", "delivery", "runtime", "registry"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ready := mode == "supported"
			host := string(identity.NewServerID())
			r, err := apps.Open(filepath.Join(testRealTempDir(t), "apps"), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Register(daemonAppManifest(host)); err != nil {
				t.Fatal(err)
			}
			if _, err = r.UpdateLifecycle(host, daemonAppID, apps.Lifecycle{Title: "Candidate", Desired: "stopped", State: "stopped"}); err != nil {
				t.Fatal(err)
			}
			_, initialRevision := r.List()
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			paired := &devices.Registry{}
			paired.Add(devices.Device{TokenHash: devices.HashToken("apps-token"), Name: "phone", PairedAt: time.Now().UTC()})
			w := &switchWire{t: t, frames: make(chan protocol.RoutingEnvelope, 16), out: make(chan protocol.RoutingEnvelope, 128), phones: map[string]switchPhone{}, pub: key.PublicKey().Bytes()}
			cfg := relay.V2SessionConfig{Frames: w.frames, Outbound: func(e protocol.RoutingEnvelope) error { w.out <- e; return nil }, StaticPriv: key.Bytes(), Devices: paired, ServerID: host, Logger: discardLogger()}
			cfg.OutboundContext = func(ctx context.Context, e protocol.RoutingEnvelope) error {
				select {
				case w.out <- e:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			var owner *apps.Owner
			var node string
			if mode != "owner" {
				node = filepath.Join(t.TempDir(), "node")
				version := "v24.15.0"
				if mode == "runtime" {
					version = "v25.0.0"
				}
				testWriteDaemonAppsRuntime(t, node, "#!/bin/sh\necho "+version+"\n")
				owner = newDaemonAppsOwner(r, node, discardLogger())
				t.Cleanup(func() { _ = owner.Shutdown() })
			}
			if mode == "delivery" {
				cfg.OutboundContext = nil
			}
			registry := r
			if mode == "registry" {
				registry = nil
			}
			cfg.HostedAppsReady = func() bool { return true }
			mgr, detach, err := newDaemonAppsManager(context.Background(), cfg, registry, owner)
			if err != nil {
				t.Fatal(err)
			}
			if ready {
				testWriteDaemonAppsRuntime(t, node, "#!/bin/sh\nexit 1\n")
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); _ = mgr.Run(ctx) }()
			t.Cleanup(func() {
				detach()
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("manager did not drain")
				}
			})
			phoneKey, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			init, err := noise.NewInitiator(phoneKey.Bytes(), w.pub)
			if err != nil {
				t.Fatal(err)
			}
			marshal := func(v any) []byte {
				t.Helper()
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			hello := protocol.HelloClientPayload{Role: "client", DeviceName: "phone", ClientVersion: "test", ProtocolVersions: []string{"v2"}, Token: "apps-token", Capabilities: []string{protocol.CapabilityHostedAppsV1}}
			data, err := init.WriteInit(marshal(protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: marshal(hello)}))
			if err != nil {
				t.Fatal(err)
			}
			w.frames <- w.wrap("apps", protocol.TypeNoiseInit, data)
			ack, send, recv, err := init.ReadResp(w.data(w.await()))
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
				t.Fatal(payload)
			}
			w.phones["apps"] = switchPhone{send, recv}
			if mode == "delivery" {
				return
			}
			request := func(id uint64) {
				t.Helper()
				sealed, err := send.Encrypt(marshal(protocol.Envelope{ID: id, Type: protocol.TypeListApps, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}))
				if err != nil {
					t.Fatal(err)
				}
				w.frames <- w.wrap("apps", protocol.TypeNoiseMsg, sealed)
			}
			request(2)
			_, env = w.read()
			if !ready {
				var p protocol.ErrorPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if env.Type != protocol.TypeError || p.Code != "protocol.unsupported" {
					t.Fatal(env)
				}
				return
			}
			var list protocol.AppsPayload
			if err := json.Unmarshal(env.Payload, &list); err != nil {
				t.Fatal(err)
			}
			if env.Type != protocol.TypeApps || list.Revision != initialRevision || len(list.Items) != 1 || list.Items[0].AppID != daemonAppID {
				t.Fatal(env, list)
			}
			state := apps.Lifecycle{Title: "Committed", Desired: "stopped", State: "stopped", ActiveRelease: "1.0.0"}
			if _, err := r.UpdateLifecycle(host, daemonAppID, state); err != nil {
				t.Fatal(err)
			}
			_, env = w.read()
			var update protocol.HostedAppRecord
			if err := json.Unmarshal(env.Payload, &update); err != nil {
				t.Fatal(err)
			}
			if env.Type != protocol.TypeAppUpdated || env.InReplyTo != nil || update.Title != "Committed" || update.State != "stopped" || update.Revision != initialRevision+1 {
				t.Fatal(env, update)
			}
			otherID := "33333333-3333-4333-8333-333333333333"
			if _, err := r.Register(bytes.ReplaceAll(daemonAppManifest(host), []byte(daemonAppID), []byte(otherID))); err != nil {
				t.Fatal(err)
			}
			_, env = w.read()
			if env.Type != protocol.TypeAppUpdated {
				t.Fatal(env)
			}
			if _, err := r.Remove(otherID); err != nil {
				t.Fatal(err)
			}
			_, env = w.read()
			for env.Type == protocol.TypeAppUpdated {
				_, env = w.read()
			}
			var removed protocol.AppRemovedPayload
			if err := json.Unmarshal(env.Payload, &removed); err != nil {
				t.Fatal(err)
			}
			_, removedRevision := r.List()
			if env.Type != protocol.TypeAppRemoved || env.InReplyTo != nil || removed.AppID != otherID || removed.Revision != removedRevision {
				t.Fatal(env, removed)
			}
			detach()
			detach()
			state.Title = "Detached"
			if _, err := r.UpdateLifecycle(host, daemonAppID, state); err != nil {
				t.Fatal(err)
			}
			request(3)
			_, env = w.read()
			if env.Type != protocol.TypeApps {
				t.Fatalf("detached consumer emitted %s", env.Type)
			}
			if err := json.Unmarshal(env.Payload, &list); err != nil {
				t.Fatal(err)
			}
			if list.Revision != removedRevision+1 || list.Items[0].Title != "Detached" {
				t.Fatal(list)
			}
		})
	}
}
func TestDaemonAppsCompositionFailure(t *testing.T) {
	r, err := apps.Open(filepath.Join(testRealTempDir(t), "apps"), string(identity.NewServerID()))
	if err != nil {
		t.Fatal(err)
	}
	mgr, detach, err := newDaemonAppsManager(context.Background(), relay.V2SessionConfig{Frames: make(chan protocol.RoutingEnvelope), Logger: discardLogger()}, r, nil)
	if err == nil || mgr != nil || detach != nil {
		t.Fatal("invalid manager construction succeeded")
	}
}
