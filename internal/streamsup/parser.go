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
// match, including never-seen ones.
//
// CORRECTED 2026-09-10 (#2245): that sentence used to name task_notification as
// a member of the dropped set, on the ground that it was measured ABSENT on this
// surface and seen once on the headless surface only. Both halves of the
// measurement were accurate when written and the conclusion has expired: #2247
// staged a turn that let a backgrounded command FINISH and captured the line
// here, so the subtype has an arm in emitSystemSubtype now and maps onto
// turnevent.BackgroundTaskUpdated. The naming is removed rather than annotated
// because it was a statement of SET MEMBERSHIP, which is the one kind of claim
// this comment cannot leave standing once false — unlike the census rows above,
// which are dated measurements and stay unedited. What replaces it is what was
// always the authority: the set is emitSystemSubtype's case arms, and this
// comment names no member of it.
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
// AMENDED 2026-09-06 (#2087): the 2026-07-30 amendment's "exactly one user/text
// string is now dropped" is spent. A whole CLASS of user/text block is dropped
// now, keyed on a line-level flag rather than on any string — see
// harnessNoOutputNudge's own 2026-09-06 entry for the trigger, the measurement
// and what stays surfaced. The row above is left unedited: it is an accurate
// record of what #1247 shipped, and this pointer is what keeps it from being read
// as the current rule.
//
// A real-claude test asserts a normal turn produces zero unrecognized events, so
// this list going stale fails the pre-ship gate rather than reaching a client.
var ignoredLineTypes = map[string]bool{
	"system": true,
}

// Parser turns the child's stdout stream-json line stream into neutral
// turnevent.Event values. It is an io.Writer wired as streamsup Config.Stdout;
// each Write consumes every complete '\n'-delimited line and emits zero-or-more
// events per line to the sink, in stream order. A `result` line ends the turn
// (→ TurnEnd); no transcript file is opened, watched, or resolved (AC4) — the
// only input is the Write bytes.
//
// Turn-stateless in everything that describes claude's output. The parser holds
// no turn counter, no awaiting flag and no transcript.
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
// AMENDED 2026-09-08 (#2224): there are TWO pieces of cross-line state, and the
// clause the paragraph above rested on — "remembers nothing any line SAID: every
// mapping is a pure function of the line it reads" — is now false rather than
// merely narrower, so it has been struck rather than qualified.
// assistantErrorCategory holds up to 256 bytes of claude's own text, read off an
// `assistant` line and published on the `result` line's turn_end. #1385's defence
// ("a COUNTER, not a memory of anything claude said") does not stretch to cover it
// and is not asked to.
//
// The RESET is unchanged and still the whole of the cross-turn argument: the same
// `result` arm clears both, unconditionally, before the emit, and there is still no
// second reset point. A category read during a turn cannot reach the next turn's
// turn_end through any completed boundary.
//
// THE RESIDUAL IS WHERE THE TWO FIELDS DIVERGE, and the counter's argument must not
// be inherited here by resemblance. A child that dies WITHOUT a `result` line leaves
// a residual on a long-lived parser (cmd/pyry builds one per session, not per turn,
// and streamsup rebinds the same one across respawns). For the counter that costs at
// most one event arriving early, bounded by the invariant at its field. For a
// remembered CATEGORY it would mean reporting one turn's rate_limit on a later turn's
// turn_end — a wrong claim rather than an early event, and nothing about the field
// bounds it.
//
// WHAT ANSWERS IT IS THE WRITE, NOT A SECOND RESET. Every `assistant` line writes the
// field, an absent `error` key writing empty, so the residual survives only until the
// next assistant line — inside the next turn, and long before its boundary. That is
// last-writer-wins at the one write site rather than a second boundary to keep
// correct, which is the property having ONE boundary buys.
//
// The narrowing is not an elimination, and the remainder is accepted rather than
// unnoticed: a turn that emits NO assistant line at all before its `result` still
// reports the dead turn's category.
// TestParser_AssistantErrorCategory_ResidualSurvivesAResultlessTurn pins exactly that,
// so the day someone adds a child-restart clear, the test that reddens is the notice
// that this decision moved. The latch's own cost — an error-bearing line followed by
// a clean one INSIDE one turn drops the category — is the fail-closed direction: a
// dropped category is precisely today's behaviour, while a stale one is a false
// statement about the operator's account.
//
// AMENDED 2026-09-08 (#2227): there are THREE pieces of cross-line state. compacting
// is the open/closed edge of a compaction banner, and it takes NEITHER of the two
// arguments above by inheritance. It is not a counter, so #1385's defence does not
// reach it; and #2224's answer — the write launders the residual — has no analogue,
// because no line writes this field on the way past.
//
// WHAT IS ACTUALLY NEW IS THAT THE RESET BECAME OBSERVABLE. A client MIRRORS this
// field: the desktop lights a banner on the rising edge and clears it on the falling
// one. The two fields above could be reset in silence because nothing outside the
// parser held a copy — for the counter nothing at all, for the category the same emit
// that published it. Clearing this one in silence would leave the parser and the
// banner disagreeing with no later line to reconcile them, so the `result` arm emits
// a falling edge before clearing. THE SINGLE RESET POINT IS UNCHANGED and remains the
// whole of the cross-turn argument; what moved is that the reset now says so on the
// wire. Its residual is bounded by that same reset and is argued at the field.
//
// AMENDED 2026-09-08 (#2234): there are FOUR pieces of cross-line state.
// deniedThisTurn is the set of tool_use_ids that already produced a denial marker
// from their own line this turn, and it takes none of the three arguments above by
// inheritance. It is not a counter; no line writes it on the way past, so #2224's
// laundering has no analogue; and nothing outside the parser mirrors it, so #2227's
// published reset is not owed.
//
// WHAT IS NEW IS THE DIRECTION ITS RESIDUAL FAILS IN, and that is the one thing this
// entry exists to say. Every field above fails by SPEAKING — an event early, a wrong
// category, a banner outliving its cause. A stale id here fails by SILENCE: it
// suppresses a later turn's genuine marker, which is the defect #2234 fixes,
// restored in the one case nobody would look. Three things bound it and no second
// reset is minted. The `result` arm clears the set unconditionally, so only a child
// that dies without a `result` leaves one at all. claude's tool_use_ids are per-call
// unique, so a later turn colliding with a stale id is not a shape claude produces.
// And a collision could only suppress a marker for an id the client has ALREADY seen
// a denial for, so the loss is a second report of one call, never the only report of
// one. THE SINGLE RESET POINT IS UNCHANGED and remains the whole of the cross-turn
// argument.
//
// AMENDED 2026-09-10 (#2250): there are SIX pieces of cross-line state, and this is
// the entry the chain above exists to make possible. The count jumps from FOUR
// because #2246's taskProgressToolUses added a fifth field and no entry: it is
// cleared at the one reset like its neighbours, publishes nothing when it is, and
// took every argument above by inheritance, so it had nothing to add here.
// rateLimitNonBenign is the opposite case in the one dimension that matters.
//
// IT IS THE FIRST PIECE OF CROSS-LINE STATE THE SINGLE RESET POINT DOES NOT CLEAR,
// and that is a departure from the paragraph every entry above rests on rather than
// an exemption from it. The reason is arithmetic, not preference: claude emits
// rate_limit_event ONCE PER RUN, so the two readings the field exists to relate are
// separated by at least one `result` line and usually by a whole child. Cleared at
// the boundary, the latch would be gone before the reading that would have used it
// ever arrived — the field would exist and never once fire. So the argument that
// bounds its residual cannot be the reset, and is not: it is the NEXT READING, which
// overwrites the latch either way. A child that dies after a non-benign reading
// costs one extra benign frame describing a window claude has just reported, which
// is compacting's fail-safe direction reached without a second reset path.
//
// #2227's published-reset question does arise and is answered differently. A client
// mirrors this state too — it is a banner, exactly as compacting's is — but the
// clear is not a silent state change needing a frame to announce it: the clearing
// FRAME IS the state change, carrying claude's own benign reading. There is nothing
// to publish separately because nothing happens separately.
//
// AMENDED 2026-09-10 (#2263): there are SEVEN pieces of cross-line state.
// apiRetryOpen records that at least one system/api_retry line has published an
// active update. It resembles compacting because a client mirrors the boolean and
// therefore needs an observable clear, but its boundary is wider: the next
// successfully decoded assistant, user, OR result line closes it before that line's
// events. Claude emits no explicit retry-end line, and the committed capture reaches
// assistant before result, so waiting for the one result reset would leave the retry
// state lit while assistant output had already resumed.
//
// Repeated active lines are never suppressed. Each carries a later attempt counter,
// so the latch guards only the falling edge. A child that dies while it is set leaves
// both parser and client in the same active state; the next assistant/user/result
// after respawn publishes the clear before its own event. That residual is bounded by
// the next content line rather than by a child-lifecycle reset, preserving one
// observable answer for the client without coupling parser state to process teardown.
//
// AMENDED 2026-09-11 (#2270): stream-event attribution is one composite eighth
// piece of cross-line state. A message_start replaces its message id and open-block
// fields, content-block events advance the open block, and the existing result arm
// clears the composite. It retains only one id, one block discriminator and scalar
// routing state; delta text is emitted directly and is never accumulated here.
//
// Single-writer invariant: os/exec drives a non-*os.File Config.Stdout through
// exactly one internal goroutine (io.Copy of the child's stdout pipe into this
// writer), so Write — hence buf and the sink calls — is only ever invoked
// serially from that one goroutine. There is no second reader of parser state,
// so no mutex is needed. (Contrast streamrunner's streamParser, which locks
// because its watchdog goroutine reads its state.) If a future slice adds a
// concurrent reader, it adds the guard then. One Parser serves every child of a
// runner, and the one other toucher of buf, dropPartialLine, runs on the Run
// goroutine only after cmd.Wait has returned: Wait joins the stdout copy
// goroutine, even past WaitDelay, so the dead child's last Write happens-before
// the drop and the drop happens-before the next child's first Write (#1503).
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

	// assistantErrorCategory is the wrapper-level API error the most recent
	// `assistant` line reported, bounded at decode by decodeAssistantError and
	// published on the next turn_end (#2224). Rewritten by EVERY assistant line —
	// an absent `error` key writes empty — and cleared with thinkingSinceEmit at
	// consumeLine's one reset. See Parser's doc for why the write is a latch rather
	// than a sticky set; it is the residual argument, not a style choice.
	//
	// THE FIRST FIELD HERE THAT HOLDS CLAUDE'S OWN BYTES, so two properties it
	// inherits are worth stating rather than assuming. Its size is bounded by
	// maxTurnEndStopField and it holds one value, never a list, so no input can grow
	// it. And it is retained only between two lines — nothing formats a *Parser and
	// no debug bundle reaches parser state, so it is not reachable by any dump.
	//
	// The single-writer invariant covers it exactly as it covers thinkingSinceEmit,
	// including across a child respawn: the runner rebinds this parser as the new
	// child's Stdout, and cmd.Wait returns only after the previous forwarder
	// goroutine has finished, which is the happens-before edge between the two
	// writers. A future concurrent reader guards both fields, not one.
	assistantErrorCategory string

	// compacting is the open/closed state of the compaction edge pair (#2227): true
	// between the system/status line reporting status:"compacting" and the one
	// reporting anything else. Written only by emitCompactingStatus and by
	// consumeLine's one reset; see emitCompactingStatus for the state machine.
	//
	// THE FIRST FIELD HERE WHOSE VALUE A CLIENT MIRRORS, and that is what makes its
	// reset different in kind from the two above rather than merely third. The
	// counter's reset is invisible by construction and the category's is published by
	// the very emit it precedes; this one has to be PUBLISHED to be a reset at all,
	// because the desktop's banner holds a copy and a silent clear would leave the
	// two disagreeing with no later line to reconcile them. consumeLine's `result`
	// arm therefore emits a falling edge before it clears the field. Still the one
	// reset point — what changed is that the reset is observable, not where it is.
	//
	// ITS RESIDUAL IS BOUNDED BY THAT RESET RATHER THAN BY A WRITE, which is where it
	// parts company with #2224 and the parting must not be papered over. A child that
	// dies mid-compaction leaves this true on a long-lived parser (cmd/pyry builds one
	// per session, not per turn) and there is no per-line write to launder it the way
	// every assistant line launders the category. What makes that acceptable is that
	// the stale value is not a false claim about a later turn: the client's banner is
	// already lit, the stale true correctly suppresses a duplicate rising edge, and
	// the next `result` line emits the falling edge and clears. The cost is a banner
	// outliving its compaction by at most one turn boundary — the fail-safe direction,
	// and cheaper than a second reset path, which would buy a second boundary to keep
	// correct for a field whose whole design rests on there being one.
	//
	// The single-writer invariant covers it exactly as it covers the two above,
	// including across a child respawn. A future concurrent reader guards all THREE
	// fields, not two.
	compacting bool

	// deniedThisTurn holds the tool_use_ids that already produced a
	// turnevent.ToolCallDenied from their own system/permission_denied line during
	// the turn now open (#2234). Written only by emitPermissionDenied, read and
	// cleared only by consumeLine's one reset; emitRecoveredDenials consults the
	// cleared-off copy to decide which of the `result` line's permission_denials
	// entries went unannounced.
	//
	// LAZILY ALLOCATED and cleared by setting nil, so a session that never denies a
	// call — every session on a posture claude does not refuse anything in — pays no
	// allocation at all. A nil set reads as empty, which is the same answer an empty
	// one gives, so no call site distinguishes the two.
	//
	// BOUNDED IN BOTH DIRECTIONS, and it is the first field here that holds a
	// COLLECTION of claude's bytes rather than one value or none. maxTurnDenials caps
	// the CARDINALITY and maxTaskFieldID has already capped each id before it is
	// recorded — emitPermissionDenied records the value it published, after the drop,
	// and records nothing when that value is empty. So no input can grow this past
	// 16 * 256 bytes, and an id too long to publish is also too long to remember. Like
	// assistantErrorCategory it is retained only inside a turn, nothing formats a
	// *Parser, and no debug bundle reaches parser state.
	//
	// See Parser's doc for the residual argument, which is the one thing that does NOT
	// carry over from the three fields above: this one's stale value fails by
	// suppressing a later marker rather than by publishing a wrong one.
	//
	// The single-writer invariant covers it exactly as it covers the three above,
	// including across a child respawn. A future concurrent reader guards all FOUR
	// fields, not three.
	deniedThisTurn map[string]struct{}

	// taskProgressToolUses maps a background task's id to the cumulative
	// usage.tool_uses carried by the last system/task_progress line that PRODUCED an
	// event for it, during the turn now open (#2246). Written and read only by
	// emitBackgroundTaskProgress, cleared only by consumeLine's one reset.
	//
	// NOT AN ACCUMULATOR, which is the difference from thinkingSinceEmit above and
	// the property AC 4 rests on. That field sums deltas claude supplied; this one
	// only ever REMEMBERS a number claude sent, so nothing published on a
	// BackgroundTaskProgress is arithmetic the daemon did. The subtraction that
	// decides an emit reads this value and discards the result.
	//
	// A MAP RATHER THAN A COUNTER, and that is forced rather than chosen. claude's
	// counters here are cumulative PER TASK, so a single turn-wide counter would mix
	// two tasks' progress into one bound and let a busy task silence a quiet one.
	// The cost is that the key set comes from the far side of the connection, which
	// is a memory-growth surface rather than a tidiness question — so this follows
	// deniedThisTurn in every dimension:
	//
	//   - LAZILY ALLOCATED and cleared by setting nil, so a session that never
	//     delegates pays no allocation at all. A nil map reads as empty on lookup and
	//     len, which is the same answer an empty one gives, so no call site
	//     distinguishes the two.
	//   - BOUNDED IN BOTH DIMENSIONS. maxTaskProgressTasks caps the CARDINALITY, and
	//     maxTaskFieldID has already capped each id before it is recorded — the emit
	//     function records the value it published, after the cut. So no input can
	//     grow this past 8 * (256 bytes + one int).
	//   - RETAINED ONLY INSIDE A TURN. Nothing formats a *Parser and no debug bundle
	//     reaches parser state, so the ids are not reachable by any dump.
	//
	// INVARIANT: every value stored here is > 0. The emit function's guard refuses a
	// non-positive tool_uses before any store, and the re-baseline branch stores a
	// value that already passed it. `seen - stored` is therefore a subtraction of two
	// non-negative ints, which cannot overflow whatever claude sends — the reason the
	// crossing test is written subtracted, exactly as thinkingSinceEmit's is, and
	// with the pressure HIGHER here because the second operand is the daemon's own
	// retained number rather than one claude supplied fresh on each line.
	//
	// NOTHING DELETES AN ENTRY WHEN A TASK ENDS. A delete on the task_notification
	// arm would couple two arms and buy a second boundary to keep correct, which is
	// what having one boundary avoids. The residual across a child that dies without
	// a `result` fails in the safe direction: an extra event for a task whose counter
	// is already past the bound, never silence.
	//
	// The single-writer invariant covers it exactly as it covers the four above,
	// including across a child respawn. A future concurrent reader guards all FIVE
	// fields, not four — and this is the SECOND that holds a collection of claude's
	// bytes, so the read-modify-write it performs is the one that would need the
	// guard first.
	taskProgressToolUses map[string]int

	// rateLimitNonBenign is true between a non-benign rate_limit_info.status and the
	// benign reading that clears it (#2250). Written and read only by emitRateLimit;
	// see that function's rung 4 for the state machine.
	//
	// THE FIRST FIELD HERE THAT consumeLine's ONE RESET DOES NOT CLEAR, and that is
	// the substance of #2250 rather than an omission in it. Every field above is
	// cleared there. This one cannot be: claude emits rate_limit_event ONCE PER RUN,
	// the parser outlives the run (cmd/pyry builds one per session and streamsup
	// rebinds the same one across child respawns, the invariant
	// assistantErrorCategory's doc states), and in every realistic sequence the two
	// readings are separated by at least one `result` line and usually by a whole
	// child. A latch cleared at the turn boundary would therefore be gone before the
	// reading that would have used it ever arrived — the field would exist and never
	// once fire.
	//
	// ITS RESIDUAL IS BOUNDED BY THE NEXT READING RATHER THAN BY ANY RESET, which is
	// where it parts company with compacting above. A child that dies after a
	// non-benign reading leaves this true, and the cost is ONE extra benign frame on
	// the next `allowed` reading — a frame reporting a window claude has just
	// described, which is a true statement rather than a stale one. The direction is
	// the same fail-safe one compacting's residual takes, reached without a second
	// reset path.
	//
	// IT HOLDS NO VALUE CLAUDE SENT. A bool, so no input can grow it and no
	// remembered text can be republished — which is what makes "the clearing frame's
	// fields come from the clearing reading, never from the remembered one" a
	// property of the shape rather than a rule somebody has to keep. Like its
	// neighbours it is not reachable by any dump: nothing formats a *Parser and no
	// debug bundle reaches parser state.
	//
	// NOTHING IN THE DAEMON MAY KEY A BEHAVIOUR ON IT. It is unexported, has no
	// accessor, and is read at exactly one place to decide whether one display frame
	// is published. turnevent.RateLimited's "a REPORT, never a control input" rule
	// reaches inside the producer here, because this is the first parser state
	// DERIVED FROM CLAUDE'S BYTES that is designed to outlive the run.
	//
	// The single-writer invariant covers it exactly as it covers the five above, and
	// here that invariant does real work rather than describing a buffer: this is the
	// first field that DEPENDS on surviving a child respawn rather than merely
	// tolerating one, so the happens-before edge is load-bearing — cmd.Wait returns
	// only after the previous forwarder goroutine has finished. A future concurrent
	// reader guards it with every other parser field.
	rateLimitNonBenign bool

	// apiRetryOpen is true after any system/api_retry line and until the next
	// assistant, user, or result line. Every retry line still emits an active update;
	// this latch suppresses only duplicate inactive edges. clearAPIRetry owns the
	// transition so all three closing arms publish the edge before their existing
	// events.
	//
	// It holds no Claude-authored value. The subtype-specific counters are emitted
	// directly and never retained, so no input can grow this state and nothing stale
	// can be republished after a child respawn. Parser's single-writer invariant covers
	// the field without a mutex.
	apiRetryOpen bool

	// streamMessageID is the message identity supplied by the latest valid
	// stream_event/message_start. The three block fields describe the one currently
	// open content block. streamBlockTextDelta records only that the block has already
	// published text; model output itself is never retained. message_start replaces
	// this composite and consumeLine's result arm clears it.
	streamMessageID      string
	streamBlockIndex     int
	streamBlockType      string
	streamBlockOpen      bool
	streamBlockTextDelta bool

	// postureGate is the ack signal this parser releases for the runner it is
	// installed on (#2064). Minted in NewParser, read by the runner through
	// PostureGate(). It carries its OWN mutex, so it is the one piece of parser state
	// that is legitimately touched from another goroutine and the single-writer
	// invariant above does not extend to it.
	postureGate *PostureGate

	// contextUsageRequests correlates the locally requested context readings whose
	// replies may emit. Its own mutex covers calls from Runner while Write consumes
	// stdout on another goroutine.
	contextUsageRequests contextUsageRequests

	// contextUsageQueries correlates requester-private context readings (#2430). Its
	// relation to contextUsageRequests above is the one mcpActuations has to
	// mcpStatusQueries: a separate map under a disjoint id prefix, because a claimed
	// reading completes one waiter and must NOT also reach the shared event sink,
	// where the automatic post-turn ask still publishes. Its own mutex covers
	// registration from callers while Write consumes stdout on another goroutine.
	contextUsageQueries contextUsageQueries

	// appliedSettingsQueries correlates requester-private get_settings reads. A
	// claimed reply completes one waiter and reaches no shared event or record. Its
	// own mutex covers registration from callers and claims from the stdout forwarder.
	appliedSettingsQueries appliedSettingsQueries

	// mcpStatusQueries correlates requester-private status reads. Unlike the
	// automatic status path below, a claimed response completes one waiter and
	// never reaches the shared event sink. Its own mutex covers registration from
	// relay workers and claims from the stdout forwarder.
	mcpStatusQueries mcpStatusQueries

	// mcpActuations correlates daemon-private MCP reconnect, toggle and task-stop acks,
	// with the same privacy property as mcpStatusQueries above: a claimed ack
	// completes one waiter and never reaches the shared event sink. Its own mutex
	// covers registration from callers while Write consumes stdout on another
	// goroutine.
	//
	// A SECOND MAP rather than a shared one, because the two id namespaces must stay
	// disjoint: mcpStatusQueries.claim consumes every unregistered id carrying ITS
	// prefix, so an actuation minting under that prefix would have its acks swallowed
	// before this correlator saw them — a hang, not a miss.
	mcpActuations mcpActuations

	// mcpStatusPolicy belongs to the current child. The runner replaces it before
	// cmd.Start, and cmd.Wait joins the prior stdout forwarder before another child
	// can replace it, so Parser's single-writer lifecycle covers the fields without
	// another lock.
	mcpStatusPolicy mcpStatusChildPolicy

	// permissionModes retains the last posture claude confirmed for this parser's
	// exact current child. Unlike mcpStatusPolicy it has its own mutex: Runner
	// registers writes and reads the confirmed value from arbitrary goroutines while
	// Parser.Write consumes acknowledgements on the stdout forwarder.
	permissionModes confirmedPermissionModes

	// canUseTool is the seam an answerer installs to receive claude's inbound
	// permission asks (#2282). Nil until SetCanUseToolHandler is called, and nil is
	// the production state today: nothing spawns with --permission-prompt-tool stdio,
	// so no ask is produced and no handler is installed. #2284 changes both together.
	//
	// A SEAM, NOT STATE, and the distinction is what keeps it off the single-writer
	// invariant above. No line reads it for meaning, no reset clears it, no residual
	// can outlive a turn or a child: it is a destination, written once before the
	// parser is handed to a runner and read on the forwarder goroutine thereafter.
	// SetCanUseToolHandler states that happens-before as its contract, which is why
	// this field carries no mutex where postureGate does — that one is genuinely
	// touched from another goroutine and this one must not be.
	canUseTool func(CanUseToolRequest)
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
		// Minted HERE and handed to the runner through PostureGate() rather than
		// accepted as a parameter (#2064). The parser is built BEFORE streamsup.New —
		// newStreamRunnerFactory installs it as Config.Stdout — so the gate has to
		// exist ahead of both halves, and minting it with the parser makes binding the
		// two to different gates impossible, which is newSessionParser's own argument
		// for minting a parser and its holds in one call. It also leaves this
		// constructor's signature alone, which nine call sites depend on. A parser with
		// no runner mints an inert gate: nothing arms it, so it stays open.
		postureGate: &PostureGate{},
	}
}

// PostureGate returns the ack signal this parser releases when claude confirms a
// spawn-time permission-mode write (#2064). The value goes onto streamsup.Config, so
// the runner arming the gate and the parser releasing it are the same object by
// construction. Never nil for a parser built by NewParser.
func (p *Parser) PostureGate() *PostureGate { return p.postureGate }

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

// dropPartialLine discards the unterminated remainder a departed child left
// behind, so the respawned child's first line is parsed on its own instead of
// being spliced onto a dead child's mid-line bytes into one Unrecognized event.
// Call it only once that child's stdout copy has finished — see the Parser's
// single-writer invariant.
func (p *Parser) dropPartialLine() {
	p.buf = nil
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

// jsonKey is a presence-only decode target: a *jsonKey field is non-nil exactly
// when its key is present with a non-null value, and retains NO BYTE of it.
//
// That is what lets a shape be recognised by a key whose VALUE must never be
// read into daemon state. `oldString` and `newString` identify an edit and are
// the entire pre- and post-edit text; `content` identifies a write and, on a
// SEARCH sidecar spelled identically, is the whole grep output. Declaring any of
// them as a string to test for presence would put claude's bytes on the decode
// target for no gain. The keys are discriminators rather than values.
//
// It also answers the present-but-EMPTY question the write shape turns on:
// `structuredPatch: []` is a present non-null value, so it is distinguishable
// from an absent key, which is the pointer-vs-present-zero rule again.
type jsonKey struct{}

// UnmarshalJSON discards the value. encoding/json still scans it for
// well-formedness before calling this, so a malformed value fails the whole
// decode rather than being silently accepted as present.
func (*jsonKey) UnmarshalJSON([]byte) error { return nil }

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
		// One known shape gets a second look before the line is called
		// unrecognized: a harness-authored `user` line whose content is a string,
		// which fails the decode above for its content alone. See
		// dropHarnessProseLine. The gate sits INSIDE the failure branch, so the
		// happy path pays nothing for it and no successfully-decoded line can reach
		// it.
		if p.dropHarnessProseLine(line) {
			return
		}
		// A SECOND known shape, and it fails the decode above for the same reason the
		// first one does (#2232): claude's system/permission_denied line carries
		// `message` as a STRING, where streamLine declares *streamMessage, so the
		// whole line errors on that one field and the subtype never reaches
		// emitSystemSubtype. The two gates are disjoint by type — that one requires
		// `user`, this one `system` — and both sit inside the failure branch, so the
		// happy path pays nothing and no successfully-decoded line can reach either.
		if p.consumePermissionDeniedLine(line) {
			return
		}
		// Not even the type is known here, so Kind stays empty. Previously this
		// dropped without recording anything at all.
		p.emitUnrecognized(turnevent.UnrecognizedUndecodable, "", line)
		return
	}
	switch sl.Type {
	case "assistant":
		p.clearAPIRetry()
		// The wrapper-level API error category (#2224), read here rather than inside
		// emitAssistant for two reasons that both bite. It is a sibling of `message`
		// on the LINE, so emitAssistant — which takes only the decoded message —
		// cannot reach it; consumeLine's `user` arm below, which passes the raw line
		// for its own sidecar, is the in-file precedent. And emitAssistant returns
		// early on a nil message and emits nothing for a message with no mappable
		// blocks, so a read inside it would miss exactly the error-bearing line
		// carrying no blocks — which is the likeliest shape for a line whose API call
		// failed, not an edge case.
		//
		// ASSIGNED, NOT OR'd: an assistant line with no `error` key clears the field.
		// That is the latch Parser's doc argues, and it is what keeps a dead turn's
		// category from riding into the next one's turn_end.
		p.assistantErrorCategory = decodeAssistantError(line)
		// The line bytes ride along for a SECOND sibling of `message` since #2191,
		// the spawning Agent call. Passed rather than latched here beside the
		// category, because it belongs to the line rather than to the turn —
		// emitAssistant's doc argues the distinction and why latching it would be a
		// bug.
		p.emitAssistant(sl.Message, line)
	case "user":
		p.clearAPIRetry()
		// The line bytes ride along so emitUser can decode the tool_use_result
		// sidecar, a sibling of `message` rather than a field inside it (#2024) —
		// and, since #2191, the spawning Agent call, a third field of that shape.
		p.emitUser(sl.Message, line)
	case "stream_event":
		p.emitStreamEvent(line)
	case "result":
		p.clearAPIRetry()
		// The turn boundary. The subtype selects the reason: an interrupted turn
		// (subtype error_during_execution, spike T1 #1075) → cancelled; a clean
		// turn and every other subtype → end_turn. Richer max_tokens/refusal
		// classification remains future work (resultTurnEndReason's default).
		//
		// Also the ONE reset point for BOTH pieces of the parser's cross-line state —
		// the #1385 accumulator and #2224's error category. Unconditional and before
		// the emit, so both result subtypes reset and a cancelled turn leaks no
		// residual into the next one. A child that dies WITHOUT a result leaves a
		// residual behind on a long-lived parser (cmd/pyry builds one per session, not
		// per turn); for the counter that is bounded by the invariant at the field —
		// the next turn's first event can arrive at most minThinkingTokensPerEvent-1
		// tokens early. The category's residual has no such bound and is answered by
		// its WRITE instead (see the assistant arm above), because a second reset path
		// would buy a second boundary to keep correct, which is what having one
		// boundary avoids.
		//
		// The category is READ into a local and cleared in the same step, so the clear
		// stays at this one point AND ahead of the emit while the value still reaches
		// it. Reading it off the field inside the composite literal below would work
		// today and break the moment anyone moved the reset a line, which is the kind
		// of ordering dependence a single reset point exists to remove.
		//
		// The line bytes ride along so decodeModelWindows can read the modelUsage
		// map, a sibling of `subtype` rather than a field inside it — the shape
		// emitUser and emitRateLimit both take the raw line for. The call sits
		// BELOW the accumulator reset and its result is passed INTO the same emit,
		// so no decode outcome can reorder, duplicate or suppress the boundary.
		//
		// decodeStopShape (#2223) takes the raw line for the same reason and under
		// the same rule: its own decode target, so no shape of is_error or
		// terminal_reason can reach segmentation, and no shape of modelUsage can
		// reach it. Both calls sit BELOW the accumulator reset and both results are
		// passed INTO the same emit, so no decode outcome can reorder, duplicate or
		// suppress the boundary.
		//
		// THE THIRD FIELD'S RESET IS CONDITIONAL ON THE WIRE (#2227) and that is not
		// an exception being carved out of the paragraph above, it is what a reset MEANS
		// for a value a client mirrors. The state clear is as unconditional as its two
		// neighbours — every result subtype passes here and no other line clears it —
		// but a silent clear would leave the desktop's banner lit against a parser that
		// believes it is dark, and nothing later reconciles the two. So the falling edge
		// is emitted when, and only when, one is open. Emitting it unconditionally would
		// put a compacting:false on every turn that ever ends, which is a claim about a
		// compaction that never ran; the guard is what keeps the frame meaningful.
		//
		// ORDERED BEFORE THE TurnEnd BELOW, deliberately: a client that sees the boundary
		// first has already closed the turn holding a lit banner, which is the stuck
		// banner AC 5 exists to forbid. The two emits are in one arm precisely so that
		// order cannot be separated from the reset it belongs to.
		//
		// THIS EDGE NAMES NO OUTCOME (#2236) and the zero values are a true statement
		// rather than a gap: there is no claude line here to read compact_result or
		// compact_error off, so what closed the banner was the daemon's own turn
		// boundary and claude reported nothing about how the compaction went. A client
		// reads empty for "absent, empty, or the daemon closed it", which are one
		// reading — see turnevent.Compacting's Result.
		if p.compacting {
			p.compacting = false
			p.emit(turnevent.Compacting{Active: false})
		}
		p.thinkingSinceEmit = 0
		errorCategory := p.assistantErrorCategory
		p.assistantErrorCategory = ""
		// THE FOURTH FIELD'S RESET IS THE THIRD THAT READS-AND-CLEARS IN ONE STEP
		// (#2234), on the category's rule above and for its reason: the clear stays at
		// this one point AND ahead of the emit while the value still reaches the arm
		// that needs it. Unconditional, like its three neighbours, so a cancelled turn
		// carries no announced id into the next one — and the direction that matters
		// here is that a residual would SUPPRESS a later marker rather than publish a
		// wrong one, which is argued at the field.
		announced := p.deniedThisTurn
		p.deniedThisTurn = nil
		// THE FIFTH FIELD'S RESET (#2246), and the first here that is a plain clear with
		// nothing read off it: no arm below consults a task's progress history, so unlike
		// the three read-and-clear neighbours above there is no value to carry past the
		// reset. Unconditional like all four, so a cancelled turn carries no task's
		// counter into the next one. Nothing is emitted to announce it, on the compacting
		// field's own test: a reset needs publishing only when a client MIRRORS the
		// value, and a client holds no copy of this — it holds rows the opening and
		// terminal frames drive, which this clear does not touch. The residual direction
		// is argued at the field: an extra event, never silence.
		p.taskProgressToolUses = nil
		p.resetStreamEventState()
		windows, droppedWindows := decodeModelWindows(line)
		isError, terminalReason := decodeStopShape(line)
		// A THIRD DECODE OF THE SAME LINE, joining the two above rather than widening
		// either (#2260). The ordering paragraph below covers it unchanged, and the
		// independence runs both ways: no outcome of the two decodes above can zero
		// these four numbers, and no outcome of this one can disturb the windows, the
		// stop shape, the denials recovered next, or the boundary itself.
		durationMS, durationAPIMS, numTurns, costUSDTotal := decodeTurnTotals(line)
		// A FIFTH DECODE OF THE SAME LINE, isolated for the nested usage counts.
		// A hostile usage cannot suppress any sibling decode or the boundary, and a
		// hostile sibling cannot suppress these counts.
		inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens := decodeTurnUsage(line)
		// ORDERED BEFORE THE TurnEnd BELOW, which is AC 1's real content rather than a
		// detail of it: a client closes the turn on that event, so a marker emitted
		// after it would arrive for a turn already finished — #2232's defect restored
		// under a different name. The call sits BELOW the resets with the three decodes
		// above it, so no outcome of any of them can reorder, duplicate or suppress the
		// boundary.
		p.emitRecoveredDenials(line, announced)
		p.emit(turnevent.TurnEnd{
			// resultTurnEndReason reads the UNBOUNDED subtype, deliberately: the cap
			// governs what is PUBLISHED, and routing the classification through it
			// would let an over-long value move stop_reason — a wire value #2223
			// promises is byte-identical for every subtype.
			Reason: resultTurnEndReason(sl.Subtype),
			// claude's token VERBATIM, per turnevent.TurnEnd.Outcome's rule: no
			// lowercasing, no mapping onto the observed set, no rejection of one it
			// does not name. The cap is the only judgement made about it, and this is
			// the ONE site that publishes the subtype — its only other readers,
			// resultTurnEndReason above and emitSystemSubtype, compare it against
			// literals and carry it nowhere, which is why the bound belongs here.
			Outcome:        boundStopField(sl.Subtype),
			IsError:        isError,
			TerminalReason: terminalReason,
			// Already bounded, at the decode rather than here: unlike the subtype
			// beside it this value was not read off this line, so there is no
			// unbounded original to bound at the publish site.
			ErrorCategory:       errorCategory,
			ModelWindows:        windows,
			DroppedModelWindows: droppedWindows,
			// claude's four numbers VERBATIM and UNDIFFERENCED (#2260). Two of them are
			// running totals and nothing here converts either into a per-turn delta —
			// see turnevent.TurnEnd's shared doc for which two and why the arithmetic
			// would be wrong. No cap is applied at this publish site, unlike the subtype
			// above it, because there is nothing claude can lengthen: the bound is over
			// the Go type's range and holds for every input.
			DurationMS:    durationMS,
			DurationAPIMS: durationAPIMS,
			NumTurns:      numTurns,
			CostUSDTotal:  costUSDTotal,
			// claude's four per-turn counts, unsummed and unconverted. InputTokens is
			// only uncached input; the two cache counts are input-side too.
			InputTokens:         inputTokens,
			OutputTokens:        outputTokens,
			CacheReadTokens:     cacheReadTokens,
			CacheCreationTokens: cacheCreationTokens,
		})
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
		// record and no event, whether they carry an inner payload or none. That stays
		// true after #2064: the posture gate's release is neither a record nor an event.
		//
		// noteControlAck runs ABOVE emitModelList so the gate opens without waiting
		// behind an emit into a downstream sink, and it reads its own decode target, so
		// which rung a models or commands payload lands on is untouched by construction.
		if p.claimMCPStatusQuery(line) {
			return
		}
		// Beside the status claim and for its reason: both consume a daemon-private
		// reply ahead of every shared consumer, so a claimed line reaches no sink and
		// no record. Their id namespaces are disjoint, so the order between these two
		// is free; the order relative to what follows is not.
		if p.claimMCPActuation(line) {
			return
		}
		// This context-usage claim has one ordering
		// constraint the other three do not carry: it must run ABOVE emitContextUsage
		// (#2430). Both context-usage paths land in this arm, so a claim below the emit
		// would publish a caller's private reading onto the shared sink as an
		// unsolicited frame before handing it over. Order among the private claims is
		// free — their id namespaces are disjoint.
		if p.claimContextUsageQuery(line) {
			return
		}
		// get_settings is private too. Claim it before every shared consumer so
		// effective settings and sources present beside applied cannot reach the
		// control-response record or any event path.
		if p.claimAppliedSettingsQuery(line) {
			return
		}
		p.emitContextUsage(line)
		p.noteControlAck(line)
		p.noteConfirmedPermissionModeAck(line)
		p.emitModelList(line)
		if status, ok := decodeMCPStatus(line); ok {
			p.emit(status)
		}
	case "control_request":
		// The first control line claude INITIATES rather than answers (#2282), and the
		// direction is the whole novelty: every other control_request in this package is
		// one the daemon wrote to the child's stdin. Under
		// --permission-prompt-tool stdio claude does not call an approval MCP tool at
		// all — it asks here, on the same stdout the parser already reads, and waits for
		// a control_response on stdin.
		//
		// Without this arm every such ask fell to the default below and rang
		// emitUnrecognized with turnevent.UnrecognizedLineType — the
		// spend-the-alarm-on-a-routine-event failure the control_response, tool_progress
		// and conversation_reset arms above each describe, and here it would fire once
		// per gated tool call.
		//
		// Its own arm rather than an ignoredLineTypes member, for #1404's, #1500's and
		// #2089's reasons verbatim: that list is documented as top-level types only and
		// as MEASURED, and the 2026-07-27 census ran no child under this flag, so it
		// could not have seen this type. Membership would also swallow every OTHER
		// inbound control subtype in silence, which is the opposite of what AC 2 asks
		// for.
		//
		// A MATCHER, and like emitConversationReset it deliberately does NOT hold
		// "emitUnrecognized unreachable BY MATCHING" — the weaker of the two guarantees,
		// taken on purpose twice over. A control request of another subtype and a
		// can_use_tool this parser cannot decode BOTH fall through and still ring the
		// bell, because each is genuinely news: the first is claude initiating something
		// nothing here models, the second is the ask arriving in a shape this codec got
		// wrong. Consuming either silently would hide exactly the report that tells
		// anyone the vendor moved.
		//
		// The subtype is read on its own target before the payload is decoded, so those
		// two outcomes stay distinguishable; see controlRequestSubtypeLine.
		var cr controlRequestSubtypeLine
		if err := json.Unmarshal(line, &cr); err == nil && cr.Request.Subtype == canUseToolSubtype {
			if req, ok := decodeCanUseTool(line); ok {
				// CONSUMED, whether or not anyone is listening. With no handler installed
				// the ask is dropped SILENTLY — no event, no log — and that is safe only
				// because no spawn passes --permission-prompt-tool stdio yet, so no ask can
				// actually be produced in production. #2284 spawns under the flag and
				// installs the answerer in the same slice, which is what keeps this from
				// becoming a window where claude waits forever on an answer nobody is
				// composing.
				//
				// The handler runs on THIS goroutine, in stream order, and holds untrusted
				// claude-authored data; SetCanUseToolHandler states both contracts and
				// CanUseToolRequest states what untrusted means per field.
				if p.canUseTool != nil {
					p.canUseTool(req)
				}
				return
			}
		}
		// Written out rather than reached by `fallthrough`, for the two arms above's
		// reason: default is not the next case in source order, and a fallthrough would
		// also run the ignoredLineTypes test below — a test this type must never pass.
		p.emitUnrecognized(turnevent.UnrecognizedLineType, sl.Type, line)
	case "tool_progress":
		// claude's in-flight progress frames (#2089). A long-running Bash call
		// emits one every few seconds, and without this arm every one of them put
		// an unrecognized_message row in the operator's chat — the same
		// spend-the-alarm-on-a-routine-event failure the control_response arm above
		// describes, except this one fires on a timer for the length of every slow
		// command rather than once per interrupt.
		//
		// Its own arm rather than an ignoredLineTypes member, for #1404's and
		// #1500's reasons verbatim: that list is documented as top-level types only
		// and as MEASURED, and emitUnrecognized stays unreachable BY MATCHING
		// rather than by list membership — the stronger of the two guarantees.
		//
		// The difference from both precedents, and the reason the arm is a matcher
		// instead of a consume-everything: this type has FOUR varieties and only
		// three carry a positive marker. Putting the type on the list would
		// consume the fourth in silence too, and that is the one variety nobody
		// has measured on this surface. So a marker-less frame deliberately falls
		// through here and still rings the bell.
		//
		// Suppression, not mapping. Hanging an elapsed count on the existing tool
		// row is a daemon change plus a wire change plus two client changes, and
		// is its own ticket; none of these frames carries news this surface
		// renders. The vendor's three-field validation (string tool_name, string
		// tool_use_id, finite elapsed_time_seconds) goes with that ticket, where it
		// has a consumer — under suppression it would validate values no code
		// reads.
		if p.consumeToolProgress(line) {
			return
		}
		// Written out rather than reached by `fallthrough`: default is not the next
		// case in source order, and a fallthrough would also run the
		// ignoredLineTypes test below — a test this type must never pass.
		p.emitUnrecognized(turnevent.UnrecognizedLineType, sl.Type, line)
	case "conversation_reset":
		// claude announcing that it replaced the conversation and mounted a fresh
		// transcript (#2134) — what desktop's Reset session produces, since it sends
		// the literal text `/clear` and claude runs a slash-prefixed message as a
		// command. Without this arm every reset put an unrecognized_message row in the
		// operator's chat: the operator asks for a reset and gets a noise row for it.
		//
		// Its own arm rather than an ignoredLineTypes member, for #1404's, #1500's and
		// #2089's reasons verbatim — that list is documented as top-level types only
		// and as MEASURED, and the 2026-07-27 census never ran /clear, so it could not
		// have seen this type. There is a second reason here the precedents do not
		// have: membership would ALSO swallow the announcement in silence, and that is
		// the defect being fixed rather than a cost being tolerated.
		//
		// A MATCHER, and unlike its two nearest precedents it does NOT hold
		// "emitUnrecognized unreachable BY MATCHING". A reset whose id the daemon
		// cannot act on falls through here ON PURPOSE and still rings the bell. See
		// emitConversationReset's doc for why that is the weaker guarantee taken
		// deliberately, and why an unconditional consume would look like a tidy-up
		// while restoring the original defect.
		if p.emitConversationReset(line) {
			return
		}
		// Written out rather than reached by `fallthrough`, for the arm above's
		// reason: default is not the next case in source order, and a fallthrough
		// would also run the ignoredLineTypes test below.
		p.emitUnrecognized(turnevent.UnrecognizedLineType, sl.Type, line)
	case "command_lifecycle":
		// On 2026-10-06, claude 2.1.280 emitted started/completed around
		// peer-message turns in two conversations. These observed bookkeeping
		// states produced two Unrecognized cards per message. The other four
		// matched states (queued, cancelled, discarded, refused) are declared
		// states from the report's schema inspection, not observed frames.
		//
		// Keep a separate arm outside ignoredLineTypes so new or malformed states
		// still surface. marshalTurnEnvelope writes no command uuid on daemon
		// user envelopes, so there is no command correlation here. Read and write
		// no parser state: started can arrive while idle, and completed after
		// result must neither close another turn nor reset the next one's state.
		var lifecycle struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(line, &lifecycle); err == nil {
			switch lifecycle.State {
			case "queued", "started", "completed", "cancelled", "discarded", "refused":
				// Only the matched vocabulary is safe to log; identifiers, raw
				// payload and unknown state values never reach this record.
				p.log.Debug("streamsup: dropping command_lifecycle",
					"site", string(turnevent.UnrecognizedLineType),
					"type", "command_lifecycle", "state", lifecycle.State)
				return
			}
		}
		p.emitUnrecognized(turnevent.UnrecognizedLineType, sl.Type, line)
	case "prompt_suggestion":
		// claude's own suggested next prompt (#2829), emitted AFTER the turn's result
		// when prompt suggestions are enabled. Its own arm rather than an
		// ignoredLineTypes member, for #1404's reasons: emitUnrecognized stays
		// unreachable for this type BY MATCHING. Every payload is consumed here,
		// valid or not, and the arm reads and writes no parser state — a suggestion
		// arrives between turns, so it must neither close the turn result already
		// closed nor clear anything the next turn starts from.
		p.emitPromptSuggestion(line)
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
			// silently by that arm.
			//
			// CORRECTED 2026-09-08 (#2227): status has left too — it maps to the
			// turnevent.Compacting edge pair now. compact_boundary is the one
			// measured-and-dropped subtype left standing, and unlike its predecessors in
			// this sentence it is dropped by a DECISION rather than for want of a
			// mapping: it carries compaction's trigger and token counts, which is #2228's
			// payload, and an arm here would emit a duplicate edge with nothing to add.
			// That it costs no unrecognized_message is the property this branch provides
			// — which is why AC 2 of #2227 is structural rather than earned.
			//
			// CORRECTED 2026-09-08 (#2237): compact_boundary has left as well, so the
			// sentence above has no member at all now and the paragraph is a record of a
			// state that lasted one ticket. Its "an arm here would emit a duplicate edge"
			// reading is the part worth not carrying forward: the mapping that landed is
			// NOT an edge — emitCompactionBoundary reads and writes no parser state and
			// produces a conversation-scoped frame of its own, because the boundary line
			// arrives AFTER the falling edge has already fired and cannot ride it. Every
			// measured `system` subtype is now claimed by emitSystemSubtype, and this
			// branch's silent drop is left for never-seen ones — where its property is
			// unchanged and is what keeps a chatty new subtype off the wire.
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
//
// CORRECTED 2026-09-08 (#2227, then #2236 the same day): the rule above briefly had
// one exception and no longer does, and both edits are kept rather than collapsed
// because the second only makes sense against the first. #2227 logged
// compact_result and compact_error from emitCompactingStatus BECAUSE that content
// did not cross the wire, inverting this site's shape — a log was then the only place
// a failed compaction was visible at all. #2236 published both fields on the
// falling edge, so that record became a bounded copy of something already on the
// wire: the same shape as here, and no longer an exception to anything.
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

// emit forwards one event to the sink. A nil sink is a no-op guard — the parser
// runs on the os/exec forwarder goroutine, where a nil-sink panic would be
// disproportionate to a misconfiguration.
func (p *Parser) emit(ev turnevent.Event) {
	if _, status := ev.(turnevent.MCPStatus); status &&
		p.mcpStatusPolicy.installed && !p.mcpStatusPolicy.eligible {
		return
	}

	if p.mcpStatusPolicy.eligible && !p.mcpStatusPolicy.attempted {
		switch ev.(type) {
		case turnevent.ModelList, turnevent.SlashCommandList:
			p.mcpStatusPolicy.attempted = true
			if p.mcpStatusPolicy.request == nil || p.mcpStatusPolicy.request() != nil {
				// Fixed fields only: a writer error may contain bytes supplied by a
				// test double or future transport and is deliberately not admitted.
				p.log.Debug("streamsup: MCP status ask not delivered",
					"event", "mcp_status.request.write_err")
			}
		}
	}

	if p.sink != nil {
		p.sink(ev)
	}
}
