package realclaude

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

const shadowHistoryFile = "thread_shadow_history.json"
const shadowExpectedFile = "thread_shadow_expected.json"

var shadowChecks = []string{"before_delivery", "main_complete", "after_delivery", "session_closed"}
var shadowPath = regexp.MustCompile(`/(?:Users|home|tmp|private|work|var|opt|root|etc|mnt|run|srv|Library|Applications|dev|usr|bin|proc|sys)/[^\s"'<>\\]*`)
var shadowSecret = regexp.MustCompile(`(?i)sk-ant-|sk-proj-|access_token|refresh_token|authorization|bearer |-----BEGIN .*PRIVATE KEY`)

type shadowCheck struct{ Executed, Failed, Skipped int }
type shadowProvenance struct {
	Schema                                                          int
	Capture, ClaudeVersion, DaemonCommit, HistorySHA256, GateReport string
	Checks                                                          map[string]shadowCheck
}
type shadowHistory struct {
	Provenance   shadowProvenance
	Conversation string
	Entries      []history.Entry
}
type shadowCheckpoint struct {
	Name    string
	Version uint64
	Items   []thread.Item
}
type shadowExpected struct {
	Provenance  shadowProvenance
	Legacy      []protocol.Envelope
	Checkpoints []shadowCheckpoint
}

func shadowReadPair(dir string) (h shadowHistory, e shadowExpected, err error) {
	for _, record := range []struct {
		name string
		dst  any
	}{{shadowHistoryFile, &h}, {shadowExpectedFile, &e}} {
		f, openErr := os.Open(filepath.Join(dir, record.name))
		if openErr != nil {
			return h, e, errors.New("shadow evidence pair missing")
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, 8*1024*1024+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(raw) > 8*1024*1024 || shadowDeny(raw) != nil {
			return h, e, errors.New("unsafe or unreadable shadow evidence")
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(record.dst) != nil || d.Decode(new(any)) != io.EOF {
			return h, e, errors.New("invalid shadow evidence schema")
		}
	}
	return h, e, nil
}
func shadowDigest(entries []history.Entry) string {
	raw, _ := json.Marshal(entries)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func shadowValidatePair(h shadowHistory, e shadowExpected, pinned bool) error {
	p := h.Provenance
	if !reflect.DeepEqual(p, e.Provenance) || p.Schema != 1 || p.Capture == "" || p.ClaudeVersion == "" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.DaemonCommit) || !conversations.ValidID(h.Conversation) || len(h.Entries) == 0 || p.HistorySHA256 != shadowDigest(h.Entries) {
		return errors.New("inconsistent shadow evidence provenance")
	}
	if pinned && !regexp.MustCompile(`^https://github\.com/pyrycode/pyrycode/(issues|pull)/[0-9]+#issuecomment-[0-9]+$`).MatchString(p.GateReport) {
		return errors.New("shadow evidence lacks counted dispatcher report pin")
	}
	if len(p.Checks) != len(shadowChecks) || len(e.Checkpoints) != len(shadowChecks) {
		return errors.New("incomplete shadow capture checks")
	}
	var previous uint64
	for i, name := range shadowChecks {
		if p.Checks[name] != (shadowCheck{Executed: 1}) || e.Checkpoints[i].Name != name || e.Checkpoints[i].Version <= previous || e.Checkpoints[i].Version > h.Entries[len(h.Entries)-1].ID || len(e.Checkpoints[i].Items) == 0 {
			return errors.New("missing failed skipped or unordered shadow checkpoint")
		}
		previous = e.Checkpoints[i].Version
	}
	for i, entry := range h.Entries {
		if entry.ID != uint64(i+1) {
			return errors.New("shadow raw history IDs are incomplete")
		}
	}
	return shadowWitness(h, e)
}

// shadowWitness checks observed markers and raw joins independently of Fold.Items.
func shadowWitness(h shadowHistory, e shadowExpected) error {
	var legacyText strings.Builder
	var agentCall string
	var childText strings.Builder
	tool, result, queued, delivered, closed := false, false, false, false, false
	for _, env := range e.Legacy {
		var p struct {
			ConversationID  string `json:"conversation_id"`
			Text            string `json:"text"`
			Name            string `json:"name"`
			ToolUseID       string `json:"tool_use_id"`
			ParentToolUseID string `json:"parent_tool_use_id"`
			Role            string `json:"role"`
		}
		if json.Unmarshal(env.Payload, &p) != nil {
			return errors.New("invalid legacy observation")
		}

		if p.ConversationID != h.Conversation {
			return errors.New("foreign legacy observation")
		}
		switch env.Type {
		case protocol.TypeAssistantDelta:
			if p.ParentToolUseID == "" {
				legacyText.WriteString(p.Text)
			} else if p.ParentToolUseID == agentCall {
				childText.WriteString(p.Text)
			}
		case protocol.TypeToolUse:
			if p.Name == "Bash" && p.ParentToolUseID == "" {
				tool = true
			}
			if (p.Name == "Agent" || p.Name == "Task") && p.ParentToolUseID == "" {
				agentCall = p.ToolUseID
			}
		case protocol.TypeToolResult:
			if bytes.Contains(env.Payload, []byte("SHADOW_TOOL")) {
				result = true
			}
		case protocol.TypeMessage:
			if p.Role == "user" && strings.Contains(p.Text, "SHADOW_QUEUED") {
				delivered = true
			}
		case protocol.TypeQueueState:
			if bytes.Contains(env.Payload, []byte("SHADOW_QUEUED")) {
				queued = true
			}
		case protocol.TypeSessionTransition:
			closed = true
		}
	}
	if !tool || !result || !queued || !delivered || !closed || agentCall == "" || !strings.Contains(childText.String(), "SHADOW_CHILD") || !strings.Contains(legacyText.String(), "SHADOW_BEFORE") || !strings.Contains(legacyText.String(), "SHADOW_AFTER") || !strings.Contains(legacyText.String(), "SHADOW_REPLY") {
		return errors.New("required legacy scenario absent")
	}
	var accepted, delivery, outcome, end, divider uint64
	var mainTurn string
	var rawMain, rawChild strings.Builder
	for _, entry := range h.Entries {
		var p struct {
			MessageID string `json:"message_id"`
			Accepted  uint64 `json:"accepted_entry_id"`
			Delivery  uint64 `json:"delivery_entry_id"`
			Parent    string `json:"parent_tool_use_id"`
			Turn      string `json:"turn_id"`
		}
		_ = json.Unmarshal(entry.Payload, &p)
		if entry.Type == protocol.TypeAssistantDelta {
			var delta protocol.AssistantDeltaPayload
			_ = json.Unmarshal(entry.Payload, &delta)
			if delta.ParentToolUseID == "" {
				rawMain.WriteString(delta.Text)
			} else if delta.ParentToolUseID == agentCall {
				rawChild.WriteString(delta.Text)
			}
		}
		if entry.Type == "send_accepted" && p.MessageID == "shadow-queued" {
			accepted = entry.ID
		}
		if entry.Type == "send_delivered" && p.Accepted == accepted && accepted != 0 {
			outcome = entry.ID
			delivery = p.Delivery
		}
		if entry.Type == protocol.TypeTurnEnd && p.Parent == "" && end == 0 {
			end = entry.ID
			mainTurn = p.Turn
		}
		if entry.Type == "session_divider" && entry.ID > outcome && outcome != 0 && divider == 0 {
			divider = entry.ID
		}
	}
	if rawMain.String() != legacyText.String() || rawChild.String() != childText.String() {
		return errors.New("raw history and observed legacy text disagree")
	}
	if end == 0 || h.Entries[end-1].Session == nil || accepted == 0 || delivery <= accepted || outcome <= delivery || end <= accepted || divider <= outcome || e.Checkpoints[0].Version != accepted || e.Checkpoints[1].Version != end || e.Checkpoints[2].Version != outcome || e.Checkpoints[3].Version != divider {
		return errors.New("raw scenario checkpoints or queue joins missing")
	}
	for _, cp := range e.Checkpoints {
		if err := shadowRawRows(h, cp); err != nil {
			return err
		}
		ids := map[uint64]thread.Item{}
		orders := map[uint64]bool{}
		foundQueue, foundChild, settled := false, false, false
		var foldedMain strings.Builder
		for _, item := range cp.Items {
			if item.ID == 0 || item.ID > cp.Version || item.Rev < item.ID || item.Rev > cp.Version || ids[item.ID].ID != 0 || item.Order > cp.Version || (item.Order == 0 && item.Status != "queued") || (item.Kind != "user_message" && item.Order != item.ID) || (item.Order != 0 && orders[item.Order]) {
				return errors.New("invalid item identity revision or placement")
			}
			source := h.Entries[item.ID-1]
			if item.ID == accepted && cp.Version >= outcome {
				source = h.Entries[delivery-1]
			}
			if source.Session != nil && source.Session.Kind != "none" && (item.Session != source.Session.SessionID || item.Agent != source.Session.Kind) {
				return errors.New("item source ownership changed")
			}
			if item.Parent != 0 {
				parent, ok := ids[item.Parent]
				if !ok || parent.ID >= item.ID || parent.Kind != "agent" || parent.Session != item.Session || parent.Agent != item.Agent {
					return errors.New("child parent or source ownership invalid")
				}
				var child protocol.AssistantDeltaPayload
				_ = json.Unmarshal(source.Payload, &child)
				var launch protocol.ToolUsePayload
				_ = json.Unmarshal(parent.Content, &launch)
				if item.Kind == "assistant_message" && child.ParentToolUseID != launch.ToolUseID {
					return errors.New("child attached to wrong recorded parent")
				}
				if bytes.Contains(item.Content, []byte("SHADOW_CHILD")) {
					foundChild = true
				}
			}
			if item.Kind == "assistant_message" && item.Parent == 0 {
				var p struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(item.Content, &p)
				foldedMain.WriteString(p.Text)
			}
			if item.ID == accepted {
				foundQueue = true
				wantStatus, wantOrder := "delivered", delivery
				if cp.Name == "before_delivery" || cp.Name == "main_complete" {
					wantStatus, wantOrder = "queued", 0
				}
				if item.Kind != "user_message" || item.Status != wantStatus || item.Order != wantOrder || !item.Shown {
					return errors.New("queued identity status or placement changed")
				}
			}
			if cp.Version >= end && item.Turn == mainTurn && item.Parent == 0 && item.Kind != "agent" && item.Active {
				return errors.New("main completion left active work")
			}
			if cp.Name == "session_closed" && item.Active && item.Session == h.Entries[end-1].Session.SessionID {
				return errors.New("session divider left active old-session work")
			}
			if cp.Name == "session_closed" && item.Status == "interrupted" && item.Rev > outcome && item.Rev < divider {
				settled = true
			}
			ids[item.ID] = item
			if item.Order != 0 {
				orders[item.Order] = true
			}
		}
		if cp.Name == "session_closed" && !settled {
			return errors.New("closure did not settle active work before divider")
		}
		if !foundQueue {
			return errors.New("accepted queue item missing")
		}
		if cp.Version >= end && (!foundChild || !strings.Contains(foldedMain.String(), "SHADOW_BEFORE") || !strings.Contains(foldedMain.String(), "SHADOW_AFTER")) {
			return errors.New("fold lost independently witnessed content")
		}
	}
	return nil
}

func shadowDeny(raw []byte) error {
	// Decode JSON first so escaped path/credential bytes cannot bypass the scan.
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return errors.New("invalid retained JSON")
	}
	decoded, _ := json.Marshal(value)
	if shadowPath.Match(decoded) || shadowSecret.Match(decoded) {
		return errors.New("retained bytes failed credential/path deny scan")
	}
	return nil
}
func shadowSanitize(raw []byte) ([]byte, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, errors.New("invalid capture JSON")
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return shadowPath.ReplaceAllStringFunc(x, func(string) string { return "$HOST_PATH" })
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			for k, v := range x {
				x[k] = walk(v)
			}
		}
		return v
	}
	return json.Marshal(walk(value))
}

// shadowRawRows derives text runs and ordinary tool outcomes from raw facts,
// without consulting a Fold or saved expected rows.
func shadowRawRows(h shadowHistory, cp shadowCheckpoint) error {
	type lane struct{ session, turn, parent string }
	open := map[lane]uint64{}
	texts := map[uint64]string{}
	results := map[string]history.Entry{}
	for _, entry := range h.Entries {
		if entry.ID > cp.Version {
			break
		}
		var p struct {
			Text   string `json:"text"`
			Turn   string `json:"turn_id"`
			Parent string `json:"parent_tool_use_id"`
			Call   string `json:"tool_use_id"`
		}
		if json.Unmarshal(entry.Payload, &p) != nil {
			return errors.New("invalid raw fact")
		}
		session := ""
		if entry.Session != nil {
			session = entry.Session.SessionID
		}
		key := lane{session, p.Turn, p.Parent}
		switch entry.Type {
		case protocol.TypeAssistantDelta:
			if open[key] == 0 {
				open[key] = entry.ID
			}
			texts[open[key]] += p.Text
		case protocol.TypeToolUse, protocol.TypeTurnEnd, "main_turn_interrupted":
			delete(open, key)
		case protocol.TypeMessage:
			for k := range open {
				if k.parent == "" {
					delete(open, k)
				}
			}
		case protocol.TypeToolResult:
			results[session+"/"+p.Turn+"/"+p.Call] = entry
		}
	}
	seen := map[uint64]bool{}
	for _, item := range cp.Items {
		if item.ID == 0 || item.ID > uint64(len(h.Entries)) || item.ID > cp.Version {
			return errors.New("row creating ID outside retained history")
		}
		source := h.Entries[item.ID-1]
		if item.Kind == "assistant_message" {
			var content struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(item.Content, &content)
			if texts[item.ID] == "" || content.Text != texts[item.ID] {
				return errors.New("text run differs from raw observations")
			}
			if item.Parent == 0 {
				active := false
				for key, id := range open {
					active = active || (key.parent == "" && id == item.ID)
				}
				status := "done"
				if active {
					status = "running"
				}
				if item.Status != status || item.Active != active {
					return errors.New("main text status differs from raw boundaries")
				}
			}
			seen[item.ID] = true
		}
		if item.Kind == "tool_call" || item.Kind == "agent" {
			seen[item.ID] = true
		}

		if item.Kind == "tool_call" {
			var call protocol.ToolUsePayload
			_ = json.Unmarshal(source.Payload, &call)
			var content map[string]json.RawMessage
			_ = json.Unmarshal(item.Content, &content)
			var saved protocol.ToolUsePayload
			_ = json.Unmarshal(item.Content, &saved)
			if source.Type != protocol.TypeToolUse || call.Name == "" || !reflect.DeepEqual(saved.Input, call.Input) {
				return errors.New("tool creation input lost")
			}
			if result, ok := results[item.Session+"/"+call.TurnID+"/"+call.ToolUseID]; ok {
				var payload protocol.ToolResultPayload
				_ = json.Unmarshal(result.Payload, &payload)
				status := "done"
				if payload.IsError {
					status = "failed"
				}
				var got, want any
				_ = json.Unmarshal(content["result"], &got)
				_ = json.Unmarshal(result.Payload, &want)
				if item.Status != status || item.Active || !reflect.DeepEqual(got, want) {
					return errors.New("tool outcome differs from raw result")
				}
			} else {
				status, active := "running", true
				if cp.Name == "session_closed" {
					status, active = "interrupted", false
				}
				if item.Status != status || item.Active != active {
					return errors.New("unfinished tool status differs from raw lifecycle")
				}
			}
		}
	}
	for _, entry := range h.Entries {
		if entry.ID <= cp.Version && entry.Type == protocol.TypeToolUse && !seen[entry.ID] {
			return errors.New("raw tool creation missing from thread")
		}
	}
	for id := range texts {
		if !seen[id] {
			return errors.New("raw text run missing from thread")
		}
	}
	return nil
}
