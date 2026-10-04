package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/update"
)

func startIdleControl(t *testing.T, provider func(context.Context) (control.UpdateWhenIdleResult, error)) (string, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	sock := filepath.Join(shortTempDir(t), "u.sock")
	srv := control.NewServer(sock, rekeyTestResolver{}, nil, nil, nil, nil)
	srv.SetUpdateWhenIdleProvider(func() (control.UpdateWhenIdleResult, error) { return provider(ctx) })
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ctx); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); _ = srv.Close(); idleAwait(t, done) })
	return sock, cancel, done
}

func idleAwait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not finish")
	}
}

func idleDecision(t *testing.T, sock string) *control.UpdateWhenIdleResult {
	t.Helper()
	got, err := control.UpdateWhenIdle(t.Context(), sock)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestUpdateWhenIdleCLI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result control.UpdateWhenIdleResult
		err    error
		want   string
	}{
		{"current", control.UpdateWhenIdleResult{Decision: control.UpdateUpToDate}, nil, "up to date\n"},
		{"refused", control.UpdateWhenIdleResult{Decision: control.UpdateNotEligible, Reason: "no managed unit"}, nil, "not eligible: no managed unit\n"},
		{"accepted", control.UpdateWhenIdleResult{Decision: control.UpdateWillInstall, ReleaseTag: "v0.9.2"}, nil, "will install v0.9.2 when idle\n"},
		{"error", control.UpdateWhenIdleResult{}, errors.New("PRIVATE"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock, _, _ := startIdleControl(t, func(context.Context) (control.UpdateWhenIdleResult, error) { return tc.result, tc.err })
			var out bytes.Buffer
			err := runUpdateArgs([]string{"--pyry-name=unused", "--pyry-socket=" + sock, "--when-idle"}, &out)
			if (err != nil) != (tc.err != nil) || out.String() != tc.want {
				t.Fatalf("output = %q, err = %v", out.String(), err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("provider details leaked")
			}
		})
	}
	for _, flag := range []string{"--check", "--check=false", "--version=v1.0.0", "--version=", "--no-restart", "--no-restart=false"} {
		t.Run(flag, func(t *testing.T) {
			err := runUpdateArgs([]string{"--pyry-socket=/absent", "--when-idle", flag}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "cannot combine") {
				t.Fatalf("conflict = %v", err)
			}
		})
	}
	if err := runUpdateArgs([]string{"--pyry-socket=" + filepath.Join(shortTempDir(t), "absent"), "--when-idle"}, io.Discard); err == nil {
		t.Fatal("transport failure did not fail")
	}
}

func TestUpdateWhenIdleNamedDaemon(t *testing.T) {
	home := shortTempDir(t)
	t.Setenv("HOME", home)
	sock, _, _ := startIdleControl(t, func(context.Context) (control.UpdateWhenIdleResult, error) {
		return control.UpdateWhenIdleResult{Decision: control.UpdateUpToDate}, nil
	})
	if err := os.Mkdir(filepath.Join(home, ".pyry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sock, filepath.Join(home, ".pyry", "elli.sock")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runUpdateArgs([]string{"--pyry-name", "elli", "--when-idle"}, &out); err != nil || out.String() != "up to date\n" {
		t.Fatalf("named daemon = %q, %v", out.String(), err)
	}
}

func TestUpdateWhenIdleSharedAttempt(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "scheduled"}[scheduled], func(t *testing.T) {
			f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
			t.Cleanup(a.join)
			var idle atomic.Bool
			polling, release := make(chan struct{}), make(chan struct{})
			a.idle = idle.Load
			a.wait = func(ctx context.Context, d time.Duration) bool {
				if d == a.startDelay {
					return true
				}
				if d != a.restartPoll {
					t.Errorf("unexpected wait %v", d)
					return false
				}
				close(polling)
				select {
				case <-ctx.Done():
					return false
				case <-release:
					return true
				}
			}
			sock, cancel, _ := startIdleControl(t, a.request)
			if scheduled {
				ctx, stop := context.WithCancel(t.Context())
				defer stop()
				done := make(chan struct{})
				go func() { defer close(done); _ = a.Run(ctx) }()
				t.Cleanup(func() { stop(); idleAwait(t, done) })
			} else {
				idleDecision(t, sock)
			}
			idleAwait(t, polling)
			for i := 0; i < 8; i++ {
				if got := idleDecision(t, sock); got.ReleaseTag != "v0.9.2" {
					t.Fatalf("pending = %+v", got)
				}
			}
			if f.requests.Load() != 1 || f.assetHits.Load() != 0 {
				t.Fatal("pending work fetched again")
			}
			idle.Store(true)
			close(release)
			a.join()
			if f.assetHits.Load() != 3 || len(f.restarts) != 1 {
				t.Fatalf("assets = %d, restarts = %d", f.assetHits.Load(), len(f.restarts))
			}
			if got := idleDecision(t, sock); got.ReleaseTag != "v0.9.2" || f.requests.Load() != 4 {
				t.Fatal("installed tag lost or installed twice")
			}
			cancel()
		})
	}
}

func TestUpdateWhenIdleConcurrentMetadata(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "eligible", true: "failed"}[fail], func(t *testing.T) {
			_, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
			t.Cleanup(a.join)
			a.idle = func() bool { return false }
			entered, release := make(chan struct{}), make(chan struct{})
			var hits atomic.Int32
			metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				close(entered)
				select {
				case <-r.Context().Done():
					return
				case <-release:
				}
				if fail {
					http.Error(w, "metadata failed", 500)
				} else {
					_, _ = w.Write([]byte(latestV092))
				}
			}))
			t.Cleanup(metadata.Close)
			a.opts.fetcher.BaseURL = metadata.URL
			allEntered := make(chan struct{}, 12)
			sock, cancel, _ := startIdleControl(t, func(ctx context.Context) (control.UpdateWhenIdleResult, error) {
				attempt := a.begin(ctx)
				allEntered <- struct{}{}
				<-attempt.decided
				return attempt.result, attempt.err
			})
			done := make(chan error, 12)
			for i := 0; i < 12; i++ {
				go func() {
					got, err := control.UpdateWhenIdle(t.Context(), sock)
					if err == nil && got.ReleaseTag != "v0.9.2" {
						err = errors.New("wrong tag")
					}
					done <- err
				}()
			}
			idleAwait(t, entered)
			for i := 0; i < 12; i++ {
				select {
				case <-allEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("request did not join")
				}
			}
			close(release)
			for i := 0; i < 12; i++ {
				if err := <-done; (err != nil) != fail {
					t.Fatalf("decision error = %v", err)
				}
			}
			if hits.Load() != 1 {
				t.Fatalf("metadata checks = %d", hits.Load())
			}
			cancel()
			a.join()
		})
	}
}

func TestUpdateWhenIdleRetryAndRestartFailure(t *testing.T) {
	for _, phase := range []string{"metadata", "fetch", "install", "restart"} {
		t.Run(phase, func(t *testing.T) {
			f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
			t.Cleanup(a.join)
			var replaces atomic.Int32
			a.opts.replace = func(target string, data []byte, mode os.FileMode) error {
				if target == f.target && replaces.Add(1) == 1 && phase == "install" {
					return errors.New("disk full")
				}
				return update.AtomicReplace(target, data, mode)
			}
			a.opts.runRestart = func(context.Context, []string) error { return errors.New("restart failed") }
			if phase == "metadata" {
				f.latest.Store("not json")
			}
			baseURL := a.opts.fetcher.BaseURL
			if phase == "fetch" {
				a.opts.fetcher.BaseURL = ":invalid"
			}
			sock, _, _ := startIdleControl(t, a.request)
			_, err := control.UpdateWhenIdle(t.Context(), sock)
			if (err != nil) != (phase == "metadata" || phase == "fetch") {
				t.Fatalf("first request = %v", err)
			}
			a.join()
			f.latest.Store(latestV092)
			a.opts.fetcher.BaseURL = baseURL
			if got := idleDecision(t, sock); got.ReleaseTag != "v0.9.2" {
				t.Fatalf("retry = %+v", got)
			}
			a.join()
			want := int32(1)
			if phase == "install" {
				want = 2
			}
			if replaces.Load() != want {
				t.Fatalf("replaces = %d, want %d", replaces.Load(), want)
			}
			requests := f.requests.Load()
			idleDecision(t, sock)
			if f.requests.Load() != requests {
				t.Fatal("installed attempt retried after restart failure")
			}
		})
	}
}

func TestUpdateWhenIdleDisconnectAndShutdown(t *testing.T) {
	f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	t.Cleanup(a.join)
	entered, release, polling := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a.idle = func() bool { return false }
	a.wait = func(ctx context.Context, _ time.Duration) bool { close(polling); <-ctx.Done(); return false }
	sock, cancel, done := startIdleControl(t, func(ctx context.Context) (control.UpdateWhenIdleResult, error) {
		close(entered)
		<-release
		return a.request(ctx)
	})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(control.Request{Verb: control.VerbUpdateWhenIdle}); err != nil {
		t.Fatal(err)
	}
	idleAwait(t, entered)
	_ = conn.Close()
	close(release)
	idleAwait(t, polling)
	if f.requests.Load() != 1 || f.assetHits.Load() != 0 {
		t.Fatal("disconnect cancelled accepted work")
	}
	cancel()
	idleAwait(t, done)
	a.join()
	if f.assetHits.Load() != 0 {
		t.Fatal("shutdown installed pending release")
	}
}

func TestUpdateWhenIdleImmediateRestartResponse(t *testing.T) {
	_, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	t.Cleanup(a.join)
	restarted, respond := make(chan struct{}), make(chan struct{})
	var cancel context.CancelFunc
	a.opts.runRestart = func(context.Context, []string) error { cancel(); close(restarted); return nil }
	sock, stop, served := startIdleControl(t, func(ctx context.Context) (control.UpdateWhenIdleResult, error) {
		result, err := a.request(ctx)
		<-respond // Deliberately hold acceptance until installation has triggered shutdown.
		return result, err
	})
	cancel = stop
	result := make(chan *control.UpdateWhenIdleResult, 1)
	go func() {
		got, err := control.UpdateWhenIdle(t.Context(), sock)
		if err != nil {
			t.Errorf("acceptance: %v", err)
		}
		result <- got
	}()
	idleAwait(t, restarted)
	select {
	case <-served:
		t.Fatal("responding daemon ended before writing acceptance")
	default:
	}
	close(respond)
	got := <-result
	if got == nil || got.ReleaseTag != "v0.9.2" {
		t.Fatalf("acceptance = %+v", got)
	}
	idleAwait(t, served)
	a.join()
}

func TestUpdateWhenIdlePendingPhases(t *testing.T) {
	for _, phase := range []string{"download", "restart"} {
		t.Run(phase, func(t *testing.T) {
			f, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
			t.Cleanup(a.join)
			entered, release := make(chan struct{}), make(chan struct{})
			var idle atomic.Bool
			idle.Store(true)
			a.idle = idle.Load
			a.wait = func(ctx context.Context, _ time.Duration) bool {
				close(entered)
				select {
				case <-ctx.Done():
					return false
				case <-release:
					idle.Store(true)
					return true
				}
			}
			if phase == "download" {
				transport := http.DefaultTransport
				a.opts.fetcher.HTTPClient = &http.Client{Transport: idleRoundTripper(func(r *http.Request) (*http.Response, error) {
					if strings.Contains(r.URL.Path, "/releases/download/") {
						select {
						case <-entered:
						default:
							close(entered)
						}
						select {
						case <-r.Context().Done():
							return nil, r.Context().Err()
						case <-release:
						}
					}
					return transport.RoundTrip(r)
				})}
			} else {
				a.opts.replace = func(target string, data []byte, mode os.FileMode) error {
					if target == f.target {
						idle.Store(false)
					}
					return update.AtomicReplace(target, data, mode)
				}
			}
			sock, _, _ := startIdleControl(t, a.request)
			idleDecision(t, sock)
			idleAwait(t, entered)
			before := f.requests.Load()
			for i := 0; i < 3; i++ {
				if got := idleDecision(t, sock); got.ReleaseTag != "v0.9.2" {
					t.Fatalf("pending = %+v", got)
				}
			}
			if f.requests.Load() != before {
				t.Fatal("pending phase started another check")
			}
			close(release)
			a.join()
			if f.requests.Load() != 4 || len(f.restarts) != 1 {
				t.Fatal("pending phase installed twice")
			}
		})
	}
}

type idleRoundTripper func(*http.Request) (*http.Response, error)

func (f idleRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateWhenIdleWakesScheduler(t *testing.T) {
	_, a := newAutoUpdateFixture(t, latestV092, testSigningPriv)
	t.Cleanup(a.join)
	waiting := make(chan struct{})
	a.wait = func(ctx context.Context, d time.Duration) bool {
		if d != a.startDelay {
			t.Errorf("scheduled another check: %v", d)
		}
		close(waiting)
		<-ctx.Done()
		return false
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(ctx) }()
	idleAwait(t, waiting)
	sock, _, _ := startIdleControl(t, a.request)
	idleDecision(t, sock)
	idleAwait(t, done)
	a.join()
}

func TestUpdateWhenIdleUnsupportedAndDisabledFlag(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "old.sock")
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
		var req control.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Verb != control.VerbUpdateWhenIdle {
			t.Errorf("verb = %s", req.Verb)
		}
		_ = json.NewEncoder(conn).Encode(control.Response{Error: "unknown verb: update.when-idle"})
	}()
	t.Cleanup(func() { _ = ln.Close(); idleAwait(t, done) })
	var out bytes.Buffer
	err = runUpdateArgs([]string{"--pyry-socket=" + sock, "--when-idle"}, &out)
	if err == nil || !strings.Contains(err.Error(), "unknown verb") || out.Len() != 0 {
		t.Fatalf("unsupported = %q, %v", out.String(), err)
	}
	// A disabled flag retains the ordinary pinned/check path and permits its flags.
	err = runUpdateArgs([]string{"--when-idle=false", "--version=0.0.0", "--check", "--no-restart"}, &out)
	if err != nil || !strings.Contains(out.String(), "Current version:") {
		t.Fatalf("ordinary update = %q, %v", out.String(), err)
	}
}
