package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// attachCall records one reach into the installed file attacher, so tests can
// assert BOTH what the handler forwarded and — for the guard cases — that it
// never reached the seam at all. The second is the load-bearing half: a guard
// that rejects and then calls anyway would still produce the right wire error.
type attachCall struct {
	sessionID string
	path      string
}

// recordingAttacher is a file attacher that answers with a fixed id (or a
// fixed error) and records every call. Defined here rather than extending a
// shared fake: this is the only verb whose dependency is a bare func, and the
// other fakes in this package are SessionResolver shaped.
type recordingAttacher struct {
	id  string
	err error

	mu    sync.Mutex
	calls []attachCall
}

func (a *recordingAttacher) attach(sessionID, path string) (string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, attachCall{sessionID: sessionID, path: path})
	a.mu.Unlock()
	if a.err != nil {
		return "", a.err
	}
	return a.id, nil
}

func (a *recordingAttacher) recorded() []attachCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]attachCall(nil), a.calls...)
}

// startServerWithAttacher starts a server on a fresh socket with attach
// installed via SetFileAttacher. A nil attach installs nothing, which is the
// inert state this slice ships in and the state v1/foreground stays in.
//
// It does not reuse startServerWithSessioner because that helper does not hand
// back the *Server, and SetFileAttacher must run between NewServer and Serve —
// the same window SetApprovalRegistry documents.
func startServerWithAttacher(t *testing.T, attach func(sessionID, path string) (string, error)) (sock string, stop func()) {
	t.Helper()
	sock = filepath.Join(shortTempDir(t), "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	if attach != nil {
		srv.SetFileAttacher(attach)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	return sock, func() {
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
}

const (
	attachTestSessionID = "33333333-4444-4555-8666-777777777777"
	attachTestMintedID  = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

// TestServer_AttachFile_NoAttacher pins the inert state this slice ships in:
// the verb EXISTS and dispatches, and refuses fail-closed with a Response.Error
// while its destination dependency is unset — the state mcp.approve sat in
// until #1080 wired a resolver.
func TestServer_AttachFile_NoAttacher(t *testing.T) {
	t.Parallel()

	sock, stop := startServerWithAttacher(t, nil)
	defer stop()

	_, err := AttachFile(context.Background(), sock, AttachFilePayload{
		SessionID: attachTestSessionID,
		Path:      "notes.md",
	})
	if err == nil {
		t.Fatal("AttachFile with no attacher = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "no file attacher configured") {
		t.Errorf("error = %q, want it to name the unconfigured attacher", err)
	}
}

// TestServer_AttachFile_Guards covers the payload boundary. Each case must
// answer with the stated diagnostic AND leave the attacher untouched — the
// second assertion is what distinguishes a guard from a refusal the seam
// happened to produce.
//
// The empty-sessionID row is not routine input hygiene: sessions.Pool.Lookup("")
// resolves to the BOOTSTRAP session, so an id defaulted here would file
// claude's bytes under a conversation that never asked for them.
func TestServer_AttachFile_Guards(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload AttachFilePayload
		omit    bool // send Request with a nil AttachFile payload
		want    string
	}{
		{name: "nil payload", omit: true, want: "missing sessionID"},
		{name: "empty session id", payload: AttachFilePayload{Path: "notes.md"}, want: "missing sessionID"},
		{name: "empty path", payload: AttachFilePayload{SessionID: attachTestSessionID}, want: "missing path"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			attacher := &recordingAttacher{id: attachTestMintedID}
			sock, stop := startServerWithAttacher(t, attacher.attach)
			defer stop()

			req := Request{Verb: VerbAttachFile}
			if !tc.omit {
				p := tc.payload
				req.AttachFile = &p
			}
			resp, err := request(context.Background(), sock, req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if !strings.Contains(resp.Error, tc.want) {
				t.Errorf("Response.Error = %q, want it to contain %q", resp.Error, tc.want)
			}
			if resp.AttachFile != nil {
				t.Errorf("Response.AttachFile = %+v, want nil on a refusal", resp.AttachFile)
			}
			if calls := attacher.recorded(); len(calls) != 0 {
				t.Errorf("attacher calls = %v, want none — the guard must reject before the seam", calls)
			}
		})
	}
}

// TestServer_AttachFile_Success pins the whole-round-trip happy path through
// the exported client helper, and that the handler forwards BOTH fields
// verbatim — the session id in particular, since it is the entire destination
// mechanism and a handler that dropped it would still answer plausibly.
func TestServer_AttachFile_Success(t *testing.T) {
	t.Parallel()

	attacher := &recordingAttacher{id: attachTestMintedID}
	sock, stop := startServerWithAttacher(t, attacher.attach)
	defer stop()

	got, err := AttachFile(context.Background(), sock, AttachFilePayload{
		SessionID: attachTestSessionID,
		Path:      "docs/notes.md",
	})
	if err != nil {
		t.Fatalf("AttachFile: %v", err)
	}
	if got.AttachmentID != attachTestMintedID {
		t.Errorf("AttachmentID = %q, want %q", got.AttachmentID, attachTestMintedID)
	}
	calls := attacher.recorded()
	if len(calls) != 1 {
		t.Fatalf("attacher calls = %d, want exactly 1", len(calls))
	}
	if calls[0].sessionID != attachTestSessionID || calls[0].path != "docs/notes.md" {
		t.Errorf("forwarded %+v, want the session id and path verbatim", calls[0])
	}
}

// TestServer_AttachFile_AttacherError pins that a refusal reason reaches the
// caller intact rather than being flattened into a generic failure — the
// contract that makes the reason actionable, which is the whole reason an
// explicit tool call was chosen over a daemon-side sweep.
func TestServer_AttachFile_AttacherError(t *testing.T) {
	t.Parallel()

	attacher := &recordingAttacher{err: errors.New("the path is outside this conversation's workspace")}
	sock, stop := startServerWithAttacher(t, attacher.attach)
	defer stop()

	_, err := AttachFile(context.Background(), sock, AttachFilePayload{
		SessionID: attachTestSessionID,
		Path:      "/etc/passwd",
	})
	if err == nil {
		t.Fatal("AttachFile = nil error, want the attacher's refusal")
	}
	if !strings.Contains(err.Error(), "outside this conversation's workspace") {
		t.Errorf("error = %q, want the attacher's reason carried through", err)
	}
	if !strings.Contains(err.Error(), "attachment.file:") {
		t.Errorf("error = %q, want the verb prefix the other handlers use", err)
	}
}
