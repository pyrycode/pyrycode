//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// This published value is synthetic, never a requirement on observed wording.
const backgroundReplySyntheticText = "pyry-3589-finished"

// Every parser input is retained in order. Only known structural keys and a
// fixed vocabulary survive; all remaining strings are rig-authored substitutes.
// Persistent parser traces must agree at each source index before publication.
func backgroundReplyReplay(t *testing.T, lines []dropcapCaptured, parent, token string, final int, execution ...*backgroundReplyExecution) ([]string, []int, func(string) string, error) {
	t.Helper()
	aliases := map[string]string{}
	alias := func(s string) string {
		if s == "" {
			return s
		}
		if aliases[s] == "" {
			aliases[s] = fmt.Sprintf("rig-id-%d", len(aliases)+1)
		}
		return aliases[s]
	}
	// Assign aliases deterministically, independent of JSON map iteration order.
	alias(parent)
	alias("35890000-0000-4000-8000-000000000001")
	var witness *backgroundReplyExecution
	if len(execution) == 1 {
		witness = execution[0]
	}
	var scrub func(string, any, bool, bool) any
	scrub = func(key string, v any, trace, normalized bool) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				value := x[k]
				// The observed task patch's timestamp is metadata, not child
				// completion evidence. Keep its numeric type with a rig value.
				if k == "end_time" {
					if _, ok := value.(float64); ok {
						out[k] = float64(0)
						continue
					}
				}
				if trace || strings.Contains(" type subtype message id role content parent_tool_use_id session_id event index content_block delta text thinking signature input name tool_use_id is_error run_in_background command prompt description subagent_type partial_json task_id task_type status task_status tasks summary patch is_backgrounded tool_name elapsed_time_seconds terminal_reason error isReplay timeout model ", " "+k+" ") {
					out[k] = scrub(k, value, trace, normalized)
				}
			}
			// This synthetic command records the physical witness join, never the
			// observed shell spelling. Normalize ToolStart traces by the same ID.
			if witness != nil {
				tool := witness.Tool
				if normalized {
					tool = alias(tool)
				}
				inputKey := ""
				if x["id"] == tool && x["name"] == "Bash" {
					inputKey = "input"
				}
				if x["ToolCallID"] == tool {
					inputKey = "RawInput"
				}
				if input, ok := out[inputKey].(map[string]any); ok {
					if _, present := input["command"]; present {
						input["command"] = "rig-synthetic-operation:" + alias(witness.Operation)
					}
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, value := range x {
				out[i] = scrub(key, value, trace, normalized)
			}
			return out
		case string:
			if key == "command" {
				return "rig-synthetic-command"
			}
			if strings.HasSuffix(key, "ID") || strings.HasSuffix(key, "_id") || key == "id" {
				if normalized {
					return x
				}
				return alias(x)
			}
			if strings.TrimSpace(x) == token {
				return "rig-release-observed"
			}
			if strings.Contains(" assistant user result success stream_event message_start content_block_start content_block_delta content_block_stop message_delta message_stop text text_delta thinking thinking_delta signature_delta input_json_delta tool_use tool_result Agent Bash Task system task_started task_updated task_notification background_tasks_changed local_agent local_bash completed running failed stopped cancelled end_turn tool execute done pyry-3589-finished pyry-3589-launched rig-release-observed rig-synthetic-command ", " "+x+" ") && x != "" {
				return x
			}
			return strings.Map(func(r rune) rune {
				if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
					return r
				}
				return 'x'
			}, x)
		default:
			return v
		}
	}
	var original, replayed []string
	index := -1
	var originalCompletion *turnevent.TextChunk
	var traceErr error
	signature := func(ev turnevent.Event, normalized bool) string {
		if text, ok := ev.(turnevent.TextChunk); ok && index == final && text.TextComplete && strings.TrimSpace(text.Text) != "" {
			owner := parent
			if normalized {
				owner = alias(parent)
			}
			if owner != "" && text.ParentToolCallID == owner {
				// Only the selected intrinsic completion's prose is canonicalized.
				// Message identity, attribution and completion remain in the trace.
				text.Text = backgroundReplySyntheticText
				ev = text
			}
		}
		if end, ok := ev.(turnevent.TurnEnd); ok {
			// Accounting does not participate in text completion or ownership.
			ev = turnevent.TurnEnd{Reason: end.Reason, Outcome: end.Outcome, IsError: end.IsError, TerminalReason: end.TerminalReason, ErrorCategory: end.ErrorCategory}
		}
		// Inventories and diagnostics carry no thread ownership/text lifecycle.
		switch ev.(type) {
		case turnevent.TextChunk, turnevent.ThoughtChunk, turnevent.ToolStart, turnevent.ToolUpdate,
			turnevent.ToolProgress, turnevent.TurnEnd, turnevent.BackgroundTaskStarted,
			turnevent.BackgroundTaskUpdated:
		default:
			return ""
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal("encode parser trace")
		}
		var v any
		if json.Unmarshal(raw, &v) != nil {
			t.Fatal("decode parser trace")
		}
		if update, ok := ev.(turnevent.BackgroundTaskUpdated); ok {
			// Patch is JSON carried inside a string. Compare its decoded key/type
			// structure rather than masking serialized bytes: whitespace and key
			// order are irrelevant, but a dropped key must remain detectable even
			// when two serialized strings happen to have equal lengths.
			patch, err := backgroundReplyPatchTrace(update.Patch)
			if err != nil {
				traceErr = err
				return ""
			}
			v.(map[string]any)["Patch"] = patch
		}
		b, _ := json.Marshal(scrub("", v, true, normalized))
		return fmt.Sprintf("%T:%s", ev, b)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := streamsup.NewParser(func(ev turnevent.Event) {
		if text, ok := ev.(turnevent.TextChunk); ok && index == final && parent != "" && text.ParentToolCallID == parent && text.TextComplete && strings.TrimSpace(text.Text) != "" {
			originalCompletion = &text
		}
		if s := signature(ev, false); s != "" {
			original = append(original, s)
		}
	}, logger)
	complete := false
	q := streamsup.NewParser(func(ev turnevent.Event) {
		if s := signature(ev, true); s != "" {
			replayed = append(replayed, s)
		}
		if text, ok := ev.(turnevent.TextChunk); ok && text.ParentToolCallID == alias(parent) && text.Text == backgroundReplySyntheticText && text.TextComplete {
			complete = true
		}
	}, logger)
	var replay []string
	var indices []int
	for i, line := range lines {
		index = i
		if line.Index != i {
			return nil, nil, alias, fmt.Errorf("noncontiguous capture")
		}
		var v any
		if json.Unmarshal(line.Raw, &v) != nil {
			return nil, nil, alias, fmt.Errorf("undecodable captured line")
		}
		// Parse originals first so identity assignment follows production events.
		_, _ = p.Write(append(append([]byte(nil), line.Raw...), '\n'))
		if i == final {
			if originalCompletion == nil {
				return nil, nil, alias, fmt.Errorf("original sequence did not establish intrinsic child completion")
			}
			frame, _ := v.(map[string]any)
			message, _ := frame["message"].(map[string]any)
			content, _ := message["content"].([]any)
			if frame["type"] != "assistant" || frame["parent_tool_use_id"] != parent || message["id"] != originalCompletion.MessageID || message["role"] != "assistant" || len(content) != 1 {
				return nil, nil, alias, fmt.Errorf("selected source is not the completed assistant response")
			}
			block, _ := content[0].(map[string]any)
			if block["type"] != "text" || block["text"] != originalCompletion.Text {
				return nil, nil, alias, fmt.Errorf("selected source text does not match intrinsic completion")
			}
			block["text"] = backgroundReplySyntheticText
		}
		safe, err := json.Marshal(scrub("", v, false, false))
		if err != nil {
			return nil, nil, alias, fmt.Errorf("encode scrubbed sequence")
		}
		complete = false
		_, _ = q.Write(append(safe, '\n'))
		if traceErr != nil {
			return nil, nil, alias, fmt.Errorf("task patch trace at source index %d: %w", i, traceErr)
		}
		if !reflect.DeepEqual(original, replayed) {
			return nil, nil, alias, fmt.Errorf("scrubbing changed relevant parser events at source index %d", i)
		}
		replay = append(replay, string(safe))
		indices = append(indices, i)
		if i == final && !complete {
			return nil, nil, alias, fmt.Errorf("full captured sequence did not retain intrinsic child completion")
		}
	}
	if originalCompletion == nil || final < 0 || final >= len(lines) {
		return nil, nil, alias, fmt.Errorf("missing selected child completion")
	}
	return replay, indices, alias, nil
}

// Preserve absence and JSON type as well as object keys. Private patch values
// pass through the same scrubber as other trace fields and are never logged.
func backgroundReplyPatchTrace(patch string) (map[string]any, error) {
	if patch == "" {
		return map[string]any{"present": false}, nil
	}
	var value any
	if json.Unmarshal([]byte(patch), &value) != nil {
		return nil, fmt.Errorf("task patch is not valid JSON")
	}
	return map[string]any{"present": true, "value": value}, nil
}
