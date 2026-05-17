package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRekeyer satisfies control.Rekeyer for tests. Safe under concurrent
// use. Standalone (NOT embedded into fakeSessioner) for symmetry with the
// standalone Rekeyer interface — slice B's *relay.V2SessionManager is a
// different type from *sessions.Pool.
//
// returnErr governs every Rekey call's result (each test owns its own
// fake). block, if non-nil, is received on before returnErr is read,
// letting tests pin client-side ctx-timeout behaviour against a stalled
// server-side call.
type fakeRekeyer struct {
	mu        sync.Mutex
	calls     []string
	returnErr error
	block     <-chan struct{}
}

func (f *fakeRekeyer) Rekey(ctx context.Context, connID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, connID)
	block := f.block
	retErr := f.returnErr
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return retErr
}

func (f *fakeRekeyer) recordedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// startServerWithRekeyer mirrors startServerWithSessioner but installs a
// Rekeyer between NewServer and Listen. resolver is a fakeResolver, kept
// for symmetry with the other helpers — VerbRekey never touches it.
func startServerWithRekeyer(t *testing.T, resolver SessionResolver, rekeyer Rekeyer) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, resolver, nil, nil, nil, nil)
	srv.SetRekeyer(rekeyer)
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
	return sock, stop
}

// TestServer_Rekey_Success exercises the happy path: the fake rekeyer
// returns nil, the client returns nil, and the fake records exactly one
// call with the canned connID.
func TestServer_Rekey_Success(t *testing.T) {
	t.Parallel()

	const cannedID = "conn-abc-123"
	rekeyer := &fakeRekeyer{}

	sock, stop := startServerWithRekeyer(t, &fakeResolver{sess: &fakeSession{}}, rekeyer)
	defer stop()

	if err := Rekey(context.Background(), sock, cannedID); err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	got := rekeyer.recordedCalls()
	if len(got) != 1 || got[0] != cannedID {
		t.Errorf("recordedCalls = %v, want exactly [%q]", got, cannedID)
	}
}

// TestServer_Rekey_ErrConnNotFound verifies typed-error propagation: the
// server detects ErrConnNotFound, sets ErrCodeConnNotFound on the wire,
// and the client reconstructs the bare sentinel so callers can errors.Is
// against it.
func TestServer_Rekey_ErrConnNotFound(t *testing.T) {
	t.Parallel()

	rekeyer := &fakeRekeyer{returnErr: ErrConnNotFound}
	sock, stop := startServerWithRekeyer(t, &fakeResolver{sess: &fakeSession{}}, rekeyer)
	defer stop()

	err := Rekey(context.Background(), sock, "unknown-conn")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrConnNotFound) {
		t.Errorf("errors.Is(err, ErrConnNotFound) = false, err = %v", err)
	}
	if !strings.Contains(err.Error(), "rekey: conn not found") {
		t.Errorf("err.Error() = %q, want it to contain %q", err.Error(), "rekey: conn not found")
	}
}

// TestServer_Rekey_ErrConnNotFound_Wrapped pins that the dispatcher uses
// errors.Is (not ==) when mapping the typed sentinel to the wire code —
// slice B's manager will wrap ErrConnNotFound with package context.
func TestServer_Rekey_ErrConnNotFound_Wrapped(t *testing.T) {
	t.Parallel()

	rekeyer := &fakeRekeyer{returnErr: fmt.Errorf("manager: %w", ErrConnNotFound)}
	sock, stop := startServerWithRekeyer(t, &fakeResolver{sess: &fakeSession{}}, rekeyer)
	defer stop()

	err := Rekey(context.Background(), sock, "unknown-conn")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrConnNotFound) {
		t.Errorf("errors.Is(err, ErrConnNotFound) = false (wrap not respected), err = %v", err)
	}
}

// TestServer_Rekey_NoRekeyerConfigured covers the nil-Rekeyer branch — the
// production state until slice B (#460) lands. The diagnostic must NOT
// carry the typed wire code; errors.Is(err, ErrConnNotFound) must be false.
func TestServer_Rekey_NoRekeyerConfigured(t *testing.T) {
	t.Parallel()

	// Note: SetRekeyer is intentionally never called here.
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("Serve did not return after cancel")
		}
	}()

	err := Rekey(context.Background(), sock, "ignored")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "rekey: no rekeyer configured"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if errors.Is(err, ErrConnNotFound) {
		t.Error("errors.Is(err, ErrConnNotFound) = true, want false (guard, not typed sentinel)")
	}
}

// TestServer_Rekey_MissingConnID covers the empty-connID guard: the server
// must reject before calling the rekeyer so the fake records zero Rekey
// calls.
func TestServer_Rekey_MissingConnID(t *testing.T) {
	t.Parallel()

	rekeyer := &fakeRekeyer{}
	sock, stop := startServerWithRekeyer(t, &fakeResolver{sess: &fakeSession{}}, rekeyer)
	defer stop()

	err := Rekey(context.Background(), sock, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "rekey: missing connID"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if got := rekeyer.recordedCalls(); len(got) != 0 {
		t.Errorf("recordedCalls = %v, want zero entries (guard fired before Rekeyer call)", got)
	}
}

// TestServer_Rekey_OtherError exercises an untyped error path: the inner
// message must round-trip verbatim and the sentinel must NOT match.
func TestServer_Rekey_OtherError(t *testing.T) {
	t.Parallel()

	const innerMsg = "manager: simulated"
	rekeyer := &fakeRekeyer{returnErr: errors.New(innerMsg)}
	sock, stop := startServerWithRekeyer(t, &fakeResolver{sess: &fakeSession{}}, rekeyer)
	defer stop()

	err := Rekey(context.Background(), sock, "some-conn")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != innerMsg {
		t.Errorf("err = %q, want verbatim %q", err.Error(), innerMsg)
	}
	if errors.Is(err, ErrConnNotFound) {
		t.Error("errors.Is(err, ErrConnNotFound) = true, want false")
	}
}

// TestServer_Rekey_ClientCtxTimeout pins that the client respects the
// caller's ctx deadline: a 100ms-deadline ctx against a server-side rekeyer
// that blocks for 2s returns within ~100ms with a deadline/timeout-shaped
// error.
func TestServer_Rekey_ClientCtxTimeout(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	rekeyer := &fakeRekeyer{block: block}
	sock, stop := startServerWithRekeyer(t, &fakeResolver{sess: &fakeSession{}}, rekeyer)
	// defers run LIFO: stop() runs last, after close(block) unblocks the
	// server-side goroutine that handleWG.Wait waits for. Without this
	// ordering the test deadlocks on shutdown.
	defer stop()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := Rekey(ctx, sock, "some-conn")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if elapsed > 1*time.Second {
		t.Errorf("Rekey returned in %v, want under 1s (client did not respect ctx deadline)", elapsed)
	}
}

// TestRekey_PassesConnIDOnWire pins the wire shape produced by the
// client-side Rekey helper. Mirrors TestSessionsRm_PassesArgsOnWire.
func TestRekey_PassesConnIDOnWire(t *testing.T) {
	t.Parallel()

	const id = "conn-aaaa-bbbb"
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	gotLine := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for {
			n, err := conn.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				if bytes.IndexByte(buf, '\n') >= 0 {
					break
				}
			}
			if err != nil {
				break
			}
		}
		gotLine <- buf
		_ = json.NewEncoder(conn).Encode(Response{OK: true})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Rekey(ctx, sock, id); err != nil {
		t.Fatalf("Rekey: %v", err)
	}

	select {
	case line := <-gotLine:
		if !bytes.Contains(line, []byte(`"verb":"rekey"`)) {
			t.Errorf("wire bytes %q missing verb", line)
		}
		if !bytes.Contains(line, []byte(`"rekey":{"connID":"`+id+`"}`)) {
			t.Errorf("wire bytes %q missing rekey payload", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive a request")
	}
}

// TestRekey_DecodesEmptyResponseAsError is a defensive client-shape check:
// a server response with neither Error nor OK populated must surface a
// clean client error rather than silently succeeding.
func TestRekey_DecodesEmptyResponseAsError(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(Response{}) // no Error, no OK
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = Rekey(ctx, sock, "some-conn")
	if err == nil {
		t.Fatal("expected error from empty server response")
	}
	if !strings.Contains(err.Error(), "missing ok flag") {
		t.Errorf("err = %q, want it to mention \"missing ok flag\"", err.Error())
	}
}
