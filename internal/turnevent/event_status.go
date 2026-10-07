package turnevent

// ThinkingProgress reports that claude is actively reasoning, and roughly how
// much. It maps claude's system/thinking_tokens line (#1385), the fourth system
// subtype the parser translates rather than drops.
//
// It exists because this is claude's ONLY mid-turn proof of life on the
// stream-json surface: during a long assistant turn nothing else crosses stdout,
// so without it a client showing "thinking" cannot separate a slow answer from a
// wedged one.
//
// The NAME is the daemon's, not claude's, for the reason BackgroundTaskStarted's
// doc gives — but here it also disambiguates, and that is worth stating because
// the colliding name sits a few lines above in this same file. ThoughtChunk
// carries the CONTENT of claude's reasoning; this variant carries NONE — only
// that reasoning is happening and an estimate of its size. A consumer that
// renders this as text has nothing to render.
//
// RATE. The producer emits at most one of these per
// streamsup.minThinkingTokensPerEvent tokens of accumulated delta, so the event
// stream carries strictly fewer of them than claude emits lines (33 lines → 8
// events on the committed capture). Two consequences a consumer must not get
// wrong: the events do NOT enumerate claude's lines, and — the important one —
// the ABSENCE of an event within any particular window does NOT mean thinking
// stopped. It may only mean the accumulated delta has not yet crossed the bound.
// Do not build a "thinking stalled" inference on the gap between two of these.
//
// STALL DETECTION is untouched by this variant, in both directions, and the note
// is here so a future wiring slice does not re-open the question. turnevent.Stall
// now has no producer in the repo: its only one, on the PTY surface, was deleted
// with that surface (#1543), and it never saw this parser anyway.
// streamsup.Watchdog consumes its own copy of raw stdout via
// io.MultiWriter and never reads parser events; it already counts every complete
// line as activity, thinking_tokens included. So this event neither masks nor
// triggers a stall, and no suppression-avoidance mechanism is needed or wanted.
//
// The same two keys the captured line carries are deliberately NOT fields here,
// for the same reasons (#1380): session_id, which is claude's session identity
// and NOT the daemon's conversation identity, and uuid, claude's per-line message
// id, which nothing in the daemon reads.
//
// Main-thread progress opens the daemon's turn; parent-attributed progress does
// not change that lifecycle or publish main-thread progress. The two numeric
// fields need no byte caps. ParentToolCallID follows TextChunk's bounded parent
// attribution contract and is used for classification only. No fields are cut,
// so there is no TruncatedFields. Like every variant here it carries no
// conversation identity — the bridge injects that.
type ThinkingProgress struct {
	// ParentToolCallID names the spawning Agent/Task call, or is empty for main-thread thinking.
	ParentToolCallID string
	// EstimatedTokens is claude's estimate of the tokens it has spent thinking, as
	// of the emitting line.
	//
	// It is cumulative within ONE INFERENCE REQUEST, not within a turn, and it is
	// NOT monotonic across a turn: it restarts near zero at every inference-request
	// boundary. That is measured, not speculative — the committed capture's single
	// turn contains four such restarts (running 5→184, 4→167, 3→126, 1→197). Treat
	// it as a progress reading, never as a turn total, and never diff two of them
	// expecting a non-negative result.
	EstimatedTokens int
	// EstimatedTokensDelta is claude's per-line increment, exactly as it appears on
	// the line that produced this event.
	//
	// The deltas a consumer RECEIVES do not sum to the turn's total, because the
	// rate bound drops most of the lines: on the committed capture the turn's 674
	// tokens of delta arrive as 243 across 8 events. It is a rate reading, not an
	// accumulator input. Summing it undercounts by whatever the dropped lines
	// carried, and no field here reports that residue.
	EstimatedTokensDelta int
}

// RateLimited reports a reading of claude's usage-limit window that the producer's
// gate judged worth repeating: the window entering a state other than the one
// measured-benign one, and — since #2250 — its RETURN to that benign state after
// such a reading. It maps claude's top-level rate_limit_event line (#1404) — the
// fifth claude line-type the parser translates rather than drops, and the first that
// is not a `system` subtype.
//
// SO A RateLimited MAY CARRY THE BENIGN STATUS, which reads as a contradiction in
// the name and is the one thing about this variant worth knowing before the fields.
// It is the FALLING EDGE, and it exists because the gate's silence on the benign
// reading was unconditional: a consumer that lit something on a warning had no event
// that ever took it down. No second variant was minted for it and no daemon-computed
// boolean marks it, because every field here is claude's and the benign Status IS
// the discriminator — see Status. The edge is per-PARSER state, so a consumer must
// not assume one arrives: a session rotation mints a fresh parser, and a warning
// raised before one is never cleared by an event.
//
// It exists so a turn that stops making progress because of a usage limit is
// representable inside the daemon at all. Until #1404 the line was dropped whole
// and the information existed nowhere in pyrycode.
//
// The NAME is the daemon's, not claude's, for the reason BackgroundTaskStarted's
// doc gives — and, as with BackgroundTaskRoster, the translation earns more here
// than insulation from a rename. claude emits rate_limit_event ONCE PER RUN
// whatever the window's state, and almost every run that produced one hit no limit
// at all, so a variant called RateLimitEvent or RateLimitStatus would read as a
// periodic report and invite a consumer to draw one row per healthy turn. This
// variant fires on a CHANGE the gate judged worth repeating and never on the routine
// benign reading; the name states that condition, and it sits with the family's
// other condition-named variants (Stall, Compacting, ApiRetry). What the name does
// NOT state is the falling edge above, and that is the accepted cost of keeping one
// variant for one line rather than splitting a claude line-type across two.
//
// It is a REPORT, never a control input. Nothing in the daemon may key a
// behaviour on it: no backoff, no throttle, no retry, no turn suspension, no
// reconnect delay. Every field is claude-authored text or a claude-authored
// NUMBER — an integer instant and, since #2249, an optional utilization reading —
// crossing the subprocess trust boundary, and the only thing downstream
// of it is display (#1405). That is what keeps a wrong — or hostile — status
// value costing at most one misleading row, rather than a resource action the
// daemon takes on itself. A slice that wants the daemon to ACT on a rate limit is
// re-opening that trust analysis, not extending this one.
//
// It opens and closes no turn, exactly as the background-task variants do not: a
// usage-limit window is orthogonal to whichever turn happened to observe it.
//
// The same two keys the captured line carries are deliberately NOT fields here,
// for the same reasons (#1380): session_id, which is claude's session identity
// and NOT the daemon's conversation identity, and uuid, claude's per-line message
// id, which nothing in the daemon reads. So are the payload's four overage keys —
// overageStatus and isUsingOverage are org-policy detail nothing in the daemon or
// in the user story reads, and overageResetsAt / overageDisabledReason are
// MEASURED version-variable (claude 2.1.158 carries the first and not the second;
// 2.1.199 and 2.1.220 the reverse). Declaring a version-variable key would be
// exactly the field-structure invention this family's decode targets each refuse.
//
// Both string fields are claude-derived and bounded by the producer AT
// CONSTRUCTION (streamsup's maxRateLimitField), following Unrecognized's
// precedent, so an oversized payload never enters the event stream, a queue, or a
// log. Like every variant here it carries no conversation identity — the bridge
// injects that.
type RateLimited struct {
	// Status is claude's own rate_limit_info.status, verbatim.
	//
	// It is on the event because it is the only field that says WHY the event
	// fired, and its value set beyond the benign one is ALMOST ENTIRELY UNMEASURED:
	// exactly one non-benign value is on record — "allowed_warning", the weekly
	// warning band, against limit_type "seven_day" — and no capture of
	// a limit actually in force exists. A frame is therefore NOT proof that anything
	// was blocked: every turn in that capture ran normally.
	//
	// SINCE #2250 THE BENIGN VALUE IS ITSELF A READING THIS FIELD CARRIES, and it is
	// the ONE value a consumer compares against — the falling edge's discriminator,
	// and the only value measured stable on every claude version on file. That does
	// not make the field a closed set in the other direction: everything else here
	// stays an opaque label to render and never to branch on. What the benign value
	// means on THIS event is bounded too — claude reports one window per line and
	// chooses which, so a benign reading is claude declining to report a non-benign
	// window, not evidence that the warned limit lifted.
	// Carrying claude's raw string is how the rest of that
	// set gets measured the first time a real limit fires, instead of the daemon
	// inventing an enum it has no evidence for — so a plain string rather than a
	// closed enum, for BackgroundTaskStarted.TaskType's reason taken one step
	// further. Never empty: the producer's gate does not emit on an empty status.
	Status string
	// LimitType is WHICH limit is in force: claude's rateLimitType ("five_hour" and
	// "seven_day" are the observed values, the second riding the one non-benign
	// status). The name is translated because RateLimitType inside a
	// type called RateLimited stutters; the daemon's snake_case name for
	// TruncatedFields purposes is "limit_type". A plain string, not a closed enum,
	// for Status's reason.
	//
	// THE FALLING EDGE NAMES A DIFFERENT LIMIT THAN THE WARNING IT CLEARS, and this
	// is the field's own trap since #2250. Every benign reading on record says
	// "five_hour" and every warning says "seven_day", so a consumer that keys a
	// banner by this value never matches the event that takes it down. Key the
	// banner on the conversation, not on the limit.
	LimitType string
	// ResetsAt is when claude says the limit lifts, as UNIX SECONDS. 0 when claude
	// did not report it — and the event still fires, because absence of the detail
	// is claude's to choose and the status is the report.
	//
	// It is CLAUDE's number, not the daemon's clock, and it is unvalidated in BOTH
	// directions: a consumer must not assume it lies in the future, and must not
	// assume it lies in a sane range at all. Negative, zero and year-40000 values
	// are all representable and none is rejected here, because rejecting one would
	// be a validation rule with no captured negative case behind it. Formatting it
	// as a date without a range check is the realistic bug.
	//
	// Not a time.Time: converting would invent a claim the bytes do not make (that
	// the number is a valid instant), create a second absent-value question
	// (time.Time{} versus 0), and drag in the project's time.Time round-trip
	// discipline for a field that is only ever a number on a wire. int64 rather
	// than int because a unix timestamp is a 64-bit quantity by nature.
	ResetsAt int64
	// Utilization is how much of the window claude says is spent, as claude's own
	// number. nil when claude reported none — and the event still fires, because
	// absence of the detail is claude's to choose and the status is the report
	// (ResetsAt's rule, stated for absence rather than for 0).
	//
	// A POINTER, unlike ResetsAt, and the asymmetry is the point. ResetsAt folds
	// absence into 0 because 0 there is DEFINED as "not reported"; 0 HERE is a
	// meaningful reading — a fresh window — so folding the two would present an
	// untouched quota as an exhausted one. That is the measured case rather than a
	// hypothetical: every committed record carrying the benign status omits this key,
	// and the single record that carries it is the single non-benign one. Since #2250
	// that measurement has a consequence rather than only a shape: the falling edge
	// carries the benign reading's fields, so a consumer expecting the clear to say
	// "now 40% spent" gets nil and must degrade rather than render a number.
	//
	// It is CLAUDE's number and it is unvalidated in BOTH directions, exactly as
	// ResetsAt is. NOT a bounded fraction: a consumer must not assume it lies in
	// 0..1, and must not assume it lies in a sane range at all. 0.94 is the one
	// observed value; negative, above-one and astronomically large readings are all
	// representable and none is rejected here, because rejecting one would be a
	// validation rule with no captured negative case behind it. Scaling a progress
	// bar by it without a range check is the realistic bug, and the one this field's
	// shape can do nothing about.
	//
	// It does not appear in TruncatedFields and needs no cap: a float64 cannot grow,
	// which is ResetsAt's reason for the same omission.
	//
	// IT IS A REPORT, NOT A THRESHOLD TO BRANCH ON. This type's doc states that
	// nothing in the daemon may key a behaviour on this variant, and a number invites
	// that far more strongly than a status string does — a percentage is the obvious
	// thing to throttle or auto-pause on. The constraint is unchanged and restated
	// here because this is the field that will tempt someone to break it: a slice
	// that wants the daemon to ACT on a usage reading is re-opening the trust
	// analysis, not extending it.
	Utilization *float64
	// TruncatedFields names the fields the producer cut to fit its cap, in
	// declaration order, using the DAEMON's snake_case names: "status",
	// "limit_type" (not claude's rateLimitType — the report names the field it
	// describes). nil when nothing was cut, never an empty non-nil slice, so a
	// consumer can emit it as absent rather than [].
	//
	// Unlike ThinkingProgress this variant DOES carry one: two of its three
	// payload fields are claude-authored strings that can be cut, so the report
	// describes a bound that exists. ResetsAt is absent from it and needs no cap —
	// an int64 cannot grow.
	TruncatedFields []string
}

// Stall is an internal-only onset marker: tui-driver raised a one-shot
// stall_detected signal (no payload, no clearing edge). It carries no fields —
// onset only, no "cleared" state, and (like every variant here) no
// conversation identity; the bridge injects that when mapping to the wire. The
// mobile adapter sends it as the wire "stall" event; the future ACP adapter
// (#600) drops it (no ACP equivalent).
type Stall struct{}

// ApiRetry is a PTY-derived status peer of Stall carrying claude's live
// API-error retry state. Active is the rising (true) / falling (false) edge;
// Current/Total are the parsed `attempt N/M` counter ({0,0} when the counter
// did not parse). Like every variant here it carries no conversation identity —
// the bridge injects it when mapping to the wire.
type ApiRetry struct {
	Active  bool
	Current int
	Total   int
}

// Compacting reports that claude is compacting the conversation. Active is the
// rising (true) / falling (false) edge; like every variant here it carries no
// conversation identity, which the bridge injects when mapping to the wire.
//
// CORRECTED 2026-09-08 (#2227): this doc said "a PTY-derived status peer of Stall",
// and that was false in both directions rather than merely dated. #1348 deleted the
// tui-driver path that made it true, leaving the variant with no producer at all
// from then until now; and the producer this ticket supplies is the stream-json one,
// streamsup's emitCompactingStatus, which maps claude's system/status line — the
// seam #2229's live capture observed. The parenthetical the sentence rested on
// ("tui-driver streams no progress payload") went with it: Active is the only field
// because the EDGE is what lights the banner, not because a deleted driver was
// silent about the rest. #2228 is the ticket that adds claude's trigger and token
// counts on top of this edge.
//
// AMENDED 2026-09-08 (#2236): "Active is the only field" held for one day. The
// closing system/status line carries claude's own outcome across compact_result and
// compact_error, and emitCompactingStatus had been decoding and capping both since
// #2227 — into a Debug record the production daemon does not print. So a failed
// compaction and a successful one were indistinguishable everywhere a client can
// see, which is the whole gap this amendment closes. What changed is which sink two
// already-bounded strings reach, not what bounds them.
type Compacting struct {
	Active bool
	// Result is claude's compact_result off the CLOSING system/status line —
	// "success" on the observed success path (claude 2.1.259, #2229's live lap).
	// An OPEN SET carried verbatim, on TurnEnd.Outcome's rule: a consumer treats an
	// unrecognised token as unknown rather than as an error, because claude may ship
	// one at any time.
	//
	// EMPTY ON EVERY RISING EDGE, and empty is honest on two further paths. claude
	// sends no key at all where compaction succeeded silently, and streamsup's
	// turn-boundary reset — the second producer of a falling edge — has no claude
	// line to read an outcome off at all. Absent, empty and daemon-reset are one
	// reading, which is why this is a plain string: a *string would buy a
	// distinction no consumer answers. #2237 owns the presence-versus-zero question
	// for the fields on the sibling compact_boundary line.
	//
	// RESULT IS NOT A DISCRIMINATOR ANYWHERE IN THE DAEMON, and that is structural
	// rather than incidental: emitCompactingStatus's falling edge is a function of
	// `status` leaving "compacting" and of nothing else, so a failed compaction
	// closes the banner exactly as a successful one does. This field says which it
	// was; it never decides whether the edge fell.
	Result string
	// ErrorText is claude's compact_error off the same line — free-form prose
	// describing why a compaction failed, absent entirely on the observed success
	// path. Named ErrorText rather than Error because a struct field called Error
	// invites confusion with the error interface at every call site that touches it.
	//
	// BOUNDED AT 256 BYTES BY streamsup's maxCompactField, and CUT rather than
	// dropped — which is where it parts company with #2224's ErrorCategory, whose
	// producer drops past its bound. That field is a token set, where a cut token
	// would match no known value while looking like one; this is prose, where a cut
	// sentence still reads as what it is. The cut is not reported: a consumer cannot
	// distinguish a cut value from a short one and needs no such distinction. The
	// producer scrubs the cut for invalid UTF-8 (truncateField), so a slice landing
	// mid-rune cannot reach a JSON string field malformed.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized —
	// TurnEnd.ErrorCategory's SECURITY paragraph applies for provenance, including
	// that the render boundary owing control-character and terminal-escape stripping
	// is the CLIENT's. It does NOT apply for shape, and a consumer that treats the
	// two alike gets this one wrong. ErrorCategory is a short category token; this is
	// arbitrary prose, and newlines, terminal escapes, markup, a URL and text
	// impersonating daemon chrome all fit inside 256 bytes. Render it as inert text
	// attributed to claude — UnrecognizedMessagePayload.Raw's rule, the closer
	// neighbour on shape — never as the daemon's own statement.
	//
	// NOTHING IN THE DAEMON ACTS ON IT: no retry, no backoff, no teardown and no
	// routing is keyed on this value. Whoever first makes the daemon behave
	// differently on it owes the review that turns claude-authored prose into an
	// actuator.
	ErrorText string
}

// CompactionBoundary reports that a compaction finished, what triggered it, and
// how far the context shrank. It maps claude's system/compact_boundary line
// (#2237) — the seventh `system` subtype the parser translates rather than drops,
// and the sibling of the line Compacting's edge pair is read off.
//
// IT IS A SEPARATE VARIANT RATHER THAN THREE MORE FIELDS ON Compacting, and the
// reason is an ORDERING that no amount of design preference can work around. The
// committed capture (internal/e2e/realclaude/testdata/compaction_v2.1.259.json,
// claude 2.1.259, one manual /compact) puts the compact turn's lines in this
// order: status:"compacting", then status:null + compact_result — WHICH IS WHERE
// THE FALLING EDGE FIRES — then system/init, then this line. By the time claude
// states the counts, the frame that would have carried them has shipped. So this
// is conversation-scoped exactly as Compacting is, and a client applies it to the
// divider it has already drawn.
//
// IT ALSO FIRES WITH NO EDGE BEFORE IT, deliberately. The producer
// (streamsup's emitCompactionBoundary) reads and writes NO parser state — not the
// compacting flag, not anything — so a boundary line that followed no
// status:"compacting" is mapped identically to one that did. An auto-compaction
// that announces itself differently is therefore still published, which is the
// second reason the falling edge was the wrong carrier.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE BOUNDARY. That is a class
// change rather than a wider payload, and it is worth stating because the nearest
// sibling is weaker: Compacting.Active is a bool the daemon COMPUTES from a string
// comparison it makes itself, so a client could read that field as the daemon's
// own observation. Nothing here is. A fabricated line reading pre_tokens 999999
// and post_tokens 1 draws a plausible compaction mark where nothing was compacted.
// Render this as claude's ASSERTION, attributed to claude, never as the daemon's
// finding — and note that NOTHING IN THE DAEMON ACTS ON ANY FIELD HERE: no retry,
// no backoff, no teardown and no routing is keyed on them, which is what keeps a
// fabricated value a misleading label rather than an actuator. Whoever first makes
// the daemon behave differently on one owes the review that changes that.
//
// The rest of compact_metadata is NOT carried, and the exclusion is structural
// rather than a filter: streamsup's decode target declares these three fields and
// encoding/json discards every other key, including ones claude has not shipped
// yet. cumulative_dropped_tokens and duration_ms are left out because nothing asks
// for them and an unused field is a claim nobody checks; preserved_segment,
// preserved_messages and logical_parent_uuid are left out because they name
// entries in the OPERATOR'S OWN TRANSCRIPT, and they sit unredacted in the
// committed capture. Like every sibling here it also carries neither claude's
// session_id nor claude's uuid, on BackgroundTaskStarted's rule.
//
// It opens and closes no turn, exactly as ModelAnnounced does not. Like every
// variant here it carries no conversation identity of the DAEMON's — the bridge
// injects that.
type CompactionBoundary struct {
	// Trigger is claude's own compact_metadata.trigger — "manual" on the observed
	// path (claude 2.1.259, a typed /compact), with "auto" claude's other documented
	// value. An OPEN SET carried verbatim, on TurnEnd.Outcome's rule: a consumer
	// treats an unrecognised token as unknown rather than as an error.
	//
	// BOUNDED AT 256 BYTES BY streamsup's maxCompactTrigger, and DROPPED rather than
	// cut — the opposite of Compacting.ErrorText beside it, and the same answer
	// maxTurnEndStopField gives for turn_end's three strings. That field is prose,
	// where a cut sentence still reads as what it is; this is a token a consumer
	// MATCHES, where a cut token would match no known value while looking like one.
	// Carrying the empty value says "no trigger I can offer you", which the consumer
	// must already handle because the set is open. There is consequently no
	// truncation report and none is owed: a dropped scalar is directly observable as
	// the empty value.
	//
	// SECURITY: claude-authored, bounded by the daemon and NOT sanitized. The
	// provenance reading is Compacting.ErrorText's; the SHAPE reading is not, and a
	// consumer that treats the two alike gets this one wrong in the safe direction
	// but for the wrong reason. This is a short token from an open set, so it takes
	// TurnEnd.Outcome's rule — switch on it against known values — never
	// UnrecognizedMessagePayload.Raw's prose latitude.
	Trigger string
	// PreTokens and PostTokens are claude's context size before and after the
	// compaction, exactly as it stated them.
	//
	// POINTERS, and this is where the variant departs from Compacting's field shape
	// on purpose. Compacting.Result is a plain string because absent, empty and
	// daemon-reset are one reading there. Here they are not: a count claude OMITTED
	// and a count of ZERO are different facts, post_tokens is optional in claude's
	// own shape, and a consumer that collapses them renders "24k → 0 tokens" for a
	// boundary claude reported without a post count. nil means claude stated no such
	// count; a non-nil pointer to 0 means claude stated zero.
	//
	// NEITHER IS CLAMPED, RANGE-CHECKED OR ORDERED, on RateLimited.ResetsAt's rule:
	// they are claude's numbers, not the daemon's. PostTokens greater than PreTokens
	// is not rejected and not corrected. A value encoding/json cannot fit in an int
	// fails the WHOLE line's decode and produces no event at all, which is
	// fail-closed and is the one place a single absurd field costs the frame rather
	// than the field.
	PreTokens  *int
	PostTokens *int
}

// Banner reports operator-facing text claude printed ABOUT the session rather
// than as part of an answer — a hook's block reason, a local command's output, a
// loop notification (#2256). It is the one typed place such text arrives; before
// this variant there was none, and the daemon dropped it.
//
// IT HAS ONE PRODUCER SINCE #2319: streamsup's emitInformationalBanner, mapping
// claude's system/informational subtype — a hook's block reason among them —
// against the capture that ticket replays. The variant shipped ahead of that
// producer deliberately, which is this package's established sequencing.
//
// IT HAS NO SECOND PRODUCER. #2258, which owned local_command_output and
// notification, is CLOSED AS ANSWERED (2026-09-10): the capture recorded both
// subtypes unobserved with zero frames, so neither had a field set to map, and the
// operator closed it rather than spend another live capture lap. That is not a claim
// claude never sends them — only that neither is reachable on the input paths tried
// at 2.1.259. The variant's second slot stays open for whichever subtype produces
// bytes first.
//
// IT CARRIES NO TURN IDENTITY, and the reason is the PRODUCER SET rather than
// taste. A prompt a hook refuses is never answered, so no turn exists to attribute
// the refusal to; a notification belongs to claude's own queue and rides no turn at
// all. Stall and Unrecognized are the conversation-scoped precedents. Like every
// variant here it carries no conversation identity of the DAEMON's either — the
// bridge injects that.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE BANNER — CompactionBoundary's
// class change, reached by a shorter route and landing somewhere sharper. That
// variant at least reports a compaction the daemon can corroborate from its own
// parser state; this one reports that claude had something to say, which nothing
// else on the wire can confirm or contradict. It is also the first variant here
// whose WHOLE PURPOSE is arbitrary claude-authored prose with no machine state
// anchoring it: Compacting.ErrorText is prose attached to a compaction the daemon
// observed, and Unrecognized.Raw is prose explicitly labelled a parser gap. A
// consumer renders this one as a first-class notice, so text impersonating daemon
// chrome at Level "warning" is the realistic abuse. Render it as claude's
// ASSERTION, attributed to claude, never as the daemon's own finding.
//
// NOTHING IN THE DAEMON ACTS ON ANY FIELD HERE — no retry, no backoff, no
// teardown, no routing — which is what keeps a fabricated banner a misleading
// label rather than an actuator. StopsTurn is where that constraint is most
// load-bearing and it is restated at the field, because the field NAME is what
// invites the mistake.
//
// It opens and closes no turn, exactly as CompactionBoundary does not.
type Banner struct {
	// Level is claude's own `level` key, adopted VERBATIM — the one field on this
	// variant that keeps claude's spelling, where Text and StopsTurn are renames.
	//
	// An OPEN SET, on TurnEnd.Outcome's rule: a consumer treats an unrecognised token
	// as unknown rather than as an error. `warning` is the observed value (claude
	// 2.1.259, the single informational line in
	// internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json); `info`,
	// `notice` and `suggestion` are claude's other documented values for the key and
	// have NOT been observed on this repo. That distinction is the point of naming
	// them separately rather than presenting four as a set this repo has seen.
	//
	// BOUNDED BY THE PRODUCER AND DROPPED RATHER THAN CUT, the opposite answer Text
	// beside it gets, and the pairing is the trap worth naming on this variant:
	// CompactionBoundary.Trigger argues it in full. This is a token a consumer
	// MATCHES, where a cut token would match no known value while still looking like
	// one; Text is prose, where a cut sentence still reads as what it is. Carrying
	// the empty value says "no level I can offer you", which a consumer must already
	// handle because the set is open, and the drop needs no report: an emptied scalar
	// is directly observable.
	Level string
	// Text is claude's `content`, renamed. It is the field this variant exists to
	// carry, and the rename is half of the translation layer the vocabulary pin in
	// internal/protocol holds — `content` beside `level` would read as the frame's
	// own body rather than as what claude printed.
	//
	// BOUNDED AT 4 KiB BY THE PRODUCER (#2319, whose maxBannerText lands beside
	// streamsup's maxCompactTrigger and maxDenialProse) and CUT rather than dropped,
	// per Level above. The bound is stated here as the CONTRACT THAT TICKET OWES
	// rather than as a fact this file enforces: nothing on this path caps anything,
	// on Compacting.ErrorText's one-cap-site rule, and this variant re-decides no
	// maximum of its own. Whether a cut happened is Truncated's answer, not a length
	// comparison a consumer should make.
	//
	// SECURITY: claude-authored, bounded by the daemon and NOT sanitized. The
	// provenance reading is Compacting.ErrorText's, and unlike CompactionBoundary.Trigger
	// the SHAPE reading carries across too — this is the riskier half of that pair,
	// not the safer one. Newlines, terminal escapes, markup, a URL and text
	// impersonating daemon chrome all fit inside 4 KiB, and the observed line
	// contains a host filesystem path and echoes the operator's own prompt back.
	// Treat it as inert text on UnrecognizedMessagePayload.Raw's rule — never an HTML
	// sink, an attribute or a URL — and attribute it to claude.
	Text string
	// Truncated says whether the PRODUCER cut Text to fit the bound above. It is the
	// producer's answer carried verbatim, never one a consumer recomputes from
	// len(Text): a second authority on the same fact is a second place it can be
	// decided differently.
	//
	// There is no companion DroppedFields slice, unlike ToolCallDenied's pair, and
	// none is owed. Text is the only field the daemon cuts, so one bool names it
	// unambiguously — ToolCallDenied needs a slice because it has six candidate
	// fields — and an emptied Level is directly observable, exactly as
	// CompactionBoundary.Trigger's unreported drop is.
	Truncated bool
	// StopsTurn is claude's `prevent_continuation`, renamed. It says claude will not
	// continue past this banner — the observed value is true, on a hook that refused
	// a prompt.
	//
	// A REPORT, NEVER AN ACTUATOR, and the constraint is restated here rather than
	// left to the type's doc because THIS FIELD'S NAME IS WHAT INVITES THE MISTAKE.
	// It reads like a lever, and a daemon that ever keyed a teardown, a retry
	// suppression or a queue decision on it would hand claude a self-service turn
	// abort — a fabricated line stopping work the operator asked for. Nothing acts
	// on it today. Whoever first makes the daemon behave differently on it owes the
	// review that turns a claude-authored bool into an actuator.
	StopsTurn bool
}
