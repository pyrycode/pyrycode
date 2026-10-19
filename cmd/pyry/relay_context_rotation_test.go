package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestContextUsageRotationHelper(t *testing.T) {
	if os.Getenv("PYRY_CONTEXT_ROTATION_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		response := map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": request.RequestID,
			"response": map[string]any{"model": os.Getenv("PYRY_CONTEXT_SESSION"), "totalTokens": 12, "maxTokens": 100, "percentage": 12},
		}}
		if json.NewEncoder(os.Stdout).Encode(response) != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func rotationUsageRunner(t *testing.T, providers ...streamsup.AccountTokenProvider) streamRunner {
	t.Helper()
	var provider streamsup.AccountTokenProvider
	if len(providers) > 0 {
		provider = providers[0]
	}
	p := streamsup.NewParser(func(turnevent.Event) {}, discardLogger())
	dir := t.TempDir()
	child := filepath.Join(dir, "helper.sh")
	launcher := "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=^TestContextUsageRotationHelper$ -- \"$@\"\n"
	if err := os.WriteFile(child, []byte(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := streamsup.New(streamsup.Config{ClaudeBin: child, WorkDir: dir, SessionID: "a",
		Env: []string{"PYRY_CONTEXT_ROTATION_HELPER=1"}, SessionIDEnvVar: "PYRY_CONTEXT_SESSION", Stdout: p, Logger: discardLogger(), AccountTokenProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return streamRunner{r: r}
}

func rotationUsageResolver(t *testing.T, base context.Context, runner contextUsageQuerier, memory ...bool) (*contextUsageResolver, *daemonLiveBindings) {
	t.Helper()
	b := testControlBindings(t, "conv", "a")
	r := newContextUsageResolver(base, func(id string) (contextUsageQuerier, conversations.ConversationID, bool) {
		if id != "conv" {
			return nil, "", false
		}
		return runner, "conv", true
	}, nil, nil)
	r.live = b
	r.rec = &contextUsageRecorder{reg: b.reg, path: filepath.Join(t.TempDir(), "registry.json"), logger: discardLogger()}
	if len(memory) == 0 || memory[0] {
		r.rec.record("conv", turnevent.ContextUsage{Model: "predecessor", TotalTokens: 90, MaxTokens: 100, Percentage: 90}, b.capture("conv", "a", false))
	}
	return r, b
}

func rotateUsageBinding(b *daemonLiveBindings, old, next string) {
	b.sink.offerMu.Lock()
	defer b.sink.offerMu.Unlock()
	b.reg.RebindSession(old, next)
	b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: sessions.SessionID(old), NewID: sessions.SessionID(next), NextAgent: "claude"})
}

func rotationUsagePersisted(t *testing.T, r *contextUsageResolver, id conversations.ConversationID, model string) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		reg, err := conversations.Load(r.rec.path)
		if err == nil {
			if row, ok := reg.Get(id); ok && row.LastContextUsage != nil && row.LastContextUsage.Model == model {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("fresh memory was not persisted")
}

func TestContextUsageRotationDelivery(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "supplied"}[supplied], func(t *testing.T) {
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			release, held := make(chan struct{}), make(chan struct{}, 1)
			var launches atomic.Int32
			runner := rotationUsageRunner(t, func(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
				if launches.Add(1) > 1 {
					held <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						return "", streamsup.AccountTokenCancellation, ctx.Err()
					}
				}
				return "fixture-token", "", nil
			})
			runDone := make(chan error, 1)
			go func() { runDone <- runner.Run(base) }()
			t.Cleanup(func() { cancel(); <-runDone })
			until := time.Now().Add(3 * time.Second)
			for runner.r.Stdin() == nil && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			if runner.r.Stdin() == nil {
				t.Fatal("predecessor did not bind")
			}
			runner.BeginRotation()
			r, b := rotationUsageResolver(t, base, runner)
			resolve := r.resolve
			resolved := make(chan struct{}, 8)
			r.resolve = func(id string) (contextUsageQuerier, conversations.ConversationID, bool) {
				q, canonical, ok := resolve(id)
				resolved <- struct{}{}
				return q, canonical, ok
			}
			cfg := relay.V2SessionConfig{}
			cfg.ContextUsageFor = r.Get
			installDaemonLiveRelay(&cfg, relayWiring{streamSink: b.sink}, r)
			type answer struct {
				payload protocol.ContextUsagePayload
				reading relay.LiveState
				ok      bool
			}
			done := make(chan answer, 1)
			go func() {
				var a answer
				if supplied {
					a.reading, a.ok = cfg.ContextUsageReadingFor(base, "conv")
					_ = json.Unmarshal(a.reading.Envelope.Payload, &a.payload)
				} else {
					a.payload, a.ok = cfg.ContextUsageFor(base, "conv")
				}
				done <- a
			}()
			<-resolved
			select {
			case a := <-done:
				t.Fatalf("request answered before successor binding: %+v", a)
			case <-time.After(100 * time.Millisecond):
			}
			// The request captured predecessor provenance before the transition.
			rotateUsageBinding(b, "a", "b")
			runner.RestartFresh("b")
			select {
			case <-held:
			case <-time.After(3 * time.Second):
				t.Fatal("successor binding was not held")
			}
			select {
			case <-done:
				t.Fatal("held successor answered")
			default:
			}
			r.mu.Lock()
			firstFlight := r.flights["conv"]
			r.mu.Unlock()
			joined := make(chan bool, 1)
			go func() { payload, ok := cfg.ContextUsageFor(base, "conv"); joined <- ok && payload.Model == "b" }()
			<-resolved
			b.sink.offerMu.Lock()
			r.mu.Lock()
			sameFlight := r.flights["conv"] == firstFlight
			r.mu.Unlock()
			b.sink.offerMu.Unlock()
			if !sameFlight {
				t.Fatal("successor request replaced the shared readiness flight")
			}
			close(release)
			select {
			case a := <-done:
				if !a.ok || a.payload.Model != "b" || a.payload.AsOf != nil {
					t.Fatalf("successor answer = %+v", a)
				}
				if supplied {
					src := b.capture("conv", "b", false)
					if string(a.reading.Envelope.SessionID) != `"b"` || a.reading.SessionGeneration != src.SessionGeneration || !cfg.LiveStateCurrent(a.reading) {
						t.Fatalf("wrong successor source: %+v", a.reading)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("request did not complete after binding")
			}
			if !<-joined {
				t.Fatal("shared successor caller lost reading")
			}
			// A second request collapses within the successor generation and keeps correlation.
			n := uint64(73)
			reading, ok := r.GetLive(base, "conv", protocol.Envelope{InReplyTo: &n})
			if !ok || string(reading.Envelope.SessionID) != `"b"` || reading.Envelope.InReplyTo == nil || *reading.Envelope.InReplyTo != n {
				t.Fatal("collapsed successor lost source/correlation")
			}
			rotationUsagePersisted(t, r, "conv", "b")
		})
	}
}

func TestContextUsageRotationCancellation(t *testing.T) {
	base, stop := context.WithCancel(t.Context())
	defer stop()
	runner := rotationUsageRunner(t)
	runner.BeginRotation()
	r, b := rotationUsageResolver(t, base, runner)
	otherQuery := newFakeContextUsageQuerier(true)
	b.reg.Create(conversations.Conversation{ID: "other", CurrentSessionID: "other-session"})
	r.resolve = func(id string) (contextUsageQuerier, conversations.ConversationID, bool) {
		if id == "other" {
			return otherQuery, "other", true
		}
		return runner, "conv", id == "conv"
	}
	stored, _, _ := r.remembered("conv")
	caller, cancel := context.WithCancel(base)
	first := make(chan bool, 1)
	go func() { _, ok := r.Get(caller, "conv"); first <- ok }()
	// Wait until the flight exists before cancelling its installing caller.
	deadline := time.After(time.Second)
	for {
		r.mu.Lock()
		flight := r.flights["conv"]
		r.mu.Unlock()
		if flight != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("flight not launched")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if <-first {
		t.Fatal("cancelled caller received a successful memory answer")
	}
	second := make(chan protocol.ContextUsagePayload, 1)
	go func() { payload, _ := r.Get(t.Context(), "conv"); second <- payload }()
	select {
	case <-second:
		t.Fatal("caller cancellation ended the shared flight")
	case <-time.After(30 * time.Millisecond):
	}
	// An unrelated conversation can still answer while this one waits.
	if _, ok := r.Get(base, "other"); !ok {
		t.Fatal("other conversation blocked")
	}
	stop()
	select {
	case fallback := <-second:
		if fallback.AsOf == nil || !fallback.AsOf.Equal(*stored.AsOf) || fallback.Model != "predecessor" {
			t.Fatal("shutdown fallback lost original memory timestamp")
		}
	case <-time.After(time.Second):
		t.Fatal("daemon shutdown did not end readiness")
	}
	rotationUsagePersisted(t, r, "other", otherQuery.usage.Model)
}

func TestContextUsageRotationStaleFlight(t *testing.T) {
	first := newFakeContextUsageQuerier(true)
	first.block = make(chan struct{})
	var current contextUsageQuerier = first
	r, b := rotationUsageResolver(t, t.Context(), first)
	r.resolve = func(string) (contextUsageQuerier, conversations.ConversationID, bool) { return current, "conv", true }
	type answer struct {
		payload protocol.ContextUsagePayload
		ok      bool
	}
	oldDone := make(chan answer, 1)
	go func() { payload, ok := r.Get(t.Context(), "conv"); oldDone <- answer{payload, ok} }()
	<-first.details
	second := newFakeContextUsageQuerier(true)
	second.usage.Model = "successor"
	b.sink.offerMu.Lock()
	b.reg.RebindSession("a", "b")
	b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "b", NextAgent: "claude"})
	current = second
	b.sink.offerMu.Unlock()
	if payload, ok := r.Get(t.Context(), "conv"); !ok || payload.Model != "successor" {
		t.Fatal("successor joined predecessor")
	}
	close(first.block)
	old := <-oldDone
	// Settlement includes memory mutation, so completion is a barrier for this assertion.
	reading, source, ok := r.rec.snapshot("conv")
	if !ok || reading.Model != "successor" || source.provenance.SessionID != "b" {
		t.Fatalf("predecessor overwrote memory: %+v %+v", reading, source)
	}
	if !old.ok || old.payload.Model != reading.Model || old.payload.AsOf == nil || !old.payload.AsOf.Equal(reading.AsOf) {
		t.Errorf("overtaken legacy caller did not receive remembered successor: %+v", old)
	}
	rotationUsagePersisted(t, r, "conv", "successor")
}

type rotationUsageAfterQuery struct {
	contextUsageQuerier
	after func()
}

func (q rotationUsageAfterQuery) QueryContextUsage(ctx context.Context, detail string) (turnevent.ContextUsage, bool) {
	usage, ok := q.contextUsageQuerier.QueryContextUsage(ctx, detail)
	if ok {
		q.after()
	}
	return usage, ok
}

func TestContextUsageRotationBeforeSettlement(t *testing.T) {
	for _, memory := range []bool{false, true} {
		for _, supplied := range []bool{false, true} {
			name := map[bool]string{false: "unavailable", true: "remembered"}[memory] + "/" + map[bool]string{false: "legacy", true: "supplied"}[supplied]
			t.Run(name, func(t *testing.T) {
				query := newFakeContextUsageQuerier(true)
				r, b := rotationUsageResolver(t, t.Context(), query, memory)
				stored, source, found := r.rec.snapshot("conv")
				// Reset after query success, before the resolver can settle it. No
				// successor request replaces the flight: the source check must reject it.
				querier := rotationUsageAfterQuery{query, func() { rotateUsageBinding(b, "a", "b") }}
				r.resolve = func(string) (contextUsageQuerier, conversations.ConversationID, bool) { return querier, "conv", true }
				cfg := relay.V2SessionConfig{ContextUsageFor: r.Get}
				installDaemonLiveRelay(&cfg, relayWiring{streamSink: b.sink}, r)
				var payload protocol.ContextUsagePayload
				var reading relay.LiveState
				var ok bool
				if supplied {
					reading, ok = cfg.ContextUsageReadingFor(t.Context(), "conv")
					if ok {
						if err := json.Unmarshal(reading.Envelope.Payload, &payload); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					payload, ok = cfg.ContextUsageFor(t.Context(), "conv")
				}
				if ok != memory || (memory && (payload.Model != stored.Model || payload.AsOf == nil || !payload.AsOf.Equal(stored.AsOf))) {
					t.Fatalf("overtaken response = %+v, ok=%v; memory=%v", payload, ok, memory)
				}
				if supplied && memory && (string(reading.Envelope.SessionID) != `"a"` || reading.SessionGeneration != source.SessionGeneration || cfg.LiveStateCurrent(reading)) {
					t.Fatalf("fallback lost predecessor source or became current: %+v", reading)
				}
				after, producer, exists := r.rec.snapshot("conv")
				if exists != found || after != stored || producer != source {
					t.Fatal("overtaken query changed remembered bytes or producer")
				}
				if retained, found := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeContextUsage, ""}]; found && !retained.Envelope.SessionStateCleared {
					t.Fatal("overtaken query published as successor live state")
				}
			})
		}
	}
}

func TestContextUsageRotationTimeout(t *testing.T) {
	runner := rotationUsageRunner(t)
	runner.BeginRotation()
	r, b := rotationUsageResolver(t, t.Context(), runner)
	stored, source, _ := r.rec.snapshot("conv")
	rotateUsageBinding(b, "a", "b")
	started := time.Now()
	reading, ok := r.GetLive(t.Context(), "conv", protocol.Envelope{})
	if !ok {
		t.Fatal("timeout lost remembered fallback")
	}
	var payload protocol.ContextUsagePayload
	if err := json.Unmarshal(reading.Envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < contextUsageQueryTimeout || elapsed > contextUsageQueryTimeout+5*time.Second {
		t.Fatalf("readiness deadline = %v", elapsed)
	}
	if payload.AsOf == nil || !payload.AsOf.Equal(stored.AsOf) || payload.Model != "predecessor" || string(reading.Envelope.SessionID) != `"a"` || reading.SessionGeneration != source.SessionGeneration {
		t.Fatal("fallback renewed or relabelled predecessor")
	}
	after, _, _ := r.rec.snapshot("conv")
	if !after.AsOf.Equal(stored.AsOf) {
		t.Fatal("fallback re-recorded memory")
	}
}
