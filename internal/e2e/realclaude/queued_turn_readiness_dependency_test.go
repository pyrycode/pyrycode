//go:build e2e_realclaude

package realclaude

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestReadinessDependencyRelease(t *testing.T) {
	service := readinessDependencyService(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Claude may enter either role first and invoke the other only after it exits.
	for _, role := range []string{"MCP", "SessionStart"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", service+"/"+role+"?child=first", nil)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("sequential role blocked on the other role")
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("independent ready service did not release role")
		}
	}
}

func TestReadinessDependencyChildIsolation(t *testing.T) {
	r := &readinessCapture{Setup: map[string]any{}}
	t.Run("lifecycles", func(t *testing.T) {
		service := readinessDependencyService(t, r)
		for _, child := range []string{"daemon-1-child-1", "daemon-1-child-2", "daemon-2-child-1"} {
			for _, role := range []string{"MCP", "SessionStart"} {
				response, err := http.Get(service + "/" + role + "?child=" + child)
				if err != nil {
					t.Fatal("child readiness unavailable")
				}
				response.Body.Close()
				if response.StatusCode != 200 {
					t.Fatal("child readiness failed")
				}
			}
		}
	})
	starts := map[string]int{}
	for _, event := range r.Startup {
		if event.Phase == "service_started" {
			starts[event.ChildKey]++
		}
	}
	if len(starts) != 3 {
		t.Fatalf("independent lifecycles = %d, want 3", len(starts))
	}
	for _, count := range starts {
		if count != 1 {
			t.Fatal("child service reset between roles")
		}
	}
}

func TestReadinessDependencyCancellation(t *testing.T) {
	service := readinessDependencyService(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", service+"/MCP?child=cancelled", nil)
	if response, err := http.DefaultClient.Do(req); err == nil {
		response.Body.Close()
		t.Fatal("cancelled request unexpectedly completed")
	}
	response, err := http.Get(service + "/MCP")
	if err != nil {
		t.Fatal("fixture unavailable")
	}
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal("missing child manufactured a lifecycle")
	}
}

func readinessDependencyService(t *testing.T, r *readinessCapture) string {
	t.Helper()
	var mu sync.Mutex
	type lifecycle struct {
		ready chan struct{}
		ok    bool
	}
	children := map[string]*lifecycle{}
	observations := []readinessStartupEvent{}
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	note := func(child, role, phase string) {
		observations = append(observations, readinessStartupEvent{At: time.Now().UTC(), ChildKey: child, Role: role, Phase: phase})
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		role, child := req.URL.Path[1:], req.URL.Query().Get("child")
		if (role != "MCP" && role != "SessionStart") || child == "" || len(child) > 100 {
			http.Error(w, "invalid fixture identity", 400)
			return
		}
		mu.Lock()
		note(child, role, "dependent_entry")
		state := children[child]
		if state == nil {
			state = &lifecycle{ready: make(chan struct{})}
			children[child] = state
			note(child, "service", "service_started")
			workers.Add(1)
			go func() {
				defer workers.Done()
				// This independently served child-specific health check supplies readiness.
				health := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.URL.Path != "/ready" {
						http.NotFound(w, req)
						return
					}
					_, _ = io.WriteString(w, "ready")
				}))
				health.Config.ReadHeaderTimeout = time.Second
				health.Config.ReadTimeout, health.Config.WriteTimeout = time.Second, time.Second
				health.Config.IdleTimeout = time.Second
				health.Start()
				defer health.Close()
				attempt, stop := context.WithTimeout(ctx, 10*time.Second)
				defer stop()
				request, _ := http.NewRequestWithContext(attempt, "GET", health.URL+"/ready", nil)
				response, err := http.DefaultClient.Do(request)
				ok := false
				if err == nil {
					body, e := io.ReadAll(io.LimitReader(response.Body, 16))
					response.Body.Close()
					ok = e == nil && response.StatusCode == 200 && string(body) == "ready"
				}
				mu.Lock()
				state.ok = ok
				phase := "service_readiness_failed"
				if ok {
					phase = "service_readiness_checked"
				}
				note(child, "service", phase)
				close(state.ready)
				mu.Unlock()
			}()
		}
		mu.Unlock()
		bounded, stop := context.WithTimeout(req.Context(), 10*time.Second)
		defer stop()
		phase, status := "dependency_timeout", http.StatusGatewayTimeout
		select {
		case <-state.ready:
			if state.ok {
				phase, status = "ready_release", http.StatusOK
			}
		case <-ctx.Done():
			phase, status = "fixture_cancelled", http.StatusServiceUnavailable
		case <-bounded.Done():
		}
		mu.Lock()
		note(child, role, phase)
		mu.Unlock()
		w.Header().Set("Content-Length", strconv.Itoa(0))
		w.WriteHeader(status)
	}))
	server.Config.ReadHeaderTimeout = time.Second
	server.Config.ReadTimeout, server.Config.WriteTimeout = 10*time.Second, 10*time.Second
	server.Config.IdleTimeout = time.Second
	server.Start()
	t.Cleanup(func() {
		cancel()
		server.Close()
		workers.Wait()
		mu.Lock()
		defer mu.Unlock()
		if r != nil {
			r.Startup = append(r.Startup, observations...)
		}
	})
	return server.URL
}
