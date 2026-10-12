package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type healthReply struct {
	Descendant       bool
	Body             string
	Status           int
	Delay, BodyDelay time.Duration
	Redirect         string
}

func healthyBody() string {
	return fmt.Sprintf(`{"ready":true,"app_id":%q,"release_version":"1.0.0"}`, testApp)
}
func healthWrite(t *testing.T, r *Registry, reply healthReply) {
	t.Helper()
	b, _ := json.Marshal(reply)
	p := filepath.Join(r.root, testApp, "data/health")
	tmp := p + ".tmp"
	if e := os.WriteFile(tmp, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(tmp, p); e != nil {
		t.Fatal(e)
	}
}
func healthFixture(t *testing.T, reply healthReply) (*Registry, *Owner) {
	t.Helper()
	r, o := ownerFixture(t)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	node := filepath.Join(t.TempDir(), "node")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = '--version' ]; then echo v24.15.0; exit 0; fi\nexec '%s' -test.run '^TestHealthHelperProcess$' -- \"$@\"\n", exe)
	if e = os.WriteFile(node, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	o.node = node
	o.startup = 700 * time.Millisecond
	o.startInterval = 20 * time.Millisecond
	o.runtimeInterval = 40 * time.Millisecond
	o.requestTimeout = 100 * time.Millisecond
	b, _ := json.Marshal(reply)
	ownerBuild(t, r, testApp, "a", "1.0.0", string(b))
	return r, o
}
func TestHealthHelperProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Getenv("PYRY_APP_PORT") == "" {
		return
	}
	data := os.Getenv("PYRY_APP_DATA_DIR")
	initial, e := os.ReadFile("dist/service/main.js")
	if e != nil {
		os.Exit(2)
	}
	if e = os.WriteFile(filepath.Join(data, "health"), initial, 0600); e != nil {
		os.Exit(3)
	}
	var config healthReply
	json.Unmarshal(initial, &config)
	if config.Descendant {
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "-test.run", "^TestOwnerHelperProcess$", "--", "descendant", "forced")
		cmd.Env = os.Environ()
		if cmd.Start() != nil {
			os.Exit(5)
		}
		for {
			if _, e := os.Stat(filepath.Join(data, "descendant")); e == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	listener, e := net.Listen("tcp4", "127.0.0.1:"+os.Getenv("PYRY_APP_PORT"))
	if e != nil {
		os.Exit(4)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/_pyry/health" {
			w.Write([]byte("resource"))
			return
		}
		b, _ := os.ReadFile(filepath.Join(data, "health"))
		var reply healthReply
		json.Unmarshal(b, &reply)
		f, _ := os.OpenFile(filepath.Join(data, "probes"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if f != nil {
			fmt.Fprintln(f, time.Now().UnixNano())
			f.Close()
		}
		time.Sleep(reply.Delay)
		if reply.Redirect != "" {
			w.Header().Set("Location", reply.Redirect)
		}
		status := reply.Status
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		if reply.BodyDelay > 0 {
			w.(http.Flusher).Flush()
			time.Sleep(reply.BodyDelay)
		}
		w.Write([]byte(reply.Body))
	})}
	go server.Serve(listener)
	if e := publishOwnerPID(filepath.Join(data, "ready")); e != nil {
		os.Exit(6)
	}
	for {
		select {
		case <-signals:
			os.Exit(0)
		case <-time.After(5 * time.Millisecond):
		}
		if _, e := os.Stat(filepath.Join(data, "exit")); e == nil {
			os.Exit(0)
		}
	}
}
func healthWait(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("health condition timed out")
}
func healthTarget(t *testing.T, o *Owner) *Target {
	t.Helper()
	var target *Target
	healthWait(t, func() bool { var e error; target, e = o.Lookup(testHost, testApp); return e == nil })
	return target
}
func healthStart(t *testing.T, r *Registry, o *Owner) *generation {
	t.Helper()
	if e := o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
	return o.generations[testApp]
}
func healthUse(target *Target) error {
	req, _ := http.NewRequest("GET", "http://caller.invalid/api/value", nil)
	resp, e := target.Do(req)
	if e == nil {
		defer resp.Body.Close()
		_, e = io.ReadAll(resp.Body)
	}
	return e
}
func TestOwnerHealthRejects(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"ready":false}`, `{"ready":true,"app_id":"wrong","release_version":"1.0.0"}`, strings.Replace(healthyBody(), "1.0.0", "2.0.0", 1), strings.Replace(healthyBody(), `"ready":true`, `"ready":"true"`, 1), healthyBody() + ` {}`, strings.Replace(healthyBody(), `"ready":true`, `"ready":false,"ready":true`, 1), strings.Repeat(" ", 4096) + healthyBody()} {
		t.Run(body[:min(40, len(body))], func(t *testing.T) {
			r, o := healthFixture(t, healthReply{Body: body})
			g := healthStart(t, r, o)
			<-g.done
			if _, e := o.Lookup(testHost, testApp); e == nil {
				t.Fatal("rejected health supplied target")
			}
			v := ownerRecord(t, r, testApp)
			if v.State != "failed" || v.LastError.Message != "Service health check failed" {
				t.Fatal(v)
			}
			waitGone(t, g.cmd.Process.Pid)
		})
	}
}
func TestOwnerHealthPersistence(t *testing.T) {
	for _, change := range []string{"success", "running write", "intent", "title", "active", "pending", "removed"} {
		t.Run(change, func(t *testing.T) {
			r, o := healthFixture(t, healthReply{Body: `{}`})
			if change == "success" {
				if _, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "Published", Desired: "stopped", State: "failed", ActiveRelease: "1.0.0", PendingRelease: "2.0.0", LastError: &LastError{Code: "app.unavailable", Message: "Service could not start"}}); e != nil {
					t.Fatal(e)
				}
			}
			g := healthStart(t, r, o)
			before := ownerRecord(t, r, testApp)
			sentinel := errors.New("running write failed")
			if change == "running write" {
				r.mu.Lock()
				r.persist = func(root string, s snapshot) error {
					if s.Records[0].State == "running" {
						return sentinel
					}
					return writeSnapshot(root, s)
				}
				r.mu.Unlock()
			}
			expected := before
			if change == "removed" {
				if _, e := r.Remove(testApp); e != nil {
					t.Fatal(e)
				}
			} else if change != "success" && change != "running write" {
				fields := Lifecycle{Title: before.Title, Desired: before.Desired, State: before.State, ActiveRelease: before.ActiveRelease}
				switch change {
				case "intent":
					fields.Desired = "stopped"
				case "title":
					fields.Title = "New title"
				case "active":
					fields.ActiveRelease = "2.0.0"
				case "pending":
					fields.PendingRelease = "3.0.0"
				}
				if _, e := r.UpdateLifecycle(testHost, testApp, fields); e != nil {
					t.Fatal(e)
				}
				expected = ownerRecord(t, r, testApp)
			}
			healthWrite(t, r, healthReply{Body: healthyBody()})
			if change == "success" {
				target := healthTarget(t, o)
				v := ownerRecord(t, r, testApp)
				if v.State != "running" || v.LastError != nil || v.Title != before.Title || v.ActiveRelease != before.ActiveRelease || v.PendingRelease != before.PendingRelease || v.Desired != before.Desired || v.Revision <= before.Revision {
					t.Fatal(v)
				}
				if e := healthUse(target); e != nil {
					t.Fatal(e)
				}
				return
			}
			<-g.done
			waitGone(t, g.cmd.Process.Pid)
			if _, e := o.Lookup(testHost, testApp); e == nil {
				t.Fatal("stale target")
			}
			if change != "removed" {
				got := ownerRecord(t, r, testApp)
				if (change != "intent" && change != "active" && got.Revision != expected.Revision) || got.Title != expected.Title || got.ActiveRelease != expected.ActiveRelease || got.PendingRelease != expected.PendingRelease || got.Desired != expected.Desired {
					t.Fatal(got, expected)
				}
			}
			if change == "running write" {
				if e := o.Shutdown(); !errors.Is(e, sentinel) {
					t.Fatal(e)
				}
				o.mu.Lock()
				o.failures = nil
				o.mu.Unlock()
				r.mu.Lock()
				r.persist = writeSnapshot
				r.mu.Unlock()
			}
		})
	}
}
func TestOwnerHealthFailures(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	g := healthStart(t, r, o)
	target := healthTarget(t, o)
	for _, n := range []int{1, 2, 0, 1, 2, 3} {
		body := `{}`
		if n == 0 {
			body = healthyBody()
		}
		healthWrite(t, r, healthReply{Body: body})
		healthWait(t, func() bool { o.mu.Lock(); defer o.mu.Unlock(); return g.healthFailures == n })
		if n < 3 {
			if e := healthUse(target); e != nil {
				t.Fatal(n, e)
			}
		}
	}
	<-g.done
	if e := healthUse(target); e == nil {
		t.Fatal("third failure target survived")
	}
	if v := ownerRecord(t, r, testApp); v.State != "failed" {
		t.Fatal(v)
	}
	waitGone(t, g.cmd.Process.Pid)
}
func TestOwnerTargetRetirement(t *testing.T) {
	for _, event := range []string{"stop", "stop write", "shutdown", "cancel", "remove", "release", "intent"} {
		t.Run(event, func(t *testing.T) {
			r, o := healthFixture(t, healthReply{Body: healthyBody()})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if e := o.Start(ctx, testApp); e != nil {
				t.Fatal(e)
			}
			target := healthTarget(t, o)
			g := o.generations[testApp]
			switch event {
			case "stop", "stop write":
				sentinel := errors.New("stop write")
				if event == "stop write" {
					r.mu.Lock()
					r.persist = func(string, snapshot) error { return sentinel }
					r.mu.Unlock()
				}
				e := o.Stop(testApp)
				if event == "stop" && e != nil || event == "stop write" && !errors.Is(e, sentinel) {
					t.Fatal(e)
				}
				r.mu.Lock()
				r.persist = writeSnapshot
				r.mu.Unlock()
			case "shutdown":
				if e := o.Shutdown(); e != nil {
					t.Fatal(e)
				}
			case "cancel":
				cancel()
			case "remove":
				if _, e := r.Remove(testApp); e != nil {
					t.Fatal(e)
				}
			default:
				v := ownerRecord(t, r, testApp)
				f := Lifecycle{Title: v.Title, Desired: v.Desired, State: v.State, ActiveRelease: v.ActiveRelease}
				if event == "release" {
					f.ActiveRelease = "2.0.0"
				} else {
					f.Desired = "stopped"
				}
				if _, e := r.UpdateLifecycle(testHost, testApp, f); e != nil {
					t.Fatal(e)
				}
			}
			if e := healthUse(target); e == nil {
				t.Fatal("retained target usable")
			}
			if _, e := o.Lookup(testHost, testApp); e == nil {
				t.Fatal("lookup survived")
			}
			cancel()
			<-g.done
			waitGone(t, g.cmd.Process.Pid)
		})
	}
}
func TestOwnerHealthExit(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody(), Descendant: true})
	g := healthStart(t, r, o)
	target := healthTarget(t, o)
	desc, _ := strconv.Atoi(string(waitFile(t, filepath.Join(r.root, testApp, "data/descendant"))))
	if e := os.WriteFile(filepath.Join(r.root, testApp, "data/exit"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	<-g.done
	v := ownerRecord(t, r, testApp)
	if v.State != "failed" || v.LastError.Message != "Service exited unexpectedly" {
		t.Fatal(v)
	}
	if e := healthUse(target); e == nil {
		t.Fatal("exit retained readiness")
	}
	waitGone(t, desc)
}

func TestOwnerHealthBounds(t *testing.T) {
	for _, reply := range []healthReply{{Body: healthyBody(), Status: 503}, {Body: healthyBody(), Delay: 200 * time.Millisecond}, {Body: healthyBody(), BodyDelay: 200 * time.Millisecond}, {Body: healthyBody() + strings.Repeat(" ", healthReplyLimit-len(healthyBody())+1)}, {Body: healthyBody() + strings.Repeat(" ", healthReplyLimit-len(healthyBody()))}} {
		t.Run(fmt.Sprint(reply.Status, reply.Delay, reply.BodyDelay, len(reply.Body)), func(t *testing.T) {
			r, o := healthFixture(t, healthReply{Body: `{}`})
			o.startup = 2 * time.Second
			g := healthStart(t, r, o)
			healthWrite(t, r, reply)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			// g's own supervise goroutine is running concurrently and writes
			// g.record under o.mu (see confirmRunning). Snapshot under the
			// same lock so this direct probe doesn't race that write.
			o.mu.Lock()
			snapshot := *g
			o.mu.Unlock()
			got := probeHealth(ctx, privateClient((&net.Dialer{}).DialContext), &snapshot)
			want := reply.Status == 0 && reply.Delay == 0 && reply.BodyDelay == 0 && len(reply.Body) <= healthReplyLimit
			if got != want {
				t.Fatal("probe result", got, want)
			}
			if time.Since(start) > 200*time.Millisecond {
				t.Fatal("request/body escaped deadline")
			}
		})
	}
	r, o := healthFixture(t, healthReply{Body: healthyBody(), BodyDelay: time.Second})
	o.startup = 120 * time.Millisecond
	o.requestTimeout = time.Second
	o.grace = 200 * time.Millisecond
	g := healthStart(t, r, o)
	healthWait(t, func() bool { return ownerRecord(t, r, testApp).State == "failed" })
	if _, e := o.Lookup(testHost, testApp); e == nil {
		t.Fatal("deadline exposed target")
	}
	<-g.done
	waitGone(t, g.cmd.Process.Pid)
}
func TestOwnerHealthTransport(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: `{}`})
	g := healthStart(t, r, o)
	hits := make(chan struct{}, 1)
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits <- struct{}{}; w.Write([]byte(healthyBody())) })}
	go server.Serve(listener)
	defer server.Close()
	healthWrite(t, r, healthReply{Status: 302, Redirect: "http://" + listener.Addr().String(), Body: healthyBody()})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := privateClient((&net.Dialer{}).DialContext)
	if probeHealth(ctx, client, g) {
		t.Fatal("redirect accepted")
	}
	select {
	case <-hits:
		t.Fatal("redirect followed")
	default:
	}
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:8080")
	t.Setenv("NO_PROXY", "")
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableKeepAlives {
		t.Fatal("unsafe transport")
	}
	healthWrite(t, r, healthReply{Body: healthyBody()})
	address := ""
	client = privateClient(func(ctx context.Context, network, addr string) (net.Conn, error) {
		address = addr
		return (&net.Dialer{}).DialContext(ctx, "tcp4", endpoint(g))
	})
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://health.invalid/_pyry/health", nil)
	resp, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if address != "health.invalid:80" {
		t.Fatal("proxy used", address)
	}
}
func TestOwnerProductionHealthTimings(t *testing.T) {
	r := testOpen(t)
	o, e := NewOwner(r, writeNodeDouble(t, "v24.15.0", false))
	if e != nil {
		t.Fatal(e)
	}
	if o.startup != 15*time.Second || o.startInterval != 250*time.Millisecond || o.runtimeInterval != 5*time.Second || o.requestTimeout != time.Second || o.grace != 5*time.Second || healthReplyLimit != 4096 {
		t.Fatal("production timing/bounds changed", o)
	}
	if e = o.Shutdown(); e != nil {
		t.Fatal(e)
	}
}
func TestOwnerTargetReuse(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	g := healthStart(t, r, o)
	old := healthTarget(t, o)
	port := g.port
	revision := g.record.Revision
	probes := filepath.Join(r.root, testApp, "data/probes")
	baseline, _ := os.ReadFile(probes)
	healthWrite(t, r, healthReply{Body: healthyBody(), BodyDelay: time.Second})
	healthWait(t, func() bool { b, _ := os.ReadFile(probes); return len(b) > len(baseline) })
	if e := o.Stop(testApp); e != nil {
		t.Fatal(e)
	}
	o.mu.Lock()
	o.previous[testApp] = 0
	o.listen = func() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port)) }
	o.mu.Unlock()
	next := healthStart(t, r, o)
	fresh := healthTarget(t, o)
	if next.port != port || next == g {
		t.Fatal("reuse scenario absent")
	}
	if e := healthUse(old); e == nil {
		t.Fatal("old target addressed replacement")
	}
	if e := healthUse(fresh); e != nil {
		t.Fatal(e)
	}
	if e := o.confirmRunning(context.Background(), testApp, g); !errors.Is(e, ErrStale) {
		t.Fatal("old health completion", e)
	}
	if _, _, e := r.CommitObservation(testHost, testApp, revision, Observation{State: "failed"}); !errors.Is(e, ErrStale) {
		t.Fatal("old exit revision", e)
	}
	if v := ownerRecord(t, r, testApp); v.State != "running" {
		t.Fatal(v)
	}
}
func TestOwnerPersistedRunningHasNoTarget(t *testing.T) {
	r, o := ownerFixture(t)
	if _, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "App", Desired: "available", State: "running", ActiveRelease: "1.0.0"}); e != nil {
		t.Fatal(e)
	}
	if _, e := o.Lookup(testHost, testApp); e == nil {
		t.Fatal("persisted state supplied target")
	}
	if _, e := o.Lookup(testID(9), testApp); e == nil {
		t.Fatal("foreign host target")
	}
	if e := healthUse(&Target{}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}

type collisionListener struct {
	net.Listener
	collision net.Listener
}

func (l *collisionListener) Close() error {
	err := l.Listener.Close()
	if l.collision == nil {
		l.collision, _ = net.Listen("tcp4", l.Listener.Addr().String())
	}
	return err
}
func TestOwnerHealthBindCollision(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	base, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	listener := &collisionListener{Listener: base}
	o.listen = func() (net.Listener, error) { return listener, nil }
	if e = o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	g := o.generations[testApp]
	<-g.done
	if listener.collision != nil {
		listener.collision.Close()
	}
	v := ownerRecord(t, r, testApp)
	if v.State != "failed" || v.LastError.Message != "Service exited unexpectedly" {
		t.Fatal(v)
	}
	waitGone(t, g.cmd.Process.Pid)
	o.listen = func() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }
	recoveryAdvance(o, time.Second)
	healthWait(t, func() bool { return recoveryGeneration(o, testApp) != g })
	next := recoveryGeneration(o, testApp)
	healthTarget(t, o)
	if g.port == next.port {
		t.Fatal("collision endpoint reused")
	}
}
