//go:build e2e_realclaude

package realclaude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestReadinessDependencyRelease(t *testing.T) {
	service := readinessDependencyService(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first := make(chan int, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "GET", service+"/MCP", nil)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			first <- 0
			return
		}
		defer response.Body.Close()
		first <- response.StatusCode
	}()
	select {
	case <-first:
		t.Fatal("dependency released before both entries")
	default:
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", service+"/SessionStart", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("service release failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || <-first != 200 {
		t.Fatal("ready service did not release both dependencies")
	}
}

func readinessDependencyService(t *testing.T, r *readinessCapture) string {
	t.Helper()
	var mu sync.Mutex
	entered := map[string]bool{}
	observations := []map[string]any{}
	ready := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		role := req.URL.Path[1:]
		if role != "MCP" && role != "SessionStart" {
			http.Error(w, "unknown fixture role", 400)
			return
		}
		mu.Lock()
		observations = append(observations, map[string]any{"role": role, "phase": "dependent_entry", "at": time.Now().UTC()})
		entered[role] = true
		if len(entered) == 2 {
			select {
			case <-ready:
			default:
				observations = append(observations, map[string]any{"role": "service", "phase": "ready_release", "at": time.Now().UTC()})
				close(ready)
			}
		}
		mu.Unlock()
		select {
		case <-ready:
			w.WriteHeader(http.StatusOK)
		case <-stopped:
			http.Error(w, "fixture stopped", 503)
		case <-req.Context().Done():
		}
	}))
	server.Config.ReadHeaderTimeout = time.Second
	server.Config.ReadTimeout = 10 * time.Second
	server.Config.WriteTimeout = 10 * time.Second
	server.Config.IdleTimeout = time.Second
	server.Start()
	t.Cleanup(func() {
		close(stopped)
		server.Close()
		mu.Lock()
		defer mu.Unlock()
		if r != nil {
			r.Setup["dependency_service_observations"] = observations
		}
	})
	return server.URL
}
