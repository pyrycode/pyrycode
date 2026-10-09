package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func startUpdateServer(t *testing.T, provider func() (UpdateWhenIdleResult, error)) (*Server, string, func()) {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "u.sock")
	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	srv.SetUpdateWhenIdleProvider(provider)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	return srv, sock, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("server did not drain")
		}
	}
}

// Unlike a fire-and-forget peer, this helper drains its connection handler.
func startUpdatePeer(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "u.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("peer did not drain")
		}
	})
	return sock
}

func TestUpdateWhenIdle_Decisions(t *testing.T) {
	t.Parallel()
	for _, want := range []UpdateWhenIdleResult{
		{Decision: UpdateUpToDate},
		{Decision: UpdateNotEligible, Reason: "service is unmanaged"},
		{Decision: UpdateWillInstall, ReleaseTag: "v9.8.7"},
	} {
		t.Run(string(want.Decision), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			_, sock, stop := startUpdateServer(t, func() (UpdateWhenIdleResult, error) {
				calls.Add(1)
				return want, nil
			})
			defer stop()
			got, err := UpdateWhenIdle(context.Background(), sock)
			if err != nil || got == nil || *got != want {
				t.Fatalf("decision = %+v, %v; want %+v", got, err, want)
			}
			if calls.Load() != 1 {
				t.Errorf("provider calls = %d, want 1", calls.Load())
			}
		})
	}
}

func TestUpdateWhenIdle_WireShape(t *testing.T) {
	t.Parallel()
	requestLine := make(chan string, 1)
	sock := startUpdatePeer(t, func(conn net.Conn) {
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			requestLine <- err.Error()
			return
		}
		requestLine <- line
		_, _ = conn.Write([]byte(`{"updateWhenIdle":{"decision":"will-install","releaseTag":"v7.6.5"}}` + "\n"))
	})
	got, err := UpdateWhenIdle(context.Background(), sock)
	want := UpdateWhenIdleResult{Decision: UpdateWillInstall, ReleaseTag: "v7.6.5"}
	if err != nil || got == nil || *got != want {
		t.Fatalf("decision = %+v, %v; want %+v", got, err, want)
	}
	if line := <-requestLine; line != "{\"verb\":\"update.when-idle\"}\n" {
		t.Errorf("request = %q", line)
	}
	encoded, err := json.Marshal(Response{UpdateWhenIdle: got})
	if err != nil || string(encoded) != `{"updateWhenIdle":{"decision":"will-install","releaseTag":"v7.6.5"}}` {
		t.Errorf("encoded response = %s, %v", encoded, err)
	}
}

func TestServer_UpdateWhenIdle_RejectsInvalidResults(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		result    UpdateWhenIdleResult
		err       error
		absent    bool
		wantError string
	}{
		{name: "absent provider", absent: true, wantError: "update.when-idle: provider not configured"},
		{name: "provider error wins", result: UpdateWhenIdleResult{Decision: UpdateWillInstall, ReleaseTag: "v1"}, err: errors.New("PRIVATE-PROVIDER-DETAIL"), wantError: "update.when-idle: operation failed"},
		{name: "missing decision"},
		{name: "unknown decision", result: UpdateWhenIdleResult{Decision: "unknown"}},
		{name: "missing reason", result: UpdateWhenIdleResult{Decision: UpdateNotEligible}},
		{name: "blank reason", result: UpdateWhenIdleResult{Decision: UpdateNotEligible, Reason: " \t"}},
		{name: "missing tag", result: UpdateWhenIdleResult{Decision: UpdateWillInstall}},
		{name: "blank tag", result: UpdateWhenIdleResult{Decision: UpdateWillInstall, ReleaseTag: " \n"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			provider := func() (UpdateWhenIdleResult, error) {
				calls.Add(1)
				return tt.result, tt.err
			}
			if tt.absent {
				provider = nil
			}
			_, sock, stop := startUpdateServer(t, provider)
			defer stop()
			resp := channelRoundTrip(t, sock, Request{Verb: VerbUpdateWhenIdle})
			if resp.Error == "" || resp.UpdateWhenIdle != nil || resp.OK {
				t.Fatalf("error response = %+v", resp)
			}
			if tt.wantError != "" && resp.Error != tt.wantError {
				t.Errorf("error = %q, want %q", resp.Error, tt.wantError)
			}
			if strings.Contains(resp.Error, "PRIVATE") || strings.Contains(resp.Error, "unknown") {
				t.Errorf("error leaked provider value: %q", resp.Error)
			}
			wantCalls := int32(1)
			if tt.absent {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Errorf("calls = %d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestUpdateWhenIdle_RejectsMalformedResponses(t *testing.T) {
	t.Parallel()
	for _, wire := range []string{
		`{}`, `{"ok":true}`, `{"updateWhenIdle":null}`, `{"updateWhenIdle":{}}`,
		`{"updateWhenIdle":{"decision":"unknown"}}`,
		`{"updateWhenIdle":{"decision":"not-eligible"}}`,
		`{"updateWhenIdle":{"decision":"will-install"}}`,
		`{"updateWhenIdle":{"decision":"not-eligible","reason":" "}}`,
		`{"updateWhenIdle":{"decision":"will-install","releaseTag":" "}}`,
		`{"error":"wire failure","updateWhenIdle":{"decision":"will-install","releaseTag":"v1"}}`,
		`{"error":"wire failure","updateWhenIdle":{}}`,
		`not json`,
	} {
		t.Run(wire, func(t *testing.T) {
			t.Parallel()
			sock := startUpdatePeer(t, func(conn net.Conn) {
				var req Request
				_ = json.NewDecoder(conn).Decode(&req)
				_, _ = conn.Write([]byte(wire + "\n"))
			})
			got, err := UpdateWhenIdle(context.Background(), sock)
			if err == nil || got != nil {
				t.Fatalf("decision = %+v, %v; want nil, error", got, err)
			}
			if strings.Contains(wire, "wire failure") && err.Error() != "wire failure" {
				t.Errorf("wire error did not win: %v", err)
			}
		})
	}
}

func TestUpdateWhenIdle_ResponseOutlastsHandshake(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	srv, sock, stop := startUpdateServer(t, func() (UpdateWhenIdleResult, error) {
		close(entered)
		<-release
		return UpdateWhenIdleResult{Decision: UpdateWillInstall, ReleaseTag: "v2"}, nil
	})
	defer stop()
	// Release even if an assertion fails before the delayed success.
	var released atomic.Bool
	defer func() {
		if !released.Load() {
			close(release)
		}
	}()
	type outcome struct {
		result *UpdateWhenIdleResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := UpdateWhenIdle(context.Background(), sock); done <- outcome{result, err} }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider not entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Status(ctx, sock); err != nil {
		t.Fatalf("concurrent status: %v", err)
	}
	setterDone := make(chan struct{})
	go func() { srv.SetUpdateWhenIdleProvider(nil); close(setterDone) }()
	select {
	case <-setterDone:
	case <-time.After(time.Second):
		t.Fatal("provider holds Server.mu")
	}
	select {
	case got := <-done:
		t.Fatalf("returned before provider release: %+v", got)
	case <-time.After(defaultHandshakeTimeout + 250*time.Millisecond):
	}
	close(release)
	released.Store(true)
	select {
	case got := <-done:
		if got.err != nil || got.result == nil || got.result.Decision != UpdateWillInstall || got.result.ReleaseTag != "v2" {
			t.Fatalf("delayed result = %+v, %v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delayed response did not arrive")
	}
}

func TestUpdateWhenIdle_CallerStopsWaiting(t *testing.T) {
	t.Parallel()
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit cancellation", true: "earlier deadline"}[deadline], func(t *testing.T) {
			t.Parallel()
			entered, release := make(chan struct{}), make(chan struct{})
			_, sock, stop := startUpdateServer(t, func() (UpdateWhenIdleResult, error) {
				close(entered)
				<-release
				return UpdateWhenIdleResult{Decision: UpdateWillInstall, ReleaseTag: "v3"}, nil
			})
			defer func() { close(release); stop() }()
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
			}
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				got, err := UpdateWhenIdle(ctx, sock)
				if err == nil || got != nil {
					t.Errorf("decision = %+v, %v; want nil, error", got, err)
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("provider not entered")
			}
			if !deadline {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("client did not stop waiting")
			}
		})
	}
}

func TestUpdateWhenIdle_SilentPeerCeiling(t *testing.T) {
	t.Parallel()
	if updateWhenIdleTimeout != 70*time.Second {
		t.Fatalf("operation timeout = %v, want 70s", updateWhenIdleTimeout)
	}
	timeout := 100 * time.Millisecond
	request := func(ctx context.Context, socket string) (*UpdateWhenIdleResult, error) {
		return updateWhenIdle(ctx, socket, timeout)
	}
	if os.Getenv("PYRY_SLOW_TESTS") == "1" {
		timeout = updateWhenIdleTimeout
		request = UpdateWhenIdle
	}
	release := make(chan struct{})
	sock := startUpdatePeer(t, func(conn net.Conn) {
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		<-release
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	start := time.Now()
	got, err := request(ctx, sock)
	elapsed := time.Since(start)
	if err == nil || got != nil {
		t.Fatalf("decision = %+v, %v; want nil, error", got, err)
	}
	if ctx.Err() != nil {
		t.Errorf("caller expired before operation bound: %v", ctx.Err())
	}
	if elapsed < timeout-20*time.Millisecond || elapsed > timeout+3*time.Second {
		t.Errorf("elapsed = %v, want near %v", elapsed, timeout)
	}
}

func TestUpdateWhenIdle_DialFailure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got, err := UpdateWhenIdle(ctx, filepath.Join(shortTempDir(t), "absent.sock"))
	if err == nil || got != nil {
		t.Fatalf("decision = %+v, %v; want nil, error", got, err)
	}
}

type updateDeadlineConn struct {
	net.Conn
	deadlines chan time.Duration
}

func (c *updateDeadlineConn) SetDeadline(deadline time.Time) error {
	c.deadlines <- time.Until(deadline)
	return c.Conn.SetDeadline(deadline)
}

func (c *updateDeadlineConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlines <- time.Until(deadline)
	return c.Conn.SetWriteDeadline(deadline)
}

func TestServer_UpdateWhenIdle_DeadlinePolicy(t *testing.T) {
	t.Parallel()
	deadlines := make(chan time.Duration, 2)
	srv := NewServer("", &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	srv.SetUpdateWhenIdleProvider(func() (UpdateWhenIdleResult, error) {
		return UpdateWhenIdleResult{Decision: UpdateUpToDate}, nil
	})
	sock := startUpdatePeer(t, func(conn net.Conn) {
		srv.handle(&updateDeadlineConn{Conn: conn, deadlines: deadlines})
	})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	// Consume some handshake time; the write bound must start afresh after it.
	time.Sleep(200 * time.Millisecond)
	if err := json.NewEncoder(conn).Encode(Request{Verb: VerbUpdateWhenIdle}); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil || resp.UpdateWhenIdle == nil {
		t.Fatalf("response = %+v, %v", resp, err)
	}
	for _, want := range []time.Duration{5 * time.Second, 70 * time.Second} {
		select {
		case got := <-deadlines:
			if got < want-100*time.Millisecond || got > want {
				t.Errorf("deadline duration = %v, want %v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("deadline was not installed")
		}
	}
}

func TestProtocol_UpdateWhenIdle_DecisionEncodings(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		result UpdateWhenIdleResult
		wire   string
	}{
		{UpdateWhenIdleResult{Decision: UpdateUpToDate}, `{"updateWhenIdle":{"decision":"up-to-date"}}`},
		{UpdateWhenIdleResult{Decision: UpdateNotEligible, Reason: "unmanaged"}, `{"updateWhenIdle":{"decision":"not-eligible","reason":"unmanaged"}}`},
		{UpdateWhenIdleResult{Decision: UpdateWillInstall, ReleaseTag: "v4"}, `{"updateWhenIdle":{"decision":"will-install","releaseTag":"v4"}}`},
	} {
		encoded, err := json.Marshal(Response{UpdateWhenIdle: &tt.result})
		if err != nil || string(encoded) != tt.wire {
			t.Errorf("encoding = %s, %v; want %s", encoded, err, tt.wire)
		}
		var decoded Response
		if err := json.Unmarshal([]byte(tt.wire), &decoded); err != nil || decoded.UpdateWhenIdle == nil || *decoded.UpdateWhenIdle != tt.result {
			t.Errorf("decoded = %+v, %v; want %+v", decoded.UpdateWhenIdle, err, tt.result)
		}
	}
}
