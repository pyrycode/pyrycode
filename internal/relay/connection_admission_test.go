package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/transport"
)

// Synthetic fixtures, never production provisioning.
const testAdmissionToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const testAdmissionPeerText = "synthetic-peer-private-diagnostic"
const testAdmissionDiagnostic = testAdmissionPeerText + ": " + testAdmissionToken

type admissionLogCapture struct {
	slog.Handler
	mu      sync.Mutex
	records []slog.Record
}

func (h *admissionLogCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func testAdmissionLogger() (*slog.Logger, *admissionLogCapture) {
	h := &admissionLogCapture{Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})}
	return slog.New(h), h
}

func assertAdmissionSafeError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, secret := range []string{testAdmissionToken, testAdmissionPeerText} {
		if strings.Contains(fmt.Sprintf("%+v %#v", err, err), secret) {
			t.Error("error chain exposed synthetic private data")
		}
	}
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Reason != "" {
		t.Error("error chain retained a peer close reason")
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			assertAdmissionSafeError(t, child)
		}
	case interface{ Unwrap() error }:
		assertAdmissionSafeError(t, e.Unwrap())
	}
}

func (h *admissionLogCapture) assertSafe(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if strings.Contains(r.Message, testAdmissionToken) || strings.Contains(r.Message, testAdmissionPeerText) {
			t.Error("log message exposed synthetic private data")
		}
		r.Attrs(func(a slog.Attr) bool {
			text := fmt.Sprintf("%+v %#v", a.Value, a.Value.Any())
			if strings.Contains(text, testAdmissionToken) || strings.Contains(text, testAdmissionPeerText) {
				t.Error("log attribute exposed synthetic private data")
			}
			if err, ok := a.Value.Any().(error); ok {
				assertAdmissionSafeError(t, err)
			}
			return true
		})
	}
}

func testAdmissionConfig(url, token string, logger *slog.Logger) Config {
	return Config{
		ServerID: identity.ServerID(testServerID), RelayURL: "ws" + strings.TrimPrefix(url, "http"),
		BinaryVersion: "0.10.0-admission-test", Logger: logger, AllowInsecureScheme: true,
		AdmissionToken: token,
	}
}

func testConnectAdmission(t *testing.T, cfg Config) (*Connection, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	c, err := Connect(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
		_ = c.Wait()
	})
	return c, ctx
}

func assertAdmissionRequest(t *testing.T, r *http.Request, token string) {
	t.Helper()
	want := map[string][]string{
		"x-pyrycode-server": {testServerID}, "x-pyrycode-version": {"0.10.0-admission-test"},
		"user-agent": {"pyry/0.10.0-admission-test"}, "x-pyrycode-admission-token": nil,
	}
	if token != "" {
		want["x-pyrycode-admission-token"] = []string{token}
	}
	for name, values := range want {
		if !reflect.DeepEqual(r.Header.Values(name), values) {
			t.Errorf("%s header multiplicity/value mismatch", name)
		}
	}
	if r.URL.RequestURI() != "/v1/server?probe=public" {
		t.Error("unexpected request URL")
	}
}

func TestAdmissionTokenValidation(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	logger, logs := testAdmissionLogger()
	for _, tc := range []struct{ name, token string }{
		{"short", testAdmissionToken[:63]}, {"long", testAdmissionToken + "0"},
		{"uppercase", strings.ToUpper(testAdmissionToken)}, {"nonhex", "g" + testAdmissionToken[1:]},
		{"leading space", " " + testAdmissionToken}, {"trailing space", testAdmissionToken + " "},
		{"embedded space", " " + testAdmissionToken[1:]}, {"comma list", testAdmissionToken + "," + testAdmissionToken},
		{"newline", "\n" + testAdmissionToken[1:]}, {"nul", "\x00" + testAdmissionToken[1:]},
		{"unicode", "é" + testAdmissionToken[2:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Connect(t.Context(), testAdmissionConfig(srv.URL, tc.token, logger))
			if c != nil {
				_ = c.Close()
				_ = c.Wait()
				t.Fatal("invalid credential started a connection")
			}
			if !errors.Is(err, ErrInvalidConfig) || err.Error() != "relay: invalid config: AdmissionToken must be 64 lowercase hexadecimal characters" {
				t.Fatalf("expected static invalid-config diagnostic, got %v", err)
			}
			assertAdmissionSafeError(t, err)
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("malformed credential caused %d requests", requests.Load())
	}
	logs.assertSafe(t)
}

func TestAdmissionHeadersAndReconnect(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", testAdmissionToken} {
		t.Run(fmt.Sprintf("supplied=%v", token != ""), func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int64
			logger, logs := testAdmissionLogger()
			drop := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertAdmissionRequest(t, r, token)
				n := requests.Add(1)
				if n == 1 {
					w.Header().Set("X-Private", testAdmissionDiagnostic)
					http.Error(w, testAdmissionDiagnostic, http.StatusServiceUnavailable)
					return
				}
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Errorf("Accept: %v", err)
					return
				}
				defer ws.CloseNow()
				_, raw, err := ws.Read(r.Context())
				if err != nil {
					return
				}
				want, _ := json.Marshal(protocol.RoutingEnvelope{ConnID: "phone-public", Frame: json.RawMessage(`{"probe":"public"}`)})
				if string(raw) != string(want) {
					t.Error("outgoing routing/application frame changed or gained credential data")
				}
				if err := ws.Write(r.Context(), websocket.MessageText, raw); err != nil {
					t.Errorf("echo: %v", err)
				}
				if n == 2 {
					select {
					case <-drop:
						_ = ws.Close(websocket.StatusInternalError, testAdmissionDiagnostic)
					case <-r.Context().Done():
					case <-t.Context().Done():
					}
				} else {
					_, _, _ = ws.Read(r.Context())
				}
			}))
			t.Cleanup(srv.Close)
			c, ctx := testConnectAdmission(t, testAdmissionConfig(srv.URL+"/v1/server?probe=public", token, logger))
			iteration := 0
			defer func() {
				t.Logf("reconnect iteration=%d requests=%d connected=%v", iteration, requests.Load(), c.Connected())
				logs.mu.Lock()
				defer logs.mu.Unlock()
				for _, record := range logs.records {
					// Lifecycle messages and timestamps contain no peer/header values.
					t.Logf("transport lifecycle %s %s", record.Time.Format(time.RFC3339Nano), record.Message)
				}
			}()
			for i := 0; i < 2; i++ {
				iteration = i
				select {
				case <-c.Reconnected():
				case <-ctx.Done():
					t.Fatalf("connection did not recover: iteration=%d requests=%d connected=%v", i, requests.Load(), c.Connected())
				}
				if err := c.SendContext(ctx, protocol.RoutingEnvelope{ConnID: "phone-public", Frame: json.RawMessage(`{"probe":"public"}`)}); err != nil {
					t.Fatalf("SendContext: %v", err)
				}
				select {
				case env, ok := <-c.Frames():
					if !ok || env.ConnID != "phone-public" || string(env.Frame) != `{"probe":"public"}` {
						t.Fatal("unexpected echoed frame")
					}
				case <-ctx.Done():
					t.Fatal("frame did not arrive")
				}
				if i == 0 {
					close(drop)
				}
			}
			_ = c.Close()
			assertAdmissionSafeError(t, c.Wait())
			if requests.Load() != 3 {
				t.Fatalf("requests = %d, want 503 plus two upgrades", requests.Load())
			}
			logs.assertSafe(t)
		})
	}
}

func TestAdmissionUnauthorizedTerminal(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", testAdmissionToken} {
		for _, reconnect := range []bool{false, true} {
			t.Run(fmt.Sprintf("supplied=%v/reconnect=%v", token != "", reconnect), func(t *testing.T) {
				t.Parallel()
				var requests atomic.Int64
				logger, logs := testAdmissionLogger()
				drop := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assertAdmissionRequest(t, r, token)
					if requests.Add(1) == 1 && reconnect {
						ws, err := websocket.Accept(w, r, nil)
						if err != nil {
							t.Errorf("Accept: %v", err)
							return
						}
						defer ws.CloseNow()
						select {
						case <-drop:
							_ = ws.Close(statusServerIDConflict, testAdmissionDiagnostic)
						case <-r.Context().Done():
						case <-t.Context().Done():
						}
						return
					}
					w.Header().Set("X-Private", testAdmissionDiagnostic)
					http.Error(w, testAdmissionDiagnostic, http.StatusUnauthorized)
				}))
				t.Cleanup(srv.Close)
				cfg := testAdmissionConfig(srv.URL+"/v1/server?probe=public", token, logger)
				cfg.ServerIDConflictThreshold = 100
				c, ctx := testConnectAdmission(t, cfg)
				want := int64(1)
				if reconnect {
					select {
					case <-c.Reconnected():
						close(drop)
					case <-ctx.Done():
						t.Fatal("initial connection did not establish")
					}
					want++
				}
				err := c.Wait()
				if !errors.Is(err, ErrAdmissionFailed) || err.Error() != "relay: daemon admission failed (HTTP 401)" {
					t.Fatalf("Wait = %v, want static admission failure", err)
				}
				for _, other := range []error{ErrServerIDConflict, transport.ErrFatalClose, context.Canceled, context.DeadlineExceeded} {
					if errors.Is(err, other) {
						t.Errorf("admission failure also matched %v", other)
					}
				}
				if requests.Load() != want {
					t.Fatalf("requests = %d, want %d", requests.Load(), want)
				}
				if _, ok := <-c.Frames(); ok {
					t.Error("Frames remained open after admission rejection")
				}
				assertAdmissionSafeError(t, err)
				logs.assertSafe(t)
			})
		}
	}
}
