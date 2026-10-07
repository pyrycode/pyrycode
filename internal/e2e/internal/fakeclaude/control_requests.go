package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The control_request subtypes fakeclaude answers, named so the dispatch in
// runStreamJSON reads by name rather than by bare literal. They are the daemon's
// strings: interrupt is streamsup.marshalInterruptEnvelope's (#1136/#1500), initialize
// is the request the daemon sends to collect the session's model list (#1689), and
// setting subtypes are marshalPermissionModeEnvelope's and marshalModelEnvelope's.
const (
	subtypeInterrupt         = "interrupt"
	subtypeInitialize        = "initialize"
	subtypeMCPStatus         = "mcp_status"
	subtypeMCPReconnect      = "mcp_reconnect"
	subtypeMCPToggle         = "mcp_toggle"
	subtypeGetContextUsage   = "get_context_usage"
	subtypeGetSettings       = "get_settings"
	subtypeSetPermissionMode = "set_permission_mode"
	subtypeSetModel          = "set_model"
)

// decodeControlRequest reports whether line is a control_request carrying subtype,
// returning the decoded envelope for a per-subtype wrapper to read its fields off. It
// mirrors userTurnText's decode discipline: a minimal struct, and a line that fails to
// decode or carries another type or subtype returns (zero, false) — the caller ignores
// it, preserving the parser's per-line resilience.
//
// ONE decode parameterised by subtype rather than a twin per subtype, mirroring the
// argvSessionID → argvIDFlag extraction #1631 made in this file: a duplicated decode
// is what drifts when the request envelope moves. #2067 needed a SECOND field off the
// same line and pushed the guard down here rather than writing that twin — so the
// wrappers below select fields and the type/subtype check exists exactly once.
func decodeControlRequest(line []byte, subtype string) (inControlRequest, bool) {
	var in inControlRequest
	if err := json.Unmarshal(line, &in); err != nil {
		return inControlRequest{}, false
	}
	if in.Type != "control_request" || in.Request.Subtype != subtype {
		return inControlRequest{}, false
	}
	return in, true
}

// controlRequestID reports whether line is a control_request carrying subtype,
// returning its correlation id for an ack to echo — the field-selecting wrapper for
// every subtype whose answer echoes the id alone.
func controlRequestID(line []byte, subtype string) (string, bool) {
	in, ok := decodeControlRequest(line, subtype)
	if !ok {
		return "", false
	}
	return in.RequestID, true
}

// interruptControlRequest reports whether line is the interrupt control_request the
// daemon writes to the child's stdin on a phone interrupt
// (streamsup.marshalInterruptEnvelope:
// {"type":"control_request",…"request":{"subtype":"interrupt"}}), returning its
// correlation id for the ack to echo (#1500).
func interruptControlRequest(line []byte) (string, bool) {
	return controlRequestID(line, subtypeInterrupt)
}

// contextUsageRequestID accepts the two detail values observed in the capture and
// returns the correlation id for the shared canned answer. A missing or unknown
// detail is not a recognized request and falls through without output.
func contextUsageRequestID(line []byte) (string, bool) {
	in, ok := decodeControlRequest(line, subtypeGetContextUsage)
	if !ok {
		return "", false
	}
	switch in.Request.Detail {
	case "summary", "full":
		return in.RequestID, true
	default:
		return "", false
	}
}

// setPermissionModeRequest reports whether line is the set_permission_mode
// control_request the daemon writes to the child's stdin
// (streamsup.marshalPermissionModeEnvelope:
// {"type":"control_request","request_id":…,"request":{"subtype":"set_permission_mode",
// "mode":…}}), returning the two values its ack echoes (#2067).
//
// The only wrapper that reads a second field, which is why decodeControlRequest exists
// underneath it. Neither value is inspected: an unknown mode, or a request carrying no
// mode at all, comes back as it was found — see writeSetPermissionModeAck.
func setPermissionModeRequest(line []byte) (requestID, mode string, ok bool) {
	in, ok := decodeControlRequest(line, subtypeSetPermissionMode)
	if !ok {
		return "", "", false
	}
	return in.RequestID, in.Request.Mode, true
}

func setModelRequest(line []byte) (requestID, model string, ok bool) {
	in, ok := decodeControlRequest(line, subtypeSetModel)
	if !ok {
		return "", "", false
	}
	return in.RequestID, in.Request.Model, true
}

// writeInterruptAck writes the control_response ack real claude answers an interrupt
// control_request with (#1500) — the line the daemon solicits for itself and, before
// this ticket, surfaced to the phone as an unrecognized_message.
//
// The envelope is transcribed from the committed capture
// (internal/e2e/realclaude/testdata/set_permission_mode_v2.1.220_revoke.json, claude
// 2.1.220, transcribed verbatim in docs/knowledge/features/set-permission-mode-inband-probe.md).
// Note what it inverts: `subtype` and `request_id` are nested UNDER `response`, NOT
// top-level, which is the opposite of the request side inControlRequest decodes.
//
// Two honest limits on the provenance, because a fixture that overclaims is what
// this ticket is cleaning up after. The capture is a set_permission_mode ack, not an
// interrupt one — what it establishes is the control channel's ENVELOPE, which is
// all the daemon's arm reads. And the inner `response` payload ({"mode":"default"}
// there) is request-specific and UNMEASURED for interrupt, so this invents none.
//
// A map[string]any like writeRateLimitEvent: keys marshal sorted, so the line is
// deterministic without declaring a struct for a shape nothing else reads. Returns
// the first marshal/write error.
func writeInterruptAck(w io.Writer, requestID string) error {
	return writeJSONLine(w, map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
		},
	})
}

// writeInitializeAck writes the control_response real claude answers an `initialize`
// control_request with (#1692): the ack envelope writeInterruptAck documents —
// `subtype` and `request_id` nested UNDER `response`, the inverse of the request side
// — wrapping the initialize payload one level deeper still, as `response.response`,
// which is where the model list lives.
//
// The envelope and the entries are transcribed from the committed capture
// (internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json, claude 2.1.239).
// TWO arrays now, models and commands, and they are SIBLINGS inside the inner payload
// rather than one nested in the other. The daemon's classification reads them under two
// INDEPENDENT gates — internal/streamsup's emitModelList emits a ModelList for a
// non-empty models array and then calls emitSlashCommandList unconditionally, which
// suppresses itself on a zero-length list — so answering with both produces one of each
// event, ModelList first, and answering with either alone still produces just that one.
//
// What is STILL not transcribed is the rest of what the real answer carries: the inner
// payload also holds agents, output_style, account, pid and session_state, and the outer
// object holds pending_permission_requests and pending_user_dialog_requests. All of it
// is OMITTED rather than invented — this is a fake, not a mirror, and these two arrays
// are what the consumers riding this need. The commands arm landed with #2008, which
// inherited it from #1683 after that ticket closed NOT_PLANNED; a reader arriving from
// the old pointer should look there.
//
// Going through writeJSONLine is load-bearing for the echo rather than a style
// preference. requestID is inbound bytes — whatever the daemon wrote to this child's
// stdin — reflected straight back onto stdout, which the daemon's stream parser reads
// as line-delimited JSON. json.Marshal escapes it, so a request_id carrying a newline
// and a forged envelope lands as one escaped string inside one physical line; built
// with fmt.Sprintf instead, that same value would split the output and fabricate an
// extra stream line the parser consumes as a real event. writeAssistantEcho already
// depends on exactly this property for the echoed prompt. A strange id is echoed,
// never rejected: the fake does not police the daemon's correlation ids.
//
// A map[string]any like writeInterruptAck and writeRateLimitEvent: keys marshal
// sorted, so the line is deterministic without declaring a struct for a shape nothing
// else reads. Returns the first marshal/write error.
func writeInitializeAck(w io.Writer, requestID string) error {
	models, err := loadInitializeModels(os.Getenv(envInitializeModels))
	if err != nil {
		return fmt.Errorf("load initialize models: %w", err)
	}
	return writeJSONLine(w, map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"models":   models,
				"commands": initializeCommands,
			},
		},
	})
}

// writeMCPStatusAck writes the narrow status fixture used by the MCP-status e2e
// flows. fresh selects the changed state returned after this child's automatic
// startup report. All text is inert and non-secret; writeJSONLine safely echoes
// the daemon's request id without allowing it to create a second physical line.
// mcpActuationRequestID reports whether line is one of the two MCP actuation
// control_requests the daemon writes on a gated mcp_reconnect / mcp_toggle
// (streamsup.marshalMCPReconnectEnvelope and marshalMCPToggleEnvelope), returning the
// correlation id its ack echoes.
//
// NEITHER THE SERVER NAME NOR THE ENABLED FLAG IS READ, and that is the fake being
// honest about where the decision lives: membership and authorization are the daemon's
// caller boundary, already settled before a byte reached this child, so a fake that
// re-checked them would prove the gate against a second copy of itself. What it does
// model is the one property claimMCPActuation reads — an ack that echoes the id and
// carries the success subtype.
func mcpActuationRequestID(line []byte) (string, bool) {
	if id, ok := controlRequestID(line, subtypeMCPReconnect); ok {
		return id, true
	}
	return controlRequestID(line, subtypeMCPToggle)
}

// writeMCPActuationAck answers one accepted MCP actuation. The shape is the minimum
// claimMCPActuation consumes: the echoed request id plus the success subtype, with no
// inner response — a matching id alone must not read as accepted, which is why that
// claim tests the subtype and why this ack carries it explicitly.
func writeMCPActuationAck(w io.Writer, requestID string) error {
	return writeJSONLine(w, map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
		},
	})
}

// writeMCPStatusAck answers one mcp_status query. fresh selects the changed-state row a
// second and later query returns; actuated overrides both with the pending reading a
// server commonly gives immediately after being reconnected, which is what lets a test
// tell a post-acknowledgement read from the membership read that preceded it.
func writeMCPStatusAck(w io.Writer, requestID string, fresh, actuated bool) error {
	server := map[string]any{
		"name":   "pyry_mcp_test",
		"status": "failed",
		"error":  "canned connection failure",
		"scope":  "local",
		"serverInfo": map[string]any{
			"version": "9.8.7-test",
		},
	}
	if fresh {
		server = map[string]any{
			"name":   "pyry_mcp_fresh",
			"status": "connected",
			"error":  "",
			"scope":  "project",
			"serverInfo": map[string]any{
				"version": "10.0.0-fresh",
			},
		}
	}
	if actuated {
		server = map[string]any{
			"name":   "pyry_mcp_fresh",
			"status": "pending",
			"error":  "",
			"scope":  "project",
			"serverInfo": map[string]any{
				"version": "10.0.0-fresh",
			},
		}
	}
	return writeJSONLine(w, map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"mcpServers": []map[string]any{server},
			},
		},
	})
}

// loadInitializeModels returns the default capture-backed model menu unless a
// test supplies a JSON fixture path. The override lets an end-to-end test feed a
// newer committed capture through the same stdout parser and retention path as
// the canned response without changing the default bytes for the rest of the
// hermetic suite.
func loadInitializeModels(path string) ([]map[string]any, error) {
	if path == "" {
		return initializeModels, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var models []map[string]any
	if err := json.Unmarshal(raw, &models); err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return models, nil
}

type cannedContextUsageCategory struct {
	Name       string `json:"name"`
	Tokens     int    `json:"tokens"`
	Color      string `json:"color"`
	IsDeferred bool   `json:"isDeferred,omitempty"`
}

type cannedContextUsageMCPTool struct {
	Name       string `json:"name"`
	ServerName string `json:"serverName"`
	Tokens     int    `json:"tokens"`
	IsLoaded   bool   `json:"isLoaded"`
}

type cannedContextUsageMemoryFile struct {
	Path   string `json:"path"`
	Tokens int    `json:"tokens"`
}

type cannedContextUsagePayload struct {
	Categories           []cannedContextUsageCategory   `json:"categories"`
	TotalTokens          int                            `json:"totalTokens"`
	MaxTokens            int                            `json:"maxTokens"`
	RawMaxTokens         int                            `json:"rawMaxTokens"`
	AutocompactSource    string                         `json:"autocompactSource"`
	Percentage           int                            `json:"percentage"`
	AutoCompactThreshold int                            `json:"autoCompactThreshold"`
	IsAutoCompactEnabled bool                           `json:"isAutoCompactEnabled"`
	Model                string                         `json:"model"`
	MCPTools             []cannedContextUsageMCPTool    `json:"mcpTools"`
	MemoryFiles          []cannedContextUsageMemoryFile `json:"memoryFiles"`
}

// writeContextUsageAck writes the captured get_context_usage success hierarchy with
// deterministic fictional values. The deliberately oversized and unsorted lists make
// downstream limit handling observable in the hermetic suite: mcpTools exceeds the
// 32-entry bound, one name exceeds 256 bytes, and every list requires sorting.
func writeContextUsageAck(w io.Writer, requestID string) error {
	return writeJSONLine(w, map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   cannedContextUsage(),
		},
	})
}

const (
	cannedAppliedModel  = "claude-fake-applied"
	cannedAppliedEffort = "high"
)

// writeAppliedSettingsAck answers one get_settings query with the smallest valid
// applied object consumed by streamsup's `decodeAppliedSettings`. The fixed model
// and effort are intentionally independent of saved daemon configuration: the fake
// represents the live child's observation, while the caller remains responsible for
// resolving the addressed child and projecting only effort across the relay seam.
func writeAppliedSettingsAck(w io.Writer, requestID string) error {
	return writeJSONLine(w, map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"applied": map[string]any{
					"model":  cannedAppliedModel,
					"effort": cannedAppliedEffort,
				},
			},
		},
	})
}

func cannedContextUsage() cannedContextUsagePayload {
	return cannedContextUsagePayload{
		Categories: []cannedContextUsageCategory{
			{Name: "Fixture system prompt", Tokens: 1800, Color: "promptBorder"},
			{Name: "Fixture tools", Tokens: 4200, Color: "inactive"},
			{Name: "Fixture deferred tools", Tokens: 900, Color: "inactive", IsDeferred: true},
			{Name: "Fixture messages", Tokens: 2600, Color: "purple_FOR_SUBAGENTS_ONLY"},
		},
		TotalTokens:          9500,
		MaxTokens:            200000,
		RawMaxTokens:         200000,
		AutocompactSource:    "auto",
		Percentage:           5,
		AutoCompactThreshold: 167000,
		IsAutoCompactEnabled: true,
		Model:                "claude-fixture-context",
		MCPTools:             cannedContextUsageMCPTools(),
		MemoryFiles: []cannedContextUsageMemoryFile{
			{Path: "/__pyry_fake__/memory/project.md", Tokens: 19},
			{Path: "/__pyry_fake__/memory/" + strings.Repeat("long-fixture-segment-", 14) + "memory.md", Tokens: 83},
			{Path: "/__pyry_fake__/memory/preferences.md", Tokens: 31},
		},
	}
}

func cannedContextUsageMCPTools() []cannedContextUsageMCPTool {
	const count = 33
	tools := make([]cannedContextUsageMCPTool, 0, count)
	for i := range count {
		name := fmt.Sprintf("mcp__fixture_server__tool_%02d", i)
		if i == count/2 {
			name += "_" + strings.Repeat("long_fixture_name_", 16)
		}
		tools = append(tools, cannedContextUsageMCPTool{
			Name:       name,
			ServerName: "fixture-server",
			Tokens:     40 + (i%7)*37,
			IsLoaded:   i%5 == 0,
		})
	}
	return tools
}

// writeSetPermissionModeAck writes the control_response real claude answers a
// `set_permission_mode` control_request with (#2067): the ack envelope
// writeInterruptAck documents — `subtype` and `request_id` nested UNDER `response`,
// the inverse of the request side — wrapping the requested mode one level deeper
// still, at `response.response.mode`.
//
// The envelope is transcribed from the committed captures, of which there are ten
// carrying eleven of these acks; the nearest is
// internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_reescalate.json
// (claude 2.1.239). Their shape is uniform: {response, type} at the top,
// {subtype, request_id, response} inside, and `mode` ALONE in the payload. Nothing
// else is invented — set_permission_mode_control_test.go cross-checks all three key
// sets against the captures, so an added key would red.
//
// BOTH echoed values are verbatim and unvalidated. writeInitializeAck argues the
// marshalling property for the id and it carries over unchanged: these are inbound
// bytes — whatever the daemon wrote to this child's stdin — reflected straight back
// onto stdout, which the daemon's stream parser reads as line-delimited JSON.
// json.Marshal escapes them, so a value carrying a newline and a forged envelope lands
// as one escaped string inside one physical line; built with fmt.Sprintf instead,
// either value would split the output and fabricate an extra stream line the parser
// consumes as a real event. The mode is the newer of the two and the one that had no
// proof, so its injection row is the one the test leads with.
//
// The mode is NOT checked against streamsup's permissionModeAllowed, and that is
// deliberate rather than an omission. That allow-list is the daemon's own OUTBOUND
// defence against minting an escalating line; a fake that re-applied it inbound could
// no longer reproduce what the daemon actually sent, which is precisely what #2064's
// ack correlation has to observe. The captures settle it: the reescalate one above
// holds claude answering a `bypassPermissions` request — a mode that list does not
// admit — with a plain success. A request naming no mode is likewise answered with the
// key present and empty, not corrected to a default and not refused. The fake echoes,
// it does not police.
//
// A map[string]any like writeInterruptAck and writeInitializeAck: keys marshal sorted,
// so the line is deterministic without declaring a struct for a shape nothing else
// reads. Returns the first marshal/write error.
func writeSetPermissionModeAck(w io.Writer, requestID, mode string) error {
	return writeJSONLine(w, map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"mode": mode,
			},
		},
	})
}

// writeSetModelAck mirrors the success envelope captured from Claude 2.1.259.
// The response correlates by request id and does not echo the alias; the resolved
// model is reported on the next application turn's system/init.
func writeSetModelAck(w io.Writer, requestID string) error {
	return writeJSONLine(w, map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
		},
	})
}

// initializeModels is the canned model list writeInitializeAck answers with — two
// entries transcribed VERBATIM from the committed capture
// (internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json), chosen because
// their two key sets are the contrast a consumer branches on:
//
//   - sonnet carries the capture's eight-key shape: the full five effort levels and
//     supportsAutoMode true.
//   - haiku carries its four-key shape — value, resolvedModel, displayName,
//     description, and NOTHING ELSE.
//
// The capture's own models_entry_fields is a nine-name UNION across entries and
// cannot see per-entry shape; both sets above were read by walking the entries, and
// initialize_control_test.go re-walks them to prove neither shape is invented. The
// capture's opus entry carries a ninth key (supportsFastMode); it is deliberately not
// canned, because two arms is what the consumers need.
//
// A map per entry, not a struct with omitempty: ABSENCE is the load-bearing property
// of this fake's OUTPUT and a map makes it literal — the key is simply not there.
// omitempty would conflate false with absent for supportsAutoMode. The minimal entry
// exists to supply that absent INPUT SHAPE, not to carry a distinction the daemon's
// reading keeps: both readings are settled, and each has one canonical home.
// turnevent.ModelOption.EffortLevels reads an absent supportedEffortLevels, a JSON
// null and a published empty array as ONE reading, spelled nil (#1828).
// turnevent.ModelOption.SupportsAutoMode reads an absent supportsAutoMode, a JSON
// null and an explicit false as ONE reading — false, so a client greys the option
// out on silence; the field is a permission GRANT and the unsafe inverse would be
// granting on silence (#1819).
//
// The collapse is the DECODE's to perform, and the two collapse in different places.
// The list needs code: streamsup's emitModelList, in its boundEach closure's
// zero-length arm, which is the only normalisation anything below the decode does.
// The bool needs none: encoding/json's absent/null no-op already lands all three on
// false, and the plain bool on streamsup's modelOptionLine IS the decision. So the
// minimal entry's job is to feed that decode the shape claude actually sends. A fake
// emitting "supportedEffortLevels":[] or "supportsAutoMode":false instead of omitting
// the keys would hand the decode an already-collapsed input, so the absent-key arm
// would never be traversed — and absent is the only shape observed: the capture's two
// four-key entries omit the entire capability block, and claude has never published
// [] or false at all.
//
// READ-ONLY: never appended to, never reassigned. It is marshalled from
// runStreamJSON's single goroutine in production and from t.Parallel() subtests in
// the package unit test; a mutation would race in a way -race catches only when the
// runs happen to overlap.
var initializeModels = []map[string]any{
	{
		"value":                    "sonnet",
		"resolvedModel":            "claude-sonnet-5",
		"displayName":              "Sonnet",
		"description":              "Sonnet 5 · Efficient for routine tasks",
		"supportsEffort":           true,
		"supportedEffortLevels":    []string{"low", "medium", "high", "xhigh", "max"},
		"supportsAdaptiveThinking": true,
		"supportsAutoMode":         true,
	},
	{
		"value":         "haiku",
		"resolvedModel": "claude-haiku-4-5-20251001",
		"displayName":   "Haiku",
		"description":   "Haiku 4.5 · Fastest for quick answers",
	},
}

// initializeCommands is the canned slash-command inventory writeInitializeAck answers
// with beside initializeModels — the workspace's invocable commands, four entries
// transcribed VERBATIM from the same committed capture
// (internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json) and kept in that
// capture's own relative order, since nothing downstream sorts them.
//
// FOUR ENTRIES, NOT THE CAPTURE'S WHOLE ARRAY, on initializeModels' terms: a fake
// transcribes the key sets a consumer branches on rather than mirroring a real reply.
// The capture has exactly two key sets and no third — most entries carry name,
// description and argumentHint, and a minority carry a fourth aliases key — so four
// rows cover the vocabulary with two of each.
//
// AN ENTRY WITHOUT ALIASES OMITS THE KEY, and this is the load-bearing property of the
// fixture rather than a transcription shortcut. A fake emitting "aliases":[] where
// claude omits the key hands the decode an already-collapsed input, so the absent-key
// arm is never traversed — the same failure initializeModels' minimal entry exists to
// avoid, and absent is the only shape on record: no entry in the capture publishes an
// empty aliases array. A map per entry, not a struct with omitempty, is what makes the
// absence literal: the key is simply not there.
//
// THE INTERLEAVING IS DELIBERATE and it is what an attribution assertion rests on. Two
// alias-bearing entries with DIFFERENT values and DIFFERENT counts — clear's two,
// non-alphabetical so their order is observable, and config's one — separated by
// entries that omit the key. With a single alias-bearing entry a client-side assertion
// cannot separate "aliases attached to the entry that owns them" from "aliases present
// somewhere in the payload", because a key-blind flat reading passes both; with these
// four, that reading reddens on compact and model, and a wrong-owner reading reddens on
// clear and config in both value and count.
//
// WHY ALIASES ARE WORTH CANNING AT ALL: clear publishes reset and new, and the desktop
// Actions menu's own reset entry is that ALIAS rather than a command name, so a path
// carrying names only greys out a command that works. The cheaper source cannot
// substitute — the same capture's system/init line carries a names-only twin under
// slash_commands with the identical names in the identical order and not one alias.
//
// These strings are WORKSPACE-authored in production, a lower-trust origin than claude's
// own: a command defined in a repository was written by whoever wrote that repository.
// They are never logged (#833). Here they are this file's literals, so the posture costs
// the fake nothing — but a consumer copying these rows into an assertion inherits the
// obligation.
//
// READ-ONLY: never appended to, never reassigned, for initializeModels' reason
// restated because it applies independently. It is marshalled from runStreamJSON's
// single goroutine in production and from t.Parallel() subtests in the package unit
// test; a mutation would race in a way -race catches only when the runs happen to
// overlap, so this has to be a stated rule rather than an observed green.
var initializeCommands = []map[string]any{
	{
		"name":         "clear",
		"description":  "Start a new session with empty context; previous session stays on disk (resumable with /resume)",
		"argumentHint": "[name]",
		"aliases":      []string{"reset", "new"},
	},
	{
		"name":         "compact",
		"description":  "Free up context by summarizing the conversation so far",
		"argumentHint": "<optional custom summarization instructions>",
	},
	{
		"name":         "config",
		"description":  "Set a setting by key",
		"argumentHint": "key=value",
		"aliases":      []string{"settings"},
	},
	{
		"name":         "model",
		"description":  "Set the AI model for Claude Code",
		"argumentHint": "<model>",
	},
}
