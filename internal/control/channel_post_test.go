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

// fakeChannelPoster records what handleChannelPost forwards and returns a canned
// result. Safe under concurrent use; each test owns its own instance.
//
// It records (name, text) rather than asserting on them internally for
// fakeChannelCreator's reason: the property under test is that internal/control
// forwards the caller's bytes unchanged, so the assertion belongs in the test
// that knows what it sent.
type fakeChannelPoster struct {
	mu        sync.Mutex
	calls     []postCall
	returnErr error
}

type postCall struct{ name, text string }

func (f *fakeChannelPoster) post(name, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, postCall{name: name, text: text})
	return f.returnErr
}

func (f *fakeChannelPoster) recordedCalls() []postCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]postCall(nil), f.calls...)
}

// startServerWithChannelPoster mirrors startServerWithChannelCreator. A nil post
// leaves the seam uninstalled, which is the production state for a daemon that
// never wired it (v1/foreground).
func startServerWithChannelPoster(t *testing.T, post func(name, text string) error) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	srv.SetChannelPoster(post)
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

// TestChannelPost_NoPosterConfigured pins the nil-dependency-degrades-cleanly
// shape, with a deliberately WELL-FORMED payload: the guard order under test is
// "nil dependency BEFORE payload validation", so a caller cannot use the shape
// of the refusal to work out which dependencies a daemon has installed. A
// malformed payload here would pass whichever order the handler used.
func TestChannelPost_NoPosterConfigured(t *testing.T) {
	t.Parallel()

	sock, stop := startServerWithChannelPoster(t, nil)
	defer stop()

	resp := channelRoundTrip(t, sock, Request{
		Verb:        VerbChannelPost,
		ChannelPost: &ChannelPostPayload{Name: "questions", Text: "hello"},
	})
	if resp.Error != "channel.post: no channel poster configured" {
		t.Errorf("Response.Error = %q, want the not-configured diagnostic", resp.Error)
	}
	if resp.OK {
		t.Error("Response.OK = true on the refusal path, want false")
	}
}

// TestChannelPost_RefusesMalformedPayload covers every wire-shape refusal in one
// table. Each case asserts BOTH the exact message and that the poster was never
// reached — a handler that called through and then discarded the result would
// pass a message-only assertion while having created a channel.
func TestChannelPost_RefusesMalformedPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload *ChannelPostPayload
		want    string
	}{
		{name: "nil payload", payload: nil, want: "channel.post: missing name"},
		{name: "empty name", payload: &ChannelPostPayload{Name: "", Text: "hi"}, want: "channel.post: missing name"},
		{name: "empty text", payload: &ChannelPostPayload{Name: "questions", Text: ""}, want: "channel.post: empty message"},
		{
			name:    "text one byte over the cap",
			payload: &ChannelPostPayload{Name: "questions", Text: strings.Repeat("x", MaxChannelPostBytes+1)},
			want:    "channel.post: message too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poster := &fakeChannelPoster{}
			sock, stop := startServerWithChannelPoster(t, poster.post)
			defer stop()

			resp := channelRoundTrip(t, sock, Request{Verb: VerbChannelPost, ChannelPost: tt.payload})
			if resp.Error != tt.want {
				t.Errorf("Response.Error = %q, want %q", resp.Error, tt.want)
			}
			if resp.OK {
				t.Error("Response.OK = true on a refusal, want false")
			}
			if calls := poster.recordedCalls(); len(calls) != 0 {
				t.Errorf("poster called %d time(s), want 0 — the guard runs first", len(calls))
			}
		})
	}
}

// TestChannelPost_AcceptsContentAtTheCap pins the boundary itself rather than
// only the byte past it: a message of exactly MaxChannelPostBytes is accepted.
// Without this, an off-by-one in the comparison refuses a legal message and no
// other test notices.
func TestChannelPost_AcceptsContentAtTheCap(t *testing.T) {
	t.Parallel()

	poster := &fakeChannelPoster{}
	sock, stop := startServerWithChannelPoster(t, poster.post)
	defer stop()

	text := strings.Repeat("x", MaxChannelPostBytes)
	resp := channelRoundTrip(t, sock, Request{
		Verb:        VerbChannelPost,
		ChannelPost: &ChannelPostPayload{Name: "questions", Text: text},
	})
	if resp.Error != "" {
		t.Errorf("Response.Error = %q, want empty at exactly the cap", resp.Error)
	}
	if !resp.OK {
		t.Error("Response.OK = false, want true at exactly the cap")
	}
	calls := poster.recordedCalls()
	if len(calls) != 1 || len(calls[0].text) != MaxChannelPostBytes {
		t.Fatalf("poster calls = %d, want 1 carrying %d bytes", len(calls), MaxChannelPostBytes)
	}
}

// TestChannelPost_ForwardsVerbatim pins the happy path and the property that
// makes the security review's single-trust-boundary claim true: this package
// validates shape only, so the name and text reach the poster as sent.
func TestChannelPost_ForwardsVerbatim(t *testing.T) {
	t.Parallel()

	poster := &fakeChannelPoster{}
	sock, stop := startServerWithChannelPoster(t, poster.post)
	defer stop()

	const name, text = "  questions/with spaces  ", "line one\nline two"
	resp := channelRoundTrip(t, sock, Request{
		Verb:        VerbChannelPost,
		ChannelPost: &ChannelPostPayload{Name: name, Text: text},
	})
	if resp.Error != "" {
		t.Errorf("Response.Error = %q, want empty", resp.Error)
	}
	if !resp.OK {
		t.Error("Response.OK = false, want true on success")
	}

	calls := poster.recordedCalls()
	if len(calls) != 1 {
		t.Fatalf("poster calls = %d, want 1", len(calls))
	}
	if calls[0].name != name {
		t.Errorf("poster got name %q, want the bytes sent %q — this package must not trim or clean", calls[0].name, name)
	}
	if calls[0].text != text {
		t.Errorf("poster got text %q, want the bytes sent %q", calls[0].text, text)
	}
}

// TestChannelPost_PosterError pins that a refusal reaches the wire with the verb
// prefix and nothing else folded in. The poster's text is the whole message
// body, which is only safe because SetChannelPoster's contract obliges every
// reason to be static or daemon-derived.
func TestChannelPost_PosterError(t *testing.T) {
	t.Parallel()

	poster := &fakeChannelPoster{returnErr: errors.New("3 channels share that name")}
	sock, stop := startServerWithChannelPoster(t, poster.post)
	defer stop()

	resp := channelRoundTrip(t, sock, Request{
		Verb:        VerbChannelPost,
		ChannelPost: &ChannelPostPayload{Name: "questions", Text: "hi"},
	})
	if resp.Error != "channel.post: 3 channels share that name" {
		t.Errorf("Response.Error = %q, want the prefixed poster reason", resp.Error)
	}
	if resp.OK {
		t.Error("Response.OK = true after a poster error, want false")
	}
}

// TestChannelPost_SetPosterNilClears pins the documented "passing nil clears a
// previously-installed poster" contract, which the tests above rely on to build
// an unconfigured daemon.
func TestChannelPost_SetPosterNilClears(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")
	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)

	poster := &fakeChannelPoster{}
	srv.SetChannelPoster(poster.post)
	srv.SetChannelPoster(nil)

	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	resp := channelRoundTrip(t, sock, Request{
		Verb:        VerbChannelPost,
		ChannelPost: &ChannelPostPayload{Name: "questions", Text: "hi"},
	})
	if resp.Error != "channel.post: no channel poster configured" {
		t.Errorf("Response.Error = %q, want the cleared-seam diagnostic", resp.Error)
	}
	if calls := poster.recordedCalls(); len(calls) != 0 {
		t.Errorf("cleared poster still called %d time(s)", len(calls))
	}
}

// TestChannelPostClient_RoundTrip drives the client helper against a real server
// and asserts the three outcomes it distinguishes: success, a server refusal
// surfaced verbatim, and a reply carrying neither an error nor the OK flag.
//
// The third case is the one worth having. A helper that returned nil for an
// empty Response would report success for a daemon that answered nothing at all,
// which is exactly what a cron must not see.
func TestChannelPostClient_RoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		poster := &fakeChannelPoster{}
		sock, stop := startServerWithChannelPoster(t, poster.post)
		defer stop()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ChannelPost(ctx, sock, "questions", "hello"); err != nil {
			t.Fatalf("ChannelPost: %v", err)
		}
		calls := poster.recordedCalls()
		if len(calls) != 1 || calls[0].name != "questions" || calls[0].text != "hello" {
			t.Errorf("poster calls = %+v, want one (questions, hello)", calls)
		}
	})

	t.Run("server refusal", func(t *testing.T) {
		poster := &fakeChannelPoster{returnErr: errors.New("could not record the message")}
		sock, stop := startServerWithChannelPoster(t, poster.post)
		defer stop()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := ChannelPost(ctx, sock, "questions", "hello")
		if err == nil {
			t.Fatal("ChannelPost = nil, want the server's refusal")
		}
		if got := err.Error(); got != "channel.post: could not record the message" {
			t.Errorf("err = %q, want the prefixed server reason", got)
		}
	})

	t.Run("reply missing the ok flag", func(t *testing.T) {
		// A server that answers an empty Response — the shape a daemon too old to
		// know this verb would produce if its unknown-verb arm ever went missing.
		// Inlined rather than shared, matching TestSessionsRename_DecodesEmptyResponseAsError.
		sock := filepath.Join(shortTempDir(t), "p.sock")
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
		err = ChannelPost(ctx, sock, "questions", "hello")
		if err == nil {
			t.Fatal("ChannelPost = nil, want a refusal for a reply with no ok flag")
		}
		if got := err.Error(); got != "control: channel.post response missing ok flag" {
			t.Errorf("err = %q, want the missing-ok diagnostic", got)
		}
	})
}
