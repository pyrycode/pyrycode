package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// fakeSessioner satisfies control.Sessioner for tests. Safe under concurrent
// use. Mirrors fakeSession's mu-guarded recorded-call shape.
type fakeSessioner struct {
	mu          sync.Mutex
	createCalls []string // labels in invocation order
	returnID    sessions.SessionID
	returnErr   error
}

func (f *fakeSessioner) Create(_ context.Context, label string) (sessions.SessionID, error) {
	f.mu.Lock()
	f.createCalls = append(f.createCalls, label)
	id, err := f.returnID, f.returnErr
	f.mu.Unlock()
	return id, err
}

func (f *fakeSessioner) recordedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.createCalls...)
}

// startServerWithSessioner mirrors startServer but plumbs a Sessioner. Kept
// separate from startServer so the existing single-call-site invocations in
// other tests don't all need to thread a sessioner argument.
func startServerWithSessioner(t *testing.T, resolver SessionResolver, sessioner Sessioner) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, resolver, nil, nil, nil, sessioner)
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

// TestProtocol_SessionsRoundTripBackCompat is the byte-equality regression
// guard for the omitempty tag on Request.Sessions. Adding the new
// SessionsPayload field must not change the wire output for existing
// verbs — v0.5.x clients (and captured fixtures) keep round-tripping
// byte-identically. Mirrors TestAttach_WireBackCompat_EmptySessionID.
func TestProtocol_SessionsRoundTripBackCompat(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(Request{Verb: VerbStatus})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := []byte(`{"verb":"status"}`)
	if !bytes.Equal(got, want) {
		t.Errorf("Marshal Request{Verb:status} = %q, want %q (omitempty on Request.Sessions is load-bearing)", got, want)
	}
}

// TestServer_SessionsNew_Success exercises the success path: the fake
// sessioner returns a canned UUID, the client decodes it, and the fake
// records the label exactly once.
func TestServer_SessionsNew_Success(t *testing.T) {
	t.Parallel()

	const cannedID sessions.SessionID = "11111111-2222-3333-4444-555555555555"
	sessioner := &fakeSessioner{returnID: cannedID}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	got, err := SessionsNew(context.Background(), sock, "my-label")
	if err != nil {
		t.Fatalf("SessionsNew: %v", err)
	}
	if got != string(cannedID) {
		t.Errorf("SessionID = %q, want %q", got, cannedID)
	}
	calls := sessioner.recordedCalls()
	if len(calls) != 1 || calls[0] != "my-label" {
		t.Errorf("Create calls = %v, want exactly [%q]", calls, "my-label")
	}
}

// TestServer_SessionsNew_EmptyLabel verifies the empty-label semantics: an
// empty label flows through verbatim and the sessioner sees "".
func TestServer_SessionsNew_EmptyLabel(t *testing.T) {
	t.Parallel()

	const cannedID sessions.SessionID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	sessioner := &fakeSessioner{returnID: cannedID}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	got, err := SessionsNew(context.Background(), sock, "")
	if err != nil {
		t.Fatalf("SessionsNew: %v", err)
	}
	if got != string(cannedID) {
		t.Errorf("SessionID = %q, want %q", got, cannedID)
	}
	calls := sessioner.recordedCalls()
	if len(calls) != 1 || calls[0] != "" {
		t.Errorf("Create calls = %v, want exactly [\"\"]", calls)
	}
}

// TestServer_SessionsNew_PoolError verifies that a Pool.Create error is
// surfaced verbatim through Response.Error — the underlying message is
// preserved with no "sessions.new: " prefix wrap.
func TestServer_SessionsNew_PoolError(t *testing.T) {
	t.Parallel()

	const innerMsg = "sessions: create supervisor: pty start: simulated"
	sessioner := &fakeSessioner{returnErr: errors.New(innerMsg)}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	_, err := SessionsNew(context.Background(), sock, "x")
	if err == nil {
		t.Fatal("expected error from SessionsNew, got nil")
	}
	if err.Error() != innerMsg {
		t.Errorf("err = %q, want verbatim %q (no prefix wrap)", err.Error(), innerMsg)
	}
}

// TestServer_SessionsNew_NoSessionerConfigured covers the nil-Sessioner
// branch — the server diagnoses "no sessioner configured" with the
// "sessions.new: " prefix matching the logs/stop precedent.
func TestServer_SessionsNew_NoSessionerConfigured(t *testing.T) {
	t.Parallel()

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, nil)
	defer stop()

	_, err := SessionsNew(context.Background(), sock, "ignored")
	if err == nil {
		t.Fatal("expected error when sessioner is nil")
	}
	const want = "sessions.new: no sessioner configured"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestSessionsNew_PassesLabelOnWire pins the wire shape produced by the
// client-side SessionsNew helper. Mirrors TestSendResize_RoundTrip /
// TestAttach_ClientSendsSessionID — a hand-rolled net.Listen server
// captures the raw bytes (after re-encoding the decoded Request to a
// canonical form) and asserts both the verb and the SessionsPayload.
func TestSessionsNew_PassesLabelOnWire(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		label     string
		wantBytes string // substring expected in the raw client-encoded line
	}{
		{
			name:      "non-empty label",
			label:     "alpha",
			wantBytes: `"sessions":{"label":"alpha"}`,
		},
		{
			name:      "empty label drops via omitempty",
			label:     "",
			wantBytes: `"sessions":{}`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

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
				// Read until newline — json.Encoder writes one
				// trailing \n per Encode.
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
				_ = json.NewEncoder(conn).Encode(Response{
					SessionsNew: &SessionsNewResult{SessionID: "11111111-2222-3333-4444-555555555555"},
				})
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := SessionsNew(ctx, sock, tc.label); err != nil {
				t.Fatalf("SessionsNew: %v", err)
			}

			select {
			case line := <-gotLine:
				if !bytes.Contains(line, []byte(`"verb":"sessions.new"`)) {
					t.Errorf("wire bytes %q missing verb", line)
				}
				if !bytes.Contains(line, []byte(tc.wantBytes)) {
					t.Errorf("wire bytes %q missing %q", line, tc.wantBytes)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server did not receive a request")
			}
		})
	}
}

// TestSessionsNew_DecodesEmptyResponseAsError is a defensive client-shape
// check: a server response with neither Error nor SessionsNew populated
// (or with an empty SessionID inside) must surface a clean client error,
// not silently return "" and let callers think they got a real id.
func TestSessionsNew_DecodesEmptyResponseAsError(t *testing.T) {
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
		_ = json.NewEncoder(conn).Encode(Response{}) // no Error, no SessionsNew
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = SessionsNew(ctx, sock, "x")
	if err == nil {
		t.Fatal("expected error from empty server response")
	}
	if !strings.Contains(err.Error(), "empty sessions.new response") {
		t.Errorf("err = %q, want it to mention \"empty sessions.new response\"", err.Error())
	}
}
