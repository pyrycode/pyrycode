package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxRateLimitField caps each claude-authored string on a turnevent.RateLimited —
// Status and LimitType. Applied at CONSTRUCTION, exactly as the caps above are,
// so an oversized payload never enters the event stream, the push queue, or any
// log.
//
// One constant for two fields, as maxTaskFieldID serves three: both are short,
// enum-ish values claude chooses out of a set it does not publish.
//
// The MULTIPLE is wide on purpose. The observed values run 7-15 bytes ("allowed",
// "rejected", "five_hour", and the warning band's "allowed_warning" and
// "seven_day"), so 256 is roughly 17x the widest observation — wider than
// maxTaskFieldID's 9x, and deliberately so: the value
// set beyond the one benign status is ALMOST ENTIRELY UNMEASURED — exactly one
// non-benign value is on record and no capture of a limit actually in force
// exists — so on the side that matters
// there is no distribution to reason about and the binding constraint has to come
// from the envelope instead.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one
// RateLimited carries 256 + 256 = 512 bytes of claude-derived text. That is 0.8%
// of the v2 application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap), an order of magnitude under the scalar
// background-task pair's 7.4% and 6.6%. Escaping is mild for maxUnrecognizedRaw's
// reason. ResetsAt and Utilization contribute NO term and get no cap: neither an
// int64 nor a float64 can grow — a float64's shortest round-trip encoding is
// bounded by the type at 39 bytes including the key, measured on the largest
// finite value — and their absence here is a statement rather than an oversight.
//
// The amplification from input to retained bytes is linear and near zero:
// rateLimitEventLine holds four scalars and no array, so a 4 MiB line
// (defaultMaxParseBuf) yields at most 512 bytes of retained text plus one
// integer and one float.
//
// There is also no RATE bound, and its absence is deliberate:
// minThinkingTokensPerEvent exists because thinking_tokens fires ~10 times per
// turn, whereas rate_limit_event fires once per RUN (the capture's census) and
// under the gate below a healthy run STILL emits ZERO — a run whose readings are
// all benign trips no falling edge, since #2250's latch opens only on a non-benign
// one. There is nothing to bound in frequency, so do not go looking for the
// constant that would. Nor did the falling edge move the ceiling that would matter
// if there were: each rung emits AT MOST ONCE and rung 4 adds no second emit to
// any of them, so N lines still yield at most N events and the alternating sequence
// a hostile claude would reach for produces exactly what N non-benign lines already
// produce today. The exposure that
// leaves is named rather than mechanised, per evidence-based fix selection:
// once-per-run is MEASURED, not enforced, so a claude emitting thousands of
// non-benign rate_limit_event lines would produce thousands of events, none of
// them a droppable delta (the droppable set is assistant_delta only, #610),
// holding queue slots. That is the same accepted cost the three background-task
// variants already carry, bounded by the same existing backpressure. Revisit on
// an OBSERVED rate, as #1385 did.
//
// A separate constant even though it currently equals maxTaskFieldID:
// maxTaskPatch's paragraph applies verbatim — they bound different fields for
// different reasons, and folding them into one would make a future change to the
// task-id budget silently move this one.
const maxRateLimitField = 256

// benignRateLimitStatus is the ONE rate_limit_info.status value that can produce no
// event. claude emits rate_limit_event once per run whatever the state of the
// usage-limit window, so without this gate a 1:1 mapping would put one "you are
// rate limited" event on every healthy turn.
//
// CAN rather than DOES since #2250, and the qualifier is the whole of what that
// slice changed here: this value is silent on a parser that has seen nothing else,
// and PUBLISHED as the falling edge on one that has seen a non-benign reading. The
// value set this constant partitions is unchanged; what is new is that the partition
// is read against the parser's memory rather than against the line alone. See
// emitRateLimit's rung 1 and rung 4.
//
// MEASURED, not chosen: the committed rate_limit_event records read "allowed" but
// for the warning band, "allowed_warning", which
// TestParser_RateLimitWarningCaptureCarriesUtilization replays — so the exception is
// pinned against claude's own bytes rather than described here. Neither a count of
// records nor a list of versions is given, and the omission is deliberate:
// compactionPinnedShapes' correction is the precedent, and a tally in a comment goes
// stale on the next capture while the shape above does not. CORRECTED 2026-09-10
// (#2250): this carried exactly such a tally ("EXCEPT ONE, across four claude
// versions") in the same paragraph that forbids one, and it had already gone wrong —
// eight records on two of five versions now read the warning band.
// What is still NOT measured is the rest of the value set: no capture of a limit
// actually in force exists, so emitRateLimit is designed for that openly and
// anything else emits.
//
// Matched by byte-exact equality — no trim, no fold, no prefix — which is the
// tolerance the observations earn, exactly as one observation earns it for
// harnessNoOutputNudge. Unlike that constant, though, the failure direction here
// is NOT the safe one, and the trade is accepted deliberately rather than
// inherited: if claude recapitalises or renames the benign value, this event
// fires once per run on healthy runs — loud and wrong, and one constant edit to
// fix. A folded or prefix match would instead swallow a genuinely new benign-ish
// value and suppress a REAL limit in silence, which is the same wrong with no way
// to notice it.
const benignRateLimitStatus = "allowed"

// rateLimitDropMsg is emitRateLimit's ONE drop message, and the constants below
// are the closed set of reasons it carries. Daemon-authored keywords, never
// claude's values: Status in particular must never reach a log, and it is the
// field a drop site is most tempted to explain itself with.
//
// One message with a closed reason set rather than three messages: it mirrors the
// dropcapReason* shape the realclaude package already uses, and it gives the
// tests one string to filter on.
const (
	rateLimitDropMsg = "streamsup: dropping rate_limit_event"

	rateLimitDropUndecodable = "undecodable"
	rateLimitDropBenign      = "benign"
	rateLimitDropNoInfo      = "no_rate_limit_info"
)

// rateLimitEventLine is the decoded payload of one top-level rate_limit_event
// line. Kept separate from streamLine for systemTaskStartedLine's reason, and it
// is the family's first NESTED target: claude carries this payload one level
// down, under rate_limit_info.
//
// The container is a plain struct, not a *rateLimitInfo. Absence and a
// present-but-empty object are treated IDENTICALLY — both leave Status empty,
// which emitRateLimit's gate reads as "no report was made" and answers with
// silence — so a pointer would buy a distinction nothing acts on. Same argument
// systemThinkingTokensLine makes for int over *int.
//
// The two keys the captured line also carries, uuid and session_id, are
// deliberately absent — see turnevent.RateLimited's doc — and so are the
// payload's four overage keys, two of which are measured version-variable across
// the captures. surpassedThreshold joins them as of #2249: the warning-band
// capture carries it beside the utilization this target now reads, and it is left
// out because nothing asks for it — an unused field is a claim nobody checks.
// Absent from the DECODE TARGET is a stronger guarantee than
// the test's reflection sweep, because a field that is never declared cannot
// leak.
type rateLimitEventLine struct {
	Info rateLimitInfo `json:"rate_limit_info"`
}

// rateLimitInfo is claude's rate_limit_info object reduced to the four keys the
// mapping reads. Status, LimitType and ResetsAt are present in every committed
// record, across four claude versions; Utilization is present in one.
//
// Both strings are plain strings, which is why truncateField's json.RawMessage
// exception does not reach this shape: encoding/json has already U+FFFD-replaced
// invalid input on decode, so our own cut is the only mid-rune hazard. A
// non-string value for either, a non-numeric resetsAt, or a resetsAt too large
// for int64, fails the WHOLE-LINE decode and takes emitRateLimit's undecodable
// arm — exactly as systemTaskUpdatedLine.TaskID does for a numeric task id.
type rateLimitInfo struct {
	Status    string `json:"status"`
	LimitType string `json:"rateLimitType"`
	ResetsAt  int64  `json:"resetsAt"`
	// Utilization is a POINTER so a reading claude omitted is decidable, which is
	// compactMetadata.PostTokens' rule and the INVERSE of the plain int64 beside it:
	// ResetsAt can collapse absence into 0 because 0 there is DEFINED as "not
	// reported", whereas a utilization of 0 is a meaningful reading — a fresh window
	// — and cannot absorb absence without presenting one as exhausted. That
	// distinction is the measured case rather than a hypothetical: the one committed
	// record carrying this key is also the only one whose status is not the benign
	// value, and every benign record omits it entirely.
	//
	// An absent key and an explicit null are ONE reading here (both nil) and nothing
	// downstream answers them differently. A non-numeric value, or a number outside
	// float64's range, fails the WHOLE-LINE decode exactly as a too-large resetsAt
	// does — encoding/json refuses the conversion rather than saturating, so no
	// non-finite reading is representable. JSON has no NaN or Inf literal either,
	// which is why no guard for one is declared.
	Utilization *float64 `json:"utilization"`
}

// emitRateLimit decodes one top-level rate_limit_event line and emits at most one
// turnevent.RateLimited. It never emits an Unrecognized, and it returns nothing:
// consumeLine's case arm consumes the line by MATCHING, so unlike the
// emitSystemSubtype family there is no "did you handle it?" to report back. Field
// mapping comes from the committed captures, never from a hand-built payload.
//
// THE GATE is the substance of this mapping; the field copying is routine. claude
// emits this line ONCE PER RUN whatever the state of the usage-limit window — the
// benign status is what almost every committed record reads, i.e. almost every run
// that produced one hit no limit at all — so a 1:1 mapping would put one "you are
// rate limited" event on every healthy turn. status is the discriminator, and — with
// the PARSER'S OWN MEMORY OF THE LAST ONE, which is what makes this a state machine
// rather than a function of one line (#2250) — it has four reachable readings:
//
//  1. status == benignRateLimitStatus, no non-benign reading before it on this
//     parser → SILENCE. The measured healthy case, and the one the whole gate
//     exists for.
//
//  2. status non-empty and not the benign value → ONE RateLimited. Emit for
//     anything that is not the one measured-benign value, because the failure
//     direction is the safe one: an unrecognised status surfaces and a human
//     looks, rather than a real limit vanishing. Same doctrine ignoredLineTypes
//     already carries — "too wide and a real new message type stays invisible".
//
//  3. rate_limit_info absent, empty, or the line will not decode into the shape →
//     SILENCE. This rung is NOT settled by rung 2, and it is decided AGAINST the
//     naive reading of it. "Emit unless status is allowed" answers an absent
//     container with emit, because an absent object decodes to an empty status.
//     Three reasons it does not. The event would be a claim with no evidence
//     behind it: Status "", LimitType "", ResetsAt 0 names no limit and no reset
//     time, so it cannot serve the purpose the variant exists for, and emitting it
//     is the daemon reporting a rate limit it never observed — the same inference
//     turnevent.BackgroundTaskRoster's doc refuses to make about a task finishing.
//     Its failure mode is the worse of the two available: if a future claude
//     renames or drops the container, rung-3-emits produces one content-free "you
//     are rate limited" row on EVERY healthy run forever, indistinguishable from a
//     real limit and actionable in the wrong direction — exactly the per-turn
//     noise row ignoredLineTypes' doctrine ranks as the outcome to avoid — while
//     rung-3-silent produces a false NEGATIVE on a condition no capture shows a
//     limit actually in force for. CORRECTED 2026-09-09 (#2249): this read "a
//     condition that has never fired once in three captures", and the non-benign
//     condition HAS now fired — the warning band is on record and rung 2 has a
//     captured witness. What survives is the weaker claim the rung actually needs,
//     that no capture shows a limit in force, so the rationale holds while its
//     evidence sentence does not. And it is this package's own precedent for an absent
//     field: emitBackgroundTaskStarted lands the field empty rather than inventing
//     a validation rule, and landing empty on the GATE INPUT means the gate reads
//     "no report was made", which is silence.
//
//  4. status == benignRateLimitStatus WITH a non-benign reading before it on this
//     parser → ONE RateLimited, carrying the benign reading's own fields (#2250).
//     THE FALLING EDGE, and it is a falling edge rather than a relaxation of rung 1.
//     Rung 1's silence is load-bearing and unchanged, but it was unconditional, so
//     the reading that would CLEAR a warning was dropped by the very rung that
//     suppresses the routine one — a client that lit a banner on an allowed_warning
//     had nothing that ever took it down.
//
//     THREE PROPERTIES FALL OUT OF WHERE THE WRITE SITS, and none of them is a rule
//     anyone has to keep. It fires ONCE, because the latch is closed before the emit
//     and a second benign reading takes rung 1. A turn boundary does not clear it,
//     because consumeLine's one reset deliberately does not touch the field — see
//     Parser.rateLimitNonBenign for why it cannot. And rungs 2 and 3 leave the latch
//     alone, because both return before the switch can write, so a malformed or
//     renamed container between the two readings cannot swallow the clear.
//
//     WHAT THE FRAME MAY CLAIM IS BOUNDED, and the doc must not upgrade it. claude
//     reports ONE window per line and chooses which, so a return to the benign
//     status is claude declining to report a non-benign window — not evidence that
//     the warned limit lifted. On every record on file the clearing reading even
//     names a DIFFERENT limit than the warning does (five_hour against seven_day)
//     and carries no utilization at all. The honest reading is "claude's latest
//     reading of the usage window is benign"; docs/protocol-mobile.md § rate_limited
//     states both consequences for a client.
//
// The cost of rung 3 is real and is stated here rather than buried: a container
// RENAME goes undetected by any automatic test. The available detector is the live
// drop census — a renamed-container line is still dropped, so it still lands in
// internal/e2e/realclaude's per-type census WITH its payload, which is exactly how
// #1260 discovered this payload in the first place. A MAPPED line does not appear
// there, so the census cleanly separates "claude reported benign" from "claude
// changed shape".
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// streamLine's doc states the property that preserves: control shapes are read
// from the top level only and nested content is never re-scanned, which is what
// stops a tool result whose text is literally `{"type":"result"}` from forging a
// turn boundary. Decoding this payload from anywhere else would make a usage-limit
// report forgeable out of claude's own tool output.
//
// A payload that will not decode is dropped with a content-free Debug and no event
// — NOT surfaced as an Unrecognized. The family's standing answer
// (emitBackgroundTaskStarted's doc) is that surfacing a malformed line of a type
// we already know is worth less than the structural guarantee that the type never
// reaches the unrecognized lane. Here that guarantee is STRONGER than it was — the
// type is claimed by a case arm rather than by list membership — and breaking it
// would put a bad payload in front of the live zero-unrecognized gate
// (internal/e2e/realclaude/interactive_stream_liveness_test.go) for a line the
// daemon does in fact recognise.
//
// NOTHING FROM THE PAYLOAD IS LOGGED, on any path. The one drop message carries a
// `reason` drawn from the closed keyword set at rateLimitDropMsg, and Status never
// reaches it: that is the field a drop site is most tempted to explain itself
// with, and the one value on this line a future claude could make arbitrarily long
// or arbitrarily revealing.
func (p *Parser) emitRateLimit(line []byte) {
	var rl rateLimitEventLine
	if err := json.Unmarshal(line, &rl); err != nil {
		p.log.Debug(rateLimitDropMsg, "reason", rateLimitDropUndecodable)
		return
	}
	switch rl.Info.Status {
	case benignRateLimitStatus:
		if !p.rateLimitNonBenign {
			p.log.Debug(rateLimitDropMsg, "reason", rateLimitDropBenign)
			return
		}
		// Rung 4, the falling edge (#2250). The latch is closed BEFORE the emit below
		// rather than after it, which is what makes the edge fire once: a second
		// benign reading takes the silent branch one line up. Nothing is logged on
		// this path — the event IS the record, and the value a drop site would be
		// tempted to explain itself with is exactly the one emitRateLimit's doc
		// forbids reaching a log.
		p.rateLimitNonBenign = false
	case "":
		// Rung 3. An absent container, a present-but-empty one, and a container
		// carrying no status all land here and are answered identically — which is
		// what makes the plain-struct decode target sufficient. It returns WITHOUT
		// touching the latch, which is rung 4's precondition rather than an accident
		// of ordering: a container claude renamed or dropped between the two readings
		// would otherwise swallow the clear, restoring the stuck banner one shape in.
		p.log.Debug(rateLimitDropMsg, "reason", rateLimitDropNoInfo)
		return
	default:
		p.rateLimitNonBenign = true
	}

	var cut []string
	bound := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// Sequential statements rather than a composite literal, for
	// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these calls,
	// and inside a literal that order would rest on the left-to-right operand rule
	// rather than on something a reader sees. The second name is the DAEMON's —
	// limit_type, not claude's rateLimitType.
	status := bound(rl.Info.Status, "status", maxRateLimitField)
	limitType := bound(rl.Info.LimitType, "limit_type", maxRateLimitField)

	p.emit(turnevent.RateLimited{
		Status:    status,
		LimitType: limitType,
		// Passed through unbounded and unvalidated in both directions: an int64
		// cannot grow, and see the field's doc for why no range check belongs here.
		ResetsAt: rl.Info.ResetsAt,
		// Unbounded and unvalidated for the same two reasons one line up, and NOT
		// clamped to 0..1: the field's doc says why a range check would be a rule with
		// no captured negative case behind it.
		//
		// THE POINTER CROSSES AS A POINTER, which is what carries claude's presence
		// rather than collapsing it — a nil reading means claude stated none and must
		// not become a zero. It is not deep-copied, the CompactionBoundary arm's rule
		// in turnbridge for compactMetadata's pointers: encoding/json allocated a fresh
		// float64 for this line, this parser retains nothing once emit returns, and
		// nothing downstream mutates an event, so the aliasing is observable to nobody
		// and a defensive copy would only obscure that.
		Utilization: rl.Info.Utilization,
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
}
