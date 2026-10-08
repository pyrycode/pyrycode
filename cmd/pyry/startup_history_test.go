package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testStartupAppend(t *testing.T, store *history.Store, id, typ, turn, tool, name, parent string, source *history.SessionProvenance) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"conversation_id": id, "turn_id": turn, "tool_use_id": tool, "tool_call_id": tool, "name": name, "parent_tool_use_id": parent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendWithMetadata(conversations.ConversationID(id), typ, raw, time.Unix(1, 0), history.Metadata{Session: source}); err != nil {
		t.Fatal(err)
	}
}

func TestStartupHistoryLegacyScopes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID), CurrentSessionID: "registry-is-not-evidence"})
	put := func(typ, turn, tool string, source *history.SessionProvenance) {
		testStartupAppend(t, store, testConvID, typ, turn, tool, "Bash", "", source)
	}
	put(protocol.TypeToolUse, "reused", "call", nil)
	put(protocol.TypeSessionTransition, "", "", nil)
	put(protocol.TypeToolUse, "reused", "call", nil)
	put(protocol.TypeTurnEnd, "reused", "", nil)
	put(protocol.TypeToolResult, "reused", "call", nil)
	put(protocol.TypeToolUse, "", "missing-turn", nil)
	put(protocol.TypeToolUse, "missing-tool", "", nil)
	put(protocol.TypeToolDenied, "denial-only", "call", nil)
	a := &history.SessionProvenance{Kind: "claude", SessionID: "old"}
	b := &history.SessionProvenance{Kind: "codex", SessionID: "old"}
	put(protocol.TypeToolUse, "reused", "call", a)
	put(protocol.TypeToolUse, "reused", "call", b)
	put(protocol.TypeTurnEnd, "reused", "", b)
	before := historyEntries(t, store, testConvID)
	reconcileStartupHistory(store, reg, discardLogger(), time.Now())
	all := historyEntries(t, store, testConvID)
	if !reflect.DeepEqual(before, all[:len(before)]) {
		t.Fatal("legacy history rewritten")
	}
	tail := all[len(before):]
	if len(tail) != 7 {
		t.Fatalf("closures: %+v", tail)
	}
	for i, source := range []*history.SessionProvenance{nil, nil, nil, nil, a, a, {Kind: "none"}} {
		if !reflect.DeepEqual(tail[i].Session, source) {
			t.Fatalf("source at %d: %+v, want %+v", i, tail[i].Session, source)
		}
	}
	n := len(all)
	reconcileStartupHistory(history.New(dir), reg, discardLogger(), time.Now())
	if tail := historyEntries(t, store, testConvID)[n:]; len(tail) != 1 {
		t.Fatalf("legacy repeated closure: %+v", tail)
	}
}

func TestStartupHistoryDiscovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	reg := &conversations.Registry{}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		reg.Create(conversations.Conversation{ID: conversations.ConversationID(id), IsArchived: i == 1})
		if i < 2 {
			testStartupAppend(t, store, id, protocol.TypeMessage, "", "", "", "", nil)
			testStartupAppend(t, store, id, historyTurnOpened, "turn", "", "", "", nil)
			if i == 0 {
				testStartupAppend(t, store, id, protocol.TypeTurnEnd, "turn", "", "", "", nil)
			}
		}
		if i == 2 {
			if err := os.MkdirAll(filepath.Join(dir, "conversations", id, "history"), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	reconcileStartupHistory(store, reg, discardLogger(), time.Now())
	for i, c := range reg.List() {
		got := historyEntries(t, store, string(c.ID))
		if i < 2 && (len(got) != 4 || got[3].Type != historySessionDivider || (i == 1 && got[2].Type != historyTurnInterrupted)) {
			t.Fatalf("surviving %d: %+v", i, got)
		}
		if i >= 2 && len(got) != 0 {
			t.Fatalf("empty %d: %+v", i, got)
		}
		if i == 3 {
			if _, err := os.Stat(filepath.Join(dir, "conversations", string(c.ID))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("discovery created directory: %v", err)
			}
		}
	}
	newID := "00000000-0000-4000-8000-000000000099"
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(newID)})
	if len(historyEntries(t, store, newID)) != 0 {
		t.Fatal("new conversation received restart marker")
	}
}

func TestStartupHistoryPagination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID)})
	testStartupAppend(t, store, testConvID, historyTurnOpened, "turn", "", "", "", nil)
	testStartupAppend(t, store, testConvID, protocol.TypeToolUse, "turn", "done", "Bash", "", nil)
	testStartupAppend(t, store, testConvID, protocol.TypeToolUse, "turn", "open", "Read", "", nil)
	// Unknown raw entries fill multiple pages and force a segment rollover.
	raw, _ := json.Marshal(strings.Repeat("x", 8192))
	for i := 0; i < 140; i++ {
		if _, err := store.Append(conversations.ConversationID(testConvID), "future_fact", raw, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	testStartupAppend(t, store, testConvID, protocol.TypeToolResult, "turn", "done", "", "", nil)
	files, err := filepath.Glob(filepath.Join(dir, "conversations", testConvID, "history", "*.jsonl"))
	if err != nil || len(files) < 2 {
		t.Fatalf("segments: %v, %v", files, err)
	}
	reconcileStartupHistory(history.New(dir), reg, discardLogger(), time.Now())
	page, err := store.Page(conversations.ConversationID(testConvID), "", 3)
	if err != nil || len(page.Entries) != 3 || page.Entries[0].Type != historySessionDivider || page.Entries[1].Type != historyTurnInterrupted || page.Entries[2].Type != historyToolInterrupted {
		t.Fatalf("closure across pages: %+v, %v", page, err)
	}
	var p runtimeHistoryFact
	if err := json.Unmarshal(page.Entries[2].Payload, &p); err != nil || p.ToolCallID != "open" {
		t.Fatalf("tool: %+v, %v", p, err)
	}
}

func TestStartupHistoryFailures(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"read", "write"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: "secret-path-or-payload-sentinel"})
			bad := conversations.ConversationID(testConvID)
			good := conversations.ConversationID("00000000-0000-4000-8000-000000000099")
			for _, id := range []conversations.ConversationID{bad, good} {
				reg.Create(conversations.Conversation{ID: id})
				testStartupAppend(t, store, string(id), historyTurnOpened, "turn", "", "", "", nil)
			}
			files, _ := filepath.Glob(filepath.Join(dir, "conversations", string(bad), "history", "*.jsonl"))
			if len(files) != 1 {
				t.Fatal(files)
			}
			if failure == "read" {
				if err := os.WriteFile(files[0], []byte("secret-path-or-payload-sentinel\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(files[0], 0400); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(files[0], 0600) })
			}
			var logs bytes.Buffer
			reconcileStartupHistory(history.New(dir), reg, slog.New(slog.NewTextHandler(&logs, nil)), time.Now())
			if logs.Len() == 0 || strings.Contains(logs.String(), dir) || strings.Contains(logs.String(), "secret-path-or-payload-sentinel") {
				t.Fatalf("failure logs: %s", logs.String())
			}
			if failure == "write" && !strings.Contains(logs.String(), "event=startup_history.append_err") {
				t.Fatalf("write failure not exercised: %s", logs.String())
			}
			if failure == "read" && !strings.Contains(logs.String(), "conversation_id="+string(bad)) {
				t.Fatalf("read failure not exercised: %s", logs.String())
			}
			if entries := historyEntries(t, store, string(good)); len(entries) != 3 {
				t.Fatalf("healthy conversation blocked: %+v", entries)
			}
		})
	}
}

func TestStartupHistoryOrdering(t *testing.T) {
	for _, relayURL := range []string{"", "wss://127.0.0.1:1"} {
		t.Run("relay="+relayURL, func(t *testing.T) {
			home := shortTempDir(t)
			t.Setenv("HOME", home)
			t.Setenv("PYRY_RELAY_URL", relayURL)
			if err := os.MkdirAll(filepath.Dir(resolveConfigPath()), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(resolveConfigPath(), []byte(`{"relay_url":""}`), 0600); err != nil {
				t.Fatal(err)
			}
			const name = "history-startup"
			reg, _ := newChannelTestRegistry(t, home)
			id := addConversation(t, reg, "archived", true, true)
			if err := reg.Save(resolveConversationsRegistryPath(name)); err != nil {
				t.Fatal(err)
			}
			instance := resolveInstanceDirPath(name)
			store := history.New(instance)
			testStartupAppend(t, store, string(id), historyTurnOpened, "thinking", "", "", "", nil)
			stop := errors.New("stop after first producer composition")
			bin := stubBinary(t)
			args := []string{"-pyry-name", name, "-pyry-socket", filepath.Join(home, "daemon.sock"), "-pyry-workdir", home, "-pyry-codex", bin, "-pyry-claude", bin}
			err := runSupervisor(args, func(_ string, h channelDeliveryHistory, _ func(conversations.ConversationID, string), _ *slog.Logger) (*channelDelivery, error) {
				entries := historyEntries(t, store, string(id))
				if len(entries) != 3 || entries[1].Type != historyTurnInterrupted || entries[2].Type != historySessionDivider {
					t.Fatalf("producer ran before reconciliation: %+v", entries)
				}
				if _, err := h.AppendWithMetadata(id, protocol.TypeMessage, json.RawMessage(`{}`), time.Now(), history.Metadata{}); err != nil {
					t.Fatal(err)
				}
				return nil, stop
			})
			if !errors.Is(err, stop) {
				t.Fatal(err)
			}
			entries := historyEntries(t, store, string(id))
			for i, e := range entries {
				if e.ID != uint64(i+1) {
					t.Fatalf("durable order: %+v", entries)
				}
			}
			owner, err := net.Listen("unix", args[3])
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			err = runSupervisor(args)
			if !errors.Is(err, control.ErrInstanceRunning) {
				t.Fatal(err)
			}
			if got := historyEntries(t, store, string(id)); !reflect.DeepEqual(got, entries) {
				t.Fatal("non-owner changed history")
			}
		})
	}
}

func TestStartupHistoryReconciliation(t *testing.T) {
	t.Parallel()
	for _, ending := range []string{"", protocol.TypeTurnEnd, historyTurnInterrupted} {
		t.Run("ending="+ending, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID), CurrentSessionID: "wrong-current"})
			source := &history.SessionProvenance{Kind: "claude", SessionID: "old-source"}
			put := func(typ, turn, tool, name, parent string) {
				testStartupAppend(t, store, testConvID, typ, turn, tool, name, parent, source)
			}
			put(historyTurnOpened, "main", "", "", "")
			for _, tool := range []string{"open", "success", "failed", "denied", "interrupted"} {
				put(protocol.TypeToolUse, "main", tool, "Bash", "")
			}
			put(protocol.TypeToolResult, "main", "success", "", "")
			put(protocol.TypeToolResult, "main", "failed", "", "")
			failed, err := json.Marshal(protocol.ToolResultPayload{ConversationID: testConvID, TurnID: "main", ToolUseID: "failed", IsError: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendWithMetadata(conversations.ConversationID(testConvID), protocol.TypeToolResult, failed, time.Now(), history.Metadata{Session: source}); err != nil {
				t.Fatal(err)
			}
			put(protocol.TypeToolDenied, "main", "denied", "", "")
			put(historyToolInterrupted, "main", "interrupted", "", "")
			put(protocol.TypeToolUse, "main", "launcher", "Agent", "")
			put(protocol.TypeToolUse, "main", "task-launcher", "Task", "")
			put(protocol.TypeToolUse, "child", "child-call", "Bash", "launcher")
			put(protocol.TypeToolDenied, "child", "child-call", "", "")
			put(protocol.TypeAssistantDelta, "child", "", "", "launcher")
			put(protocol.TypeBackgroundTaskStarted, "background", "", "", "")
			if ending != "" {
				put(ending, "main", "", "", "")
				put(protocol.TypeAssistantDelta, "main", "", "", "")
			}
			put(historyTurnOpened, "thinking", "", "", "")
			before := historyEntries(t, store, testConvID)
			at := time.Unix(123, 456).UTC()
			reconcileStartupHistory(store, reg, discardLogger(), at)
			all := historyEntries(t, store, testConvID)
			if !reflect.DeepEqual(before, all[:len(before)]) {
				t.Fatal("historical prefix changed")
			}
			want := []string{historyToolInterrupted + ":main:open", historyTurnInterrupted + ":main:", historyTurnInterrupted + ":thinking:", historySessionDivider + "::"}
			if ending != "" {
				want = want[2:]
			}
			var got []string
			for _, e := range all[len(before):] {
				var p runtimeHistoryFact
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Fatal(err)
				}
				got = append(got, e.Type+":"+p.TurnID+":"+p.ToolCallID)
				if p.Cause != "daemon_restart" || !p.OccurredAt.Equal(at) || !e.TS.Equal(at) || e.Shown == nil || *e.Shown != (e.Type != historySessionDivider) {
					t.Fatalf("fact: %+v, %+v", e, p)
				}
				if e.Type != historySessionDivider && !reflect.DeepEqual(e.Session, source) {
					t.Fatalf("provenance: %+v", e.Session)
				}
				if legacyHistoryType(e.Type) {
					t.Fatalf("legacy eligibility: %s", e.Type)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			// Late output must not reopen the already interrupted identity.
			put(protocol.TypeToolUse, "thinking", "late", "Bash", "")
			n := len(historyEntries(t, store, testConvID))
			reconcileStartupHistory(history.New(dir), reg, discardLogger(), at.Add(time.Second))
			if tail := historyEntries(t, store, testConvID)[n:]; len(tail) != 1 || tail[0].Type != historySessionDivider {
				t.Fatalf("repeated start: %+v", tail)
			}
		})
	}
}
