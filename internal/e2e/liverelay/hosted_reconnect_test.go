//go:build e2e_liverelay

package liverelay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func hostedExists(path string) bool { _, err := os.Stat(path); return err == nil }

// TestHostedResourceProcess is the race-instrumented backend child, not an acceptance arm.
func TestHostedResourceProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "hosted-resource-child" {
		return
	}
	data := os.Getenv("PYRY_APP_DATA_DIR")
	mark := func(name string, value []byte) {
		if os.WriteFile(filepath.Join(data, name), value, 0600) != nil {
			os.Exit(2)
		}
	}
	mark("pid", []byte(fmt.Sprint(os.Getpid())))
	var mu sync.Mutex
	stop := make(chan struct{})
	barrier := func(r *http.Request, ready, release string) bool {
		mark(ready, []byte("yes"))
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		done := r.Context().Done()
		for {
			select {
			case <-stop:
				return false
			case <-timer.C:
				return false
			case <-done:
				mark("response-retired", []byte("yes"))
				done = nil
			case <-time.After(5 * time.Millisecond):
				if hostedExists(filepath.Join(data, release)) {
					return true
				}
			}
		}
	}
	server := &http.Server{Addr: "127.0.0.1:" + os.Getenv("PYRY_APP_PORT"), ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_pyry/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "app_id": os.Getenv("PYRY_APP_ID"), "release_version": os.Getenv("PYRY_APP_RELEASE")})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Set-Cookie", "private=forbidden")
		w.Header().Set("Location", "http://forbidden.invalid")
		if r.Method == "POST" {
			b, err := io.ReadAll(io.LimitReader(r.Body, 32769))
			if err != nil {
				w.WriteHeader(500)
				return
			}
			mu.Lock()
			mark("state", b)
			f, err := os.OpenFile(filepath.Join(data, "invocations"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err == nil {
				_, err = f.WriteString("write\n")
				_ = f.Close()
			}
			mu.Unlock()
			if err != nil {
				w.WriteHeader(500)
				return
			}
			if r.URL.RawQuery == "barrier=1" {
				if !barrier(r, "committed", "release-old") {
					return
				}
				w.WriteHeader(204)
				mark("old-released", []byte("yes"))
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.RawQuery == "barrier=read" && !barrier(r, "fresh-active", "release-fresh") {
			return
		}
		mu.Lock()
		b, err := os.ReadFile(filepath.Join(data, "state"))
		mu.Unlock()
		if err != nil {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(b)
	})
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		os.Exit(3)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		<-sig
		close(stop)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			os.Exit(4)
		}
		close(done)
	}()
	_ = server.Serve(listener)
	<-done
	os.Exit(0)
}

func hostedLauncher(t *testing.T, command string) []string {
	t.Helper()
	dir := t.TempDir()
	node := filepath.Join(dir, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\n"+command+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "GORACE=atexit_sleep_ms=0"}
}

func hostedQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func hostedAPI() protocol.AppAPIRequestPayload {
	return protocol.AppAPIRequestPayload{AppID: hostedAppID, ReleaseVersion: "1.0.0", Path: "/api/state", Method: "GET"}
}

func TestLiveRelay_HostedResources(t *testing.T) {
	r := startRelay(t, ensureRelayBuilt(t))
	home := hostedHome(t)
	pair := hostedPair(t, home, r)
	server := readPersistedServerID(t, home)
	build, data := hostedSeed(t, home, server)
	binary := bytes.Repeat([]byte{0, 255, 3, 128, 7}, 14000)
	hostedWrite(t, filepath.Join(build, "dist/web/image.bin"), binary)
	saved := []byte("retained database\x00")
	hostedWrite(t, filepath.Join(data, "app.sqlite"), saved)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := hostedLauncher(t, "if [ \"$1\" = --version ]; then echo v24.15.0; else exec "+hostedQuote(bin)+" -test.run='^TestHostedResourceProcess$' -- hosted-resource-child; fi")
	d := spawnDaemon(t, home, r.baseWS, env...)
	hostedRunning(t, home, d)
	r.waitBinaryRegistered(t, 10*time.Second)
	pid, err := os.ReadFile(filepath.Join(data, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	p := hostedConnect(t, r, home, pair)
	asset := protocol.AppAssetRequestPayload{AppID: hostedAppID, ReleaseVersion: "1.0.0", Path: "/image.bin"}
	p.request(10, protocol.TypeAppAssetRequest, asset)
	p.response(10, 200, binary, false)
	req := hostedAPI()
	req.Method = "POST"
	req.Body = base64.StdEncoding.EncodeToString([]byte("saved\x00body"))
	req.Headers = map[string]string{"content-type": "application/octet-stream"}
	p.request(11, protocol.TypeAppAPIRequest, req)
	p.response(11, 204, nil, false)
	p.request(12, protocol.TypeAppAPIRequest, hostedAPI())
	p.response(12, 200, []byte("saved\x00body"), false)
	exits := func(n int) {
		deadline := time.Now().Add(3 * time.Second)
		hostedWait(t, func() bool {
			if time.Now().After(deadline) {
				t.Fatal("resource worker did not exit before the five-second inactivity timeout")
			}
			return strings.Count(d.stderr.String(), "event=v2.hosted.resource_exited") == n
		})
	}
	exits(3)
	p.request(60, protocol.TypeAppAssetRequest, asset)
	p.response(60, 200, binary, true)
	_ = p.phone.Close()
	exits(4)
	p = hostedConnect(t, r, home, pair)
	p.request(60, protocol.TypeAppAssetRequest, asset)
	p.response(60, 200, binary, false)
	exits(5)
	// The mutation commits once; not one response byte may precede retirement.
	req.Query = "barrier=1"
	req.Body = base64.StdEncoding.EncodeToString([]byte("uncertain committed"))
	p.request(70, protocol.TypeAppAPIRequest, req)
	hostedWait(t, func() bool { return hostedExists(filepath.Join(data, "committed")) })
	p.quiet()
	_ = p.phone.Close()
	exits(6)
	hostedWait(t, func() bool { return hostedExists(filepath.Join(data, "response-retired")) })
	p = hostedConnect(t, r, home, pair)
	fresh := hostedAPI()
	fresh.Query = "barrier=read"
	p.request(70, protocol.TypeAppAPIRequest, fresh)
	hostedWait(t, func() bool { return hostedExists(filepath.Join(data, "fresh-active")) })
	hostedWrite(t, filepath.Join(data, "release-old"), []byte("yes"))
	hostedWait(t, func() bool { return hostedExists(filepath.Join(data, "old-released")) })
	p.quiet()
	hostedWrite(t, filepath.Join(data, "release-fresh"), []byte("yes"))
	p.response(70, 200, []byte("uncertain committed"), false)
	exits(7)
	for name, want := range map[string][]byte{"invocations": []byte("write\nwrite\n"), "app.sqlite": saved, "pid": pid} {
		got, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("reconnect changed", name, err)
		}
	}
	if readPersistedServerID(t, home) != server {
		t.Fatal("host identity changed")
	}
	d.stop(t)
	var servicePID int
	_, _ = fmt.Sscan(string(pid), &servicePID)
	if syscall.Kill(servicePID, 0) == nil {
		t.Fatal("backend not joined")
	}
}

// Keep exec-based setup cancellable; no test-side migration runs during service ownership.
func hostedCommand(t *testing.T, bin, dir string, env []string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hosted setup %s: %v: %s", filepath.Base(bin), err, out)
	}
	return out
}
