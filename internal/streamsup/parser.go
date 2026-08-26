package streamsup

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
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
// THE ENVELOPE ARITHMETIC IS NO LONGER COMPLETE, and this commit (#1827) is what
// opened the gap: there are THREE terms now and only two of them are bounded. Per
// ENTRY the THREE-STRING worst case is maxModelResolved + maxModelValue +
// maxModelDisplayName = 768 bytes of claude-derived text, 1.2% of the v2
// application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap). 768 is written here as the MULTIPLICAND FOR THOSE
// THREE rather than as the per-entry total, and the factor it is multiplied by is
// maxModelListEntries — which carries the aggregate arithmetic rather than
// restating it here, exactly as maxTaskRosterEntries carries the roster's. A
// per-entry text cap alone would leave a list's total a function of a number claude
// chooses, which is maxTaskRosterEntries' doctrine; the count bound supplies the
// missing factor, and the one constraint it had to satisfy is checked there: the
// observed list is already SIX entries, so maxTaskRosterEntries' 8 could not be
// reached for by analogy without checking that six fits with room left.
//
// The THIRD term is maxModelEffortLevel multiplied by a level count claude chooses,
// so the honest per-entry figure is 768 + 32N. Nothing bounds N: the element cap
// bounds each level STRING and no cap bounds how many levels arrive. Closing it is
// #1821's, and maxModelListEntries' derivation carries the arithmetic showing that a
// count cap alone cannot do it. Stated here rather than left to be re-derived,
// because this is the paragraph a reader lands on to learn what the per-entry budget
// IS, and inheriting the old completeness sentence would be inheriting a product the
// numbers no longer support.
//
// Amplification from input to retained bytes is bounded but NOT near 1 the way the
// scalar targets' is, and this is the one place that difference shows. The cap is
// applied AFTER json.Unmarshal, so a hostile array is materialised in transient
// memory before any of it is bounded — maxTaskRosterEntries' transient paragraph,
// verbatim. Three facts bound the exposure: defaultMaxParseBuf caps the whole line
// at 4 MiB before the decoder sees it and the densest legal entry is `{}`, so one
// pathological line is order 100 MB of transient; it is TRANSIENT, not retained,
// because turnbridge.MapEvent's default drops the event and frees it, one in flight
// at a time; and nothing puts it in the eventring or on the wire until #1693.
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
// It bounds the ELEMENT and nothing bounds the COUNT — the gap maxModelResolved's
// envelope paragraph states and maxModelListEntries' derivation quantifies, and
// which #1821 closes.
const maxModelEffortLevel = 32

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
//   - Multiplicand: 768 bytes per entry (maxModelResolved + maxModelValue +
//     maxModelDisplayName), inherited from maxModelResolved's doc, which states it
//     for this constant rather than leaving it to be re-derived. Since #1827 it is a
//     FLOOR on per-entry bytes rather than the total — see the third-term paragraph
//     under this list.
//   - Ceiling: 8192 bytes, maxTaskRosterEntries' product, which is exactly HALF of
//     maxUnrecognizedRaw's whole-line 16 KiB and is how the package keeps its
//     ordering one level up — a whole KNOWN event must not approach the cap on an
//     entire UNKNOWN line. 8192 / 768 = 10.67, so 10 is the largest count whose
//     THREE-STRING product stays under that landmark.
//   - Floor: the observed list is SIX entries (the committed capture, claude
//     2.1.239), the check maxModelResolved's doc requires. 10 leaves four slots.
//   - Product: 10 * 768 = 7680 bytes OF THE THREE STRINGS, 11.7% of the v2
//     application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
//     Application-envelope size cap) and 46.9% of maxUnrecognizedRaw — just under
//     the roster's half, so the ordering holds with the roster's own margin.
//     Escaping is mild for maxUnrecognizedRaw's reason verbatim: these are JSON
//     string values, so the growth is quotes and backslashes rather than a \u00XX
//     expansion of every byte. Pathological all-quote content roughly doubles it —
//     ~15 KB, ~23% of the envelope.
//
// THAT PRODUCT BOUNDS THE THREE STRINGS AND NOTHING ELSE since #1827 added
// maxModelEffortLevel: an entry also carries a level LIST whose ELEMENTS are capped
// at 32 bytes and whose LENGTH is capped by nothing, so the real per-entry figure is
// 768 + 32N. The arithmetic is worth writing out because it shows the gap cannot be
// closed by adding a count bound alone: 10 * (768 + 32N) <= 8192 requires N <= 1.6,
// so ANY level-count cap of 2 or more breaks the product above. #1821 must therefore
// move this constant, or the 8192 ceiling, or both, in addition to bounding N — and
// which of those levers is right is #1821's decision, deliberately not made here.
// Nor does a thinner per-level cap avoid it: keeping 10 entries under 8192 with the
// five levels claude actually sends would need a level cap of 10 bytes, which would
// cut a level claude has not shipped yet and would fire on ORDINARY output, the
// failure the NOT 8 paragraph rejects for the count.
//
// NOT 8, borrowed from maxTaskRosterEntries by analogy. Two slots above an
// observation of six is not room, and the failure that buys is a cap firing on
// claude's ORDINARY output — the menu everyone sees, cut, on every child.
//
// NOT a power of two, unlike every other constant in this family, because 768 =
// 3 * 256 puts none of them near the ceiling: 8 is the too-tight case above and 16
// lands the product at 12 KiB, 75% of the whole-line UNKNOWN budget. The decimal is
// what satisfies both binding constraints at once.
//
// The multiple over the observation is thinner than the text caps' 10x, and that is
// deliberate: a text cap's overflow mangles an identifier in place, while this one
// shortens a list and says BY HOW MANY, so a client can render "6 of 40" rather
// than a wrong menu. The reported failure mode is what buys the thinner margin.
//
// 768 counts claude-derived text only. An entry can also carry up to three
// DAEMON-authored names in TruncatedFields (~40 bytes), which claude cannot inflate
// and which maxTaskRosterEntries' arithmetic likewise excludes.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened — maxTaskRosterEntries' accepted trade,
// with maxModelResolved's amplification paragraph bounding the exposure. This cap
// bounds what is RETAINED and what crosses the wire, which is the property that
// matters.
const maxModelListEntries = 10

// controlResponseSuccess is the ONE response.subtype whose payload this parser
// will read. Byte-exact equality against a DAEMON-authored constant, never a fold
// or a prefix: it is half of emitModelList's conjunctive gate, and the half that
// refuses to read an inventory out of a response reporting FAILURE. Everything
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
const (
	controlResponseMsg = "streamsup: consuming solicited control_response"

	controlResponseNAK         = "nak"         // subtype was not controlResponseSuccess
	controlResponseAck         = "ack"         // success, no model list on the line
	controlResponseUndecodable = "undecodable" // the nested shape did not decode
	controlResponseModelList   = "model_list"  // one turnevent.ModelList emitted
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
// The initialize payload's other thirteen top-level keys are absent for that same
// reason and one of them is why it matters: `account`. It is never decoded, never
// bounded, never retained and never logged, because it is not on this struct.
type controlResponseLine struct {
	Response struct {
		Subtype  string `json:"subtype"`
		Response struct {
			Models []modelOptionLine `json:"models"`
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
// supportedEffortLevels it lands as nil, which is the same ZERO-LENGTH shape an
// absent key gets — without that being a claim that the two MEAN the same thing,
// which is #1828's to settle and turnevent.ModelOption.EffortLevels' to state.
type modelOptionLine struct {
	ResolvedModel    string   `json:"resolvedModel"`
	Value            string   `json:"value"`
	DisplayName      string   `json:"displayName"`
	EffortLevels     []string `json:"supportedEffortLevels"`
	SupportsAutoMode bool     `json:"supportsAutoMode"`
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
		p.emitUser(sl.Message)
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
// turnevent.ModelList. It never emits an Unrecognized, and it returns nothing:
// consumeLine's case arm consumes the line by MATCHING, exactly as emitRateLimit's
// does, so unlike the emitSystemSubtype family there is no "did you handle it?" to
// report back. Field mapping comes from the committed capture, read in the tests
// through capturedInitializePayload, never from a hand-built payload.
//
// THE DISCRIMINANT is the substance of this mapping; the field copying is routine.
// claude answers three different requests on this one line type and only one of the
// replies carries a payload the daemon reads, so the gate is CONJUNCTIVE —
// subtype == controlResponseSuccess AND a non-empty decoded models array — and
// each half is load-bearing on its own:
//
//   - The models half alone already excludes all three sibling shapes on record: a
//     set_permission_mode success (whose inner response is `{"mode":"default"}`), a
//     set_permission_mode NAK (which carries an `error` string and no inner
//     response), and an interrupt ack (which carries no inner response at all).
//     None of them has the key.
//   - The subtype half is what stops a payload being read out of a response that
//     reported FAILURE. It is what makes the classification total as well: every
//     subtype that is not success — including an absent one — lands on the nak rung
//     rather than falling through to a shape test.
//
// IT RECOGNISES A SHAPE, NOT A CORRELATED REPLY, and the limit is written here so a
// later reader does not infer more than the code claims. This says "the line
// carries a success-subtype model list", not "this is the reply to the initialize
// request THIS daemon sent": claude authors the inner response object on every
// control response, so a future claude putting a models array inside some other ack
// would have that ack read as an inventory. The consequence is bounded — the value
// would still be claude's own claim about itself, bounded by the same three caps,
// retained by nothing, published to nobody, and acted on nowhere. The alternative
// buys real provenance and costs new cross-object state: Runner.nextControlID mints
// the id inline at the call site and nothing retains it, exactly as its two sibling
// writers discard theirs, so the parser holds no link to it. That trade is worth
// revisiting at #1693, when the value first reaches a client and provenance starts
// to matter; it is not worth new parser state in a slice nothing publishes.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// streamLine's doc states the property it preserves: control shapes are read from
// the top level only and nested content is never re-scanned, which is what stops a
// tool result whose text is literally `{"type":"result"}` from forging a turn
// boundary. Decoding this payload from anywhere else would let claude's own tool
// output announce a model inventory the daemon never asked for.
//
// FOUR RUNGS, total over the input, none of which can panic and none of which
// surfaces an Unrecognized:
//
//  1. The line will not decode into the shape — a `models` that is a number or an
//     object, a non-object `response` at either level — → undecodable, no event.
//  2. subtype is not success → nak, no event. The rung the arm's old doc named as
//     the accepted gap.
//  3. models absent, null, empty, or decoded empty → ack, no event. NOT settled by
//     rung 4's naive reading: emitRateLimit's rung 3 is the precedent, and its
//     argument carries over unchanged — a ModelList carrying zero entries names no
//     model, so it cannot serve the purpose the variant exists for, and emitting it
//     would be the daemon reporting an inventory it never observed. The safe failure
//     direction here is the false NEGATIVE, and nothing publishes the value until
//     #1693, so no client can read a false zero in the meantime.
//  4. success and a non-empty array → one ModelList.
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
// The level LIST goes through a cap like the three strings and reports like none of
// them: ONE name in TruncatedFields per entry however many of that entry's levels
// were cut, because the report names FIELDS and a list is one field. What an absent
// supportedEffortLevels READS AS is likewise turnevent.ModelOption.EffortLevels' to
// say rather than this function's, and it is OPEN (#1828) — which is why nothing
// below turns an absent key's nil into an empty slice or a published empty array
// into nil. Either would be this function answering a question it does not own.
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
		p.logControlResponse(controlResponseUndecodable, 0, 0)
		return
	}
	if cr.Response.Subtype != controlResponseSuccess {
		p.logControlResponse(controlResponseNAK, 0, 0)
		return
	}
	entries := cr.Response.Response.Models
	if len(entries) == 0 {
		// Rung 3. An absent `models`, a null one, an empty array, and a response
		// object carrying no such key all land here and are answered identically.
		p.logControlResponse(controlResponseAck, 0, 0)
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
		// boundEach is bound's sibling for the one field of this entry that is a LIST,
		// and it exists rather than a fourth bound call because a list is where ONE
		// name has to cover MANY values. Three properties, each load-bearing:
		//
		//   - An empty input is returned UNCHANGED and appends nothing, so nil stays nil
		//     and empty stays empty. That is the no-normalisation rule expressed as the
		//     absence of code: which of the two an absent key should read as is #1828's
		//     (turnevent.ModelOption.EffortLevels), and allocating either shape here
		//     would be answering it.
		//   - Every element goes through truncateField into a slice of the SAME length,
		//     so cutting an element neither drops it nor disturbs claude's order.
		//   - The name is appended AT MOST ONCE, after the loop, and only if some
		//     element was cut. Appending inside the loop would name the field once per
		//     cut level, which is the realistic mistake and the one the three-over-long
		//     row of TestParser_ModelListFieldsAreCapped exists to catch.
		//
		// It is named for the ELEMENT the cap applies to, because what bounds the list's
		// LENGTH is nothing yet — see maxModelEffortLevel and #1821. It closes over the
		// same per-entry `cut` slice bound does and cannot be hoisted for the same
		// reason.
		boundEach := func(values []string, name string, limit int) []string {
			if len(values) == 0 {
				return values
			}
			out := make([]string, len(values))
			var cutAny bool
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

		models = append(models, turnevent.ModelOption{
			// claude's values VERBATIM: no lowercasing, no alias expansion, no
			// date-stamping, no family mapping, no lookup against any published model
			// list (#1600's rule). The cap is the only judgement made about them here.
			ResolvedModel: resolvedModel,
			Value:         value,
			DisplayName:   displayName,
			// Through `boundEach` rather than `bound`: the cap is per ELEMENT and the
			// report is per FIELD. claude's order and the list's cardinality survive a cut
			// untouched, and #1600's verbatim rule covers the elements exactly as it covers
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

	p.logControlResponse(controlResponseModelList, len(models), dropped)
	p.emit(turnevent.ModelList{Models: models, DroppedModels: dropped})
}

// logControlResponse writes emitModelList's ONE record, and it exists so the
// content-free rule is decided in a single place rather than on each of the four
// rungs. Every control_response produces exactly one of these, whatever it was a
// reply to.
//
// Four attributes and NOTHING else. `type` is a constant here rather than
// sl.Type, which the case arm's match makes byte-identical; `reason` comes from the
// closed keyword set at controlResponseMsg; `models` is the emitted entry count and
// `dropped` how many maxModelListEntries cut, both 0 on the three non-emitting
// rungs. No value, no resolvedModel, no displayName, no request_id, no error string,
// no unmarshal err, no line bytes. The three strings are precisely what #833's
// posture — restated across internal/relay's v2session_settings.go and
// internal/sessions' pool.go as "model / effort / YOLO values are NEVER logged at
// any level" — exists to keep out of a log, and a drop site explaining itself with
// the value it dropped is how that rule usually breaks. Both integers are
// DAEMON-computed and carry none of claude's bytes, which is what admits them where
// no string from the payload is admitted.
//
// `dropped` is here rather than omitted (#1812) for the reason the wire field's own
// doc argues about a permanent zero, one layer down: `models=6` on a reply that
// carried forty reads as "claude offers six models". emitBackgroundTaskRoster logs
// no count and does not oppose this — its only record is the UNDECODABLE drop, so it
// has no success record to complete, while this path has one and completing it is
// consistent. Until #1693 publishes the event, this record is the ONLY observable
// the cap has: without it, a cap firing in production is a cap nobody can know
// fired, and the first evidence that 10 is the wrong number would arrive as a user's
// short menu. The attribute set is FIXED at four on every rung, which is why the
// non-emitting ones pass 0 rather than omitting the key.
func (p *Parser) logControlResponse(reason string, models, dropped int) {
	p.log.Debug(controlResponseMsg,
		"type", "control_response", "reason", reason, "models", models, "dropped", dropped)
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
func (p *Parser) emitUser(msg *streamMessage) {
	if msg == nil {
		return
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
			ToolCallID: block.ToolUseID,
			Status:     toolStatus(block.IsError),
			Content:    toolResultContent(block.Content),
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
