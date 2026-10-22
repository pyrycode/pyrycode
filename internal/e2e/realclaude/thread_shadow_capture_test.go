//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestThreadShadowHistoryCapture arms under the ordinary live suite whenever
// either record is absent. Generated records still require builder gate pinning.
func TestThreadShadowHistoryCapture(t *testing.T) {
	dir := filepath.Join(packageDir(t), "testdata")
	missing := false
	for _, name := range []string{shadowHistoryFile, shadowExpectedFile} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			t.Fatal("unsafe shadow artifact leaf")
		}
	}
	if !missing {
		h, e, err := shadowReadPair(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !t.Run(shadowScenarioCheck, func(t *testing.T) {
			if err := shadowValidatePair(h, e, true); err != nil {
				t.Fatal(err)
			}
		}) {
			return
		}
		for i, name := range shadowChecks {
			t.Run(name, func(t *testing.T) { shadowReplay(t, h, shadowExpected{Checkpoints: e.Checkpoints[i : i+1]}, false) })
		}
		return
	}
	h, conv := startStreamRunningTurnHarness(t)
	version, _ := captureClaudeVersion(t)
	source, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal("daemon source commit unavailable")
	}

	snapshot := scenario.SourceSince(t, h.home, conv)
	poll := scenario.PollingReceive(t, h.phone, h.initRecv)
	var mark uint64
	var legacy []protocol.Envelope
	var pending []history.Entry
	ends, toolWindow := 0, false
	read := func(deadline time.Time) protocol.Envelope {
		p := time.Now().Add(20 * time.Millisecond)
		if deadline.Before(p) {
			p = deadline
		}
		env := poll(p)
		switch env.Type {
		case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd, protocol.TypeSessionTransition, protocol.TypeModelRefusalFallback, protocol.TypeModelRefusalNoFallback, protocol.TypeCompactionBoundary, protocol.TypeUnrecognizedMessage:
			t.Fatal("retired shadow content reached app")
		}
		for _, entry := range snapshot(conv) {
			if entry.ID <= mark {
				continue
			}
			mark = entry.ID
			if entry.Type == protocol.TypeSessionTransition {
				legacy = append(legacy, protocol.Envelope{Type: entry.Type, TS: entry.TS, Payload: entry.Payload})
			}
			var p struct {
				Parent string `json:"parent_tool_use_id"`
				Name   string `json:"name"`
			}
			if json.Unmarshal(entry.Payload, &p) != nil {
				t.Fatal("invalid shadow source")
			}
			if entry.Type == protocol.TypeToolUse && p.Parent == "" && p.Name == "Bash" {
				toolWindow = true
			}
			if entry.Type == protocol.TypeTurnEnd && p.Parent == "" {
				if entry.Session == nil {
					t.Fatal("shadow terminal lacks producing session")
				}
				pending = append(pending, entry)
			}
		}
		if env.Type == protocol.TypeTurnState && len(pending) > 0 {
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid shadow live phase")
			}
			if p.ConversationID == conv && p.State == "idle" && !env.TS.Before(pending[0].TS) {
				ends++
				pending = pending[1:]
			}
		}
		if env.Type == protocol.TypeError {
			t.Fatal("shadow request failed")
		}
		if !time.Now().Before(deadline) {
			t.Fatal("shadow observation timed out")
		}
		return env
	}
	sealSendMessage(t, h.phone, h.initSend, 2, conv, "shadow-start", fmt.Sprintf(
		"Follow these steps exactly. First say SHADOW_BEFORE. Then use Bash once in the foreground (never background) to run: for i in $(seq 1 15); do sleep 1; done; echo SHADOW_TOOL. After Bash completes say SHADOW_AFTER. Then use Agent exactly once in the foreground; ask it to say exactly SHADOW_CHILD without tools. Wait for it and then say SHADOW_MAIN_DONE. No other tools. run=%d", time.Now().UnixNano()))
	deadline := time.Now().Add(3 * perTurnReplyBudget)
	sent := false
	for ends < 2 {
		read(deadline)
		if toolWindow && !sent {
			sealSendMessage(t, h.phone, h.initSend, 3, conv, "shadow-queued", "SHADOW_QUEUED: reply exactly SHADOW_REPLY without tools.")
			sent = true
		}
	}
	toolWindow = false
	sealSendMessage(t, h.phone, h.initSend, 4, conv, "shadow-close", "Use Bash once in the foreground: for i in $(seq 1 15); do sleep 1; done; echo SHADOW_CLOSURE.")
	deadline = time.Now().Add(perTurnReplyBudget)
	for !toolWindow {
		read(deadline)
	}
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: 5, Type: protocol.TypeNewSession, TS: time.Now().UTC()})
	deadline = time.Now().Add(rotateBudget)
	for {
		read(deadline)
		if len(legacy) > 0 {
			env := legacy[len(legacy)-1]
			var p protocol.SessionTransitionPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid closure transition")
			}
			if p.ConversationID == conv {
				break
			}
		}
	}
	log := history.New(filepath.Join(h.home, ".pyry", "test"))
	var entries []history.Entry
	page, err := log.Page(conversations.ConversationID(conv), "", 1)
	if err != nil || len(page.Entries) == 0 {
		t.Fatal("raw history boundary unavailable")
	}
	reader, err := log.Forward(conversations.ConversationID(conv), 0)
	if err != nil {
		t.Fatal("raw reader unavailable")
	}
	if reader.Walk(context.Background(), page.Entries[0].ID, func(chunk []history.Entry) error { entries = append(entries, chunk...); return nil }) != nil {
		t.Fatal("raw history incomplete")
	}

	raw, _ := json.Marshal(shadowHistory{Conversation: conv, Entries: entries})
	raw, err = shadowSanitize(raw)
	if err != nil {
		t.Fatal(err)
	}
	var retained shadowHistory
	if json.Unmarshal(raw, &retained) != nil {
		t.Fatal("cannot decode sanitized history")
	}
	raw, _ = json.Marshal(legacy)
	raw, err = shadowSanitize(raw)
	if err != nil {
		t.Fatal(err)
	}
	expected := shadowExpected{}
	if json.Unmarshal(raw, &expected.Legacy) != nil {
		t.Fatal("cannot decode sanitized observations")
	}
	var acceptance uint64
	versions := map[string]uint64{}
	for _, entry := range retained.Entries {
		var p struct {
			Message  string `json:"message_id"`
			Accepted uint64 `json:"accepted_entry_id"`
			Parent   string `json:"parent_tool_use_id"`
		}
		_ = json.Unmarshal(entry.Payload, &p)
		if entry.Type == "send_accepted" && p.Message == "shadow-queued" {
			acceptance = entry.ID
			versions["before_delivery"] = entry.ID
		}
		if entry.Type == protocol.TypeTurnEnd && p.Parent == "" && versions["main_complete"] == 0 {
			versions["main_complete"] = entry.ID
		}
		if entry.Type == "send_delivered" && acceptance != 0 && p.Accepted == acceptance {
			versions["after_delivery"] = entry.ID
		}
		if entry.Type == "session_divider" && entry.ID > versions["after_delivery"] && versions["session_closed"] == 0 {
			versions["session_closed"] = entry.ID
		}
	}
	checks := map[string]shadowCheck{}
	for _, name := range shadowChecks {
		cp := shadowCheckpoint{Name: name, Version: versions[name]}
		if cp.Version == 0 {
			t.Fatal("required raw checkpoint absent")
		}
		passed := t.Run(name, func(t *testing.T) {
			one := shadowReplay(t, retained, shadowExpected{Checkpoints: []shadowCheckpoint{cp}}, true)
			cp = one.Checkpoints[0]
		})
		if !passed {
			return
		}
		checks[name] = shadowCheck{Executed: 1}
		expected.Checkpoints = append(expected.Checkpoints, cp)
	}
	retained.Provenance = shadowProvenance{Schema: 1, Capture: fmt.Sprintf("shadow-%d", time.Now().UnixNano()), ClaudeVersion: version, DaemonCommit: strings.TrimSpace(string(source)), HistorySHA256: shadowDigest(retained.Entries), Checks: checks}
	expected.Provenance = retained.Provenance
	if !t.Run(shadowScenarioCheck, func(t *testing.T) {
		checks[shadowScenarioCheck] = shadowCheck{Executed: 1}
		if err := shadowValidatePair(retained, expected, false); err != nil {
			t.Fatal(err)
		}
	}) {
		return
	}
	scanner := newDropcapScanner(h.home, dir, h.workdir)
	for _, record := range []struct {
		name  string
		value any
	}{{shadowHistoryFile, retained}, {shadowExpectedFile, expected}} {
		raw, err := json.Marshal(record.value)
		if err != nil {
			t.Fatal("cannot encode shadow artifact")
		}
		hits, _ := scanner.scan(raw)
		if len(hits) != 0 || shadowDeny(raw) != nil {
			t.Fatal("shadow artifact failed complete credential/path deny scan")
		}
		tmp, err := os.CreateTemp(dir, ".shadow-*")
		if err != nil {
			t.Fatal("cannot create shadow artifact")
		}
		path := tmp.Name()
		writeErr := tmp.Chmod(0600)
		if writeErr == nil {
			_, writeErr = tmp.Write(append(raw, '\n'))
		}
		if writeErr == nil {
			writeErr = tmp.Sync()
		}
		closeErr := tmp.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			t.Fatal("cannot persist shadow artifact")
		}
		if os.Rename(path, filepath.Join(dir, record.name)) != nil {
			_ = os.Remove(path)
			t.Fatal("cannot publish shadow artifact")
		}
	}
	t.Log("Retain thread_shadow_history.json and thread_shadow_expected.json; builder must pin both to this run's counted dispatcher report before committing.")
}
