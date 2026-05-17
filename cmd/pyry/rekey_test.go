package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// fakeRekeyer satisfies control.Rekeyer for tests. Single-shot: returnErr
// governs every Rekey call's result. Mirrors the shape of the in-package
// fakeRekeyer in internal/control/rekey_test.go, re-implemented here because
// package-private _test.go types are not importable across packages.
type fakeRekeyer struct {
	mu        sync.Mutex
	calls     []string
	returnErr error
}

func (f *fakeRekeyer) Rekey(_ context.Context, connID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, connID)
	retErr := f.returnErr
	f.mu.Unlock()
	return retErr
}

// rekeyTestResolver satisfies control.SessionResolver for tests. VerbRekey
// never touches the resolver path; the stub only exists because
// control.NewServer panics on a nil resolver argument.
type rekeyTestResolver struct{}

func (rekeyTestResolver) Lookup(_ sessions.SessionID) (control.Session, error) {
	return nil, errors.New("not used")
}

func (rekeyTestResolver) ResolveID(_ string) (sessions.SessionID, error) {
	return "", errors.New("not used")
}

// shortSockTempDir returns a short tempdir suitable for Unix socket paths.
// t.TempDir() lives under /var/folders/... on macOS, which combined with
// long test names blows past the 104-byte sun_path limit. /tmp is short.
// Local mirror of internal/control/server_test.go:shortTempDir.
func shortSockTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pyryrekey")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startControlServerWithRekeyer wires a control.Server on a tempdir socket
// with the given Rekeyer installed, then runs Serve in a goroutine. Returns
// the socket path and a stop function that cancels the context and waits
// for Serve to return. Local re-implementation of
// internal/control/rekey_test.go's startServerWithRekeyer — same shape,
// re-defined in package main because the original is package-private.
//
// Passing a nil Rekeyer is valid: SetRekeyer with nil leaves the server's
// rekeyer field as nil, which is the production "no rekeyer configured"
// state.
func startControlServerWithRekeyer(t *testing.T, r control.Rekeyer) (sockPath string, stop func()) {
	t.Helper()
	dir := shortSockTempDir(t)
	sockPath = filepath.Join(dir, "p.sock")

	srv := control.NewServer(sockPath, rekeyTestResolver{}, nil, nil, nil, nil)
	if r != nil {
		srv.SetRekeyer(r)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	stop = func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("Serve did not return after cancel")
		}
	}
	return sockPath, stop
}

// TestParseRekeyArgs covers the FlagSet surface of `pyry rekey`: the
// happy path and the three error shapes runRekey maps to exit 2. Mirrors
// TestParsePairRevokeArgs.
func TestParseRekeyArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantConnID string
		wantErr    string
	}{
		{name: "happy", args: []string{"conn-abc"}, wantConnID: "conn-abc"},
		{name: "missing positional", args: nil, wantErr: "missing conn_id"},
		{name: "extra positional", args: []string{"a", "b"}, wantErr: "unexpected positional"},
		{name: "unknown flag rejected", args: []string{"--bogus", "conn-abc"}, wantErr: "flag provided but not defined"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRekeyArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q missing fragment %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.connID != tc.wantConnID {
				t.Errorf("connID=%q want %q", got.connID, tc.wantConnID)
			}
		})
	}
}

// TestRekeyVerdict covers the (exitCode, stderrLine) formatter for every
// error class runRekey routes through it. The verdict helper is the seam
// that keeps formatting unit-testable without intercepting os.Exit.
func TestRekeyVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		connID         string
		err            error
		wantExit       int
		wantStderrLine string
	}{
		{
			name:           "success",
			connID:         "conn-abc",
			err:            nil,
			wantExit:       0,
			wantStderrLine: "",
		},
		{
			name:           "conn not found typed sentinel",
			connID:         "conn-abc",
			err:            control.ErrConnNotFound,
			wantExit:       1,
			wantStderrLine: `pyry rekey: conn_id "conn-abc" not found`,
		},
		{
			name:           "conn not found wrapped",
			connID:         "conn-abc",
			err:            fmt.Errorf("wrapped: %w", control.ErrConnNotFound),
			wantExit:       1,
			wantStderrLine: `pyry rekey: conn_id "conn-abc" not found`,
		},
		{
			name:           "no rekeyer configured surfaced verbatim",
			connID:         "conn-xyz",
			err:            errors.New("rekey: no rekeyer configured"),
			wantExit:       1,
			wantStderrLine: "pyry rekey: rekey: no rekeyer configured",
		},
		{
			name:           "transport-shaped untyped error",
			connID:         "conn-xyz",
			err:            errors.New("dial unix /tmp/x.sock: connect: connection refused"),
			wantExit:       1,
			wantStderrLine: "pyry rekey: dial unix /tmp/x.sock: connect: connection refused",
		},
		{
			name:           "connID with embedded quote is Go-quoted",
			connID:         `conn"id`,
			err:            control.ErrConnNotFound,
			wantExit:       1,
			wantStderrLine: `pyry rekey: conn_id "conn\"id" not found`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotExit, gotLine := rekeyVerdict(tc.connID, tc.err)
			if gotExit != tc.wantExit {
				t.Errorf("exitCode = %d, want %d", gotExit, tc.wantExit)
			}
			if gotLine != tc.wantStderrLine {
				t.Errorf("stderrLine = %q, want %q", gotLine, tc.wantStderrLine)
			}
		})
	}
}

// TestRunRekey_BogusSocket_ReturnsWrappedError pins AC4 bullet 1: the
// transport-error path. Verb invoked against a bogus socket path returns
// a wrapped error with the `rekey:` substring. Mirrors
// TestRunSessions_RmDispatch at cmd/pyry/sessions_test.go:73-93.
func TestRunRekey_BogusSocket_ReturnsWrappedError(t *testing.T) {
	t.Setenv("PYRY_NAME", "")
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	err := runRekey([]string{"-pyry-socket", bogusSock, "some-conn-id"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rekey:") {
		t.Errorf("error %q missing %q wrap fragment", msg, "rekey:")
	}
	if strings.Contains(msg, "%!") {
		t.Errorf("error %q contains %%-verb format escape (raw %%v dump)", msg)
	}
}

// TestRunRekey_UnknownConnID_ExitsOnePrintsTypedMessage pins AC4 bullet 2:
// the wire round-trip from a server-side ErrConnNotFound through
// Response.ErrorCode = ErrCodeConnNotFound back to a reconstructed
// sentinel. Asserts the verdict formatter prints the AC-pinned message.
//
// runRekey itself is not invoked because the typed-not-found path ends in
// os.Exit(1), which would kill the test process. The wire path under test
// IS exercised — control.Rekey is the real client, control.Server is the
// real server, and the error fed into rekeyVerdict is the real
// reconstructed sentinel.
func TestRunRekey_UnknownConnID_ExitsOnePrintsTypedMessage(t *testing.T) {
	t.Parallel()

	sock, stop := startControlServerWithRekeyer(t, &fakeRekeyer{returnErr: control.ErrConnNotFound})
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := control.Rekey(ctx, sock, "missing-conn")
	if err == nil {
		t.Fatal("expected error from control.Rekey, got nil")
	}
	if !errors.Is(err, control.ErrConnNotFound) {
		t.Fatalf("errors.Is(err, control.ErrConnNotFound) = false, err = %v", err)
	}

	gotExit, gotLine := rekeyVerdict("missing-conn", err)
	const wantLine = `pyry rekey: conn_id "missing-conn" not found`
	if gotExit != 1 {
		t.Errorf("exitCode = %d, want 1", gotExit)
	}
	if gotLine != wantLine {
		t.Errorf("stderrLine = %q, want %q", gotLine, wantLine)
	}
}

// TestRunRekey_NoRekeyerConfigured_ExitsOnePrintsVerbatimReject pins AC4
// bullet 3: the production-state "no rekeyer configured" guard. The
// server reply carries no ErrorCode (it is a plain Response.Error), so
// the client returns a plain errors.New(resp.Error); the verdict helper
// surfaces it verbatim with the double-`rekey:` shape the AC pins.
func TestRunRekey_NoRekeyerConfigured_ExitsOnePrintsVerbatimReject(t *testing.T) {
	t.Parallel()

	// nil rekeyer: server's rekeyer field stays nil, handleRekey replies
	// "rekey: no rekeyer configured" without a wire error code.
	sock, stop := startControlServerWithRekeyer(t, nil)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := control.Rekey(ctx, sock, "any-conn")
	if err == nil {
		t.Fatal("expected error from control.Rekey, got nil")
	}
	if errors.Is(err, control.ErrConnNotFound) {
		t.Errorf("errors.Is(err, ErrConnNotFound) = true, want false (guard, not typed sentinel)")
	}
	if err.Error() != "rekey: no rekeyer configured" {
		t.Errorf("err.Error() = %q, want %q", err.Error(), "rekey: no rekeyer configured")
	}

	gotExit, gotLine := rekeyVerdict("any-conn", err)
	const wantLine = "pyry rekey: rekey: no rekeyer configured"
	if gotExit != 1 {
		t.Errorf("exitCode = %d, want 1", gotExit)
	}
	if gotLine != wantLine {
		t.Errorf("stderrLine = %q, want %q", gotLine, wantLine)
	}
}
