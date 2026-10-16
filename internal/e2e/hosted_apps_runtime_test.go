//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type hostedProcess struct {
	PID        int `json:"pid"`
	Descendant int `json:"descendant"`
	Port       int `json:"port"`
}

func TestHostedAppProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "hosted-app-child" {
		return
	}
	child := exec.Command("/bin/sleep", "99999")
	if child.Start() != nil {
		os.Exit(2)
	}
	var port int
	_, _ = fmt.Sscan(os.Getenv("PYRY_APP_PORT"), &port)
	data := os.Getenv("PYRY_APP_DATA_DIR")
	proof, _ := json.Marshal(hostedProcess{os.Getpid(), child.Process.Pid, port})
	if os.WriteFile(filepath.Join(data, "process.tmp"), proof, 0600) != nil || os.Rename(filepath.Join(data, "process.tmp"), filepath.Join(data, "process.json")) != nil {
		os.Exit(3)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	service, err := os.ReadFile("dist/service/main.js")
	if err != nil {
		os.Exit(4)
	}
	resources := hostedResourceHandler(data)
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_pyry/health" {
			resources.ServeHTTP(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": string(service) != "bad-health", "app_id": os.Getenv("PYRY_APP_ID"), "release_version": os.Getenv("PYRY_APP_RELEASE")})
	})}
	go func() { <-sig; _ = child.Process.Kill(); _ = child.Wait(); _ = server.Close() }()
	time.Sleep(150 * time.Millisecond)
	_ = server.ListenAndServe()
	os.Exit(0)
}
func hostedNodeEnv(t *testing.T) []string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo v24.15.0; else exec '%s' -test.run='^TestHostedAppProcess$' -- hosted-app-child; fi\n", strings.ReplaceAll(bin, "'", "'\\''"))
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "GORACE=atexit_sleep_ms=0"}
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
	t.Fatal("hosted app observation deadline")
}
func hostedRunning(t *testing.T, home string) hostedSnapshot {
	t.Helper()
	// Listen can precede registry normalization. Spawn evidence prevents a seeded
	// running observation from satisfying this wait before the daemon opens apps.
	hostedProof(t, home, hostedAppID)
	var s hostedSnapshot
	hostedWait(t, func() bool {
		s, _ = hostedRead(t, home)
		return s.Records[0].State == "running" && s.Records[3].State == "running"
	})
	return s
}
func hostedProof(t *testing.T, home, id string) hostedProcess {
	t.Helper()
	var p hostedProcess
	hostedWait(t, func() bool {
		b, err := os.ReadFile(filepath.Join(hostedRoot(home), id, "data/process.json"))
		return err == nil && json.Unmarshal(b, &p) == nil
	})
	return p
}
func hostedGone(t *testing.T, p hostedProcess) {
	t.Helper()
	hostedWait(t, func() bool {
		for _, pid := range []int{p.PID, p.Descendant} {
			if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				// A killed orphan can remain a zombie until the host reaps it.
				b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if err != nil || !strings.Contains(string(b), ") Z ") {
					return false
				}
			}
		}
		return true
	})
}
func hostedHealthy(t *testing.T, p hostedProcess) {
	t.Helper()
	c := http.Client{Timeout: time.Second}
	res, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/_pyry/health", p.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
}

func TestHostedAppsRuntimeFailureIsolation(t *testing.T) {
	for _, mode := range []string{"missing", "unsupported", "start", "health"} {
		t.Run(mode, func(t *testing.T) {
			home := shortHome(t)
			fr := fakerelay.New(relayTestLogger())
			t.Cleanup(func() { _ = fr.Close() })
			pairing, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: fr.URL() + "/v2/server", DeviceName: "phone"})
			if err != nil {
				t.Fatal(err)
			}
			seeded := hostedSeed(t, home, "Failure isolation")
			env := hostedNodeEnv(t)
			node := strings.TrimPrefix(strings.Split(env[0], ":")[0], "PATH=") + "/node"
			switch mode {
			case "missing":
				if err := os.Remove(node); err != nil {
					t.Fatal(err)
				}
				env[0] = "PATH=" + filepath.Dir(node)
			case "unsupported":
				if err := os.WriteFile(node, []byte("#!/bin/sh\necho v25.0.0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			case "start":
				old, err := os.ReadFile(node)
				if err != nil {
					t.Fatal(err)
				}
				script := strings.Replace(string(old), "else exec", "elif [ \"$PYRY_APP_ID\" = \""+hostedAppID+"\" ]; then exit 1; else exec", 1)
				if err := os.WriteFile(node, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "health":
				path := filepath.Join(hostedRoot(home), hostedAppID, "builds", strings.Repeat("a", 64), "dist/service/main.js")
				if err := os.WriteFile(path, []byte("bad-health"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			h := StartStreamInteractiveWithRelay(t, home, "11111111-1111-4111-8111-111111111111", pairing.Relay, env...)
			pub, err := base64.StdEncoding.DecodeString(pairing.ServerStaticPubkey)
			if err != nil {
				t.Fatal(err)
			}
			phone, send, recv := dialPairedPhone(t, fr, home, pairing.Token, pub, "phone")
			defer phone.Close()
			seal, next := noiseWire(t, phone, send, recv)
			conv := createConversationOverWire(t, seal, next, 32049)
			sendAndAwaitEcho(t, seal, next, "turn despite app failure", 32050, conv, "failure-turn", "still usable", "still usable")
			if mode != "missing" {
				deadline := time.Now().Add(20 * time.Second)
				for {
					s, _ := hostedRead(t, home)
					if s.Records[0].State == "failed" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("missing failure observation", s)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if mode == "start" || mode == "health" {
				// Failure and sibling readiness are independent owner observations.
				hostedWait(t, func() bool {
					s, _ := hostedRead(t, home)
					return s.Records[3].State == "running"
				})
				hostedHealthy(t, hostedProof(t, home, seeded.Records[3].AppID))
			}
			h.Stop(t)
		})
	}
}
