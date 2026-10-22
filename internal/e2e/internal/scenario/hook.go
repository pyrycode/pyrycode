package scenario

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	oslcapBlockMarker  = "PYRY-2255-BLOCKME"
	oslcapBlockReason  = "blocked by the pyry 2255 capture rig: this prompt carries the rig marker"
	oslcapHookBlocked  = "blocked"
	oslcapHookPassed   = "passed"
	oslcapSettingsName = "settings.json"
	oslcapHookName     = "hook.sh"
	oslcapWitnessName  = "hook-witness.log"
	HookRigDir         = "hookbanner-rig-2320"
	HookBlockPrompt    = "Reply with the single word ACK and nothing else. Do not use any tools. PYRY-2255-BLOCKME run=tape-hook"
	HookLivePrompt     = "Reply with the single word OK and nothing else. Do not use any tools. run=tape-hook"
)

type HookRig = oslcapRig

func WriteHookRig(dir string) (HookRig, error) { return oslcapWriteHookRig(dir) }
func HookVerdicts(path string) []string        { return oslcapHookVerdicts(path) }

type oslcapRig struct {
	Dir          string
	SettingsPath string
	ScriptPath   string
	WitnessPath  string
	Script       string
	Settings     string
}

// oslcapShellQuote wraps s in single quotes, refusing any value that carries one.
//
// POSIX single quotes are the only quoting that makes a value fully literal, and
// they have no escape: a value carrying a quote closes the quoting and turns the
// remainder into shell source. There is no sanitising fallback here on purpose —
// every value this file quotes is either a compile-time constant or a path Go minted,
// so a refusal is a bug in the caller, not an input to be repaired.
func oslcapShellQuote(s string) (string, error) {
	if strings.Contains(s, "'") {
		return "", fmt.Errorf("oslcap: %d-byte value carries a single quote and cannot be safely "+
			"interpolated into shell source; the value is deliberately not printed", len(s))
	}
	return "'" + s + "'", nil
}

// oslcapHookScript renders the UserPromptSubmit hook.
//
// A hook is ARBITRARY CODE EXECUTION AS THE OPERATOR by design — writeMCPSettings'
// doc makes exactly that point about a file pyry hands claude as --settings — so the
// one place this file builds shell source enforces its own quoting rather than
// resting on today's inputs happening to be metacharacter-free.
//
// It writes one fixed word per invocation and nothing else. The payload claude hands
// it on stdin carries the prompt, the cwd and the session id, and the record needs
// none of that: the question is whether the hook ran and whether it blocked. It also
// reads no environment variable, so the credential the child inherits has no path
// into the witness file.
//
// Exit 2 is the blocking code for UserPromptSubmit and stderr is the reason. Any
// other non-zero code is non-blocking, so the prompt would run normally and the
// trigger would silently not fire — which is what
// TestOslcapHookScriptBlocksOnlyTheMarkedPrompt pins.
func oslcapHookScript(witnessPath, marker, reason string) (string, error) {
	witness, err := oslcapShellQuote(witnessPath)
	if err != nil {
		return "", fmt.Errorf("hook witness path: %w", err)
	}
	quotedMarker, err := oslcapShellQuote(marker)
	if err != nil {
		return "", fmt.Errorf("hook marker: %w", err)
	}
	quotedReason, err := oslcapShellQuote(reason)
	if err != nil {
		return "", fmt.Errorf("hook reason: %w", err)
	}
	return "#!/bin/sh\n" +
		"# Rig-authored by pyry's #2255 capture. Blocks ONLY a prompt carrying the rig\n" +
		"# marker, so later turns on the same child still run. Records one word per\n" +
		"# invocation and never the payload.\n" +
		"payload=$(cat)\n" +
		"case \"$payload\" in\n" +
		"  *" + quotedMarker + "*)\n" +
		"    printf '%s\\n' " + oslcapHookBlockedLiteral + " >> " + witness + "\n" +
		"    printf '%s\\n' " + quotedReason + " >&2\n" +
		"    exit 2\n" +
		"    ;;\n" +
		"esac\n" +
		"printf '%s\\n' " + oslcapHookPassedLiteral + " >> " + witness + "\n" +
		"exit 0\n", nil
}

// The two verdict words as shell literals. Constants rather than a call to
// oslcapShellQuote, because they are this file's own and quoting them at runtime
// would add an error path that can never be taken.
const (
	oslcapHookBlockedLiteral = "'" + oslcapHookBlocked + "'"
	oslcapHookPassedLiteral  = "'" + oslcapHookPassed + "'"
)

// oslcapSettingsJSON renders the settings file: one UserPromptSubmit command hook,
// and nothing else.
//
// Deliberately NOT mcpSettingsFile's payload. That file exists to suppress two
// startup dialogs on the PTY path; this is the stream-json path, where the sibling
// captures spawn with no settings at all and never see a dialog. Adding a hook and
// changing no posture is what keeps this capture's spawn comparable to theirs.
//
// The marshal error is structurally unreachable — every field is a string or a slice
// of them — and returning an empty object rather than swallowing it makes an
// impossible failure loud at TestOslcapSettingsFileIsTheShapeClaudeReads instead of
// silent at a live gate.
func oslcapSettingsJSON(scriptPath string) string {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type matcher struct {
		Hooks []hook `json:"hooks"`
	}
	type settings struct {
		Hooks map[string][]matcher `json:"hooks"`
	}
	blob, err := json.MarshalIndent(settings{
		Hooks: map[string][]matcher{
			"UserPromptSubmit": {{Hooks: []hook{{Type: "command", Command: scriptPath}}}},
		},
	}, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(blob) + "\n"
}

// oslcapWriteHookRig creates the rig directory and its three files.
//
// Modes are set with an explicit Chmod after each write rather than left to
// os.WriteFile's perm argument alone, which is masked by the process umask: the
// settings file is executable policy claude reads, and a mode that depends on the
// runner's umask is a mode no test can pin.
//
// The witness is created here, empty, rather than by the script's first append, for
// the same reason — an append-created file takes the CHILD's umask.
func oslcapWriteHookRig(dir string) (oslcapRig, error) {
	rig := oslcapRig{
		Dir:          dir,
		SettingsPath: filepath.Join(dir, oslcapSettingsName),
		ScriptPath:   filepath.Join(dir, oslcapHookName),
		WitnessPath:  filepath.Join(dir, oslcapWitnessName),
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return rig, fmt.Errorf("create the rig dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return rig, fmt.Errorf("chmod the rig dir: %w", err)
	}

	script, err := oslcapHookScript(rig.WitnessPath, oslcapBlockMarker, oslcapBlockReason)
	if err != nil {
		return rig, err
	}
	rig.Script = script
	rig.Settings = oslcapSettingsJSON(rig.ScriptPath)

	for _, f := range []struct {
		path    string
		content string
		mode    os.FileMode
	}{
		{rig.ScriptPath, rig.Script, 0o700},
		{rig.SettingsPath, rig.Settings, 0o600},
		{rig.WitnessPath, "", 0o600},
	} {
		if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
			return rig, fmt.Errorf("write %s: %w", filepath.Base(f.path), err)
		}
		if err := os.Chmod(f.path, f.mode); err != nil {
			return rig, fmt.Errorf("chmod %s: %w", filepath.Base(f.path), err)
		}
	}
	return rig, nil
}

// oslcapHookVerdicts reads the witness back, in order. A missing or empty file means
// the hook never ran, which is a measurement and not an error: it is precisely the
// "the trigger never fired" case the whole record is built to separate from "claude
// sent nothing".
func oslcapHookVerdicts(witnessPath string) []string {
	blob, err := os.ReadFile(witnessPath)
	if err != nil {
		return nil
	}
	return strings.Fields(string(blob))
}

func InstallHookUserSettings(home string, rig oslcapRig) error {
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the user settings dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod the user settings dir: %w", err)
	}
	path := filepath.Join(dir, "settings.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; this harness has grown a second writer of claude's user "+
				"settings and the hook rig would be competing with it", path)
		}
		return fmt.Errorf("create the user settings file: %w", err)
	}
	if _, err := f.WriteString(rig.Settings); err != nil {
		_ = f.Close()
		return fmt.Errorf("write the user settings file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close the user settings file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod the user settings file: %w", err)
	}
	return nil
}

func hookBannerIsRefusal(p protocol.BannerPayload, convID string) bool {
	return p.ConversationID == convID && bytes.Contains([]byte(p.Text), []byte(oslcapBlockReason))
}

// CheckHookBannerWindow verifies saved refusal/opening facts and live ACK/state
// ordering. Content and compatibility receipts must never reach the transport.
func CheckHookBannerWindow(convID string, liveReqID uint64, entries []history.Entry, frames []protocol.Envelope) error {
	raw := make(map[uint64]history.Entry, len(entries))
	ack, firstRunning := -1, -1
	var openingTurn, assistantTurn string
	var refusalEntry, openingEntry history.Entry
	for _, entry := range entries {
		raw[entry.ID] = entry
		if entry.Type == protocol.TypeBanner {
			var p protocol.BannerPayload
			if json.Unmarshal(entry.Payload, &p) == nil && hookBannerIsRefusal(p, convID) {
				if refusalEntry.ID != 0 || p.Truncated {
					return fmt.Errorf("duplicate or truncated refusal source")
				}
				refusalEntry = entry
			}
		}
		if entry.Type == "main_turn_opened" {
			var p struct {
				Conversation string    `json:"conversation_id"`
				Turn         string    `json:"turn_id"`
				At           time.Time `json:"occurred_at"`
			}
			if json.Unmarshal(entry.Payload, &p) == nil && p.Conversation == convID {
				if openingEntry.ID != 0 || p.Turn == "" || !p.At.Equal(entry.TS) {
					return fmt.Errorf("invalid opening source")
				}
				openingEntry, openingTurn = entry, p.Turn
			}
		}
	}
	for i, env := range frames {
		switch env.Type {
		case protocol.TypeError, protocol.TypeUnrecognizedMessage, protocol.TypeBanner, protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd:
			return fmt.Errorf("unexpected content envelope %q", env.Type)
		case protocol.TypeAck:
			if env.InReplyTo != nil && *env.InReplyTo == liveReqID {
				ack = i
			}
		case protocol.TypeTurnState:
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				return fmt.Errorf("invalid lifecycle frame")
			}
			if p.ConversationID == convID {
				if ack < 0 {
					return fmt.Errorf("phase preceded liveness ack")
				}
				if p.State != "idle" && firstRunning < 0 {
					firstRunning = i
				}
			}
		}
	}
	for _, e := range entries {
		if e.Type != protocol.TypeAssistantDelta || e.ID <= openingEntry.ID {
			continue
		}
		var p protocol.AssistantDeltaPayload
		if json.Unmarshal(e.Payload, &p) != nil {
			return fmt.Errorf("invalid hook assistant source")
		}
		if p.ConversationID == convID && p.ParentToolUseID == "" && strings.TrimSpace(p.Text) != "" && !e.TS.Before(openingEntry.TS) {
			if e.Session == nil || openingEntry.Session == nil || *e.Session != *openingEntry.Session {
				return fmt.Errorf("hook source changed producing session")
			}
			assistantTurn = p.TurnID
			break
		}
	}

	if ack < 0 || firstRunning <= ack || refusalEntry.ID == 0 || openingTurn == "" || openingTurn != assistantTurn || openingEntry.ID <= refusalEntry.ID || openingEntry.TS.Before(refusalEntry.TS) {
		return fmt.Errorf("opening does not identify the post-refusal assistant turn")
	}
	ordered := append([]history.Entry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	fold := thread.New(convID)
	if err := fold.Feed(ordered); err != nil {
		return fmt.Errorf("fold queued hook sends: %w", err)
	}
	for _, messageID := range []string{"m-blocked", "m-live"} {
		found := false
		for _, item := range fold.Items() {
			var content struct {
				MessageID string `json:"message_id"`
			}
			if item.Kind != "user_message" || json.Unmarshal(item.Content, &content) != nil || content.MessageID != messageID {
				continue
			}
			if found || item.Status != "delivered" || item.Active || !item.Shown || item.Order <= item.ID || raw[item.ID].Type != "send_accepted" {
				return fmt.Errorf("invalid canonical queued delivery for %q", messageID)
			}
			found = true
		}
		if !found {
			return fmt.Errorf("missing canonical queued delivery for %q", messageID)
		}
	}
	return nil
}

type HookBannerConfig struct {
	Home, ConversationID, BlockPrompt, LivePrompt string
	TurnBudget, SettleBudget                      time.Duration
	CheckWitness                                  func(bool)
}

// HookBanner retains the live no-read absence window. The next send's exact ACK
// separates the refused source from the subsequent application turn.
func HookBanner(t *testing.T, w Wire, c HookBannerConfig) {
	t.Helper()
	var frames []protocol.Envelope
	next := func(deadline time.Time) protocol.Envelope {
		env := w.Next(deadline)
		if env.Type == protocol.TypeError || env.Type == protocol.TypeUnrecognizedMessage {
			t.Fatalf("unexpected hook-window envelope %q", env.Type)
		}
		if env.Type != "" {
			frames = append(frames, env)
		}
		return env
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "m-blocked", Text: c.BlockPrompt})
	deadline := time.Now().Add(c.TurnBudget)
	refused := false
	for !refused && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		for _, entry := range w.Source(c.ConversationID) {
			if entry.Type != protocol.TypeBanner {
				continue
			}
			var p protocol.BannerPayload
			if json.Unmarshal(entry.Payload, &p) != nil {
				t.Fatal("invalid saved hook banner")
			}
			if hookBannerIsRefusal(p, c.ConversationID) {
				if p.Truncated {
					t.Fatal("hook refusal truncated")
				}
				refused = true
			}
		}
	}
	if !refused {
		t.Fatal("hook refusal banner missing")
	}
	c.CheckWitness(false)
	time.Sleep(c.SettleBudget)
	liveID := w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "m-live", Text: c.LivePrompt})

	ack := false
	var turn sourceTurn
	checked := func(d time.Time) protocol.Envelope {
		env := next(d)
		turn.feed(t, c.ConversationID, w.Source(c.ConversationID))
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == liveID {
			ack = true
		}
		if env.Type == protocol.TypeTurnState {
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid hook live phase")
			}
			if p.ConversationID == c.ConversationID && !ack {
				t.Fatal("hook turn phase preceded correlated ack")
			}
		}
		return env
	}
	CompletedTurn(t, checked, c.ConversationID, c.TurnBudget, false, w.Source)
	if !ack {
		t.Fatal("hook liveness has no correlated acknowledgement")
	}

	c.CheckWitness(true)
	store := history.New(filepath.Join(c.Home, ".pyry", "test"))
	var entries []history.Entry
	cursor := ""
	for {
		page, err := store.Page(conversations.ConversationID(c.ConversationID), cursor, 128)
		if err != nil {
			t.Fatal("read hook-window raw history")
		}
		entries = append(entries, page.Entries...)
		if page.AtStart {
			break
		}
		cursor = page.Cursor
	}
	if err := CheckHookBannerWindow(c.ConversationID, liveID, entries, frames); err != nil {
		t.Fatal(err)
	}
}
