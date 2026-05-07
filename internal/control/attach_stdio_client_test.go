package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAttachStdio_ByteForwarding asserts that AttachStdio is a transparent
// byte conduit: every byte written to `in` lands at the server verbatim,
// including bytes that would trigger PTY-mode's escape-key state machine
// (`Ctrl-B d`) and binary/NUL bytes that copyWithEscape's 1-byte loop has
// no business filtering. Pins AC#3 — "no translation".
func TestAttachStdio_ByteForwarding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []byte
	}{
		{"plain ascii", []byte("hello world")},
		{"stream-json line", []byte(`{"type":"user","content":"hi"}` + "\n")},
		{"escape-key sequence is NOT consumed", []byte("abc\x02d\x02defg")},
		{"binary-safe (NUL, high bytes)", []byte{0x00, 0x01, 0x02, 0xff, 0x7f, 0x80}},
		{"empty input", []byte{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := shortTempDir(t)
			sock := filepath.Join(dir, "p.sock")

			provider := &fakeAttachProvider{}
			srv := NewServer(sock, sessionResolverWith(provider.Attach), nil, nil, nil, nil)
			if err := srv.Listen(); err != nil {
				t.Fatalf("Listen: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = srv.Serve(ctx) }()

			var out bytes.Buffer
			err := AttachStdio(context.Background(), sock, "", bytes.NewReader(tt.input), &out, false)
			if err != nil {
				t.Fatalf("AttachStdio: %v", err)
			}

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if bytes.Equal(provider.received(), tt.input) {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Errorf("provider received %q, want %q", provider.received(), tt.input)
		})
	}
}

// echoAttachProvider's Attach goroutine copies in → out, so any byte the
// client sends comes back verbatim. The natural client-writes-before-
// server-writes ordering avoids racing with the JSON ack on the conn.
type echoAttachProvider struct{}

func (echoAttachProvider) Attach(in io.Reader, out io.Writer) (<-chan struct{}, error) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(out, in)
	}()
	return done, nil
}

// TestAttachStdio_ServerToClientStream asserts the server → client byte
// path: bytes the supervisor writes to its end of the bridge appear at
// AttachStdio's `out` verbatim. Drives this through an echoing provider
// so the test controls the timing — server output arrives only after the
// client has written into the conn, which in turn happens only after the
// JSON ack has been decoded.
func TestAttachStdio_ServerToClientStream(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	srv := NewServer(sock, sessionResolverWith(echoAttachProvider{}.Attach), nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	pr, pw := io.Pipe()

	out := newSyncBuffer()
	doneAttach := make(chan error, 1)
	go func() { doneAttach <- AttachStdio(context.Background(), sock, "", pr, out, false) }()

	want := []byte("from-server-to-client\x00\x01\xff")
	if _, err := pw.Write(want); err != nil {
		t.Fatalf("pw.Write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Equal(out.bytes(), want) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !bytes.Equal(out.bytes(), want) {
		t.Errorf("out = %q, want %q", out.bytes(), want)
	}

	// Closing `pw` EOFs `in` → AttachStdio shuts down cleanly.
	_ = pw.Close()
	select {
	case err := <-doneAttach:
		if err != nil {
			t.Errorf("AttachStdio returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AttachStdio did not return after closing in")
	}
}

// TestAttachStdio_EOFReturnsNil asserts that EOF on `in` returns a nil
// error (clean detach) and that the output goroutine has joined before
// the function returns (no leak).
func TestAttachStdio_EOFReturnsNil(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	provider := &fakeAttachProvider{}
	srv := NewServer(sock, sessionResolverWith(provider.Attach), nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	in := bytes.NewReader([]byte("payload"))
	var out bytes.Buffer
	if err := AttachStdio(context.Background(), sock, "", in, &out, false); err != nil {
		t.Fatalf("AttachStdio: %v", err)
	}
}

// TestAttachStdio_ServerHangupReturnsNil asserts that an immediate
// server-side close (after ack) surfaces as a nil return when the client
// has nothing to write — mirrors writerErr's contract for the conn-EOF
// case where there is no in-flight byte to fail on.
func TestAttachStdio_ServerHangupReturnsNil(t *testing.T) {
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
		var req Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(conn).Encode(Response{OK: true})
		// Hang up immediately.
		_ = conn.Close()
	}()

	// `in` is empty — io.Copy returns (0, nil) → AttachStdio returns nil
	// once the output goroutine joins (server hangup wakes it).
	in := bytes.NewReader(nil)
	var out bytes.Buffer
	if err := AttachStdio(context.Background(), sock, "", in, &out, false); err != nil {
		t.Errorf("AttachStdio after server hangup = %v, want nil", err)
	}
}

// TestAttachStdio_AckErrorPropagates asserts that a Response.Error from
// the ack surfaces verbatim — including the foreground-mode wire string,
// which is the byte-identical contract the PTY-mode path also honors
// (TestServer_AttachOnForegroundSession).
func TestAttachStdio_AckErrorPropagates(t *testing.T) {
	t.Parallel()

	const want = "attach: no attach provider configured (daemon may be in foreground mode)"

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
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(Response{Error: want})
	}()

	err = AttachStdio(context.Background(), sock, "", bytes.NewReader(nil), io.Discard, false)
	if err == nil || err.Error() != want {
		t.Errorf("AttachStdio err = %v, want %q", err, want)
	}
}

// TestAttachStdio_AckMissingOK asserts the parity-with-Attach error for
// a malformed ack (OK=false, Error="").
func TestAttachStdio_AckMissingOK(t *testing.T) {
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
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(Response{}) // OK=false, Error=""
	}()

	err = AttachStdio(context.Background(), sock, "", bytes.NewReader(nil), io.Discard, false)
	if err == nil || err.Error() != "control: attach ack missing" {
		t.Errorf("AttachStdio err = %v, want \"control: attach ack missing\"", err)
	}
}

// TestAttachStdio_DialError asserts that a missing socket surfaces as a
// wrapped dial error.
func TestAttachStdio_DialError(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "does-not-exist.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := AttachStdio(ctx, sock, "", bytes.NewReader(nil), io.Discard, false)
	if err == nil {
		t.Fatal("expected dial error, got nil")
	}
	if !strings.Contains(err.Error(), "dial") {
		t.Errorf("err = %v, want a wrapped dial error", err)
	}
}

// TestAttachStdio_SessionIDOnWire pins that the selector flows through
// AttachStdio's handshake verbatim, mirroring TestAttach_ClientSendsSessionID.
// Also asserts Cols/Rows are zero on the wire — the "no geometry" promise.
func TestAttachStdio_SessionIDOnWire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string
	}{
		{"empty selector → bootstrap", ""},
		{"full UUID flows through verbatim", "11111111-2222-3333-4444-555555555555"},
		{"prefix flows through verbatim", "1111"},
		{"whitespace-only flows through (server lints)", " "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := shortTempDir(t)
			sock := filepath.Join(dir, "p.sock")

			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer func() { _ = ln.Close() }()

			gotReq := make(chan Request, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				var req Request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				gotReq <- req
				_ = json.NewEncoder(conn).Encode(Response{Error: "test: short-circuit"})
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = AttachStdio(ctx, sock, tt.sessionID, bytes.NewReader(nil), io.Discard, false)
			if err == nil || !strings.Contains(err.Error(), "test: short-circuit") {
				t.Fatalf("AttachStdio: want short-circuit error, got %v", err)
			}

			select {
			case req := <-gotReq:
				if req.Verb != VerbAttach {
					t.Errorf("Verb = %q, want %q", req.Verb, VerbAttach)
				}
				if req.Attach == nil {
					t.Fatalf("Attach payload missing")
				}
				if req.Attach.SessionID != tt.sessionID {
					t.Errorf("SessionID = %q, want %q", req.Attach.SessionID, tt.sessionID)
				}
				if req.Attach.Cols != 0 || req.Attach.Rows != 0 {
					t.Errorf("Cols/Rows = %d/%d, want 0/0 (no geometry promise)",
						req.Attach.Cols, req.Attach.Rows)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server did not receive a request")
			}
		})
	}
}

// TestAttachStdio_NoGeometryOnWire is the byte-shape companion to
// TestAttach_EmptySessionIDOmittedOnWire: omitempty must keep `cols` and
// `rows` off the wire entirely, so a stdio handshake is byte-indistinguishable
// from a v0.5.x client (which doesn't know the fields). A future change
// that sets Cols=0 explicitly via a struct literal would still pass an
// `encoded.Cols == 0` check but break the wire-bytes promise.
func TestAttachStdio_NoGeometryOnWire(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	gotRaw := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		gotRaw <- append([]byte(nil), buf[:n]...)
		_ = json.NewEncoder(conn).Encode(Response{Error: "test: short-circuit"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = AttachStdio(ctx, sock, "", bytes.NewReader(nil), io.Discard, false)

	select {
	case raw := <-gotRaw:
		if bytes.Contains(raw, []byte(`"cols"`)) {
			t.Errorf("cols leaked onto the wire: %s", raw)
		}
		if bytes.Contains(raw, []byte(`"rows"`)) {
			t.Errorf("rows leaked onto the wire: %s", raw)
		}
		if bytes.Contains(raw, []byte("sessionID")) {
			t.Errorf("empty SessionID leaked onto the wire: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive bytes")
	}
}

// TestAttachStdio_InReadErrorPropagates asserts that a non-EOF read error
// on `in` propagates wrapped — distinguishes "stdin EOFed" (clean) from
// "stdin pipe broke" (real failure).
func TestAttachStdio_InReadErrorPropagates(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	provider := &fakeAttachProvider{}
	srv := NewServer(sock, sessionResolverWith(provider.Attach), nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	want := errors.New("synthetic stdin read failure")
	err := AttachStdio(context.Background(), sock, "", &errReader{err: want}, io.Discard, false)
	if err == nil || !errors.Is(err, want) {
		t.Errorf("AttachStdio err = %v, want errors.Is == %v", err, want)
	}
}

// syncBuffer is a bytes.Buffer guarded by a mutex so the test can read
// it from one goroutine while AttachStdio's output goroutine writes from
// another — the race detector flags concurrent bytes.Buffer access
// otherwise.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{} }

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}
