package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestCodexSession_ClaudeOnlyFeaturesUnavailable (#2586): MCP status, MCP
// reconnect, context-usage detail, the applied-settings query and the
// slash-command list each give their existing unavailable or refused reply
// for a conversation bound to a Codex session. codexRunner implements none of
// the Claude-only seams, so each resolver falls through its type assertion
// without calling the runner, which here is never started.
func TestCodexSession_ClaudeOnlyFeaturesUnavailable(t *testing.T) {
	t.Parallel()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return newCodexRunner(codexRunnerConfig{Tag: newStreamSessionTag(cfg.SessionID)}), nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	id, err := pool.Mint("codex-claude-only", "")
	if err != nil && !errors.Is(err, sessions.ErrPoolNotRunning) {
		t.Fatalf("pool.Mint: %v", err)
	}
	reg := &conversations.Registry{}
	conv := conversations.Conversation{ID: "conv-codex", CurrentSessionID: string(id), LastUsedAt: time.Now().UTC()}
	reg.Create(conv)
	convID := string(conv.ID)

	runner, _, ok := resolveBoundRunner(reg, pool, convID)
	if !ok {
		t.Fatal("resolveBoundRunner refused the bound Codex session")
	}
	if _, isCodex := runner.(*codexRunner); !isCodex {
		t.Fatalf("bound runner is %T, want *codexRunner", runner)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok := resolveBoundMCPStatus(ctx, reg, pool, convID); ok {
		t.Error("MCP status answered for a Codex session")
	}
	if actuator, ok := boundMCPChildActuator(reg, pool)(convID); ok || actuator != nil {
		t.Error("MCP reconnect found an actuator on a Codex session")
	}
	querier, canonicalID, ok := contextUsageResolve(reg, pool)(convID)
	if querier != nil || !ok || canonicalID != conv.ID {
		t.Errorf("context usage resolve = %v, %q, %v; want no querier for a hosted conversation", querier, canonicalID, ok)
	}
	if effort, ok := resolveBoundEffectiveEffort(ctx, reg, pool, convID); ok || effort != nil {
		t.Error("applied-settings query answered for a Codex session")
	}
	if _, ok := resolveBoundSlashCommandList(reg, pool, convID); ok {
		t.Error("slash-command list answered for a Codex session")
	}
	if ctx.Err() != nil {
		t.Error("a resolver waited out the deadline instead of refusing")
	}
}
