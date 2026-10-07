package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// --- stream-json mode ------------------------------------------------------
//
// The types and functions below implement PYRY_FAKE_CLAUDE_STREAM_JSON: the
// line-delimited stream-json I/O the daemon's interactive_runner path drives. The
// wire shapes are hand-mirrored (not imported from internal/streamsup) to keep
// this stand-in zero-dependency, exactly like interruptMarkerLine. The inbound
// decode mirrors streamsup.userTurn (envelope.go); the outbound lines are
// byte-compatible with what streamsup.Parser (parser.go) maps to
// turnevent.TextChunk + turnevent.TurnEnd.

// streamSessionID is the fixed session_id fakeclaude stamps on every result line.
// streamsup.Parser.streamLine never decodes session_id, so the value is cosmetic —
// a literal avoids threading the daemon's injected --session-id through the argv
// the fake deliberately ignores.
const streamSessionID = "fake-stream"

// inUserTurn is the minimal decode of one inbound stdin line — only the fields the
// fake reads (the top-level type and the message's text content). It mirrors
// streamsup.userTurn (envelope.go), which is unexported there.
type inUserTurn struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// inControlRequest is the minimal decode of one inbound control_request line — only
// the fields the fake reads (the top-level type and request subtype, the correlation
// id the ack echoes since #1500, and the requested setting value). It mirrors
// streamsup.controlRequest (envelope.go), which is unexported there.
//
// The inbound values sit at different levels, and all are the wire's, not a
// convenience. RequestID is TOP-LEVEL, matching marshalInterruptEnvelope's request
// side; the response side inverts that — see writeInterruptAck. Mode is a SIBLING of
// Subtype inside Request, matching marshalPermissionModeEnvelope, not a second
// top-level field beside RequestID.
//
// Mode, Model, and Detail are empty for subtypes that do not carry them. These are
// decoded absences, not sentinels; the subtype wrappers decide what a response echoes.
type inControlRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype string `json:"subtype"`
		Mode    string `json:"mode"`
		Model   string `json:"model"`
		Detail  string `json:"detail"`
	} `json:"request"`
}

// stdioPermissionRider is the presence-preserving inner request configured by
// envStreamCanUseTool. Values stay raw until json.Marshal writes the request, so
// objects and arrays retain their JSON shape and false remains distinguishable from
// an absent boolean.
type stdioPermissionRider map[string]json.RawMessage

// pendingStdioPermission is the one ask runStreamJSON has written but not yet
// resolved. The stream loop is sequential, so no second ask can become outstanding
// while this value is set.
type pendingStdioPermission struct {
	requestID string
	messageID string
	toolName  string
	toolUseID string
	input     json.RawMessage
}

// inPermissionResponse is the minimal response half of streamsup's can_use_tool
// codec. updatedInput remains raw because the allowed tool_use must expose the
// answering host's JSON value without guessing its schema.
type inPermissionResponse struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior     string          `json:"behavior"`
			UpdatedInput json.RawMessage `json:"updatedInput"`
		} `json:"response"`
	} `json:"response"`
}

// outAssistant / outResult are the two outbound stdout lines fakeclaude writes per
// user turn. Field order/tags produce shapes byte-compatible with
// stream_turn_drain_test.go's assistantTextLine / resultLine, so streamsup.Parser
// maps them to turnevent.TextChunk then turnevent.TurnEnd{end_turn}.
type outAssistant struct {
	Type    string         `json:"type"`
	Message outAsstMessage `json:"message"`
}

type outAsstMessage struct {
	ID      string         `json:"id"`
	Role    string         `json:"role"`
	Content []outTextBlock `json:"content"`
}

type outTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// outAssistantToolUse / outAsstToolMessage / outToolUseBlock are the assistant line
// carrying ONE tool_use block — the shape every committed permission capture
// (internal/e2e/realclaude/testdata/permission_protocol_*) shows for a gated call:
// its own assistant line whose content is a single-element array. The capture's
// message also carries model, usage, stop_reason and more, all omitted here for the
// reason the echo omits them — streamsup's streamMessage reads only id, role and
// content, and streamBlock reads only type/id/name/input off the block.
//
// A DUPLICATED family rather than widening outAsstMessage.Content to []any.
// writeAssistantEcho is that type's only constructor and has three callers
// (runStreamJSON, writeStreamResponse, writeVerdictResponse) whose bytes must not
// move; widening would marshal identically, but then "the echo's bytes are
// unchanged" would rest on a marshal-equivalence argument instead of on nothing
// shared having been touched. Same trade this file already made when it duplicated
// runStreamJSON's read loop for the approve rider.
//
// Structs rather than the nested map[string]any writeInterruptAck and its siblings
// use: those writers need absent-vs-present-false key semantics, this block has a
// fixed four-key shape, and Go sorts map keys — which would ship id, input, name,
// type instead of the captures' order and would stop the shape being
// compile-checked.
type outAssistantToolUse struct {
	Type    string             `json:"type"`
	Message outAsstToolMessage `json:"message"`
}

type outAsstToolMessage struct {
	ID      string            `json:"id"`
	Role    string            `json:"role"`
	Content []outToolUseBlock `json:"content"`
}

type outToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type outResult struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	// ModelUsage is claude's per-model usage map on a result line, of which #2101
	// decodes only contextWindow. omitempty is load-bearing: a nil map emits NO
	// key, so every result line this file writes without the rider stays
	// byte-identical to the pre-#2107 wire.
	ModelUsage map[string]outModelUsage `json:"modelUsage,omitempty"`
}

// outModelUsage is one entry of that map, carrying the full field set the
// committed captures show rather than only the one field the daemon reads. A
// minimal {"contextWindow":N} would still exercise the decode, but it would stop
// being a faithful copy of the wire, and the next reader of this fixture would
// have to go back to the captures to learn what claude actually sends.
type outModelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	WebSearchRequests        int     `json:"webSearchRequests"`
	CostUSD                  float64 `json:"costUSD"`
	ContextWindow            int     `json:"contextWindow"`
	MaxOutputTokens          int     `json:"maxOutputTokens"`
	CanonicalModel           string  `json:"canonicalModel"`
	Provider                 string  `json:"provider"`
}

// riderModelWindowSonnetBase is the model id a TRANSCRIPT names for the entry the
// rider reports a 1M window for; riderModelWindowSonnetKey is the modelUsage KEY
// that same entry is reported under. THE TWO ARE DELIBERATELY DIFFERENT STRINGS
// (#2118), and a test that plants a transcript must name the BASE.
//
// They used to be one constant, and that is exactly what made the full-stack e2e
// unable to fail: both sides of the join were written from it, so the harness
// reproduced the very defect it was meant to catch. The channels genuinely
// disagree — measured end to end on 2026-09-05, claude keys its result-line map
// "claude-opus-5[1m]" while every usage-bearing transcript entry of the same
// session writes "claude-opus-5" — and a fake that spells them identically
// cannot exercise the join's variant fallback at all.
//
// PROVENANCE: the suffixed SHAPE is the live probe recorded on #2118, not a
// reading of a committed capture. No capture in this tree carries a bracketed
// modelUsage key; all 31 key by claude-haiku-4-5, claude-haiku-4-5-20251001 or
// claude-sonnet-5. Only riderModelUsage's NUMERIC fields are copied from a
// capture.
const (
	riderModelWindowSonnetBase = "claude-sonnet-5"
	riderModelWindowSonnetKey  = riderModelWindowSonnetBase + "[1m]"
)

// riderModelUsage is the modelUsage map the rider writes, copied field-for-field
// from the committed capture internal/e2e/realclaude/testdata/
// permission_mode_switch_v2.1.239_plan.json — read as DATA, with no live-claude
// run behind it.
//
// That capture is chosen over the other 29 because it is one of the FOUR whose
// two entries are two genuinely different models at two different window sizes,
// where the other 26 are an alias pair for a single model at one size. A fixture
// of the common shape could not tell a correct join from "report the only
// window", "report the first" or "report the largest"; this one can, because a
// consumer's answer has to name which of the two it followed.
//
// Note the haiku entry appears in its DATED form only, with no undated alias
// beside it — that is how the capture reads, and normalising it here would
// quietly delete the property the fixture exists to carry.
//
// The sonnet entry's KEY carries a trailing variant group (#2118) while its
// canonicalModel keeps the base — that is what the field means, and it is the
// live shape: the variant rides the key, not the canonical name. Its numbers are
// still the capture's, field for field.
func riderModelUsage() map[string]outModelUsage {
	return map[string]outModelUsage{
		"claude-haiku-4-5-20251001": {
			InputTokens:     906,
			OutputTokens:    8,
			CostUSD:         0.000946,
			ContextWindow:   200000,
			MaxOutputTokens: 32000,
			CanonicalModel:  "claude-haiku-4-5",
			Provider:        "firstParty",
		},
		riderModelWindowSonnetKey: {
			InputTokens:              2,
			OutputTokens:             4,
			CacheReadInputTokens:     35298,
			CacheCreationInputTokens: 11869,
			CostUSD:                  0.0545796,
			ContextWindow:            1000000,
			MaxOutputTokens:          64000,
			CanonicalModel:           riderModelWindowSonnetBase,
			Provider:                 "firstParty",
		},
	}
}

// loadStdioPermissionRider reads the default-off can_use_tool rider. It admits only
// fields carried by streamsup.CanUseToolRequest and validates the fields whose scalar
// type that codec pins. Opaque fields accept every JSON shape. subtype is deliberately
// not configurable: writeStdioPermissionRequest supplies can_use_tool itself.
func loadStdioPermissionRider(raw string) stdioPermissionRider {
	if raw == "" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return nil
	}
	for key, value := range fields {
		switch key {
		case "tool_name", "tool_use_id", "agent_id", "blocked_path", "decision_reason_type",
			"title", "display_name", "description":
			var decoded string
			if err := json.Unmarshal(value, &decoded); err != nil {
				return nil
			}
		case "classifier_approvable", "suppress_always_allow_rule", "default_to_no",
			"requires_user_interaction":
			var decoded bool
			if err := json.Unmarshal(value, &decoded); err != nil {
				return nil
			}
		case "input", "permission_suggestions", "decision_reason", "matched_ask_rule":
			// Opaque in streamsup.CanUseToolRequest; encoding/json already proved the
			// value is valid JSON when it decoded the containing object.
		default:
			return nil
		}
	}
	return stdioPermissionRider(fields)
}

// configuredString decodes one optional scalar from the rider. The loader has
// already validated its shape, so the error is unreachable; a missing or JSON-null
// field has the same empty Go value streamsup.CanUseToolRequest would decode.
func (r stdioPermissionRider) configuredString(key string) string {
	var value string
	_ = json.Unmarshal(r[key], &value)
	return value
}

// writeStdioPermissionRequest emits exactly one newline-terminated request. Copying
// the map before adding subtype leaves the loaded configuration reusable on every
// turn and prevents a configured field from replacing the fixed discriminator.
func writeStdioPermissionRequest(w io.Writer, requestID string, rider stdioPermissionRider) error {
	request := make(map[string]json.RawMessage, len(rider)+1)
	for key, value := range rider {
		request[key] = value
	}
	request["subtype"] = json.RawMessage(`"can_use_tool"`)
	return writeJSONLine(w, map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    request,
	})
}

// permissionResponse decodes a successful can_use_tool answer. Its boolean reports
// only the envelope shape; request-id correlation and behavior selection remain in
// runStreamJSON so an unrelated answer cannot resolve the outstanding ask.
func permissionResponse(line []byte) (inPermissionResponse, bool) {
	var response inPermissionResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return inPermissionResponse{}, false
	}
	if response.Type != "control_response" || response.Response.Subtype != "success" {
		return inPermissionResponse{}, false
	}
	return response, true
}

// runStreamJSON reads line-delimited stream-json user-turn envelopes from r and,
// for each {"type":"user",…} line, writes one assistant text line (echoing the
// prompt) followed by one result line to w — one response per received user turn.
// Non-user lines (a blank line, an unparsable line) are read and ignored, mirroring
// streamsup.Parser's per-line resilience. Returns on r EOF (the daemon closed the
// child's stdin during teardown) or the first write error (the daemon's read end is
// gone — treat like EOF). Runs entirely on main()'s goroutine — a single reader, no
// shared state — so -race is clean by construction. The io.Reader/io.Writer seam is
// what lets the package unit test drive read→emit against in-memory buffers.
//
// honorInterrupt selects the interrupt mode (#1136, default-off): a user turn emits
// the assistant echo line ONLY (the result is withheld, so the turn stays in flight),
// and an interrupt control_request emits a control_response ack echoing the request's
// id (#1500) followed by a result{error_during_execution} — which the daemon's parser
// consumes content-free and maps to TurnEnd{cancelled} respectively, ending the
// in-flight turn interrupted. With honorInterrupt false the INTERRUPT control_request
// is ignored (the default the send / new_session / queue riders depend on staying
// byte-identical). The mode is stateless: it emits that pair on each interrupt
// control_request.
//
// An `initialize` control_request (#1689) is answered in BOTH modes and under no
// rider — see writeInitializeAck. A `set_permission_mode` one (#2067) is answered on
// the same terms — see writeSetPermissionModeAck. They are the control lines the fake
// handles unconditionally, because once the daemon starts sending them every
// fake-daemon run sees them regardless of rider. A `get_settings` request is likewise
// answered unconditionally: the production session-settings provider sends it during
// ordinary fake-daemon runs and waits for the correlated response. An `mcp_status`
// request is recognized separately and answered only under envStreamMCPStatus; with
// that rider off it keeps the prior no-answer behavior. Every other control_request
// remains unchanged.
//
// rateLimitStatus selects the rate-limit rider (#1411, default-off): non-empty
// prepends one top-level rate_limit_event line carrying that string as its
// rate_limit_info.status, ahead of the normal reply. Empty means off — which is why
// the parser's third rung (an absent or empty rate_limit_info) is deliberately not
// reachable through this seam and stays parser-tier.
//
// withholdModeAck selects the withheld-mode-ack rider (#2067, default-off): the
// set_permission_mode request is still READ and still consumed by its own arm, but no
// ack is written, so a caller can drive a child that never confirms its posture. It is
// the only rider here that suppresses a line rather than adding one, and the only one
// riding an answer that is otherwise unconditional — which is why the check sits
// INSIDE that arm rather than on its condition: gating the condition would let the
// line fall through to whatever arm follows, inert today and a latent bug the moment
// one is added. False ⟹ off ⟹ the ack is emitted.
//
// rosterTasks selects the background-task-roster rider (#2080, default-off): a
// positive count prepends one system/background_tasks_changed line canning that many
// task entries, ahead of the normal reply, on the rate-limit rider's terms. Zero means
// off. It is a COUNT rather than a bool for two reasons writeBackgroundTaskRoster
// gives — it keeps the tail's mis-slot a compile error, and the number is what lets a
// caller drive the roster over the daemon's entry cap.
//
// emitInit selects the init rider (#2315, default-off): each user turn is preceded by
// one system/init line — see writeSystemInitLine — ahead of everything else the turn
// writes, on the rate-limit rider's terms. It is the FIRST rider whose line the daemon
// turns into two events rather than one: streamsup's emitInitLine decodes the line once
// and calls both emitModelAnnounced and emitSessionFacts, so a capture-faithful line
// carrying `model` produces a model_announced frame beside the session_facts one. A
// consumer must therefore filter by frame type rather than counting frames. False ⟹
// off ⟹ byte-identical.
//
// The riders are parameters in the order they landed, and withholdModeAck is appended
// rather than grouped with the leading bools deliberately: the resulting string, bool
// tail makes a mis-slotted call-site edit a compile error, which a third adjacent bool
// would not. rosterTasks extends the same discipline — bool, int, not a fourth bool.
// modelWindows extends it once more: it lands AFTER the int, so the tail reads
// int, bool and a transposed call site is still a compile error. resetToID extends
// it a further time, so the tail reads bool, string — the same alternation, the same
// compile error on a transposition. emitInit lands last and keeps the alternation
// going one more step: string, bool. It is a BOOL rather than a knob carrying the
// version string for exactly that reason — a second adjacent string would end the
// property every rider before it has preserved, and there is nothing to choose anyway,
// the fixture being one canned line.
func runStreamJSON(r io.Reader, w io.Writer, honorInterrupt, emitBogus bool, rateLimitStatus string,
	withholdModeAck bool, rosterTasks int, modelWindows bool, resetToID string, emitInit bool) {
	runStreamJSONConfigured(r, w, streamRunConfig{
		honorInterrupt:  honorInterrupt,
		emitBogus:       emitBogus,
		rateLimitStatus: rateLimitStatus,
		withholdModeAck: withholdModeAck,
		rosterTasks:     rosterTasks,
		modelWindows:    modelWindows,
		resetToID:       resetToID,
		emitInit:        emitInit,
	})
}

type streamRunConfig struct {
	honorInterrupt  bool
	emitBogus       bool
	rateLimitStatus string
	withholdModeAck bool
	rosterTasks     int
	modelWindows    bool
	resetToID       string
	emitInit        bool
	replay          *streamReplay
	inputCloser     io.Closer
}

type streamReplay struct {
	first       []byte
	second      []byte
	releasePath string
}

func loadStreamReplay(firstPath, secondPath, releasePath string) (*streamReplay, error) {
	if firstPath == "" {
		return nil, nil
	}
	if secondPath == "" && releasePath != "" {
		return nil, errors.New("stream replay release requires a second fragment")
	}
	if secondPath != "" && releasePath == "" {
		return nil, errors.New("stream replay second fragment requires a release path")
	}

	first, err := os.ReadFile(firstPath)
	if err != nil {
		return nil, fmt.Errorf("read first fragment: %w", err)
	}
	replay := &streamReplay{first: first, releasePath: releasePath}
	if secondPath == "" {
		return replay, nil
	}
	second, err := os.ReadFile(secondPath)
	if err != nil {
		return nil, fmt.Errorf("read second fragment: %w", err)
	}
	replay.second = second
	return replay, nil
}

type streamReadResult struct {
	line string
	err  error
}

type asyncStreamReader struct {
	results <-chan streamReadResult
	stopCh  chan struct{}
	done    <-chan struct{}
	closer  io.Closer
}

func startAsyncStreamReader(r io.Reader, closer io.Closer) *asyncStreamReader {
	results := make(chan streamReadResult)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(results)
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			result := streamReadResult{line: line, err: err}
			select {
			case results <- result:
			case <-stopCh:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return &asyncStreamReader{results: results, stopCh: stopCh, done: done, closer: closer}
}

func (r *asyncStreamReader) stop() {
	close(r.stopCh)
	_ = r.closer.Close()
	<-r.done
}

func writeStreamFragment(w io.Writer, fragment []byte) error {
	n, err := w.Write(fragment)
	if err != nil {
		return err
	}
	if n != len(fragment) {
		return io.ErrShortWrite
	}
	return nil
}

func runStreamJSONConfigured(r io.Reader, w io.Writer, cfg streamRunConfig) {
	honorInterrupt := cfg.honorInterrupt
	emitBogus := cfg.emitBogus
	rateLimitStatus := cfg.rateLimitStatus
	withholdModeAck := cfg.withholdModeAck
	rosterTasks := cfg.rosterTasks
	var currentTasks []map[string]any
	modelWindows := cfg.modelWindows
	resetToID := cfg.resetToID
	emitInit := cfg.emitInit

	// bufio.ReadString (not bufio.Scanner) so an arbitrarily long line — a
	// stream-json envelope carries a whole prompt — is never truncated by a token
	// cap, and the final non-newline-terminated bytes at EOF are still processed.
	br := bufio.NewReader(r)
	var input *asyncStreamReader
	var releaseTicker *time.Ticker
	var releaseC <-chan time.Time
	if cfg.replay != nil && cfg.replay.releasePath != "" {
		closer := cfg.inputCloser
		if closer == nil {
			closer, _ = r.(io.Closer)
		}
		if closer == nil {
			return
		}
		input = startAsyncStreamReader(r, closer)
		defer input.stop()
		releaseTicker = time.NewTicker(pollInterval)
		defer releaseTicker.Stop()
	}
	replayStarted := false
	replayComplete := false
	turn := 0
	mcpStatusRequests := 0
	// mcpActuated is STICKY: once this child has accepted any MCP actuation, every
	// later mcp_status answer reports the pending row. A one-shot flag would make the
	// second verb's membership read disagree with the first verb's answer for no reason
	// a test could name, and the stream loop is sequential so no lock is needed.
	mcpActuated := false
	permissionRider := loadStdioPermissionRider(os.Getenv(envStreamCanUseTool))
	var pendingPermission *pendingStdioPermission
	for {
		var line string
		var err error
		if input == nil {
			line, err = br.ReadString('\n')
		} else {
			select {
			case result, ok := <-input.results:
				if !ok {
					return
				}
				line, err = result.line, result.err
			case <-releaseC:
				if _, statErr := os.Stat(cfg.replay.releasePath); statErr != nil {
					continue
				}
				if werr := writeStreamFragment(w, cfg.replay.second); werr != nil {
					return
				}
				_ = os.Remove(cfg.replay.releasePath)
				replayComplete = true
				releaseC = nil
				continue
			}
		}
		if len(line) > 0 {
			b := []byte(line)
			if pendingPermission != nil {
				response, ok := permissionResponse(b)
				if ok && response.Response.RequestID == pendingPermission.requestID {
					switch response.Response.Response.Behavior {
					case "allow":
						input := response.Response.Response.UpdatedInput
						if input == nil {
							input = pendingPermission.input
						}
						if werr := writeAssistantToolUseFor(w, pendingPermission.messageID,
							pendingPermission.toolUseID, pendingPermission.toolName, input); werr != nil {
							return
						}
						if werr := writeVerdictResponse(w, pendingPermission.messageID, verdictAllow); werr != nil {
							return
						}
						pendingPermission = nil
					case "deny":
						if werr := writeVerdictResponse(w, pendingPermission.messageID, verdictDeny); werr != nil {
							return
						}
						pendingPermission = nil
					}
				}
			} else if text, ok := userTurnText(b); ok {
				if cfg.replay != nil {
					if !replayStarted {
						if werr := writeStreamFragment(w, cfg.replay.first); werr != nil {
							return
						}
						replayStarted = true
						if cfg.replay.releasePath == "" {
							replayComplete = true
						} else {
							releaseC = releaseTicker.C
						}
						continue
					}
					if !replayComplete {
						continue
					}
				}
				turn++
				msgID := fmt.Sprintf("m%d", turn)
				if permissionRider != nil {
					requestID := fmt.Sprintf("permission-%d", turn)
					if werr := writeStdioPermissionRequest(w, requestID, permissionRider); werr != nil {
						return
					}
					pendingPermission = &pendingStdioPermission{
						requestID: requestID,
						messageID: msgID,
						toolName:  permissionRider.configuredString("tool_name"),
						toolUseID: permissionRider.configuredString("tool_use_id"),
						input:     permissionRider["input"],
					}
					if os.Getenv(envStreamExitAfterCanUseTool) == "1" {
						return
					}
					if err != nil {
						return
					}
					continue
				}
				// The init rider writes FIRST among the prepends, because that is
				// where claude itself puts the line: every captured turn opens with
				// it. No test drives two riders at once, so the ordering is
				// unobservable today and the choice is capture-fidelity rather than
				// a constraint anything depends on.
				if emitInit {
					if werr := writeSystemInitLine(w); werr != nil {
						return
					}
				}
				// The bogus rider emits its two unmappable shapes BEFORE the real
				// reply, so a test that waits on the reply has necessarily already
				// seen them — no ordering race to tune.
				if emitBogus {
					if werr := writeBogusLines(w, msgID); werr != nil {
						return
					}
				}
				// The rate-limit rider writes on the same terms and for the same
				// reason: BEFORE the reply, so turn_end reaching a client implies the
				// fed line has already been through the parser — no sleep, no poll,
				// no ordering race to tune.
				if rateLimitStatus != "" {
					if werr := writeRateLimitEvent(w, rateLimitStatus); werr != nil {
						return
					}
				}
				// The roster rider writes on the same terms and for the same
				// reason again: BEFORE the reply, so a turn_end reaching a client
				// implies the roster line has already been through the parser —
				// and therefore that the daemon's per-session hold already holds
				// it. That is the happens-before #2080's e2e uses in place of a
				// sleep, and it survives whichever side of the hold's delegation
				// the record happens on, because the two are different LINES read
				// in order on one goroutine.
				if rosterTasks > 0 {
					if werr := writeBackgroundTaskRoster(w, rosterTasks); werr != nil {
						return
					}
					currentTasks = backgroundTaskRows(rosterTasks)
				}
				// The announced-reset rider (#2135) writes on the same terms and for
				// the same reason once more: BEFORE the reply, so a turn_end reaching a
				// client implies the announcement has already been through the parser,
				// the follower and the pool re-key. That is the happens-before the e2e
				// uses in place of a sleep.
				//
				// FIRST TURN ONLY. claude mounts one fresh transcript per reset, and a
				// second announcement of the same id would be an equal-id no-op the
				// daemon deliberately ignores — so re-announcing would test nothing and
				// would make "how many transitions did the client see" depend on how
				// many turns a test happens to drive.
				if resetToID != "" && turn == 1 {
					if werr := writeConversationReset(w, resetToID); werr != nil {
						return
					}
				}
				// Default mode ends the turn (echo + result{success}); interrupt mode
				// echoes ONLY, withholding the result so the turn stays in flight.
				// The model-window rider is NOT a line of its own: it rides the
				// result line claude actually carries modelUsage on, so the
				// interrupt arm — which withholds that line to keep the turn in
				// flight — correctly reports no windows either.
				var usage map[string]outModelUsage
				if modelWindows {
					usage = riderModelUsage()
				}
				var werr error
				if honorInterrupt {
					werr = writeAssistantEcho(w, msgID, text)
				} else {
					werr = writeStreamResponse(w, msgID, text, usage)
				}
				if werr != nil {
					return
				}
			} else if req, ok := decodeControlRequest(b, "stop_task"); ok {
				var task struct {
					Request struct {
						TaskID string `json:"task_id"`
					} `json:"request"`
				}
				if json.Unmarshal(b, &task) != nil {
					return
				}
				index := -1
				for i, row := range currentTasks {
					if row["task_id"] == task.Request.TaskID {
						index = i
						break
					}
				}
				response := map[string]any{"request_id": req.RequestID, "subtype": "error", "error": "unknown task"}
				if index >= 0 {
					response = map[string]any{"request_id": req.RequestID, "subtype": "success", "response": map[string]any{}}
				}
				if writeJSONLine(w, map[string]any{"type": "control_response", "response": response}) != nil {
					return
				}
				if index >= 0 {
					currentTasks = append(currentTasks[:index], currentTasks[index+1:]...)
					// Provisional terminal signal; the standing live gate verifies the vocabulary.
					if writeJSONLine(w, map[string]any{"type": "system", "subtype": "task_notification", "task_id": task.Request.TaskID, "status": "stopped"}) != nil {
						return
					}
					if writeTaskRosterRows(w, currentTasks) != nil {
						return
					}
				}
			} else if reqID, ok := controlRequestID(b, subtypeInitialize); ok {
				// The daemon asked this child to initialize (#1689) — the request real
				// claude answers with the session's model list. Answered UNCONDITIONALLY,
				// beside the honorInterrupt arm rather than inside it: once the daemon
				// starts sending the line every fake-daemon run sees it regardless of
				// rider, so a gated answer would leave the default path — the one every
				// existing suite runs — silently unanswered. Nothing sends the request
				// today, so no existing suite's bytes change.
				if werr := writeInitializeAck(w, reqID); werr != nil {
					return
				}
			} else if reqID, ok := controlRequestID(b, subtypeMCPStatus); ok {
				// The rider is opt-in because every eligible fake child now receives this
				// request, while only the MCP-status e2e wants the reply to enter its wire.
				if os.Getenv(envStreamMCPStatus) != "" {
					mcpStatusRequests++
					if werr := writeMCPStatusAck(w, reqID, mcpStatusRequests > 1, mcpActuated); werr != nil {
						return
					}
				}
			} else if reqID, ok := mcpActuationRequestID(b); ok {
				// The two actuation verbs (#2420). Answered UNCONDITIONALLY, beside the
				// initialize and context-usage arms and for the reason those record: only a
				// gated actuation sends one, so no existing suite's bytes change, and a
				// rider-gated answer would leave a suite that does send one hanging until
				// its caller's deadline rather than failing on the assertion it came for.
				//
				// ACCEPTING ONE FLIPS THE STICKY FLAG the status arm above reads, which is
				// what makes a post-acknowledgement read observably different from the
				// membership read that preceded it. It also models claude: the committed
				// mcp_status capture shows a just-reconnected server commonly reporting
				// pending rather than connected.
				mcpActuated = true
				if werr := writeMCPActuationAck(w, reqID); werr != nil {
					return
				}
			} else if reqID, ok := contextUsageRequestID(b); ok {
				// Context usage is requested during ordinary fake-daemon runs, so its
				// canned answer is unconditional on stream riders just like initialize.
				// Summary and full deliberately share this response: the committed
				// capture found the same payload hierarchy for both detail values.
				if werr := writeContextUsageAck(w, reqID); werr != nil {
					return
				}
			} else if reqID, ok := controlRequestID(b, subtypeGetSettings); ok {
				// Applied settings are requested by the production session-settings
				// provider, so the fake must complete the same correlated control round
				// trip instead of parking the requesting connection until its deadline.
				if werr := writeAppliedSettingsAck(w, reqID); werr != nil {
					return
				}
			} else if reqID, mode, ok := setPermissionModeRequest(b); ok {
				// The daemon told this child which permission posture to adopt (#2067) —
				// the request it will, from #2064 on, write at spawn time and hold every
				// user turn behind. Answered UNCONDITIONALLY and beside the honorInterrupt
				// arm, for the reason the initialize arm above records: once the daemon
				// starts sending the line every fake-daemon run sees it regardless of
				// rider. Nothing sends it today, so no existing suite's bytes change.
				//
				// The rider is checked HERE, inside the arm, rather than on its condition.
				// Withholding means the request is still read and still consumed by this
				// arm and merely goes unanswered — which is the child #2064 needs, one
				// that never confirms its posture but is otherwise alive. Gating the
				// condition instead would let the line fall through to whatever arm
				// follows: inert today, a latent bug the moment one is added.
				if !withholdModeAck {
					if werr := writeSetPermissionModeAck(w, reqID, mode); werr != nil {
						return
					}
				}
			} else if reqID, _, ok := setModelRequest(b); ok {
				if werr := writeSetModelAck(w, reqID); werr != nil {
					return
				}
			} else if honorInterrupt {
				if reqID, ok := interruptControlRequest(b); ok {
					// The daemon routed a phone interrupt to this child as a control_request:
					// ack it, then end the in-flight turn with result{error_during_execution}.
					//
					// The ack goes FIRST, matching real claude's order (~40ms ack, then the
					// result) and buying the e2e its causality on the rate-limit rider's terms
					// above: a turn_end reaching a client implies the ack has already been
					// through the parser, so the zero-unrecognized_message assertion needs no
					// sleep, no poll and no ordering race to tune (#1500).
					if werr := writeInterruptAck(w, reqID); werr != nil {
						return
					}
					if werr := writeInterruptedResult(w); werr != nil {
						return
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// userTurnText decodes one inbound line and, when it is a {"type":"user",…}
// envelope, returns the first text block's text (empty string if the turn carries
// no text block) and true. A line that fails to decode or is not a user turn
// returns ("", false) — the caller skips it, producing no response.
func userTurnText(line []byte) (string, bool) {
	var in inUserTurn
	if err := json.Unmarshal(line, &in); err != nil {
		return "", false
	}
	if in.Type != "user" {
		return "", false
	}
	for _, block := range in.Message.Content {
		if block.Type == "text" {
			return block.Text, true
		}
	}
	return "", true
}
