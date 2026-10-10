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
const shadowScenarioCheck = "scenario_evidence"

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
	if len(p.Checks) != len(shadowChecks)+1 || len(e.Checkpoints) != len(shadowChecks) {
		return errors.New("incomplete shadow capture checks")
	}
	if p.Checks[shadowScenarioCheck] != (shadowCheck{Executed: 1}) {
		return errors.New("missing failed skipped scenario evidence check")
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
	var legacyQueued, legacyDelivered string
	tool, result, queued, delivered, closed := false, false, false, false, false
	for _, env := range e.Legacy {
		var p struct {
			ConversationID  string `json:"conversation_id"`
			Text            string `json:"text"`
			Name            string `json:"name"`
			ToolUseID       string `json:"tool_use_id"`
			ParentToolUseID string `json:"parent_tool_use_id"`
			Role            string `json:"role"`
			MessageID       string `json:"message_id"`
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
			if p.Role == "user" && p.MessageID == "shadow-queued" && strings.Contains(p.Text, "SHADOW_QUEUED") {
				delivered = true
				legacyDelivered = p.Text
			}
		case protocol.TypeQueueState:
			var state protocol.QueueStatePayload
			if json.Unmarshal(env.Payload, &state) != nil {
				return errors.New("invalid legacy queue observation")
			}
			for _, msg := range state.Queued {
				if msg.MessageID == "shadow-queued" && strings.Contains(msg.Text, "SHADOW_QUEUED") {
					queued = true
					legacyQueued = msg.Text
				}
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
	var acceptance struct {
		Text     string `json:"text"`
		DeviceID string `json:"device_id"`
	}
	var message protocol.MessagePayload
	if json.Unmarshal(h.Entries[accepted-1].Payload, &acceptance) != nil || acceptance.DeviceID == "" || acceptance.Text != legacyQueued ||
		h.Entries[delivery-1].Type != protocol.TypeMessage || json.Unmarshal(h.Entries[delivery-1].Payload, &message) != nil ||
		message.Role != "user" || message.MessageID != "shadow-queued" || message.Text != legacyDelivered {
		return errors.New("raw acceptance or delivery differs from observed legacy user text")
	}
	for _, cp := range e.Checkpoints {
		if err := shadowRawRows(h, cp); err != nil {
			return fmt.Errorf("raw checkpoint version=%d: %w", cp.Version, err)
		}
		ids := map[uint64]thread.Item{}
		lifetimes := shadowLifetimes(h, cp.Version)
		orders := map[uint64]bool{}
		foundQueue, foundChild, settled, foundDivider := false, false, false, false
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
			if item.Kind == "agent" || item.Kind == "tool_call" || item.Kind == "assistant_message" {
				var recorded struct {
					Turn string `json:"turn_id"`
				}
				_ = json.Unmarshal(source.Payload, &recorded)
				if item.Turn != recorded.Turn {
					return errors.New("work turn differs from raw creation")
				}
			}
			if item.Kind == "agent" || item.Kind == "tool_call" || item.Kind == "assistant_message" {
				if err := shadowRawOwner(h, cp, item, ids, lifetimes); err != nil {
					return err
				}
			}
			if item.Parent != 0 {
				parent, ok := ids[item.Parent]
				if !ok || parent.ID >= item.ID || parent.Kind != "agent" || parent.Session != item.Session || parent.Agent != item.Agent {
					return errors.New("child parent or source ownership invalid")
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
			if item.ID == divider {
				foundDivider = true
				saved := h.Entries[divider-1]
				var boundary struct {
					Session string `json:"previous_session_id"`
					Agent   string `json:"previous_agent"`
				}
				_ = json.Unmarshal(saved.Payload, &boundary)
				noChild := false
				if saved.Session != nil {
					boundary.Session, boundary.Agent = saved.Session.SessionID, saved.Session.Kind
					noChild = saved.Session.Kind == "none"
				}
				if boundary.Agent != "claude" && boundary.Agent != "codex" {
					boundary.Agent = "unknown"
				}
				if item.Kind != "session_divider" || item.Order != divider || item.Rev != divider || item.Parent != 0 || item.Active || !item.Shown || item.NoChild != noChild || item.Session != boundary.Session || item.Agent != boundary.Agent || !shadowJSONEqual(item.Content, saved.Payload) {
					return errors.New("closure divider differs from raw fact")
				}
			}
			if cp.Version >= end && item.Turn == mainTurn && item.Parent == 0 && item.Kind != "agent" && item.Active {
				return errors.New("main completion left active work")
			}
			if cp.Name == "session_closed" && item.Active && item.Session == h.Entries[end-1].Session.SessionID {
				return errors.New("session divider left active old-session work")
			}
			// Closure work created after delivery must have been cut short before
			// the divider. The daemon's own interruption facts give "interrupted";
			// a live reset first lets Claude cancel the call, which records an
			// error result and so "failed" (observed 2026-10-10, Claude 2.1.280).
			// shadowRawRows has already matched that status to the first raw
			// terminal, so a call that ran to success cannot satisfy this.
			work := item.Kind == "tool_call" || item.Kind == "agent"
			cut := item.Status == "interrupted" || (work && item.ID > outcome && item.Status != "done" && item.Status != "finished" && item.Status != "running" && item.Status != "stopping")
			if cp.Name == "session_closed" && cut && !item.Active && item.Rev > outcome && item.Rev < divider {
				settled = true
			}
			ids[item.ID] = item
			if item.Order != 0 {
				orders[item.Order] = true
			}
		}
		if cp.Name == "session_closed" && (!settled || !foundDivider) {
			return errors.New("closure did not retain divider and settle active work before it")
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

// shadowRawRows derives user content, text runs and work outcomes from raw facts,
// without consulting a Fold or saved expected rows.
func shadowRawRows(h shadowHistory, cp shadowCheckpoint) error {
	type lane struct{ session, turn, parent string }
	type toolKey struct {
		lane
		call string
	}
	type terminal struct {
		entry         history.Entry
		field, status string
	}
	open := map[lane]uint64{}
	texts := map[uint64]string{}
	textEndings := map[uint64]history.Entry{}
	calls := map[toolKey]bool{}
	terminals := map[toolKey]terminal{}
	sendOutcomes := map[uint64]history.Entry{}
	for _, entry := range h.Entries {
		if entry.ID > cp.Version {
			break
		}
		var p struct {
			Text            string `json:"text"`
			Turn            string `json:"turn_id"`
			Parent          string `json:"parent_tool_use_id"`
			Call            string `json:"tool_use_id"`
			InterruptedCall string `json:"tool_call_id"`
			IsError         bool   `json:"is_error"`
			Accepted        uint64 `json:"accepted_entry_id"`
		}
		if json.Unmarshal(entry.Payload, &p) != nil {
			return errors.New("invalid raw fact")
		}
		session := ""
		if entry.Session != nil {
			session = entry.Session.SessionID
		}
		key := lane{session, p.Turn, p.Parent}
		call := toolKey{key, p.Call}
		switch entry.Type {
		case "send_delivered":
			if sendOutcomes[p.Accepted].ID == 0 {
				sendOutcomes[p.Accepted] = entry
			}
		case protocol.TypeAssistantDelta:
			if open[key] == 0 {
				open[key] = entry.ID
			}
			texts[open[key]] += p.Text
		case protocol.TypeToolUse:
			calls[call] = true
			delete(open, key)
		case protocol.TypeTurnEnd, "main_turn_interrupted":
			if key.parent != "" && open[key] != 0 {
				textEndings[open[key]] = entry
			}
			delete(open, key)
			for call := range calls {
				if call.lane == key && terminals[call].entry.ID == 0 {
					terminals[call] = terminal{entry, "ending", "interrupted"}
				}
			}
		case protocol.TypeMessage:
			for k := range open {
				if k.parent == "" {
					delete(open, k)
				}
			}
		case protocol.TypeToolResult:
			if terminals[call].entry.ID == 0 {
				status := "done"
				if p.IsError {
					status = "failed"
				}
				terminals[call] = terminal{entry, "result", status}
			}
		case protocol.TypeToolDenied:
			if terminals[call].entry.ID == 0 {
				terminals[call] = terminal{entry, "denial", "denied"}
			}
		case "main_tool_interrupted":
			call.call = p.InterruptedCall
			if terminals[call].entry.ID == 0 {
				terminals[call] = terminal{entry, "interruption", "interrupted"}
			}
		}
	}
	seen := map[uint64]bool{}
	parents := map[uint64]thread.Item{}
	for _, item := range cp.Items {
		if item.ID == 0 || item.ID > uint64(len(h.Entries)) || item.ID > cp.Version {
			return errors.New("row creating ID outside retained history")
		}
		source := h.Entries[item.ID-1]
		if item.Kind == "user_message" {
			var want, saved map[string]any
			if json.Unmarshal(source.Payload, &want) != nil || want == nil || (source.Type != "send_accepted" && source.Type != protocol.TypeMessage) {
				return errors.New("user row lacks raw acceptance or message")
			}
			if source.Type == "send_accepted" {
				if outcome, ok := sendOutcomes[item.ID]; ok {
					var link struct {
						Delivery uint64 `json:"delivery_entry_id"`
					}
					if json.Unmarshal(outcome.Payload, &link) != nil || link.Delivery <= item.ID || link.Delivery >= outcome.ID {
						return errors.New("invalid raw user delivery link")
					}
					message := h.Entries[link.Delivery-1]
					var delivered, terminal map[string]any
					if message.Type != protocol.TypeMessage || json.Unmarshal(message.Payload, &delivered) != nil || delivered["role"] != "user" || json.Unmarshal(outcome.Payload, &terminal) != nil {
						return errors.New("invalid raw user delivery payload")
					}
					// Receiving content comes from the message; sender identity and
					// times remain those recorded at acceptance.
					for _, field := range []string{"device_id", "message_id", "accepted_at", "client_sent_at"} {
						if value, present := want[field]; present {
							delivered[field] = value
						}
					}
					delivered["outcome"] = terminal
					want = delivered
				}
			}
			if json.Unmarshal(item.Content, &saved) != nil || !reflect.DeepEqual(saved, want) {
				return errors.New("user content or sender identity differs from raw facts")
			}
		}
		if item.Kind == "assistant_message" {
			var content struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(item.Content, &content)
			if texts[item.ID] == "" || content.Text != texts[item.ID] {
				return errors.New("text run differs from raw observations")
			}
			active := false
			for _, id := range open {
				active = active || id == item.ID
			}
			status := "done"
			if active {
				status = "running"
				if ending := shadowParentEnding(parents[item.Parent]); ending != nil {
					status, active = "interrupted", false
					var content map[string]json.RawMessage
					_ = json.Unmarshal(item.Content, &content)
					if !shadowJSONEqual(content["parent_ending"], ending) {
						return errors.New("child text lost independently validated parent ending")
					}
				}
			}
			if ending, ok := textEndings[item.ID]; ok {
				var content map[string]json.RawMessage
				_ = json.Unmarshal(item.Content, &content)
				if !shadowJSONEqual(content["ending"], ending.Payload) {
					return errors.New("child text lost raw lane ending")
				}
			}
			if item.Status != status || item.Active != active {
				return errors.New("text status differs from raw boundaries or parent final")
			}
			seen[item.ID] = true
		}
		if item.Kind == "tool_call" || item.Kind == "agent" {
			seen[item.ID] = true
		}

		if item.Kind == "tool_call" || item.Kind == "agent" {
			var call protocol.ToolUsePayload
			_ = json.Unmarshal(source.Payload, &call)
			var content map[string]json.RawMessage
			_ = json.Unmarshal(item.Content, &content)
			var launch map[string]json.RawMessage
			_ = json.Unmarshal(source.Payload, &launch)
			var saved protocol.ToolUsePayload
			_ = json.Unmarshal(item.Content, &saved)
			if source.Type != protocol.TypeToolUse || call.Name == "" || !reflect.DeepEqual(saved, call) {
				return errors.New("work row lacks raw creation")
			}
			for field, raw := range launch {
				if !shadowJSONEqual(content[field], raw) {
					return errors.New("work creation fields differ from raw launch")
				}
			}
			kind := "tool_call"
			if (call.Name == "Agent" || call.Name == "Task") && (source.Session == nil || source.Session.Kind == "claude") {
				kind = "agent"
			}
			if item.Kind != kind {
				return errors.New("work kind differs from raw launch")
			}
			if item.Kind == "agent" {
				if err := shadowRawAgent(h, cp, item, source, call, parents[item.Parent]); err != nil {
					return err
				}
				parents[item.ID] = item
				continue
			}
			if linked, err := shadowRawShell(h, cp, item, source, call, shadowParentEnding(parents[item.Parent])); err != nil {
				return err
			} else if linked {
				continue
			}
			key := toolKey{lane{item.Session, call.TurnID, call.ParentToolUseID}, call.ToolUseID}
			if outcome, ok := terminals[key]; ok {
				var got, want any
				_ = json.Unmarshal(content[outcome.field], &got)
				_ = json.Unmarshal(outcome.entry.Payload, &want)
				if item.Status != outcome.status || item.Active || !reflect.DeepEqual(got, want) {
					return fmt.Errorf("tool outcome differs from first raw terminal: version=%d creation=%d terminal=%d status_match=%t inactive=%t content_match=%t", cp.Version, item.ID, outcome.entry.ID, item.Status == outcome.status, !item.Active, reflect.DeepEqual(got, want))
				}
			} else {
				status, active := "running", true
				if ending := shadowParentEnding(parents[item.Parent]); ending != nil {
					status, active = "interrupted", false
					if !shadowJSONEqual(content["parent_ending"], ending) {
						return errors.New("child tool lost independently validated parent ending")
					}
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
