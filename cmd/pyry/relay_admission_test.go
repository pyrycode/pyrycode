package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/identity"
)

// Predictable fixtures, never real user credentials.
var daemonAdmissionOld = strings.Repeat("a", 64)
var daemonAdmissionNew = strings.Repeat("b", 64)
var daemonAdmissionOther = strings.Repeat("c", 64)

const daemonAdmissionPeer = "private-peer-admission-diagnostic"
const daemonAdmissionMessage = "relay: admission failed; correct relay admission configuration and restart the daemon"

type daemonAdmissionAttempt struct {
	id     string
	tokens []string
	ws     *websocket.Conn
}
type daemonAdmissionRelay struct {
	url      string
	attempts chan daemonAdmissionAttempt
	count    atomic.Int64
}

func testDaemonAdmissionRelay(t *testing.T, accept func(string, string) bool) *daemonAdmissionRelay {
	t.Helper()
	relay := &daemonAdmissionRelay{attempts: make(chan daemonAdmissionAttempt, 20)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relay.count.Add(1)
		if r.URL.RequestURI() != "/v1/server" {
			t.Error("admission credential or extra data entered URL")
		}
		attempt := daemonAdmissionAttempt{id: r.Header.Get("x-pyrycode-server"), tokens: r.Header.Values("x-pyrycode-admission-token")}
		if !accept(attempt.id, r.Header.Get("x-pyrycode-admission-token")) {
			w.Header().Set("X-Private-Diagnostic", daemonAdmissionPeer+daemonAdmissionOld)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(daemonAdmissionPeer + daemonAdmissionOld))
			relay.attempts <- attempt
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		attempt.ws = ws
		relay.attempts <- attempt
		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			assertDaemonAdmissionSafe(t, string(raw))
		}
	}))
	relay.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Cleanup(srv.Close)
	return relay
}

func (r *daemonAdmissionRelay) next(t *testing.T, id, token string, admitted bool) daemonAdmissionAttempt {
	t.Helper()
	select {
	case got := <-r.attempts:
		var want []string
		if token != "" {
			want = []string{token}
		}
		if got.id != id || !reflect.DeepEqual(got.tokens, want) || (got.ws != nil) != admitted {
			t.Fatal("identity, admission header multiplicity/value or upgrade mismatch")
		}
		return got
	case <-time.After(8 * time.Second):
		t.Fatal("relay attempt did not arrive")
	}
	return daemonAdmissionAttempt{}
}

func testDaemonAdmissionHome(t *testing.T) string {
	t.Helper()
	home := accountTestHome(t)
	t.Setenv("PYRY_ALLOW_INSECURE_RELAY", "1")
	t.Setenv("PYRY_RELAY_4409_THRESHOLD", "1")
	return home
}

func testWriteDaemonAdmission(t *testing.T, tokens map[string]string) {
	t.Helper()
	cfg := map[string]any{"relay_url": ""}
	if tokens != nil {
		cfg["relay_admission_tokens"] = tokens
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolveConfigPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testStartAdmissionDaemon(t *testing.T, home, name, url string) (string, func()) {
	t.Helper()
	socket := filepath.Join(home, name+".sock")
	done := make(chan error, 1)
	stub := stubBinary(t)
	go func() {
		done <- runSupervisor([]string{"-pyry-name", name, "-pyry-socket", socket, "-pyry-workdir", home, "-pyry-relay", url, "-pyry-claude", stub, "-pyry-codex", stub})
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = control.Stop(ctx, socket)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("daemon stop: %v", err)
				}
			case <-ctx.Done():
				t.Error("daemon failed to stop")
			}
		})
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := control.Status(t.Context(), socket); err == nil {
			return socket, stop
		}
		select {
		case err := <-done:
			done <- err
			t.Fatalf("daemon startup: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon status did not become responsive")
	return "", stop
}

func assertDaemonAdmissionSafe(t *testing.T, value string) {
	t.Helper()
	for _, forbidden := range []string{daemonAdmissionOld, daemonAdmissionNew, daemonAdmissionOther, daemonAdmissionPeer, "relay_admission_tokens"} {
		if strings.Contains(value, forbidden) {
			t.Error("admission private data exposed outside upgrade header")
		}
	}
}

func testAdmissionLocalState(t *testing.T, socket string, rejected bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := control.Status(t.Context(), socket)
		if err != nil {
			t.Fatal(err)
		}
		assertDaemonAdmissionSafe(t, fmt.Sprintf("%+v", status))
		logs, err := control.Logs(t.Context(), socket)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(logs)
		assertDaemonAdmissionSafe(t, string(raw))
		found := strings.Contains(string(raw), daemonAdmissionMessage)
		if !rejected || found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("static admission diagnostic absent")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDaemonAdmissionRejection(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		for _, reconnect := range []bool{false, true} {
			t.Run(fmt.Sprintf("supplied=%v/reconnect=%v", supplied, reconnect), func(t *testing.T) {
				home := testDaemonAdmissionHome(t)
				id, err := identity.LoadOrCreate(resolveServerIDPath("rejected"))
				if err != nil {
					t.Fatal(err)
				}
				token := ""
				// A different exact identity is configured even when the selected entry is absent.
				tokens := map[string]string{"other-identity": daemonAdmissionOther}
				if supplied {
					token = daemonAdmissionOld
					tokens[string(id)] = token
				}
				testWriteDaemonAdmission(t, tokens)
				var reject atomic.Bool
				reject.Store(!reconnect)
				r := testDaemonAdmissionRelay(t, func(_, _ string) bool { return !reject.Load() })
				socket, stop := testStartAdmissionDaemon(t, home, "rejected", r.url)
				attempt := r.next(t, string(id), token, reconnect)
				want := int64(1)
				if reconnect {
					reject.Store(true)
					_ = attempt.ws.CloseNow()
					r.next(t, string(id), token, false)
					want++
				}
				testAdmissionLocalState(t, socket, true)
				// Longer than the first retry delay, even at maximum jitter.
				time.Sleep(1300 * time.Millisecond)
				testAdmissionLocalState(t, socket, true)
				if r.count.Load() != want {
					t.Error("401 continued automatic attempts")
				}
				stop()
			})
		}
	}
}

func TestDaemonAdmissionSelectionAndRotation(t *testing.T) {
	home := testDaemonAdmissionHome(t)
	a, err := identity.LoadOrCreate(resolveServerIDPath("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.LoadOrCreate(resolveServerIDPath("beta"))
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{string(a): daemonAdmissionOld, string(b): daemonAdmissionOther}
	testWriteDaemonAdmission(t, tokens)
	var revoked atomic.Bool
	r := testDaemonAdmissionRelay(t, func(id, token string) bool {
		return id == string(a) && (token == daemonAdmissionNew || token == daemonAdmissionOld && !revoked.Load()) || id == string(b) && token == daemonAdmissionOther
	})
	socketA, stopA := testStartAdmissionDaemon(t, home, "alpha", r.url)
	first := r.next(t, string(a), daemonAdmissionOld, true)
	socketB, stopB := testStartAdmissionDaemon(t, home, "beta", r.url)
	other := r.next(t, string(b), daemonAdmissionOther, true)
	pairing, err := control.MintPairing(t.Context(), socketA, "synthetic-phone", false)
	if err != nil {
		t.Fatal(err)
	}
	assertDaemonAdmissionSafe(t, pairing)
	// File changes do not affect existing daemons, including reconnect selection.
	tokens[string(a)] = daemonAdmissionNew
	testWriteDaemonAdmission(t, tokens)
	_ = first.ws.CloseNow()
	old := r.next(t, string(a), daemonAdmissionOld, true)
	// Both credentials work during overlap, but only restart selects the new token.
	stopA()
	socketA, stopA = testStartAdmissionDaemon(t, home, "alpha", r.url)
	fresh := r.next(t, string(a), daemonAdmissionNew, true)
	testAdmissionLocalState(t, socketA, false)
	stopA()
	tokens[string(a)] = daemonAdmissionOld
	testWriteDaemonAdmission(t, tokens)
	socketA, stopA = testStartAdmissionDaemon(t, home, "alpha", r.url)
	old = r.next(t, string(a), daemonAdmissionOld, true)
	// Simulate relay restart revoking old credentials and dropping all connections.
	revoked.Store(true)
	_ = old.ws.CloseNow()
	_ = other.ws.CloseNow()
	// The two reconnects can arrive in either order.
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case got := <-r.attempts:
			if got.id == string(a) {
				if got.ws != nil || !reflect.DeepEqual(got.tokens, []string{daemonAdmissionOld}) {
					t.Fatal("revoked old token admitted")
				}
			} else if got.id == string(b) {
				if got.ws == nil || !reflect.DeepEqual(got.tokens, []string{daemonAdmissionOther}) {
					t.Fatal("other identity affected")
				}
			} else {
				t.Fatal("unexpected identity")
			}
			seen[got.id] = true
		case <-time.After(8 * time.Second):
			t.Fatal("revocation reconnect missing")
		}
	}
	if len(seen) != 2 {
		t.Fatal("identity reconnect coverage incomplete")
	}
	testAdmissionLocalState(t, socketA, true)
	testAdmissionLocalState(t, socketB, false)
	tokens[string(a)] = daemonAdmissionNew
	testWriteDaemonAdmission(t, tokens)
	count := r.count.Load()
	time.Sleep(1300 * time.Millisecond)
	if r.count.Load() != count {
		t.Fatal("config edit restarted rejected relay leg")
	}
	stopA()
	socketA, stopA = testStartAdmissionDaemon(t, home, "alpha", r.url)
	fresh = r.next(t, string(a), daemonAdmissionNew, true)
	testAdmissionLocalState(t, socketA, false)
	if fresh.ws == nil {
		t.Fatal("corrected restart did not recover")
	}
	stopA()
	stopB()
}
