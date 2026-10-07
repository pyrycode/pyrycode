package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// writeStreamResponse writes fakeclaude's canned reply to one user turn: one
// assistant text line carrying text (the echoed prompt), then one
// result{subtype:"success"} line. Both are json.Marshal-encoded from local structs
// (never string-concatenated) so text — caller-controlled bytes — is escaped and
// each object is exactly one physical line regardless of prompt content. Returns
// the first marshal/write error.
//
// modelUsage rides onto the result line when non-nil (#2107's rider). nil is the
// default and emits no key at all, so the line stays byte-identical to the one
// every existing spec already asserts against.
func writeStreamResponse(w io.Writer, msgID, text string, modelUsage map[string]outModelUsage) error {
	if err := writeAssistantEcho(w, msgID, text); err != nil {
		return err
	}
	return writeJSONLine(w, outResult{
		Type:       "result",
		Subtype:    "success",
		SessionID:  streamSessionID,
		ModelUsage: modelUsage,
	})
}

// writeAssistantEcho writes the single assistant text line echoing text (the
// prompt) as message msgID — the assistant half of writeStreamResponse. It is split
// out so the interrupt mode (#1136) can emit the assistant line WITHOUT the trailing
// result, keeping the turn in flight; the line is byte-identical to the assistant
// line writeStreamResponse emits, so the default path is unchanged.
func writeAssistantEcho(w io.Writer, msgID, text string) error {
	return writeJSONLine(w, outAssistant{
		Type: "assistant",
		Message: outAsstMessage{
			ID:      msgID,
			Role:    "assistant",
			Content: []outTextBlock{{Type: "text", Text: text}},
		},
	})
}

// writeBogusLines writes the two shapes the daemon's stream parser has no
// mapping for: one top-level line of an invented type, and one assistant message
// whose single content block is of an invented type. They exercise the two drop
// sites that used to vanish into a debug log the production daemon never prints,
// so an e2e can assert both now reach a client as unrecognized_message frames.
//
// The needles are deliberately distinctive strings so the assertion cannot pass
// on some other frame's content.
func writeBogusLines(w io.Writer, msgID string) error {
	if err := writeJSONLine(w, map[string]any{
		"type":   bogusLineType,
		"detail": bogusLineNeedle,
	}); err != nil {
		return err
	}
	return writeJSONLine(w, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"id":   msgID + "-bogus",
			"role": "assistant",
			"content": []any{map[string]any{
				"type":   bogusBlockType,
				"detail": bogusBlockNeedle,
			}},
		},
	})
}

// The invented type names and needles the bogus rider emits. Exported-in-spirit
// constants rather than inline literals so the e2e asserts against the same
// strings the fake writes.
const (
	bogusLineType    = "fake_future_event"
	bogusLineNeedle  = "bogus-line-needle"
	bogusBlockType   = "fake_future_block"
	bogusBlockNeedle = "bogus-block-needle"
)

// writeRateLimitEvent writes one top-level rate_limit_event line carrying the
// captured rate_limit_info object with `status` substituted for the caller's value.
// It is the fake half of #1411's two-tier proof: the daemon's gate reads status and
// nothing else, so one writer driven at two statuses puts the benign case and its
// arrival control on one path, differing in exactly that string.
//
// The object is transcribed from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json, claude 2.1.220) —
// ALL SIX keys, not just the three streamsup.rateLimitInfo declares. Carrying the
// three overage keys costs nothing and makes "match the capture" literally true,
// while also feeding the parser keys its decode target deliberately omits.
//
// uuid and session_id are the capture's ENVELOPE identifiers, templated there
// ($SESSION_ID) rather than values to copy: the fake supplies its own. Neither is in
// the parser's decode target, so neither can reach the gate.
//
// A map[string]any like writeBogusLines: keys marshal sorted, so the line is
// deterministic without declaring a struct for a shape nothing else reads. Returns
// the first marshal/write error.
func writeRateLimitEvent(w io.Writer, status string) error {
	return writeJSONLine(w, map[string]any{
		"type": "rate_limit_event",
		"rate_limit_info": map[string]any{
			"status":                status,
			"resetsAt":              rateLimitResetsAt,
			"rateLimitType":         rateLimitLimitType,
			"overageStatus":         "rejected",
			"overageDisabledReason": "org_level_disabled",
			"isUsingOverage":        false,
		},
		"uuid":       rateLimitUUID,
		"session_id": streamSessionID,
	})
}

// The captured rate_limit_info values the rate-limit rider writes verbatim, plus the
// synthetic uuid it stamps in place of the capture's. Constants rather than inline
// literals for the bogus needles' reason: the e2e asserts against the same values the
// fake writes, across a main-package boundary it cannot import.
const (
	rateLimitResetsAt  = int64(1785699000)
	rateLimitLimitType = "five_hour"
	rateLimitUUID      = "44444444-4444-4444-8444-444444444444"
)

// writeSystemInitLine writes one top-level system/init line — the line claude opens
// every turn with, and the one this file wrote no form of before #2315. It is the fake
// half of that ticket's proof: the daemon turns this single line into a session_facts
// frame on a connected client, and nothing hermetic drove that path before.
//
// PROVENANCE: transcribed from the committed capture
// (internal/e2e/realclaude/testdata/effort_init_v2.1.259_sonnet_effort.json, claude
// 2.1.259, whose three init lines agree on every key here). ALL TWENTY-FOUR top-level
// keys, and every scalar value is the capture's verbatim. Three open-ended arrays —
// skills, slash_commands, tools — carry the capture's first entries rather than its
// full lists: nothing between here and the wire decodes any of them, the key's
// PRESENCE is the whole of what this fixture owes them, and ninety lines of tool names
// would bury the four keys that actually matter.
//
// THE FULL KEY SET IS THE POINT, not padding, and interruptMarkerLine's doc states the
// argument this inherits: a minimal three-key line would turn a presence among
// twenty-four keys into a presence among three. Twenty-one of these keys are absent
// from the daemon's decode target (streamsup's systemInitLine, which declares model,
// claude_code_version and permissionMode and nothing else), so feeding them is what
// shows the frame CANNOT carry one — four of them name the operator's filesystem (cwd,
// memory_paths, messaging_socket_path) or claude's own session identity (session_id),
// and those four are exactly what SessionFactsPayload's doc promises are absent.
//
// The identity values are SUBSTITUTED, shape-preserving, as writeRateLimitEvent
// substitutes: the capture templates cwd, memory_paths, messaging_socket_path and
// session_id as $WORKDIR / $TEMP_HOME / $MESSAGING_SOCKET / $SESSION_ID, which are
// placeholders rather than values to copy, and its uuid names a real session. None is
// in the parser's decode target, so none can reach a frame either way. The synthetic
// replacements are obviously synthetic so no reader mistakes one for a measured path.
//
// mcp_servers is carried verbatim because #2275 publishes a conversation's MCP server
// status from this same line and can populate its payload from this fixture rather
// than rebuilding it.
//
// permissionMode is claude's own camelCase spelling, deliberately: it is the key as it
// appears on the line, and the daemon's snake_case permission_mode appears only where
// the daemon names the field itself. interruptMarkerLine also mentions this key and is
// NOT the place to add it — there its ABSENCE is load-bearing (one extra key and the
// marker reads as a human prompt), and that line rides the JSONL/TUI lane rather than
// this one, so the two never meet.
//
// A map[string]any like writeRateLimitEvent: keys marshal sorted, so the line is
// deterministic without declaring a struct for a shape nothing else reads. json.Marshal
// over a map and never a shell or os.Expand, which is what keeps a `$` in any of these
// values an inert byte — writeBackgroundTaskRoster states the same rule for its
// captured `cat $FIFO` row. Returns the first marshal/write error.
func writeSystemInitLine(w io.Writer) error {
	return writeJSONLine(w, map[string]any{
		"type":                      "system",
		"subtype":                   "init",
		"claude_code_version":       initLineClaudeCodeVersion,
		"permissionMode":            initLinePermissionMode,
		"model":                     initLineModel,
		"agents":                    []string{"claude", "Explore", "general-purpose", "Plan", "statusline-setup"},
		"analytics_disabled":        false,
		"apiKeySource":              "none",
		"capabilities":              []string{"interrupt_receipt_v1", "interrupt_cancel_queued_v1", "msg_lifecycle_v1"},
		"fast_mode_disabled_reason": "sdk_opt_in_required",
		"fast_mode_state":           "off",
		"mcp_servers": []map[string]any{
			{"name": "pyry_approve", "status": "connected"},
		},
		"output_style":              "default",
		"plugins":                   []string{},
		"product_feedback_disabled": false,
		"skills":                    []string{"deep-research", "design-sync", "dataviz"},
		"slash_commands":            []string{"deep-research", "design-sync", "dataviz"},
		"terminal_slash_commands":   []string{"doctor", "color"},
		"tools":                     []string{"Task", "AskUserQuestion", "Bash"},
		// The four the daemon must never surface, substituted for the capture's
		// templates. Their presence here is the assertion's whole subject.
		"cwd":                   initLineCWD,
		"memory_paths":          map[string]any{"auto": initLineMemoryPath},
		"messaging_socket_path": initLineMessagingSocket,
		"session_id":            streamSessionID,
		"uuid":                  initLineUUID,
	})
}

// The captured init-line values the init rider writes verbatim, plus the synthetic
// identity values it stamps in place of the capture's templates. Constants rather than
// inline literals for the rate-limit fixture's reason: the e2e asserts against the same
// values the fake writes, across a main-package boundary it cannot import.
//
// The version and the posture are the two the daemon actually publishes, and both are
// an order of magnitude under the producer's 256-byte per-field caps, so nothing on
// this path truncates and the frame's truncated_fields must cross as null.
const (
	initLineClaudeCodeVersion = "2.1.259"
	initLinePermissionMode    = "default"
	initLineModel             = "claude-sonnet-5"
	initLineUUID              = "77777777-7777-4777-8777-777777777777"
	initLineCWD               = "/fake-claude/workdir"
	initLineMemoryPath        = "/fake-claude/memory/"
	initLineMessagingSocket   = "/fake-claude/messaging.sock"
)

// writeBackgroundTaskRoster writes one system/background_tasks_changed line canning
// `entries` task rows — claude's mid-turn report of what is running in the background,
// and the one line in the daemon's background-task family that no other knob here can
// produce. It is the fake half of #2080's late-connect proof: the daemon retains the
// roster it decodes from this line, and a client connecting afterwards is unicast it.
//
// THE FIRST ROW IS TRANSCRIBED, THE REST ARE SYNTHETIC, and the split is stated
// rather than blurred. Row 0 is the committed capture's own row verbatim
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json, its
// background_tasks_changed record, claude 2.1.220) — including `cat $FIFO`, a literal
// shell command line for the local_bash task type. That value is deliberately kept:
// it is built into the line by json.Marshal over a map (never a shell, never
// os.Expand) and asserted verbatim four layers later, which is what proves the whole
// path treats a command line as inert bytes (#833). The capture holds exactly ONE
// row, so rows 1..entries-1 are synthetic siblings over the same key set — invented
// values, transcribed shape — with ids distinct enough that no assertion can pass on
// the wrong row.
//
// WHY THE KNOB CARRIES A COUNT rather than a boolean, twice over. runStreamJSON's own
// doc records that withholdModeAck was appended so the resulting `string, bool` tail
// makes a mis-slotted call-site edit a compile error, "which a third adjacent bool
// would not"; an int keeps that. And the number is load-bearing: the daemon's parser
// caps a roster at streamsup's maxTaskRosterEntries and reports the remainder as
// dropped_tasks, so driving this OVER that cap is the only way an e2e can prove the
// count is carried rather than defaulted — a fixture whose dropped_tasks is always 0
// cannot tell "carried" from "never populated".
//
// uuid and session_id are carried even though the parser's decode target declares
// NEITHER. That is the point rather than an oversight: every real line has them, and
// a retention that somehow surfaced one would be caught by the e2e asserting the
// payload's field set rather than downstream. The uuid is the capture's own; the
// session_id is the fake's, as writeRateLimitEvent also substitutes.
//
// A map[string]any like writeRateLimitEvent: keys marshal sorted, so the line is
// deterministic without declaring a struct for a shape nothing else reads. Returns
// the first marshal/write error.
func writeBackgroundTaskRoster(w io.Writer, entries int) error {
	return writeTaskRosterRows(w, backgroundTaskRows(entries))
}

func backgroundTaskRows(entries int) []map[string]any {
	tasks := make([]map[string]any, 0, entries)
	for i := range entries {
		if i == 0 {
			tasks = append(tasks, map[string]any{
				"task_id":     rosterCapturedTaskID,
				"task_type":   rosterCapturedTaskType,
				"description": rosterCapturedDescription,
			})
			continue
		}
		tasks = append(tasks, map[string]any{
			"task_id":     fmt.Sprintf("%s%d", rosterSyntheticIDPrefix, i),
			"task_type":   rosterCapturedTaskType,
			"description": fmt.Sprintf("%s%d", rosterSyntheticDescPrefix, i),
		})
	}
	return tasks
}

func writeTaskRosterRows(w io.Writer, tasks []map[string]any) error {
	return writeJSONLine(w, map[string]any{
		"type":       "system",
		"subtype":    "background_tasks_changed",
		"tasks":      tasks,
		"uuid":       rosterUUID,
		"session_id": streamSessionID,
	})
}

// The roster rider's canned values. Constants rather than inline literals for the
// rate-limit fixture's reason: the e2e asserts against the same values the fake
// writes, across a main-package boundary it cannot import, so the two copies must at
// least be greppable as one fixture.
//
// The first three are the capture's own bytes. The two synthetic prefixes are
// obviously so, which is deliberate — no reader should mistake a generated row for a
// measured one — and both stay far under the daemon's per-field caps (256 bytes for
// the two ids, 512 for a roster description), so nothing this rider writes is ever
// truncated and every delivered row's truncated_fields is null.
const (
	rosterCapturedTaskID      = "bybi8g8i8"
	rosterCapturedTaskType    = "local_bash"
	rosterCapturedDescription = "cat $FIFO"
	rosterSyntheticIDPrefix   = "e2e-roster-task-"
	rosterSyntheticDescPrefix = "e2e-roster-description-"
	rosterUUID                = "702eb3a1-a939-43d8-b47d-e200e77712ae"
)

// writeInterruptedResult writes a single result{subtype:"error_during_execution"}
// line — the stream-json shape claude emits for an interrupt-terminated turn.
// streamsup.Parser maps this subtype to turnevent.TurnEnd{TurnEndReasonCancelled}
// (parser.go resultTurnEndReason), so the daemon reports the in-flight turn ended
// cancelled. Used only by the interrupt mode (#1136).
func writeInterruptedResult(w io.Writer) error {
	return writeJSONLine(w, outResult{
		Type:      "result",
		Subtype:   "error_during_execution",
		SessionID: streamSessionID,
	})
}

// writeJSONLine marshals v and writes it to w followed by a single '\n', so the
// emitted object is exactly one stream-json physical line.
func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// writeConversationReset writes one top-level conversation_reset line — claude's
// own announcement that it reset the conversation and mounted a fresh transcript
// under newID (#2135).
//
// The SHAPE is claude's, from #2088's live capture, and the two keys are exactly
// the ones streamsup's emitConversationReset decodes. The VALUE is the caller's,
// so a test can pre-place a transcript under the announced id and assert the
// daemon's context gauge follows it — which is the only way the AC 4 assertion
// discriminates rather than passing on a shared default.
//
// A map[string]any like the sibling writers: keys marshal sorted, so the line is
// deterministic without declaring a struct for a shape nothing else reads. No
// session_id and no uuid rider here, unlike writeBackgroundTaskRoster: the capture
// this is modelled on carries neither, and inventing a field the real line does not
// have would let a daemon-side decode quietly depend on it.
func writeConversationReset(w io.Writer, newID string) error {
	return writeJSONLine(w, map[string]any{
		"type":                "conversation_reset",
		"new_conversation_id": newID,
	})
}
