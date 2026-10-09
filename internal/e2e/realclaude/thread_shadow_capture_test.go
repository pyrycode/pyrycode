//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		if err := shadowValidatePair(h, e, true); err != nil {
			t.Fatal(err)
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
	sealSendMessage(t, h.phone, h.initSend, 2, conv, "shadow-start", fmt.Sprintf(
		"Follow these steps exactly. First say SHADOW_BEFORE. Then use Bash once in the foreground (never background) to run: for i in $(seq 1 15); do sleep 1; done; echo SHADOW_TOOL. After Bash completes say SHADOW_AFTER. Then use Agent exactly once in the foreground; ask it to say exactly SHADOW_CHILD without tools. Wait for it and then say SHADOW_MAIN_DONE. No other tools. run=%d", time.Now().UnixNano()))
	var legacy []protocol.Envelope
	deadline := time.Now().Add(3 * perTurnReplyBudget)
	sent, ends := false, 0
	for ends < 2 {
		env, ok := nextSubagentTextEnvelope(t, h, deadline)
		if !ok {
			t.Fatal("shadow conversation did not complete")
		}
		switch env.Type {
		case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeMessage, protocol.TypeQueueState, protocol.TypeTurnEnd, protocol.TypeSessionTransition:
			var owner struct {
				Conversation string `json:"conversation_id"`
			}
			_ = json.Unmarshal(env.Payload, &owner)
			if owner.Conversation == conv {
				legacy = append(legacy, env)
			}
		}
		if env.Type == protocol.TypeToolUse && !sent {
			var p protocol.ToolUsePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid tool observation")
			}
			if p.ConversationID == conv && p.Name == "Bash" && p.ParentToolUseID == "" {
				sealSendMessage(t, h.phone, h.initSend, 3, conv, "shadow-queued", "SHADOW_QUEUED: reply exactly SHADOW_REPLY without tools.")
				sent = true
			}
		}
		if env.Type == protocol.TypeTurnEnd {
			var p protocol.TurnEndPayload
			_ = json.Unmarshal(env.Payload, &p)
			if p.ConversationID == conv {
				ends++
			}
		}
	}
	if !sent {
		t.Fatal("no busy Bash window for queue acceptance")
	}
	// Close a genuinely active third turn so settlement before the divider is observable.
	sealSendMessage(t, h.phone, h.initSend, 4, conv, "shadow-close", "Use Bash once in the foreground: for i in $(seq 1 15); do sleep 1; done; echo SHADOW_CLOSURE.")
	deadline = time.Now().Add(perTurnReplyBudget)
	for {
		env, ok := nextSubagentTextEnvelope(t, h, deadline)
		if !ok {
			t.Fatal("no active closure work")
		}
		if env.Type == protocol.TypeAssistantDelta || env.Type == protocol.TypeToolUse {
			legacy = append(legacy, env)
		}
		if env.Type == protocol.TypeToolUse {
			var p protocol.ToolUsePayload
			_ = json.Unmarshal(env.Payload, &p)
			if p.ConversationID == conv && p.Name == "Bash" {
				break
			}
		}
	}
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: 5, Type: protocol.TypeNewSession, TS: time.Now().UTC()})
	deadline = time.Now().Add(rotateBudget)
	for {
		env, ok := nextSubagentTextEnvelope(t, h, deadline)
		if !ok {
			t.Fatal("session closure absent")
		}
		if env.Type == protocol.TypeAssistantDelta || env.Type == protocol.TypeToolResult || env.Type == protocol.TypeTurnEnd {
			legacy = append(legacy, env)
		}
		if env.Type == protocol.TypeSessionTransition {
			var p protocol.SessionTransitionPayload
			_ = json.Unmarshal(env.Payload, &p)
			if p.ConversationID == conv {
				legacy = append(legacy, env)
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
	if err := shadowValidatePair(retained, expected, false); err != nil {
		t.Fatal(err)
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
