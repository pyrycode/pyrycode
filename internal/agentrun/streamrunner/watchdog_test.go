package streamrunner

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a controllable time source for the parser's `now` seam.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// --- Run-level watchdog integration tests (TestHelperProcess fake claude) ---

// stalls while awaiting the FIRST assistant turn (no events at all) → the
// watchdog must fire, claude is killed fast, and Run synthesises the
// idle_stall result line on stdout.
func TestRun_IdleStall_NoEvents(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "stall_silent", &stdout, &stderr)
	cfg.PromptBytes = []byte("noop")
	cfg.IdleTimeout = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run on idle stall: %v, want nil (synthetic result is the signal)", err)
	}
	elapsed := time.Since(start)
	// idle (200ms) + tick + SIGTERM-handled exit — must be well under the
	// SIGKILL grace fallthrough.
	if elapsed > 5*time.Second {
		t.Errorf("Run took %v, want < 5s (SIGTERM grace likely fell through to SIGKILL)", elapsed)
	}
	out := stdout.String()
	for _, want := range []string{
		`"type":"result"`,
		`"subtype":"error_idle_stall"`,
		`"terminal_reason":"idle_stall"`,
		`"is_error":true`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("synthetic result missing %q\nstdout:\n%s", want, out)
		}
	}
}

// stalls right after a tool_result came back (claude owes the next assistant
// turn) → the watchdog must fire even though earlier events flowed.
func TestRun_IdleStall_AfterToolResult(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "stall_after_user", &stdout, &stderr)
	cfg.PromptBytes = []byte("noop")
	cfg.IdleTimeout = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run on post-tool-result stall: %v, want nil", err)
	}
	out := stdout.String()
	// Earlier events passed through unchanged...
	if !strings.Contains(out, `"type":"user"`) {
		t.Errorf("passthrough missing the tool_result user line\nstdout:\n%s", out)
	}
	// ...and the synthetic idle_stall trailer was appended.
	if !strings.Contains(out, `"terminal_reason":"idle_stall"`) {
		t.Errorf("missing synthetic idle_stall result\nstdout:\n%s", out)
	}
}

// a legitimately long in-flight tool run (silence AFTER an assistant turn,
// awaitingAssistant==false) must NOT trip the watchdog — claude completes and
// emits its own success result; no synthetic line is written.
func TestRun_SlowTool_NoFire(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "slow_tool", &stdout, &stderr,
		"GO_STREAMRUNNER_HELPER_SLEEP_MS=1500",
	)
	cfg.PromptBytes = []byte("noop")
	// idle threshold well under the 1500ms tool silence: a type-blind
	// watchdog WOULD fire; the type-aware one must not.
	cfg.IdleTimeout = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run on slow tool: %v, want nil (clean completion)", err)
	}
	out := stdout.String()
	if strings.Contains(out, "idle_stall") {
		t.Errorf("watchdog wrongly fired during a slow tool run\nstdout:\n%s", out)
	}
	if !strings.Contains(out, `"subtype":"success"`) {
		t.Errorf("missing claude's own success result (run was killed?)\nstdout:\n%s", out)
	}
}

// operator shutdown (parent ctx cancel) is a clean no-result teardown — even
// for a run that was sitting idle, no synthetic idle_stall is written.
func TestRun_OperatorShutdown_NoSyntheticResult(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "stall_silent", &stdout, &stderr)
	cfg.PromptBytes = []byte("noop")
	// Long enough that the watchdog will NOT fire before the operator cancel.
	cfg.IdleTimeout = 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run on operator shutdown: %v, want nil", err)
	}
	if strings.Contains(stdout.String(), "idle_stall") {
		t.Errorf("synthetic idle_stall written on operator shutdown\nstdout:\n%s", stdout.String())
	}
}

// a run that completes through claude's own result line writes NO synthetic
// trailer and passes the byte stream through unchanged.
func TestRun_CleanExit_NoSyntheticResult_ByteExact(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	cfg := helperRunCfg(t, "clean", &stdout, &stderr)
	cfg.PromptBytes = []byte("hi")
	cfg.IdleTimeout = 1 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}` + "\n" +
		`{"type":"result","subtype":"success"}` + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("passthrough not byte-exact (or synthetic appended):\n got:\n%s\nwant:\n%s", got, want)
	}
}

// --- streamParser unit tests (white-box) ---

func TestStreamParser_AwaitingTransitions(t *testing.T) {
	t.Parallel()
	var dst bytes.Buffer
	p := newStreamParser(&dst, nil)

	// Starts awaiting the first assistant turn.
	if a, _ := p.snapshot(); !a {
		t.Fatal("parser should start in the awaiting state")
	}

	writeLine := func(s string) {
		if _, err := p.Write([]byte(s + "\n")); err != nil {
			t.Fatalf("Write(%q): %v", s, err)
		}
	}

	// system init: activity only, still awaiting.
	writeLine(`{"type":"system","subtype":"init"}`)
	if a, _ := p.snapshot(); !a {
		t.Error("system line should leave awaiting=true")
	}

	// assistant: claude produced a turn → stop awaiting (silence expected).
	writeLine(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use"}]}}`)
	if a, _ := p.snapshot(); a {
		t.Error("assistant line should set awaiting=false")
	}

	// user (tool_result): claude owes the next turn → awaiting again.
	writeLine(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`)
	if a, _ := p.snapshot(); !a {
		t.Error("user/tool_result line should set awaiting=true")
	}

	// result: run done → not awaiting, sawResult set.
	writeLine(`{"type":"result","subtype":"success"}`)
	if a, _ := p.snapshot(); a {
		t.Error("result line should set awaiting=false")
	}
	if !p.hasSeenResult() {
		t.Error("result line should set sawResult=true")
	}
}

func TestStreamParser_LastEventResets(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{t: time.Unix(1_000, 0)}
	var dst bytes.Buffer
	p := newStreamParser(&dst, clk.now)

	_, last0 := p.snapshot()
	clk.advance(3 * time.Second)
	if _, err := p.Write([]byte(`{"type":"system"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_, last1 := p.snapshot()
	if !last1.After(last0) {
		t.Errorf("lastEvent did not advance: last0=%v last1=%v", last0, last1)
	}
	if want := time.Unix(1_003, 0); !last1.Equal(want) {
		t.Errorf("lastEvent = %v, want %v", last1, want)
	}
}

func TestStreamParser_PartialLine(t *testing.T) {
	t.Parallel()
	var dst bytes.Buffer
	p := newStreamParser(&dst, nil)

	// First half of an assistant line, no newline yet → no state change.
	if _, err := p.Write([]byte(`{"type":"assi`)); err != nil {
		t.Fatalf("Write part 1: %v", err)
	}
	if a, _ := p.snapshot(); !a {
		t.Error("partial line must not change awaiting (still true)")
	}

	// Completion arrives → now the line parses, awaiting flips.
	if _, err := p.Write([]byte(`stant"}` + "\n")); err != nil {
		t.Fatalf("Write part 2: %v", err)
	}
	if a, _ := p.snapshot(); a {
		t.Error("completed assistant line should set awaiting=false")
	}
	if got := dst.String(); got != `{"type":"assistant"}`+"\n" {
		t.Errorf("passthrough mismatch across the split: %q", got)
	}
}

func TestStreamParser_OversizedLine(t *testing.T) {
	t.Parallel()
	var dst bytes.Buffer
	p := newStreamParser(&dst, nil)
	p.maxBuf = 64

	// A long newline-less blob must not be buffered unbounded; the partial
	// remainder is dropped once it exceeds maxParseBuf.
	blob := bytes.Repeat([]byte("x"), 200)
	if _, err := p.Write(blob); err != nil {
		t.Fatalf("Write blob: %v", err)
	}
	p.mu.Lock()
	bufLen := len(p.buf)
	p.mu.Unlock()
	if bufLen != 0 {
		t.Errorf("oversized newline-less remainder not dropped: buf len = %d", bufLen)
	}

	// Parsing still works after a drop.
	if _, err := p.Write([]byte(`{"type":"assistant"}` + "\n")); err != nil {
		t.Fatalf("Write after drop: %v", err)
	}
	if a, _ := p.snapshot(); a {
		t.Error("parser should recover after dropping an oversized partial")
	}

	// Passthrough is unaffected by the drop — every byte was forwarded.
	if got := dst.Len(); got != len(blob)+len(`{"type":"assistant"}`)+1 {
		t.Errorf("passthrough byte count = %d, want %d", got, len(blob)+len(`{"type":"assistant"}`)+1)
	}
}

func TestStreamParser_PassthroughByteExact(t *testing.T) {
	t.Parallel()
	var dst bytes.Buffer
	p := newStreamParser(&dst, nil)

	full := `{"type":"system"}` + "\n" +
		`{"type":"assistant"}` + "\n" +
		`{"type":"result"}` + "\n"
	// Feed in awkward chunks straddling line boundaries.
	chunks := []string{
		`{"type":"sys`,
		`tem"}` + "\n" + `{"type":"assi`,
		`stant"}` + "\n",
		`{"type":"result"}` + "\n",
	}
	for _, c := range chunks {
		if _, err := p.Write([]byte(c)); err != nil {
			t.Fatalf("Write(%q): %v", c, err)
		}
	}
	if got := dst.String(); got != full {
		t.Errorf("passthrough not byte-exact:\n got: %q\nwant: %q", got, full)
	}
}

func TestWatchdogTickFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		idle time.Duration
		want time.Duration
	}{
		{idle: idleStall, want: watchdogTick},               // 240s/8=30s, capped to 5s
		{idle: 200 * time.Millisecond, want: 25 * time.Millisecond}, // /8
		{idle: 1 * time.Microsecond, want: minWatchdogTick}, // floored
	}
	for _, c := range cases {
		if got := watchdogTickFor(c.idle); got != c.want {
			t.Errorf("watchdogTickFor(%v) = %v, want %v", c.idle, got, c.want)
		}
	}
}
