package turnevent

// ThinkingProgress reports that claude is reasoning, and roughly how much. It
// maps claude's system/thinking_tokens line, claude's only mid-turn proof of
// life on stream-json: during a long assistant turn nothing else crosses stdout,
// so without it a client cannot tell a slow answer from a wedged one. It carries
// none of the reasoning's content; ThoughtChunk does.
//
// Rate: the producer emits at most one event per streamsup's
// minThinkingTokensPerEvent tokens of accumulated delta (33 lines become 8
// events on the committed capture). Events do not enumerate claude's lines, and
// a gap between two does not mean thinking stopped.
//
// It neither masks nor triggers stall detection: streamsup's Watchdog reads raw
// stdout, counts every line as activity and never reads parser events.
//
// Only a main-thread reading (empty ParentToolCallID, per ToolStart's rules)
// opens the daemon's turn and is published; a parent-attributed reading is used
// for classification only. The numeric fields need no cap, so there is no
// TruncatedFields.
type ThinkingProgress struct {
	// ParentToolCallID names the spawning Agent/Task call, or is empty for
	// main-thread thinking.
	ParentToolCallID string
	// EstimatedTokens is claude's estimate of the tokens spent thinking as of
	// the emitting line. It is cumulative within one inference request, not a
	// turn, and restarts near zero at each request boundary (the captured turn
	// has four restarts). Treat it as a progress reading, never a turn total, and
	// never expect the difference of two readings to be non-negative.
	EstimatedTokens int
	// EstimatedTokensDelta is claude's per-line increment from the emitting line.
	// Because the rate bound drops most lines, received deltas do not sum to the
	// turn's total (674 tokens arrive as 243 across 8 events on the capture), and
	// nothing reports the residue. It is a rate reading, not an accumulator input.
	EstimatedTokensDelta int
}

// RateLimited reports a reading of claude's usage-limit window that the
// producer's gate judged worth repeating. It maps claude's top-level
// rate_limit_event line, which claude emits once per run whatever the window's
// state, so the gate drops the routine benign reading and fires on a change:
// the window entering a non-benign state, and its return to the benign state
// after one.
//
// So a RateLimited may carry the benign status. That is the falling edge, and
// the benign Status is its discriminator; no other field or variant marks it.
// The edge is per-parser state, so a consumer must not rely on one arriving: a
// session rotation starts a fresh parser, and a warning raised before it is
// never cleared by an event.
//
// It is a report, never a control input. Nothing in the daemon may key a
// behaviour on it (no backoff, throttle, retry, turn suspension or reconnect
// delay): every field is claude-authored and crosses the subprocess trust
// boundary, and display is its only use downstream. Making the daemon act on a
// rate limit means redoing that trust analysis.
//
// claude's four overage keys are not carried: overageStatus and isUsingOverage
// are org-policy detail nothing reads, and overageResetsAt and
// overageDisabledReason vary by claude version. Both string fields are bounded
// at construction by streamsup's maxRateLimitField.
type RateLimited struct {
	// Status is claude's rate_limit_info.status, verbatim and never empty (the
	// gate does not emit on an empty status). Beyond the benign value
	// (streamsup's benignRateLimitStatus) the set is almost entirely unmeasured:
	// one non-benign value is on record, "allowed_warning", the weekly warning
	// band, and no capture of a limit actually in force exists. An event is not
	// proof that anything was blocked.
	//
	// The benign value is the one a consumer compares against, as the falling
	// edge's discriminator. Every other value is an opaque label to render, never
	// to branch on; a plain string keeps the rest of the set measurable instead
	// of inventing an enum. claude reports one window per line and chooses which,
	// so a benign reading means claude reported no non-benign window, not that
	// the warned limit lifted.
	Status string
	// LimitType is which limit the reading is about: claude's rateLimitType
	// ("five_hour" and "seven_day" observed). Renamed because RateLimitType inside
	// RateLimited stutters; its TruncatedFields name is "limit_type". A plain
	// string for Status's reason.
	//
	// The falling edge names a different limit than the warning it clears: every
	// benign reading on record says "five_hour" and every warning "seven_day".
	// Key a banner on the conversation, not on this value.
	LimitType string
	// ResetsAt is when claude says the limit lifts, in Unix seconds; 0 when
	// claude did not report it, and the event still fires. It is claude's
	// unvalidated number: it need not lie in the future or in any sane range, so
	// range-check it before formatting it as a date. An int64, not a time.Time,
	// which would claim the bytes are a valid instant and add a second absent
	// value.
	ResetsAt int64
	// Utilization is how much of the window claude says is spent, as claude's own
	// number; nil when claude reported none, and the event still fires.
	//
	// A pointer, unlike ResetsAt, because 0 here is a meaningful reading (a fresh
	// window), and folding absence into it would show an untouched quota as an
	// exhausted one. Every committed benign record omits it, so the falling edge
	// usually carries nil and a consumer must degrade rather than render a number.
	//
	// Unvalidated like ResetsAt: not guaranteed to lie in 0..1 or any sane range
	// (0.94 is the one observed value), so range-check it before scaling a
	// progress bar by it. A float64 needs no cap. Like the whole variant it is a
	// report, not a threshold: never throttle or pause on it.
	Utilization *float64
	// TruncatedFields names cut fields ("status", "limit_type") per
	// BackgroundTaskStarted.TruncatedFields. The numeric fields cannot be cut.
	TruncatedFields []string
}

// Stall is an internal-only onset marker that a child stalled: no payload and no
// clearing edge. The mobile adapter sends it as the wire "stall" event; it has
// no ACP equivalent. No producer constructs it today.
type Stall struct{}

// ApiRetry is claude's live API-error retry state, a status peer of Stall.
// streamsup maps each system/api_retry line to an Active true reading and
// publishes one Active false edge before the next assistant, user or result
// content. Current and Total are claude's attempt and max_retries, {0,0} when
// the counter did not decode.
type ApiRetry struct {
	Active  bool
	Current int
	Total   int
}

// Compacting reports that claude is compacting the conversation. Active is the
// rising (true) or falling (false) edge, which is what lights and clears a
// client's banner. streamsup's emitCompactingStatus maps claude's system/status
// line, streamsup's turn-boundary reset also produces a falling edge, and
// codexsup maps Codex's contextCompaction item. The falling edge off claude's
// closing status line carries claude's outcome in Result and ErrorText.
type Compacting struct {
	Active bool
	// Result is claude's compact_result off the closing system/status line
	// ("success" observed at claude 2.1.259). An open set carried verbatim, on
	// TurnEnd.Outcome's rule. Empty on every rising edge, when claude sends no
	// key and on the daemon's turn-boundary reset; those are one reading.
	//
	// It is not a discriminator anywhere in the daemon: the falling edge depends
	// only on claude's status leaving "compacting", so a failed compaction closes
	// the banner exactly as a successful one does. This field says which it was.
	Result string
	// ErrorText is claude's compact_error off the same line: free-form prose on
	// why a compaction failed, absent on success. Named ErrorText, not Error, to
	// avoid confusion with the error interface.
	//
	// Bounded at 256 bytes by streamsup's maxCompactField and cut rather than
	// dropped, since a cut sentence still reads as what it is. The cut is not
	// reported, and truncateField scrubs a mid-rune cut.
	//
	// Security: claude-authored, bounded and not sanitized, and arbitrary prose:
	// newlines, terminal escapes, markup, a URL or text impersonating daemon
	// chrome all fit in 256 bytes. Render it as inert text attributed to claude
	// (protocol.UnrecognizedMessagePayload.Raw's rule); the client's render
	// boundary owes the stripping. Nothing in the daemon acts on it.
	ErrorText string
}

// CompactionBoundary reports that a compaction finished, what triggered it and
// how far the context shrank. It maps claude's system/compact_boundary line.
//
// It is a separate variant, not more Compacting fields, because of line order.
// In the committed capture,
// internal/e2e/realclaude/testdata/compaction_v2.1.259.json, claude sends status
// "compacting", then the closing status line where Compacting's falling edge
// fires, then system/init, then this line. The counts arrive after the edge has
// shipped, so a client applies them to the divider it already drew. The
// producer (streamsup's emitCompactionBoundary) reads and writes no parser
// state, so it also fires when no compacting edge preceded it.
//
// Every field is claude's, and so is the fact of the boundary: a fabricated line
// draws a plausible compaction where none happened. Render it as claude's
// assertion, never the daemon's finding. Nothing in the daemon acts on any
// field.
//
// The rest of compact_metadata is not on streamsup's decode target:
// cumulative_dropped_tokens and duration_ms have no consumer, and
// preserved_segment, preserved_messages and logical_parent_uuid name entries in
// the operator's own transcript. It opens and closes no turn.
type CompactionBoundary struct {
	// Trigger is claude's compact_metadata.trigger: "manual" observed (a typed
	// /compact), "auto" documented. An open set carried verbatim, on
	// TurnEnd.Outcome's rule.
	//
	// Bounded at 256 bytes by streamsup's maxCompactTrigger and dropped rather
	// than cut: a consumer matches this token, and a cut token would match
	// nothing while looking real. It is the only droppable field here, so an
	// empty value needs no drop report.
	//
	// Security: claude-authored, bounded and not sanitized; a short open-set
	// token, so switch on it against known values.
	Trigger string
	// PreTokens and PostTokens are claude's context size before and after the
	// compaction, as stated. Pointers because an omitted count and a count of
	// zero are different facts, and post_tokens is optional in claude's shape: nil
	// means claude stated none.
	//
	// Neither is clamped, range-checked or ordered; PostTokens above PreTokens is
	// kept as sent. A value that does not fit an int fails the whole line's decode
	// and produces no event.
	PreTokens  *int
	PostTokens *int
}

// Banner reports operator-facing text claude printed about the session rather
// than as part of an answer, such as a hook's block reason. streamsup's
// emitInformationalBanner maps claude's system/informational line, and
// codexsup's translator reports a Codex model reroute as a warning banner.
//
// It carries no turn identity: a prompt a hook refuses is never answered, so no
// turn exists to attribute it to. It opens and closes no turn.
//
// Every field is claude's, and so is the fact of the banner, which nothing else
// on the wire can confirm. Its whole purpose is arbitrary claude-authored prose
// shown as a first-class notice, so text impersonating daemon chrome at Level
// "warning" is the realistic abuse: render it as claude's assertion, attributed
// to claude. Nothing in the daemon acts on any field.
type Banner struct {
	// Level is claude's `level` key, verbatim: an open set, on
	// TurnEnd.Outcome's rule. "warning" is observed (claude 2.1.259, in
	// internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json);
	// "info", "notice" and "suggestion" are documented but not observed.
	//
	// Dropped rather than cut past the producer's bound, on
	// CompactionBoundary.Trigger's rule, because a consumer matches it. An
	// emptied Level is directly observable and needs no report.
	Level string
	// Text is claude's `content`, renamed so it does not read as the frame's own
	// body. It is the field this variant exists to carry. Bounded at 4 KiB by
	// streamsup's maxBannerText and cut rather than dropped; Truncated says
	// whether a cut happened.
	//
	// Security: claude-authored, bounded and not sanitized, and arbitrary prose:
	// newlines, terminal escapes, markup, a URL and text impersonating daemon
	// chrome all fit, and the observed line contains a host path and echoes the
	// operator's prompt. Treat it as inert text on
	// protocol.UnrecognizedMessagePayload.Raw's rule (never an HTML sink, an
	// attribute or a URL) and attribute it to claude.
	Text string
	// Truncated is the producer's answer on whether it cut Text; use it rather
	// than recomputing from len(Text). Text is the only field the daemon cuts, so
	// one bool suffices.
	Truncated bool
	// StopsTurn is claude's `prevent_continuation`, renamed: claude will not
	// continue past this banner (observed true on a hook that refused a prompt).
	// It is the only client-visible sign that a prompt was refused, since such a
	// prompt opens and closes no turn.
	//
	// A report, never an actuator, despite a name that reads like a lever:
	// keying a teardown, retry suppression or queue decision on it would let a
	// fabricated line abort the operator's work. Nothing acts on it; making it an
	// actuator needs a security review.
	StopsTurn bool
}
