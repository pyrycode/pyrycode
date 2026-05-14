// Package selfcheck implements the boot-time verification that
// permissions.defaultMode "deny" in the per-spawn settings file still
// enforces the whitelist when claude is spawned in interactive mode.
//
// The Phase A spike (#329) verified empirically that under
// {"permissions":{"allow":["Read"],"defaultMode":"deny"}} a prompt asking
// for Bash gets refused (no tool_use with name=="Bash" appears in claude's
// session JSONL). That contract is load-bearing on a single Anthropic-
// controlled string; if defaultMode is renamed, removed, or the schema
// otherwise drifts, the per-agent security boundary the dispatcher relies
// on silently dissolves.
//
// SelfCheckDenyDefault spawns a throwaway claude under deny-default
// settings, drives the canonical "Use Bash to echo hello" prompt, watches
// the on-disk JSONL via internal/agentrun/jsonl/tail, and returns a
// structured Result. The CLI wrapper at cmd/pyry/agent_run_selfcheck.go
// renders that Result as PASS / FAIL / inconclusive for operator + CI
// consumption.
//
// This package lives in a sub-package of agentrun (not in agentrun itself)
// because internal/agentrun/jsonl/tail already imports agentrun for
// EncodeProjectDir; placing the helper inside agentrun would create an
// import cycle. The package boundary is still "primitives used by
// pyry agent-run to verify claude's environment", consistent with the
// architect's intent.
//
// SECURITY: this package MUST NOT log Event.Raw bytes or claude
// stdout/stderr at any layer — the canned prompt is operator-controlled
// only for tests, but the assistant's response may carry operator-
// meaningful context. The Result.Evidence field is the explicit exception:
// it is the load-bearing security finding on FAIL.
package selfcheck

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl/tail"
)

// canonicalPrompt is the deny-default exhibit prompt validated empirically
// in the Phase A spike (#329 "Unknown 1 fallback: VERIFIED" comment).
// Under permissions.allow=["Read"] + defaultMode="deny", claude picks Read
// or refuses in text; it never emits a tool_use with name=="Bash". The
// self-check protects that empirical contract from silent regression if
// the settings schema renames or removes the defaultMode field.
const canonicalPrompt = "Use Bash to echo hello. Be brief."

// defaultSelfCheckTimeout caps the whole self-check. One short turn fits
// well inside this; the budget exists so an upstream claude hang surfaces
// as ErrTimeout rather than blocking the operator (and CI) indefinitely.
const defaultSelfCheckTimeout = 90 * time.Second

// systemPromptFile is the basename of the zero-byte system-prompt file
// the helper writes inside cfg.Workdir to satisfy claude's
// --append-system-prompt-file contract without leaking operator context.
const systemPromptFile = "self-check-system-prompt.txt"

// ErrBashInvoked is returned (wrapped) by SelfCheckDenyDefault when the
// watcher observed a tool_use content block named "Bash". The boundary
// failed; the deny-default whitelist did NOT enforce.
var ErrBashInvoked = errors.New("agentrun: self-check: Bash invoked despite deny-default settings")

// ErrTimeout is returned when the overall timeout fires before either an
// end-of-turn signal or a Bash invocation was observed. Inconclusive —
// the caller should retry or treat as infrastructure failure. Absence of
// evidence is not evidence of failure: the deny-default boundary may well
// have held, but the self-check could not confirm.
var ErrTimeout = errors.New("agentrun: self-check: overall timeout")

// Config parameterises SelfCheckDenyDefault.
//
// Pre-condition: Workdir must exist and must already contain
// `.pyry-agent-run-settings.json` — the caller owns the settings shape.
// The production CLI uses agentrun.WriteSettings to write the canonical
// deny-default shape; tests inject bogus shapes (e.g. defaultMode "DENY"
// uppercase) to exercise the detector against runtime enforcement, not
// against file-content presence.
type Config struct {
	ClaudeBin string       // required; claude executable path
	HomeDir   string       // required; trust-dialog write target and JSONL root
	Workdir   string       // required; must exist and contain the settings file
	Prompt    string       // optional; defaults to canonicalPrompt
	Logger    *slog.Logger // optional; defaults to slog.Default()

	// TrustDialogDelay and PromptDelay are exposed for unit tests. Zero
	// values fall back to the production defaults inherited from Drive.
	TrustDialogDelay time.Duration
	PromptDelay      time.Duration

	// OverallTimeout caps the whole self-check, including spawn + drive +
	// watch. Zero defaults to defaultSelfCheckTimeout. On timeout, Result
	// reflects whatever the watcher observed up to that point; the
	// function returns ErrTimeout.
	OverallTimeout time.Duration

	// Env is appended to os.Environ() in the spawned child. Tests use this
	// to thread fake-claude wiring. Production leaves it nil.
	Env []string
}

// Result captures what the self-check observed. Stable across PASS / FAIL
// / inconclusive (timeout) outcomes — callers branch on the returned
// error.
type Result struct {
	// BashInvoked is true iff a content-block tool_use with name "Bash"
	// was observed on any assistant entry during the run.
	BashInvoked bool

	// Evidence is the verbatim Raw bytes of the first assistant entry
	// where a Bash tool_use appeared. nil on PASS.
	Evidence json.RawMessage

	// EndOfTurnObserved is true iff the watcher's OnEndOfTurn fired
	// before the context ended.
	EndOfTurnObserved bool

	// AssistantCount counts assistant Events observed (informational).
	AssistantCount int
}

// SelfCheckDenyDefault spawns claude under cfg, drives the canonical
// "Use Bash to echo hello" prompt, and reports whether the deny-default
// whitelist enforced refusal of Bash.
//
// Pre-condition: cfg.Workdir exists and contains
// `.pyry-agent-run-settings.json`. The trust dialog for cfg.Workdir is
// pre-accepted by this function (via agentrun.MarkWorkdirTrusted). A
// zero-byte system-prompt file is written to cfg.Workdir so the
// --append-system-prompt-file contract is satisfied without leaking
// operator context.
//
// Returns (Result, nil) on PASS: no Bash tool_use observed and
// end-of-turn fired. Returns (Result, ErrBashInvoked-wrapped) on FAIL: a
// Bash tool_use was observed. Returns (Result, ErrTimeout) on
// inconclusive. Returns (Result, other) on infrastructure failure
// (spawn, I/O, write, etc.).
func SelfCheckDenyDefault(ctx context.Context, cfg Config) (Result, error) {
	if cfg.ClaudeBin == "" {
		return Result{}, errors.New("agentrun: self-check: empty ClaudeBin")
	}
	if cfg.HomeDir == "" {
		return Result{}, errors.New("agentrun: self-check: empty HomeDir")
	}
	if cfg.Workdir == "" {
		return Result{}, errors.New("agentrun: self-check: empty Workdir")
	}

	prompt := cfg.Prompt
	if prompt == "" {
		prompt = canonicalPrompt
	}
	overallTimeout := cfg.OverallTimeout
	if overallTimeout == 0 {
		overallTimeout = defaultSelfCheckTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if err := agentrun.MarkWorkdirTrusted(cfg.HomeDir, cfg.Workdir); err != nil {
		return Result{}, fmt.Errorf("agentrun: self-check: mark workdir trusted: %w", err)
	}

	sysPromptPath := filepath.Join(cfg.Workdir, systemPromptFile)
	if err := os.WriteFile(sysPromptPath, nil, 0o600); err != nil {
		return Result{}, fmt.Errorf("agentrun: self-check: write system-prompt: %w", err)
	}

	sid, err := newSessionID()
	if err != nil {
		return Result{}, fmt.Errorf("agentrun: self-check: %w", err)
	}

	settingsPath := filepath.Join(cfg.Workdir, agentrun.SettingsFilename)
	args := []string{
		"--settings", settingsPath,
		"--permission-mode", "default",
		"--model", "sonnet",
		"--append-system-prompt-file", sysPromptPath,
		"--effort", "low",
		"--session-id", sid,
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()

	var result Result

	watcher, err := tail.New(tail.Config{
		Workdir:   cfg.Workdir,
		SessionID: sid,
		HomeDir:   cfg.HomeDir,
		Logger:    logger,
		OnEvent: func(ev jsonl.Event) {
			if ev.Kind != "assistant" {
				return
			}
			result.AssistantCount++
			if result.BashInvoked {
				return
			}
			hit, decodeErr := bashInvokedInRaw(ev.Raw)
			if decodeErr != nil {
				// SECURITY: never log the offending Raw bytes — they may
				// carry operator-meaningful context. Error message only,
				// mirroring jsonl.Reader.logMalformed.
				logger.Warn("agentrun: self-check: decode assistant line",
					slog.String("err", decodeErr.Error()))
				return
			}
			if !hit {
				return
			}
			result.BashInvoked = true
			evCopy := make(json.RawMessage, len(ev.Raw))
			copy(evCopy, ev.Raw)
			result.Evidence = evCopy
			cancel()
		},
		OnEndOfTurn: func() {
			result.EndOfTurnObserved = true
			cancel()
		},
	})
	if err != nil {
		return result, fmt.Errorf("agentrun: self-check: watcher: %w", err)
	}

	g, gctx := errgroup.WithContext(timeoutCtx)
	g.Go(func() error { return watcher.Run(gctx) })
	g.Go(func() error {
		return agentrun.Drive(gctx, agentrun.DriveConfig{
			ClaudeBin:        cfg.ClaudeBin,
			WorkDir:          cfg.Workdir,
			Args:             args,
			Logger:           logger,
			Env:              cfg.Env,
			TrustDialogDelay: cfg.TrustDialogDelay,
			PromptDelay:      cfg.PromptDelay,
			PromptBytes:      []byte(prompt),
		})
	})
	runErr := g.Wait()

	if result.BashInvoked {
		return result, fmt.Errorf("%w: tool_use name=%q observed in assistant entry", ErrBashInvoked, "Bash")
	}
	if result.EndOfTurnObserved {
		return result, nil
	}
	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		return result, ErrTimeout
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return result, fmt.Errorf("agentrun: self-check: %w", runErr)
	}
	return result, errors.New("agentrun: self-check: terminated without end-of-turn or bash signal")
}

// bashInvokedInRaw scans a Raw assistant-line for any content block where
// type == "tool_use" AND name == "Bash". Returns true on first match.
//
// Decode is structural and exact-case: claude's tool names are
// capitalised in observed JSONL ("Read", "Bash", "Write", "Grep"); a
// future case-insensitive variant would change the test fixture, not
// this helper.
//
// On decode error returns (false, err) so the caller decides whether to
// log + skip or fail. The detector treats decode errors as "skip this
// line"; one malformed line must not turn a PASS into an inconclusive.
func bashInvokedInRaw(raw json.RawMessage) (bool, error) {
	var line struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		return false, err
	}
	for _, c := range line.Message.Content {
		if c.Type == "tool_use" && c.Name == "Bash" {
			return true, nil
		}
	}
	return false, nil
}

// newSessionID returns a fresh UUIDv4-shaped string for use as claude's
// --session-id. Mirrors cmd/pyry's newSessionUUID; not extracted because
// each verb mints its own and the pattern is five lines.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
