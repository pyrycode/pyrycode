package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const reflectedSecret = "synthetic-private-relay-credential"

type testResponseError struct {
	response *http.Response
	cause    error
}

func (e *testResponseError) Error() string { return "response failure" }
func (e *testResponseError) Unwrap() error { return e.cause }

func TestSafeTransportErrorClassifications(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []error{ErrUpgradeRejected, ErrFatalClose, ErrClosed, ErrDisconnected, ErrNotConnected, context.Canceled, context.DeadlineExceeded} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			raw := &testResponseError{
				response: &http.Response{Header: http.Header{"X-Private": {reflectedSecret}}, Body: io.NopCloser(strings.NewReader(reflectedSecret))},
				cause:    errors.Join(sentinel, &UpgradeRejectionError{StatusCode: 401}, websocket.CloseError{Code: 4409, Reason: reflectedSecret}),
			}
			err := safeTransportError("dial", raw)
			if !errors.Is(err, sentinel) || websocket.CloseStatus(err) != 4409 || upgradeStatus(err) != 401 {
				t.Fatalf("lost classification: %v", err)
			}
			var responseError *testResponseError
			if errors.Is(err, raw) || errors.As(err, &responseError) {
				t.Fatal("raw response object remains reachable")
			}
			assertSafeTransportError(t, err)
		})
	}
}

func assertSafeTransportError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", err, err), reflectedSecret) {
		t.Fatalf("unsafe error: %v", err)
	}
	if ce := (websocket.CloseError{}); errors.As(err, &ce) && ce.Reason != "" {
		t.Fatalf("retained close reason: %q", ce.Reason)
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			assertSafeTransportError(t, child)
		}
	case interface{ Unwrap() error }:
		if child := e.Unwrap(); child != nil {
			assertSafeTransportError(t, child)
		}
	}
}

func assertSafeTransportLogs(t *testing.T, rec *recordingHandler) {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, r := range rec.records {
		if strings.Contains(r.Message, reflectedSecret) {
			t.Fatalf("unsafe log message: %s", r.Message)
		}
		r.Attrs(func(a slog.Attr) bool {
			if strings.Contains(fmt.Sprintf("%+v %#v", a.Value, a.Value.Any()), reflectedSecret) {
				t.Errorf("unsafe log attribute: %s", a)
			}
			if err, ok := a.Value.Any().(error); ok {
				assertSafeTransportError(t, err)
			}
			return true
		})
	}
}

func TestSafeUpgradeDiagnostics(t *testing.T) {
	t.Parallel()
	for _, status := range []int{401, 503, 101} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Connection", reflectedSecret)
				w.Header().Set("Upgrade", reflectedSecret)
				w.Header().Set("X-Private", reflectedSecret)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(reflectedSecret))
			}))
			defer srv.Close()
			c := New(Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Logger: testLogger(t)})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := c.realDial(ctx)
			assertSafeTransportError(t, err)
		})
	}
}

func TestSafeFatalCloseDiagnostics(t *testing.T) {
	t.Parallel()
	for _, established := range []bool{false, true} {
		t.Run(fmt.Sprintf("established=%v", established), func(t *testing.T) {
			rec := &recordingHandler{}
			relay := newCloseCodeRelay(t, websocket.StatusCode(4409), reflectedSecret)
			var dials atomic.Int64
			c := newClientForTest(t, Config{
				URL: relay.URL(), Logger: slog.New(rec), FatalCloseCodes: []websocket.StatusCode{4409}, FatalCloseThreshold: 2,
			}, testOpts{reconnectInitial: time.Millisecond, reconnectMax: time.Millisecond})
			c.dialFn = func(ctx context.Context) (*websocket.Conn, error) {
				if dials.Add(1) == 2 && established {
					return c.realDial(ctx)
				}
				return nil, fmt.Errorf("%s: %w", reflectedSecret, websocket.CloseError{Code: 4409, Reason: reflectedSecret})
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			err := c.Connect(ctx)
			if !errors.Is(err, ErrFatalClose) || websocket.CloseStatus(err) != 4409 || dials.Load() != 2 {
				t.Fatalf("fatal result: %v, status %d, dials %d", err, websocket.CloseStatus(err), dials.Load())
			}
			assertSafeTransportError(t, err)
			assertSafeTransportLogs(t, rec)
		})
	}
}

func TestSafePumpDiagnostics(t *testing.T) {
	t.Parallel()
	for _, pump := range []string{"recv", "ping", "send"} {
		t.Run(pump, func(t *testing.T) {
			rec := &recordingHandler{}
			relay := newCloseCodeRelay(t, websocket.StatusCode(4409), reflectedSecret)
			c := newClientForTest(t, Config{URL: relay.URL(), Logger: slog.New(rec)}, testOpts{pingInterval: time.Millisecond})
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			conn, err := c.realDial(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			switch pump {
			case "recv":
				err = c.recvPump(ctx, conn)
			case "ping":
				// A read processes the close frame; ping may then see a generic
				// socket failure rather than the original close code.
				_, _, _ = conn.Read(ctx)
				err = c.pingLoop(ctx, conn)
			case "send":
				_, _, _ = conn.Read(ctx)
				c.setConn(conn)
				c.connDone = make(chan struct{})
				done := make(chan error, 1)
				go func() { done <- c.sendPump(ctx, conn) }()
				err = c.SendContext(ctx, []byte("frame"))
				assertSafeTransportError(t, <-done)
			}
			if pump == "recv" && websocket.CloseStatus(err) != 4409 {
				t.Fatalf("lost close status: %v", err)
			}
			assertSafeTransportError(t, err)
			assertSafeTransportLogs(t, rec)
		})
	}
}

func TestTerminalUpgradeRejection(t *testing.T) {
	t.Parallel()
	for _, reconnect := range []bool{false, true} {
		for _, status := range []int{401, 403} {
			t.Run(fmt.Sprintf("reconnect=%v/status=%d", reconnect, status), func(t *testing.T) {
				var dials atomic.Int64
				rec := &recordingHandler{}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if dials.Add(1) == 1 && reconnect {
						conn, err := websocket.Accept(w, r, nil)
						if err == nil {
							_ = conn.Close(websocket.StatusNormalClosure, reflectedSecret)
						}
						return
					}
					w.Header().Set("X-Private", reflectedSecret)
					http.Error(w, reflectedSecret, status)
				}))
				defer srv.Close()
				c := newClientForTest(t, Config{
					URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Logger: slog.New(rec),
					TerminalUpgradeStatuses: []int{401, 403}, FatalCloseThreshold: 100,
				}, testOpts{reconnectInitial: time.Millisecond, reconnectMax: time.Millisecond})
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				err := c.Connect(ctx)
				var rejection *UpgradeRejectionError
				if !errors.As(err, &rejection) || rejection.StatusCode != status || !errors.Is(err, ErrUpgradeRejected) {
					t.Fatalf("Connect = %v, want typed HTTP %d rejection", err, status)
				}
				if errors.Is(err, ErrFatalClose) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("wrong classification: %v", err)
				}
				wantDials := int64(1)
				if reconnect {
					wantDials++
				}
				if dials.Load() != wantDials {
					t.Fatalf("dials = %d, want %d", dials.Load(), wantDials)
				}
				assertSafeTransportError(t, err)
				assertSafeTransportLogs(t, rec)
			})
		}
	}
}

func TestUpgradeRetryRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		policy   []int
		statuses []int
		warns    int
	}{
		{"default-rearms-warning", nil, []int{401, 401, 101, 401, 401, 101}, 2},
		{"unselected-status", []int{401}, []int{403, 503, 101}, 1},
		{"invalid-101", []int{401, 101}, []int{101, 101}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, dials atomic.Int64
			rec := &recordingHandler{}
			drop := make(chan struct{}, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(requests.Add(1))
				if n > len(tc.statuses) {
					http.Error(w, reflectedSecret, http.StatusServiceUnavailable)
					return
				}
				if tc.statuses[n-1] != 101 || (tc.name == "invalid-101" && n == 1) {
					w.Header().Set("Connection", reflectedSecret)
					w.WriteHeader(tc.statuses[n-1])
					_, _ = w.Write([]byte(reflectedSecret))
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if n < len(tc.statuses) {
					select {
					case <-drop:
						_ = conn.Close(websocket.StatusNormalClosure, reflectedSecret)
					case <-ctx.Done():
					}
				} else {
					_, _, _ = conn.Read(r.Context())
				}
			}))
			defer srv.Close()
			c := newClientForTest(t, Config{
				URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Logger: slog.New(rec), TerminalUpgradeStatuses: tc.policy,
			}, testOpts{reconnectInitial: time.Millisecond, reconnectMax: time.Millisecond})
			c.dialFn = func(ctx context.Context) (*websocket.Conn, error) {
				if dials.Add(1) == 1 {
					return nil, errors.New(reflectedSecret)
				}
				return c.realDial(ctx)
			}
			done := make(chan error, 1)
			go func() { done <- c.Connect(ctx) }()
			connections := 1
			if tc.name == "default-rearms-warning" {
				connections = 2
			}
			for i := 0; i < connections; i++ {
				select {
				case <-c.Connected():
					if i+1 < connections {
						drop <- struct{}{}
					}
				case err := <-done:
					t.Fatalf("did not recover: %v", err)
				}
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("shutdown = %v", err)
			}
			if got, want := dials.Load(), int64(len(tc.statuses)+1); got != want {
				t.Fatalf("dials = %d, want %d", got, want)
			}
			assertSafeTransportLogs(t, rec)
			rec.mu.Lock()
			defer rec.mu.Unlock()
			warns := 0
			for _, r := range rec.records {
				if r.Level == slog.LevelWarn && strings.Contains(r.Message, "upgrade") {
					warns++
				}
			}
			if warns != tc.warns {
				t.Fatalf("upgrade warnings = %d, want %d", warns, tc.warns)
			}
		})
	}
}

func TestUpgradeRetryShutdown(t *testing.T) {
	t.Parallel()
	for _, closeClient := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%v", closeClient), func(t *testing.T) {
			rec := &recordingHandler{}
			attempted := make(chan struct{}, 1)
			var dials atomic.Int64
			c := newClientForTest(t, Config{Logger: slog.New(rec)}, testOpts{
				reconnectInitial: time.Hour, reconnectMax: time.Hour,
				dialFn: func(context.Context) (*websocket.Conn, error) {
					dials.Add(1)
					attempted <- struct{}{}
					return nil, &UpgradeRejectionError{StatusCode: 401}
				},
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.Connect(ctx) }()
			<-attempted
			want := context.Canceled
			if closeClient {
				_ = c.Close()
				want = ErrClosed
			} else {
				cancel()
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("shutdown = %v, want %v", err, want)
			}
			if dials.Load() != 1 {
				t.Fatalf("dials = %d, want 1", dials.Load())
			}
			assertSafeTransportLogs(t, rec)
		})
	}
}
