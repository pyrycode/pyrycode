//go:build e2e_realclaude

package realclaude

// TestRealClaudeAppliedSettingsBeforeFirstTurn asks two clean live children for the
// settings Claude actually applied. The inherited arm discovers an effort level from
// this installation's initialize menu; the explicit arm launches with that value.
// Neither arm sends a user turn, and both disable saved settings sources.
//
// Run through the dispatcher-owned live gate. A local reproduction is:
//
//	go test -tags e2e_realclaude -count=1 -v \
//	  -run '^TestRealClaudeAppliedSettingsBeforeFirstTurn$' ./internal/e2e/realclaude/
//
// This test writes no fixture. Read the named === RUN result rather than the process
// exit status: missing credentials skip the tagged suite successfully.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	appliedSettingsLiveModel       = "opus"
	appliedSettingsLiveQueryBudget = 30 * time.Second
	appliedSettingsLiveMenuBudget  = 30 * time.Second
)

type liveAppliedSettings struct {
	Model  string
	Effort *string
}

// appliedSettingsReplyTap independently decodes only response.applied from the raw
// child stream. It deliberately does not reuse streamsup's decoder: agreement between
// two observations is the live proof, not a result compared with itself.
type appliedSettingsReplyTap struct {
	mu      sync.Mutex
	buf     []byte
	replies []liveAppliedSettings
}

func (t *appliedSettingsReplyTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		at := bytes.IndexByte(t.buf, '\n')
		if at < 0 {
			break
		}
		line := bytes.Clone(t.buf[:at])
		t.buf = append(t.buf[:0], t.buf[at+1:]...)
		if reply, ok := decodeLiveAppliedSettings(line); ok {
			t.replies = append(t.replies, reply)
		}
	}
	return len(p), nil
}

func decodeLiveAppliedSettings(line []byte) (liveAppliedSettings, bool) {
	var outer struct {
		Type     string `json:"type"`
		Response struct {
			Subtype  string `json:"subtype"`
			Response *struct {
				Applied json.RawMessage `json:"applied"`
			} `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &outer) != nil || outer.Type != "control_response" ||
		outer.Response.Subtype != "success" || outer.Response.Response == nil ||
		len(outer.Response.Response.Applied) == 0 {
		return liveAppliedSettings{}, false
	}
	var applied struct {
		Model  json.RawMessage `json:"model"`
		Effort json.RawMessage `json:"effort"`
	}
	if json.Unmarshal(outer.Response.Response.Applied, &applied) != nil ||
		len(applied.Model) == 0 || len(applied.Effort) == 0 {
		return liveAppliedSettings{}, false
	}
	var model string
	if json.Unmarshal(applied.Model, &model) != nil || model == "" {
		return liveAppliedSettings{}, false
	}
	if bytes.Equal(bytes.TrimSpace(applied.Effort), []byte("null")) {
		return liveAppliedSettings{Model: model}, true
	}
	var effort string
	if json.Unmarshal(applied.Effort, &effort) != nil {
		return liveAppliedSettings{}, false
	}
	return liveAppliedSettings{Model: model, Effort: &effort}, true
}

func (t *appliedSettingsReplyTap) latest() (liveAppliedSettings, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.replies) == 0 {
		return liveAppliedSettings{}, false
	}
	return t.replies[len(t.replies)-1], true
}

type liveAppliedSettingsArm struct {
	name        string
	sessionID   string
	effort      string
	requestMenu bool
}

func runLiveAppliedSettingsArm(t *testing.T, claudeBin, home string, arm liveAppliedSettingsArm) (streamsup.AppliedSettings, liveAppliedSettings, turnevent.ModelList) {
	t.Helper()
	workdir := filepath.Join(home, "applied-settings-"+arm.name)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create %s workdir: %v", arm.name, err)
	}

	menuCh := make(chan turnevent.ModelList, 1)
	parser := streamsup.NewParser(func(ev turnevent.Event) {
		if menu, ok := ev.(turnevent.ModelList); ok {
			select {
			case menuCh <- menu:
			default:
			}
		}
	}, slog.New(slog.DiscardHandler))
	tap := &appliedSettingsReplyTap{}
	args := []string{
		"--model", appliedSettingsLiveModel,
		"--setting-sources", "",
		"--dangerously-skip-permissions",
	}
	if arm.effort != "" {
		args = append(args, "--effort", arm.effort)
	}
	runner, err := streamsup.New(streamsup.Config{
		ClaudeBin:                claudeBin,
		WorkDir:                  workdir,
		SessionID:                arm.sessionID,
		Args:                     args,
		Stdout:                   io.MultiWriter(tap, parser),
		Stderr:                   io.Discard,
		Logger:                   slog.New(slog.DiscardHandler),
		RequestInitializeOnSpawn: arm.requestMenu,
	})
	if err != nil {
		t.Fatalf("construct %s runner: %v", arm.name, err)
	}

	runCtx, stop := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(runCtx) }()
	defer func() {
		stop()
		select {
		case <-runDone:
		case <-time.After(inbandRunExitWait):
			t.Errorf("%s runner did not stop within %s", arm.name, inbandRunExitWait)
		}
	}()

	if !inbandWaitForChild(inbandRunner{Runner: runner}) {
		t.Fatalf("%s child did not become live within %s", arm.name, inbandSpawnWait)
	}

	var menu turnevent.ModelList
	if arm.requestMenu {
		select {
		case menu = <-menuCh:
		case <-time.After(appliedSettingsLiveMenuBudget):
			t.Fatalf("%s child returned no initialize model menu within %s", arm.name, appliedSettingsLiveMenuBudget)
		}
	}

	queryCtx, cancelQuery := context.WithTimeout(context.Background(), appliedSettingsLiveQueryBudget)
	defer cancelQuery()
	settings, ok := runner.QueryAppliedSettings(queryCtx)
	if !ok {
		t.Fatalf("%s get_settings query was unavailable before the first user turn", arm.name)
	}
	raw, ok := tap.latest()
	if !ok {
		t.Fatalf("%s query returned but the independent stdout tap saw no successful response.applied", arm.name)
	}
	return settings, raw, menu
}

func pickLiveAppliedEffort(t *testing.T, menu turnevent.ModelList) (string, string) {
	t.Helper()
	for _, model := range menu.Models {
		if model.Value != appliedSettingsLiveModel || model.ResolvedModel == "" ||
			len(model.EffortLevels) == 0 || slices.Contains(model.TruncatedFields, "effort_levels") {
			continue
		}
		return model.ResolvedModel, model.EffortLevels[0]
	}
	t.Fatalf("installed Claude menu has no complete %q row with an advertised effort level: %+v",
		appliedSettingsLiveModel, menu.Models)
	return "", ""
}

func assertLiveAppliedSettingsMatch(t *testing.T, name string, got streamsup.AppliedSettings, raw liveAppliedSettings) {
	t.Helper()
	if got.Model != raw.Model {
		t.Fatalf("%s parsed model %q, raw applied reply reports %q", name, got.Model, raw.Model)
	}
	switch {
	case got.Effort == nil && raw.Effort == nil:
	case got.Effort == nil:
		t.Fatalf("%s parsed null effort, raw applied reply reports %q", name, *raw.Effort)
	case raw.Effort == nil:
		t.Fatalf("%s parsed effort %q, raw applied reply reports explicit null", name, *got.Effort)
	case *got.Effort != *raw.Effort:
		t.Fatalf("%s parsed effort %q, raw applied reply reports %q", name, *got.Effort, *raw.Effort)
	}
}

func TestRealClaudeAppliedSettingsBeforeFirstTurn(t *testing.T) {
	claudeBin := resolveClaudeBin(t)
	home := WithWorktreeAuthenticated(t)

	inherited, inheritedRaw, menu := runLiveAppliedSettingsArm(t, claudeBin, home, liveAppliedSettingsArm{
		name:        "inherited",
		sessionID:   "25050000-0000-4000-8000-000000000001",
		requestMenu: true,
	})
	assertLiveAppliedSettingsMatch(t, "inherited", inherited, inheritedRaw)
	resolvedModel, advertisedEffort := pickLiveAppliedEffort(t, menu)
	if inherited.Model != resolvedModel {
		t.Fatalf("inherited applied model %q, installed %q menu row resolves to %q",
			inherited.Model, appliedSettingsLiveModel, resolvedModel)
	}

	explicit, explicitRaw, _ := runLiveAppliedSettingsArm(t, claudeBin, home, liveAppliedSettingsArm{
		name:      "explicit",
		sessionID: "25050000-0000-4000-8000-000000000002",
		effort:    advertisedEffort,
	})
	assertLiveAppliedSettingsMatch(t, "explicit", explicit, explicitRaw)
	if explicit.Model != resolvedModel {
		t.Fatalf("explicit applied model %q, installed %q menu row resolves to %q",
			explicit.Model, appliedSettingsLiveModel, resolvedModel)
	}
	if explicit.Effort == nil || *explicit.Effort != advertisedEffort {
		t.Fatalf("explicit applied effort = %v, want installed Claude's advertised level %q",
			explicit.Effort, advertisedEffort)
	}

	// The inherited value is intentionally only reported, never compared with a fixed
	// daemon default. It may change with the installed Claude release or user policy.
	if inherited.Effort == nil {
		t.Logf("inherited applied settings: model=%q effort=null; explicit effort=%q",
			inherited.Model, advertisedEffort)
	} else {
		t.Logf("inherited applied settings: model=%q effort=%q; explicit effort=%q",
			inherited.Model, *inherited.Effort, advertisedEffort)
	}
}
