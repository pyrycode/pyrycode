package permbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// awaitWithin blocks on p.Await in a goroutine and fails the test if no verdict
// arrives within d. It proves Await returns within a bound (the timeout path is
// what guarantees it never hangs forever).
func awaitWithin(t *testing.T, p *Pending, d time.Duration) Verdict {
	t.Helper()
	done := make(chan Verdict, 1)
	go func() { done <- p.Await() }()
	select {
	case v := <-done:
		return v
	case <-time.After(d):
		t.Fatalf("Await did not return within %v", d)
		return Verdict{}
	}
}

// registryLen reads the live pending count under the registry lock. Same-package
// test access to the unexported map is how we assert entries are retired.
func registryLen(r *Registry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// waitRetired polls Lookup until id is gone (or fails after d), so a test can
// wait out a fail-closed timer without a fixed sleep.
func waitRetired(t *testing.T, r *Registry, id string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, ok := r.Lookup(id); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("entry %q not retired within %v", id, d)
}

func sampleRequest(id string) Request {
	return Request{
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"ls -la"}`),
		ToolUseID: id,
	}
}

func TestRegistry_AllowPath(t *testing.T) {
	t.Parallel()
	r := New()
	req := sampleRequest("allow-1")

	p, err := r.Register("allow-1", req, time.Second)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	resolvedCh := make(chan bool, 1)
	go func() { resolvedCh <- r.Resolve("allow-1", Allow(req.Input)) }()

	v := awaitWithin(t, p, time.Second)
	if v.Behavior != BehaviorAllow {
		t.Fatalf("Behavior = %q, want %q", v.Behavior, BehaviorAllow)
	}
	if string(v.UpdatedInput) != string(req.Input) {
		t.Fatalf("UpdatedInput = %s, want %s", v.UpdatedInput, req.Input)
	}
	if v.Message != "" {
		t.Fatalf("Message = %q, want empty on allow", v.Message)
	}
	// The channel handoff synchronizes with the resolver goroutine before we read
	// its result and confirm the entry retired.
	if resolved := <-resolvedCh; !resolved {
		t.Fatal("Resolve returned false, want true for a live entry")
	}
	waitRetired(t, r, "allow-1", time.Second)
	if _, ok := r.Lookup("allow-1"); ok {
		t.Fatal("Lookup still finds a resolved entry")
	}
}

func TestRegistry_DenyPath(t *testing.T) {
	t.Parallel()
	r := New()
	req := sampleRequest("deny-1")

	p, err := r.Register("deny-1", req, time.Second)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	go func() { r.Resolve("deny-1", Deny("nope")) }()

	v := awaitWithin(t, p, time.Second)
	if v.Behavior != BehaviorDeny {
		t.Fatalf("Behavior = %q, want %q", v.Behavior, BehaviorDeny)
	}
	if v.Message != "nope" {
		t.Fatalf("Message = %q, want %q", v.Message, "nope")
	}
	if len(v.UpdatedInput) != 0 {
		t.Fatalf("UpdatedInput = %s, want empty on deny", v.UpdatedInput)
	}
	waitRetired(t, r, "deny-1", time.Second)
}

func TestRegistry_TimeoutPathDenies(t *testing.T) {
	t.Parallel()
	r := New()
	req := sampleRequest("timeout-1")

	p, err := r.Register("timeout-1", req, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	v := awaitWithin(t, p, time.Second)
	if v.Behavior != BehaviorDeny {
		t.Fatalf("Behavior = %q, want %q (fail-closed)", v.Behavior, BehaviorDeny)
	}
	if v.Message != reasonTimeout {
		t.Fatalf("Message = %q, want %q", v.Message, reasonTimeout)
	}
	waitRetired(t, r, "timeout-1", time.Second)
}

func TestRegistry_LateResolveDoesNotFlipDeny(t *testing.T) {
	t.Parallel()
	r := New()
	req := sampleRequest("late-1")

	p, err := r.Register("late-1", req, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	v := awaitWithin(t, p, time.Second)
	if v.Behavior != BehaviorDeny {
		t.Fatalf("Behavior = %q, want deny before late resolve", v.Behavior)
	}
	waitRetired(t, r, "late-1", time.Second)

	// A resolve arriving after the deadline must be a no-op — it cannot flip the
	// already-delivered deny to allow (AC-3, the security-critical invariant).
	if r.Resolve("late-1", Allow(req.Input)) {
		t.Fatal("Resolve after timeout returned true, want false (no flip)")
	}
	if v.Behavior != BehaviorDeny {
		t.Fatalf("delivered verdict mutated to %q after late resolve", v.Behavior)
	}
}

// TestRegistry_AllowVsTimeoutRace is the deterministic proof of the one-shot: a
// resolve racing the fail-closed timer must yield allow IFF the resolve won, and
// deny otherwise — never a second delivery, never allow-after-deny. Runs under
// -race.
func TestRegistry_AllowVsTimeoutRace(t *testing.T) {
	t.Parallel()
	for i := 0; i < 300; i++ {
		r := New()
		id := fmt.Sprintf("race-%d", i)
		req := sampleRequest(id)

		p, err := r.Register(id, req, time.Millisecond)
		if err != nil {
			t.Fatalf("Register: %v", err)
		}

		var resolvedOK atomic.Bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolvedOK.Store(r.Resolve(id, Allow(req.Input)))
		}()

		v := p.Await()
		wg.Wait()

		if resolvedOK.Load() {
			if v.Behavior != BehaviorAllow {
				t.Fatalf("iter %d: Resolve won but verdict = %q, want allow", i, v.Behavior)
			}
		} else {
			if v.Behavior != BehaviorDeny {
				t.Fatalf("iter %d: Resolve lost but verdict = %q, want deny", i, v.Behavior)
			}
		}

		// Exactly one verdict is ever delivered: the buffer must be empty now.
		select {
		case extra := <-p.ch:
			t.Fatalf("iter %d: a second verdict was delivered: %+v", i, extra)
		default:
		}
	}
}

func TestRegistry_UnknownIDIsNoOp(t *testing.T) {
	t.Parallel()
	r := New()

	if r.Resolve("nope", Allow(nil)) {
		t.Fatal("Resolve of unknown id returned true, want false")
	}
	if req, ok := r.Lookup("nope"); ok {
		t.Fatalf("Lookup of unknown id returned (%+v, true), want (Request{}, false)", req)
	}
	if n := registryLen(r); n != 0 {
		t.Fatalf("registry length = %d after unknown-id ops, want 0", n)
	}
}

func TestRegistry_DuplicateAndEmptyID(t *testing.T) {
	t.Parallel()
	r := New()
	req := sampleRequest("dup-1")

	p, err := r.Register("dup-1", req, time.Second)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := r.Register("dup-1", req, time.Second); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate Register err = %v, want ErrDuplicateID", err)
	}
	if _, err := r.Register("", req, time.Second); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("empty-id Register err = %v, want ErrDuplicateID", err)
	}

	// The first, still-pending entry is unaffected and remains resolvable.
	if !r.Resolve("dup-1", Allow(req.Input)) {
		t.Fatal("first entry no longer resolvable after rejected duplicate")
	}
	if v := awaitWithin(t, p, time.Second); v.Behavior != BehaviorAllow {
		t.Fatalf("Behavior = %q, want allow", v.Behavior)
	}
}

func TestRegistry_LostCallerSelfCleans(t *testing.T) {
	t.Parallel()
	r := New()
	req := sampleRequest("lost-1")

	// Register and never Await — the fail-closed timer must retire the entry on
	// its own, leaving no leaked pending entry (AC-4).
	if _, err := r.Register("lost-1", req, 20*time.Millisecond); err != nil {
		t.Fatalf("Register: %v", err)
	}

	waitRetired(t, r, "lost-1", time.Second)
	if n := registryLen(r); n != 0 {
		t.Fatalf("registry length = %d after lost-caller timeout, want 0", n)
	}
}

func TestRegistry_LookupReturnsParkedRequest(t *testing.T) {
	t.Parallel()
	r := New()
	req := sampleRequest("look-1")

	if _, err := r.Register("look-1", req, time.Second); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := r.Lookup("look-1")
	if !ok {
		t.Fatal("Lookup of a pending entry returned false")
	}
	if got.ToolName != req.ToolName || got.ToolUseID != req.ToolUseID || string(got.Input) != string(req.Input) {
		t.Fatalf("Lookup = %+v, want %+v", got, req)
	}
	// Lookup does not retire: the entry is still pending afterward.
	if _, ok := r.Lookup("look-1"); !ok {
		t.Fatal("entry retired by Lookup, want still pending")
	}
}

func TestVerdict_MarshalShape(t *testing.T) {
	t.Parallel()

	allow, err := json.Marshal(Allow(json.RawMessage(`{"command":"ls"}`)))
	if err != nil {
		t.Fatalf("marshal allow: %v", err)
	}
	as := string(allow)
	if !strings.Contains(as, `"behavior":"allow"`) {
		t.Fatalf("allow verdict %s missing behavior:allow", as)
	}
	if !strings.Contains(as, `"updatedInput"`) {
		t.Fatalf("allow verdict %s missing updatedInput", as)
	}
	if strings.Contains(as, `"message"`) {
		t.Fatalf("allow verdict %s must omit message", as)
	}

	deny, err := json.Marshal(Deny("blocked"))
	if err != nil {
		t.Fatalf("marshal deny: %v", err)
	}
	ds := string(deny)
	if !strings.Contains(ds, `"behavior":"deny"`) {
		t.Fatalf("deny verdict %s missing behavior:deny", ds)
	}
	if !strings.Contains(ds, `"message":"blocked"`) {
		t.Fatalf("deny verdict %s missing message", ds)
	}
	if strings.Contains(ds, `"updatedInput"`) {
		t.Fatalf("deny verdict %s must omit updatedInput", ds)
	}
}
