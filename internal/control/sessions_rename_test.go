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

// TestServer_SessionsRename_Success exercises the success path: the fake
// sessioner returns nil, the client returns nil, and the fake records the
// id and newLabel passed unchanged.
func TestServer_SessionsRename_Success(t *testing.T) {
	t.Parallel()

	const cannedID = "11111111-2222-3333-4444-555555555555"
	sessioner := &fakeSessioner{}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	if err := SessionsRename(context.Background(), sock, cannedID, "new-label"); err != nil {
		t.Fatalf("SessionsRename: %v", err)
	}
	got := sessioner.recordedRenames()
	if len(got) != 1 {
		t.Fatalf("recordedRenames = %v, want exactly one entry", got)
	}
	if got[0].ID != sessions.SessionID(cannedID) {
		t.Errorf("ID = %q, want %q", got[0].ID, cannedID)
	}
	if got[0].NewLabel != "new-label" {
		t.Errorf("NewLabel = %q, want %q", got[0].NewLabel, "new-label")
	}
}

// TestServer_SessionsRename_EmptyNewLabel verifies the empty-newLabel
// semantics: an empty newLabel flows through verbatim (omitempty on the
// wire, "" decoded server-side, "" forwarded to Pool.Rename's "clear the
// label" contract).
func TestServer_SessionsRename_EmptyNewLabel(t *testing.T) {
	t.Parallel()

	const cannedID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	sessioner := &fakeSessioner{}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	if err := SessionsRename(context.Background(), sock, cannedID, ""); err != nil {
		t.Fatalf("SessionsRename: %v", err)
	}
	got := sessioner.recordedRenames()
	if len(got) != 1 {
		t.Fatalf("recordedRenames = %v, want exactly one entry", got)
	}
	if got[0].NewLabel != "" {
		t.Errorf("NewLabel = %q, want empty string (forwarded unchanged)", got[0].NewLabel)
	}
}

// TestServer_SessionsRename_ErrSessionNotFound verifies typed-error
// propagation: the server detects ErrSessionNotFound, sets ErrorCode, and
// the client reconstructs the bare sentinel so callers can errors.Is
// against it.
func TestServer_SessionsRename_ErrSessionNotFound(t *testing.T) {
	t.Parallel()

	sessioner := &fakeSessioner{returnErr: sessions.ErrSessionNotFound}
	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	err := SessionsRename(context.Background(), sock, "deadbeef-dead-beef-dead-beefdeadbeef", "new")
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

// TestServer_SessionsRename_OtherPoolError exercises an untyped error
// path: the inner message must round-trip verbatim and the not-found
// sentinel must NOT match.
func TestServer_SessionsRename_OtherPoolError(t *testing.T) {
	t.Parallel()

	const innerMsg = "sessions: persist registry: simulated"
	sessioner := &fakeSessioner{returnErr: errors.New(innerMsg)}
	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	err := SessionsRename(context.Background(), sock, "some-id", "new")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != innerMsg {
		t.Errorf("err = %q, want verbatim %q", err.Error(), innerMsg)
	}
	if errors.Is(err, sessions.ErrSessionNotFound) {
		t.Error("errors.Is(err, ErrSessionNotFound) = true, want false")
	}
}

// TestServer_SessionsRename_NoSessionerConfigured covers the
// nil-Sessioner branch — the diagnostic message follows the
// "sessions.rename: " prefix convention used by the other server-side
// guards.
func TestServer_SessionsRename_NoSessionerConfigured(t *testing.T) {
	t.Parallel()

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, nil)
	defer stop()

	err := SessionsRename(context.Background(), sock, "ignored", "ignored")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "sessions.rename: no sessioner configured"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestServer_SessionsRename_MissingID covers the empty-ID guard: the
// server must reject before calling the sessioner so the fake records
// zero Rename calls.
func TestServer_SessionsRename_MissingID(t *testing.T) {
	t.Parallel()

	sessioner := &fakeSessioner{}
	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	err := SessionsRename(context.Background(), sock, "", "new")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "sessions.rename: missing id"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	if got := sessioner.recordedRenames(); len(got) != 0 {
		t.Errorf("recordedRenames = %v, want zero entries (guard fired before seam call)", got)
	}
}

// TestSessionsRename_PassesArgsOnWire pins the wire shape produced by
// the client-side SessionsRename helper. Mirrors
// TestSessionsRm_PassesArgsOnWire.
func TestSessionsRename_PassesArgsOnWire(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		id        string
		newLabel  string
		wantBytes string
	}{
		{
			name:      "non-empty newLabel",
			id:        "11111111-1111-1111-1111-111111111111",
			newLabel:  "alpha",
			wantBytes: `"sessions":{"id":"11111111-1111-1111-1111-111111111111","newLabel":"alpha"}`,
		},
		{
			name:      "empty newLabel drops via omitempty",
			id:        "22222222-2222-2222-2222-222222222222",
			newLabel:  "",
			wantBytes: `"sessions":{"id":"22222222-2222-2222-2222-222222222222"}`,
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
			if err := SessionsRename(ctx, sock, tc.id, tc.newLabel); err != nil {
				t.Fatalf("SessionsRename: %v", err)
			}

			select {
			case line := <-gotLine:
				if !bytes.Contains(line, []byte(`"verb":"sessions.rename"`)) {
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

// TestSessionsRename_DecodesEmptyResponseAsError is a defensive
// client-shape check: a server response with neither Error nor OK
// populated must surface a clean client error rather than silently
// succeeding.
func TestSessionsRename_DecodesEmptyResponseAsError(t *testing.T) {
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
	err = SessionsRename(ctx, sock, "some-id", "new")
	if err == nil {
		t.Fatal("expected error from empty server response")
	}
	if !strings.Contains(err.Error(), "missing ok flag") {
		t.Errorf("err = %q, want it to mention \"missing ok flag\"", err.Error())
	}
}
