package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeChannelCreator records what handleChannelNew forwards and returns a
// canned result. Safe under concurrent use; each test owns its own instance.
//
// It records (cwd, name) rather than asserting on them internally because the
// property under test is that internal/control does NO path handling — the
// bytes it received must reach the creator unchanged, so the assertion belongs
// in the test that knows what it sent.
type fakeChannelCreator struct {
	mu        sync.Mutex
	calls     []channelCall
	returnID  string
	returnErr error
}

type channelCall struct{ cwd, name string }

func (f *fakeChannelCreator) create(cwd, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, channelCall{cwd: cwd, name: name})
	return f.returnID, f.returnErr
}

func (f *fakeChannelCreator) recordedCalls() []channelCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]channelCall(nil), f.calls...)
}

// startServerWithChannelCreator mirrors startServerWithRekeyer but installs a
// channel creator between NewServer and Listen. A nil create leaves the seam
// uninstalled, which is the production state for a daemon that never wired it.
func startServerWithChannelCreator(t *testing.T, create func(cwd, name string) (string, error)) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	srv.SetChannelCreator(create)
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

// channelRoundTrip dials sock, sends req, and returns the single Response.
func channelRoundTrip(t *testing.T, sock string, req Request) Response {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// TestChannelNew_NoCreatorConfigured pins AC#3's "a daemon that has not
// installed the seam answers with a clear 'not configured' error rather than
// panicking" — the handleRekey idiom.
//
// The payload is deliberately WELL-FORMED. The guard order under test is
// "nil dependency BEFORE payload validation", copied from handleAttachFile, so
// that a caller cannot use the shape of the refusal to work out which
// dependencies a daemon has installed. A test that sent a malformed payload
// here would pass whichever order the handler used.
func TestChannelNew_NoCreatorConfigured(t *testing.T) {
	t.Parallel()

	sock, stop := startServerWithChannelCreator(t, nil)
	defer stop()

	resp := channelRoundTrip(t, sock, Request{
		Verb:    VerbChannelNew,
		Channel: &ChannelPayload{Cwd: "/home/op/project", Name: "project"},
	})
	if resp.Error != "channel.new: no channel creator configured" {
		t.Errorf("Response.Error = %q, want the not-configured diagnostic", resp.Error)
	}
	if resp.ChannelNew != nil {
		t.Errorf("Response.ChannelNew = %+v, want nil on the refusal path", resp.ChannelNew)
	}
}

// TestChannelNew_MissingCwd pins the wire-shape half of the two-sided empty-Cwd
// guard: an absent payload and an empty Cwd are both refused before the creator
// is reached. The creator's own half (see channelCreator) is what protects the
// fail-open seam from a caller that does not come through here.
func TestChannelNew_MissingCwd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload *ChannelPayload
	}{
		{name: "nil payload", payload: nil},
		{name: "empty cwd", payload: &ChannelPayload{Cwd: ""}},
		{name: "empty cwd with a name", payload: &ChannelPayload{Cwd: "", Name: "project"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creator := &fakeChannelCreator{returnID: "must-not-be-reached"}
			sock, stop := startServerWithChannelCreator(t, creator.create)
			defer stop()

			resp := channelRoundTrip(t, sock, Request{Verb: VerbChannelNew, Channel: tt.payload})
			if resp.Error != "channel.new: missing cwd" {
				t.Errorf("Response.Error = %q, want %q", resp.Error, "channel.new: missing cwd")
			}
			if calls := creator.recordedCalls(); len(calls) != 0 {
				t.Errorf("creator called %d time(s) with %+v, want 0 — the guard runs first", len(calls), calls)
			}
		})
	}
}

// TestChannelNew_ForwardsVerbatim pins AC#1's happy path and the property that
// makes the security review's single-trust-boundary claim true: this package
// performs NO path handling, so the cwd and name reach the creator as sent.
func TestChannelNew_ForwardsVerbatim(t *testing.T) {
	t.Parallel()

	creator := &fakeChannelCreator{returnID: "1b4e28ba-2fa1-4d3b-a3f5-cc0b7f3d1e77"}
	sock, stop := startServerWithChannelCreator(t, creator.create)
	defer stop()

	// A path with a symlink component and a trailing dot segment: the daemon-side
	// creator canonicalises, and this asserts the handler does not pre-empt it.
	const sent = "/home/op/link/./project"
	resp := channelRoundTrip(t, sock, Request{
		Verb:    VerbChannelNew,
		Channel: &ChannelPayload{Cwd: sent, Name: "chosen"},
	})
	if resp.Error != "" {
		t.Fatalf("Response.Error = %q, want empty", resp.Error)
	}
	if resp.ChannelNew == nil {
		t.Fatalf("Response.ChannelNew = nil, want the minted id")
	}
	if resp.ChannelNew.ConversationID != creator.returnID {
		t.Errorf("ConversationID = %q, want %q", resp.ChannelNew.ConversationID, creator.returnID)
	}

	calls := creator.recordedCalls()
	if len(calls) != 1 {
		t.Fatalf("creator called %d time(s), want 1", len(calls))
	}
	if calls[0].cwd != sent {
		t.Errorf("creator got cwd %q, want %q verbatim — the handler must not canonicalise", calls[0].cwd, sent)
	}
	if calls[0].name != "chosen" {
		t.Errorf("creator got name %q, want %q", calls[0].name, "chosen")
	}
}

// TestChannelNew_EmptyNameForwarded pins that an omitted --name reaches the
// creator as the empty string rather than being defaulted client- or
// handler-side. The base-name default is computed after confinement, so it can
// only be the creator's job.
func TestChannelNew_EmptyNameForwarded(t *testing.T) {
	t.Parallel()

	creator := &fakeChannelCreator{returnID: "aa11bb22-cc33-4d44-9e55-ff6677889900"}
	sock, stop := startServerWithChannelCreator(t, creator.create)
	defer stop()

	resp := channelRoundTrip(t, sock, Request{
		Verb:    VerbChannelNew,
		Channel: &ChannelPayload{Cwd: "/home/op/project"},
	})
	if resp.Error != "" {
		t.Fatalf("Response.Error = %q, want empty", resp.Error)
	}
	calls := creator.recordedCalls()
	if len(calls) != 1 || calls[0].name != "" {
		t.Fatalf("creator calls = %+v, want exactly one with an empty name", calls)
	}
}

// TestChannelNew_CreatorError pins that a refusal reaches the wire prefixed
// exactly once, and — the part that matters for AC#2 — that the handler adds
// nothing of its own. The creator's contract obliges every message to be
// static; the handler is what would break that by decorating it with the
// request's contents.
func TestChannelNew_CreatorError(t *testing.T) {
	t.Parallel()

	creator := &fakeChannelCreator{returnErr: errors.New("working directory not allowed")}
	sock, stop := startServerWithChannelCreator(t, creator.create)
	defer stop()

	const sentCwd = "/etc/shadow-ish/secret-project"
	resp := channelRoundTrip(t, sock, Request{
		Verb:    VerbChannelNew,
		Channel: &ChannelPayload{Cwd: sentCwd, Name: "secret-name"},
	})
	if resp.Error != "channel.new: working directory not allowed" {
		t.Errorf("Response.Error = %q, want the prefixed static refusal", resp.Error)
	}
	if resp.ChannelNew != nil {
		t.Errorf("Response.ChannelNew = %+v, want nil on the error path", resp.ChannelNew)
	}
	// The handler must not fold the request back into the refusal.
	for _, leak := range []string{sentCwd, "secret-project", "secret-name"} {
		if strings.Contains(resp.Error, leak) {
			t.Errorf("Response.Error %q echoes %q from the request", resp.Error, leak)
		}
	}
}
