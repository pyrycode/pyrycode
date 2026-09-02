package streamsup

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// defaultMaxParseBuf caps the partial-line accumulator (streamrunner's value).
// claude emits newline-delimited JSON, so the remainder between newlines is one
// in-flight line; if it grows past this without a newline (pathological or
// hostile child), the partial is dropped rather than buffered unbounded. 4 MiB
// clears any realistic single stream-json line. Carried as a per-parser field
// (maxBuf) so a test can shrink it without racing a shared global.
const defaultMaxParseBuf = 4 << 20

// maxUnrecognizedRaw caps the raw JSON carried on a turnevent.Unrecognized.
// Applied at CONSTRUCTION, so an oversized payload never enters the event
// stream, the push queue, or any log — the cap is the only thing standing
// between a pathological line and the wire's size limit.
//
// The binding limit is the v2 application-envelope cap of 65519 bytes, NOT v1's
// 1 MiB, which v2 superseded (docs/protocol-mobile.md § Application-envelope
// size cap). 16 KiB is roughly a quarter of it, which leaves room for the
// envelope's other fields plus the JSON escaping this blob picks up on the way
// out. Escaping is mild in practice because the payload is already JSON text:
// its control characters arrive pre-escaped as printable pairs, so the growth is
// quotes and backslashes rather than a \u00XX expansion of every byte.
//
// It is also far more than a human reads off a timeline row, which is the other
// reason not to raise it.
const maxUnrecognizedRaw = 16 << 10

// maxTaskFieldID caps each machine-generated identifier on a
// turnevent.BackgroundTaskStarted — TaskID, ToolCallID, TaskType. Applied at
// CONSTRUCTION, exactly as maxUnrecognizedRaw is, so an oversized payload never
// enters the event stream, the push queue, or any log.
//
// The longest such field in the committed capture is tool_use_id at 29 bytes
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), so 256 is
// roughly 9x the observed maximum: room for a format claude has not shipped yet,
// and still a hard cut on anything that has stopped being an identifier.
const maxTaskFieldID = 256

// maxTaskDescription caps the Description field, which is model-authored and
// genuinely variable — for claude's local_bash task type it is the command line
// itself, so it earns a far larger cap than the identifiers above.
//
// The capture's description reads 9 bytes, but it is REDACTED: the record's
// payload_len_bytes_captured (377) minus payload_len_bytes (235) is 142 bytes of
// redaction across two sites ($SESSION_ID and $FIFO), which puts the real
// description at roughly 126 bytes. 4096 is about 32x that.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one event
// carries 3*256 + 4096 = 4864 bytes of claude-derived text. That is 7.4% of the
// v2 application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap) and under a third of maxUnrecognizedRaw's
// whole-line 16 KiB, which leaves room for the envelope's other fields plus the
// JSON escaping these strings pick up on the way out. It is also far more command
// line than a human reads off a timeline row, which is the other reason not to
// raise it.
const maxTaskDescription = 4 << 10

// maxTaskPatch caps the Patch field on a turnevent.BackgroundTaskUpdated —
// claude's patch object, serialized and carried whole. Applied at CONSTRUCTION,
// exactly as the caps above are, so an oversized payload never enters the event
// stream, the push queue, or any log. That the field is decoded permissively
// (json.RawMessage takes ANY valid JSON value) is what makes this cap
// load-bearing rather than cosmetic: it is the only shape constraint on the
// value.
//
// Neither existing constant fits. maxTaskFieldID (256) caps machine-generated
// identifiers, which have a bounded format; a patch has none, and 256 bytes is
// about three short keys — one prose-ish value (an error string, a status
// message) would be cut on arrival. And the observation is too weak to multiply:
// the captured patch is 24 bytes with ONE key, and a single-key patch says
// nothing about a two-key one. maxTaskDescription could anchor on its
// observation because a command line's size distribution is something we can
// reason about; this cannot, so the binding constraint comes from the other
// side — the envelope.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one
// BackgroundTaskUpdated carries 256 + 4096 = 4352 bytes of claude-derived text.
// That is 6.6% of the v2 application-envelope cap of 65519 bytes
// (docs/protocol-mobile.md § Application-envelope size cap), deliberately in
// line with BackgroundTaskStarted's 4864 bytes / 7.4% so the event family has
// ONE worst case a reader can hold rather than a per-variant number to
// re-derive. Escaping is mild for maxUnrecognizedRaw's reason, which applies
// here verbatim: a patch is already JSON text, so its control characters arrive
// pre-escaped as printable pairs and the growth is quotes and backslashes, not
// a \u00XX expansion of every byte. Pathological all-quote content roughly
// doubles it — ~8.7 KB, 13.3% of the envelope, still comfortable.
//
// 4096 is a quarter of maxUnrecognizedRaw's whole-line 16 KiB, which is the
// ordering that must hold: one field of one KNOWN line must not approach the cap
// on an entire UNKNOWN line.
//
// A separate constant even though it currently equals maxTaskDescription: they
// bound different fields for different reasons, and folding them into one would
// make a future change to the command-line budget silently move the patch
// budget.
const maxTaskPatch = 4 << 10

// maxTaskRosterEntries caps how many entries a turnevent.BackgroundTaskRoster
// carries. It is the family's first CARDINALITY bound and the one dimension with
// no precedent in this package: every cap above bounds text on a fixed field
// set, and a per-entry text cap alone would leave a roster's total size a
// function of a number claude chooses. Applied at CONSTRUCTION like the others,
// so an oversized payload never enters the event stream, the push queue, or any
// log. Overflow is REPORTED (BackgroundTaskRoster.DroppedTasks), not silent, so
// an under-sized count is visible rather than a lie.
//
// It QUALIFIES the family's single-worst-case doctrine stated at maxTaskPatch,
// which does not survive an aggregate variant unexamined. Reusing
// maxTaskDescription here would put one entry at 256 + 256 + 4096 = 4608 bytes,
// so only a ONE-entry roster could match the scalar pair's ~4.9 KB — and a
// one-entry roster is not a roster; at 14 entries a single event would consume
// 98.5% of the envelope and at 15 exceed it. The doctrine's purpose is
// legibility, so it is qualified rather than forced: ONE worst case per SHAPE,
// not per variant. A scalar background-task event is <= ~4.9 KB; the roster is
// <= 8 KiB. Two numbers, one per shape, both a small fraction of the envelope.
// maxTaskPatch's own sentence is left unedited — it describes the scalar pair
// accurately and still does.
//
// The arithmetic, in maxUnrecognizedRaw's style:
//
//   - One entry: maxTaskFieldID + maxTaskFieldID + maxTaskRosterDescription =
//     256 + 256 + 512 = 1024 bytes exactly, a unit a reader can hold.
//   - Worst case one event: 8 * 1024 = 8192 bytes, 12.5% of the v2
//     application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
//     Application-envelope size cap). Larger than the scalar pair's 7.4% and
//     6.6%, per the qualification above, and still a fraction.
//   - 8192 is exactly HALF of maxUnrecognizedRaw's whole-line 16 KiB, which
//     preserves the package's ordering one level up: a whole KNOWN event must
//     not approach the cap on an entire UNKNOWN line.
//   - Escaping is mild for maxUnrecognizedRaw's reason, verbatim: these are JSON
//     string values, so control characters arrive pre-escaped as printable pairs
//     and the growth is quotes and backslashes, not a \u00XX expansion of every
//     byte. Pathological all-quote content roughly doubles it — ~16 KB, ~25% of
//     the envelope, still comfortable.
//   - The count itself: the observed roster holds ONE entry, so 8 is 8x the
//     observation — the same multiple-of-observation form maxTaskFieldID uses,
//     and the number that makes the product land on a clean 8 KiB.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened. That is bounded, not unbounded:
// defaultMaxParseBuf caps the whole line at 4 MiB before the decoder sees it,
// the densest legal entry is ~55 bytes of input for a ~64-byte struct, so the
// amplification is linear and near 1. This cap bounds what is RETAINED and what
// crosses the wire, which is the property that matters.
const maxTaskRosterEntries = 8

// maxTaskRosterDescription caps each roster entry's Description — the same
// model-authored field maxTaskDescription bounds on a
// turnevent.BackgroundTaskStarted, deliberately given a smaller budget here.
//
// Not thrift: the MULTIPLICATION. This is the one field in the family whose
// budget is multiplied by a count claude chooses, and a multiplied field earns a
// smaller unit budget than the same field carried once. It is also a different
// ROLE: on task_started the description is the event's payload, the one thing
// the event is about; in a roster it is a label in a list whose authoritative
// full-length copy already crossed the wire on the BackgroundTaskStarted this
// entry's task_id joins back to. A cut here loses nothing a consumer holding
// that event cannot recover, and TruncatedFields says it happened.
//
// 512 is ~4x the ~126-byte real description the capture's redaction arithmetic
// implies (payload_len_bytes_captured 354 - payload_len_bytes 212 = 142 bytes
// across $SESSION_ID and $FIFO). A thinner multiple than maxTaskFieldID's 9x or
// maxTaskDescription's 32x, and deliberately so, for the multiplication reason
// above. The weak point is a consumer that never saw the BackgroundTaskStarted —
// connected mid-session, or the task predates the connection — for which 512
// bytes is all there is; TruncatedFields is what will surface that if it bites.
const maxTaskRosterDescription = 512

// minThinkingTokensPerEvent is the accumulated estimated_tokens_delta that earns
// one turnevent.ThinkingProgress. It is the package's first constant bounding
// FREQUENCY rather than SIZE, and a `min` rather than a `max` because it is the
// smallest quantum that earns an event — the max* naming above would read
// backwards.
//
// There is deliberately NO envelope arithmetic here, and its absence is the
// point rather than an omission: every constant above bounds claude-derived TEXT,
// whose length claude chooses, against the v2 application-envelope cap. This
// event carries two ints, which cannot grow, so the size argument has no term to
// compute. What needs bounding instead is how OFTEN the event fires —
// system/thinking_tokens is the highest-rate subtype claude puts on this surface
// (~10 lines/turn on the 2026-07-27 measurement, 33 in the committed capture's
// one turn), and a 1:1 mapping would put every one of them on the event stream.
//
// The CEILING is measured, not chosen. In the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json) the turn's 33
// lines fall into four bursts — claude's counter restarts at each inference
// request — totalling 184, 167, 126 and 197 tokens of delta. The smallest is 126,
// so any bound above it lets a whole burst go silent: at 127 burst 3 emits
// nothing while the other three still do, and a visibly-thinking turn falls quiet
// for a stretch. 126 is therefore a hard ceiling from the data.
//
// 64 is roughly half of it, and the multiple is the safety margin exactly as
// maxTaskFieldID's 9x and maxTaskDescription's 32x are: a future burst HALF the
// size of the smallest one ever observed still emits, from a zero residual. It is
// also a real reduction — 33 lines become 8 events on the capture, ~4x — and at
// the 2026-07-27 measurement's ~10 lines/turn and ~20 tokens/line it implies
// roughly 1-2 events on a typical turn. Power of two, matching the package's
// other constants.
const minThinkingTokensPerEvent = 64

// maxRateLimitField caps each claude-authored string on a turnevent.RateLimited —
// Status and LimitType. Applied at CONSTRUCTION, exactly as the caps above are,
// so an oversized payload never enters the event stream, the push queue, or any
// log.
//
// One constant for two fields, as maxTaskFieldID serves three: both are short,
// enum-ish values claude chooses out of a set it does not publish.
//
// The MULTIPLE is wide on purpose. The observed values run 7-9 bytes ("allowed",
// "rejected", "five_hour" across the three captures), so 256 is roughly 28x the
// observation — wider than maxTaskFieldID's 9x, and deliberately so: the value
// set beyond the one benign status is UNMEASURED, so on the side that matters
// there is no distribution to reason about and the binding constraint has to come
// from the envelope instead.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one
// RateLimited carries 256 + 256 = 512 bytes of claude-derived text. That is 0.8%
// of the v2 application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap), an order of magnitude under the scalar
// background-task pair's 7.4% and 6.6%. Escaping is mild for maxUnrecognizedRaw's
// reason. ResetsAt contributes NO term and gets no cap: an int64 cannot grow, and
// its absence here is a statement rather than an oversight.
//
// The amplification from input to retained bytes is linear and near zero:
// rateLimitEventLine holds three scalars and no array, so a 4 MiB line
// (defaultMaxParseBuf) yields at most 512 bytes of retained text plus one
// integer.
//
// There is also no RATE bound, and its absence is deliberate:
// minThinkingTokensPerEvent exists because thinking_tokens fires ~10 times per
// turn, whereas rate_limit_event fires once per RUN (the capture's census) and
// under the gate below a healthy run emits ZERO. There is nothing to bound in
// frequency, so do not go looking for the constant that would. The exposure that
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

// benignRateLimitStatus is the ONE rate_limit_info.status value that produces no
// event. claude emits rate_limit_event once per run whatever the state of the
// usage-limit window, so without this gate a 1:1 mapping would put one "you are
// rate limited" event on every healthy turn.
//
// MEASURED, not chosen: "allowed" in all three captures on record
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json and
// permission_protocol_v2.1.158.json / _v2.1.199.json — three claude versions).
// What is NOT measured is the rest of the value set: no capture of a limit
// actually in force exists, so emitRateLimit is designed for that openly and
// anything else emits.
//
// Matched by byte-exact equality — no trim, no fold, no prefix — which is the
// tolerance three observations earn, exactly as one observation earns it for
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

// maxModelField caps turnevent.ModelAnnounced's Model — claude's announced model
// identifier, off the system/init line. Applied at CONSTRUCTION, exactly as the
// caps above are, so an oversized value never enters the event stream, the push
// queue, or any log.
//
// MEASURED, not chosen. Three observations across two claude versions and three
// spawn shapes: claude-haiku-4-5-20251001 (25 bytes, the committed capture, where
// claude DATED the bare `haiku` alias it was spawned with),
// claude-haiku-4-5 (16 bytes, the permission_protocol_* captures, echoed
// unchanged), claude-sonnet-5 (15 bytes, #1582's recorded run, the machine default
// echoed unchanged). 256 is roughly 10x the observed maximum — near maxTaskFieldID's
// 9x over its 29-byte observation, and for the same reason: room for a naming
// scheme claude has not shipped yet, and still a hard cut on anything that has
// stopped being an identifier.
//
// A separate constant even though it currently equals maxTaskFieldID and
// maxRateLimitField: maxRateLimitField's paragraph applies verbatim — they bound
// different fields for different reasons, and folding them into one would make a
// future change to the task-id budget silently move this one.
//
// Deliberately NOT validModel's 64 (internal/relay/v2session_settings.go), and the
// distinction is the point rather than an oversight. That validator bounds a
// phone-supplied OVERRIDE the daemon accepts, and it enforces a charset besides.
// This cap bounds what claude ANNOUNCES, which the daemon neither controls nor may
// reject: unifying them would make a claude that echoes a longer identifier look
// like a malformed client request.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one
// ModelAnnounced carries 256 bytes of claude-derived text, 0.4% of the v2
// application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap) — half RateLimited's 0.8% and the smallest
// contribution in the family. Escaping is mild for maxUnrecognizedRaw's reason.
// Amplification from input to retained bytes is near zero: systemInitLine holds one
// scalar and no array, so a 4 MiB line (defaultMaxParseBuf) yields at most 256
// retained bytes plus one bool.
//
// No RATE bound, and none is owed: init fires once per TURN, below the ~1-2 per
// turn minThinkingTokensPerEvent's gate already accepts for ThinkingProgress. The
// event is not a droppable delta (the droppable set is assistant_delta only, #610),
// so it holds a queue slot under the same existing backpressure the five sibling
// variants do. Revisit on an OBSERVED rate, as #1385 did.
const maxModelField = 256

// maxModelResolved caps turnevent.ModelOption.ResolvedModel — claude's concrete
// identifier for one entry of the initialize reply's models array. Applied at
// CONSTRUCTION, exactly as the caps above are, so an oversized value never enters
// the event stream, the push queue, or any log.
//
// MEASURED against the committed capture (#1688, claude 2.1.239, six entries):
// the longest resolvedModel is 25 bytes (claude-haiku-4-5-20251001), so 256 is
// roughly 10x the observation — maxModelField's multiple over the same identifier
// shape, and for its reason verbatim: room for a naming scheme claude has not
// shipped yet, and still a hard cut on anything that has stopped being an
// identifier.
//
// A separate constant even though it currently equals maxModelField,
// maxRateLimitField and maxTaskFieldID: maxRateLimitField's paragraph applies
// verbatim — they bound different fields for different reasons, and folding them
// into one would make a future change to one budget silently move this one.
//
// THE ENVELOPE ARITHMETIC, and it is COMPLETE: FOUR terms, every one of them bounded
// by a constant of the daemon's. Per ENTRY the worst case is maxModelResolved +
// maxModelValue + maxModelDisplayName + maxModelEffortLevelCount *
// maxModelEffortLevel = 256 + 256 + 256 + 256 = 1024 bytes of claude-derived text,
// 1.6% of the v2 application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap). 1024 is written here as the MULTIPLICAND rather
// than as anything else, and the factor it is multiplied by is maxModelListEntries —
// which carries the aggregate arithmetic rather than restating it here, exactly as
// maxTaskRosterEntries carries the roster's. A per-entry text cap alone would leave a
// list's total a function of a number claude chooses, which is maxTaskRosterEntries'
// doctrine; the count bound supplies the missing factor, and the one constraint it
// had to satisfy is checked there: the observed list is already SIX entries, so
// maxTaskRosterEntries' 8 could not be reached for by analogy without checking that
// six fits with room left.
//
// THE FOURTH TERM IS A PRODUCT RATHER THAN A CAP, which is worth naming because it
// was the last dimension of this entry left open. An entry carries a level LIST, so
// its budget is a per-element cap times a count bound and not a single number: until
// both existed the honest per-entry figure was 768 + 32N with N claude's to choose,
// and a reader who lands here to learn what an entry costs would have inherited a
// completeness the numbers did not support. That the four terms come out EQUAL is
// maxModelEffortLevelCount's third bullet rather than a coincidence to lean on:
// 8 * 32 = 256 bytes is exactly one string field's cap, which lands this multiplicand
// on maxTaskRosterEntries' own 1024-byte entry unit.
//
// Amplification from input to retained bytes is bounded but NOT near 1 the way the
// scalar targets' is, and this is the one place that difference shows. The cap is
// applied AFTER json.Unmarshal, so a hostile array is materialised in transient
// memory before any of it is bounded — maxTaskRosterEntries' transient paragraph,
// verbatim. defaultMaxParseBuf caps the whole line at 4 MiB before the decoder sees
// it and the densest legal entry is `{}`, so one pathological line is order 100 MB
// of transient — the spike is real, and that constant is the whole of what bounds
// it.
//
// TRANSIENT AND RETAINED ARE TWO DIFFERENT FIGURES HERE, which is what makes the
// bound above the whole story rather than half of it. What is TRANSIENT is the
// unbounded decoded array itself: it lives from json.Unmarshal until emitModelList
// applies the caps, and is garbage from that point on — nothing downstream is ever
// handed it. What is RETAINED is only the CAPPED RESULT: at most
// maxModelListEntries entries, each bounded by the three string caps and by the
// level product above, and maxModelListEntries carries that aggregate rather than
// restating it here. TWO holders retain it, both in cmd/pyry — sessionModelHold
// keeps the decoded value for the session's life (#1840) and emitMapped's eventring
// append keeps the mapped payload per conversation (#1849) — so what outlives the
// line is the product, never the spike.
const maxModelResolved = 256

// maxModelValue caps turnevent.ModelOption.Value. maxModelResolved's paragraph
// applies verbatim — the measurement, the multiple, the separate-constant rule and
// the envelope arithmetic are ONE argument covering all three fields, stated once
// there rather than transcribed three times.
//
// The one sentence that is this field's own: Value is not a dated identifier and
// not even always a model name — an alias (sonnet), a bracketed variant
// (claude-fable-5[1m], the capture's longest at 18 bytes), or default — so what
// this cap bounds is claude's argument vocabulary rather than its naming scheme.
const maxModelValue = 256

// maxModelDisplayName caps turnevent.ModelOption.DisplayName. maxModelResolved's
// paragraph applies verbatim, for maxModelValue's reason.
//
// The one sentence that is this field's own: DisplayName is PROSE rather than an
// identifier ("Default (recommended)", the capture's longest at 21 bytes), so it
// has the weakest claim of the three to a naturally bounded length. That argues for
// keeping it level with its two siblings, NOT for giving it a smaller cap: a label
// claude lengthens is not a malformation, and a tighter budget would cut a
// legitimate one first while saving 0.x% of an envelope this list does not reach.
const maxModelDisplayName = 256

// maxModelEffortLevel caps ONE ELEMENT of turnevent.ModelOption.EffortLevels, not
// the list. maxModelResolved's paragraph applies verbatim for maxModelValue's
// reason — the separate-constant rule and the construction-time application are one
// argument covering all four fields, stated once there rather than transcribed a
// fourth time. A power of two, matching the family.
//
// MEASURED against the same capture: the longest level is 6 bytes (medium), so 32
// is roughly 5x the observation — a THINNER multiple than the three strings' 10x,
// for two reasons of which the second is the real one. A level is the most
// constrained shape in the family, a token from a menu claude publishes and a client
// renders as a control's options, not prose like DisplayName. And this cap is paid
// PER ELEMENT against a count claude chooses, so generosity here multiplies where
// the siblings' does not. 32 still admits every plausible future spelling —
// ultrathink is 10 bytes, extended-thinking 17 — which is the property that matters:
// a level claude adds later must DECODE, not arrive mangled.
//
// NOT sized to internal/relay's validEffort, whose CLOSED enum's longest member is
// the same 6 bytes. That enum bounds a PHONE-supplied override on an INBOUND path
// and is deliberately a different rule; sizing this cap to it would be applying the
// inbound rule outbound by the back door, and the level claude adds next is what
// both mistakes lose. See turnevent.ModelOption.EffortLevels, where the same
// separation is stated for the consumer.
//
// It bounds the ELEMENT and maxModelEffortLevelCount bounds the COUNT, which is what
// turns the multiplication sentence above from a warning into an arithmetic term:
// this cap is multiplied by a KNOWN factor now rather than by a number claude
// chooses, so raising it moves a per-entry budget a reader can compute instead of
// reopening one that cannot be computed at all. 32 * 8 = 256 bytes is that budget,
// and maxModelEffortLevelCount carries its derivation.
const maxModelEffortLevel = 32

// maxModelEffortLevelCount caps how many effort levels ONE
// turnevent.ModelOption retains — the COUNT maxModelEffortLevel's per-element cap
// cannot supply, and the last dimension of a model entry that was a function of a
// number claude chooses. It is the family's third CARDINALITY bound and the first
// that is per-ENTRY: maxTaskRosterEntries and maxModelListEntries bound a whole
// event's entries, this one bounds a list INSIDE one entry. Applied at CONSTRUCTION
// like every cap above, so an oversized payload never enters the event stream, the
// push queue, or any log. Overflow is REPORTED — turnevent.ModelOption.TruncatedFields
// names "effort_levels" — rather than silent, which is the property that makes a
// cardinality bound honest: a client shown three of claude's ten levels as a complete
// menu is a lie rather than a gap.
//
// THE NAME ENDS IN Count DELIBERATELY, and the plural maxModelEffortLevels was
// rejected rather than not considered. It would differ from maxModelEffortLevel by
// ONE character, both are int, and both bound the same field, so swapping them at a
// call site COMPILES and neither vet nor a type error would say so: an 8-byte element
// cap would cut `medium`, claude's ordinary output, on every child, and a 32-level
// count cap would bound nothing worth bounding. The Entries suffix the two other
// cardinality caps use is not available without maxModelEffortLevelEntries, which is
// worse. maxTaskPatch's separate-constant paragraph is this same instinct one step
// earlier — prevent a silent coupling before it can happen.
//
// The arithmetic, in maxTaskRosterEntries' style:
//
//   - The count itself: the observed list is FIVE levels — low, medium, high, xhigh,
//     max, in the committed capture (claude 2.1.239), identically in all three of
//     #1763's arms — so 8 is 1.6x the observation, essentially maxModelListEntries'
//     own 1.67x over six entries. A THINNER multiple than the text caps' 10x for
//     maxModelListEntries' stated reason: a cardinality overflow is REPORTED, and the
//     reported failure mode is what buys the thinner margin. Three slots above what
//     claude sends, so the level claude adds next is carried rather than cut.
//   - A power of two, matching every constant in this family except
//     maxModelListEntries, whose own doc explains why it alone is decimal.
//   - The LIST's budget: 8 * 32 = 256 bytes, exactly one string field's cap. That is
//     what puts maxModelResolved's per-entry multiplicand on 4 * 256 = 1024 bytes —
//     maxTaskRosterEntries' per-entry unit, to the byte — so the family has ONE entry
//     unit across both of its aggregate variants. Presented as the number both landed
//     on rather than as a rule the next variant must satisfy: the two entries have
//     different field sets (256 + 256 + 512 against 4 * 256), so the agreement is
//     arithmetic and a variant that re-derives its own unit is not violating anything.
//   - The aggregate is maxModelListEntries', which carries the product, the ceiling it
//     is measured against and why that ceiling. Cross-referenced rather than restated,
//     exactly as maxModelResolved does.
//
// NOT sized to the five levels claude sends, and NOT to internal/relay's validEffort
// whose closed enum has the same cardinality. A cap AT the observation fires the
// moment claude ships a sixth level, which is the failure maxModelListEntries' NOT 8
// paragraph rejects by name at its own scale — a cap firing on claude's ORDINARY
// output. The validEffort separation is maxModelEffortLevel's paragraph verbatim: a
// CLOSED enum bounding a PHONE-supplied override on an INBOUND path is deliberately a
// different rule, and sizing an outbound cap to it in either direction is applying the
// inbound rule by the back door.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened — maxTaskRosterEntries' accepted trade, with
// maxModelResolved's amplification paragraph bounding the exposure. This cap bounds
// what is RETAINED and what crosses the wire, which is the property that matters.
const maxModelEffortLevelCount = 8

// maxModelListEntries caps how many entries a turnevent.ModelList carries. It is
// the second application of maxTaskRosterEntries' doctrine and the constant that
// supplies the factor maxModelResolved's per-entry budget was missing: a per-entry
// text cap alone leaves a list's total size a function of a number claude chooses.
// Applied at CONSTRUCTION like every cap above, so an oversized payload never
// enters the event stream, the push queue, or any log. Overflow is REPORTED
// (turnevent.ModelList.DroppedModels), not silent — the one property that makes a
// cardinality bound honest, because a client showing six of claude's forty models
// as a complete menu is a lie rather than a gap.
//
// The number is DERIVED, and the derivation is written out because it is decimal
// rather than a power of two and a reader will ask why:
//
//   - Multiplicand: 1024 bytes per entry (maxModelResolved + maxModelValue +
//     maxModelDisplayName + maxModelEffortLevelCount * maxModelEffortLevel),
//     inherited from maxModelResolved's doc, which states it for this constant rather
//     than leaving it to be re-derived. All four terms are bounded by a constant of
//     the daemon's, so it is the per-entry TOTAL and not a floor on it.
//   - Ceiling: maxUnrecognizedRaw's whole-line 16 KiB, at 5/8 of it. That is the
//     package's ordering one level up — a whole KNOWN event must not approach the cap
//     on an entire UNKNOWN line — measured retained-against-retained, which is the
//     comparison the rule is about. 16384 * 5/8 = 10240, and 10240 / 1024 = 10 exactly.
//   - Floor: the observed list is SIX entries (the committed capture, claude
//     2.1.239), the check maxModelResolved's doc requires. 10 leaves four slots.
//   - Product: 10 * 1024 = 10240 bytes = 10 KiB, ten entries at the family's
//     one-kibibyte entry unit. 15.6% of the v2 application-envelope cap of 65519 bytes
//     (docs/protocol-mobile.md § Application-envelope size cap), which reads alongside
//     the family's 7.4%, 6.6% and 12.5%. Escaping is mild for maxUnrecognizedRaw's
//     reason verbatim: these are JSON string values, so the growth is quotes and
//     backslashes rather than a \u00XX expansion of every byte. Pathological all-quote
//     content roughly doubles it — ~20 KB, ~31% of the envelope. That doubling is
//     stated against the ENVELOPE only: the maxUnrecognizedRaw comparison above is on
//     RETAINED bytes, and mixing the two would measure a doubled wire figure against a
//     retained cap.
//
// THE CEILING MOVED AND THE PRODUCT GREW, which is written out because a reader
// re-running the arithmetic this doc used to carry will find it no longer closes. The
// old ceiling was 8192, and 8192 was never DERIVED as one: it is maxTaskRosterEntries'
// PRODUCT, whose doc NOTICED that 8 * 1024 lands on half of maxUnrecognizedRaw, and
// this constant inherited the noticed landmark as a constraint. What actually survives
// is the rule the roster stated, which fixes no particular fraction — so the fraction
// is stated PER SHAPE: the roster reads 1/2, this list reads 5/8. THE NEXT AGGREGATE
// VARIANT RE-DERIVES ITS OWN FRACTION rather than inheriting 5/8, because inheriting a
// noticed landmark as a constraint is exactly the mistake this paragraph undoes.
//
// It HAD to move, and the proof is short. Both factors have a doctrine floor. A level
// cap at claude's observed five fires on ORDINARY output, so maxModelEffortLevelCount
// is at least 6; an entry cap of 8 is rejected by name below, so this constant is at
// least 10. The smallest product consistent with both is 10 * (768 + 6 * 32) = 9600
// bytes, already above 8192. No pair of caps this family's own doctrine permits fits
// the inherited ceiling, so the ceiling was the only lever left rather than one of
// three. The two alternatives are closed where they live: a 10-byte per-level cap is
// what 8192 would need at ten entries and five levels, and maxModelEffortLevel argues
// 32 against spellings claude has not shipped (ultrathink is 10 bytes,
// extended-thinking 17); and RAISING maxUnrecognizedRaw would loosen the bound on an
// UNKNOWN line to make room for a KNOWN one, inverting the ordering rule it exists to
// state.
//
// WHICH NUMBERS MOVED: the ceiling, from 8192 to 5/8 of 16 KiB, and the multiplicand,
// from three strings to four terms. maxModelEffortLevel (32) did not — its own doc's
// argument is unchanged, and a thinner per-level cap is the alternative rejected
// above. This constant did not either: the NOT 8 paragraph rejects a smaller count on
// evidence that has not changed, and trading a bound on the dimension claude has never
// inflated (five levels of eight) for a tighter bound on the dimension claude is
// closest to (six entries of ten) is the wrong direction.
//
// NOT 8, borrowed from maxTaskRosterEntries by analogy. Two slots above an
// observation of six is not room, and the failure that buys is a cap firing on
// claude's ORDINARY output — the menu everyone sees, cut, on every child.
//
// NOT a power of two, unlike every other constant in this family, because neither
// neighbouring power fits: 8 is the too-tight case above, and 16 lands the product at
// 16384 bytes, EXACTLY maxUnrecognizedRaw's whole-line 16 KiB, so a KNOWN event would
// REACH the cap on an entire UNKNOWN line rather than staying below it. The decimal is
// what satisfies both binding constraints at once.
//
// The multiple over the observation is thinner than the text caps' 10x, and that is
// deliberate: a text cap's overflow mangles an identifier in place, while this one
// shortens a list and says BY HOW MANY, so a client can render "6 of 40" rather
// than a wrong menu. The reported failure mode is what buys the thinner margin.
//
// 1024 counts claude-derived text only. An entry can also carry up to four
// DAEMON-authored names in TruncatedFields (~55 bytes), which claude cannot inflate
// and which maxTaskRosterEntries' arithmetic likewise excludes.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened — maxTaskRosterEntries' accepted trade,
// with maxModelResolved's amplification paragraph bounding the exposure. This cap
// bounds what is RETAINED and what crosses the wire, which is the property that
// matters.
const maxModelListEntries = 10

// maxSlashCommandName caps turnevent.SlashCommand.Name — claude's name for one entry
// of the initialize reply's commands array, the workspace's slash-command inventory.
// Applied at CONSTRUCTION, exactly as the model caps above are, so an oversized value
// never enters the event stream, the push queue, or any log.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): fifty-one entries, `name` present and non-empty on every
// one, longest 24 bytes (fewer-permission-prompts), mean 9.69, median 8, 494 bytes in
// total. So 256 is roughly 10.7x the observation — maxModelField's and
// maxModelResolved's multiple over the same identifier shape, and for their reason
// verbatim: room for a naming scheme claude has not shipped yet, and still a hard cut
// on anything that has stopped being a name.
//
// A separate constant even though it currently equals maxModelResolved, maxModelField
// and maxModelValue: maxRateLimitField's paragraph applies verbatim — they bound
// different fields for different reasons, and folding them into one would make a
// future change to one budget silently move this one.
//
// A BYTE CUT AND NOTHING ELSE. One captured name is outside [a-z0-9-]
// (__remote-workflow), the committed proof that no charset may be assumed, so #1600's
// verbatim rule governs here as it does for the model strings: no lowercasing, no
// trimming, no charset filtering, and no leading "/" added or removed. See
// turnevent.SlashCommand.Name, which states the same rule for the consumer.
//
// THE PER-ENTRY TERM, stated so the field slices add to ONE derived figure rather than
// each re-deriving it, and COMPLETE since #1825: one entry costs at most
// maxSlashCommandName + maxSlashCommandArgumentHint + maxSlashCommandDescription +
// maxSlashCommandAliasCount * maxSlashCommandAlias = 256 + 256 + 256 + 8 * 64 = 1280
// bytes of workspace-derived text. IT IS A SUM PLUS A PRODUCT, which is what the fourth
// field changed about the SHAPE of this term and not only its value: aliases are a
// nested array, so they add a third size dimension — entries x aliases x alias length —
// that a per-field byte cap alone does not cover, and maxModelResolved's own term has
// carried the same shape since #1821. The field this paragraph named as the one that
// would dominate the budget was the description, and it does — it arrived in #1904 with
// a cap of its own, whose doc carries that cap's derivation and the arithmetic this
// term hands forward. The argument hint joined in #1957 and costs the same 256 while
// contributing 530 bytes across the whole capture against the descriptions' 10,580,
// which is why the term grew by half again while the observation barely moved; aliases
// joined in #1825 and cost 512 while contributing 64, the same disproportion one step
// further. THE AGGREGATE IS SETTLED SINCE #1826 and no factor is outstanding. The one
// that was missing here was the entry COUNT alone, and maxSlashCommandListEntries
// supplied it exactly as maxModelListEntries supplied maxModelResolved's: 128 entries
// against this 1280-byte term. What that constant does NOT do, and what makes this
// aggregate unlike maxModelResolved's, is land under any ceiling the package cites —
// its own doc records which convention it spent to keep the cap off claude's ordinary
// output, and that arithmetic is cross-referenced rather than restated here. No field
// slice remains outstanding either.
//
// TRANSIENT AND RETAINED ARE TWO DIFFERENT FIGURES HERE TOO, which is what makes this
// bound the whole story rather than half of it. What is TRANSIENT is the unbounded
// decoded array, bounded one level up by defaultMaxParseBuf's 4 MiB whole-line cap
// before the decoder ever sees it — commandEntryLine's own paragraph. What is
// RETAINED is only this capped copy inside the emitted turnevent.SlashCommandList,
// and cmd/pyry's sessionSlashCommandHold now keeps THAT for the session's life (#2004),
// replacing it on each report rather than accumulating. So this cap no longer differs
// from maxModelResolved on the point it used to: what one child's initialize reply can
// hold onto is a capped list either way, and the retained figure here is bounded by
// this term times maxSlashCommandListEntries rather than by an event's lifetime.
const maxSlashCommandName = 256

// maxSlashCommandDescription caps turnevent.SlashCommand.Description — claude's own
// description of one entry of the initialize reply's commands array (#1904). Applied
// at CONSTRUCTION in emitSlashCommandList, exactly as every cap above is, so an
// oversized value never enters the event stream, the push queue, or any log.
//
// THE DOC-SHAPE PRECEDENT IS maxTaskRosterDescription, not the identifier-cap family
// this constant's neighbour belongs to. That constant bounds the same SHAPE under the
// same pressure — a prose description multiplied by a count claude chooses — and its
// two arguments are the two this derivation weighs, one pushing down and one up.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): fifty-one entries, `description` present and non-empty on
// every one, longest 1145 bytes (dataviz), then 1078, 1075, 1023 and 797, mean 207.5,
// median 69, 10,580 bytes in total. 10 are over 256 bytes, 16 over 128, 28 over 64.
// The names, for contrast, total 494 with a 24-byte longest. So 256 is ~1.2x the mean
// and ~3.7x the median — a far thinner multiple than maxSlashCommandName's 10.7x over
// the same capture, and the thinness is the decision rather than an accident of it.
//
// WHY THINNER, and it is maxTaskRosterDescription's MULTIPLICATION argument with the
// multiplier worse: a multiplied field earns a smaller unit budget than the same field
// carried once. The roster multiplies its description by 8; this list multiplies by
// claude's observed 51. That pushes BELOW the roster's 512, and 256 is where it lands.
//
// WHY NOT LOWER, and here maxTaskRosterDescription's ROLE argument INVERTS rather than
// carrying: a roster description's authoritative full-length copy already crossed the
// wire on the BackgroundTaskStarted its task_id joins back to, so a cut there loses
// nothing a consumer holding that event cannot recover. A CUT SLASH-COMMAND
// DESCRIPTION IS RECOVERABLE FROM NOTHING — no second copy of it exists on any lane.
// The bound on how far that pushes is the named consumer, pyrycode-desktop#694's
// type-ahead, which renders one row per command as a name, an argument hint and a
// description: 128 would cut 16 of the capture's 51 and 64 would cut 28, so over half
// the menu would arrive truncated for the one consumer the field exists for. 256 cuts
// 10.
//
// THE MULTIPLICAND IS THE SUM OF CAPS PLUS ONE PRODUCT, which is how maxModelListEntries
// derives its own and what makes the figure a worst case rather than a description of
// one capture: maxSlashCommandName + maxSlashCommandArgumentHint +
// maxSlashCommandDescription + maxSlashCommandAliasCount * maxSlashCommandAlias =
// 256 + 256 + 256 + 8 * 64 = 1280 bytes since #1825 added the fourth term. It became a
// sum plus a product rather than a longer sum because the fourth field is a NESTED
// ARRAY — see maxSlashCommandName's own statement of the term, which owns the shape.
//
// ROUNDNESS WAS A TWO-TERM TIEBREAKER AND THE LATER TERMS DISSOLVE IT, which is
// recorded rather than repaired. This paragraph used to read the sum's 512 as a unit a
// reader can hold — the virtue maxTaskRosterEntries' arithmetic paragraph claims by
// name for its own 1024 — and to note that no other candidate weighed here landed on
// one. Re-derived at three terms the candidates were 64 -> 576, 128 -> 640, 256 -> 768
// and 512 -> 1024, so the REJECTED 512 candidate became the round one and the
// tiebreaker inverted. At four terms it is dissolved outright: the sum is 1280 and the
// candidates around it land nowhere in particular. It does NOT re-open this constant's
// value: 256 was decided on the multiplication and role arguments below, which neither
// later term touches, with roundness standing beside them rather than under them.
// #1825's fourth term was NOT chosen to make the sum come out even — its own two
// constants record that the round candidate (a count cap of 4, a term of 256, a sum of
// 1024) was available and declined on an argument about what a cut alias costs.
//
// THE CEILING IS maxUnrecognizedRaw's whole-line 16 KiB, and 8192 is deliberately NOT
// cited as one. maxModelListEntries' doc records why: 8192 is maxTaskRosterEntries'
// PRODUCT, whose doc merely NOTICED that it lands on half of maxUnrecognizedRaw, and
// inheriting a noticed landmark as a constraint is the mistake that paragraph undoes.
// What survives is the RULE — a whole KNOWN event must not approach the cap on an
// entire UNKNOWN line, measured retained-against-retained — with the fraction stated
// PER SHAPE: the roster reads 1/2 and the model list 5/8.
//
// THIS SHAPE HAS NO FRACTION, and that is #1826's answer rather than a gap this doc
// left open. What this paragraph used to owe was arithmetic handed forward: at the
// 1280-byte per-entry term, 16384 * 1/2 = 8192 leaves 6 entries and 16384 * 5/8 = 10240
// leaves 8, and #1826 was to pick the fraction and the count from those two. IT PICKED
// NEITHER, and the candidate set is kept here rather than deleted because a reader
// re-deriving it will reach those same two numbers and needs to be told they were
// weighed and declined. Both CUT claude's ordinary output — a cap of 6 or 8 shortens
// the capture's fifty-one-entry menu by 43 or 45 — which maxModelListEntries' NOT 8
// paragraph rejects by name. maxSlashCommandListEntries took the OBSERVATION as its
// base instead of this worst-case term, landed on 128, and records there which
// convention it had to spend to do it; no fraction of maxUnrecognizedRaw survives the
// move, so none is stated for this shape. The term above is still the one derived
// figure a later slice multiplies, and it is why the term is stated here at all.
//
// WHAT THE OBSERVED CAPTURE COSTS AT THIS CAP is the sharpest figure in the
// derivation, and it is computed THROUGH truncateField — byte cut, then the
// empty-replacement scrub — rather than as a naive byte-cut sum. Name, argument hint,
// description and aliases across all 51 entries retain 6,711 bytes, which is under 8192
// AND under 10240, so today's real workspace fits inside BOTH established fractions
// after the cut. It happens to EQUAL the naive byte-cut sum at these caps, no captured
// value being cut mid-rune at any of them and no entry's alias list reaching its count
// cap, and saying so is cheaper than leaving a reader to wonder why the two agree. The
// fourth field moved that figure by 64 bytes, the whole of its captured footprint,
// because nothing about it is cut at all. No larger candidate for THIS field clears
// both: 512 retains 8,827 and clears only 5/8.
//
// A CAP FIRING ON CLAUDE'S ORDINARY OUTPUT IS UNAVOIDABLE FOR THIS FIELD, and saying
// so is honest where maxModelListEntries' NOT 8 paragraph could reject exactly that
// outcome for the COUNT. The descriptions alone total 10,580 bytes — already past
// 10240 and past 8192 before a single name is counted — and one of them is 1145 bytes
// by itself. It is stated against the FRACTION and not against the whole 16 KiB
// deliberately: uncut name+hint+description+aliases is 11,668, over both established
// fractions but UNDER 16384, so "past the whole ceiling uncut" would be false and would
// rest this argument on a premise a reader can knock down. The third field moved that
// figure and the fourth moved it again by its whole 64-byte footprint; the premise is
// re-checked at each field rather than assumed, this sentence being built to be
// knock-down-able and a stale number being exactly the knock-down. TruncatedFields is what makes the cut
// honest rather than silent.
//
// MID-RUNE IS ON THE LIVE PATH for this field where it is a corner case for the name:
// 14 of the 51 descriptions carry non-ASCII and no name does, so truncateField's
// empty-replacement DELETION — a cut value landing 1-3 bytes under the limit — is
// ordinary here. At 256 no entry in the capture is mid-rune reachable at all; at 128
// dataviz is (losing 1 byte) and at 64 artifact-capabilities is (losing 2).
//
// Escaping is mild for maxUnrecognizedRaw's reason, verbatim: these are JSON string
// values, so the growth is quotes and backslashes rather than a \u00XX expansion of
// every byte. The one wrinkle measured here is narrow and must not be widened into a
// claim about control characters: claude-api's description carries two 0x0a bytes, and
// 0x0a is the ONLY sub-0x20 byte anywhere across the entries' string fields
// (protocol.SlashCommand's doc carries that measurement). They arrive pre-escaped as
// two printable bytes each, and they are NOT stripped — #1600's verbatim rule governs
// and the client's render boundary owns sanitization.
//
// A separate constant even though it currently equals maxSlashCommandName — and
// maxModelResolved, maxModelField and maxModelValue: maxRateLimitField's paragraph
// applies verbatim, they bound different fields for different reasons, and folding
// them into one would make a future change to one budget silently move this one. This
// doc names its peers; theirs are left as written, #1877 having added
// maxSlashCommandName without reopening them either.
const maxSlashCommandDescription = 256

// maxSlashCommandArgumentHint caps turnevent.SlashCommand.ArgumentHint — claude's
// synopsis of what one entry of the initialize reply's commands array takes AFTER its
// name (#1957). Applied at CONSTRUCTION in emitSlashCommandList, exactly as every cap
// above is, so an oversized value never enters the event stream, the push queue, or
// any log.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): fifty-one entries, `argumentHint` PRESENT on every one and
// absent from none — but 33 of the 51 present-and-EMPTY, and 18 carrying text. Longest
// 121 bytes (auto-mode-setup, the only hint carrying non-ASCII), then 77, 55, 44, 41.
// Over the 18 non-empty: mean 29.4, median 17.5 — an EVEN count, so that median is the
// mean of the 9th and 10th of the sorted 18 (16 and 19) and not the 10th alone. Over
// all 51: mean 10.4, median 0. Seven exceed 24 bytes, five exceed 32, two exceed 64 and
// NONE exceeds 128. 530 bytes in total, against the names' 494 and the descriptions'
// 10,580.
//
// THE MULTIPLE IS OVER THE LONGEST, AND NAMING THAT BASE IS THIS DOC'S OWN OBLIGATION
// rather than a courtesy: 256 is 2.1x the longest observed hint, maxSlashCommandName's
// base and stated as its base. That constant takes its 10.7x over the LONGEST and
// maxSlashCommandDescription its ~1.2x and ~3.7x over the MEAN and MEDIAN OF ALL 51;
// neither had to flag the choice, because `name` and `description` are non-empty on
// every entry and the two populations coincide there. HERE THEY DO NOT. "Median 17.5"
// and "median 0" are both true of this field, and over all 51 the median is 0 — so no
// multiple over THAT base exists at all. The other bases are stated so a reader
// re-deriving one does not reach a different number and think this doc wrong: 8.7x the
// non-empty mean, 14.6x the non-empty median, 24.6x the all-51 mean. A figure quoted
// beside the description's 3.7x without its base compares two different measurements.
// maxSlashCommandAlias INHERITED this convention (#1825) and discharged it differently:
// `aliases` is absent on 42 of 51, so its own populations are even further apart than
// this field's — but the base it names is neither of them. It takes its multiple over
// the captured NAMES' longest, on a measured argument that an alias is drawn from the
// name population, and states its own two bases beside it.
//
// WHY NOT 128, and this is the decision rather than an accident of it: 121 is 94.5% of
// 128, so the cap would sit inside the observation's own noise. What 128 does NOT cost
// is any captured value — no hint is cut at 128 either, so the two candidates are
// INDISTINGUISHABLE on the observation and separable only on headroom. A hint is an
// argument synopsis, a function of how many flags a workspace command takes, so it has
// a growth direction a NAME does not, and auto-mode-setup's 121 bytes are the committed
// evidence that workspace authors write long ones. At 128 the claim that this cap does
// not fire on claude's ordinary output is one a point release can falsify with seven
// bytes.
//
// WHY NOT 64 OR BELOW is where this field's argument DIVERGES from the description's
// rather than copying it: at 64 two captured hints are cut, at 32 five and at 24 seven,
// so the cap fires on claude's ordinary output. maxSlashCommandDescription's doc accepts
// exactly that outcome for itself and says so honestly — but only because it has no
// alternative, its descriptions totalling 10,580 bytes with one at 1145 by itself. This
// field HAS an alternative, so accepting the same outcome here would be a choice rather
// than a necessity.
//
// WHY NOT LARGER is maxSlashCommandDescription's MULTIPLICATION argument verbatim: a
// multiplied field earns a smaller unit budget than the same field carried once, and
// this list multiplies by claude's observed 51. What does NOT carry is that constant's
// ROLE argument, the one that kept the description from going lower — this field's whole
// observed footprint is 530 bytes across the capture, ONE TWENTIETH of the
// descriptions', so nothing in the observation asks for more room than 2.1x the longest.
//
// MID-RUNE IS UNREACHABLE ON THE LIVE PATH at this cap, which is the property
// maxSlashCommandDescription's doc records for itself at its own: no captured hint is
// cut at 256, so none is mid-rune reachable, and auto-mode-setup's is the only captured
// value for which the question could arise at all. The cut SEMANTICS are inherited
// whole and unchanged — truncateField's <= boundary and its empty-replacement DELETION
// still govern a constructed value landing 1-3 bytes short.
//
// A separate constant even though it currently equals maxSlashCommandName and
// maxSlashCommandDescription — and maxModelResolved, maxModelField and maxModelValue:
// maxRateLimitField's paragraph applies verbatim, they bound different fields for
// different reasons, and folding them into one would make a future change to one budget
// silently move this one. The obligation is the sharper here for the numbers agreeing
// three ways on three adjacent fields of the SAME entry.
const maxSlashCommandArgumentHint = 256

// maxSlashCommandAlias caps ONE ELEMENT of turnevent.SlashCommand.Aliases, not the
// list — claude's alternative name for one entry of the initialize reply's commands
// array (#1825). Applied at CONSTRUCTION in emitSlashCommandList, exactly as every cap
// above is, so an oversized value never enters the event stream, the push queue, or any
// log. maxModelEffortLevel is the DOC-SHAPE precedent rather than the three sibling
// string caps: it is the family's only other cap on an element of a list INSIDE a list
// entry, and it bounds the same third size dimension.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): fifty-one entries, NINE carrying `aliases` and forty-two
// omitting the key, ZERO carrying an empty array. Eleven aliases in total, 64 bytes —
// against the names' 494, the hints' 530 and the descriptions' 10,580, the cheapest
// field of the four by an order of magnitude. Longest 9 bytes (proactive), shortest 3,
// mean 5.82, median 5. NONE carries non-ASCII.
//
// THE MULTIPLE IS OVER THE CAPTURED NAMES' LONGEST AND NOT OVER THIS FIELD'S OWN,
// which is maxSlashCommandArgumentHint's name-the-base obligation inherited with the
// problem different rather than worse. Both bases are stated so a reader re-deriving
// one does not reach a different number and think this doc wrong: 64 is 2.67x the
// longest captured NAME (24, fewer-permission-prompts) and 7.1x the longest captured
// ALIAS (9), also 11x the alias mean and 12.8x the alias median. The name witness is
// named because this doc INVITES re-derivation: __remote-workflow, which carries the
// charset fact everywhere else in this file, is only 17 bytes and would put a
// re-deriving reader at 3.76x.
//
// WHY THE NAME POPULATION IS THE RIGHT BASE, and it is MEASURED rather than assumed
// because the obvious intuition is false. "An alias is a short form of the name" would
// make this field's own longest the base — but THREE of the eleven captured aliases are
// strictly LONGER than the name they alias (checkup for doctor, proactive for loop,
// settings for config) and THREE more TIE it (routines for schedule, reset for clear,
// stats for usage), so six of the eleven are at least as long as the name they stand
// in for. An alias is simply another name for the command, written by the same
// workspace author in the same file, and one captured alias (name, for rename) is a
// token that could equally have been a command's own name. So the population an alias
// is drawn from is the NAME population, whose longest member the capture puts at 24
// bytes, and a cap that admits any name-shaped alias is what this field needs.
//
// WHY NOT 32: it is 3.6x this field's own longest but only 1.33x the longest captured
// name, so the cap would sit inside the noise of the population aliases come from —
// maxSlashCommandArgumentHint's WHY NOT 128 argument, with fewer-permission-prompts'
// 24 bytes as the committed evidence that this vocabulary reaches into that range. 16
// cuts a name-shaped alias outright.
//
// WHY NOT 256, maxSlashCommandName's own number: that cap's 10.7x is paid ONCE per
// entry, and this one is paid up to maxSlashCommandAliasCount times, so copying it
// would apply a singly-multiplied budget to a doubly-multiplied field.
// maxSlashCommandDescription's MULTIPLICATION argument one dimension further down, and
// maxModelEffortLevel's position in its own family: this is the SMALLEST unit budget in
// the slash-command entry, for the field with the smallest observed footprint and the
// most multiplication.
//
// MID-RUNE IS UNREACHABLE ON THE LIVE PATH at this cap, and by a wider margin than any
// sibling's: no captured alias carries non-ASCII at all and the longest is 9 bytes
// against 64, so no captured value is cut and none could be cut inside a rune. The cut
// SEMANTICS are inherited whole and unchanged — truncateField's <= boundary and its
// empty-replacement DELETION still govern a constructed value landing 1-3 bytes short.
//
// It bounds the ELEMENT and maxSlashCommandAliasCount bounds the COUNT, which is what
// turns the multiplication sentence above from a warning into an arithmetic term: this
// cap is multiplied by a KNOWN factor rather than by a number claude chooses, so
// raising it moves a per-entry budget a reader can compute. 64 * 8 = 512 bytes is that
// budget, and maxSlashCommandAliasCount carries its derivation.
//
// A separate constant even though it equals no sibling's number today: maxRateLimitField's
// paragraph applies verbatim — different fields, different reasons, and folding them
// would make a future change to one budget silently move another.
const maxSlashCommandAlias = 64

// maxSlashCommandAliasCount caps how many aliases ONE turnevent.SlashCommand retains —
// the COUNT maxSlashCommandAlias's per-element cap cannot supply, and the last
// unbounded dimension of a command entry (#1825). It is the family's second per-ENTRY
// cardinality bound, maxModelEffortLevelCount being the first; maxTaskRosterEntries and
// maxModelListEntries bound a whole event's entries where these two bound a list INSIDE
// one entry. Applied at CONSTRUCTION like every cap above. Overflow is REPORTED —
// turnevent.SlashCommand.TruncatedFields names "aliases" — rather than silent, which is
// the property that makes a cardinality bound honest.
//
// THE NAME ENDS IN Count DELIBERATELY, maxModelEffortLevelCount's paragraph verbatim
// and for its reason: the plural maxSlashCommandAliases would differ from
// maxSlashCommandAlias by ONE character, both are int, both bound the same field, so
// swapping them at a call site COMPILES and neither vet nor a type error would say so.
// A 64-alias count cap would bound nothing worth bounding and an 8-byte element cap
// would cut `proactive`, claude's ordinary output, on every child.
//
// The arithmetic, in maxModelEffortLevelCount's style:
//
//   - The count itself: SEVEN of the capture's nine alias-carrying entries hold ONE
//     alias and two hold two, so the observed maximum is 2 and over all fifty-one the
//     mean is 0.216 and the median 0. 8 is 4x that maximum, six slots above claude's
//     observed output.
//   - A power of two, matching every constant in this family except maxModelListEntries,
//     whose own doc explains why it alone is decimal.
//   - The LIST's budget: 8 * 64 = 512 bytes. That is TWICE one string field's cap,
//     where maxModelEffortLevelCount's product landed on exactly one — the two variants
//     have different field sets and different multiplicands, and a variant re-deriving
//     its own unit is not violating anything.
//   - The aggregate is maxSlashCommandDescription's, which carries the per-entry term
//     and the candidate fractions #1826 weighed and declined; that slice's own constant,
//     maxSlashCommandListEntries, carries the count it took instead. Cross-referenced
//     rather than restated.
//
// A THICKER MULTIPLE THAN THE FAMILY'S CARDINALITY CONVENTION, and that is the decision
// rather than an accident of it. maxModelListEntries takes 1.67x and
// maxModelEffortLevelCount 1.6x; applied literally here that convention lands on 3 or 4.
// Two arguments push past it, neither of which the existing cardinality caps have.
// FIRST, those bound vocabularies CLAUDE controls — a published effort menu, a
// published model list — while this bounds a list a WORKSPACE author writes in a
// repository, so its growth direction is not bounded by anything claude ships. A
// repository giving one command r, rev, review and code-rev is unremarkable and a cap
// of 4 cuts it, which is maxModelListEntries' NOT 8 failure at this scale: a cap firing
// on ordinary output. SECOND, and this is maxSlashCommandDescription's ROLE argument
// pointing the OTHER way for once: a cut description costs a truncated row, where a cut
// ALIAS costs a working command greyed out in the consumer's menu, because the alias
// the consumer was matching against is the one that was cut. That is the exact failure
// turnevent.SlashCommand.Aliases exists to prevent, so the dimension's own cap must not
// be the thing that causes it.
//
// WHY NOT 16: the product doubles for headroom nothing in the observation asks for, on
// the field with the smallest observed footprint in the entry — 64 bytes across the
// whole capture.
//
// THE PRODUCT WAS NOT CHOSEN TO MAKE THE PER-ENTRY SUM COME OUT ROUND, which
// maxSlashCommandDescription's roundness paragraph asks of this slice by name. 64 and 8
// are each derived above on their own populations and land on 512, putting the term at
// 1280. The round candidate WAS available and is declined rather than missed: a count
// cap of 4 gives 256 and a sum of 1024, and it is rejected on the ROLE argument above
// and not on the arithmetic.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened — maxTaskRosterEntries' accepted trade, with
// maxModelResolved's amplification paragraph bounding the exposure and
// defaultMaxParseBuf's 4 MiB whole-line cap bounding the input. This cap bounds what is
// RETAINED, which is the property that matters.
const maxSlashCommandAliasCount = 8

// maxSlashCommandListEntries caps how many entries a turnevent.SlashCommandList carries
// (#1826) — the LAST unbounded dimension of the initialize reply's commands array, and
// the factor the four per-field caps above cannot supply between them: the array's
// LENGTH is claude's, really the WORKSPACE's, to choose, so a per-entry text cap alone
// leaves the total a function of a number the daemon does not control. It is
// maxTaskRosterEntries' doctrine's third application after maxModelListEntries, and that
// constant is its structural twin in every part except the one this doc spends itself
// on. Applied at CONSTRUCTION like every cap above; truncation is FROM THE TAIL, so
// claude's order is preserved and no ranking is invented. Overflow is REPORTED —
// turnevent.SlashCommandList.DroppedCommands — rather than silent, which is the property
// that makes a cardinality bound honest.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): FIFTY-ONE entries. A second observation exists and is NOT from
// the capture — SEVENTY-FOUR, a hand count against claude 2.1.220 in a different working
// directory — and it is the binding one below. The count is working-directory AND
// version dependent, which is the feature's whole point, so nothing here may hardcode
// either figure; they are the population this cap is derived to clear, not a shape it
// may assume.
//
// THE NUMBERS DO NOT ALL FIT, AND SPENDING ONE OF THEM IS THIS CONSTANT'S SUBSTANCE.
// Three things this family holds true elsewhere cannot all hold here, and the derivation
// is worthless unless it says which one it gives up.
//
// THE HANDED-FORWARD CANDIDATES ARE NOT AVAILABLE AS WRITTEN, and declining them is the
// first step rather than an afterthought. maxSlashCommandDescription's fraction table
// computes this slice's options against the 1280-byte per-entry WORST CASE: 16384 * 1/2
// = 8192 leaves 6 entries and 16384 * 5/8 = 10240 leaves 8. Both CUT A 51-ENTRY MENU BY
// 43 OR 45 ENTRIES — a cap firing on claude's ORDINARY output, which maxModelListEntries'
// NOT 8 paragraph rejects by name and maxSlashCommandAliasCount's ROLE argument sharpens
// one dimension up: a cut command is a WORKING command greyed out in the named consumer's
// menu, pyrycode-desktop#694 rendering a type-ahead over the whole list. A candidate set
// offered by a doc paragraph is still a candidate set, and taking one mechanically
// because it was handed forward is the failure this paragraph exists to refuse.
//
// SO THE BASE MOVES FROM THE WORST CASE TO THE OBSERVATION, and the figure is the
// package's own rather than a new measurement: maxSlashCommandDescription computes what
// the capture's 51 entries RETAIN through truncateField — name, argument hint,
// description and aliases — at 6,711 bytes, which is 131.59 bytes per entry against the
// 1280-byte worst case. Re-derived at that base the same ceiling gives 62 entries at 1/2
// and 77 at 5/8, and 124 at the whole 16384.
//
// CHANGING THE BASE IS NOT ENOUGH, which is the result that decides everything below.
// The 74-entry observation RETAINS about 9,738 bytes — 59% of maxUnrecognizedRaw's whole
// UNKNOWN-line cap, before any cap fires and with nothing this constant can do about it.
// 1/2 CUTS that observation by twelve entries. 5/8 clears it by THREE, a 1.04x margin on
// a count that is workspace- and version-dependent by design, which is inside the
// observation's own noise. And the family's power-of-two convention leaves exactly two
// candidates above 74: 64, which cuts it, and 128, whose projected retained cost of
// 16,844 bytes is past the WHOLE 16384 ceiling that fraction 1 already forbids
// approaching.
//
// THAT IS maxModelListEntries' OWN SITUATION VERBATIM — "no pair of caps this family's
// own doctrine permits fits the inherited ceiling, so the ceiling was the only lever
// left rather than one of three" — and it is resolved the same way, by the ceiling
// giving. What is NOT copied is that constant's resolution: it moved to a FRACTION of a
// larger ceiling, and this shape has no ceiling it can meet at all. Both halves are
// checkable. The worst-case product exceeds the 65519-byte v2 application-envelope cap
// at any count clearing 74 (74 * 1280 = 94,720), the only count whose worst case fits
// being 51, the observation itself. And maxUnrecognizedRaw's rule is an ORDERING rule —
// a whole KNOWN event must not approach the bound on an entire UNKNOWN line — which
// presupposes the known event is the cheaper thing; for this shape that presupposition is
// already false at claude's ordinary output, at 0.59 of the ceiling, so applying it would
// set this number by how large an UNKNOWN line may be rather than by how large claude's
// KNOWN inventory actually is.
//
// WHAT IS SPENT, stated plainly because a derivation that hides its cost is worth
// nothing: the WORST-CASE-PRODUCT convention. What is KEPT is the no-fire-on-ordinary-
// output rule. What this cap therefore buys is exactly two things — the retained size
// stops being a function of a number the workspace chooses and becomes a function of a
// daemon constant, where before the only bound below it was defaultMaxParseBuf's 4 MiB;
// and the cut is COUNTED, so a shortened menu says by how much. What it does NOT buy is
// a frame that fits: 128 * 1280 = 163,840 raw bytes is 2.5x the 65519-byte envelope —
// and that is raw-byte arithmetic rather than a wire measurement, since Go escapes '<',
// '>', '&' and U+2028/U+2029 at six bytes each, so the true wire worst case is higher
// still. NO count cap can close that gap either way.
//
// THAT FRAME-LEVEL BOUND NOW EXISTS, and it is not here: turnbridge's
// maxSlashCommandListBytes (#2002) cuts the mapped list a second time, by MEASURED
// bytes, at the one place both wire consumers pass through. The two cuts compose rather
// than compete — this one bounds the RETAINED COUNT and reports it, that one bounds the
// SERIALISED SIZE and adds its own drops to the same number, so len(commands) +
// dropped_commands stays the list's true size after both. A reader arriving from there
// must still not read "the entry count is bounded" as "the frame fits": it does not,
// and a second, differently-shaped cut is exactly why.
//
// 128, AND THE THREE CONSTRAINTS LEAVE ONE SURVIVOR:
//
//   - ABOVE THE LARGER OBSERVATION. 1.73x over 74 and 2.51x over the capture's 51. That
//     is above the family's cardinality convention (maxModelListEntries 1.67x,
//     maxModelEffortLevelCount 1.6x) and below maxSlashCommandAliasCount's 4x, and it
//     earns the thicker end for that constant's own two reasons: this bounds a list a
//     WORKSPACE author writes in a repository, whose growth direction nothing claude
//     ships bounds, and a cut entry costs a working command greyed out in the consumer's
//     menu rather than a truncated row.
//   - A POWER OF TWO, matching every constant in this family except maxModelListEntries,
//     whose own doc explains why it alone is decimal.
//   - NOT GRATUITOUS. 256 is 3.5x the larger observation and projects 33,687 bytes
//     retained — over twice the whole-line ceiling and past half the envelope — for
//     headroom neither observation asks for. maxSlashCommandAliasCount's WHY NOT 16,
//     one dimension up.
//
// THE CUT IS IN emitModelList, NOT IN THE EMITTER, and the placement is load-bearing
// three ways. It runs ONCE above BOTH rungs that read the array, so there is no second
// cap for the two to keep in step. It is available to logControlResponse, which both
// rungs call BEFORE any emit, which is what lets the record report the drop at all.
// And it BOUNDS THE ALLOCATION in emitSlashCommandList, whose make() is sized from
// len(entries): moving this cut into that emitter's loop would compile, pass every
// assertion about the emitted value, and silently restore an allocation sized by the
// workspace.
//
// IT CANNOT MOVE A RUNG'S CLASSIFICATION, and that is structural rather than tested-in:
// the cap is >= 1, so the emitted count is 0 exactly when the decoded count is, and the
// ack rung's "neither array" test partitions against the commands-only rung exactly as
// it did. Capping can neither create an empty list nor rescue one.
//
// THE RESLICE IS DELIBERATE where boundAliases refuses the same shape, and the two are
// consistent rather than in tension. That closure must not return values[:n] because its
// result IS retained by the emitted event; this cut MAY reslice because its result is
// not — emitSlashCommandList appends CONSTRUCTED values into a fresh allocation, and the
// resliced header dies with emitModelList. maxModelListEntries' block reslices for this
// same reason.
//
// THE AMPLIFICATION IS COMPUTED HERE RATHER THAN INHERITED, because the paragraph this
// family usually cross-references is FALSE for this struct. maxTaskRosterEntries reports
// its own as "linear and near 1" on ~55 bytes of input per ~64-byte struct.
// commandEntryLine's densest legal entry is `{},` — THREE bytes, every key being optional
// and absence being claude's to choose — for a 72-byte struct (three string headers and
// one slice header). So a single defaultMaxParseBuf-sized 4 MiB line yields ~1.4M decoded
// entries and on the order of 100 MB of TRANSIENT allocation before this cut discards all
// but 128: an amplification near 24x, not near 1. It is bounded rather than unbounded —
// one line at a time, reclaimed, and nothing past emitModelList retains it — which is
// maxTaskRosterEntries' accepted trade, but the FIGURE is this struct's own.
const maxSlashCommandListEntries = 128

// controlResponseSuccess is the ONE response.subtype whose payload this parser
// will read. Byte-exact equality against a DAEMON-authored constant, never a fold
// or a prefix: it is the SHARED PRECONDITION of both of emitModelList's emits, and
// what refuses to read an inventory out of a response reporting FAILURE. It was
// HALF OF A CONJUNCTION until #1891, the other half being a non-empty models array;
// since that slice each array decides its own event below this test, so this
// constant gates both emits and neither array gates the other — emitModelList's
// THE DISCRIMINANT states the lattice that replaced the conjunction. Everything
// else — "error", a subtype claude invents later, an absent one — is not success
// and takes the nak rung, which is what makes that classification total.
const controlResponseSuccess = "success"

// controlResponseMsg is the ONE record every control_response produces, and the
// constants below are the closed set of reasons it carries. rateLimitDropMsg's
// shape and its argument: daemon-authored keywords, never claude's values, and one
// message string for the tests to filter on.
//
// The message is UNCHANGED from #1500 and every control_response still produces
// exactly one record; what #1811 added is `reason` and a `models` count on records
// that previously carried the type alone, and what #1812 added beside them is a
// `dropped` count, so a shortened list is not read as a whole one. All three are
// daemon-authored — a keyword from this set and two integers — so the content-free
// discipline is intact, and the side
// benefit is the gap consumeLine's own arm recorded as accepted: a NAK is no longer
// indistinguishable from a success in the log.
//
// #1890 added the fifth keyword and NARROWED the second. `ack` used to answer every
// non-emitting success, which folded two different payloads under one word: a success
// carrying neither array, and one carrying a slash-command inventory and no model
// list. Only the `commands` count told them apart on the record. The keyword now does,
// and the set below is FIVE rather than four.
//
// WHAT DECIDES THE NEW KEYWORD'S NAME is that it must name the payload's SHAPE rather
// than what the daemon did about it, and the constraint was two-sided: a name for the
// emit — `command_list`, parallel to `model_list` — was false in the state #1890
// shipped, where that rung emitted nothing, and a name spelling "ack" would have gone
// false the moment the rung started emitting. #1891 IS THAT MOMENT, and it arrived
// without touching this set: `commands_only` names the shape, so it survived the state
// change that each of the two rejected spellings would have failed on one side of. The
// set was decided ONCE, and the slice that added the emit re-authored a trailing
// comment rather than moving a keyword. That is why this block is the place the
// no-new-keyword boundary is READABLE rather than merely observed.
const (
	controlResponseMsg = "streamsup: consuming solicited control_response"

	controlResponseNAK          = "nak"           // subtype was not controlResponseSuccess
	controlResponseAck          = "ack"           // success, neither array on the line
	controlResponseUndecodable  = "undecodable"   // the nested shape did not decode
	controlResponseCommandsOnly = "commands_only" // success, a commands inventory and no model list; one turnevent.SlashCommandList emitted
	controlResponseModelList    = "model_list"    // one turnevent.ModelList emitted
)

// ignoredLineTypes is the MEASURED set of top-level stream-json types the
// parser deliberately drops in silence. Membership is what separates "known and
// deliberately ignored" from "genuinely unrecognized"; getting it wrong in
// either direction is the whole risk of the unrecognized-message feature. Too
// narrow and every turn grows a noise row; too wide and a real new message type
// stays invisible.
//
// Measured 2026-07-27 by driving claude directly on this exact bare stream-json
// surface (the fixed --input-format/--output-format/--verbose prefix
// buildArgs emits), three turns each on haiku and on the default model, one turn
// per run calling tools. Observed top-level types across both runs:
//
//	assistant, user, result   — mapped below
//	system                    — subtypes init, thinking_tokens (and status, per
//	                            the #1088 spike); init fires ONCE PER TURN, and
//	                            thinking_tokens fired ~10 times per turn
//	rate_limit_event          — once per run
//
// MAPPED since 2026-08-09 (#1404): the rate_limit_event row above is a statement
// about what CLAUDE emits and still holds exactly, but the type is no longer on
// the list below — see the correction under "Still dropped in silence". The
// census row is left unedited so the 2026-07-27 measurement stays legible; this
// pointer is what keeps a reader from taking the census for the drop list, which
// is the way a list and its rationale come to disagree.
//
// system is claude's catch-all namespace and its highest-rate emitter (see the
// per-turn counts above), so subtype-grained matching risks turning every new
// subtype into a per-turn noise row — the exact failure the two-tier design
// exists to prevent. Until 2026-08-07, that argument was implemented by ignoring
// system WHOLESALE.
//
// CORRECTED 2026-08-07 (#1380): system is NO LONGER ignored wholesale. The
// parser matches subtypes INSIDE this list's drop branch — see
// emitSystemSubtype, whose case arms are the one place the mapped set is
// enumerated. Do not restate that set here: no comment fails a build when it
// goes stale, so a single enumeration site with pointers to it is worth more
// than four independent lists. Adding a subtype IS adding a case arm there, and
// this comment carries no count of them — a count is the smallest possible
// restatement of the set, and #1385 falsified the previous one (which said
// "all three subtypes the capture holds a payload for", already loose: the
// capture holds payloads for init and thinking_tokens too).
//
// That is a REFINEMENT of the 2026-07-27 measurement, not a reversal. The
// argument above is about what to DRAW, and it was used to decide what to SEND.
// Those are separate decisions, and only the second belongs to the daemon's
// callers: the per-turn noise row the measurement forbade is a rendering choice
// the client owns. Mapping a measured subtype into the daemon's own vocabulary
// changes what is sent and leaves the drawing decision — and the measurement
// behind it — exactly where they were.
//
// Still dropped in silence: every system subtype emitSystemSubtype does not
// match, including never-seen ones and task_notification (measured ABSENT on
// this surface; seen once on the headless surface only).
//
// CORRECTED 2026-08-09 (#1404): rate_limit_event is no longer on this list and no
// longer dropped whole. The census row above measures it as a TOP-LEVEL type
// carrying no subtype, so mapping it was never an emitSystemSubtype-shaped
// change: it has its own arm in consumeLine's main switch, alongside
// assistant/user/result, and emitRateLimit's gate decides what it produces. A
// line reporting the measured-benign status, and a line carrying no decodable
// rate_limit_info, are both still silent — but by that ARM consuming them, not by
// membership here.
//
// The list is therefore down to ONE member, and it stays top-level types only.
// system stays on it, which is what keeps emitUnrecognized structurally
// unreachable from any system line whatever its subtype, and keeps a genuinely
// new MESSAGE TYPE — the alarm worth raising — the thing the top-level key
// catches. One member is a transient state, not the design.
//
// The measurement also settled the open question of whether claude echoes the
// delivered prompt back as a `user` message holding a `text` block, as it does
// on the agent-run surface: it does NOT here. Every `user` line in both runs
// carried tool_result blocks only.
//
// AMENDED 2026-07-30 (#1247): that measurement never drove a BACKGROUNDED
// command. Backgrounding produces a turn with no visible model output, and
// claude's harness then injects a `user` message holding a `text` block prodding
// the model to say something. So exactly one user/text string is now dropped —
// see harnessNoOutputNudge for the capture, the claude version, and why the
// author being the harness (neither the user nor the model) makes it a
// suppression rather than a mapping. That is a block-level constant, NOT an
// entry in this list, which stays top-level types only and is unchanged. Every
// OTHER user/text block still surfaces, and that remains the real change worth
// seeing.
//
// A real-claude test asserts a normal turn produces zero unrecognized events, so
// this list going stale fails the pre-ship gate rather than reaching a client.
var ignoredLineTypes = map[string]bool{
	"system": true,
}

// harnessNoOutputNudge is the ONE user/text block the parser drops in silence.
//
// It is claude's harness prodding the model after a turn produced no visible
// output; backgrounding a command is the observed way to get there. The author
// is neither the user nor the model, and that is what makes this a SUPPRESSION
// rather than a mapping: rendering it as a user/text block would put the
// harness's self-talk into the person's own message history, and the same would
// go for any future harness-injected prose. The narrowest possible match is the
// only safe shape here.
//
// This is the parser's FIRST block-level suppression — a new tier, not an entry
// on an existing list. ignoredLineTypes is top-level types only, and
// emitAssistant states the block-level position explicitly ("No known-ignored
// list at block level … Any fourth is news"). Scope is emitUser: an assistant
// text block carrying these bytes is model speech and still maps to TextChunk.
//
// Provenance, which is thinner than the string's confident tone suggests:
// transcribed byte-exact from the #1247 capture — claude 2.1.220, the #1240
// probe, 3 of 3 occurrences in one session. The string exists in no tracked
// file and the local ~/.claude/projects corpus corroborates nothing, so
// cross-version stability is UNMEASURED. 100 bytes, ASCII only, single-spaced,
// ASCII hyphen in "user-visible".
//
// Matched by byte-exact equality — no trim, no fold, no prefix, no substring —
// because that is the tolerance one observation earns, and because its failure
// direction is the safe one: drift means the Unrecognized row comes back and a
// human looks. A loose match would instead swallow the next harness payload, or
// a prompt echo should claude ever start echoing on this surface, in silence. A
// SECOND confirmed payload, with a measurement behind it, is what promotes this
// constant to a set with a pin test — not before.
const harnessNoOutputNudge = "[Your previous response had no visible output. Please continue and produce a user-visible response.]"

// Parser turns the child's stdout stream-json line stream into neutral
// turnevent.Event values. It is an io.Writer wired as streamsup Config.Stdout;
// each Write consumes every complete '\n'-delimited line and emits zero-or-more
// events per line to the sink, in stream order. A `result` line ends the turn
// (→ TurnEnd); no transcript file is opened, watched, or resolved (AC4) — the
// only input is the Write bytes.
//
// Turn-stateless in everything that describes claude's output. The parser holds
// no turn counter, no awaiting flag, no transcript, and remembers nothing any
// line SAID: every mapping is a pure function of the line it reads.
//
// AMENDED 2026-08-09 (#1385): it holds exactly one piece of cross-line state, and
// the absolute phrasing this paragraph used to carry ("no per-session
// accumulator") is now false. thinkingSinceEmit is a token COUNTER — not a memory
// of anything claude said — and it exists because the thinking_tokens mapping is
// rate-bounded and so cannot be a function of one line alone. Zero cross-turn
// bleed stays structural, but by the RESET rather than by the absence of state:
// consumeLine's `result` arm zeroes it unconditionally, both result subtypes go
// through that arm, and it is the only cross-line state besides the partial-line
// buffer. No other line type can create, reset, or leak state across a boundary.
//
// Single-writer invariant: os/exec drives a non-*os.File Config.Stdout through
// exactly one internal goroutine (io.Copy of the child's stdout pipe into this
// writer), so Write — hence buf and the sink calls — is only ever invoked
// serially from that one goroutine. There is no second reader of parser state,
// so no mutex is needed. (Contrast streamrunner's streamParser, which locks
// because its watchdog goroutine reads its state.) If a future slice adds a
// concurrent reader, it adds the guard then.
type Parser struct {
	sink   func(turnevent.Event)
	log    *slog.Logger
	maxBuf int
	buf    []byte
	// thinkingSinceEmit accumulates estimated_tokens_delta since the last
	// ThinkingProgress. Reset to 0 on every `result` line; see emitThinkingProgress
	// for the rule.
	//
	// INVARIANT: thinkingSinceEmit ∈ [0, minThinkingTokensPerEvent-1] after every
	// line. The only way to reach the bound is to emit, which resets to 0, and
	// negative deltas are refused before they are added. Two things depend on it,
	// and neither survives it being relaxed: a zero-delta line can never trigger an
	// emit even if the guard were removed, and — the load-bearing one —
	// `bound - thinkingSinceEmit` stays in [1, bound], which is what makes the
	// crossing test unable to overflow whatever claude sends. Do not "simplify" the
	// crossing test back into its additive form; see emitThinkingProgress.
	//
	// NO LOCK, and this is the first field for which Parser's single-writer
	// invariant does real work rather than describing a buffer: os/exec drives
	// Write from exactly one goroutine and nothing else reads parser state, so the
	// read-modify-write here needs no mutex. If a future slice adds a concurrent
	// reader, this is the first field its guard has to cover.
	thinkingSinceEmit int
}

var _ io.Writer = (*Parser)(nil)

// NewParser returns a Parser that emits events to sink. sink is called serially,
// in stream order, on the os/exec forwarder goroutine; the consumer owns any
// synchronization it needs beyond that (the round-trip test's sink pushes to a
// channel; a real consumer forwards to the relay). logger is used for
// content-free Debug diagnostics only; nil falls back to slog.Default.
func NewParser(sink func(turnevent.Event), logger *slog.Logger) *Parser {
	if logger == nil {
		logger = slog.Default()
	}
	return &Parser{
		sink:   sink,
		log:    logger,
		maxBuf: defaultMaxParseBuf,
	}
}

// Write appends b to the line buffer, consumes every complete '\n'-delimited
// line (emitting events to the sink), and keeps the partial remainder for the
// next Write. It always reports (len(b), nil): the parser is the terminal stdout
// sink, not a tee, so it fully consumes what it is handed and has no downstream
// short-write to propagate.
func (p *Parser) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	rest := p.buf
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		p.consumeLine(rest[:i])
		rest = rest[i+1:]
	}
	if len(rest) > p.maxBuf {
		p.log.Debug("streamsup: dropping oversized partial line", "bytes", len(rest))
		rest = nil
	}
	// Copy the remainder into a fresh slice so the (possibly large) backing
	// array of p.buf is released.
	p.buf = append([]byte(nil), rest...)
	return len(b), nil
}

// streamLine is the minimal decoded shape of one stdout stream-json line: only
// the fields the mapping reads are declared; everything else claude emits is
// ignored. Segmentation keys on the top-level Type only — nested content is
// never re-scanned for control types, so a tool result whose text is literally
// `{"type":"result"}` cannot forge a turn boundary.
type streamLine struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype"`
	Message *streamMessage `json:"message"`
}

// systemTaskStartedLine is the decoded payload of one system/task_started line.
// Kept separate from streamLine, which is the line-level SEGMENTATION struct and
// stays at Type/Subtype/Message; these fields belong to a single subtype and
// widening the segmentation struct with them would blur that boundary.
//
// The field set is exactly what the committed capture shows and nothing
// invented. Two keys the captured line also carries are deliberately absent:
// uuid, which nothing in the daemon reads, and session_id, which is claude's
// session identity and NOT the daemon's conversation identity — see
// turnevent.BackgroundTaskStarted's doc.
type systemTaskStartedLine struct {
	TaskID      string `json:"task_id"`
	ToolUseID   string `json:"tool_use_id"`
	Description string `json:"description"`
	TaskType    string `json:"task_type"`
}

// systemTaskUpdatedLine is the decoded payload of one system/task_updated line.
// Kept separate from streamLine for systemTaskStartedLine's reason, and separate
// from systemTaskStartedLine because the two subtypes share no payload shape
// beyond task_id.
//
// The field set is exactly what the committed capture shows and nothing
// invented, and it is the whole mapped set: the same two keys the captured line
// also carries, uuid and session_id, are deliberately absent — see
// turnevent.BackgroundTaskUpdated's doc. Absent from the DECODE TARGET is a
// stronger guarantee than the test's reflection sweep, because a field that is
// never declared cannot leak.
//
// The two fields' types are deliberately asymmetric. TaskID stays a string, so a
// non-string task_id fails the whole decode and takes the undecodable path.
// Patch is json.RawMessage, which accepts ANY valid JSON value — an object, a
// string, a number, null — and carries claude's bytes verbatim. Both halves of
// that matter: decoding into map[string]any and re-marshalling would normalize
// key order and round every number through float64 (a large integer id in a
// future patch would lose precision), and declaring a shape would DISCARD every
// unknown field, which is streamMessage.Content's reasoning for the same choice.
// The permissiveness is deliberate — "whatever claude puts there" is the point,
// and inventing a validation rule for a shape we have one observation of is
// exactly what #1380 declined to do for missing fields. maxTaskPatch is what
// makes it safe.
type systemTaskUpdatedLine struct {
	TaskID string          `json:"task_id"`
	Patch  json.RawMessage `json:"patch"`
}

// systemBackgroundTasksLine is the decoded payload of one
// system/background_tasks_changed line. Kept separate from streamLine for
// systemTaskStartedLine's reason, and separate from both scalar targets because
// this subtype's payload is an ARRAY — the shape difference the whole ticket
// sits on.
//
// The two keys the captured line also carries, uuid and session_id, are
// deliberately absent — see turnevent.BackgroundTaskRoster's doc. Absent from
// the DECODE TARGET is a stronger guarantee than the test's reflection sweep,
// because a field that is never declared cannot leak.
type systemBackgroundTasksLine struct {
	Tasks []systemBackgroundTaskEntry `json:"tasks"`
}

// systemBackgroundTaskEntry is one element of that array. The field set is
// exactly what the committed capture shows and nothing invented: no tool_use_id
// and no patch, which the scalar siblings' targets carry because their LINES do,
// and mirroring either here would invent a key claude does not send.
//
// All three are plain strings, which is why truncateField's json.RawMessage
// exception does not reach this subtype: encoding/json has already
// U+FFFD-replaced invalid input on decode, so our own cut is the only mid-rune
// hazard. A non-string value for any of them fails the whole decode and takes
// the undecodable path, exactly as systemTaskUpdatedLine.TaskID does.
type systemBackgroundTaskEntry struct {
	TaskID      string `json:"task_id"`
	TaskType    string `json:"task_type"`
	Description string `json:"description"`
}

// systemThinkingTokensLine is the decoded payload of one system/thinking_tokens
// line. Kept separate from streamLine for systemTaskStartedLine's reason, and
// separate from the three background-task targets because it shares no key with
// any of them.
//
// The field set is exactly what the committed capture shows and nothing invented:
// all 33 captured records carry the identical key set, and these are the two of
// them the mapping reads. The other two, uuid and session_id, are deliberately
// absent — see turnevent.ThinkingProgress's doc. Absent from the DECODE TARGET is
// a stronger guarantee than the test's reflection sweep, because a field that is
// never declared cannot leak.
//
// Plain int, not *int: absence and an explicit zero are treated IDENTICALLY
// because the rule's response to both is the same — accumulate nothing, emit
// nothing — so a pointer would buy a distinction nothing acts on. A non-numeric
// value, or one too large for int, fails the whole decode and takes the
// undecodable path, exactly as systemTaskUpdatedLine.TaskID does.
type systemThinkingTokensLine struct {
	EstimatedTokens      int `json:"estimated_tokens"`
	EstimatedTokensDelta int `json:"estimated_tokens_delta"`
}

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
// the three captures. Absent from the DECODE TARGET is a stronger guarantee than
// the test's reflection sweep, because a field that is never declared cannot
// leak.
type rateLimitEventLine struct {
	Info rateLimitInfo `json:"rate_limit_info"`
}

// rateLimitInfo is claude's rate_limit_info object reduced to the three keys the
// mapping reads. All three are present in all three captures on record, across
// three claude versions.
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
}

// systemInitLine is the decoded payload of one system/init line. Kept separate
// from streamLine for systemTaskStartedLine's reason, and separate from every
// other subtype target because it shares no key with any of them.
//
// ONE FIELD, and the OMISSIONS are the point. The captured line carries 22 keys —
// type, subtype, cwd, session_id, tools, mcp_servers, model, permissionMode,
// slash_commands, apiKeySource, claude_code_version, output_style, agents, skills,
// plugins, capabilities, analytics_disabled, product_feedback_disabled, uuid,
// memory_paths, fast_mode_state, fast_mode_disabled_reason — and twenty-one are
// deliberately absent from this target. Two of them are why that matters: cwd is
// the operator's local filesystem path, and session_id is claude's session identity
// and NOT the daemon's conversation identity (#1380). See turnevent.ModelAnnounced's
// doc. Absent from the DECODE TARGET is a stronger guarantee than the test's
// reflection sweep, because a field that is never declared cannot leak.
//
// A plain string, which is why truncateField's json.RawMessage exception does not
// reach this shape: encoding/json has already U+FFFD-replaced invalid input on
// decode, so our own cut is the only mid-rune hazard.
//
// A non-string model fails the whole decode and takes emitModelAnnounced's
// undecodable arm, exactly as systemTaskUpdatedLine.TaskID does for a numeric task
// id — and that is the ONLY reachable undecodable case here, which is what tells a
// test how to build the fixture: consumeLine has already decoded this line into
// streamLine, so malformed JSON never reaches this function at all.
type systemInitLine struct {
	Model string `json:"model"`
}

// controlResponseLine is the decoded payload of one top-level control_response
// line. Kept separate from streamLine for systemTaskStartedLine's reason, and it
// is the family's first DOUBLE-nested target: the outer `response` is claude's
// wrapper carrying subtype and request_id, and its own `response` is the
// initialize payload the models array sits in.
//
// The nesting path is spelled out in full deliberately. The decode's input is the
// TOP-LEVEL line bytes, never a nested field — streamLine's doc states the
// property that preserves — so every level between the line and the array has to
// appear here, exactly as rateLimitEventLine spells rate_limit_info.
//
// request_id is deliberately absent, and so is `error`. Nothing correlates the id
// (see emitModelList's provenance paragraph) and nothing reads the error string,
// which is claude's prose about a failure the daemon takes no action on; a field
// never declared cannot reach a log or an event, which is systemInitLine's
// argument for its own twenty-one omissions.
//
// The initialize payload's other twelve top-level keys are absent for that same
// reason and one of them is why it matters: `account`. It is never decoded, never
// bounded, never retained and never logged, because it is not on this struct.
type controlResponseLine struct {
	Response struct {
		Subtype  string `json:"subtype"`
		Response struct {
			Models []modelOptionLine `json:"models"`
			// A plain []commandEntryLine for Models' reason verbatim: absent, null and
			// empty are answered identically — a count of 0 — so a pointer would buy a
			// distinction nothing acts on.
			Commands []commandEntryLine `json:"commands"`
		} `json:"response"`
	} `json:"response"`
}

// modelOptionLine is one element of that array, reduced to the five keys the
// mapping reads. The field set is exactly what turnevent.ModelOption carries and
// nothing invented: description, supportsEffort, supportsAdaptiveThinking and
// supportsFastMode are all absent by decision, not by oversight — see that type's
// doc. supportedEffortLevels is decoded HERE (#1827), which leaves no capability key
// claude sends unaccounted for.
//
// A plain []modelOptionLine on the container above, not a pointer to one, and no
// distinction is kept between an absent `models`, a null one and an empty array:
// emitModelAnnounced's formulation carries over verbatim — absent,
// present-but-empty, and a line carrying no such key all land in the same rung and
// are answered identically, which is what makes a plain decode target sufficient.
//
// The first three are plain strings, and so is every ELEMENT of EffortLevels,
// which is why truncateField's json.RawMessage exception does not reach this shape:
// encoding/json has already U+FFFD-replaced invalid input on decode, so our own cut
// is the only mid-rune hazard. A non-string value for any of the three — or a
// supportsAutoMode that is a string, a number, an object or an array, or a
// supportedEffortLevels that is a string, a number or an object, or a `models` that
// is a number, an object or a string, or a non-object `response` at either level —
// fails the WHOLE-LINE decode and takes emitModelList's undecodable rung, exactly as
// systemTaskUpdatedLine.TaskID does for a numeric task id. That type mismatch is
// the ONLY reachable failure there: consumeLine has already decoded this line into
// streamLine, so malformed JSON never reaches the function at all.
//
// A supportedEffortLevels ARRAY carrying a non-string element fails the same way,
// and it is named here rather than left to be inferred because it is the family's
// first ELEMENT-level mismatch and "the array decoded but one element was wrong" is
// the shape a reader would otherwise assume is tolerated. The decode is
// all-or-nothing at the LINE: there is no path that keeps an array's good elements
// and drops the bad one, which is the behaviour this field wants, since a menu that
// silently lost an element would be published as claude's complete one.
//
// JSON null is the one carve-out, and it has always applied to the three strings
// as much as to the bool: encoding/json documents unmarshalling a null into a
// non-pointer Go value as a NO-OP producing no error, so a null-valued key decodes
// cleanly and lands as the zero value rather than on the undecodable rung. For
// supportsAutoMode that is the same reading an absent key gets, which is what
// turnevent.ModelOption.SupportsAutoMode's doc argues is deliberate. For
// supportedEffortLevels it lands as nil, which is the same reading an absent key
// gets — and, since #1828, the same one a published [] gets too, the producer
// normalising that third shape onto this nil in emitModelList's boundEach. There is
// no branch in which a null reads as its own thing; the argument for the single
// reading is turnevent.ModelOption.EffortLevels'.
type modelOptionLine struct {
	ResolvedModel    string   `json:"resolvedModel"`
	Value            string   `json:"value"`
	DisplayName      string   `json:"displayName"`
	EffortLevels     []string `json:"supportedEffortLevels"`
	SupportsAutoMode bool     `json:"supportsAutoMode"`
}

// commandEntryLine is one element of the initialize payload's `commands` array —
// claude's slash-command inventory for the workspace the child was spawned in
// (#1853). "Entry" rather than modelOptionLine's "option": that word names a menu
// choice the daemon publishes, and a slash command is not one.
//
// FOUR FIELDS OF FOUR SINCE #1825, AND THE OMISSION THIS PARAGRAPH USED TO DEFEND IS
// GONE RATHER THAN RELAXED. claude sends four keys per entry — name, argumentHint,
// description, aliases. Name arrived with the decode (#1853), Description with #1904,
// ArgumentHint with #1957 and Aliases with #1825, each when a slice had a consumer for
// it. The SLICE-SCOPED reading the paragraph insisted on is what made that sequence
// possible: a field that is never declared cannot reach a log or an event, which is
// what each omission bought until the slice that spent it. What a later reader must
// not do now is the mirror of what it forbade then — treat this key set as open and
// add a field for a key claude has not been observed to send. protocol.SlashCommand's
// own doc carries the measurement that the four ARE the whole vocabulary, so a fifth
// key arrives as a documented gap against a stated measurement rather than as a silent
// drop.
//
// THE MEMORY ARITHMETIC INVERTED WHEN THE SECOND FIELD LANDED AND IS SETTLED NOW, and
// what moved over the sequence was the ARGUMENT and not only the percentage. The
// captured array is 14,277 bytes compact; the fifty-one `name` strings inside it total
// 494, the fifty-one `argumentHint` strings 530, the fifty-one `description` strings
// 10,580 and the eleven aliases across nine entries 64 — so 11,668 of 14,277, 81.7%,
// becomes a Go string and nothing is kept out by omission at all. It was 96% when one
// field was declared, 77.6% when two were and 81.3% when three were, which is why the
// omission USED to be the whole of the memory story and is none of it now. The fourth
// field is the cheapest of the four by an order of magnitude, 64 bytes against the
// hints' 530 and the descriptions' 10,580, so it moved the percentage by four tenths of
// a point while moving the worst-case term by two thirds. What carries that weight is
// the per-field cap one step downstream, which bounds what is RETAINED whatever this
// struct decodes: the 11,668 is transient, and the capture's 51 entries retain 6,711
// bytes of it after maxSlashCommandName, maxSlashCommandArgumentHint,
// maxSlashCommandDescription and the alias pair have run.
//
// EVERY STRING HERE IS WORKSPACE-AUTHORED. A slash command defined in a repository
// was written by whoever wrote that repository, and the daemon reads it in whatever
// directory the operator points a session at. Name is nevertheless NOT validated —
// not for emptiness, not for charset, not for control bytes — and the reason is NO
// SINK rather than safe bytes. The capture carries `__remote-workflow`, which is the
// committed proof that no charset may be assumed.
//
// THAT VALIDATION QUESTION WAS LEFT OPEN FOR THE FIRST SLICE THAT READS Name, AND IT
// IS ANSWERED HERE. A byte-capped COPY of Name now leaves emitSlashCommandList inside
// a turnevent.SlashCommand (#1877), so the count is no longer the only thing that does.
// Validation stays REFUSED and the reason is still NO SINK — but a CHECKED no-sink
// rather than a structural impossibility, and the check is what this paragraph
// records. Name reaches: one field of a daemon-internal struct, the parser's emit
// callback, and from there cmd/pyry's interactiveTurnEmitterV2.Handle default, which
// has no case for the variant and logs it BY KIND through eventKind before discarding
// it. It stops THERE and does not reach turnbridge.MapEvent at all — an enumeration
// naming MapEvent's default would be crediting it with reach it does not have, since
// MapEvent's two production call sites are emitMapped, which only Handle's TYPED arms
// reach and none of those is SlashCommandList, and resolveBoundModelList, which hands
// it a ModelList explicitly. It reaches
// no exec.Command argument, no filepath.Join, no filepath.Match, no regexp, no log
// attribute, no eventring append and no wire frame. An ENUMERATION OF REACHED SINKS
// is the answer rather than a judgement about the bytes, and precisely because `[`,
// `*` and `?` are syntax to filepath.Match and to regexp with no shell anywhere in
// sight: "these look like identifiers" would be the wrong argument for a string one
// captured entry already spells __remote-workflow.
//
// DESCRIPTION WAS WALKED THROUGH THAT ENUMERATION ON ITS OWN (#1904) rather than
// inheriting Name's answer, because the enumeration above is a claim about Name
// established by inspection and not a property of the path. It rides the same struct
// field set, the same emit callback and the same two defaults, and it reaches the same
// empty set of sinks — so the answer is the same and the REASON it is the same is the
// re-walk, not the shared origin. What differs is only that the "these look like
// identifiers" argument was never available for it at all: this field is prose, and 14
// of the capture's 51 descriptions carry non-ASCII.
//
// THE ARGUMENT HINT WAS WALKED THROUGH IT ON ITS OWN TOO (#1957), inheriting NEITHER
// answer, for the reason the paragraph above gives: the enumeration is a claim about a
// value established by inspection and not a property of the path. It rides the same
// struct field set, the same emit callback and the same two defaults, and it reaches
// the same empty set of sinks — no exec.Command argument, no filepath.Join, no
// filepath.Match, no regexp, no log attribute, no eventring append and no wire frame.
// So the answer is the same and the REASON it is the same is the re-walk. What differs
// is that "these look like identifiers" is LEAST available here of the three: 13 of the
// capture's 18 non-empty hints carry `[` and `]`, ten carry `<` and `>`, seven carry
// `|` and one carries parentheses — and `[` alone is a character class to regexp and a
// bracket expression to filepath.Match. Where Name's enumeration had to reach for a
// single counter-example, syntax characters are this field's MAJORITY case. No captured
// hint carries `*`, `?` or a backslash, and the argument neither needs them nor claims
// them.
//
// EVERY STRING IN Aliases WAS WALKED THROUGH IT ON ITS OWN TOO (#1825), inheriting
// none of the three answers, for the reason the two paragraphs above give: the
// enumeration is a claim about a value established by inspection, not a property of the
// path. Each alias rides the same struct field set, the same emit callback and the same
// ONE default, and reaches the same empty set of sinks — no exec.Command argument, no
// filepath.Join, no filepath.Match, no regexp, no log attribute, no eventring append
// and no wire frame. So the answer is the same and the REASON it is the same is the
// re-walk. TWO things differ. The value is not one string but a LIST of them, so the
// enumeration is over every element and not over a field, which is why this paragraph
// says "every string in Aliases" rather than naming the field; the count is bounded by
// maxSlashCommandAliasCount, so the enumeration is over a bounded set rather than over
// a number claude chooses. And "these look like identifiers" is available here in a way
// it is not for the other three — every captured alias is inside [a-z], the shortest 3
// bytes and the longest 9 — which is exactly why it is REFUSED: an alias is drawn from
// the same population as a NAME, one member of which the capture spells
// __remote-workflow, so an argument from the observed alias charset would rest on nine
// entries' worth of coincidence. The empty sink set is the answer for this field as it
// is for the others.
//
// THE RE-OPEN TRIGGER, named rather than left to be noticed and covering ALL FOUR
// declared values: the first slice that gives any of them a SYNTAX sink — a path
// element, a match pattern, a regexp, an argv element, a log attribute — or that renders
// one into an HTML sink, an attribute or a URL inherits the question open again. #1720
// is the nearest such slice, and what it owes is the CLIENT-side render boundary
// turnevent.SlashCommandList's SECURITY paragraph already assigns.
//
// THE DECODED SLICE IS UNCAPPED HERE, and the bound is one level up rather than
// added: defaultMaxParseBuf caps the whole line at 4 MiB before the decoder sees it,
// which is already the whole of what bounds the models array's transient spike (see
// controlResponseLine's neighbouring paragraph). The arithmetic still favours this
// array on both sides, and #1825 is where the COMPARISON had to be re-derived rather
// than restated: this struct is FOUR fields where modelOptionLine is five, and the
// fourth is a []string exactly as modelOptionLine's EffortLevels is, so the two are now
// compared shape for shape rather than three-scalars against five-mixed. Per
// densest-legal element the worst-case transient is still a fraction of the
// already-accepted one — four fields against five, one nested list each — and a single
// 4 MiB line cannot maximise both. The direction is unchanged and the margin is
// thinner, which is why this is re-derived here and not simply restated.
//
// FOUR PER-FIELD CAPS DO EXIST ACROSS FIVE DIMENSIONS, one step downstream, which is
// what narrows the transient/retained contrast this paragraph used to draw:
// maxSlashCommandName bounds Name, maxSlashCommandArgumentHint bounds ArgumentHint,
// maxSlashCommandDescription bounds Description, and maxSlashCommandAlias with
// maxSlashCommandAliasCount bound Aliases in its two — all at CONSTRUCTION in
// emitSlashCommandList (#1877, #1904, #1957, #1825), where every cap in this package is
// applied. They are separate constants rather than one shared limit for
// maxRateLimitField's reason, and the description's is by far the largest contributor
// to what is retained. The transience is unchanged — this slice still lives from
// json.Unmarshal until emitModelList returns — but a BOUNDED COPY of each of the three
// strings and of a bounded number of bounded aliases is now retained inside the emitted
// turnevent.SlashCommandList. That lifetime is the models array's since #2004 and no
// longer shorter than it: cmd/pyry holds the CAPPED result of each for the session's
// life, this one in sessionSlashCommandHold and that one in sessionModelHold.
//
// The decode is all-or-nothing at the LINE, modelOptionLine's rule verbatim and at
// the ELEMENT level too: a `commands` that is a number, a string or an object, an
// element that is a bare string or a number, a `name`, an `argumentHint` or a
// `description` that is not a string, an `aliases` that is not an array, or an ELEMENT
// of `aliases` that is not a string all fail the WHOLE-LINE decode and take
// emitModelList's undecodable rung. The rule is the STRUCT's and not any one field's,
// which is why the second, third and fourth declared keys inherit it rather than
// earning branches of their own. The bare string is worth naming because it is the
// shape a future claude most plausibly sends: systemInitLine's line already spells this
// same inventory as bare strings under slash_commands.
//
// THE FOURTH KEY EXTENDS THAT RULE ONE LEVEL DEEPER AND THAT IS THE WHOLE OF WHAT IS
// NEW: `aliases` is the only declared key with an INSIDE, so it is the only one where a
// well-formed value of the right outer shape can still fail on an element. A
// `["reset", 5]` fails the whole line exactly as a numeric `name` does — the failure is
// the struct's, and no partial alias list survives carrying only the elements that
// happened to decode. The AVAILABILITY COST of declaring a key is real and is accepted
// here as it was three times before: a shape claude ships later that this struct cannot
// hold now costs the whole line, the models array with it, rather than being ignored.
// What makes that honest is the rung table, which carries a row per declared key and
// records the transition each time — a key that was undeclared and silently ignored
// becomes a decode-failure source the moment it is declared.
//
// JSON null is the one carve-out and it applies at ALL FOUR positions, for
// modelOptionLine's reason: encoding/json documents unmarshalling a null into a
// non-pointer Go value as a NO-OP producing no error, and documents a null into a SLICE
// as setting it nil — so a null `commands` lands as a nil slice, a null `name`,
// `argumentHint` or `description` lands as "" and a null `aliases` lands as nil, each a
// counted entry rather than a failed line. For all four that is also the reading an
// ABSENT key gets, so absent and null are one reading per position and no consumer can
// branch on the difference.
//
// THE FOURTH POSITION'S ZERO VALUE IS nil AND NOT "", which is why it is spelled out
// rather than folded into the sentence above: the collapse there is over three shapes
// and not two, a published `[]` being a shape the other three positions have no
// analogue for. This struct does NOT collapse it — an absent key and a null leave the
// field nil while a published `[]` leaves an empty non-nil slice — and the
// normalisation happens one step downstream at construction. turnevent.SlashCommand's
// own field doc owns that decision and the 0-of-51 measurement behind it.
//
// THAT COLLAPSE COSTS NOTHING OBSERVED FOR argumentHint, and it is written down rather
// than left implicit because it was the first field for which the question was worth
// asking at all. The key is present on all 51 captured entries and absent from none,
// while 33 of them carry "" — so an EMPTY hint is the ordinary case and an absent one
// is not observed. `aliases` INVERTS that and was decided on its own measurement rather
// than on this one: 42 of 51 entries omit the key, ZERO carry an empty array, and the
// collapse there folds a common absence into an emptiness claude has never sent. The
// distinguishability protocol.SlashCommand's doc protects is the WIRE's, and it is
// protected there by declaring no omitempty rather than by anything this decode does;
// #1819 is the precedent for stating on the type WHY a collapse is safe instead of
// leaving a reader to infer it.
type commandEntryLine struct {
	Name         string   `json:"name"`
	ArgumentHint string   `json:"argumentHint"`
	Description  string   `json:"description"`
	Aliases      []string `json:"aliases"`
}

// Content is held as raw bytes, not []streamBlock, and each element is decoded
// on demand in emitAssistant / emitUser. streamBlock declares only the fields
// the mapping reads, so decoding straight into it would DISCARD every unknown
// field — and re-marshalling the struct afterwards would lose exactly the
// content an unrecognized block exists to show. Keeping the bytes costs one
// deferred Unmarshal per block and makes the block's original JSON available
// verbatim; it also turns a block that fails to decode into a surfaced event
// rather than a silent skip.
type streamMessage struct {
	ID      string            `json:"id"`
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

// streamBlock is one content block of an assistant/user message, decoded from
// the raw bytes streamMessage.Content holds. The fields are
// a union across the block types we map: text (assistant text), thinking
// (assistant thinking), id/name/input (tool_use), tool_use_id/content/is_error
// (tool_result). Content is decoded as `any` so a tool_result's content — a JSON
// string or an array of text blocks — lands as the string / []any that
// toolResultText switches on (mirroring mapper.go).
type streamBlock struct {
	Type string `json:"type"`

	// assistant text / thinking
	Text     string `json:"text"`
	Thinking string `json:"thinking"`

	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// tool_result
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"`
	IsError   bool   `json:"is_error"`
}

// userToolResultLine carries the tool_use_result SIDECAR of one user line: the
// structured outcome claude writes as a TOP-LEVEL field, a sibling of type and
// message rather than something inside the message (#2024).
//
// Kept separate from streamLine for systemTaskStartedLine's reason — streamLine
// is the line-level SEGMENTATION struct and stays at Type/Subtype/Message, and a
// field belonging to one line type would blur that boundary. The line is decoded
// a second time instead, which is systemTaskUpdatedLine.Patch's shape.
//
// THE KEY IS snake_case, AND THAT IS THE FINDING #2023 EXISTS TO HAVE BOUGHT.
// The TRANSCRIPT (internal/agentrun/jsonl fixtures) spells this payload
// `toolUseResult`. This package does not read the transcript — it parses
// claude's stdout, spawned --output-format stream-json — and on that surface the
// same payload is keyed `tool_use_result`. A decoder keyed on the camelCase name
// is DEAD CODE here, and no hermetic test built from transcript-lifted fixtures
// can catch it: the fixture would carry the same wrong key and agree with it.
// #2023's first live run searched only the camelCase name and reported
// "sidecar-absent" — literally true, materially the inverse of the truth; the
// re-run recorded a spelling census of {"tool_use_result": 2} against zero
// camelCase on claude 2.1.239. THE RENAME STOPS AT THE ENVELOPE: every key
// INSIDE the sidecar stays camelCase (see sidecarFile). "Correcting" those to
// match this one is the second way to write a decoder that never fires.
//
// json.RawMessage accepts ANY valid JSON value — object, string, number, null —
// which is required, not merely convenient: one captured teardown carries the
// sidecar as the bare string "Error: Exit code 1". emitSlashCommandList's
// objection to a second json.Unmarshal ("it would add a second undecodable
// outcome to classify") does not transfer, because consumeLine has already
// decoded this line once and a RawMessage target cannot fail.
type userToolResultLine struct {
	ToolUseResult json.RawMessage `json:"tool_use_result"`
}

// jsonKey is a presence-only decode target: a *jsonKey field is non-nil exactly
// when its key is present with a non-null value, and retains NO BYTE of it.
//
// That is what lets a shape be recognised by a key whose VALUE must never be
// read into daemon state. `oldString` and `newString` identify an edit and are
// the entire pre- and post-edit text; `content` identifies a write and, on a
// SEARCH sidecar spelled identically, is the whole grep output. Declaring any of
// them as a string to test for presence would put claude's bytes on the decode
// target for no gain — the rule sidecarFile's doc states, applied to keys that
// are discriminators rather than values.
//
// It also answers the present-but-EMPTY question the write shape turns on:
// `structuredPatch: []` is a present non-null value, so it is distinguishable
// from an absent key, which is the pointer-vs-present-zero rule again.
type jsonKey struct{}

// UnmarshalJSON discards the value. encoding/json still scans it for
// well-formedness before calling this, so a malformed value fails the whole
// decode rather than being silently accepted as present.
func (*jsonKey) UnmarshalJSON([]byte) error { return nil }

// toolResultSidecar is the second-stage decode of that sidecar: the SHAPE
// DISCRIMINATOR for all five shapes (#2025 added four to #2024's one). Each
// sidecar shape is self-identifying by its KEY SET, so no correlation back to
// the tool_use and no tool-name switch is needed — #1678's rule again, be driven
// by the available data and not by the names.
//
// IDENTIFY BY THE PRESENCE OF THE NAMED KEYS, NEVER BY AN EXACT KEY SET. 288 of
// 4883 observed shell sidecars (5.9%) carry a sixth key — gitOperation,
// persistedOutputPath, backgroundTaskId and others — and 90 edit/write sidecars
// carry an extra one. An arm keyed on "exactly these five keys" silently loses
// every one of them.
//
// Deliberately NOT keyed on the sidecar's own `type` except where a write's two
// verbs are the news: measured, a read carries type "text" and a write carries
// type "create", so `type` alone identifies nothing. What identifies each shape:
//
//	read    file.numLines AND file.totalLines
//	shell   stdout AND interrupted
//	edit    structuredPatch AND oldString AND newString
//	write   structuredPatch AND content AND type in {create, update}
//	search  mode, then numLines before numFiles
//
// The two structuredPatch arms are separated by their OTHER keys, not by their
// type. Note that numLines here is the SEARCH shape's top-level count; the read's
// identically-spelled one is nested under `file` and is a different field.
//
// Only safe scalars are held by value. Everything else is a *jsonKey, so this
// struct can recognise every shape while holding almost none of claude's bytes.
type toolResultSidecar struct {
	File *sidecarFile `json:"file"`

	Stdout      *jsonKey `json:"stdout"`
	Interrupted *jsonKey `json:"interrupted"`

	StructuredPatch *jsonKey `json:"structuredPatch"`
	OldString       *jsonKey `json:"oldString"`
	NewString       *jsonKey `json:"newString"`
	Content         *jsonKey `json:"content"`
	Type            *string  `json:"type"`

	Mode     *jsonKey `json:"mode"`
	NumLines *int64   `json:"numLines"`
	NumFiles *int64   `json:"numFiles"`
}

// sidecarStdout, sidecarPatch and sidecarContent are the third-stage targets for
// the three arms that need an unbounded claude-supplied value because a count is
// computed from it. Each arm decodes the sidecar AGAIN into its own narrow
// struct rather than sharing fields on toolResultSidecar, and that separation is
// a security control rather than tidiness: `content` is spelled the same on a
// write sidecar (where it is the file being written, and is counted) and on a
// SEARCH sidecar (where it is the whole grep output, and must not be read at
// all). One shared field would put the grep output on the search arm's decode
// target. filePath, originalFile, filenames and stderr are declared on NONE of
// these, so no arm can reach them.
//
// None of these values outlives its composer: every arm returns formatted
// integers and literals, never a sub-slice of what it decoded, so nothing here
// pins the decode's allocation the way a "capped" substring silently would.
type sidecarStdout struct {
	Stdout string `json:"stdout"`
}

type sidecarPatch struct {
	StructuredPatch []patchHunk `json:"structuredPatch"`
}

// patchHunk is one hunk of a structuredPatch, narrowed to its `lines`.
//
// oldStart/oldLines/newStart/newLines are deliberately absent: oldLines and
// newLines are the hunk's SPANS with context lines included, so differencing
// them yields the net change and not "+10 −3". The two numbers can only come
// from the +/- prefixes of `lines`, and an undeclared span cannot be reached for
// by a later edit.
type patchHunk struct {
	Lines []string `json:"lines"`
}

type sidecarContent struct {
	Content string `json:"content"`
}

// minusSign and middleDot are the row separators, and they are CONTRACT rather
// than formatting preference: the client renders this text verbatim. Written as
// literals rather than \u escapes so that a search for either glyph finds its
// declaration; the trailing comment names the codepoint, because a U+002D typed
// in place of the U+2212 would otherwise be invisible in a diff. They are the
// first non-ASCII bytes this field has ever carried — see maxResultDetailBytes,
// which is where that fact is load-bearing.
const (
	minusSign = "−" // MINUS SIGN, NOT U+002D HYPHEN-MINUS
	middleDot = "·" // MIDDLE DOT, spaced on both sides
)

// sidecarFile is the read shape's `file` object, narrowed to its two counts.
//
// WHAT IS ABSENT HERE IS THE SECURITY CONTROL. The observed object also carries
// `content` — THE ENTIRE CONTENTS OF THE FILE CLAUDE READ — and `filePath`, an
// absolute path disclosing the operator's home directory and project layout.
// Neither is declared, so neither can be read into daemon state at all: absent
// from the DECODE TARGET is a stronger guarantee than any test sweep, because a
// field that is never declared cannot leak (systemTaskUpdatedLine's doc states
// the same rule for uuid/session_id). `startLine` is absent for a plainer
// reason: the row names what was returned and what exists, and an offset adds
// nothing to that.
//
// The two fields are POINTERS so an absent key is distinguishable from a present
// zero: a sidecar carrying only one of the two counts is not a read, while 0/0
// is present-and-zero and takes its own arm in readDetail.
//
// They are int64 rather than float64 or any, and that choice is what makes the
// composed string's length STRUCTURAL rather than a second cap to keep correct.
// numLines is a JSON number of arbitrary precision; a count that does not decode
// as a fixed-width whole number — 4.5, "40", a value past int64 — fails the
// decode and sends nothing. Note the camelCase: the envelope rename stops at
// userToolResultLine.
type sidecarFile struct {
	NumLines   *int64 `json:"numLines"`
	TotalLines *int64 `json:"totalLines"`
}

// maxResultDetailBytes is the longest string toolResultDetail can compose across
// ALL FIVE shapes, and it is a DERIVED FACT rather than a cap enforced anywhere
// — nothing checks a composed string against it, because nothing can exceed it.
//
// Every count formats through strconv.FormatInt over a non-negative int64, so
// each is at most 19 digits. Re-derived per form (#2025 added the last four):
//
//	read    19 + len(" of ") + 19 + len(" lines")              = 48
//	edit    len("+") + 19 + len(" ") + len(minusSign) + 19     = 43
//	write   len("updated") + 1 + len(middleDot) + 1 + 19 + 6   = 36
//	shell   19 + len(" lines")                                 = 25
//	search  19 + len(" lines") / 19 + len(" files")            = 25
//
// The read's two-part form is still the longest, so the value does not move.
// WHAT MOVED IS THE ALPHABET: it is digits, spaces, ASCII letters and TWO
// MULTI-BYTE RUNES, minusSign (3 bytes) and middleDot (2), whose lengths are
// already counted above. encoding/json escapes neither — it escapes only HTML
// delimiters, control bytes and U+2028/U+2029 — so 48 bytes is still the wire
// cost. That is why turnbridge maps the field through UNCAPPED while capping its
// ResultSummary neighbour: the bound here is over int64's RANGE, not over
// claude's input length, so no hostile or absurd count can grow the frame.
// Against the ~4.2 KB of headroom turnbridge.maxResultSummaryRunes' doc measures
// on this payload, 48 bytes is under 1.6%, and
// TestToolResultPayload_FitV2EnvelopeCap measures the envelope with the field
// present rather than taking that on trust.
const maxResultDetailBytes = 48

// toolResultDetail composes one tool row's result text from a tool_use_result
// sidecar, or returns "" for every shape it does not recognise. It is the ONLY
// crossing in this package from the sidecar's untrusted bytes into daemon state,
// and it is total: there is no error return, because every failure has the same
// answer.
//
// FIVE ARMS, TRIED IN A FIXED ORDER: read, shell, edit, write, search. The order
// is stated so it is deterministic rather than incidental; the observed key sets
// are disjoint, so no sidecar reaches a second arm having matched a first.
// Falling off the end is what makes the partially-recognised shapes correct for
// free — a structuredPatch matching neither patch arm, a write whose type is
// neither create nor update, a `mode` with no count — each simply runs out of
// arms and sends nothing.
//
// The four arms after the read decode the sidecar a THIRD time into their own
// narrow targets (see sidecarStdout). That is four json.Unmarshal sites where
// #2024 had one, and they stay ONE trust boundary because all of them live in
// this function's arms, none is exported, no arm receives a *slog.Logger, and no
// arm returns a decoded value — every one returns a freshly formatted string.
//
// FAILING CLOSED IS THE DESIGN, NOT A FALLBACK. An absent sidecar, a non-object
// one, and an object with no read keys are the same case — send nothing. About
// 5% of calls are tools with no meaningful count and they should have none, and
// a shape this decoder does not recognise costs a missing count, never a wrong
// one. Nothing here emits an Unrecognized, and that is a CONFINEMENT control
// rather than a tidiness one: emitUnrecognized does not merely log, it puts the
// offending bytes on the wire as turnevent.Unrecognized.Raw (cut by
// truncateRaw), so surfacing an unrecognised sidecar that way would ship the
// first maxUnrecognizedRaw bytes of every file claude reads to the phone.
//
// NO RELATIONAL VALIDATION. The daemon does not second-guess claude's
// arithmetic: it requires only that both counts decode as non-negative whole
// numbers and picks the form by whether they are equal, so a returned count
// larger than its total renders as given. Inventing a validation rule for a
// shape we have few observations of is what #1380 declined to do, and
// systemTaskUpdatedLine.Patch carries the same reasoning in this file.
//
// The composed string contains NO claude-supplied byte — every arm returns
// formatted integers and literals — so it has no injection surface and, unlike a
// "capped" substring, retains no sub-slice of the decoded sidecar to pin its
// allocation.
func toolResultDetail(sidecar json.RawMessage) string {
	if len(sidecar) == 0 {
		return ""
	}
	var sc toolResultSidecar
	// The error is deliberately swallowed rather than classified. The line has
	// already decoded once, so this cannot be news; see the fail-closed note above.
	if err := json.Unmarshal(sidecar, &sc); err != nil {
		return ""
	}
	if d := readDetail(sc.File); d != "" {
		return d
	}
	if d := shellDetail(&sc, sidecar); d != "" {
		return d
	}
	if d := editDetail(&sc, sidecar); d != "" {
		return d
	}
	if d := writeDetail(&sc, sidecar); d != "" {
		return d
	}
	return searchDetail(&sc)
}

// readDetail composes a read's row text: "265 lines", or "110 of 1676 lines"
// when only a slice of a longer file was returned.
//
// NO RELATIONAL VALIDATION, per toolResultDetail's contract: a returned count
// larger than its total renders as given.
func readDetail(f *sidecarFile) string {
	if f == nil || f.NumLines == nil || f.TotalLines == nil {
		return ""
	}
	n, total := *f.NumLines, *f.TotalLines
	if n < 0 || total < 0 {
		return ""
	}
	if n == 0 && total == 0 {
		// A read that returned no lines from an empty file has nothing worth
		// saying. A zero returned count against a NON-zero total is not this case
		// and still sends both, because "0 of 1676 lines" says something true.
		return ""
	}
	if n == total {
		// Not pluralised: "1 lines" is the form. #1794 measured the shortest value
		// at 7 characters, which is "4 lines", so the family's own measurement is
		// of the unpluralised form and a singular arm would be a rule the design
		// does not carry.
		return strconv.FormatInt(n, 10) + " lines"
	}
	// Returned before total — the whole point of the two-part form is telling at a
	// glance that only a slice of a longer file was seen. Reads are partial 56.5%
	// of the time across 93227 measured calls.
	return strconv.FormatInt(n, 10) + " of " + strconv.FormatInt(total, 10) + " lines"
}

// shellDetail composes a shell call's row text from the line count of STDOUT.
//
// stderr is not counted into it and is not declared anywhere, so it cannot be:
// the confinement rule doing double duty as a correctness one. Empty stdout
// sends NO count rather than "0 lines" — a call that produced nothing has
// nothing worth saying, which is the fail-closed principle the dispatch already
// applies. The write arm makes the opposite call for a stated reason.
func shellDetail(sc *toolResultSidecar, sidecar json.RawMessage) string {
	if sc.Stdout == nil || sc.Interrupted == nil {
		return ""
	}
	var out sidecarStdout
	if err := json.Unmarshal(sidecar, &out); err != nil {
		return ""
	}
	n := countLines(out.Stdout)
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10) + " lines"
}

// editDetail composes an edit's row text — "+10 −3", either half omitted when it
// is zero, nothing when both are.
//
// THE TWO NUMBERS CAN ONLY COME FROM THE +/- PREFIXES OF THE HUNKS' LINES; see
// patchHunk for why the hunk spans cannot give them. Those prefixes are the
// diff's own ASCII bytes and are read as data: the U+2212 in the output is
// display text this function composes, never a byte that came from claude.
//
// Both halves zero is unobserved — of 632 measured edits, 425 changed both
// halves, 190 added only and 17 removed only — so dropping it is a fail-closed
// decision about a shape with no observations, not a measured rule.
func editDetail(sc *toolResultSidecar, sidecar json.RawMessage) string {
	if sc.StructuredPatch == nil || sc.OldString == nil || sc.NewString == nil {
		return ""
	}
	var p sidecarPatch
	if err := json.Unmarshal(sidecar, &p); err != nil {
		return ""
	}
	var added, removed int64
	for _, h := range p.StructuredPatch {
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
		}
	}
	switch {
	case added > 0 && removed > 0:
		return "+" + strconv.FormatInt(added, 10) + " " + minusSign + strconv.FormatInt(removed, 10)
	case added > 0:
		return "+" + strconv.FormatInt(added, 10)
	case removed > 0:
		return minusSign + strconv.FormatInt(removed, 10)
	}
	return ""
}

// writeDetail composes a write's row text — "created · 54 lines".
//
// The `type` is checked BEFORE the content is decoded, so a verb this row cannot
// name never materialises the file being written. structuredPatch is required to
// be PRESENT but not non-empty: it is [] on every one of the 240 observed
// creates, so an arm demanding hunks would be dead code on 97% of writes.
//
// An empty content still sends "created · 0 lines", which is the opposite of the
// shell arm's empty-stdout drop and deliberately so: here the VERB is the news,
// and an empty file that was created is something that happened.
func writeDetail(sc *toolResultSidecar, sidecar json.RawMessage) string {
	if sc.StructuredPatch == nil || sc.Content == nil || sc.Type == nil {
		return ""
	}
	var verb string
	switch *sc.Type {
	case "create":
		verb = "created"
	case "update":
		verb = "updated"
	default:
		return ""
	}
	var c sidecarContent
	if err := json.Unmarshal(sidecar, &c); err != nil {
		return ""
	}
	return verb + " " + middleDot + " " + strconv.FormatInt(countLines(c.Content), 10) + " lines"
}

// searchDetail composes a search's row text — "78 lines" when the call asked for
// lines, "5 files" when it asked for files.
//
// numLines takes precedence by PRESENCE, and a present-but-invalid one sends
// nothing rather than falling back to numFiles: a fallback would let a malformed
// field silently change which quantity the row reports, and a wrong count is the
// one outcome this design refuses.
//
// One number, not the read's two-part form, even though totalLines rides along
// on all 200 observed numLines shapes. The read sends both because reads are
// partial 56.5% of the time and a partial read is a surprise; no equivalent has
// been measured for search.
func searchDetail(sc *toolResultSidecar) string {
	if sc.Mode == nil {
		return ""
	}
	switch {
	case sc.NumLines != nil:
		if *sc.NumLines < 0 {
			return ""
		}
		return strconv.FormatInt(*sc.NumLines, 10) + " lines"
	case sc.NumFiles != nil:
		if *sc.NumFiles < 0 {
			return ""
		}
		return strconv.FormatInt(*sc.NumFiles, 10) + " files"
	}
	return ""
}

// countLines pins the counting rule for the two arms that COUNT rather than
// being told: a trailing newline does not add a line, so "a\nb\n" and "a\nb" are
// both 2, and "" is 0. The read and search arms take their numbers from claude
// and never come here.
//
// It returns an int64 rather than a sub-slice or a []string, which is what keeps
// the composed string from pinning the decoded value's allocation.
func countLines(s string) int64 {
	if s == "" {
		return 0
	}
	n := int64(strings.Count(s, "\n"))
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// countToolResultBlocks counts the tool_result blocks of one user message.
//
// It decodes each block into a type-only struct rather than reusing streamBlock,
// whose Content is `any` and would materialise a whole file read's text just to
// read a type. Its caller gates on having a count to place at all, so the ~95%
// of lines with no count pay nothing for this.
//
// A block that will not decode is not counted: it is not a tool_result as far as
// anything here can tell, and the main loop in emitUser still surfaces it as an
// Unrecognized on its own terms.
func countToolResultBlocks(msg *streamMessage) int {
	n := 0
	for _, raw := range msg.Content {
		var block struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			continue
		}
		if block.Type == "tool_result" {
			n++
		}
	}
	return n
}

// consumeLine decodes one complete line and emits the events it maps to. A blank
// line emits nothing. A line on the measured known-ignored list emits nothing and
// is Debug-logged by type only — never content. A line that fails to decode, and
// a line of a type outside both the mapped set and the ignored list, emits a
// turnevent.Unrecognized so the drop is VISIBLE to a client instead of dying in a
// debug log the production daemon does not print. Either way one bad line never
// poisons later lines.
func (p *Parser) consumeLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var sl streamLine
	if err := json.Unmarshal(line, &sl); err != nil {
		// Not even the type is known here, so Kind stays empty. Previously this
		// dropped without recording anything at all.
		p.emitUnrecognized(turnevent.UnrecognizedUndecodable, "", line)
		return
	}
	switch sl.Type {
	case "assistant":
		p.emitAssistant(sl.Message)
	case "user":
		// The line bytes ride along so emitUser can decode the tool_use_result
		// sidecar, a sibling of `message` rather than a field inside it (#2024).
		p.emitUser(sl.Message, line)
	case "result":
		// The turn boundary. The subtype selects the reason: an interrupted turn
		// (subtype error_during_execution, spike T1 #1075) → cancelled; a clean
		// turn and every other subtype → end_turn. Richer max_tokens/refusal
		// classification remains future work (resultTurnEndReason's default).
		//
		// Also the ONE reset point for the parser's only accumulator (#1385).
		// Unconditional and before the emit, so both result subtypes reset and a
		// cancelled turn leaks no residual into the next one. A child that dies
		// WITHOUT a result leaves a residual behind on a long-lived parser
		// (cmd/pyry builds one per session, not per turn); that is bounded by the
		// invariant at the field — the next turn's first event can arrive at most
		// minThinkingTokensPerEvent-1 tokens early — and a second reset path for it
		// would buy a second boundary to keep correct, which is what having one
		// boundary avoids.
		p.thinkingSinceEmit = 0
		p.emit(turnevent.TurnEnd{Reason: resultTurnEndReason(sl.Subtype)})
	case "rate_limit_event":
		// Its own arm rather than an ignoredLineTypes member with a subtype
		// carve-out (#1404). ignoredLineTypes is documented as top-level types only,
		// and its own census measures this one as carrying no subtype at all, so
		// there is nothing for emitSystemSubtype's dispatch to match on. Two
		// consequences worth naming: emitUnrecognized stays unreachable for this type
		// BY MATCHING rather than by list membership, which is the stronger of the
		// two guarantees; and emitRateLimit needs no bool return, unlike the
		// emitSystemSubtype family, because matching this case IS consuming the line
		// and there is no "did you handle it?" to report back.
		p.emitRateLimit(line)
	case "control_response":
		// The reply the DAEMON ITSELF solicited (#1500). Interrupt on this path is a
		// stdin control_request and claude answers it ~40ms later on the same stdout
		// the parser reads — see Runner.Interrupt and marshalInterruptEnvelope for the
		// request side, WriteInitialize and marshalInitializeEnvelope for the
		// initialize one. Without this arm every interrupt put an unrecognized_message
		// row on the phone, which is the one frame whose whole value is meaning
		// "claude started emitting something NEW"; firing it on a routine user action
		// spends that meaning. So consuming this is not tolerating a stranger's line,
		// it is the daemon not alarming about its own.
		//
		// Its own arm rather than an ignoredLineTypes member, for #1404's reasons
		// verbatim: that list is documented as top-level types only and as MEASURED
		// (the 2026-07-27 census, which never interrupted and so never saw this type),
		// and emitUnrecognized stays unreachable here BY MATCHING rather than by list
		// membership — the stronger of the two guarantees. emitModelList holds that
		// guarantee on every rung, malformed payloads included.
		//
		// Shape authority is the verbatim capture in
		// docs/knowledge/features/set-permission-mode-inband-probe.md for the two ack
		// shapes and internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json
		// for the initialize one, and both invert the request side: `subtype` and
		// `request_id` are nested UNDER `response`, not top-level, so streamLine.Subtype
		// decodes empty and there is nothing for an emitSystemSubtype-shaped dispatch
		// to match on.
		//
		// CORRECTED 2026-08-26 (#1811): this arm no longer reads nothing below the
		// top-level `type`, and the consequence that choice justified has expired with
		// it. emitModelList decodes the nested shape, so a subtype:"error" NAK is no
		// longer consumed INDISTINGUISHABLY from a success — the decode target whose
		// cost the old reasoning weighed against discriminating one now exists for the
		// initialize payload's sake, and the discrimination is one comparison against a
		// daemon-authored constant. What has NOT changed is the BEHAVIOUR for every
		// response that is not the initialize reply: an interrupt ack, a
		// set_permission_mode ack and a NAK are each still consumed content-free, one
		// record and no event, whether they carry an inner payload or none.
		p.emitModelList(line)
	default:
		if ignoredLineTypes[sl.Type] {
			// The subtype match lives INSIDE this branch, which is what keeps
			// emitUnrecognized below structurally unreachable from any system line.
			//
			// CORRECTED 2026-08-09 (#1404): the sl.Type guard's stated reason used to
			// be rate_limit_event — the one list member carrying no subtype — and that
			// member now has its own case arm above. The guard STAYS, and its reason is
			// the general one it always really was: it scopes the subtype match to
			// `system` BY CONSTRUCTION, so a future list member whose payload happens
			// to carry a colliding subtype name cannot reach the wrong emitter.
			// Deleting it while the list has one member would make that property rest
			// on the list's current size, and a one-member list is a transient state,
			// not the design.
			if sl.Type == "system" && p.emitSystemSubtype(sl.Subtype, line) {
				return
			}
			// system (status / any subtype emitSystemSubtype does not map): tolerated
			// and dropped, silently. CORRECTED 2026-08-07 (#1380) — it is no longer
			// "exactly as before": the subtypes emitSystemSubtype maps become events
			// above.
			//
			// CORRECTED 2026-08-09 (#1385): thinking_tokens is no longer among the
			// examples here — it is MAPPED now (→ turnevent.ThinkingProgress), so
			// naming it as dropped became false the moment that arm landed. The list
			// above is illustrative and the authority is emitSystemSubtype's case
			// arms.
			//
			// CORRECTED 2026-08-19 (#1600): init has left the examples for the same
			// reason — it maps to turnevent.ModelAnnounced now, so a model-carrying init
			// no longer reaches this Debug at all and a model-less one is consumed
			// silently by that arm. status is the one measured-and-dropped subtype left
			// standing.
			//
			// system/init is a per-turn marker (spike § 1), not a session-open event,
			// and it is still NOT the accumulator's boundary. The parser is no longer
			// wholly turn-stateless: it holds one accumulator (#1385), whose boundary is
			// the `result` arm above. Resetting on init as well would give one piece of
			// state two boundaries to keep agreeing, which is the cost having a single
			// boundary avoids — and mapping the line changed what is SENT, not where
			// that boundary is. See ignoredLineTypes for the full statement and the
			// measurement behind the list.
			p.log.Debug("streamsup: dropping stdout line", "type", sl.Type)
			return
		}
		// A type we have never seen. Surface it: this is the one arm that tells
		// anyone claude started emitting something new.
		p.emitUnrecognized(turnevent.UnrecognizedLineType, sl.Type, line)
	}
}

// emitSystemSubtype maps one system line's subtype, reporting whether it
// CONSUMED the line. Called from consumeLine's ignoredLineTypes branch, so a
// subtype the switch does not match (false) falls through to that branch's
// existing content-free drop.
//
// The case arms are the ONE enumeration of the mapped set. Every comment that
// describes the drop rule points here instead of restating it, because none of
// them fails a build when it goes stale and adding a subtype IS adding a case.
//
// Unknown system subtypes fall through to silence DELIBERATELY, not by
// oversight. Surfacing them would reintroduce the per-turn noise row the
// 2026-07-27 measurement forbade — system is claude's highest-rate emitter — and
// would break the live zero-unrecognized gate
// (internal/e2e/realclaude/`drainForCompletedTurn` fatals on one)
// the next time a claude release adds a chatty subtype.
func (p *Parser) emitSystemSubtype(subtype string, line []byte) bool {
	switch subtype {
	case "task_started":
		return p.emitBackgroundTaskStarted(line)
	case "task_updated":
		return p.emitBackgroundTaskUpdated(line)
	case "background_tasks_changed":
		return p.emitBackgroundTaskRoster(line)
	case "thinking_tokens":
		return p.emitThinkingProgress(line)
	case "init":
		return p.emitModelAnnounced(line)
	default:
		return false
	}
}

// emitBackgroundTaskStarted decodes a system/task_started line and emits one
// turnevent.BackgroundTaskStarted, reporting that it consumed the line either
// way. Field mapping and cap numbers come from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never from a
// hand-built payload.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// streamLine's doc states the property that makes a tool result whose text is
// literally `{"type":"result"}` unable to forge a turn boundary: control shapes
// are read from the top level only, and nested content is never re-scanned.
// Decoding this payload from anywhere else would make a background task forgeable
// out of claude's own tool output.
//
// A payload that will not decode into the shape (a numeric task_id, say) is
// dropped with a content-free Debug and no event — NOT surfaced as an
// Unrecognized. Keeping system whole on ignoredLineTypes is what makes "no system
// line reaches the unrecognized lane" structural, and that guarantee is worth
// more than surfacing a malformed line of a subtype we already know. A missing
// field is not an error either: absence is claude's to choose, there is no
// captured negative case, so the field lands empty rather than inventing a
// validation rule.
func (p *Parser) emitBackgroundTaskStarted(line []byte) bool {
	var tl systemTaskStartedLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. None of the decoded fields is logged.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "task_started")
		return true
	}

	var cut []string
	bound := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// Sequential statements rather than a composite literal: TruncatedFields is
	// ordered by these calls, and inside a literal that order would rest on the
	// left-to-right operand rule rather than on something a reader sees. The names
	// are the DAEMON's — tool_call_id, not claude's tool_use_id.
	taskID := bound(tl.TaskID, "task_id", maxTaskFieldID)
	toolCallID := bound(tl.ToolUseID, "tool_call_id", maxTaskFieldID)
	description := bound(tl.Description, "description", maxTaskDescription)
	taskType := bound(tl.TaskType, "task_type", maxTaskFieldID)

	p.emit(turnevent.BackgroundTaskStarted{
		TaskID:      taskID,
		ToolCallID:  toolCallID,
		Description: description,
		TaskType:    taskType,
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
	return true
}

// emitBackgroundTaskUpdated decodes a system/task_updated line and emits one
// turnevent.BackgroundTaskUpdated, reporting that it consumed the line either
// way. Peer of emitBackgroundTaskStarted, and its every structural choice is the
// same one for the same reason. Field mapping comes from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never from a
// hand-built payload.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// That is not a call-shape convention: streamLine's doc states the property it
// preserves, that control shapes are read from the top level only and nested
// content is never re-scanned, which is what stops a tool result whose text is
// literally `{"type":"result"}` from forging a turn boundary. Decoding this
// payload from anywhere else would make a background-task update forgeable out
// of claude's own tool output.
//
// A payload that will not decode into the shape (a numeric task_id, say) is
// dropped with a content-free Debug and no event — NOT surfaced as an
// Unrecognized, because keeping system whole on ignoredLineTypes is what makes
// "no system line reaches the unrecognized lane" structural. An absent patch is
// not an error either: absence is claude's to choose, so the field lands empty
// rather than inventing a validation rule.
func (p *Parser) emitBackgroundTaskUpdated(line []byte) bool {
	var tl systemTaskUpdatedLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. Neither the decoded fields nor the patch is logged, and the patch
		// is the thing this handler is most tempted to explain itself with.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "task_updated")
		return true
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
	// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these
	// calls, and inside a literal that order would rest on the left-to-right
	// operand rule rather than on something a reader sees. Neither name is
	// translated here — claude's keys and the daemon's fields agree.
	//
	// string(tl.Patch) is "" when claude omits the key, which is the empty-Patch
	// contract; json.RawMessage COPIES its input on decode, so this does not alias
	// p.buf and nothing outlives the buffer it came from.
	taskID := bound(tl.TaskID, "task_id", maxTaskFieldID)
	patch := bound(string(tl.Patch), "patch", maxTaskPatch)

	p.emit(turnevent.BackgroundTaskUpdated{
		TaskID: taskID,
		Patch:  patch,
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
	return true
}

// emitBackgroundTaskRoster decodes a system/background_tasks_changed line and
// emits one turnevent.BackgroundTaskRoster, reporting that it consumed the line
// either way. Peer of emitBackgroundTaskUpdated, and its every structural choice
// is the same one for the same reason. Field mapping comes from the committed
// capture (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never
// from a hand-built payload; both bounds come from lines synthesized to exceed
// them, which the capture's single 212-byte entry cannot.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, and
// the reason matters more on this subtype than on either sibling. streamLine's
// doc states the property it preserves: control shapes are read from the top
// level only and nested content is never re-scanned, which is what stops a tool
// result whose text is literally `{"type":"result"}` from forging a turn
// boundary. Decoding this payload from anywhere else would make a whole
// background-task roster forgeable out of claude's own tool output — and this is
// the most valuable variant to forge, because it is the one that claims what is
// ALIVE.
//
// A payload that will not decode into the shape (tasks as an object, say) is
// dropped with a content-free Debug and no event — NOT surfaced as an
// Unrecognized, because keeping system whole on ignoredLineTypes is what makes
// "no system line reaches the unrecognized lane" structural. An absent or empty
// tasks array is not an error either: an EMPTY roster is the signal that nothing
// is alive, so the event is still emitted rather than dropped.
//
// No terminal, finish, or completion event is synthesized here or anywhere —
// see turnevent.BackgroundTaskRoster's doc. The parser holding no ROSTER and no
// per-task memory is that refusal's enforcement mechanism, not an incidental
// property: detecting a task's disappearance would require remembering the
// previous roster.
//
// CORRECTED 2026-08-09 (#1385): the enforcement is stated above in terms of what
// the parser remembers about TASKS, because the broader claim this sentence used
// to make — that the parser holds no cross-line state at all — is no longer
// literally true. It now holds one int, thinkingSinceEmit, a token counter reset
// at the turn boundary. That does not weaken the refusal by a step: the counter
// remembers no task, no roster, and nothing any line said, so nothing about it
// brings a synthesized finish event any closer to being derivable.
func (p *Parser) emitBackgroundTaskRoster(line []byte) bool {
	var tl systemBackgroundTasksLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. Nothing decoded is logged, and neither is the entry COUNT: a
		// roster is a list, lists read as diagnostics, and "just the length" is the
		// leak a content-free rule is most often bent for.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "background_tasks_changed")
		return true
	}

	// The COUNT bound runs before the loop, and truncation is FROM THE TAIL:
	// claude's order is preserved because no ranking is invented, its ordering
	// semantics being unobserved.
	entries := tl.Tasks
	var dropped int
	if len(entries) > maxTaskRosterEntries {
		dropped = len(entries) - maxTaskRosterEntries
		entries = entries[:maxTaskRosterEntries]
	}

	// nil for an empty or absent array: append never runs, which is the contract
	// BackgroundTaskRoster.Tasks states.
	var tasks []turnevent.BackgroundTask
	for _, entry := range entries {
		// The TEXT bound is per entry, so `cut` is per entry — which is the whole
		// reason this closure cannot be hoisted out of the loop.
		var cut []string
		bound := func(value, name string, limit int) string {
			out, truncated := truncateField(value, limit)
			if truncated {
				cut = append(cut, name)
			}
			return out
		}
		// Sequential statements rather than a composite literal, for
		// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these
		// calls, and inside a literal that order would rest on the left-to-right
		// operand rule rather than on something a reader sees. No name is translated
		// — claude's keys and the daemon's fields agree on this subtype.
		taskID := bound(entry.TaskID, "task_id", maxTaskFieldID)
		taskType := bound(entry.TaskType, "task_type", maxTaskFieldID)
		description := bound(entry.Description, "description", maxTaskRosterDescription)

		tasks = append(tasks, turnevent.BackgroundTask{
			TaskID:      taskID,
			TaskType:    taskType,
			Description: description,
			// nil when nothing was cut: append never ran.
			TruncatedFields: cut,
		})
	}

	// Each dimension reports where it happens: the text cut rides its entry, the
	// count rides the event. A count folded into a top-level TruncatedFields
	// naming "tasks" would lose HOW MANY were lost.
	p.emit(turnevent.BackgroundTaskRoster{
		Tasks:        tasks,
		DroppedTasks: dropped,
	})
	return true
}

// emitThinkingProgress decodes a system/thinking_tokens line and emits at most
// one turnevent.ThinkingProgress, reporting that it consumed the line either way.
// Field mapping and the bound come from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never from a
// hand-built payload.
//
// Unlike its three siblings this mapping is NOT a pure function of one line: it
// is rate-bounded, so most lines accumulate and emit nothing. The rule is
//
//	d <= 0                -> consume, no event               (guard)
//	d >= bound - acc      -> emit the line's two values; acc = 0
//	otherwise             -> acc += d
//
// with acc reset at the `result` arm in consumeLine. >= rather than > because a
// line landing exactly on the bound has delivered the full quantum.
//
// WRITE THE CROSSING TEST SUBTRACTED, NEVER ADDITIVELY. `acc += d; if acc >=
// bound` is arithmetically identical for every value that fits and OVERFLOWS for
// one that does not: estimated_tokens_delta 9223372036854775807 decodes into int
// cleanly, and on a nonzero residual acc+d wraps to roughly -2^63. The comparison
// then reads false, the accumulator lands where no realistic delta climbs out of,
// and the turn's liveness signal is silently dead until the next `result` — an
// unrecoverable state in the one feature whose purpose is making a wedged turn
// visible, from a single malformed or version-drifted line. Subtracted, both
// operands of `bound - acc` are in [1, bound] by the field's invariant, so no
// expression here can overflow at all: the failure is unrepresentable rather than
// guarded against. This is the obvious refactor to get wrong, which is why
// TestParser_ThinkingAccumulatorSurvivesAnExtremeDelta pins it — and why its
// discriminating assertion is the SECOND event after the extreme line, not the
// first.
//
// The d <= 0 guard is not a defence against an unobserved claude bug. A negative
// delta would drive the accumulator down and could leave the bound uncrossable
// for the rest of the turn — SILENCE, which is the single outcome the no-silent-
// burst property forbids. One comparison keeps that property true for inputs the
// capture does not constrain.
//
// NO SILENT BURST, by construction rather than by tuning. claude's cumulative
// counter restarts at every inference request, so a rule keyed on its high-water
// mark emits nothing for a burst that never exceeds an earlier peak (the
// capture's burst 2 tops out below burst 1's). Keying on the DELTA is immune: the
// accumulator only ever grows within a turn, so any burst whose own delta total
// reaches the bound emits at least once regardless of the residual it inherited —
// a residual can only bring the emit FORWARD. Every observed burst clears the
// bound with roughly 2x to spare.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, and
// the reason is sharper here than on any sibling. streamLine's doc states the
// property it preserves: control shapes are read from the top level only and
// nested content is never re-scanned, which is what stops a tool result whose
// text is literally `{"type":"result"}` from forging a turn boundary. This event
// is a LIVENESS CLAIM, so decoding it from anywhere else would let claude's own
// tool output assert that the daemon is alive while it is wedged.
//
// A payload that will not decode into the shape (a string estimated_tokens, say,
// or a number too large for int) is dropped with a content-free Debug and no
// event — NOT surfaced as an Unrecognized, because keeping system whole on
// ignoredLineTypes is what makes "no system line reaches the unrecognized lane"
// structural. NOTHING NUMERIC IS LOGGED on either path: the package rule is that
// nothing derived from claude's output reaches a log, and a token count is
// exactly the "it's just a number" exception that rule gets bent for first —
// emitBackgroundTaskRoster's doc refuses the identical bend for the roster's
// entry count.
func (p *Parser) emitThinkingProgress(line []byte) bool {
	var tl systemThinkingTokensLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. Neither token number is logged, and the count is the thing this
		// handler is most tempted to explain itself with.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "thinking_tokens")
		return true
	}

	// Absent, zero and negative all land here and are treated identically: no
	// progress to report, and nothing accumulated.
	if tl.EstimatedTokensDelta <= 0 {
		return true
	}
	// The COMPLEMENT of the doc's `d >= bound - acc -> emit`, written this way so
	// the accumulate branch returns early and the emit is the function's tail.
	// Subtracted, never additive — see the doc above. `bound - acc` is in
	// [1, bound] by the invariant at thinkingSinceEmit, so it cannot overflow
	// whatever claude sends; and the addition below is reached only when
	// d < bound - acc, which is exactly the condition making acc + d < bound, so
	// it cannot overflow either and the invariant is restored on the way out.
	if tl.EstimatedTokensDelta < minThinkingTokensPerEvent-p.thinkingSinceEmit {
		p.thinkingSinceEmit += tl.EstimatedTokensDelta
		return true
	}

	p.thinkingSinceEmit = 0
	// The line's OWN values, not the accumulated total: the event stays a pure
	// function of the line that produced it, and the accumulator's residue is a
	// documented consumer hazard on turnevent.ThinkingProgress rather than a
	// number invented here. No caps — two ints cannot blow the envelope.
	p.emit(turnevent.ThinkingProgress{
		EstimatedTokens:      tl.EstimatedTokens,
		EstimatedTokensDelta: tl.EstimatedTokensDelta,
	})
	return true
}

// emitRateLimit decodes one top-level rate_limit_event line and emits at most one
// turnevent.RateLimited. It never emits an Unrecognized, and it returns nothing:
// consumeLine's case arm consumes the line by MATCHING, so unlike the
// emitSystemSubtype family there is no "did you handle it?" to report back. Field
// mapping comes from the committed captures, never from a hand-built payload.
//
// THE GATE is the substance of this mapping; the field copying is routine. claude
// emits this line ONCE PER RUN whatever the state of the usage-limit window —
// status read "allowed" in all three captures on record, i.e. every run that
// produced one hit no limit at all — so a 1:1 mapping would put one "you are rate
// limited" event on every healthy turn. status is the discriminator, and it has
// three reachable readings:
//
//  1. status == benignRateLimitStatus → SILENCE. The measured healthy case.
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
//     rung-3-silent produces a false NEGATIVE on a condition that has never fired
//     once in three captures. And it is this package's own precedent for an absent
//     field: emitBackgroundTaskStarted lands the field empty rather than inventing
//     a validation rule, and landing empty on the GATE INPUT means the gate reads
//     "no report was made", which is silence.
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
		p.log.Debug(rateLimitDropMsg, "reason", rateLimitDropBenign)
		return
	case "":
		// Rung 3. An absent container, a present-but-empty one, and a container
		// carrying no status all land here and are answered identically — which is
		// what makes the plain-struct decode target sufficient.
		p.log.Debug(rateLimitDropMsg, "reason", rateLimitDropNoInfo)
		return
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
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
}

// emitModelAnnounced decodes a system/init line and emits AT MOST ONE
// turnevent.ModelAnnounced, reporting that it CONSUMED the line either way. Field
// mapping comes from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never from a
// hand-built payload.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// streamLine's doc states the property it preserves: control shapes are read from
// the top level only and nested content is never re-scanned, which is what stops a
// tool result whose text is literally `{"type":"result"}` from forging a turn
// boundary. Decoding this payload from anywhere else would let claude's own tool
// output announce a model the daemon never ran.
//
// AN EMPTY model SUPPRESSES the event, and that DIVERGES from the task handlers,
// which treat a missing field as claude's choice and emit with the field empty.
// Here the model IS the whole payload: an event carrying an empty one would assert
// "claude announced a model" while naming none, which is a claim the line did not
// make. emitRateLimit's `case ""` rung is the precedent, and its formulation
// carries over — absence, a present-but-empty value, and a line carrying no such
// key all land here and are answered identically, which is what makes a plain
// string decode target sufficient.
//
// THAT DROP IS SILENT, unlike emitRateLimit's rungs, which each log a
// daemon-authored reason keyword. emitRateLimit fires once per RUN and has three
// distinguishable rungs; init fires once per TURN and has one non-undecodable drop
// reason, so a Debug there would put a record in the daemon log on every turn —
// reinstating in the log the per-turn noise row
// TestParser_IgnoredLineTypesStaySilent's doc exists to prevent in the event
// stream. emitThinkingProgress's `delta <= 0` arm is the precedent: consumed,
// silent, no log. The observable consequence is worth naming rather than
// discovering: before this arm every init produced one "streamsup: dropping stdout
// line" record per turn from the branch above, and after it a model-carrying init
// produces no record at all and a model-less one produces none either. That is one
// FEWER Debug per turn.
//
// Nothing is surfaced as an Unrecognized on any path, because keeping `system`
// whole on ignoredLineTypes is what makes "no system line reaches the unrecognized
// lane" structural, and that guarantee is worth more than surfacing a malformed
// line of a subtype we already know.
func (p *Parser) emitModelAnnounced(line []byte) bool {
	var il systemInitLine
	if err := json.Unmarshal(line, &il); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content, and the message is byte-identical to the task siblings' arms.
		//
		// The err is deliberately NOT logged, and this is the sharpest instance of
		// that rule in the package: encoding/json QUOTES the offending input bytes
		// into its error text, so `"err", err` on a line whose model is long or
		// revealing would put that value in the daemon log through a channel no
		// per-path attribute check can see. It is precisely the value #833's posture
		// — restated across internal/relay's v2session_settings.go and
		// internal/sessions' pool.go as "model / effort / YOLO values are NEVER logged
		// at any level" — exists to keep out. The house idiom points the other way
		// (CLAUDE.md: wrap errors with context), which is why it is stated here rather
		// than assumed; cmd/pyry's emit marshal-error path says it outright, and both
		// task siblings' arms already follow it.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "init")
		return true
	}
	// Absent, present-but-empty, and a line carrying no such key all land here and
	// are answered identically.
	if il.Model == "" {
		return true
	}
	// No `bound` closure and no sequential-statements rule: one field means there is
	// no TruncatedFields ORDER for a composite literal to decide, which is the only
	// thing that rule protects.
	model, truncated := truncateField(il.Model, maxModelField)
	p.emit(turnevent.ModelAnnounced{
		// claude's value VERBATIM: no lowercasing, no alias expansion, no
		// date-stamping, no family mapping, no lookup against any published model
		// list. The cap is the only judgement made about it here — see the field's
		// doc for why repairing it would be inventing rather than reporting.
		Model:     model,
		Truncated: truncated,
	})
	return true
}

// emitModelList decodes one top-level control_response line and emits AT MOST ONE
// turnevent.ModelList, from one rung, and AT MOST ONE turnevent.SlashCommandList, from
// either of two rungs. The two are decided INDEPENDENTLY under one shared subtype gate.
// It never emits an Unrecognized, and it returns nothing: consumeLine's case arm
// consumes the line by MATCHING, exactly as emitRateLimit's does, so unlike the
// emitSystemSubtype family there is no "did you handle it?" to report back. Field
// mapping comes from the committed capture, read in the tests through
// capturedInitializePayload, never from a hand-built payload.
//
// THE DISCRIMINANT is the substance of this mapping; the field copying is routine.
// claude answers three different requests on this one line type and only one of the
// replies carries a payload the daemon reads, so there is ONE SHARED PRECONDITION and
// then TWO INDEPENDENT DECISIONS. The precondition is
// subtype == controlResponseSuccess. Below it the two arrays are read as a LATTICE
// rather than a conjunction: a non-empty decoded models array decides the ModelList, a
// non-empty decoded commands array decides the SlashCommandList, and NEITHER GATES THE
// OTHER. It was a conjunction until #1891, when the commands-only corner stopped being
// a non-emitting one.
//
//   - The subtype half is what stops a payload being read out of a response that
//     reported FAILURE, and it is unchanged by the split into two decisions: it is
//     the precondition of BOTH emits. It is what makes the classification total as
//     well: every subtype that is not success — including an absent one — lands on
//     the nak rung rather than falling through to a shape test.
//   - The models half alone already excludes all three sibling shapes on record: a
//     set_permission_mode success (whose inner response is `{"mode":"default"}`), a
//     set_permission_mode NAK (which carries an `error` string and no inner
//     response), and an interrupt ack (which carries no inner response at all).
//     None of them has the key. That is a claim about which KEYS those shapes carry,
//     so it is scoped to the MODELS emit and does not transfer to the second one by
//     inheritance.
//   - The commands half needs the same exclusion ESTABLISHED, not borrowed, and it
//     holds: none of those three shapes carries a `commands` key either — the
//     set_permission_mode success has `mode` alone, the NAK has `error` and no inner
//     response, the interrupt ack has no inner response at all. So a non-empty
//     commands array excludes all three on record exactly as a non-empty models one
//     does, per key rather than by analogy.
//
// SO THE EMIT PARTITION IS NOT THE RUNG PARTITION: rungs 1-3 emit nothing, rung 4
// emits one SlashCommandList, and rung 5 emits one ModelList and, under its second
// gate, at most one SlashCommandList beside it. Exactly ONE rung can emit a ModelList;
// TWO can emit a SlashCommandList.
//
// IT RECOGNISES A SHAPE, NOT A CORRELATED REPLY, and the limit is written here so a
// later reader does not infer more than the code claims. This says "the line
// carries a success-subtype model list", not "this is the reply to the initialize
// request THIS daemon sent": claude authors the inner response object on every
// control response, so a future claude putting a models array inside some other ack
// would have that ack read as an inventory.
//
// WHERE A MIS-READ INVENTORY NOW GOES, written out because it used to go nowhere
// and that absence was once the whole bound. cmd/pyry's sessionModelHold retains it
// as the session's menu for the child's life (#1840),
// turnbridge.MapEvent's ModelList arm maps it onto protocol.ModelListPayload
// (#1848), and cmd/pyry's interactiveTurnEmitterV2.Handle emits the mapped frame to
// any interactive conn and appends it to the eventring (#1849). A menu the daemon
// never asked for would be presented to a client as one it did.
//
// THE TRADE STILL LANDS THE SAME WAY, and what decides it is the CONTENT bound
// rather than the audience. Correlating request_id would prove WHICH REPLY the
// bytes answered; it would not make the bytes more trustworthy, because the
// subprocess that could plant a models array in an unrelated ack is the same
// subprocess that authors the initialize reply — a correlated inventory is claude's
// own claim about itself exactly as an uncorrelated one is. That claim stays
// bounded whichever line carries it, and by the same three things: the caps above,
// nothing from the payload reaching a log (logControlResponse), and the render
// boundary the CLIENT owes (protocol.ModelOption's SECURITY paragraph). The
// alternative buys real provenance and costs new cross-object state:
// Runner.nextControlID mints the id inline at the call site and nothing retains it,
// exactly as its two sibling writers discard theirs, so the parser holds no link to
// it. This paragraph once deferred the question to whenever the value first reached
// a client; that HAPPENED, and the answer was re-taken here unchanged — recognise
// the shape, hold no cross-object parser state for provenance. No new trigger is
// set, because there is no later fact that would move the content bound.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// streamLine's doc states the property it preserves: control shapes are read from
// the top level only and nested content is never re-scanned, which is what stops a
// tool result whose text is literally `{"type":"result"}` from forging a turn
// boundary. Decoding this payload from anywhere else would let claude's own tool
// output announce a model inventory the daemon never asked for — and, since #1853,
// a slash-command inventory too. commandEntryLine rides this same input; nothing
// reaches for a nested field to get at either array.
//
// FIVE RUNGS, total over the input, none of which can panic and none of which
// surfaces an Unrecognized:
//
//  1. The line will not decode into the shape — a `models` that is a number or an
//     object, a non-object `response` at either level — → undecodable, no event.
//  2. subtype is not success → nak, no event. The rung the arm's old doc named as
//     the accepted gap.
//  3. models absent, null, empty, or decoded empty, and NO commands either →
//     controlResponseAck, no event. This rung is the narrow one: it now means
//     "neither array", so its record is all-zero exactly as the two above it are.
//     Its suppression is NOT settled by the MODEL-LIST rung's naive reading. That
//     rung is named rather than called "the emitting rung", which since #1891 is two
//     of them and no longer picks one out.
//     emitRateLimit's rung 3 is the precedent, and its
//     argument carries over unchanged — a ModelList carrying zero entries names no
//     model, so it cannot serve the purpose the variant exists for, and emitting it
//     would be the daemon reporting an inventory it never observed. The safe failure
//     direction here is the false NEGATIVE, and the choice is made with BOTH
//     outcomes visible: a client on the live interactive lane reads the result
//     today (#1849). A false ZERO would reach that client's menu as "claude offers
//     no models". A false NEGATIVE shows no menu at all — cmd/pyry's
//     sessionModelHold holds nothing, so resolveBoundModelList refuses rather than
//     answering an empty list. Both are a MISSING menu; only the false positive is
//     a WRONG one, and that asymmetry is the footing. It is stronger now that
//     either outcome is observable than it was when neither was.
//  4. success, the same empty-models reading as rung 3, and a NON-EMPTY commands
//     array → controlResponseCommandsOnly, and ONE SlashCommandList (#1891). It
//     differs from rung 3 in the keyword AND in the emit; what it shares with rung 3
//     is that the MODELS HALF IS READ IDENTICALLY, which is what still lets rung 3's
//     false-negative asymmetry be referenced rather than re-argued and keeps one copy
//     of it to correct. What the keyword split bought (#1890) is that the record names
//     the outcome instead of leaving the `commands` count to carry the distinction
//     alone; what this rung's emit rests on is emitSlashCommandList's own SUPPRESSION
//     RATHER THAN AN EMPTY EMIT paragraph, which travelled there with the construction,
//     declines to inherit rung 3's asymmetry and already covers this rung.
//  5. success and a non-empty models array → one ModelList. The `commands` array is
//     then read on this same rung under a SECOND, INDEPENDENT gate: non-empty → one
//     SlashCommandList beside it, absent / null / empty → the ModelList alone
//     (#1877). A non-empty `commands` with NO models does not reach here at all — it
//     is the controlResponseCommandsOnly rung above, which emits the same list from
//     its own discriminant. What this rung has that rung 4 does not is the ModelList.
//
// A per-entry field is never validated beyond its cap. An entry whose value is
// empty, whose resolvedModel is missing, or which carries no keys at all still
// becomes an entry: absence is claude's to choose, and emitBackgroundTaskStarted's
// doc is the standing answer — the field lands empty rather than inventing a
// validation rule. The capture's two four-key entries are the committed proof that
// a partial key set is claude's NORMAL output rather than a malformation. The gate
// that suppresses the event lives at the LIST level, not the entry level. That
// extends to the bool with one difference: what an absent supportsAutoMode READS AS
// is decided at turnevent.ModelOption's type rather than here, because the field's
// shape is the decision — there is no daemon code below implementing the collapse.
//
// The level LIST goes through TWO caps where each string goes through one —
// maxModelEffortLevel on every ELEMENT and maxModelEffortLevelCount on how many are
// RETAINED — and reports like neither: ONE name in TruncatedFields per entry whether
// that entry's levels were cut, its list was shortened, or both, because the report
// names FIELDS and a list is one field. What an absent
// supportedEffortLevels READS AS is likewise turnevent.ModelOption.EffortLevels' to
// say rather than this function's, and it SAYS that an absent key, a null and a
// published empty array are ONE reading, spelled nil (#1828). Unlike the bool that
// reading needs code, because encoding/json keeps the two shapes apart for free:
// boundEach's zero-length arm is where it lands, and it is the only normalisation
// anything below performs.
//
// NOTHING FROM THE PAYLOAD IS LOGGED, on any path — see logControlResponse, which
// is the one place that is decided.
func (p *Parser) emitModelList(line []byte) {
	var cr controlResponseLine
	if err := json.Unmarshal(line, &cr); err != nil {
		// The err is deliberately NOT logged, and emitModelAnnounced's undecodable arm
		// is where that rule is argued at length: encoding/json QUOTES the offending
		// input bytes into its error text, so `"err", err` would route claude's own
		// strings into the daemon log through a channel no per-attribute check can see.
		// #1853 made that strictly more load-bearing: with commandEntryLine declared,
		// the bytes a type error quotes are workspace-authored command names.
		p.logControlResponse(controlResponseUndecodable, 0, 0, 0, 0, 0)
		return
	}
	if cr.Response.Subtype != controlResponseSuccess {
		p.logControlResponse(controlResponseNAK, 0, 0, 0, 0, 0)
		return
	}
	// BELOW the success gate and ABOVE the empty-models block, and both halves are the
	// placement. Taking it above the subtype comparison would report a count off a
	// response that announced FAILURE. The lower bound binds HARDER since #1890 than
	// it did when this comment argued it from what the record would say: this int is
	// no longer only a number the record reports, it is the DISCRIMINANT between the
	// ack rung and the controlResponseCommandsOnly one. Taking it below the block
	// would not merely make a record read 0 — the branch could not be taken at all.
	// Since #1826 the block below is ALSO where the entry count is CUT, so the upper
	// bound binds for a third reason: a cut taken above the subtype comparison would
	// shorten an array off a response that announced FAILURE, on a path that emits
	// nothing at all.
	//
	// The COUNT bound runs HERE, above every rung, and maxSlashCommandListEntries' own
	// doc argues the placement: once rather than per rung, above the log call both
	// emitting rungs make before any emit, and above the emitter whose allocation it
	// bounds. Truncation is FROM THE TAIL — claude's order preserved, no ranking
	// invented, its ordering semantics being unobserved. The reslice shares the
	// decoder's backing array and that is deliberate; the constant's doc states why
	// this cut may do what boundAliases must not.
	commandEntries := cr.Response.Response.Commands
	var commandsDropped int
	if len(commandEntries) > maxSlashCommandListEntries {
		commandsDropped = len(commandEntries) - maxSlashCommandListEntries
		commandEntries = commandEntries[:maxSlashCommandListEntries]
	}
	// The EMITTED count since #1826, where it was the DECODED one — logControlResponse's
	// `models` and `dropped` are the pair this copies, and the two counts now mean the
	// same thing on both halves of the record. It is still the DISCRIMINANT between the
	// ack rung and the controlResponseCommandsOnly one, and the cap cannot disturb that:
	// being >= 1 it makes this int 0 exactly when the decoded length is 0.
	commands := len(commandEntries)
	entries := cr.Response.Response.Models
	if len(entries) == 0 {
		// An absent `models`, a null one, an empty array, and a response object
		// carrying no such key are ONE reading, and it is what both rungs below share;
		// the `commands` count is the only thing that separates them.
		if commands == 0 {
			// Rung 3, narrowed by #1890 to "neither array". The literal 0 rather than
			// `commands`: the guard proves the two identical, so no test can tell them
			// apart, and passing the literal says what this rung MEANS — an all-zero
			// record, exactly like the undecodable and nak rungs above it. The SIXTH
			// literal is the same choice for `commandsDropped` and rests on a proof one
			// step longer: the cap is >= 1, so a non-zero drop implies an emitted count of
			// at least the cap, which this guard has excluded. A test cannot tell the
			// literal from the variable here either.
			p.logControlResponse(controlResponseAck, 0, 0, 0, 0, 0)
			return
		}
		// Rung 4, and it EMITS since #1891: this rung was reached because the `commands`
		// array is non-empty, which is its own discriminant, so it reaches the emitter on
		// that array alone rather than behind a models list. The suppression is
		// emitSlashCommandList's PRECONDITION and is not repeated here — the same reason
		// rung 5's call is unconditional — and it cannot fire from this call site anyway,
		// the guard above having proved the slice non-empty. Its argument for suppressing
		// rather than emitting an empty list is that emitter's SUPPRESSION RATHER THAN AN
		// EMPTY EMIT paragraph, which covers this rung and is not restated here.
		//
		// The call sits BELOW logControlResponse and INSIDE this branch. Below the record
		// for the reason emitSlashCommandList's IT LOGS NOTHING paragraph rests on: the
		// seven attributes are written before anything below can run. Inside the branch
		// because falling through to the shared tail would run the models loop and put a
		// ModelList on a line that carries no models.
		//
		// It WIDENS which inputs reach the constructing-and-retaining path, which is
		// worth seeing at the site: before this, only a payload carrying BOTH arrays had
		// its WORKSPACE-authored command names copied into turnevent.SlashCommand values
		// and retained for the event's lifetime. The boundary itself does not move — one
		// decode target (commandEntryLine), one construction site, two caps
		// (maxSlashCommandName and, since #1904, maxSlashCommandDescription) — and the
		// argument passed is the same expression rung 5 passes, so there is no second
		// decode, loop or cap to keep in step. The cap COUNT grew with the field set while
		// the number of PLACES a cap is applied did not, which is what keeps this rung and
		// rung 5 in step without either one repeating the other.
		p.logControlResponse(controlResponseCommandsOnly, 0, 0, 0, commands, commandsDropped)
		p.emitSlashCommandList(commandEntries, commandsDropped)
		return
	}

	// Never nil and never empty: the rung above returned on both. The COUNT bound
	// runs before the loop, and truncation is FROM THE TAIL: claude's order is
	// preserved because no ranking is invented, its ordering semantics being
	// unobserved. It sits BELOW rung 3 and cannot move a rung's classification —
	// the cap is >= 1, so capping can neither create an empty list nor rescue one.
	var dropped int
	if len(entries) > maxModelListEntries {
		dropped = len(entries) - maxModelListEntries
		entries = entries[:maxModelListEntries]
	}

	models := make([]turnevent.ModelOption, 0, len(entries))
	// levelsDropped totals what the LEVEL-count bound cut across the RETAINED entries.
	// Entries the count cap above removed are already counted by `dropped`, and their
	// levels are never seen by the loop below, so nothing is counted twice.
	var levelsDropped int
	for _, entry := range entries {
		// The TEXT bound is per entry, so `cut` is per entry — which is the whole
		// reason this closure cannot be hoisted out of the loop.
		var cut []string
		// droppedLevels is per entry for `cut`'s reason, and its SCOPE is the whole of
		// what makes it correct: declared inside the loop, it cannot carry one entry's
		// drop onto the next, which is the mistake the "a cut on one entry does not
		// appear on the entries AFTER it" row exists to catch on the sibling path.
		var droppedLevels int
		bound := func(value, name string, limit int) string {
			out, truncated := truncateField(value, limit)
			if truncated {
				cut = append(cut, name)
			}
			return out
		}
		// boundEach is bound's sibling for the one field of this entry that is a LIST,
		// and it exists rather than a fourth bound call because a list is where ONE
		// name has to cover MANY values. Four properties, each load-bearing, in the
		// order the statements run:
		//
		//   - A ZERO-LENGTH input returns nil and appends nothing, so a published []
		//     reads as an absent key does. That is #1828's collapse implemented, and
		//     nil is the direction because it is this struct's own spelling for an
		//     empty list — see turnevent.ModelOption.TruncatedFields for the convention
		//     and ModelOption.EffortLevels for the argument. It normalises how Go
		//     spells ZERO and no element or position, so #1600's verbatim rule is
		//     untouched. The arm returns BEFORE everything below, so a zero-length list
		//     is neither counted against the cap nor named in TruncatedFields.
		//   - The COUNT bound then runs, and truncation is FROM THE TAIL for the
		//     entry-count cap's reason verbatim: claude's order is preserved because no
		//     ranking is invented, its ordering semantics being unobserved. Being >= 1
		//     it can neither create an empty list nor rescue one, so it cannot turn a
		//     non-empty list into the empty one whose reading #1828 settled, nor the
		//     other way about — the two mechanisms are independent by construction
		//     rather than by care. The > boundary matches truncateField's <=, and unlike
		//     maxModelListEntries' it is NOT an equivalent mutant: that block computes a
		//     count and a slice, both identity at len == cap, while this one also raises
		//     the report flag, so >= would name the field on a list nothing happened to.
		//   - Every SURVIVING element then goes through truncateField into a slice of
		//     the SAME length, so cutting an element neither drops it nor disturbs
		//     claude's order. That is the ELEMENT cap's property and not the closure's:
		//     the count bound above does shorten the list, which is why it reports.
		//   - The name is appended AT MOST ONCE, after the loop, and only if the list
		//     was shortened or some element was cut or both. Appending inside the loop
		//     would name the field once per cut level, which is the realistic mistake
		//     and the one the three-over-long row of TestParser_ModelListFieldsAreCapped
		//     exists to catch.
		//
		// The name is KEPT even though the closure now bounds the list's LENGTH too.
		// Bounding each element is still what it does per value, the count bound is one
		// statement before the loop, and the report is still one name; a rename would
		// buy no behaviour and would rot the by-symbol citations this function's own doc
		// and turnevent.ModelOption.EffortLevels make to it. The count constant is read
		// from package scope rather than taken as a second parameter beside `limit`: a
		// fourth int argument would put the two caps adjacent at the call site, which is
		// precisely the swap maxModelEffortLevelCount's naming paragraph spends itself
		// preventing, and unlike `bound` — three fields, three limits — this closure has
		// one call site and one field, so parameterizing buys nothing. It closes over
		// the same per-entry `cut` slice bound does, plus the per-entry droppedLevels
		// counter, and cannot be hoisted for the same reason.
		boundEach := func(values []string, name string, limit int) []string {
			if len(values) == 0 {
				return nil
			}
			var cutAny bool
			if len(values) > maxModelEffortLevelCount {
				droppedLevels = len(values) - maxModelEffortLevelCount
				values = values[:maxModelEffortLevelCount]
				cutAny = true
			}
			out := make([]string, len(values))
			for i, level := range values {
				var truncated bool
				out[i], truncated = truncateField(level, limit)
				cutAny = cutAny || truncated
			}
			if cutAny {
				cut = append(cut, name)
			}
			return out
		}
		// Sequential statements rather than a composite literal, for
		// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these
		// calls, and inside a literal that order would rest on the left-to-right
		// operand rule rather than on something a reader sees. The names are the
		// DAEMON's snake_case ones, not claude's camelCase keys.
		resolvedModel := bound(entry.ResolvedModel, "resolved_model", maxModelResolved)
		value := bound(entry.Value, "value", maxModelValue)
		displayName := bound(entry.DisplayName, "display_name", maxModelDisplayName)
		effortLevels := boundEach(entry.EffortLevels, "effort_levels", maxModelEffortLevel)
		// The per-entry drop joins the total HERE rather than inside the closure. A
		// counter the closure incremented directly would be one declared outside the
		// loop, and that is how a drop on one entry starts appearing on the next.
		levelsDropped += droppedLevels

		models = append(models, turnevent.ModelOption{
			// claude's values VERBATIM: no lowercasing, no alias expansion, no
			// date-stamping, no family mapping, no lookup against any published model
			// list (#1600's rule). The cap is the only judgement made about them here.
			ResolvedModel: resolvedModel,
			Value:         value,
			DisplayName:   displayName,
			// Through `boundEach` rather than `bound`: the caps are per ELEMENT and per
			// COUNT while the report is per FIELD. claude's order survives both of them and
			// the list's cardinality survives an element CUT untouched — what changes the
			// cardinality is the count bound, which shortens FROM THE TAIL and says so in
			// TruncatedFields. #1600's verbatim rule covers the elements exactly as it covers
			// the three strings — no lowercasing and no canonicalisation into any effort
			// vocabulary of the daemon's own. internal/relay's validEffort is a CLOSED enum
			// on an INBOUND path and is deliberately not consulted here.
			EffortLevels: effortLevels,
			// Not through `bound`: a bool has no length to cut, carries none of claude's
			// bytes into the per-entry budget, and is therefore never named in
			// TruncatedFields. Absent, null and false arrive here already collapsed by
			// encoding/json — see turnevent.ModelOption.SupportsAutoMode for why that is
			// the intended reading rather than a distinction lost.
			SupportsAutoMode: entry.SupportsAutoMode,
			// nil when nothing was cut: append never ran.
			TruncatedFields: cut,
		})
	}

	p.logControlResponse(controlResponseModelList, len(models), dropped, levelsDropped, commands, commandsDropped)
	p.emit(turnevent.ModelList{Models: models, DroppedModels: dropped})

	// THE SECOND GATE, and it is INDEPENDENT of the models one: this rung was reached
	// because the models array is non-empty, and whether a SlashCommandList joins the
	// ModelList is decided on the `commands` array alone. The gate itself is
	// emitSlashCommandList's PRECONDITION rather than a guard written here, so a caller
	// inherits the suppression instead of repeating it and this call is unconditional.
	// The second call site that argument was written for EXISTS: rung 4 above calls the
	// same emitter with the same expression and likewise writes no guard of its own.
	//
	// THE ORDER IS DELIBERATE, and it is ONE RULE COVERING BOTH CALL SITES rather than
	// this rung's own arrangement: each rung emits its OWN DISCRIMINANT's event first,
	// then any independently gated one. Here the discriminant is the models array — the
	// rung exists because of it — so the ModelList goes first and the command inventory
	// follows; on rung 4 the discriminant is the `commands` array and the rule is
	// satisfied trivially, there being one event. On both rungs logControlResponse runs
	// before any emit. Gate order and statement order are then one order a reader checks
	// once, at two sites.
	//
	// The CALL sits BELOW logControlResponse, which is what makes "the record is
	// unchanged" structural rather than merely intended: seven attributes, the same
	// values and the same reason keyword, whatever happens below. emitSlashCommandList
	// logs nothing on any path, so that holds through the callee too.
	p.emitSlashCommandList(commandEntries, commandsDropped)
}

// emitSlashCommandList emits AT MOST ONE turnevent.SlashCommandList for one decoded
// `commands` array — the initialize reply's slash-command inventory for the workspace
// the child was spawned in. It takes the DECODED entries rather than the line: the
// decode, its error path and the whole rung classification stay in emitModelList,
// and a second json.Unmarshal of the same bytes would add a second undecodable outcome
// to classify.
//
// IT LOGS NOTHING, on any path. The one record every control_response produces is
// logControlResponse's and is written by emitModelList BEFORE this call, which is what
// keeps "seven attributes, the same values, the same reason keyword" a structural
// property of the caller rather than a promise this function has to keep.
//
// THE GATE IS THIS EMITTER'S PRECONDITION, so every caller inherits the suppression
// rather than repeating it. An absent `commands`, a null one, an empty array and a
// response object carrying no such key are ONE reading and all return here —
// controlResponseLine.Commands is a plain slice precisely so that they are.
//
// SUPPRESSION RATHER THAN AN EMPTY EMIT, and the decision is this producer's to
// take: turnevent.SlashCommandList.Commands' doc hands it here explicitly. What
// decides it is that THE PRODUCER CANNOT MAKE THE STATEMENT AN EMPTY EMIT WOULD BE
// MAKING. protocol.SlashCommandListPayload.MarshalJSON declares a wire [] a
// POSITIVE statement — claude offered nothing — but by the time this line runs the
// decode has already collapsed absent, null and a published [] onto one nil slice,
// so the daemon cannot tell "claude offered nothing" from "claude said nothing
// about commands". Emitting an empty list would assert the first from evidence
// that cannot distinguish it from the second, which is inventing a distinction
// rather than reporting one. Rung 3's false-negative asymmetry is NOT the
// argument, and was weighed rather than inherited: it rests on both outcomes being
// observable to a client (#1849 made them so for models), and nothing publishes
// this list today (#1720), so that footing is unavailable here. The wire's []
// position is untouched either way — it governs how a list that WAS emitted
// serialises, which says nothing about whether to emit one, and #1720 reads it for
// that.
//
// WHERE THE CONSTRUCTED LIST GOES, and it is NOT where a mis-read model inventory
// goes — emitModelList's own paragraph on that names three destinations and this value
// reaches none of the three, so the two must not be read across. It goes to the
// parser's emit callback and, on the interactive lane, to cmd/pyry's
// interactiveTurnEmitterV2.Handle, which has NO CASE for this variant: it lands on that
// function's own default, which logs it by kind through eventKind and returns. Because
// that default returns, emitMapped never runs for it — and emitMapped is
// turnbridge.MapEvent's only caller on this lane, resolveBoundModelList being handed a
// ModelList explicitly — so the value never reaches MapEvent AT ALL. "It falls to
// MapEvent's default" is the wrong reason for the right conclusion. IT IS ALSO RETAINED
// since #2004, by cmd/pyry's sessionSlashCommandHold, for the session's life —
// maxSlashCommandName's doc states and owns that, as it owned the absence before it.
// MapEvent's arm has since LANDED
// (#2001) and that does not change a word above: an arm is not a route, and nothing on
// this lane calls MapEvent with this variant until Handle's case, which is #2003's and
// still open. What the value DOES reach is eventKind, whose
// SlashCommandList arm returns the variant NAME only — and that arm, not this doc,
// carries the enumeration of which drop sites are reachable for it and which are not,
// so there is one copy to correct when #2003's case lands.
func (p *Parser) emitSlashCommandList(entries []commandEntryLine, dropped int) {
	if len(entries) == 0 {
		return
	}
	// slashCommands rather than a name shared with the parameter: `entries` holds the
	// DECODED entries and this holds the CONSTRUCTED turnevent.SlashCommand values, so
	// two names keep the two apart. The shadowing this name used to avoid — the int
	// `commands` the record reports — is no longer a hazard: that int stays in
	// emitModelList.
	slashCommands := make([]turnevent.SlashCommand, 0, len(entries))
	for _, entry := range entries {
		// Declared INSIDE the loop, and that scope is the whole of what makes the report
		// per entry: a cut on one entry cannot appear on the entries after it.
		// emitModelList's models loop declares its own `cut` and `droppedLevels` inside
		// its own loop for the same reason.
		var cut []string
		// A `bound` closure since #1904, where a one-field entry needed none. The claim
		// this comment used to carry — that one field means there is no TruncatedFields
		// ORDER for a composite literal to decide, which was the only thing the device
		// protected — stopped being true the moment there were two fields to order, so
		// the premise went rather than the conclusion being defended. It is
		// emitModelList's own closure in shape and in scope: declared INSIDE the loop
		// because `cut` is, which is what keeps one entry's report off the entries after
		// it, and closing over that slice rather than returning a second value. Two
		// sequential truncateField calls would work equally well and were weighed; the
		// closure wins on there being ONE place the append happens rather than one per
		// field, so "declaration order == call order" is checked at the call sequence
		// below and nowhere else — which is what made INSERTING the third call in #1957
		// a one-line edit rather than a re-derivation of where the append happens. It
		// bounds THREE of the entry's four fields since #1825; the fourth is a list and
		// gets boundAliases below, which appends to this same `cut` slice so the ONE place
		// stays one place.
		// emitModelAnnounced's copy of the retired sentence is
		// about ModelAnnounced.Model's own single field and is untouched by this.
		bound := func(value, name string, limit int) string {
			out, truncated := truncateField(value, limit)
			if truncated {
				cut = append(cut, name)
			}
			return out
		}
		// boundAliases is bound's sibling for the one field of this entry that is a LIST,
		// and it exists rather than a fourth bound call because a list is where ONE name
		// has to cover MANY values — and, here, TWO cut dimensions. It is emitModelList's
		// boundEach in shape and in every property; what follows states only what this
		// copy owes on its own, the four properties themselves being that closure's doc.
		//
		//   - A ZERO-LENGTH input returns nil and appends nothing, so an absent key, a JSON
		//     null and a published [] all read alike. That is the collapse
		//     turnevent.SlashCommand.Aliases decides, implemented here; the arm returns
		//     BEFORE everything below, so an empty list is neither counted against the
		//     count cap nor named in the report.
		//   - The COUNT bound then runs, truncating FROM THE TAIL so claude's order
		//     survives, with a > boundary rather than >= so a list at exactly the cap is not
		//     named as cut. Its position is load-bearing for a SECOND reason this copy
		//     states because the sibling has no need to: it bounds the ALLOCATION below.
		//     An entry carrying a million aliases makes a slice of maxSlashCommandAliasCount
		//     and runs truncateField that many times, not a million of each.
		//   - Every SURVIVING element then goes through truncateField into a slice of the
		//     SAME length, so cutting an element neither drops it nor disturbs claude's
		//     order.
		//   - The name is appended AT MOST ONCE, after the loop, whether the cause was an
		//     over-long element, an over-long list or both.
		//
		// THE RESULT IS ALWAYS A FRESH ALLOCATION, never the resliced input, and that is
		// the one plausible optimisation this closure must refuse. Returning values[:n] on
		// the all-fit path would make the emitted event retain the DECODER's backing array
		// — up to a 4 MiB line's worth of string headers — for the event's whole lifetime,
		// where the point of these caps is that what is RETAINED is bounded.
		//
		// Named for its FIELD rather than boundEach, which is what emitModelList's copy is
		// called: two closures of one name in one package make every by-symbol citation to
		// either one ambiguous, and a citation is the only thing that would notice. It has
		// one call site and one field, so naming it for the field costs nothing. The
		// (values, name, limit) signature is KEPT despite that, because the report name at
		// the CALL SITE is what makes declaration order visible in the call sequence below;
		// the count constant is read from package scope rather than passed as a fourth int,
		// boundEach's stated reason — a fourth int argument puts the two caps adjacent at
		// the call site, which is the swap maxSlashCommandAliasCount's naming paragraph
		// spends itself preventing. It closes over the same per-entry `cut` slice bound
		// does and cannot be hoisted for the same reason.
		boundAliases := func(values []string, name string, limit int) []string {
			if len(values) == 0 {
				return nil
			}
			var cutAny bool
			if len(values) > maxSlashCommandAliasCount {
				values = values[:maxSlashCommandAliasCount]
				cutAny = true
			}
			out := make([]string, len(values))
			for i, alias := range values {
				var truncated bool
				out[i], truncated = truncateField(alias, limit)
				cutAny = cutAny || truncated
			}
			if cutAny {
				cut = append(cut, name)
			}
			return out
		}
		// Sequential statements in DECLARATION ORDER, emitModelList's models loop's rule
		// and now for its reason rather than by resemblance: TruncatedFields is ordered by
		// these calls, so `[]string{"name", "argument_hint", "description", "aliases"}` on
		// an entry with all four cut is a
		// property of the call sequence a reader sees. The names are the DAEMON's
		// snake_case ones, not claude's camelCase keys — and for all four they
		// coincide with the wire names protocol.SlashCommand.TruncatedFields documents, so
		// a later mapping is a copy rather than a translation.
		// That distinction was a PREDICTION until #1957 and is the live case now:
		// `name`, `description` and `aliases` are byte-identical as claude's key and as the
		// daemon's name, where the hint is claude's `argumentHint` against the daemon's
		// `argument_hint` — one field of four, which is what makes it a distinction rather
		// than a rule.
		//
		// The hint's call is INSERTED BETWEEN the other two rather than appended after
		// them. Appending it compiles, passes any membership check, and silently breaks
		// the declaration order turnevent.SlashCommand's doc promises and the wire
		// mapping depends on — which is why TestParser_SlashCommandFieldsAreCapped
		// compares TruncatedFields by exact equality rather than by membership. The alias
		// call IS appended, and correctly: its slot is the END of the declaration order on
		// both types, so this is the one of the three field slices where the placement that
		// compiles most easily is also the right one — which is why the exact-equality
		// comparison matters no less here.
		name := bound(entry.Name, "name", maxSlashCommandName)
		hint := bound(entry.ArgumentHint, "argument_hint", maxSlashCommandArgumentHint)
		description := bound(entry.Description, "description", maxSlashCommandDescription)
		aliases := boundAliases(entry.Aliases, "aliases", maxSlashCommandAlias)
		slashCommands = append(slashCommands, turnevent.SlashCommand{
			// claude's name VERBATIM (#1600's rule): no lowercasing, no trimming, no
			// charset filtering, no leading "/" added or removed. The cap is the only
			// judgement made about it here and it is a BYTE cut — see maxSlashCommandName,
			// and turnevent.SlashCommand.Name for the same rule stated at the consumer.
			Name: name,
			// claude's hint VERBATIM under the same #1600 rule: no lowercasing, no
			// trimming, no charset filtering. Its cap is its OWN
			// (maxSlashCommandArgumentHint), not Name's and not Description's, even though
			// all three numbers currently agree.
			//
			// AN EMPTY HINT IS CARRIED AS AN EMPTY STRING rather than dropped or defaulted,
			// and it is the ORDINARY case rather than an edge one: 33 of the capture's 51
			// entries carry "" and none omits the key. Nothing here branches on emptiness,
			// which is what keeps that true — turnevent.SlashCommand's own field doc states
			// the consequence for a consumer.
			//
			// BETWEEN Name and Description: protocol.SlashCommand's declaration order, and
			// the slot turnevent.SlashCommand's doc reserved for this field before it
			// existed.
			ArgumentHint: hint,
			// claude's description VERBATIM under the same #1600 rule, with NO NEWLINE
			// STRIPPING named explicitly because this is the field a captured value carries
			// newlines in — see protocol.SlashCommand's doc for that measurement and its
			// narrowness. Its cap is its OWN (maxSlashCommandDescription), not Name's, even
			// though the two numbers currently agree.
			//
			// BETWEEN ArgumentHint and TruncatedFields, which is protocol.SlashCommand's declaration
			// order minus the fields the daemon type does not declare yet — see
			// turnevent.SlashCommand, whose doc owns that promise and now cites this field as
			// the first evidence it was kept.
			Description: description,
			// Through `boundAliases` rather than `bound`: the caps are per ELEMENT and per
			// COUNT while the report is per FIELD. claude's order survives both of them and
			// the list's cardinality survives an element CUT untouched — what changes the
			// cardinality is the count bound, which shortens FROM THE TAIL and says so in
			// TruncatedFields. #1600's verbatim rule covers the elements exactly as it covers
			// the three strings, and one clause of it is this field's alone: NO EXPANSION INTO
			// SYNTHETIC ENTRIES. `reset` is an alias of `clear` and must not become a command
			// named `reset`; the loop this statement sits in appends exactly one
			// turnevent.SlashCommand per decoded entry, and that is the whole of what keeps
			// it true.
			//
			// AN ABSENT, NULL OR EMPTY LIST IS CARRIED AS nil rather than as an empty non-nil
			// slice, which is the closure's zero-length arm and the collapse
			// turnevent.SlashCommand.Aliases owns the argument for. Absence is the MAJORITY
			// case here where an EMPTY hint is the majority case above: 42 of the capture's
			// 51 entries omit the key and none publishes [].
			//
			// BETWEEN Description and TruncatedFields: protocol.SlashCommand's declaration
			// order, and the slot turnevent.SlashCommand's doc reserved for this field before
			// it existed. Its bound call is APPENDED after the other three rather than
			// inserted between them, which is what that slot means at the call site.
			Aliases: aliases,
			// nil when nothing was cut: append never ran.
			TruncatedFields: cut,
		})
	}
	// THE ENTRY-COUNT CAP IS THE CALLER'S AND THE DROP COUNT ARRIVES AS A PARAMETER
	// (#1826), which is the one place this emitter is not self-contained and the reason
	// is worth having at the site. logControlResponse's record is written by emitModelList
	// BEFORE this call and this function logs nothing on any path, so a count computed
	// HERE could not reach the record without threading it back out of a void-returning
	// function with two call sites. Cutting in the caller instead puts the number where
	// the record can read it, keeps ONE cut for BOTH rungs, and bounds the make() above —
	// maxSlashCommandListEntries' doc argues all three. The precedent is exact and in
	// this emitter's caller: #1811 emitted turnevent.ModelList with no entry-count cap
	// and #1812 added maxModelListEntries and DroppedModels in the next slice.
	//
	// `dropped` is 0 whenever the precondition returned above, by construction rather
	// than by a check here: the caller cuts to a cap of at least one, so an empty
	// argument slice cannot have come with a non-zero drop.
	p.emit(turnevent.SlashCommandList{Commands: slashCommands, DroppedCommands: dropped})
}

// logControlResponse writes emitModelList's ONE record, and it exists so the
// content-free rule is decided in a single place rather than on each of the rungs.
// Every control_response produces exactly one of these, whatever it was a reply to.
//
// Seven attributes and NOTHING else. `type` is a constant here rather than
// sl.Type, which the case arm's match makes byte-identical; `reason` comes from the
// closed keyword set at controlResponseMsg; `models` is the emitted entry count,
// `dropped` how many maxModelListEntries cut, and `levels_dropped` how many effort
// levels maxModelEffortLevelCount cut in TOTAL across the RETAINED entries — all
// three 0 on every rung but the model-list one, which is the only rung that reads a
// models array at all. `commands` and `commands_dropped` are that first pair one array
// over: the EMITTED slash-command count and how many maxSlashCommandListEntries cut,
// both 0 on every rung but the two that read the commands array. Levels belonging to
// entries `dropped`
// removed are not counted again there. No value, no resolvedModel, no displayName, no
// level string, no request_id, no error string,
// no unmarshal err, no line bytes. The three strings are precisely what #833's
// posture — restated across internal/relay's v2session_settings.go and
// internal/sessions' pool.go as "model / effort / YOLO values are NEVER logged at
// any level" — exists to keep out of a log, and a drop site explaining itself with
// the value it dropped is how that rule usually breaks. All four integers are
// DAEMON-computed and carry none of claude's bytes, which is what admits them where
// no string from the payload is admitted.
//
// `dropped` is here rather than omitted (#1812) for the reason the wire field's own
// doc argues about a permanent zero, one layer down: `models=6` on a reply that
// carried forty reads as "claude offers six models". emitBackgroundTaskRoster logs
// no count and does not oppose this — its only record is the UNDECODABLE drop, so it
// has no success record to complete, while this path has one and completing it is
// consistent. The entry count is no longer this record's alone:
// turnbridge.MapEvent's ModelList arm carries turnevent.ModelList.DroppedModels
// through verbatim (#1848) and cmd/pyry's interactiveTurnEmitterV2.Handle puts it
// on the wire as dropped_models (#1849), where
// protocol.ModelListPayload.DroppedModels documents it as client-facing. The record
// keeps its own reason, on two facts the wire field cannot supply. AN
// OPERATOR-FACING SIGNAL IS NOT A CLIENT-FACING ONE — the wire field tells a phone
// its menu is short, this record tells an operator, on the daemon's own timeline.
// And the wire field is not a RELIABLE observable of the cap: no conn need be
// interactive when the initialize exchange happens, and the live send is droppable
// at the fan-in, so a cap can fire with no frame reaching anyone. This record always
// exists. So a cap firing in production is still a cap no OPERATOR can know fired
// without it — that would take a phone having been connected and having reported
// back — and the first evidence that 10 is the wrong number would arrive as a user's
// short menu.
//
// `levels_dropped` is here for that argument VERBATIM, one dimension down, and here
// the record is still the only place the NUMBER appears at all.
// turnevent.ModelOption.TruncatedFields does reach a client — MapEvent's arm crosses
// it as the slice it is (#1848), and protocol.ModelOption.MarshalJSON deliberately
// exempts it so nothing-was-cut arrives as null — but what crosses is a NAME,
// "effort_levels", at most once per entry, saying the same thing whether one level
// was cut or ninety were dropped. The MAGNITUDE reaches nowhere else, which
// turnevent.ModelOption.EffortLevels' own "WHAT THAT GIVES UP" paragraph states
// from the other side: the true level count is not recoverable from the event, where
// ModelList's true entry count is recoverable as len(Models) + DroppedModels. The
// operator-versus-client and best-effort points from `dropped` above apply here
// unchanged. So without this the level bound would be a cap on subprocess-supplied
// data with no count anywhere, and the first evidence that maxModelEffortLevelCount
// is the wrong number would arrive as a user's short effort menu. It is admissible
// under this function's own rule for the same reason the other two counts are — a
// DAEMON-computed integer derived from slice lengths, carrying none of claude's
// bytes — and the level STRINGS it counts are exactly the "effort values are NEVER
// logged at any level" half of #833's posture, so none of them goes anywhere near
// this record.
//
// `commands` is the initialize payload's slash-command entry count (#1853), and its
// argument is `dropped`'s SIMPLER and STRONGER: this record is the ONLY observable
// that decode has. The operator-versus-client half does not transfer — `dropped`
// completes a client-facing wire field, and this number has no wire field to
// complete — which is NOT what changed when turnbridge.MapEvent's arm mapped
// `dropped` onto protocol.SlashCommandListPayload.DroppedCommands (#2001): that
// mapping gave THAT counter its wire field, and this one still has none. What it no
// longer lacks is an event, a daemon-internal value and a retention: since #1877 the
// entries this counts are copied into a turnevent.SlashCommandList and retained for
// that event's lifetime, each name under maxSlashCommandName. Without it a
// `commands` array that stopped decoding would be a change nobody could know
// happened. IT COUNTS WHAT WAS EMITTED, exactly as `models` does since #1826 gave this
// array an entry-count cap of its own. That is a CORRECTION rather than a widening: this
// paragraph argued for years that the two counts had different meanings, `models` being
// post-cut and `commands` being a raw decode count, and concluded from the absence of an
// entry-count cap that no seventh attribute was needed. maxSlashCommandListEntries
// falsified the premise, so the conclusion went with it and the two halves of this record
// now read alike. Nothing became unobservable
// either: a non-empty `commands` now always means emitted on BOTH rungs that read the
// array — the model-list one and the commands-only one (#1891) — and the rungs that
// emit nothing no longer rest on this count at all. #1890 took the keyword decision
// this sentence used to defer, so a commands-only success logs
// controlResponseCommandsOnly and the narrowed `ack` means "neither array". The
// separation is the KEYWORD's, and this count qualifies it rather than carrying it.
// Admissible on the other three counts' footing exactly — a DAEMON-computed
// integer derived from a slice length, carrying none of claude's bytes. No name, no
// argumentHint, no description and no alias reaches this record on any rung — and
// SINCE #1825 THE DISTINCTION BEHIND THAT SENTENCE IS DRAWN ONE LAST TIME, which is
// worth redrawing rather than renumbering. commandEntryLine declares ALL FOUR keys now,
// so NOTHING is UNREACHABLE by omission any more: the category that used to hold the
// undeclared keys — a value that never becomes a Go string cannot be written — is
// EMPTY. The description crossed out of it in #1904, the argument hint in #1957 and the
// aliases in #1825, so all four are UNWRITTEN, held in a decoded struct and in a
// constructed event, and kept out of this record by what this function chooses to log
// rather than by what the decode target can hold. Each slice moved one key across that
// line and the sentence above stayed true for a different reason each time, which is
// why it was redrawn rather than renumbered; this is the last redraw the sequence has,
// and what the sentence rests on from here is this function's own choice alone. That
// is the weaker of
// the two guarantees and it is the one #833's posture actually asks for elsewhere; on
// the cmd/pyry lane it is pinned deterministically rather than left advisory, by
// TestInteractiveTurnEmitterV2's log-leak negative over
// emitterSlashCommandListSentinels, whose own doc records that the enumeration does NOT
// grow with the field set and that each field-adding slice therefore owes it a
// sentinel. The record carries SEVEN attributes and five daemon-computed integers since
// #1826, and no decoded content on any rung.
//
// `commands_dropped` is how many entries maxSlashCommandListEntries cut, and it is
// `dropped`'s argument one array over: without it `commands=128` on a reply that carried
// a thousand reads as "the workspace offers 128 commands". It is the SEVENTH attribute
// the paragraph above spent years declining, and the decline was correct while its
// premise held — the premise was that no entry-count cap existed, so the decoded count
// was the emitted count and there was nothing to disambiguate. What it never rested on
// is a claim that seven attributes are too many.
//
// STILL NO ATTRIBUTE FOR THE ALIAS DROP, and that decline is UNAFFECTED because it rests
// on something else entirely. `levels_dropped` earns its place because the magnitude
// reaches nowhere else AND this record is the emit's only observable; the alias drop's
// report channel is per ENTRY and already exists, turnevent.SlashCommand.TruncatedFields
// naming "aliases", which is what that cardinality bound's honesty rests on. The
// threading objection that used to sit beside it — that a count computed inside
// emitSlashCommandList could not reach a record already written — is what the ENTRY-COUNT
// cut answers by living in emitModelList instead, and it answers it only for the count
// that is cut there. An alias drop is decided per entry inside the emitter's loop and
// would still have to be threaded back out.
//
// THE TWO COMMAND INTS ARE THE LAST TWO PARAMETERS AND THE LAST TWO ATTRIBUTES, so the
// two orders are one order a reader checks once. The record is now two pairs and a
// qualifier: `models` with `dropped`, `commands` with `commands_dropped`, and
// `levels_dropped` qualifying the first pair from inside its own dimension. Inserting
// either new int between the model trio would split a group that reads as a unit. Five
// adjacent ints is a swap hazard and it is PINNED rather than designed away, on the same
// rung the four-int version was pinned on: the controlResponseCommandsOnly one, where
// the model trio is all-zero and the command pair is not. Since #1826 that rung pins one
// swap more, the pair against itself, and only because the two members can differ — a
// fixture whose drop happens to equal its emitted count would make the pair
// self-symmetric and that swap invisible again.
//
// The attribute set is FIXED at seven on every rung, which is why a rung with no models
// array to describe passes 0 rather than omitting the key.
func (p *Parser) logControlResponse(reason string, models, dropped, levelsDropped, commands, commandsDropped int) {
	p.log.Debug(controlResponseMsg,
		"type", "control_response", "reason", reason, "models", models, "dropped", dropped,
		"levels_dropped", levelsDropped, "commands", commands, "commands_dropped", commandsDropped)
}

// truncateField cuts s to limit bytes, reporting whether it cut. Mirrors
// truncateRaw: byte-sliced, then scrubbed of any invalid UTF-8 the cut may have
// produced, because slicing can land mid-rune and the value rides a JSON string
// field downstream where an invalid sequence would be silently replaced anyway.
// The boundary is <=, so a field of exactly limit bytes is not truncated.
//
// The scrub is unconditional for symmetry with truncateRaw. On the untruncated
// path it is a no-op for every field decoded into a Go STRING: encoding/json
// replaces invalid input bytes with U+FFFD on decode, so our own cut is the only
// mid-rune hazard there.
//
// CORRECTED 2026-08-07 (#1382): that no-op claim is scoped to string-decoded
// fields, and BackgroundTaskUpdated.Patch is the first exception. It is sourced
// from a json.RawMessage, which carries claude's bytes VERBATIM — the decoder
// does not U+FFFD-replace inside a RawMessage — so an invalid sequence survives
// the decode and the scrub is load-bearing for it even when nothing is
// truncated.
//
// Like truncateRaw the replacement is EMPTY, so a partial rune is deleted rather
// than replaced — which is why a cut value can come out 1-3 bytes under the
// limit, and why a scrub REMOVAL is not reported as a truncation (the bool says
// only whether the cap cut; see Patch's doc for the consequence).
func truncateField(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return strings.ToValidUTF8(s, ""), false
	}
	return strings.ToValidUTF8(s[:limit], ""), true
}

// emitUnrecognized builds and emits one turnevent.Unrecognized, truncating raw to
// maxUnrecognizedRaw first so an oversized payload never enters the event stream.
// The log records site, type, and byte count only — never the content itself,
// which is the package's standing rule; the content crosses the wire, not the log.
func (p *Parser) emitUnrecognized(site turnevent.UnrecognizedSite, kind string, raw []byte) {
	text, truncated := truncateRaw(raw)
	p.log.Debug("streamsup: unrecognized payload",
		"site", string(site),
		"type", kind,
		"bytes", len(raw),
		"truncated", truncated)
	p.emit(turnevent.Unrecognized{
		Site:      site,
		Kind:      kind,
		Raw:       text,
		Truncated: truncated,
	})
}

// truncateRaw cuts raw to maxUnrecognizedRaw bytes, reporting whether it cut.
// Byte-sliced, then scrubbed of any invalid UTF-8 the cut may have produced:
// slicing can land mid-rune, and the result rides a JSON string field, where an
// invalid sequence would be silently replaced downstream anyway. Doing it here
// keeps the payload well-formed at the point of construction. The value is a
// diagnostic blob, not text to read to the end of, so cutting mid-structure is
// fine; Truncated is what tells the reader the JSON is incomplete.
func truncateRaw(raw []byte) (string, bool) {
	if len(raw) <= maxUnrecognizedRaw {
		return strings.ToValidUTF8(string(raw), ""), false
	}
	return strings.ToValidUTF8(string(raw[:maxUnrecognizedRaw]), ""), true
}

// resultTurnEndReason maps a result line's subtype to its TurnEnd reason.
// error_during_execution is claude's interrupt-terminated turn (spike T1,
// #1075) → cancelled; every other subtype (success, and any unknown) keeps
// end_turn — correct for a clean turn and a safe default otherwise. The change
// is scoped to error_during_execution only: this is not a general subtype→reason
// table (max_tokens/refusal classification remains future work).
func resultTurnEndReason(subtype string) turnevent.TurnEndReason {
	switch subtype {
	case "error_during_execution":
		return turnevent.TurnEndReasonCancelled
	default:
		return turnevent.TurnEndReasonEndTurn
	}
}

// emitAssistant maps one assistant message's content blocks. Unlike mapper.go
// (one block per JSONL line), a stream-json assistant event carries a whole
// message that may hold several blocks; we iterate them and emit one event per
// block, preserving order. A nil message or zero mappable blocks emits nothing.
func (p *Parser) emitAssistant(msg *streamMessage) {
	if msg == nil {
		return
	}
	for _, raw := range msg.Content {
		block, ok := p.decodeBlock(raw)
		if !ok {
			continue
		}
		switch block.Type {
		case "text":
			p.emit(turnevent.TextChunk{MessageID: msg.ID, Text: block.Text})
		case "thinking":
			p.emit(turnevent.ThoughtChunk{MessageID: msg.ID, Text: block.Thinking})
		case "tool_use":
			p.emit(turnevent.ToolStart{
				ToolCallID: block.ID,
				Title:      block.Name,
				Kind:       toolKind(block.Name),
				RawInput:   rawInput(block.Input),
			})
		default:
			// No known-ignored list at block level: the measurement found exactly
			// these three assistant block types and nothing else, so there is no
			// per-turn noise to suppress. Any fourth is news.
			p.emitUnrecognized(turnevent.UnrecognizedAssistantBlock, block.Type, raw)
		}
	}
}

// decodeBlock unmarshals one content block's raw bytes into streamBlock. A block
// that fails to decode is surfaced as an undecodable Unrecognized rather than
// skipped in silence, and reports ok == false so the caller moves on to the next
// block — one malformed block never costs the rest of the message.
func (p *Parser) decodeBlock(raw json.RawMessage) (streamBlock, bool) {
	var block streamBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		p.emitUnrecognized(turnevent.UnrecognizedUndecodable, "", raw)
		return streamBlock{}, false
	}
	return block, true
}

// emitUser maps one user message's tool_result blocks to ToolUpdate. A nil
// message emits nothing; any non-tool_result block emits an Unrecognized, with
// the single exception of the harness nudge, which is dropped in silence.
//
// It takes the RAW LINE BYTES as well as the message because the tool_use_result
// sidecar is a sibling of `message` on the LINE, not a field inside it, so this
// type-switch call site is the only place the two can meet (#2024).
// consumeLine's rate_limit_event arm, which hands emitRateLimit(line) the same
// way, is the in-file precedent.
func (p *Parser) emitUser(msg *streamMessage, line []byte) {
	if msg == nil {
		return
	}
	// One line carries one sidecar; a message may carry many tool_result blocks.
	// MORE THAN ONE BLOCK DROPS THE COUNT FROM ALL OF THEM: the sidecar cannot be
	// attributed to a particular block, and a count on the wrong row is worse than
	// no count. The capture's block histogram is {"0": 1, "1": 2}, so no
	// multi-block line was observed — this is a fail-closed decision about an
	// unobserved case rather than a measured shape.
	//
	// Composed FIRST and gated on being non-empty, so the ~95% of lines with no
	// count never pay for the block-count pre-pass.
	var utl userToolResultLine
	// The error is dropped, not classified: the line has already decoded once in
	// consumeLine, and a json.RawMessage target accepts any valid JSON value, so
	// there is no second undecodable outcome to report. See toolResultDetail.
	_ = json.Unmarshal(line, &utl)
	detail := toolResultDetail(utl.ToolUseResult)
	if detail != "" && countToolResultBlocks(msg) != 1 {
		detail = ""
	}
	for _, raw := range msg.Content {
		block, ok := p.decodeBlock(raw)
		if !ok {
			continue
		}
		if block.Type == "text" && block.Text == harnessNoOutputNudge {
			// The one known harness payload (see harnessNoOutputNudge for the
			// capture and why it is suppressed rather than mapped). `continue`, not
			// `return`: the suppression is scoped to this BLOCK, so a tool_result
			// sharing the message still maps. Requiring type "text" is what keeps
			// the guard unreachable from tool output — a tool_result's payload
			// decodes into Content, never into Text — so tripping it takes control
			// of the block's type, not just of a string. Logged content-free: site
			// and type only, exactly like the known-ignored line drop above.
			p.log.Debug("streamsup: dropping known harness user block",
				"site", string(turnevent.UnrecognizedUserBlock),
				"type", block.Type)
			continue
		}
		if block.Type != "tool_result" {
			// The measurement found user messages carry tool_result blocks and
			// nothing else — notably NOT a text echo of the delivered prompt, which
			// the agent-run surface does emit. So a user/text block reaching here
			// (i.e. every one but the harness nudge caught above) would be a genuine
			// change, and gets surfaced rather than dropped.
			p.emitUnrecognized(turnevent.UnrecognizedUserBlock, block.Type, raw)
			continue
		}
		p.emit(turnevent.ToolUpdate{
			ToolCallID:   block.ToolUseID,
			Status:       toolStatus(block.IsError),
			Content:      toolResultContent(block.Content),
			ResultDetail: detail,
		})
	}
}

// emit forwards one event to the sink. A nil sink is a no-op guard — the parser
// runs on the os/exec forwarder goroutine, where a nil-sink panic would be
// disproportionate to a misconfiguration.
func (p *Parser) emit(ev turnevent.Event) {
	if p.sink != nil {
		p.sink(ev)
	}
}

// The helpers below mirror internal/turnbridge/mapper.go: unexported and keyed
// on tui-driver types there, so they cannot be imported; re-implemented here on
// our already-decoded fields (no ParseToolUse/ParseToolResult re-parse).

// toolKind maps a claude tool name to its ACP kind, best-effort; unknown names
// fall to ToolKindOther.
func toolKind(name string) turnevent.ToolKind {
	switch name {
	case "Read":
		return turnevent.ToolKindRead
	case "Edit", "Write":
		return turnevent.ToolKindEdit
	case "Bash":
		return turnevent.ToolKindExecute
	case "Grep", "Glob":
		return turnevent.ToolKindSearch
	case "WebFetch":
		return turnevent.ToolKindFetch
	case "Task":
		return turnevent.ToolKindThink
	default:
		return turnevent.ToolKindOther
	}
}

// toolStatus maps a tool_result's is_error flag to a terminal status: a
// tool_result marks the call finished, so completed/failed (never pending).
func toolStatus(isError bool) turnevent.ToolStatus {
	if isError {
		return turnevent.ToolStatusFailed
	}
	return turnevent.ToolStatusCompleted
}

// toolResultContent maps a tool_result's content union to ToolContent. Empty or
// absent content yields nil — the legal status-only ToolUpdate.
func toolResultContent(content any) turnevent.ToolContent {
	text := toolResultText(content)
	if text == "" {
		return nil
	}
	return turnevent.TextContent{Text: text}
}

// toolResultText extracts plain text from a tool_result content union: a string
// returns itself; a []any joins the "text" field of each {"type":"text",…}
// block; anything else returns "".
func toolResultText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := block["type"].(string); t != "text" {
				continue
			}
			s, _ := block["text"].(string)
			b.WriteString(s)
		}
		return b.String()
	default:
		return ""
	}
}

// rawInput carries a tool_use's already-decoded input bytes through opaquely for
// ToolStart.RawInput. Empty/absent input → nil. Unlike mapper.go's rawInput (a
// map re-marshal, which sorts keys), passing claude's raw bytes preserves the
// original key order and avoids a second marshal — RawInput is opaque
// pass-through the consumer never key-orders against.
func rawInput(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	return in
}
