package streamsup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turncommit"
)

// waitForState polls r.State() until pred holds or the timeout elapses, returning
// the matching snapshot. Lets a test observe a transient lifecycle phase (e.g.
// Backoff) without racing the Run goroutine's writes.
func waitForState(t *testing.T, r *Runner, pred func(State) bool, timeout time.Duration) State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := r.State(); pred(st) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state predicate; last = %+v", r.State())
	return State{}
}

// --- WriteUserTurn: AC2 ------------------------------------------------------

// TestRunner_WriteUserTurn_NoLiveChild: with no child spawned yet, Stdin() is nil
// and WriteUserTurn returns the retryable ErrNoLiveChild without writing —
// WriteTurn's verbatim no-live-child refusal, surfaced through the interface
// method.
func TestRunner_WriteUserTurn_NoLiveChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.WriteUserTurn(context.Background(), "c1", []byte("hello")); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn with no live child = %v, want ErrNoLiveChild", err)
	}
}

// TestRunner_WriteUserTurn_LiveChildDelivers: on a live child WriteUserTurn writes
// the user envelope onto the held-open stdin and returns nil; the fake child
// echoes the line back, proving the prompt reached it exactly once.
func TestRunner_WriteUserTurn_LiveChildDelivers(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	const marker = "write-user-turn-marker-AC2"
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
		t.Fatalf("WriteUserTurn on a live child: %v", err)
	}
	// The child echoes each stdin line as ECHO:<line>; the envelope carries the
	// marker as its content text, so the echo proves the envelope was delivered.
	waitForContains(t, out, "ECHO:", 3*time.Second)
	if !strings.Contains(out.String(), marker) {
		t.Errorf("child output missing delivered marker %q:\n%s", marker, out.String())
	}
}

// TestRunner_WriteUserTurn_GateDropsWithoutWriting: a false turncommit claim
// means the queued head was dropped during the ready-wait, so WriteUserTurn
// surfaces turncommit.ErrDropped and writes ZERO bytes onto the child's stdin.
// The zero-bytes property is proven by a FIFO barrier: a second, un-gated turn
// carrying a sentinel is echoed by the child, and once that sentinel arrives the
// dropped marker's echo must be absent — the pipe is FIFO, so a written dropped
// line would have echoed before the sentinel.
func TestRunner_WriteUserTurn_GateDropsWithoutWriting(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	const dropped = "dropped-marker-AC2"
	const sentinel = "sentinel-marker-AC2"

	// A false gate drops the turn: ErrDropped, and nothing must reach the child.
	deniedCtx := turncommit.With(context.Background(), func() bool { return false })
	if err := r.WriteUserTurn(deniedCtx, "c1", []byte(dropped)); !errors.Is(err, turncommit.ErrDropped) {
		t.Fatalf("WriteUserTurn with a false gate = %v, want turncommit.ErrDropped", err)
	}

	// An un-gated turn delivers; its echo is the FIFO barrier.
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(sentinel)); err != nil {
		t.Fatalf("WriteUserTurn (sentinel): %v", err)
	}
	waitForContains(t, out, sentinel, 3*time.Second)
	if strings.Contains(out.String(), dropped) {
		t.Errorf("dropped turn reached the child — a false gate must write zero bytes\n%s", out.String())
	}
}

// --- Interrupt: AC1 + AC2 ----------------------------------------------------

// TestRunner_Interrupt_NoLiveChild: with no child spawned, Stdin() is nil and
// Interrupt returns the retryable ErrNoLiveChild without writing and without
// panicking — the safe no-op refusal (AC2). Mirrors WriteUserTurn's no-live-child
// contract.
func TestRunner_Interrupt_NoLiveChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Interrupt(); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("Interrupt with no live child = %v, want ErrNoLiveChild", err)
	}
}

// TestRunner_NextControlID_Monotonic: correlation ids are locally minted (not
// caller-supplied) and strictly increasing within the runner's lifetime, so a
// future ack-correlator can distinguish successive control requests.
func TestRunner_NextControlID_Monotonic(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	first, second := r.nextControlID(), r.nextControlID()
	if first == second {
		t.Fatalf("nextControlID returned the same id twice: %q", first)
	}
	if first != "1" || second != "2" {
		t.Fatalf("nextControlID minted %q, %q, want 1, 2 (monotonic from zero value)", first, second)
	}
}

func TestRunner_SetModel_NoLiveChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetModel("sonnet"); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("SetModel with no live child = %v, want ErrNoLiveChild", err)
	}
}

// TestRunner_Interrupt_LiveChildDelivers: on a live child Interrupt writes a
// single control_request line onto the held-open stdin (AC1); the echo_lines
// fake child echoes it back as ECHO:<line>, proving the exact interrupt envelope
// reached the child — type control_request, request.subtype interrupt, and a
// non-empty locally-minted request_id.
func TestRunner_Interrupt_LiveChildDelivers(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	if err := r.Interrupt(); err != nil {
		t.Fatalf("Interrupt on a live child: %v", err)
	}
	// The child echoes each stdin line as ECHO:<line>; locate the echoed
	// interrupt line, strip the prefix, and decode it.
	waitForContains(t, out, "ECHO:", 3*time.Second)
	echoed := findEchoedLine(t, out.String())
	var cr decodedControlRequest
	if err := json.Unmarshal([]byte(echoed), &cr); err != nil {
		t.Fatalf("echoed interrupt line did not decode: %v (%q)", err, echoed)
	}
	if cr.Type != "control_request" {
		t.Errorf("echoed interrupt type = %q, want control_request", cr.Type)
	}
	if cr.Request.Subtype != "interrupt" {
		t.Errorf("echoed interrupt request.subtype = %q, want interrupt", cr.Request.Subtype)
	}
	if cr.RequestID == "" {
		t.Error("echoed interrupt request_id is empty, want a locally-minted id")
	}
}

// findEchoedLines returns every ECHO:-prefixed line's payload from the child's
// captured stdout, in the order the child echoed them (stdin is a FIFO pipe, so
// that is the order the runner wrote them).
func findEchoedLines(output string) []string {
	var echoed []string
	for _, line := range strings.Split(output, "\n") {
		if after, ok := strings.CutPrefix(line, "ECHO:"); ok {
			echoed = append(echoed, after)
		}
	}
	return echoed
}

// findEchoedLine returns the first ECHO:-prefixed line's payload from the child's
// captured stdout, failing the test if none is present.
func findEchoedLine(t *testing.T, output string) string {
	t.Helper()
	if echoed := findEchoedLines(output); len(echoed) > 0 {
		return echoed[0]
	}
	t.Fatalf("no ECHO: line in child output:\n%s", output)
	return ""
}

// --- RevokeBypass: AC4 + the shared control sequence -------------------------

// TestRunner_RevokeBypass_NoLiveChild: with no child spawned, Stdin() is nil and
// RevokeBypass returns ErrNoLiveChild without writing and without panicking — the
// same safe no-op refusal Interrupt gives, at the runner level (AC4).
func TestRunner_RevokeBypass_NoLiveChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.RevokeBypass(); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("RevokeBypass with no live child = %v, want ErrNoLiveChild", err)
	}
}

// TestRunner_SetPermissionMode_NoLiveChild: with no child spawned, Stdin() is nil
// and SetPermissionMode returns ErrNoLiveChild without writing and without
// panicking — the mode-carrying method inherits the same safe no-op refusal its
// three control-request siblings give (#2042 AC4).
func TestRunner_SetPermissionMode_NoLiveChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetPermissionMode("acceptEdits"); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("SetPermissionMode with no live child = %v, want ErrNoLiveChild", err)
	}
}

// TestRunner_SetPermissionMode_RefusesUnknownMode: the allow-list refusal reaches
// the caller THROUGH the runner, not merely through the free function, and stays
// distinguishable from the retryable no-live-child error even on a runner that has
// no live child — the state in which every caller of this method first meets it.
//
// This is the method a wire-driven caller will reach (#1687, #1686), so the
// refusal has to survive the one hop between WritePermissionMode and the seam.
//
// The mode is a NEAR MISS of the escalation rather than the escalation itself,
// which #2066 admitted to the allow-list. The property under test never was "the
// escalation is refused" — it is that a VOCABULARY refusal reaches the caller
// through the runner and does not read as the retryable no-live-child error — and a
// near miss exercises it while also pinning that the widening was by membership and
// did not spread to its neighbours.
func TestRunner_SetPermissionMode_RefusesUnknownMode(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const nearMiss = "Bypasspermissions"
	err = r.SetPermissionMode(nearMiss)
	if !errors.Is(err, ErrUnsupportedPermissionMode) {
		t.Fatalf("SetPermissionMode(%q) = %v, want ErrUnsupportedPermissionMode", nearMiss, err)
	}
	if errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("SetPermissionMode(%q) reported the retryable ErrNoLiveChild: %v", nearMiss, err)
	}
}

// TestRunner_RevokeBypass_LiveChildDelivers: on a live child RevokeBypass writes a
// single set_permission_mode control_request line onto the held-open stdin; the
// echo_lines fake child echoes it back as ECHO:<line>, proving the exact envelope
// reached the child — type control_request, request.subtype set_permission_mode,
// request.mode default, and a non-empty locally-minted request_id.
//
// An Interrupt follows in the same test to pin the shared-sequence invariant: both
// control subtypes mint from one counter, so their request_ids must differ. Two
// per-subtype counters would both start at "1" and a future ack-correlator keyed
// on request_id could not tell the two acks apart. The interrupt echo doubles as
// the FIFO barrier — stdin is a pipe, so once the second line comes back the first
// already has.
func TestRunner_RevokeBypass_LiveChildDelivers(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	if err := r.RevokeBypass(); err != nil {
		t.Fatalf("RevokeBypass on a live child: %v", err)
	}
	if err := r.Interrupt(); err != nil {
		t.Fatalf("Interrupt on a live child: %v", err)
	}
	waitForContains(t, out, `"subtype":"interrupt"`, 3*time.Second)

	bySubtype := map[string]decodedControlRequest{}
	for _, line := range findEchoedLines(out.String()) {
		var cr decodedControlRequest
		if err := json.Unmarshal([]byte(line), &cr); err != nil {
			t.Fatalf("echoed control line did not decode: %v (%q)", err, line)
		}
		bySubtype[cr.Request.Subtype] = cr
	}

	revocation, ok := bySubtype["set_permission_mode"]
	if !ok {
		t.Fatalf("no set_permission_mode line reached the child:\n%s", out.String())
	}
	if revocation.Type != "control_request" {
		t.Errorf("echoed revocation type = %q, want control_request", revocation.Type)
	}
	if revocation.Request.Mode != "default" {
		t.Errorf("echoed revocation request.mode = %q, want default", revocation.Request.Mode)
	}
	if revocation.RequestID == "" {
		t.Error("echoed revocation request_id is empty, want a locally-minted id")
	}

	interrupt, ok := bySubtype["interrupt"]
	if !ok {
		t.Fatalf("no interrupt line reached the child:\n%s", out.String())
	}
	if revocation.RequestID == interrupt.RequestID {
		t.Errorf("revocation and interrupt share request_id %q; both subtypes must mint from one control sequence", revocation.RequestID)
	}
}

// --- RequestInitialize: AC2 + AC3 --------------------------------------------

// TestRunner_RequestInitialize_NoLiveChild: with no child spawned, Stdin() is nil
// and RequestInitialize returns ErrNoLiveChild without writing and without
// panicking — the same safe no-op refusal Interrupt and RevokeBypass give, at the
// runner level (AC3).
func TestRunner_RequestInitialize_NoLiveChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.RequestInitialize(); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("RequestInitialize with no live child = %v, want ErrNoLiveChild", err)
	}
}

// TestRunner_RequestInitialize_LiveChildDelivers: on a live child
// RequestInitialize writes a single initialize control_request line onto the
// held-open stdin (AC2); the echo_lines fake child echoes it back as ECHO:<line>,
// proving the exact envelope reached the child — type control_request,
// request.subtype initialize, and a non-empty locally-minted request_id.
//
// An Interrupt follows in the same test to pin AC2's "same local source" clause:
// all three control subtypes mint from one counter, so their request_ids must
// differ. The distinct-id assertion is the SOLE detector for minting the
// initialize id from a fresh per-subtype counter — every other assertion stays
// green under that mutant, since the id is still non-empty, the byte-exact
// marshal test uses a fixed literal id, and the nil-refusal path never mints. The
// interrupt echo doubles as the FIFO barrier — stdin is a pipe, so once the
// second line comes back the first already has.
func TestRunner_RequestInitialize_LiveChildDelivers(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	if err := r.RequestInitialize(); err != nil {
		t.Fatalf("RequestInitialize on a live child: %v", err)
	}
	if err := r.Interrupt(); err != nil {
		t.Fatalf("Interrupt on a live child: %v", err)
	}
	waitForContains(t, out, `"subtype":"interrupt"`, 3*time.Second)

	bySubtype := map[string]decodedControlRequest{}
	for _, line := range findEchoedLines(out.String()) {
		var cr decodedControlRequest
		if err := json.Unmarshal([]byte(line), &cr); err != nil {
			t.Fatalf("echoed control line did not decode: %v (%q)", err, line)
		}
		bySubtype[cr.Request.Subtype] = cr
	}

	initialize, ok := bySubtype["initialize"]
	if !ok {
		t.Fatalf("no initialize line reached the child:\n%s", out.String())
	}
	if initialize.Type != "control_request" {
		t.Errorf("echoed initialize type = %q, want control_request", initialize.Type)
	}
	if initialize.RequestID == "" {
		t.Error("echoed initialize request_id is empty, want a locally-minted id")
	}

	interrupt, ok := bySubtype["interrupt"]
	if !ok {
		t.Fatalf("no interrupt line reached the child:\n%s", out.String())
	}
	if initialize.RequestID == interrupt.RequestID {
		t.Errorf("initialize and interrupt share request_id %q; both subtypes must mint from one control sequence", initialize.RequestID)
	}
}

func TestRunner_RequestContextUsage_RefusalOutranksNoChild(t *testing.T) {
	t.Parallel()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = r.RequestContextUsage("detailed")
	if !errors.Is(err, ErrUnsupportedContextUsageDetail) {
		t.Fatalf("RequestContextUsage(unsupported) = %v, want ErrUnsupportedContextUsageDetail", err)
	}
	if errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("RequestContextUsage(unsupported) reported ErrNoLiveChild: %v", err)
	}
	if id := r.nextControlID(); id != "1" {
		t.Fatalf("unsupported detail consumed control request id; next id = %q, want 1", id)
	}
	if err := r.RequestContextUsage("summary"); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("RequestContextUsage(summary) with no child = %v, want ErrNoLiveChild", err)
	}
}

func TestRunner_RequestContextUsage_LiveChildDelivers(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	waitForContains(t, out, "READY", 3*time.Second)

	for _, detail := range []string{"summary", "full"} {
		if err := r.RequestContextUsage(detail); err != nil {
			t.Fatalf("RequestContextUsage(%q): %v", detail, err)
		}
	}
	if err := r.Interrupt(); err != nil {
		t.Fatalf("Interrupt FIFO barrier: %v", err)
	}
	waitForContains(t, out, `"subtype":"interrupt"`, 3*time.Second)

	byDetail := make(map[string][]decodedControlRequest)
	var interrupt decodedControlRequest
	for _, line := range findEchoedLines(out.String()) {
		var cr decodedControlRequest
		if err := json.Unmarshal([]byte(line), &cr); err != nil {
			t.Fatalf("echoed control line did not decode: %v (%q)", err, line)
		}
		switch cr.Request.Subtype {
		case "get_context_usage":
			byDetail[cr.Request.Detail] = append(byDetail[cr.Request.Detail], cr)
		case "interrupt":
			interrupt = cr
		}
	}

	if interrupt.RequestID == "" {
		t.Fatal("interrupt barrier has empty request id")
	}
	ids := map[string]bool{interrupt.RequestID: true}
	for _, detail := range []string{"summary", "full"} {
		requests := byDetail[detail]
		if len(requests) != 1 {
			t.Fatalf("get_context_usage detail %q reached child %d times, want exactly 1; output:\n%s", detail, len(requests), out.String())
		}
		request := requests[0]
		if request.Type != "control_request" {
			t.Errorf("get_context_usage detail %q type = %q, want control_request", detail, request.Type)
		}
		if request.RequestID == "" {
			t.Errorf("get_context_usage detail %q has empty request id", detail)
		} else if ids[request.RequestID] {
			t.Errorf("get_context_usage detail %q reused request id %q from the shared sequence", detail, request.RequestID)
		}
		ids[request.RequestID] = true
	}
}

// --- RequestInitializeOnSpawn: the per-spawn ask -----------------------------

// initializeAsks returns the request id of every initialize control_request the
// child echoed back, in arrival order. The echo_lines child echoes EVERY stdin
// line, so what it returns is a faithful transcript of what the daemon wrote:
// the length is the ask count and the entries are the minted ids.
//
// A payload that is not an initialize request — a user-turn envelope, another
// control subtype — is skipped rather than failed, so a scenario is free to write
// turns as FIFO barriers.
func initializeAsks(output string) []string {
	var ids []string
	for _, line := range findEchoedLines(output) {
		var cr decodedControlRequest
		if err := json.Unmarshal([]byte(line), &cr); err != nil {
			continue
		}
		if cr.Request.Subtype == "initialize" {
			ids = append(ids, cr.RequestID)
		}
	}
	return ids
}

// TestRunner_RequestInitializeOnSpawn_OncePerChild pins the cardinality in both
// directions: with the flag set a live child is asked exactly ONCE, and with the
// zero value it is not asked at all.
//
// The two turns are load-bearing rather than decorative. "Once per spawn" and
// "once per turn" are indistinguishable in a single-turn test, so a trigger on the
// child's system/init line — the obvious-looking per-child signal that
// emitModelAnnounced's doc measures firing once per TURN — would stay green there
// and reddens here. They are also the barrier: stdin is a pipe and the ask is
// written at spawn, ahead of both turns, so once the second turn's marker comes
// back every line the daemon wrote to this child already has.
//
// The disabled row is not the enabled row's mirror image. It is what pins every
// OTHER streamsup.Config construction site as untouched — including the three
// internal/e2e/realclaude runners, which the hermetic gate cannot even compile —
// since the field defaults to false and nothing but the interactive daemon's
// mapper sets it.
func TestRunner_RequestInitializeOnSpawn_OncePerChild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		flag bool
		want int
	}{
		{name: "enabled asks the live child once", flag: true, want: 1},
		{name: "zero value never asks", flag: false, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, stderr := &safeBuffer{}, &safeBuffer{}
			cfg := helperRunCfg(t, "echo_lines", out, stderr)
			cfg.RequestInitializeOnSpawn = tc.flag
			spawned := make(chan struct{}, 1)
			cfg.onSpawn = func(int) {
				select {
				case spawned <- struct{}{}:
				default:
				}
			}
			r, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			cancel, join := runInBackground(t, r)
			defer func() { cancel(); join() }()

			select {
			case <-spawned:
			case <-time.After(5 * time.Second):
				t.Fatal("child never spawned")
			}
			waitForContains(t, out, "READY", 3*time.Second)

			const lastMarker = "once-per-child-turn-2"
			for _, marker := range []string{"once-per-child-turn-1", lastMarker} {
				if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
					t.Fatalf("WriteUserTurn(%q) on a live child: %v", marker, err)
				}
			}
			waitForContains(t, out, lastMarker, 3*time.Second)

			ids := initializeAsks(out.String())
			if len(ids) != tc.want {
				t.Fatalf("child was asked %d time(s) across two turns, want %d:\n%s", len(ids), tc.want, out.String())
			}
			if tc.want > 0 && ids[0] == "" {
				t.Error("the ask carried an empty request_id, want a locally-minted id")
			}
		})
	}
}

// TestRunner_RequestInitializeOnSpawn_ReplacementChild pins that a child which
// replaces an earlier one is asked in its own right, so a session that has
// respawned or rotated is not left with the question unasked.
//
// Both rows kill a LIVE echo_lines child, which never self-exits, so the second
// spawn is attributable to the restart rather than to the child's own exit. The
// RestartFresh row is what pins the rotation decision that is otherwise prose
// only: the ask reads Stdin() rather than the rotation-gated turnTarget, so a
// rotation's successor is an ordinary new child here and is asked like one.
//
// The distinct-id assertion is the sole detector for a design that mints or caches
// ONE id per runner rather than one per ask — the count stays 2 and both ids stay
// non-empty under that mutant.
func TestRunner_RequestInitializeOnSpawn_ReplacementChild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		restart func(r *Runner, cfg Config)
	}{
		{
			// Restart swaps the base argv VERBATIM, so the live Args are
			// re-installed: passing nil would clear them.
			name:    "Restart",
			restart: func(r *Runner, cfg Config) { r.Restart(cfg.Args) },
		},
		{
			name:    "RestartFresh",
			restart: func(r *Runner, _ Config) { r.RestartFresh("sess-rotated-once-per-child") },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, stderr := &safeBuffer{}, &safeBuffer{}
			cfg := helperRunCfg(t, "echo_lines", out, stderr)
			cfg.RequestInitializeOnSpawn = true
			spawns := make(chan struct{}, 8)
			cfg.onSpawn = func(int) {
				select {
				case spawns <- struct{}{}:
				default:
				}
			}
			r, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			cancel, join := runInBackground(t, r)
			defer func() { cancel(); join() }()

			waitSpawn := func(n int) {
				t.Helper()
				select {
				case <-spawns:
				case <-time.After(5 * time.Second):
					t.Fatalf("child %d never spawned", n)
				}
			}
			waitSpawn(1)
			// The first child must have ECHOED its ask before it is killed: onSpawn
			// proves only that the daemon wrote the line, and the kill can beat the
			// child's read of it.
			waitForContains(t, out, `"subtype":"initialize"`, 3*time.Second)

			tc.restart(r, cfg)
			waitSpawn(2)

			// The same FIFO barrier, on the second child: its ask was written at
			// spawn, ahead of this turn, so the turn's echo proves the ask's echo
			// has already landed.
			const marker = "replacement-child-barrier"
			if err := r.WriteUserTurn(context.Background(), "c1", []byte(marker)); err != nil {
				t.Fatalf("WriteUserTurn on the replacement child: %v", err)
			}
			waitForContains(t, out, marker, 3*time.Second)

			ids := initializeAsks(out.String())
			if len(ids) != 2 {
				t.Fatalf("saw %d ask(s) across two children, want 2:\n%s", len(ids), out.String())
			}
			if ids[0] == ids[1] {
				t.Errorf("both children were asked with request_id %q; each ask must mint its own id", ids[0])
			}
		})
	}
}

// TestRunner_RequestInitializeOnSpawn_UndeliverableAskAbsorbed pins that an ask
// which cannot be delivered restarts no child, fails no spawn, and neither stalls
// nor exits the supervision loop.
//
// The crash child exits almost immediately, so the ask races a dying child and may
// take the ErrNoLiveChild arm (takeStdin already ran), the EPIPE arm, or land
// cleanly. The test deliberately forces none of them: WriteInitialize's own error
// contract is already pinned by TestWriteInitialize_NilRefusal and
// TestWriteInitialize_WriteError, and all that is new here is that the CALL SITE
// swallows whatever comes back.
func TestRunner_RequestInitializeOnSpawn_UndeliverableAskAbsorbed(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "crash", out, stderr)
	cfg.RequestInitializeOnSpawn = true
	cfg.BackoffInitial = time.Millisecond
	cfg.BackoffMax = 5 * time.Millisecond
	spawns := make(chan struct{}, 32)
	cfg.onSpawn = func(int) {
		select {
		case spawns <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)

	// ≥2 spawns: the ladder respawned past a child whose ask may well have failed,
	// so that failure entered neither the spawn's error nor the backoff decision.
	for i := 0; i < 2; i++ {
		select {
		case <-spawns:
		case <-time.After(5 * time.Second):
			t.Fatalf("saw only %d spawn(s), want ≥2 — the supervision loop stalled", i)
		}
	}
	// Run's own deferred updateState is what sets PhaseStopped, so anything else
	// means Run has not returned.
	if st := r.State(); st.Phase == PhaseStopped {
		t.Fatalf("Run returned while the loop should still be supervising; state = %+v", st)
	}

	cancel()
	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after teardown", err)
	}
}

// --- Live Restart: AC3 -------------------------------------------------------

// waitArgvLines polls the argv capture file until it holds at least n complete
// (newline-terminated) records, and returns those records.
//
// onSpawn fires immediately after cmd.Start returns, so it proves only that the
// fork/exec succeeded — the child may not have executed a single line of Go yet.
// Every teardown here SIGTERMs the live child (Restart cancels the iteration
// ctx; so does cancelling Run's ctx), and a child still in runtime startup dies
// on the default SIGTERM disposition, before record_block writes its argv.
// Waiting on the record itself — the only artifact the assertions consume — is
// the happens-before edge onSpawn cannot supply.
//
// The deadline is a FAILURE BOUND, not a calibration: the write is the child's
// first act, so a healthy run reaches it in milliseconds and only a child that
// never records waits it out.
func waitArgvLines(t *testing.T, path string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var data []byte
	var recs []string
	for {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			// Count only complete records: whatever follows the final newline is
			// a torn write, not a recorded argv line.
			data = b
			recs = strings.Split(string(b), "\n")
			recs = recs[:len(recs)-1]
			if len(recs) >= n {
				return recs
			}
		case errors.Is(err, os.ErrNotExist):
			// record_block creates the file with O_CREATE on its first write, so
			// a missing file is the normal pre-write state: zero records so far.
			data, recs = nil, nil
		default:
			t.Fatalf("read argv capture %s: %v", path, err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out waiting for the child's argv record: want ≥%d complete line(s) in %s, have %d:\n%s",
				n, path, len(recs), data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunner_LiveRestart drives a deliberate Restart against a live child and
// asserts (a) the child is respawned with the swapped args and --resume (not
// --session-id), (b) Run's ctx is NOT cancelled by the restart (Run keeps
// looping — proven by cancelling it explicitly afterward and observing
// context.Canceled), and (c) RestartCount does not increment, because a
// deliberate restart is not a crash and skips the backoff path.
func TestRunner_LiveRestart(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	argvFile := filepath.Join(t.TempDir(), "argv")
	cfg := helperRunCfg(t, "record_block", out, stderr, "GO_STREAMSUP_HELPER_ARGV_FILE="+argvFile)
	spawns := make(chan struct{}, 32)
	cfg.onSpawn = func(int) {
		select {
		case spawns <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)

	// Wait for the first child (it records its argv, then blocks until SIGTERM —
	// it will not self-exit, so a second spawn can only come from Restart).
	select {
	case <-spawns:
	case <-time.After(5 * time.Second):
		t.Fatal("first child never spawned")
	}

	// Restart kills this child; its argv must be on disk before that happens.
	waitArgvLines(t, argvFile, 1)

	r.Restart([]string{"--model", "restart-marker"})

	// The restart must force a relaunch with the swapped args.
	select {
	case <-spawns:
	case <-time.After(5 * time.Second):
		t.Fatal("Restart did not respawn the child")
	}

	// Same window on spawn 2: the cancel() below kills it, so wait out its record
	// too. Both argv lines are in hand from here on.
	lines := waitArgvLines(t, argvFile, 2)

	// Run is still looping (Restart did not cancel it): cancelling now returns a
	// context error, proving Run outlived the restart.
	cancel()
	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after teardown", err)
	}

	// A deliberate restart is not a crash: RestartCount stays 0.
	if got := r.State().RestartCount; got != 0 {
		t.Errorf("RestartCount = %d after a deliberate restart, want 0 (not a crash)", got)
	}

	first, second := lines[0], lines[1]

	// Spawn 1 establishes the id with --session-id and carries no restart arg.
	if !strings.Contains(first, "--session-id "+testSessionID) {
		t.Errorf("spawn 1 argv missing --session-id %s:\n%s", testSessionID, first)
	}
	if strings.Contains(first, "restart-marker") {
		t.Errorf("spawn 1 argv unexpectedly carries the restart arg:\n%s", first)
	}
	// Spawn 2 resumes the SAME id and carries the swapped restart arg.
	if !strings.Contains(second, "--resume "+testSessionID) {
		t.Errorf("spawn 2 argv missing --resume %s (a live restart resumes, not forks):\n%s", testSessionID, second)
	}
	if strings.Contains(second, "--session-id") {
		t.Errorf("spawn 2 argv unexpectedly carries --session-id:\n%s", second)
	}
	if !strings.Contains(second, "--model restart-marker") {
		t.Errorf("spawn 2 argv missing the swapped --model restart-marker:\n%s", second)
	}
}

// --- State: AC4 --------------------------------------------------------------

// TestRunner_StatePhaseProgression asserts State() reflects the lifecycle:
// Running (with a live pid and a start time) while the child is up, then Stopped
// on ctx cancel.
func TestRunner_StatePhaseProgression(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Before Run, State reports the initial Starting phase.
	if got := r.State().Phase; got != PhaseStarting {
		t.Errorf("pre-Run Phase = %q, want %q", got, PhaseStarting)
	}

	cancel, join := runInBackground(t, r)
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}

	st := waitForState(t, r, func(s State) bool { return s.Phase == PhaseRunning }, 3*time.Second)
	if st.ChildPID <= 0 {
		t.Errorf("Running state ChildPID = %d, want > 0", st.ChildPID)
	}
	if st.StartedAt.IsZero() {
		t.Error("Running state StartedAt is zero, want the Run start time")
	}

	cancel()
	if err := join(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
	final := r.State()
	if final.Phase != PhaseStopped {
		t.Errorf("post-cancel Phase = %q, want %q", final.Phase, PhaseStopped)
	}
	if final.ChildPID != 0 {
		t.Errorf("post-cancel ChildPID = %d, want 0", final.ChildPID)
	}
}

// TestRunner_StateBackoffOnCrash asserts a crash drives State() into Backoff with
// an incremented RestartCount and a scheduled NextBackoff. A generous
// BackoffInitial keeps the Backoff window wide enough to observe reliably.
func TestRunner_StateBackoffOnCrash(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "crash", out, stderr)
	cfg.BackoffInitial = 500 * time.Millisecond
	cfg.BackoffMax = 500 * time.Millisecond
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	st := waitForState(t, r, func(s State) bool { return s.Phase == PhaseBackoff }, 5*time.Second)
	if st.RestartCount < 1 {
		t.Errorf("Backoff state RestartCount = %d, want ≥1", st.RestartCount)
	}
	if st.NextBackoff <= 0 {
		t.Errorf("Backoff state NextBackoff = %v, want > 0", st.NextBackoff)
	}
	if st.ChildPID != 0 {
		t.Errorf("Backoff state ChildPID = %d, want 0", st.ChildPID)
	}
}

// --- WaitForPTY: AC5 ---------------------------------------------------------

// TestRunner_WaitForPTY: the stream path has no PTY, so WaitForPTY returns nil in
// every window — no live child, a live child, and even under a cancelled ctx (it
// is a bare return nil, no readiness gate).
func TestRunner_WaitForPTY(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := r.WaitForPTY(context.Background()); err != nil {
		t.Errorf("WaitForPTY before spawn = %v, want nil", err)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if err := r.WaitForPTY(cctx); err != nil {
		t.Errorf("WaitForPTY with cancelled ctx = %v, want nil", err)
	}

	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}
	if err := r.WaitForPTY(context.Background()); err != nil {
		t.Errorf("WaitForPTY with live child = %v, want nil", err)
	}
}
