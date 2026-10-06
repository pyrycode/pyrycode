package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestControlStartupReadinessWiring(t *testing.T) {
	t.Parallel()
	source := formattedGoFunc(t, "main.go", "runSupervisor")
	if !strings.Contains(source, "ctrlDone <- serveControlWhenReady(ctx, pool.Ready(), controlCtx, ctrl)") {
		t.Fatal("runSupervisor must gate control serving with daemon cancellation and pool readiness")
	}
	if strings.Contains(source, "ctrl.Serve(") {
		t.Fatal("runSupervisor bypasses the readiness gate")
	}
}

func TestControlStartupReadinessCreation(t *testing.T) {
	for _, alreadyReady := range []bool{false, true} {
		name := "held"
		if alreadyReady {
			name = "already_ready"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "sessions.json")
			pool := newRecordingPool(t, path, dir, &workDirRecorder{})
			if alreadyReady {
				runPoolReady(t, pool)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, socket, _, _ := testReadinessControl(t, pool)
			conn, err := net.DialTimeout("unix", socket, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := json.NewEncoder(conn).Encode(control.Request{
				Verb: control.VerbSessionsNew, Sessions: &control.SessionsPayload{Label: "requested"},
			}); err != nil {
				t.Fatal(err)
			}
			if !alreadyReady {
				// Readiness stays open because Run has not been called. The request
				// is queued on the bound socket, rather than racing pool scheduling.
				if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				var response control.Response
				err := json.NewDecoder(conn).Decode(&response)
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Errorf("request handled before readiness: response=%+v err=%v", response, err)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := len(pool.List()); got != 1 || !bytes.Equal(before, after) {
					t.Fatalf("creation before readiness: in-memory sessions=%d registry changed=%v", got, !bytes.Equal(before, after))
				}
				runPoolReady(t, pool)
			}
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var response control.Response
			if err := json.NewDecoder(conn).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Error != "" || response.SessionsNew == nil || response.SessionsNew.SessionID == "" {
				t.Fatalf("ready creation: %+v", response)
			}
			id := sessions.SessionID(response.SessionsNew.SessionID)
			if got := pool.List(); len(got) != 2 {
				t.Fatalf("ready creation left %d in-memory sessions, want bootstrap plus requested", len(got))
			}
			if _, err := pool.Lookup(id); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var registry struct {
				Sessions []struct {
					ID    string `json:"id"`
					Label string `json:"label"`
				} `json:"sessions"`
			}
			if err := json.Unmarshal(raw, &registry); err != nil {
				t.Fatal(err)
			}
			requested := 0
			for _, entry := range registry.Sessions {
				if entry.ID == string(id) && entry.Label == "requested" {
					requested++
				}
			}
			if len(registry.Sessions) != 2 || requested != 1 {
				t.Fatalf("persisted sessions=%+v, want bootstrap plus exactly one requested session", registry.Sessions)
			}
		})
	}
}

func TestControlStartupReadinessCancellation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pool := newRecordingPool(t, filepath.Join(dir, "sessions.json"), dir, &workDirRecorder{})
	srv, socket, cancel, done := testReadinessControl(t, pool)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("startup cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control startup join hung with readiness open")
	}
	// Cancellation of the wait must not release ownership ahead of writers.
	replacement := control.NewServer(socket, poolResolver{pool}, nil, nil, quietLogger(), pool)
	defer replacement.Close()
	if err := replacement.Listen(); !errors.Is(err, control.ErrInstanceRunning) {
		t.Fatalf("socket ownership released by startup wait: %v", err)
	}
	// Mirror runSupervisor's close-before-control-join teardown.
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Listen(); err != nil {
		t.Fatal(err)
	}
}

func testReadinessControl(t *testing.T, pool *sessions.Pool) (*control.Server, string, context.CancelFunc, <-chan error) {
	t.Helper()
	socket := filepath.Join(shortTempDir(t), "control.sock")
	srv := control.NewServer(socket, poolResolver{pool}, nil, nil, quietLogger(), pool)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	controlCtx, controlCancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- serveControlWhenReady(ctx, pool.Ready(), controlCtx, srv); close(done) }()
	t.Cleanup(func() {
		cancel()
		controlCancel()
		_ = srv.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("control teardown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("control teardown hung")
		}
	})
	return srv, socket, cancel, done
}
