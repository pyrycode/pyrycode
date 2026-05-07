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

// hasIDResolver is a SessionResolver that drives Lookup from a present
// map: ids in the map return (sess, nil); ids absent from the map
// return (nil, ErrSessionNotFound). Records every Lookup call so tests
// can assert the handler reached (or did not reach) the seam.
//
// If lookupErr is set, every Lookup returns it verbatim — used to pin
// the defensive non-ErrSessionNotFound branch in handleSessionsHasID.
//
// Defined here, in the test file, rather than extending the production
// fakeResolver: this verb is the only one with a present/absent split,
// and reshaping the shared fake risked drift in tests it wasn't
// designed for. Mirrors the architect note in #157's design doc.
type hasIDResolver struct {
	sess      Session
	present   map[sessions.SessionID]bool
	lookupErr error

	mu          sync.Mutex
	lookupCalls []sessions.SessionID
}

func (r *hasIDResolver) Lookup(id sessions.SessionID) (Session, error) {
	r.mu.Lock()
	r.lookupCalls = append(r.lookupCalls, id)
	r.mu.Unlock()
	if r.lookupErr != nil {
		return nil, r.lookupErr
	}
	if r.present[id] {
		return r.sess, nil
	}
	return nil, sessions.ErrSessionNotFound
}

func (r *hasIDResolver) ResolveID(arg string) (sessions.SessionID, error) {
	return sessions.SessionID(arg), nil
}

func (r *hasIDResolver) recordedLookups() []sessions.SessionID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessions.SessionID(nil), r.lookupCalls...)
}

// Two canonical UUIDv4 strings (sessions.ValidID-clean).
const (
	hasIDPresentUUID = "11111111-2222-4333-8444-555555555555"
	hasIDAbsentUUID  = "22222222-3333-4555-9666-777777777777"
)

// TestServer_SessionsHasID_Present exercises the present-id success
// path: the resolver answers Lookup(known) with a session; the client
// receives Has: true and the resolver records exactly one Lookup with
// the known SessionID.
func TestServer_SessionsHasID_Present(t *testing.T) {
	t.Parallel()

	resolver := &hasIDResolver{
		sess:    &fakeSession{},
		present: map[sessions.SessionID]bool{hasIDPresentUUID: true},
	}
	sock, stop := startServerWithSessioner(t, resolver, nil)
	defer stop()

	got, err := SessionsHasID(context.Background(), sock, hasIDPresentUUID)
	if err != nil {
		t.Fatalf("SessionsHasID: %v", err)
	}
	if !got {
		t.Errorf("SessionsHasID = false, want true")
	}
	calls := resolver.recordedLookups()
	if len(calls) != 1 || calls[0] != hasIDPresentUUID {
		t.Errorf("Lookup calls = %v, want exactly [%q]", calls, hasIDPresentUUID)
	}
}

// TestServer_SessionsHasID_Absent pins the AC's "well-formed but absent
// → false, not error" contract: the resolver returns
// ErrSessionNotFound and the client receives Has: false with no error.
func TestServer_SessionsHasID_Absent(t *testing.T) {
	t.Parallel()

	resolver := &hasIDResolver{
		sess:    &fakeSession{},
		present: map[sessions.SessionID]bool{}, // empty — every id absent
	}
	sock, stop := startServerWithSessioner(t, resolver, nil)
	defer stop()

	got, err := SessionsHasID(context.Background(), sock, hasIDAbsentUUID)
	if err != nil {
		t.Fatalf("SessionsHasID: %v", err)
	}
	if got {
		t.Errorf("SessionsHasID = true, want false")
	}
	calls := resolver.recordedLookups()
	if len(calls) != 1 || calls[0] != hasIDAbsentUUID {
		t.Errorf("Lookup calls = %v, want exactly [%q]", calls, hasIDAbsentUUID)
	}
}

// TestServer_SessionsHasID_MissingID covers the boundary check: a
// request with no payload, and a request with an explicitly empty ID,
// must both return the "missing id" diagnostic without consulting the
// resolver.
func TestServer_SessionsHasID_MissingID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		req  Request
	}{
		{
			name: "no payload",
			req:  Request{Verb: VerbSessionsHasID},
		},
		{
			name: "empty ID payload",
			req:  Request{Verb: VerbSessionsHasID, Sessions: &SessionsPayload{}},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := &hasIDResolver{sess: &fakeSession{}}
			sock, stop := startServerWithSessioner(t, resolver, nil)
			defer stop()

			resp, err := rawRequest(t, sock, tc.req)
			if err != nil {
				t.Fatalf("rawRequest: %v", err)
			}
			const want = "sessions.has-id: missing id"
			if resp.Error != want {
				t.Errorf("Response.Error = %q, want %q", resp.Error, want)
			}
			if resp.SessionsHasID != nil {
				t.Errorf("Response.SessionsHasID = %+v, want nil", resp.SessionsHasID)
			}
			if calls := resolver.recordedLookups(); len(calls) != 0 {
				t.Errorf("Lookup calls = %v, want exactly [] (boundary fail-fast)", calls)
			}
		})
	}
}

// TestServer_SessionsHasID_InvalidUUID covers the strict-UUID
// validation gate: a non-UUIDv4 id surfaces as "invalid uuid" without
// reaching the resolver.
func TestServer_SessionsHasID_InvalidUUID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
	}{
		{name: "obviously bogus", id: "not-a-uuid"},
		{name: "wrong-length-but-shaped", id: "11111111-2222-3333-4444-XXXXXXXXXXXX"},
		{name: "v3-not-v4", id: "11111111-2222-3333-8444-555555555555"},
		{name: "wrong variant", id: "11111111-2222-4333-c444-555555555555"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := &hasIDResolver{sess: &fakeSession{}}
			sock, stop := startServerWithSessioner(t, resolver, nil)
			defer stop()

			got, err := SessionsHasID(context.Background(), sock, tc.id)
			if err == nil {
				t.Fatalf("expected error, got %v", got)
			}
			const want = "sessions.has-id: invalid uuid"
			if err.Error() != want {
				t.Errorf("err = %q, want %q", err.Error(), want)
			}
			if calls := resolver.recordedLookups(); len(calls) != 0 {
				t.Errorf("Lookup calls = %v, want exactly [] (boundary fail-fast)", calls)
			}
		})
	}
}

// TestServer_SessionsHasID_LookupError pins the defensive branch:
// Pool.Lookup never returns a non-ErrSessionNotFound error today, but
// if it ever does, the handler must surface it as
// "sessions.has-id: <err>" rather than answering Has: false.
func TestServer_SessionsHasID_LookupError(t *testing.T) {
	t.Parallel()

	resolver := &hasIDResolver{
		sess:      &fakeSession{},
		lookupErr: errors.New("boom"),
	}
	sock, stop := startServerWithSessioner(t, resolver, nil)
	defer stop()

	_, err := SessionsHasID(context.Background(), sock, hasIDPresentUUID)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "sessions.has-id: boom"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestClient_SessionsHasID_Wire pins the wire bytes the client emits:
// exactly {"verb":"sessions.has-id","sessions":{"id":"<uuid>"}}\n.
// Confirms Label / JSONLPolicy / NewLabel all elide via omitempty.
func TestClient_SessionsHasID_Wire(t *testing.T) {
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
		_ = json.NewEncoder(conn).Encode(Response{
			SessionsHasID: &SessionsHasIDResult{Has: true},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SessionsHasID(ctx, sock, hasIDPresentUUID); err != nil {
		t.Fatalf("SessionsHasID: %v", err)
	}

	select {
	case line := <-gotLine:
		want := `{"verb":"sessions.has-id","sessions":{"id":"` + hasIDPresentUUID + `"}}` + "\n"
		if string(line) != want {
			t.Errorf("wire bytes = %q, want %q", line, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive a request")
	}
}

// TestClient_SessionsHasID_EmptyResponse pins the meaningful empty-
// response guard: a Response{} (no SessionsHasID, no Error) is a
// daemon contract violation and surfaces as a clean client error.
func TestClient_SessionsHasID_EmptyResponse(t *testing.T) {
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
		_ = json.NewEncoder(conn).Encode(Response{}) // no Error, no SessionsHasID
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = SessionsHasID(ctx, sock, hasIDPresentUUID)
	if err == nil {
		t.Fatal("expected error from empty server response")
	}
	if !strings.Contains(err.Error(), "empty sessions.has-id response") {
		t.Errorf("err = %q, want it to mention \"empty sessions.has-id response\"", err.Error())
	}
}

// TestProtocol_SessionsHasIDResult_HasNotOmitempty pins the load-
// bearing not-omitempty design choice: marshalling
// SessionsHasIDResult{Has: false} must produce {"has":false}, not {}.
// Otherwise an absent-id response is indistinguishable from a malformed
// "field forgot to populate" response.
func TestProtocol_SessionsHasIDResult_HasNotOmitempty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		v    SessionsHasIDResult
		want string
	}{
		{name: "Has=false emits explicitly", v: SessionsHasIDResult{Has: false}, want: `{"has":false}`},
		{name: "Has=true emits explicitly", v: SessionsHasIDResult{Has: true}, want: `{"has":true}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("Marshal = %s, want %s", got, tc.want)
			}
		})
	}
}

// rawRequest dials the socket, sends one request, decodes one
// response. Lets MissingID assert the server's verbatim error string
// without going through the client wrapper's "control: empty"
// indirection.
func rawRequest(t *testing.T, socketPath string, req Request) (*Response, error) {
	t.Helper()
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
