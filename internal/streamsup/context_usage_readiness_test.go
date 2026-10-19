package streamsup

import (
	"context"
	"testing"
	"time"
)

func TestContextUsageReadinessRotation(t *testing.T) {
	p := NewParser(nil, discardLogger())
	w := newMCPStatusQueryWriter()
	r := contextUsageQueryRunner(t, p, w)
	firstAbort := r.BeginRotation()
	r.RestartFresh("second")
	// A second arm supersedes the first restart's spawn entitlement.
	r.BeginRotation()
	firstAbort()
	done := make(chan uint64, 1)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go func() {
		generation, ok := r.WaitContextUsageReady(ctx)
		if !ok {
			generation = 0
		}
		done <- generation
	}()
	r.setStdin(w, 1, false)
	select {
	case <-done:
		t.Fatal("ineligible intermediate binding released wait")
	case <-time.After(30 * time.Millisecond):
	}
	r.RestartFresh("third")
	r.setStdin(w, 2, false)
	select {
	case generation := <-done:
		if generation == 0 {
			t.Fatal("entitled binding refused")
		}
	case <-ctx.Done():
		t.Fatal("entitled binding did not release wait")
	}
}

func TestContextUsageReadinessCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			r := contextUsageQueryRunner(t, NewParser(nil, discardLogger()), newMCPStatusQueryWriter())
			r.BeginRotation()
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
			}
			defer cancel()
			done := make(chan bool, 1)
			go func() { _, ok := r.WaitContextUsageReady(ctx); done <- ok }()
			if !deadline {
				cancel()
			}
			select {
			case ok := <-done:
				if ok {
					t.Fatal("cancelled readiness succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("readiness ignored cancellation")
			}
		})
	}
}

func TestContextUsageGenerationQuery(t *testing.T) {
	p := NewParser(nil, discardLogger())
	w := newMCPStatusQueryWriter()
	r := contextUsageQueryRunner(t, p, w)
	generation, ok := r.WaitContextUsageReady(t.Context())
	if !ok {
		t.Fatal("live binding refused")
	}
	r.setStdin(w, 0, false)
	if _, ok := r.QueryContextUsageGeneration(t.Context(), "full", generation); ok || w.count() != 0 {
		t.Fatal("replaced generation queried")
	}
	generation, ok = r.WaitContextUsageReady(t.Context())
	if !ok {
		t.Fatal("replacement refused")
	}
	done := make(chan bool, 1)
	go func() { _, ok := r.QueryContextUsageGeneration(t.Context(), "full", generation); done <- ok }()
	request := awaitContextUsageRequest(t, w)
	r.BeginRotation()
	_, _ = p.Write(contextUsageQueryResponse(t, request.RequestID, controlResponseSuccess, "predecessor"))
	select {
	case ok := <-done:
		if ok {
			t.Fatal("delayed predecessor response accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("query did not settle")
	}
}
