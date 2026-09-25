package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/codexsup"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turncommit"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// codexMinVersion is the oldest Codex the runner drives: the version
// codexsup's wire contract was captured from.
const codexMinVersion = "0.156.1"

// harnessCodex is RunnerConfig.Harness for a Codex session. The sessions
// package carries the value without naming it; the factory decides.
const harnessCodex = "codex"

const (
	codexBackoffInitial = time.Second
	codexBackoffMax     = 30 * time.Second
	// codexBackoffReset is the uptime after which a crash backs off from
	// codexBackoffInitial again rather than from the doubled delay.
	codexBackoffReset = time.Minute
	// codexStartTimeout bounds one spawn's handshake and thread open.
	codexStartTimeout = 30 * time.Second
	// codexCallTimeout bounds an interrupt and a stop.
	codexCallTimeout = 5 * time.Second
)

// codexHarness is the daemon-wide input to the Codex runner factory: the
// binary (-pyry-codex), the daemon-owned CODEX_HOME, the one turn-event
// fan-in Claude sessions also feed, and the approval registry, window and
// modal surface Claude sessions also use. Only the registry, timeout and
// surface of approval are read: Codex approvals are in-band, whatever
// transport Claude's take.
type codexHarness struct {
	bin, home string
	sink      *streamTurnSink
	approval  streamApprovalConfig
}

// newCodexRunnerFactory builds a Codex runner per session (#2620). Like
// newStreamRunnerFactory it mints one live tag and binds BOTH the event lane
// and the exit lane from it, so a Codex turn and a Codex exit reach the drain
// under the same session id. The Codex home's config is rewritten on every
// construction, so its read-only baseline is restored even if it was edited;
// every turn then asserts the session's stored posture (codexTurnOverrides).
//
// Before returning a runner it probes Codex once (#2621): a Codex below
// codexMinVersion or a daemon home with no sign-in refuses the session here,
// because Run would only back off and retry the same failure.
func newCodexRunnerFactory(h codexHarness) sessions.RunnerFactory {
	return func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		if h.sink == nil || h.home == "" {
			return nil, errors.New("cmd/pyry: codex runner: no turn sink or Codex home")
		}
		if err := prepareCodexHome(h.home); err != nil {
			return nil, fmt.Errorf("cmd/pyry: codex runner: %w", err)
		}
		dir := cfg.WorkDir
		if dir == "" {
			wd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("cmd/pyry: codex runner: workdir: %w", err)
			}
			dir = wd
		}
		bin := h.bin
		if bin == "" {
			bin = "codex"
		}
		if err := probeCodex(bin, h.home, dir); err != nil {
			return nil, fmt.Errorf("cmd/pyry: codex runner: %w", err)
		}
		tag := newStreamSessionTag(cfg.SessionID)
		model, effort := codexTurnSettings(cfg.ClaudeArgs)
		return newCodexRunner(codexRunnerConfig{
			Binary:         bin,
			Home:           h.home,
			Dir:            dir,
			Tag:            tag,
			Sink:           h.sink.sinkForTag(tag.ID),
			OnExit:         h.sink.exitForTag(tag.ID),
			Model:          model,
			Effort:         effort,
			PermissionMode: cfg.PermissionMode,
			Backoff:        cfg.BackoffInitial,
			Log:            cfg.Logger,
			ThreadID:       cfg.ThreadID,
			OnThread:       recordCodexThread(cfg.RecordThread, cfg.Logger),
			Approvals:      newCodexApprovals(h.approval.registry, h.approval.timeout, h.approval.surface),
		}), nil
	}
}

// recordCodexThread adapts the pool's RecordThread to the runner's OnThread
// (#2622). A report for an id the pool no longer holds is a stale one racing a
// rotation and is dropped quietly; any other failure is logged, without the
// thread id.
func recordCodexThread(record func(sessionID, threadID string) error, log *slog.Logger) func(sessionID, threadID string) {
	if record == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return func(sessionID, threadID string) {
		if err := record(sessionID, threadID); err != nil && !errors.Is(err, sessions.ErrSessionNotFound) {
			log.Warn("codex: thread not recorded in the registry", "session", sessionID, "err", err)
		}
	}
}

// probeCodex starts the app-server against the daemon home, checks its
// version and its sign-in, and stops it. It opens no thread. The account's
// details are never read, so none reach the error or the log.
func probeCodex(bin, home, dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), codexStartTimeout)
	defer cancel()
	client, err := codexsup.Start(ctx, codexsup.Config{
		Binary: bin, Dir: dir, CodexHome: home, ClientVersion: Version,
	})
	if err != nil {
		return fmt.Errorf("probe codex: %w", err)
	}
	defer stopCodexClient(client)
	if err := checkCodexVersion(client.Version()); err != nil {
		return err
	}
	signedIn, err := client.SignedIn(ctx)
	if err != nil {
		return fmt.Errorf("probe codex: %w", err)
	}
	if !signedIn {
		return fmt.Errorf("the daemon's Codex home is not signed in; sign in with CODEX_HOME=%s codex login", home)
	}
	return nil
}

// checkCodexVersion refuses a version below codexMinVersion or one that is
// not MAJOR.MINOR.PATCH with an optional -prerelease or +build suffix. A
// prerelease sorts below its release, as in semver.
func checkCodexVersion(v string) error {
	have, havePre, ok := parseCodexVersion(v)
	if !ok {
		return fmt.Errorf("cannot parse Codex version %q; Codex %s or later is required", v, codexMinVersion)
	}
	want, _, _ := parseCodexVersion(codexMinVersion)
	if c := slices.Compare(have[:], want[:]); c > 0 || (c == 0 && !havePre) {
		return nil
	}
	return fmt.Errorf("found Codex %q, below the required %s; upgrade Codex", v, codexMinVersion)
}

// parseCodexVersion splits v into its three numeric components and whether it
// carries a prerelease.
func parseCodexVersion(v string) (core [3]int, pre, ok bool) {
	v, _, _ = strings.Cut(v, "+")
	v, prerelease, pre := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != len(core) || (pre && prerelease == "") {
		return core, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return core, false, false
		}
		core[i] = n
	}
	return core, pre, true
}

// codexTurnSettings reads the session's model and effort out of the
// Claude-shaped argv the pool composes (claudeSettingsArgs): the last
// --model and --effort values, in either the spaced or the = form. Every other
// flag in the argv is Claude-only and ignored; the posture reaches the runner
// as a mode (SetSpawnPermissionMode), not through the argv.
func codexTurnSettings(args []string) (model, effort string) {
	for i := 0; i < len(args); i++ {
		for _, flag := range []string{"--model", "--effort"} {
			var v string
			switch {
			case args[i] == flag && i+1 < len(args):
				v = args[i+1]
			case strings.HasPrefix(args[i], flag+"="):
				v = strings.TrimPrefix(args[i], flag+"=")
			default:
				continue
			}
			if flag == "--model" {
				model = v
			} else {
				effort = v
			}
		}
	}
	return model, effort
}

// codexRunnerConfig is one Codex runner's construction input. Sink and OnExit
// must not block; Tag is the live pool session id both are bound to. ThreadID
// is the stored thread the first spawn resumes, empty to start one; OnThread,
// when set, is told the live pool id and the id of every thread the runner
// starts, so the pool can persist it (#2622). Approvals routes approval
// requests to the permission modal; nil declines every one (#2587).
type codexRunnerConfig struct {
	Binary, Home, Dir string
	Tag               *streamSessionTag
	Sink              func(turnevent.Event)
	OnExit            func()
	Model, Effort     string
	PermissionMode    string
	Backoff           time.Duration
	Log               *slog.Logger
	ThreadID          string
	OnThread          func(sessionID, threadID string)
	Approvals         *codexApprovals
}

// codexRunner is the Codex implementation of sessions.Runner: it supervises
// one codexsup.Client at a time, respawning it after a crash and resuming the
// same Codex thread, and gives the pool the write-refusal gates streamsup gives
// a Claude session. Codex mints its own thread ids, so the pool's session id
// (the tag) stays the stable key and the thread id is held beside it: in
// memory, where a later Run after an eviction resumes it, and in the session's
// registry entry through OnThread, where a runner rebuilt after a daemon
// restart or a revive picks it up as ThreadID (#2622).
//
// Model, effort and posture are per-turn overrides: every turn/start carries
// all of them from the stored settings (codexTurnOverrides), so a settings
// change sends nothing and respawns nothing, and applies from the next turn.
// Command and file-change approvals go to the permission modal through
// cfg.Approvals (#2587); every other server request takes codexsup's default
// decline, and nothing accepts unless the operator allows it.
type codexRunner struct {
	cfg       codexRunnerConfig
	log       *slog.Logger
	restartCh chan struct{}

	// mu is a leaf: it is never held across a Codex call or with trMu.
	mu          sync.Mutex
	client      *codexsup.Client // bound live client, nil when none may take a turn
	threadID    string           // Codex thread to resume; empty means start one
	turnID      string           // running turn, from turn/started
	freshSeq    uint64           // bumped by RestartFresh
	armFreshSeq uint64           // freshSeq when the rotation gate was armed
	rotateGen   uint64
	rotating    bool
	tearingDown bool
	iterCancel  context.CancelFunc
	model       string
	effort      string
	mode        string // stored permission mode, never logged
	state       sessions.State

	// trMu guards the current spawn's translator between its read loop and
	// WriteUserTurn's model update.
	trMu sync.Mutex
	tr   *codexsup.Translator
}

func newCodexRunner(cfg codexRunnerConfig) *codexRunner {
	if cfg.Backoff <= 0 {
		cfg.Backoff = codexBackoffInitial
	}
	if cfg.Sink == nil {
		cfg.Sink = func(turnevent.Event) {}
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &codexRunner{
		cfg:       cfg,
		log:       log,
		restartCh: make(chan struct{}, 1),
		model:     cfg.Model,
		effort:    cfg.Effort,
		mode:      cfg.PermissionMode,
		threadID:  cfg.ThreadID,
	}
}

// turnSettings is the stored settings every turn asserts. The runner learns the
// posture only as a mode; the pool derives the YOLO bit from the same mode.
// Caller holds r.mu.
func (r *codexRunner) turnSettings() sessions.SessionSettings {
	return sessions.SessionSettings{
		Model:          r.model,
		Effort:         r.effort,
		PermissionMode: r.mode,
		YOLO:           r.mode == sessions.PermissionModeBypass,
	}
}

func (r *codexRunner) updateState(fn func(*sessions.State)) {
	r.mu.Lock()
	fn(&r.state)
	r.mu.Unlock()
}

// State reports the supervise loop's phase. ChildPID stays 0: codexsup does
// not expose the app-server's pid.
func (r *codexRunner) State() sessions.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// WaitForPTY has nothing to wait for: there is no terminal.
func (r *codexRunner) WaitForPTY(context.Context) error { return nil }

// Run supervises the app-server until ctx ends. Each iteration starts a
// process, resumes the held thread (or starts one), binds the client for
// turns, and waits for it to exit. OnExit fires after every iteration, before
// the shutdown return, as streamsup's OnChildExit does. A deliberate restart
// relaunches at once; a crash or a failed start backs off.
func (r *codexRunner) Run(ctx context.Context) error {
	r.updateState(func(s *sessions.State) {
		s.Phase = sessions.PhaseStarting
		s.StartedAt = time.Now()
	})
	defer r.updateState(func(s *sessions.State) {
		s.Phase = sessions.PhaseStopped
		s.NextBackoff = 0
	})
	delay := r.cfg.Backoff
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		iterCtx, cancel := context.WithCancel(ctx)
		r.mu.Lock()
		r.iterCancel = cancel
		threadID, seq := r.threadID, r.freshSeq
		r.mu.Unlock()

		start := time.Now()
		err := r.runOnce(iterCtx, threadID, seq)
		cancel()
		r.mu.Lock()
		r.iterCancel = nil
		r.mu.Unlock()
		uptime := time.Since(start)

		if r.cfg.OnExit != nil {
			r.cfg.OnExit()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			r.log.Warn("codex app-server exited", "session", r.cfg.Tag.ID(), "err", err, "uptime", uptime)
		} else {
			r.log.Info("codex app-server exited", "session", r.cfg.Tag.ID(), "uptime", uptime)
		}
		if r.drainRestart() {
			continue
		}
		if uptime >= codexBackoffReset {
			delay = r.cfg.Backoff
		}
		r.updateState(func(s *sessions.State) {
			s.Phase = sessions.PhaseBackoff
			s.RestartCount++
			s.LastUptime = uptime
			s.NextBackoff = delay
		})
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		case <-r.restartCh:
		}
		delay = min(delay*2, codexBackoffMax)
	}
}

// runOnce is one supervised app-server: start, open the thread, bind, wait.
// It returns nil when ctx ended it and the exit or start error otherwise.
func (r *codexRunner) runOnce(ctx context.Context, threadID string, seq uint64) error {
	r.mu.Lock()
	model := r.model
	r.mu.Unlock()
	tr := codexsup.NewTranslator(model)
	startCtx, cancelStart := context.WithTimeout(ctx, codexStartTimeout)
	defer cancelStart()
	client, err := codexsup.Start(startCtx, codexsup.Config{
		Binary:        r.cfg.Binary,
		Dir:           r.cfg.Dir,
		CodexHome:     r.cfg.Home,
		ClientVersion: Version,
		OnNotification: func(method string, params json.RawMessage) {
			r.notify(tr, method, params)
		},
		OnServerRequest: r.cfg.Approvals.handle,
		Log:             r.log,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	started := threadID == ""
	if started {
		threadID, err = client.StartThread(startCtx)
	} else {
		err = client.ResumeThread(startCtx, threadID)
	}
	if err != nil {
		stopCodexClient(client)
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("open thread: %w", err)
	}

	r.trMu.Lock()
	r.tr = tr
	r.trMu.Unlock()
	r.mu.Lock()
	if ctx.Err() != nil {
		r.mu.Unlock()
		stopCodexClient(client)
		return nil
	}
	// The tag is read beside the seq check: RestartFresh bumps the seq and
	// rotates the tag in one r.mu section, so a thread this spawn started is
	// reported under the id it belongs to, or not at all.
	var reportTo string
	if r.freshSeq == seq {
		r.threadID = threadID
		if started {
			reportTo = r.cfg.Tag.ID()
		}
	}
	r.client = client
	r.tearingDown = false
	if r.rotating && seq > r.armFreshSeq {
		r.rotating = false
	}
	r.state.Phase = sessions.PhaseRunning
	r.state.NextBackoff = 0
	r.mu.Unlock()
	if reportTo != "" && r.cfg.OnThread != nil {
		r.cfg.OnThread(reportTo, threadID)
	}

	select {
	case <-client.Done():
	case <-ctx.Done():
	}
	r.mu.Lock()
	r.client = nil
	r.mu.Unlock()
	exitErr := stopCodexClient(client)
	r.cfg.Approvals.declineAll(reasonCodexExit)
	r.mu.Lock()
	r.turnID = ""
	r.mu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	return exitErr
}

// stopCodexClient stops c within codexCallTimeout and returns its exit error.
func stopCodexClient(c *codexsup.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), codexCallTimeout)
	defer cancel()
	return c.Stop(ctx)
}

// notify runs on the client's read loop. It tracks the running turn — ordered
// with the turn's own notifications, so it cannot race StartTurn's return —
// and forwards the translated events. Sink is non-blocking.
func (r *codexRunner) notify(tr *codexsup.Translator, method string, params json.RawMessage) {
	switch method {
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &p) == nil && p.Turn.ID != "" {
			r.mu.Lock()
			r.turnID = p.Turn.ID
			r.mu.Unlock()
		}
	case "turn/completed":
		r.mu.Lock()
		r.turnID = ""
		r.mu.Unlock()
	}
	r.cfg.Approvals.observe(method, params)
	r.trMu.Lock()
	events := tr.Translate(method, params)
	r.trMu.Unlock()
	for _, ev := range events {
		r.cfg.Sink(ev)
	}
}

// WriteUserTurn starts a Codex turn with payload as its text and the session's
// model, effort and posture as per-turn overrides. It refuses with the retryable
// streamsup.ErrNoLiveChild, sending nothing, while a rotation or teardown is
// armed or no client is bound, and claims the queue's commit gate before
// sending. Neither the payload nor the conversation id is logged.
func (r *codexRunner) WriteUserTurn(ctx context.Context, _ string, payload []byte) error {
	r.mu.Lock()
	client, rotating, tearingDown := r.client, r.rotating, r.tearingDown
	in := codexTurnOverrides(r.turnSettings())
	r.mu.Unlock()
	switch {
	case rotating:
		r.log.Info("codex: turn refused; new_session rotation in flight", "session", r.cfg.Tag.ID())
		return streamsup.ErrNoLiveChild
	case client == nil:
		return streamsup.ErrNoLiveChild
	case tearingDown:
		r.log.Info("codex: turn refused; session teardown in flight", "session", r.cfg.Tag.ID())
		return streamsup.ErrNoLiveChild
	}
	if gate := turncommit.From(ctx); gate != nil && !gate() {
		return turncommit.ErrDropped
	}
	if in.Model != "" {
		r.trMu.Lock()
		if r.tr != nil {
			r.tr.SetModel(in.Model)
		}
		r.trMu.Unlock()
	}
	in.Text = string(payload)
	if _, err := client.StartTurn(ctx, in); err != nil {
		return fmt.Errorf("cmd/pyry: codex turn: %w", err)
	}
	return nil
}

// Interrupt declines the approvals the turn is waiting on and ends the running
// turn, which then completes as interrupted. With no client bound it is the
// retryable ErrNoLiveChild; with no turn running there is nothing to end.
func (r *codexRunner) Interrupt() error {
	r.cfg.Approvals.declineAll(reasonCodexInterrupt)
	r.mu.Lock()
	client, turnID := r.client, r.turnID
	r.mu.Unlock()
	if client == nil {
		return streamsup.ErrNoLiveChild
	}
	if turnID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexCallTimeout)
	defer cancel()
	return client.Interrupt(ctx, turnID)
}

func (r *codexRunner) setTurnSettings(args []string) {
	model, effort := codexTurnSettings(args)
	r.mu.Lock()
	r.model, r.effort = model, effort
	r.mu.Unlock()
}

// endIteration asks Run to relaunch at once and ends the live process.
func (r *codexRunner) endIteration() {
	r.mu.Lock()
	cancel := r.iterCancel
	r.mu.Unlock()
	select {
	case r.restartCh <- struct{}{}:
	default:
	}
	if cancel != nil {
		cancel()
	}
}

func (r *codexRunner) drainRestart() bool {
	select {
	case <-r.restartCh:
		return true
	default:
		return false
	}
}

// Restart installs the model and effort from args and respawns the
// app-server, which resumes the same thread. The posture is not in args; it is
// held from SetSpawnPermissionMode and asserted on the next turn.
func (r *codexRunner) Restart(args []string) {
	r.setTurnSettings(args)
	r.endIteration()
}

// SetSpawnArgs installs the model and effort from args for the next turn,
// leaving the live process alone.
func (r *codexRunner) SetSpawnArgs(args []string) { r.setTurnSettings(args) }

// SetModel takes effect on the next turn, which carries the model as its
// override, so it reports success.
func (r *codexRunner) SetModel(model string) error {
	r.mu.Lock()
	r.model = model
	r.mu.Unlock()
	return nil
}

// SetEffort takes effect on the next turn, which carries the effort as its
// override. It is the pool's in-band effort path for Codex, in place of the
// "/effort" command turn Claude takes, which Codex would run as a prompt.
func (r *codexRunner) SetEffort(effort string) error {
	r.mu.Lock()
	r.effort = effort
	r.mu.Unlock()
	return nil
}

// SetSpawnPermissionMode stores the posture the next turn asserts. Codex has
// no spawn-time posture beyond the home's read-only baseline.
func (r *codexRunner) SetSpawnPermissionMode(mode string) {
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
}

// SetPermissionMode is the live posture change: it takes effect on the next
// turn, which carries the posture as its overrides, so it reports success. A
// turn already running keeps the posture it started with. An unrecognised
// mode maps to read-only in codexTurnOverrides.
func (r *codexRunner) SetPermissionMode(mode string) error {
	r.SetSpawnPermissionMode(mode)
	return nil
}

// BeginTeardown refuses turns until the next client binds and declines every
// parked approval.
func (r *codexRunner) BeginTeardown() {
	r.mu.Lock()
	r.tearingDown = true
	r.mu.Unlock()
	r.cfg.Approvals.declineAll(reasonCodexTeardown)
}

// BeginRotation refuses turns until a client binds from a spawn that follows
// a RestartFresh issued after this call. The returned abort clears the gate
// unless another BeginRotation has re-armed it since.
func (r *codexRunner) BeginRotation() func() {
	r.mu.Lock()
	r.rotating = true
	r.rotateGen++
	r.armFreshSeq = r.freshSeq
	gen := r.rotateGen
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		if r.rotateGen == gen {
			r.rotating = false
		}
		r.mu.Unlock()
	}
}

// RestartFresh moves the runner onto the pool's new session id and respawns
// on a new Codex thread; the old thread id is dropped, not resumed.
func (r *codexRunner) RestartFresh(newID string) {
	if newID == "" {
		r.log.Warn("codex: RestartFresh called with empty id; ignoring")
		return
	}
	r.mu.Lock()
	r.threadID = ""
	r.freshSeq++
	r.cfg.Tag.Rotate(newID)
	r.mu.Unlock()
	r.endIteration()
}

var _ sessions.Runner = (*codexRunner)(nil)

// Content-free deny reasons for the paths the operator did not choose. They
// never reach Codex, which is only ever told "decline".
const (
	reasonCodexWithdrawn = "approval request withdrawn by Codex"
	reasonCodexInterrupt = "approval request ended by an interrupt"
	reasonCodexTeardown  = "approval request ended with its session"
	reasonCodexExit      = "approval request ended with its Codex process"
)

const (
	// codexMaxDescription caps the modal description built from a request.
	codexMaxDescription = 4096
	// codexMaxTrackedChanges caps the file-change items whose paths are held
	// for a later approval request.
	codexMaxTrackedChanges = 256
)

// codexApprovals routes one runner's Codex approval requests onto the
// daemon-wide approval registry and the permission modal, as
// stdioPermissionHandler does for a Claude session. live holds only this
// runner's parked requests, so its decline paths leave other sessions alone.
// mu is a leaf, never held across a registry, surface or Codex call.
type codexApprovals struct {
	registry *permbridge.Registry
	timeout  time.Duration
	surface  *approvalSurfaceReport

	mu      sync.Mutex
	live    map[string]*codexApproval // registry id → parked request
	changes map[string][]string       // fileChange item id → its paths
}

// codexApproval is one parked request. withdrawn is set when Codex resolved it
// on its own, before the registry entry is resolved, so its await writes
// nothing to Codex.
type codexApproval struct {
	req       *codexsup.ServerRequest
	withdrawn atomic.Bool
}

// newCodexApprovals returns nil without a registry: every request then takes
// codexsup's default decline.
func newCodexApprovals(registry *permbridge.Registry, timeout time.Duration, surface *approvalSurfaceReport) *codexApprovals {
	if registry == nil {
		return nil
	}
	return &codexApprovals{
		registry: registry,
		timeout:  timeout,
		surface:  surface,
		live:     make(map[string]*codexApproval),
		changes:  make(map[string][]string),
	}
}

// handle is codexsup.Config.OnServerRequest. It runs on the read loop, so it
// only parks the request; surfacing, waiting and the answer run on await's
// goroutine. Everything it cannot park is declined.
func (a *codexApprovals) handle(req *codexsup.ServerRequest) {
	if a == nil {
		_ = req.Decline()
		return
	}
	var item struct {
		ItemID string `json:"itemId"`
	}
	_ = json.Unmarshal(req.Params, &item)
	a.mu.Lock()
	paths := a.changes[item.ItemID]
	a.mu.Unlock()
	parked, ok := codexApprovalRequest(req.Method, req.Params, paths)
	if !ok {
		_ = req.Decline()
		return
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = req.Decline()
		return
	}
	id := "codex-" + hex.EncodeToString(nonce[:])
	parked.ToolUseID = id
	pending, err := a.registry.Register(id, parked, a.timeout)
	if err != nil {
		_ = req.Decline()
		return
	}
	ap := &codexApproval{req: req}
	a.mu.Lock()
	a.live[id] = ap
	a.mu.Unlock()
	go a.await(id, ap, parked, pending)
}

// await is the sole writer of the request's answer, and writes it only when
// Codex is still waiting for it.
func (a *codexApprovals) await(id string, ap *codexApproval, parked permbridge.Request, pending *permbridge.Pending) {
	retire := a.surface.surface(parked)
	verdict := pending.Await()
	a.mu.Lock()
	if a.live[id] == ap {
		delete(a.live, id)
	}
	a.mu.Unlock()
	if !ap.withdrawn.Load() {
		_ = ap.req.Respond(map[string]string{"decision": codexDecision(verdict)})
	}
	retire()
}

// codexDecision maps a registry verdict onto Codex's approval vocabulary. Only
// an explicit allow accepts; cancel, which also interrupts the turn, is never
// produced.
func codexDecision(v permbridge.Verdict) string {
	switch {
	case v.Behavior == permbridge.BehaviorAllow && v.ForSession:
		return "acceptForSession"
	case v.Behavior == permbridge.BehaviorAllow:
		return "accept"
	default:
		return "decline"
	}
}

// observe runs on the read loop for every notification. It holds each
// file-change item's paths, which its approval request does not carry, and
// retires a parked request that Codex resolved on its own.
func (a *codexApprovals) observe(method string, params json.RawMessage) {
	if a == nil {
		return
	}
	switch method {
	case "item/started", "item/completed":
		var p struct {
			Item struct {
				Type    string `json:"type"`
				ID      string `json:"id"`
				Changes []struct {
					Path string `json:"path"`
				} `json:"changes"`
			} `json:"item"`
		}
		if json.Unmarshal(params, &p) != nil || p.Item.Type != "fileChange" {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if method == "item/completed" {
			delete(a.changes, p.Item.ID)
			return
		}
		if len(a.changes) >= codexMaxTrackedChanges {
			return
		}
		paths := make([]string, 0, len(p.Item.Changes))
		for _, c := range p.Item.Changes {
			paths = append(paths, c.Path)
		}
		a.changes[p.Item.ID] = paths
	case "serverRequest/resolved":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(params, &p) != nil || len(p.RequestID) == 0 {
			return
		}
		want := bytes.TrimSpace(p.RequestID)
		// Every match: Codex numbers requests per process, and an entry from a
		// dead process may linger until its await deletes it. Resolving that
		// one again is a registry no-op.
		var ids []string
		a.mu.Lock()
		for id, ap := range a.live {
			if bytes.Equal(bytes.TrimSpace(ap.req.ID), want) {
				ap.withdrawn.Store(true)
				ids = append(ids, id)
			}
		}
		a.mu.Unlock()
		for _, id := range ids {
			a.registry.Resolve(id, permbridge.Deny(reasonCodexWithdrawn))
		}
	}
}

// declineAll resolves every request this runner has parked to a decline and
// forgets the held file-change paths. The registry one-shot arbitrates a
// racing answer or window; await stays the sole writer.
func (a *codexApprovals) declineAll(reason string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	ids := make([]string, 0, len(a.live))
	for id := range a.live {
		ids = append(ids, id)
	}
	clear(a.changes)
	a.mu.Unlock()
	for _, id := range ids {
		a.registry.Resolve(id, permbridge.Deny(reason))
	}
}

// codexApprovalRequest builds the modal's request from a command or
// file-change approval. params are untrusted: every string shown passes
// through codexDisplay. paths are the file-change item's, held by observe.
// Any other method, or params that do not parse, is not parked. Neither is a
// request whose grant the modal cannot show in full: stdin for a running
// terminal, a network approval, a missing command, a command too long to
// show with its cwd, a session-wide write root, or a file change whose paths
// were not seen.
func codexApprovalRequest(method string, params json.RawMessage, paths []string) (permbridge.Request, bool) {
	var p struct {
		Command            string            `json:"command"`
		Cwd                string            `json:"cwd"`
		Kind               string            `json:"kind"`
		NetworkContext     json.RawMessage   `json:"networkApprovalContext"`
		GrantRoot          json.RawMessage   `json:"grantRoot"`
		Reason             string            `json:"reason"`
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return permbridge.Request{}, false
	}
	req := permbridge.Request{Input: params}
	var label string
	switch method {
	case "item/commandExecution/requestApproval":
		if (p.Kind != "" && p.Kind != "command") || !jsonAbsent(p.NetworkContext) || p.Command == "" {
			return permbridge.Request{}, false
		}
		label = codexDisplay(p.Command)
		req.ToolName = "Codex command"
		req.Description = "command: " + label + "\ncwd: " + codexDisplay(p.Cwd)
		if len(req.Description) > codexMaxDescription {
			return permbridge.Request{}, false
		}
	case "item/fileChange/requestApproval":
		if !jsonAbsent(p.GrantRoot) || len(paths) == 0 {
			return permbridge.Request{}, false
		}
		shown := make([]string, len(paths))
		for i, path := range paths {
			shown[i] = codexDisplay(path)
		}
		label = strings.Join(shown, ", ")
		req.ToolName = "Codex file change"
		req.Description = truncateDisplay(strings.Join(append([]string{"paths:"}, shown...), "\n"), codexMaxDescription)
	default:
		return permbridge.Request{}, false
	}
	if p.Reason != "" {
		req.DecisionReason, _ = json.Marshal(codexDisplay(p.Reason))
	}
	for _, d := range p.AvailableDecisions {
		var s string
		if json.Unmarshal(d, &s) == nil && s == "acceptForSession" {
			req.AlwaysAllow = permbridge.SessionGrant(label)
			break
		}
	}
	return req, true
}

// codexDisplay escapes every non-printable rune, so a newline, control or
// bidi-format character in a Codex-supplied string cannot forge another line
// of the modal.
func codexDisplay(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strconv.IsPrint(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteString(strings.Trim(strconv.QuoteRune(r), "'"))
	}
	return b.String()
}

// jsonAbsent reports whether an optional field was left out or sent as null.
func jsonAbsent(v json.RawMessage) bool {
	v = bytes.TrimSpace(v)
	return len(v) == 0 || bytes.Equal(v, []byte("null"))
}

// codexTruncatedMarker ends a cut description, so the operator sees that
// something is not shown. The cut leaves room for it within the cap.
const codexTruncatedMarker = "\n…[truncated %d bytes]"

// truncateDisplay cuts s on a rune boundary to fit n bytes with the marker
// appended.
func truncateDisplay(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := max(0, n-len(fmt.Sprintf(codexTruncatedMarker, len(s))))
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf(codexTruncatedMarker, len(s)-cut)
}
