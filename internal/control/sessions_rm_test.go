package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestServer_SessionsRm_Success exercises the success path: the fake
// sessioner returns nil, the client returns nil, and the fake records the
// request's id and JSONL policy translated to the internal enum.
func TestServer_SessionsRm_Success(t *testing.T) {
	t.Parallel()

	const cannedID = "11111111-2222-3333-4444-555555555555"
	sessioner := &fakeSessioner{}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	if err := SessionsRm(context.Background(), sock, cannedID, JSONLPolicyArchive); err != nil {
		t.Fatalf("SessionsRm: %v", err)
	}
	got := sessioner.recordedRemoves()
	if len(got) != 1 {
		t.Fatalf("recordedRemoves = %v, want exactly one entry", got)
	}
	if got[0].ID != sessions.SessionID(cannedID) {
		t.Errorf("ID = %q, want %q", got[0].ID, cannedID)
	}
	if got[0].Opts.JSONL != sessions.JSONLArchive {
		t.Errorf("Opts.JSONL = %v, want JSONLArchive", got[0].Opts.JSONL)
	}
}

// TestServer_SessionsRm_PolicyEachValue covers each JSONLPolicy wire value,
// including the empty string which must map to JSONLLeave (matching
// sessions.JSONLPolicy's zero-value default).
func TestServer_SessionsRm_PolicyEachValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		wire JSONLPolicy
		want sessions.JSONLPolicy
	}{
		{"empty defaults to leave", "", sessions.JSONLLeave},
		{"explicit leave", JSONLPolicyLeave, sessions.JSONLLeave},
		{"archive", JSONLPolicyArchive, sessions.JSONLArchive},
		{"purge", JSONLPolicyPurge, sessions.JSONLPurge},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sessioner := &fakeSessioner{}
			sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
			defer stop()

			const id = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
			if err := SessionsRm(context.Background(), sock, id, tc.wire); err != nil {
				t.Fatalf("SessionsRm: %v", err)
			}
			got := sessioner.recordedRemoves()
			if len(got) != 1 {
				t.Fatalf("recordedRemoves = %v, want exactly one entry", got)
			}
			if got[0].Opts.JSONL != tc.want {
				t.Errorf("Opts.JSONL = %v, want %v", got[0].Opts.JSONL, tc.want)
			}
		})
	}
}

// TestServer_SessionsRm_ErrSessionNotFound verifies typed-error propagation:
// the server detects ErrSessionNotFound, sets ErrorCode, and the client
// reconstructs the bare sentinel so callers can errors.Is against it.
func TestServer_SessionsRm_ErrSessionNotFound(t *testing.T) {
	t.Parallel()

	sessioner := &fakeSessioner{returnErr: sessions.ErrSessionNotFound}
	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	err := SessionsRm(context.Background(), sock, "deadbeef-dead-beef-dead-beefdeadbeef", JSONLPolicyLeave)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Errorf("errors.Is(err, ErrSessionNotFound) = false, err = %v", err)
	}
	if !strings.Contains(err.Error(), "sessions: session not found") {
		t.Errorf("err.Error() = %q, want it to contain %q", err.Error(), "sessions: session not found")
	}
}

// TestServer_SessionsRm_ErrCannotRemoveBootstrap mirrors the previous test
// for the second typed sentinel.
func TestServer_SessionsRm_ErrCannotRemoveBootstrap(t *testing.T) {
	t.Parallel()

	sessioner := &fakeSessioner{returnErr: sessions.ErrCannotRemoveBootstrap}
	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	err := SessionsRm(context.Background(), sock, "bootstrap-id", JSONLPolicyLeave)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sessions.ErrCannotRemoveBootstrap) {
		t.Errorf("errors.Is(err, ErrCannotRemoveBootstrap) = false, err = %v", err)
	}
	if !strings.Contains(err.Error(), "cannot remove bootstrap session") {
		t.Errorf("err.Error() = %q, want it to mention bootstrap", err.Error())
	}
}

// TestServer_SessionsRm_OtherPoolError exercises an untyped error path: the
// inner message must round-trip verbatim and the sentinels must NOT match.
func TestServer_SessionsRm_OtherPoolError(t *testing.T) {
	t.Parallel()

	const innerMsg = "sessions: persist registry: simulated"
	sessioner := &fakeSessioner{returnErr: errors.New(innerMsg)}
	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	err := SessionsRm(context.Background(), sock, "some-id", JSONLPolicyPurge)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != innerMsg {
		t.Errorf("err = %q, want verbatim %q", err.Error(), innerMsg)
	}
	if errors.Is(err, sessions.ErrSessionNotFound) {
		t.Error("errors.Is(err, ErrSessionNotFound) = true, want false")
	}
	if errors.Is(err, sessions.ErrCannotRemoveBootstrap) {
		t.Error("errors.Is(err, ErrCannotRemoveBootstrap) = true, want false")
	}
}

// TestServer_SessionsRm_NoSessionerConfigured covers the nil-Sessioner
// branch — the diagnostic message follows the "sessions.rm: " prefix
// convention used by the other server-side guards.
func TestServer_SessionsRm_NoSessionerConfigured(t *testing.T) {
	t.Parallel()

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, nil)
	defer stop()

	err := SessionsRm(context.Background(), sock, "ignored", JSONLPolicyLeave)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "sessions.rm: no sessioner configured"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestServer_SessionsRm_MissingID covers the empty-ID guard: the server
// must reject before calling the sessioner so the fake records zero
// Remove calls.
func TestServer_SessionsRm_MissingID(t *testing.T) {
	t.Parallel()

	sessioner := &fakeSessioner{}
	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	err := SessionsRm(context.Background(), sock, "", JSONLPolicyLeave)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "sessions.rm: missing id"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if got := sessioner.recordedRemoves(); len(got) != 0 {
		t.Errorf("recordedRemoves = %v, want zero entries (guard fired before seam call)", got)
	}
}

// TestServer_SessionsRm_BadPolicy covers the unknown-policy guard. The
// client wrapper takes a typed JSONLPolicy, but JSONLPolicy is a string
// newtype so a hand-rolled bogus value is reachable from any caller.
func TestServer_SessionsRm_BadPolicy(t *testing.T) {
	t.Parallel()

	sessioner := &fakeSessioner{}
	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	err := SessionsRm(context.Background(), sock, "some-id", JSONLPolicy("bogus"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = `sessions.rm: unknown jsonl policy "bogus"`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if got := sessioner.recordedRemoves(); len(got) != 0 {
		t.Errorf("recordedRemoves = %v, want zero entries (guard fired before seam call)", got)
	}
}

// TestSessionsRm_PassesArgsOnWire pins the wire shape produced by the
// client-side SessionsRm helper. Mirrors TestSessionsNew_PassesLabelOnWire.
func TestSessionsRm_PassesArgsOnWire(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		id        string
		policy    JSONLPolicy
		wantBytes string
	}{
		{
			name:      "archive policy",
			id:        "11111111-1111-1111-1111-111111111111",
			policy:    JSONLPolicyArchive,
			wantBytes: `"sessions":{"id":"11111111-1111-1111-1111-111111111111","jsonlPolicy":"archive"}`,
		},
		{
			name:      "explicit leave policy",
			id:        "22222222-2222-2222-2222-222222222222",
			policy:    JSONLPolicyLeave,
			wantBytes: `"sessions":{"id":"22222222-2222-2222-2222-222222222222","jsonlPolicy":"leave"}`,
		},
		{
			name:      "empty policy drops via omitempty",
			id:        "33333333-3333-3333-3333-333333333333",
			policy:    "",
			wantBytes: `"sessions":{"id":"33333333-3333-3333-3333-333333333333"}`,
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
			if err := SessionsRm(ctx, sock, tc.id, tc.policy); err != nil {
				t.Fatalf("SessionsRm: %v", err)
			}

			select {
			case line := <-gotLine:
				if !bytes.Contains(line, []byte(`"verb":"sessions.rm"`)) {
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

// TestSessionsRm_DecodesEmptyResponseAsError is a defensive client-shape
// check: a server response with neither Error nor OK populated must
// surface a clean client error rather than silently succeeding.
func TestSessionsRm_DecodesEmptyResponseAsError(t *testing.T) {
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
	err = SessionsRm(ctx, sock, "some-id", JSONLPolicyLeave)
	if err == nil {
		t.Fatal("expected error from empty server response")
	}
	if !strings.Contains(err.Error(), "missing ok flag") {
		t.Errorf("err = %q, want it to mention \"missing ok flag\"", err.Error())
	}
}
