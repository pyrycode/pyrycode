package permbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

func permissionSuggestionBatch(rules int, toolName string) json.RawMessage {
	entries := make([]string, rules)
	for i := range entries {
		entries[i] = `{"toolName":"` + toolName + `"}`
	}
	return json.RawMessage(`[{"type":"addRules","behavior":"allow","rules":[` + strings.Join(entries, ",") + `]}]`)
}

func TestParseAlwaysAllow(t *testing.T) {
	t.Parallel()
	empty := ""
	readRule := "//src/**"
	valid := json.RawMessage(`[` +
		`{"type":"addRules","behavior":"allow","rules":[{"toolName":"Bash"},{"toolName":"Read","ruleContent":"//src/**"}]},` +
		`{"type":"addRules","behavior":"allow","rules":[{"toolName":"Write","ruleContent":""}]}` +
		`]`)

	offer := ParseAlwaysAllow(valid, false)
	if !offer.Offered() {
		t.Fatal("valid suggestions were not offered")
	}
	if got, want := offer.Rules(), []string{"Bash", "Read(//src/**)", "Write()"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered rules = %v, want %v", got, want)
	}
	wantUpdates := []PermissionUpdate{
		{Type: "addRules", Behavior: "allow", Rules: []PermissionRule{{ToolName: "Bash"}, {ToolName: "Read", RuleContent: &readRule}}},
		{Type: "addRules", Behavior: "allow", Rules: []PermissionRule{{ToolName: "Write", RuleContent: &empty}}},
	}
	if got := offer.Updates(); !reflect.DeepEqual(got, wantUpdates) {
		t.Fatalf("updates = %#v, want %#v", got, wantUpdates)
	}

	// Both accessors return deep copies: a future grant path cannot mutate the
	// validated offer retained by the outstanding modal.
	rules := offer.Rules()
	rules[0] = "changed"
	updates := offer.Updates()
	updates[0].Rules[1].ToolName = "changed"
	*updates[0].Rules[1].RuleContent = "changed"
	if got := offer.Rules(); !reflect.DeepEqual(got, []string{"Bash", "Read(//src/**)", "Write()"}) {
		t.Fatalf("mutating accessor results changed offer: %v", got)
	}

	overRaw := json.RawMessage(`[{"type":"addRules","behavior":"allow","padding":"` + strings.Repeat("x", 16<<10) + `","rules":[{"toolName":"Bash"}]}]`)
	cases := []struct {
		name       string
		raw        json.RawMessage
		suppressed bool
	}{
		{name: "absent"},
		{name: "null", raw: json.RawMessage(`null`)},
		{name: "empty", raw: json.RawMessage(`[]`)},
		{name: "suppressed", raw: valid, suppressed: true},
		{name: "malformed", raw: json.RawMessage(`[{`)},
		{name: "over raw byte bound", raw: overRaw},
		{name: "unsupported type", raw: json.RawMessage(`[{"type":"removeRules","behavior":"allow","rules":[{"toolName":"Bash"}]}]`)},
		{name: "unsupported behavior", raw: json.RawMessage(`[{"type":"addRules","behavior":"deny","rules":[{"toolName":"Bash"}]}]`)},
		{name: "missing rules", raw: json.RawMessage(`[{"type":"addRules","behavior":"allow"}]`)},
		{name: "empty rules", raw: json.RawMessage(`[{"type":"addRules","behavior":"allow","rules":[]}]`)},
		{name: "too many rules", raw: permissionSuggestionBatch(17, "Bash")},
		{name: "empty tool name", raw: permissionSuggestionBatch(1, "")},
		{name: "wrong tool name type", raw: json.RawMessage(`[{"type":"addRules","behavior":"allow","rules":[{"toolName":7}]}]`)},
		{name: "null rule content", raw: json.RawMessage(`[{"type":"addRules","behavior":"allow","rules":[{"toolName":"Bash","ruleContent":null}]}]`)},
		{name: "wrong rule content type", raw: json.RawMessage(`[{"type":"addRules","behavior":"allow","rules":[{"toolName":"Bash","ruleContent":{}}]}]`)},
		{name: "rendered rule over byte bound", raw: permissionSuggestionBatch(1, strings.Repeat("x", 1025))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseAlwaysAllow(tc.raw, tc.suppressed)
			if got.Offered() || got.Rules() != nil || got.Updates() != nil {
				t.Errorf("rejected suggestions returned partial offer: offered=%v rules=%v updates=%v", got.Offered(), got.Rules(), got.Updates())
			}
		})
	}

	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "sixteen rules", raw: permissionSuggestionBatch(16, "Bash")},
		{name: "rendered rule exactly 1024 bytes", raw: permissionSuggestionBatch(1, strings.Repeat("x", 1024))},
		{name: "unknown keys tolerated", raw: json.RawMessage(`[{"type":"addRules","behavior":"allow","future":true,"rules":[{"toolName":"Bash","future":"value"}]}]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseAlwaysAllow(tc.raw, false); !got.Offered() {
				t.Errorf("boundary-valid suggestions unavailable: %s", tc.raw)
			}
		})
	}
}

// scriptedAnswerable is the shared AnswerableFunc double. It counts every ask —
// the non-vacuity guard for the extension scenarios, where "still parked" alone
// would also pass if the timer had never fired at all — and replies from a
// programmable sequence.
type scriptedAnswerable struct {
	calls atomic.Int64
	// answers[i] replies to the (i+1)-th ask; the last entry repeats for every
	// later ask. An empty slice answers true forever.
	answers []bool
	// hook, when non-nil, runs after the ask is counted and before the answer is
	// returned — the seam for a re-entrant or a deliberately slow report.
	hook func(id string, req Request)
}

func (s *scriptedAnswerable) ask(id string, req Request) bool {
	n := int(s.calls.Add(1))
	if s.hook != nil {
		s.hook(id, req)
	}
	if len(s.answers) == 0 {
		return true
	}
	if n > len(s.answers) {
		n = len(s.answers)
	}
	return s.answers[n-1]
}

// waitAsks polls the scripted report's counter until it has been asked n times
// (or fails after d), so a test can wait out one or more windows without a fixed
// sleep — the awaitWithin/waitRetired idiom applied to the liveness question.
func waitAsks(t *testing.T, s *scriptedAnswerable, n int64, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.calls.Load() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("liveness report asked %d times within %v, want at least %d", s.calls.Load(), d, n)
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

// TestRegistry_AnswerableExtendsPastWindow is the extension's headline: a window
// that elapses on an approval somebody can still answer resolves nothing, and a
// decision arriving after that point still lands as an allow.
func TestRegistry_AnswerableExtendsPastWindow(t *testing.T) {
	t.Parallel()
	r := New()
	req := sampleRequest("extend-1")
	s := &scriptedAnswerable{} // answers true forever
	r.SetAnswerable(s.ask)

	p, err := r.Register("extend-1", req, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Two asks mean two windows demonstrably elapsed and the entry survived both.
	waitAsks(t, s, 2, 2*time.Second)
	if _, ok := r.Lookup("extend-1"); !ok {
		t.Fatal("entry retired at its window while the report says somebody can still answer")
	}

	if !r.Resolve("extend-1", Allow(req.Input)) {
		t.Fatal("Resolve past the window returned false, want true — the entry must still be live")
	}
	v := awaitWithin(t, p, time.Second)
	if v.Behavior != BehaviorAllow {
		t.Fatalf("Behavior = %q, want %q for an allow arriving past the window", v.Behavior, BehaviorAllow)
	}
	if string(v.UpdatedInput) != string(req.Input) {
		t.Fatalf("UpdatedInput = %s, want %s", v.UpdatedInput, req.Input)
	}
	waitRetired(t, r, "extend-1", time.Second)
}

// TestRegistry_UnanswerableDeniesAtWindow covers the three ways an approval reads
// unanswerable. The first two rows are the "byte-identical unwired" evidence: with
// no report the registry never asks and denies exactly as it always has.
func TestRegistry_UnanswerableDeniesAtWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		install   func(r *Registry, s *scriptedAnswerable)
		wantAsked bool
	}{
		{
			name:    "no report installed",
			install: func(*Registry, *scriptedAnswerable) {},
		},
		{
			// Installed first, then cleared: a row that only ever called
			// SetAnswerable(nil) is behaviourally identical to the row above and
			// cannot separate "nil clears" from "nil is silently ignored".
			name:    "installed report cleared with nil",
			install: func(r *Registry, s *scriptedAnswerable) { r.SetAnswerable(s.ask); r.SetAnswerable(nil) },
		},
		{
			name:      "report says nobody can answer",
			install:   func(r *Registry, s *scriptedAnswerable) { r.SetAnswerable(s.ask) },
			wantAsked: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := New()
			s := &scriptedAnswerable{answers: []bool{false}}
			tt.install(r, s)
			req := sampleRequest("unanswerable-1")

			p, err := r.Register("unanswerable-1", req, 20*time.Millisecond)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}

			v := awaitWithin(t, p, 2*time.Second)
			if v.Behavior != BehaviorDeny {
				t.Fatalf("Behavior = %q, want %q (fail-closed)", v.Behavior, BehaviorDeny)
			}
			if v.Message != reasonTimeout {
				t.Fatalf("Message = %q, want %q", v.Message, reasonTimeout)
			}
			waitRetired(t, r, "unanswerable-1", time.Second)

			switch n := s.calls.Load(); {
			case tt.wantAsked && n == 0:
				t.Fatal("report was never asked, want at least one ask at the window")
			case !tt.wantAsked && n != 0:
				t.Fatalf("report asked %d times with no live report, want 0", n)
			}
		})
	}
}

// TestRegistry_NonPositiveWindowIsNeverExtended pins the guard that keeps
// Register's promise that a non-positive timeout denies ≈immediately. The
// promise is reachable in production — approvalTimeout passes whatever
// PYRY_APPROVAL_TIMEOUT parses to straight through — and without the guard an
// always-true report re-arms a zero window, spinning on the report as fast as
// the scheduler allows instead of ever delivering a verdict.
func TestRegistry_NonPositiveWindowIsNeverExtended(t *testing.T) {
	t.Parallel()
	for _, window := range []time.Duration{0, -time.Second} {
		t.Run(window.String(), func(t *testing.T) {
			t.Parallel()
			r := New()
			s := &scriptedAnswerable{} // answers true forever: only the guard can deny
			r.SetAnswerable(s.ask)
			req := sampleRequest("nonpositive-1")

			p, err := r.Register("nonpositive-1", req, window)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}

			v := awaitWithin(t, p, 2*time.Second)
			if v.Behavior != BehaviorDeny {
				t.Fatalf("Behavior = %q, want %q (fail-closed)", v.Behavior, BehaviorDeny)
			}
			if v.Message != reasonTimeout {
				t.Fatalf("Message = %q, want %q", v.Message, reasonTimeout)
			}
			waitRetired(t, r, "nonpositive-1", time.Second)

			// The report is not even consulted: a non-positive window is denied
			// beside the no-report case, before the ask.
			if n := s.calls.Load(); n != 0 {
				t.Fatalf("report asked %d times for a %v window, want 0", n, window)
			}
		})
	}
}

// TestRegistry_AnswerableIsReAskedNotSettledOnce pins that the liveness question is
// re-asked at every window: one positive reading buys one window, never an
// unbounded wait, and the deny that follows arms nothing further.
func TestRegistry_AnswerableIsReAskedNotSettledOnce(t *testing.T) {
	t.Parallel()
	const window = 20 * time.Millisecond
	r := New()
	s := &scriptedAnswerable{answers: []bool{true, true, false}}
	r.SetAnswerable(s.ask)
	req := sampleRequest("reask-1")

	p, err := r.Register("reask-1", req, window)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	v := awaitWithin(t, p, 2*time.Second)
	if v.Behavior != BehaviorDeny {
		t.Fatalf("Behavior = %q, want %q once the report says nobody can answer", v.Behavior, BehaviorDeny)
	}
	if v.Message != reasonTimeout {
		t.Fatalf("Message = %q, want %q", v.Message, reasonTimeout)
	}
	if n := s.calls.Load(); n < 3 {
		t.Fatalf("report asked %d times before the deny, want at least 3 — the entry must have survived two windows", n)
	}
	waitRetired(t, r, "reask-1", time.Second)

	// Denied without further extension: no window is armed past the deny.
	settled := s.calls.Load()
	time.Sleep(5 * window)
	if again := s.calls.Load(); again != settled {
		t.Fatalf("report asked again after the deny (%d -> %d), want no further window", settled, again)
	}
}

// TestRegistry_ReportNotConsultedOffTheExpiryPath holds the unknown/duplicate/
// already-resolved ids at exactly today's behaviour: the report is reachable only
// from an expiry, so none of them consults it.
func TestRegistry_ReportNotConsultedOffTheExpiryPath(t *testing.T) {
	t.Parallel()
	r := New()
	s := &scriptedAnswerable{} // always true: an ask here would extend, not deny
	r.SetAnswerable(s.ask)
	req := sampleRequest("offpath-1")

	if r.Resolve("never-registered", Allow(req.Input)) {
		t.Fatal("Resolve of an unknown id returned true, want false")
	}

	// A window long enough that no expiry can fire while the test runs.
	p, err := r.Register("offpath-1", req, 10*time.Second)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := r.Register("offpath-1", req, 10*time.Second); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate Register err = %v, want ErrDuplicateID", err)
	}
	if !r.Resolve("offpath-1", Allow(req.Input)) {
		t.Fatal("first entry no longer resolvable after a rejected duplicate")
	}
	if v := awaitWithin(t, p, time.Second); v.Behavior != BehaviorAllow {
		t.Fatalf("Behavior = %q, want allow", v.Behavior)
	}
	if r.Resolve("offpath-1", Deny("late")) {
		t.Fatal("Resolve of an already-resolved id returned true, want false")
	}

	if n := s.calls.Load(); n != 0 {
		t.Fatalf("report asked %d times off the expiry path, want 0", n)
	}
}

// TestRegistry_ExtendedAllowVsTimeoutRace carries the one-shot proof across the
// extension path: however many windows an approval survives, exactly one caller
// writes its verdict. Runs under -race.
//
// The per-iteration jitter is what makes it a race across that path rather than
// a duplicate of TestRegistry_AllowVsTimeoutRace. Resolving immediately wins
// before the first window on every iteration, so the timer branch is never taken
// and the report is never asked; staggering the resolve across the extension
// windows lands it on both sides. totalAsks is the non-vacuity guard for exactly
// that — it reads 0 if the resolve ever stops reaching the timer path.
func TestRegistry_ExtendedAllowVsTimeoutRace(t *testing.T) {
	t.Parallel()
	var totalAsks int64
	for i := 0; i < 300; i++ {
		r := New()
		s := &scriptedAnswerable{answers: []bool{true, true, false}}
		r.SetAnswerable(s.ask)
		id := fmt.Sprintf("extend-race-%d", i)
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
			time.Sleep(time.Duration(i%5) * time.Millisecond)
			resolvedOK.Store(r.Resolve(id, Allow(req.Input)))
		}()

		// Bounded rather than a bare Await: now that iterations reach the timer
		// path, a lock-discipline regression is a named failure, not a go test
		// timeout with no attribution.
		v := awaitWithin(t, p, 2*time.Second)
		wg.Wait()
		totalAsks += s.calls.Load()

		if resolvedOK.Load() {
			if v.Behavior != BehaviorAllow {
				t.Fatalf("iter %d: Resolve won but verdict = %q, want allow", i, v.Behavior)
			}
		} else {
			if v.Behavior != BehaviorDeny {
				t.Fatalf("iter %d: Resolve lost but verdict = %q, want deny", i, v.Behavior)
			}
		}

		// Exactly one verdict, however many windows the entry survived.
		select {
		case extra := <-p.ch:
			t.Fatalf("iter %d: a second verdict was delivered: %+v", i, extra)
		default:
		}
	}

	if totalAsks == 0 {
		t.Fatal("the report was never asked across 300 iterations: every resolve won before the first window, so no iteration raced the extension path")
	}
}

// TestRegistry_ReportCalledWithMuReleased pins the leaf-lock discipline: the report
// is consulted with mu released, so it may re-enter the registry. A design that
// held mu across the ask deadlocks the timer goroutine here, which the bounded
// wait reports as a failure rather than a hang.
func TestRegistry_ReportCalledWithMuReleased(t *testing.T) {
	t.Parallel()
	r := New()
	seen := make(chan bool, 4)
	s := &scriptedAnswerable{hook: func(id string, _ Request) {
		_, ok := r.Lookup(id)
		select {
		case seen <- ok:
		default:
		}
	}}
	r.SetAnswerable(s.ask)
	req := sampleRequest("reentrant-1")

	p, err := r.Register("reentrant-1", req, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	select {
	case ok := <-seen:
		if !ok {
			t.Fatal("re-entrant Lookup from the report did not find the entry it was asked about")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the report never completed a re-entrant Lookup — is mu held across the ask?")
	}

	// The positive reading extended: the entry is still live and still resolvable.
	if !r.Resolve("reentrant-1", Allow(req.Input)) {
		t.Fatal("Resolve returned false, want true — the extension must have kept the entry live")
	}
	if v := awaitWithin(t, p, time.Second); v.Behavior != BehaviorAllow {
		t.Fatalf("Behavior = %q, want allow", v.Behavior)
	}
}

// TestRegistry_SlowReportProducesOneExpiry pins the re-arm-after-ask ordering: the
// next window is armed only once the report has answered, so a report slower than
// its own window stretches the wait instead of overlapping with itself.
func TestRegistry_SlowReportProducesOneExpiry(t *testing.T) {
	t.Parallel()
	const window = 20 * time.Millisecond
	r := New()
	release := make(chan struct{})
	s := &scriptedAnswerable{answers: []bool{false}, hook: func(string, Request) { <-release }}
	r.SetAnswerable(s.ask)
	req := sampleRequest("slow-1")

	p, err := r.Register("slow-1", req, window)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	waitAsks(t, s, 1, 2*time.Second)
	time.Sleep(5 * window)
	if n := s.calls.Load(); n != 1 {
		t.Fatalf("report asked %d times while still answering the first ask, want 1", n)
	}
	close(release)

	v := awaitWithin(t, p, 2*time.Second)
	if v.Behavior != BehaviorDeny {
		t.Fatalf("Behavior = %q, want %q", v.Behavior, BehaviorDeny)
	}
	if v.Message != reasonTimeout {
		t.Fatalf("Message = %q, want %q", v.Message, reasonTimeout)
	}
	waitRetired(t, r, "slow-1", time.Second)

	time.Sleep(3 * window)
	select {
	case extra := <-p.ch:
		t.Fatalf("a second verdict was delivered: %+v", extra)
	default:
	}
	if n := registryLen(r); n != 0 {
		t.Fatalf("registry length = %d after the slow report's deny, want 0", n)
	}
}

func TestRegistry_StaleExpiryCannotResolveReusedIDGeneration(t *testing.T) {
	r := New()
	entered := make(chan Request, 1)
	release := make(chan struct{})
	s := &scriptedAnswerable{answers: []bool{false}, hook: func(_ string, req Request) {
		entered <- req
		<-release
	}}
	r.SetAnswerable(s.ask)

	firstReq := sampleRequest("reused")
	firstReq.Description = "first generation"
	first, err := r.Register("reused", firstReq, time.Millisecond)
	if err != nil {
		t.Fatalf("register first: %v", err)
	}
	if got := <-entered; got.Description != firstReq.Description {
		t.Fatalf("answerable request = %+v, want first generation", got)
	}
	if !r.Resolve("reused", Allow(firstReq.Input)) {
		t.Fatal("resolve first = false")
	}

	secondReq := sampleRequest("reused")
	secondReq.Description = "second generation"
	second, err := r.Register("reused", secondReq, time.Minute)
	if err != nil {
		t.Fatalf("register second: %v", err)
	}
	close(release)
	if got := awaitWithin(t, first, time.Second); got.Behavior != BehaviorAllow {
		t.Fatalf("first verdict = %+v, want allow", got)
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := r.Lookup("reused"); !ok {
		t.Fatal("stale first-generation expiry resolved the second generation")
	}
	if !r.Resolve("reused", Deny("cleanup")) {
		t.Fatal("second generation was not live for cleanup")
	}
	if got := awaitWithin(t, second, time.Second); got.Behavior != BehaviorDeny {
		t.Fatalf("second verdict = %+v, want deny cleanup", got)
	}
}
