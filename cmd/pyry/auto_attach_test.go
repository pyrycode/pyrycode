package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

func TestExtractSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"space_separated_double_dash", []string{"--session-id", "abc"}, "abc"},
		{"space_separated_single_dash", []string{"-session-id", "abc"}, "abc"},
		{"glued_double_dash", []string{"--session-id=abc"}, "abc"},
		{"glued_single_dash", []string{"-session-id=abc"}, "abc"},
		{"absent", []string{"--model", "sonnet"}, ""},
		{"no_args", nil, ""},
		{"last_arg_no_value", []string{"--session-id"}, ""},
		{"empty_value", []string{"--session-id", ""}, ""},
		{"empty_glued_value", []string{"--session-id="}, ""},
		{"preserves_value_with_dashes",
			[]string{"--session-id", "11111111-2222-4333-8444-555555555555"},
			"11111111-2222-4333-8444-555555555555"},
		{"first_match_wins", []string{"--session-id", "A", "--session-id", "B"}, "A"},
		{"embedded_in_args",
			[]string{"--model", "sonnet", "--session-id", "abc", "-p", "hi"},
			"abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extractSessionID(tt.in); got != tt.want {
				t.Errorf("extractSessionID(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// shortTempDir mirrors internal/control's helper: t.TempDir() lives under
// /var/folders/... on macOS which combined with long test names blows past
// the 104-byte sun_path limit. /tmp is short.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pyryauto")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// canonicalUUID is a valid-shaped UUIDv4 string for tests that exercise
// the wire-call branch. The value is opaque to the helper; the daemon
// validates server-side.
const canonicalUUID = "11111111-2222-4333-8444-555555555555"

// startHasIDStub stands up a tiny Unix-socket accept loop that decodes
// one Request and writes back a canned has-id Response. Replaces a full
// test server for the unit-level branches that only need to drive the
// has-id decision (the AttachStdio commit path is e2e in #163, not here).
//
// answer drives Response.SessionsHasID.Has when err is empty; if err is
// non-empty, Response.Error wins (matches the wire semantics that
// SessionsHasID returns an error to the client).
func startHasIDStub(t *testing.T, sock string, answer bool, errMsg string) func() {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				var req control.Request
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}
				resp := control.Response{}
				if errMsg != "" {
					resp.Error = errMsg
				} else {
					resp.SessionsHasID = &control.SessionsHasIDResult{Has: answer}
				}
				_ = json.NewEncoder(c).Encode(resp)
			}(conn)
		}
	}()
	return func() {
		_ = ln.Close()
		wg.Wait()
	}
}

func TestTryAutoAttach_NoSessionID(t *testing.T) {
	// Not parallel — t.Setenv mutates the process environment.
	t.Setenv("PYRY_NO_AUTO_ATTACH", "")

	// Pass a path that does not exist. Even if the helper called os.Stat
	// the result would still be fall-through, but the no-session-id branch
	// is supposed to return before any syscall — assert via a tight wall
	// clock budget.
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "absent.sock")

	start := time.Now()
	handled, err := tryAutoAttach(sock, []string{"--model", "sonnet"})
	elapsed := time.Since(start)

	if handled || err != nil {
		t.Fatalf("got (handled=%v, err=%v), want (false, nil)", handled, err)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("no-session-id path took %v, want < 50ms", elapsed)
	}
}

func TestTryAutoAttach_EnvOptOut(t *testing.T) {
	t.Setenv("PYRY_NO_AUTO_ATTACH", "1")

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")
	stop := startHasIDStub(t, sock, true, "")
	defer stop()

	handled, err := tryAutoAttach(sock, []string{"--session-id", canonicalUUID})
	if handled || err != nil {
		t.Fatalf("got (handled=%v, err=%v), want (false, nil) under PYRY_NO_AUTO_ATTACH=1",
			handled, err)
	}
}

func TestTryAutoAttach_EnvOptOutNonOne(t *testing.T) {
	// Strict-"1" semantics: any other value (including "true") still
	// probes. Documented convention; matches GODEBUG / GOTRACEBACK style.
	t.Setenv("PYRY_NO_AUTO_ATTACH", "true")

	dir := shortTempDir(t)
	// Socket is absent → probe falls through, but the *probe runs*. We
	// can't observe "the probe ran" directly without a stub, so this
	// test asserts the absent-socket fall-through still returns
	// (false, nil) with the env value not equal to "1" — ie. the env
	// gate didn't short-circuit.
	sock := filepath.Join(dir, "absent.sock")

	handled, err := tryAutoAttach(sock, []string{"--session-id", canonicalUUID})
	if handled || err != nil {
		t.Fatalf("got (handled=%v, err=%v), want (false, nil)", handled, err)
	}
}

func TestTryAutoAttach_SocketAbsent_FastPath(t *testing.T) {
	t.Setenv("PYRY_NO_AUTO_ATTACH", "")

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "absent.sock")

	start := time.Now()
	handled, err := tryAutoAttach(sock, []string{"--session-id", canonicalUUID})
	elapsed := time.Since(start)

	if handled || err != nil {
		t.Fatalf("got (handled=%v, err=%v), want (false, nil)", handled, err)
	}
	// AC#3: <50ms in the no-daemon case. Real numbers are sub-ms; the
	// 50ms ceiling is the contract, not the steady-state expectation.
	if elapsed > 50*time.Millisecond {
		t.Errorf("ENOENT fast path took %v, want < 50ms", elapsed)
	}
}

func TestTryAutoAttach_DaemonUnresponsive(t *testing.T) {
	t.Setenv("PYRY_NO_AUTO_ATTACH", "")

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	// Listen but never accept — connections queue (or stall) and the
	// 1s probe context bounds the wait.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	start := time.Now()
	handled, err := tryAutoAttach(sock, []string{"--session-id", canonicalUUID})
	elapsed := time.Since(start)

	if handled || err != nil {
		t.Fatalf("got (handled=%v, err=%v), want (false, nil)", handled, err)
	}
	// 1s probe ctx + small slack for goroutine scheduling.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("unresponsive-daemon path took %v, want ≤ ~1.5s", elapsed)
	}
}

func TestTryAutoAttach_HasIDFalse(t *testing.T) {
	t.Setenv("PYRY_NO_AUTO_ATTACH", "")

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")
	stop := startHasIDStub(t, sock, false, "")
	defer stop()

	handled, err := tryAutoAttach(sock, []string{"--session-id", canonicalUUID})
	if handled || err != nil {
		t.Fatalf("got (handled=%v, err=%v), want (false, nil) when has-id returns false",
			handled, err)
	}
}

func TestTryAutoAttach_HasIDInvalid(t *testing.T) {
	t.Setenv("PYRY_NO_AUTO_ATTACH", "")

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")
	stop := startHasIDStub(t, sock, false, "sessions.has-id: invalid uuid")
	defer stop()

	handled, err := tryAutoAttach(sock, []string{"--session-id", "not-a-uuid"})
	if handled || err != nil {
		t.Fatalf("got (handled=%v, err=%v), want (false, nil) when has-id errors on malformed input",
			handled, err)
	}
}
