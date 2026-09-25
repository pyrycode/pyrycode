package codexsup

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Bounds on what the translator builds from Codex's bytes, mirroring
// streamsup's maxUnrecognizedRaw, maxBannerText, maxTurnEndStopField and
// maxModelWindowID.
const (
	maxUnrecognizedRaw = 16 << 10
	maxBannerText      = 4 << 10
	maxStopField       = 256
	maxModelID         = 256
	// maxToolTitle bounds a command's ToolStart title, which reaches the wire
	// as the tool's name.
	maxToolTitle = 4 << 10
	// maxPendingTurns caps the per-turn state keyed by Codex's turnId. Codex
	// runs one turn per thread at a time, so a peer past this is not one.
	maxPendingTurns = 8
)

// Method lanes. Every serverNotifications method sits on exactly one list;
// TestMethodListsPartitionServerNotifications fails otherwise, so a schema
// bump forces a decision about each new method.
var (
	// mappedMethods are translated by Translate.
	mappedMethods = []string{
		"item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta",
		"thread/tokenUsage/updated", "turn/completed", "item/started", "item/completed",
		"model/rerouted",
	}
	// ignoredMethods carry no turn meaning, following streamsup's
	// ignoredLineTypes precedent.
	ignoredMethods = []string{
		"thread/started", "thread/status/changed", "turn/started", "serverRequest/resolved",
		"item/reasoning/summaryPartAdded", "mcpServer/startupStatus/updated",
		"account/updated", "remoteControl/status/changed", "thread/name/updated",
		"thread/settings/updated", "skills/changed", "app/list/updated",
		// Deprecated in favour of the contextCompaction item, which already
		// yields Compacting; a second signal adds no turn meaning.
		"thread/compacted",
		// Fires on every turn. Mapping it onto RateLimited is a later
		// rate-limit ticket.
		"account/rateLimits/updated",
		// The completed item carries the whole output or diff as the call's one
		// ToolUpdate; turnbridge makes every ToolUpdate a tool_result frame, so a
		// delta each would give one row several results.
		"item/commandExecution/outputDelta", "item/fileChange/outputDelta",
	}
	// unrecognizedMethods become Unrecognized on the codex_method lane: seen,
	// but with no mapping yet. model/verification is here so it is never
	// silent.
	unrecognizedMethods = []string{
		"error", "thread/archived", "thread/deleted", "thread/unarchived", "thread/closed",
		"thread/reverted", "thread/attachment/updated", "thread/goal/updated",
		"thread/goal/cleared", "thread/queue/changed", "project/changed",
		"thread/project/updated", "thread/environment/connected",
		"thread/environment/disconnected", "hook/started", "hook/completed",
		"turn/diff/updated", "turn/plan/updated", "item/autoApprovalReview/started",
		"item/autoApprovalReview/completed", "autoApprovalReview/strictReviewRequired",
		"item/plan/delta", "command/exec/outputDelta", "process/outputDelta", "process/exited",
		"item/commandExecution/terminalInteraction", "item/fileChange/patchUpdated",
		"item/mcpToolCall/progress",
		"mcpServer/oauthLogin/completed", "mcpServer/event/stream/notification",
		"externalAgentConfig/import/progress", "externalAgentConfig/import/completed",
		"fs/changed", "model/verification",
		"modelProvider/authRecoveryStarted", "modelProvider/authRecoveryCompleted",
		"turn/moderationMetadata", "model/safetyBuffering/updated", "warning",
		"guardianWarning", "deprecationNotice", "configWarning",
		"fuzzyFileSearch/sessionUpdated", "fuzzyFileSearch/sessionCompleted",
		"thread/realtime/started", "thread/realtime/itemAdded", "thread/realtime/item/started",
		"thread/realtime/item/transcript/delta", "thread/realtime/item/completed",
		"thread/realtime/transcript/delta", "thread/realtime/transcript/done",
		"thread/realtime/outputAudio/delta", "thread/realtime/sdp", "thread/realtime/error",
		"thread/realtime/closed", "windows/worldWritableWarning",
		"windowsSandbox/setupCompleted", "account/login/completed",
	}
)

// Item-type lanes, the same three-way classification over ThreadItem types;
// TestItemTypesClassified checks them against the schema.
var (
	mappedItemTypes = []string{"contextCompaction", "commandExecution", "fileChange"}
	// ignoredItemTypes carry no new turn meaning: a user message echoes the
	// prompt, and agentMessage and reasoning text already arrived as deltas.
	ignoredItemTypes = []string{"userMessage", "agentMessage", "reasoning"}
	// unrecognizedItemTypes become Unrecognized on the codex_item lane, once
	// per item, on item/started.
	unrecognizedItemTypes = []string{
		"hookPrompt", "functionCallOutput", "plan", "mcpToolCall", "dynamicToolCall",
		"collabAgentToolCall", "subAgentActivity", "webSearch", "imageView", "sleep",
		"imageGeneration", "enteredReviewMode", "exitedReviewMode",
	}
)

var (
	mappedItemSet  = setOf(mappedItemTypes)
	ignoredItemSet = setOf(ignoredItemTypes)
	ignoredSet     = setOf(ignoredMethods)
)

func setOf(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// tokenBreakdown is the v2 TokenUsageBreakdown fields a TurnEnd carries.
type tokenBreakdown struct {
	InputTokens           int `json:"inputTokens"`
	CachedInputTokens     int `json:"cachedInputTokens"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
	OutputTokens          int `json:"outputTokens"`
}

func (a tokenBreakdown) minus(b tokenBreakdown) tokenBreakdown {
	return tokenBreakdown{
		a.InputTokens - b.InputTokens, a.CachedInputTokens - b.CachedInputTokens,
		a.CacheWriteInputTokens - b.CacheWriteInputTokens, a.OutputTokens - b.OutputTokens,
	}
}

// clamped raises every negative count to zero. The counts are differences of
// peer-supplied totals, so malformed usage must not reach a TurnEnd negative.
func (a tokenBreakdown) clamped() tokenBreakdown {
	return tokenBreakdown{
		max(a.InputTokens, 0), max(a.CachedInputTokens, 0),
		max(a.CacheWriteInputTokens, 0), max(a.OutputTokens, 0),
	}
}

// turnUsage is one turn's usage so far: the thread's running total before
// the turn began, its latest running total, and the latest window.
type turnUsage struct {
	base, latest tokenBreakdown
	window       int
}

// Translator maps Codex server notifications onto turn events: the Codex twin
// of streamsup's parser. It holds per-turn state (usage, reroute target) and
// is not safe for concurrent use; call it from OnNotification, which runs on
// one goroutine.
//
// Params are untrusted. Text is carried, never interpreted; every string the
// translator builds from them other than text deltas is bounded here. A tool
// call's input, output, paths and diffs are carried as streamsup carries
// tool input and tool_result text, bounded by acp's line cap and capped by
// turnbridge on the way to the wire.
type Translator struct {
	model    string
	usage    map[string]*turnUsage
	rerouted map[string]string
}

// NewTranslator returns a translator for turns running on model, the model
// the caller sets on each turn (TurnInput.Model).
func NewTranslator(model string) *Translator {
	return &Translator{model: model, usage: map[string]*turnUsage{}, rerouted: map[string]string{}}
}

// SetModel changes the model later turns run on.
func (t *Translator) SetModel(model string) { t.model = model }

// Translate maps one server notification to the events it means, in order.
// An ignored method yields none; a method or item type with no mapping yields
// one Unrecognized.
func (t *Translator) Translate(method string, params json.RawMessage) []turnevent.Event {
	switch method {
	case "item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(params, &p) != nil {
			return undecodable(params)
		}
		if method == "item/agentMessage/delta" {
			return []turnevent.Event{turnevent.TextChunk{MessageID: p.ItemID, Text: p.Delta}}
		}
		return []turnevent.Event{turnevent.ThoughtChunk{MessageID: p.ItemID, Text: p.Delta}}
	case "thread/tokenUsage/updated":
		return t.holdUsage(params)
	case "turn/completed":
		return t.turnCompleted(params)
	case "item/started", "item/completed":
		return t.item(method, params)
	case "model/rerouted":
		return t.reroute(params)
	}
	if ignoredSet[method] {
		return nil
	}
	return []turnevent.Event{unrecognized(turnevent.UnrecognizedCodexMethod, method, params)}
}

// holdUsage keeps the turn's usage until its turn/completed. Codex sends one
// update per model call, each before turn/completed; `last` covers only that
// call, so the turn's counts are the change in `total`. The first update of a
// turn covers its first call, so total − last there is the thread's total
// before the turn — which holds on a resumed thread too.
func (t *Translator) holdUsage(params json.RawMessage) []turnevent.Event {
	var p struct {
		TurnID     string `json:"turnId"`
		TokenUsage struct {
			Total              tokenBreakdown `json:"total"`
			Last               tokenBreakdown `json:"last"`
			ModelContextWindow *int           `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(params, &p) != nil {
		return undecodable(params)
	}
	u, ok := t.usage[p.TurnID]
	if !ok {
		if len(t.usage) >= maxPendingTurns {
			clear(t.usage)
		}
		u = &turnUsage{base: p.TokenUsage.Total.minus(p.TokenUsage.Last)}
		t.usage[p.TurnID] = u
	}
	u.latest = p.TokenUsage.Total
	if w := p.TokenUsage.ModelContextWindow; w != nil {
		u.window = *w
	}
	return nil
}

// turnCompleted emits the turn's single TurnEnd, preceded by RateLimited when
// the turn failed on the usage limit.
func (t *Translator) turnCompleted(params json.RawMessage) []turnevent.Event {
	var p struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &p) != nil {
		return undecodable(params)
	}
	end := turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}
	var events []turnevent.Event
	switch p.Turn.Status {
	case "interrupted":
		end.Reason = turnevent.TurnEndReasonCancelled
	case "failed":
		end.IsError = true
		if p.Turn.Error != nil {
			end.ErrorCategory = errorCategory(p.Turn.Error.CodexErrorInfo)
		}
		switch end.ErrorCategory {
		case "contextWindowExceeded":
			end.Reason = turnevent.TurnEndReasonMaxTokens
		case "usageLimitExceeded":
			events = append(events, turnevent.RateLimited{Status: "rejected"})
		}
	}
	model := t.model
	if to, ok := t.rerouted[p.Turn.ID]; ok {
		model = to
		delete(t.rerouted, p.Turn.ID)
	}
	if u, ok := t.usage[p.Turn.ID]; ok {
		delete(t.usage, p.Turn.ID)
		c := u.latest.minus(u.base).clamped()
		// Codex's inputTokens includes the cached input (observed: cached ≤
		// input on every captured update); TurnEnd.InputTokens is uncached only.
		// cacheWriteInputTokens was 0 throughout the capture, so whether input
		// includes it is unmeasured and it is not subtracted.
		end.InputTokens = max(c.InputTokens-c.CachedInputTokens, 0)
		end.CacheReadTokens = c.CachedInputTokens
		end.CacheCreationTokens = c.CacheWriteInputTokens
		end.OutputTokens = c.OutputTokens
		switch {
		case u.window <= 0:
		case model == "" || len(model) > maxModelID:
			end.DroppedModelWindows = 1
		default:
			end.ModelWindows = []turnevent.ModelWindow{{ModelID: model, WindowTokens: u.window}}
		}
	}
	return append(events, end)
}

// errorCategory is a codexErrorInfo token verbatim: the string variant
// itself, or the key of an object variant. An object with other than one key,
// or a token over maxStopField, yields "".
func errorCategory(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return boundStop(s)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || len(obj) != 1 {
		return ""
	}
	for k := range obj {
		return boundStop(k)
	}
	return ""
}

func boundStop(s string) string {
	if len(s) > maxStopField {
		return ""
	}
	return s
}

// item maps item/started and item/completed by the item's type.
func (t *Translator) item(method string, params json.RawMessage) []turnevent.Event {
	var p struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &p) != nil {
		return undecodable(params)
	}
	typ := p.Item.Type
	switch {
	case typ == "contextCompaction":
		return []turnevent.Event{turnevent.Compacting{Active: method == "item/started"}}
	case typ == "commandExecution":
		return commandItem(method, params)
	case typ == "fileChange":
		return fileChangeItem(method, params)
	case ignoredItemSet[typ], mappedItemSet[typ]:
		return nil
	case method == "item/completed":
		// One row per unmapped item: its item/started already surfaced it.
		return nil
	}
	return []turnevent.Event{unrecognized(turnevent.UnrecognizedCodexItem, typ, params)}
}

// commandItem maps a commandExecution item: its item/started to a ToolStart,
// its item/completed to the call's one ToolUpdate.
func commandItem(method string, params json.RawMessage) []turnevent.Event {
	var p struct {
		Item struct {
			ID               string  `json:"id"`
			Command          string  `json:"command"`
			Cwd              string  `json:"cwd"`
			Status           string  `json:"status"`
			AggregatedOutput *string `json:"aggregatedOutput"`
			ExitCode         *int32  `json:"exitCode"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &p) != nil {
		return undecodable(params)
	}
	it := p.Item
	if method == "item/started" {
		input, err := json.Marshal(struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		}{it.Command, it.Cwd})
		if err != nil {
			return undecodable(params)
		}
		return []turnevent.Event{turnevent.ToolStart{
			ToolCallID: it.ID, Title: cut(it.Command, maxToolTitle),
			Kind: turnevent.ToolKindExecute, RawInput: input,
		}}
	}
	update := turnevent.ToolUpdate{ToolCallID: it.ID, Status: toolStatus(it.Status)}
	if it.AggregatedOutput != nil && *it.AggregatedOutput != "" {
		update.Content = turnevent.TextContent{Text: *it.AggregatedOutput}
	}
	switch {
	case it.Status == "declined":
		update.ResultDetail = "declined"
	case it.ExitCode != nil:
		update.ResultDetail = "exit " + exitCode(*it.ExitCode)
	}
	return []turnevent.Event{update}
}

// fileChangeItem maps a fileChange item: its item/started to a ToolStart with
// one Location per changed path, its item/completed to the call's one
// ToolUpdate, whose text is each change's path and then its unified diff as
// Codex sent it.
func fileChangeItem(method string, params json.RawMessage) []turnevent.Event {
	var p struct {
		Item struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Changes []struct {
				Path string `json:"path"`
				Diff string `json:"diff"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &p) != nil {
		return undecodable(params)
	}
	it := p.Item
	if method == "item/started" {
		var locs []turnevent.Location
		for _, c := range it.Changes {
			locs = append(locs, turnevent.Location{Path: c.Path})
		}
		// apply_patch is the Codex tool that makes this item; the title is the
		// wire's tool name, as Claude's Edit is.
		return []turnevent.Event{turnevent.ToolStart{
			ToolCallID: it.ID, Title: "apply_patch", Kind: turnevent.ToolKindEdit, Locations: locs,
		}}
	}
	update := turnevent.ToolUpdate{ToolCallID: it.ID, Status: toolStatus(it.Status)}
	if len(it.Changes) > 0 {
		var b strings.Builder
		for i, c := range it.Changes {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(c.Path + "\n" + c.Diff)
		}
		update.Content = turnevent.TextContent{Text: b.String()}
	}
	if it.Status == "declined" {
		update.ResultDetail = "declined"
	}
	return []turnevent.Event{update}
}

// toolStatus is a completed item's terminal status: only "completed" succeeded.
// failed, declined, and an out-of-contract status on item/completed did not.
func toolStatus(status string) turnevent.ToolStatus {
	if status == "completed" {
		return turnevent.ToolStatusCompleted
	}
	return turnevent.ToolStatusFailed
}

// exitCode formats a decoded exit code for ResultDetail, whose alphabet has
// U+2212 MINUS SIGN and not the ASCII hyphen.
func exitCode(code int32) string {
	if code < 0 {
		return "\u2212" + strconv.FormatInt(-int64(code), 10)
	}
	return strconv.FormatInt(int64(code), 10)
}

// reroute makes a model reroute visible and keys the turn's ModelWindows by
// the model it moved to: a silent downgrade would pass for the asked model.
func (t *Translator) reroute(params json.RawMessage) []turnevent.Event {
	var p struct {
		TurnID    string `json:"turnId"`
		FromModel string `json:"fromModel"`
		ToModel   string `json:"toModel"`
		Reason    string `json:"reason"`
	}
	if json.Unmarshal(params, &p) != nil {
		return undecodable(params)
	}
	if len(t.rerouted) >= maxPendingTurns {
		clear(t.rerouted)
	}
	t.rerouted[p.TurnID] = p.ToModel
	text := "Codex rerouted this turn from " + cut(p.FromModel, maxModelID) +
		" to " + cut(p.ToModel, maxModelID) + " (" + cut(p.Reason, maxStopField) + ")"
	text, truncated := truncate(text, maxBannerText)
	return []turnevent.Event{turnevent.Banner{Level: "warning", Text: text, Truncated: truncated}}
}

func cut(s string, limit int) string {
	s, _ = truncate(s, limit)
	return s
}

// truncate cuts s to limit bytes and scrubs any rune the cut split,
// reporting whether it cut.
func truncate(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return strings.ToValidUTF8(s, ""), false
	}
	return strings.ToValidUTF8(s[:limit], ""), true
}

func unrecognized(site turnevent.UnrecognizedSite, kind string, params json.RawMessage) turnevent.Unrecognized {
	raw, truncated := truncate(string(params), maxUnrecognizedRaw)
	kind, _ = truncate(kind, maxStopField)
	return turnevent.Unrecognized{Site: site, Kind: kind, Raw: raw, Truncated: truncated}
}

func undecodable(params json.RawMessage) []turnevent.Event {
	return []turnevent.Event{unrecognized(turnevent.UnrecognizedUndecodable, "", params)}
}
