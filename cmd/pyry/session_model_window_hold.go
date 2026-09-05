package main

import (
	"slices"
	"sync"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modelWindowReport is one turn's per-model context-window report, as retained for the
// session (#2106): the entries claude's `result` line carried and the count of what the
// producer cut on the way.
//
// THE TWO FIELDS ARE ONE REPORT AND TRAVEL TOGETHER, which is why this is a pair rather
// than two returns a caller could take one of. Their sum is exactly what claude sent, so a
// read that carried only the entries would let a capped report be read as a whole one —
// turnevent.BackgroundTaskRoster's DroppedTasks rule, which holds here for the same reason
// and one step more strongly: turnevent.ModelWindow has no per-entry TruncatedFields, so
// this count is the variant's ONLY truncation report.
type modelWindowReport struct {
	// Windows is what turnevent.TurnEnd.ModelWindows carried: one entry per model id
	// claude used, sorted by ModelID, every entry with a WindowTokens above zero. The
	// producer's guarantees ride along unchanged — this type re-decides nothing.
	//
	// SECURITY: each ModelID is claude-authored text, bounded at 256 bytes by streamsup's
	// maxModelWindowID and deliberately NOT sanitized — no control-character or
	// terminal-escape stripping happens anywhere on this path. See ModelWindows' doc for
	// the two obligations that ride a value handed out of the retention.
	Windows []turnevent.ModelWindow
	// Dropped is how many entries claude sent that Windows does NOT carry; 0 when
	// nothing was dropped. Daemon-derived — an int computed from map and slice lengths,
	// carrying none of claude's bytes.
	Dropped int
}

// sessionModelWindowHold is one session's retained per-model context-window report plus the
// sink decorator that fills it (#2106). streamsup's decodeModelWindows reads claude's
// modelUsage map off each `result` line into a turnevent.TurnEnd and the bridge pushes that
// event to whatever interactive connections exist at that instant; nothing kept the
// windows, so they existed for a microsecond and were gone. This keeps the newest usable
// report for the session's life so #2107 can answer a consumer reading on a relay-leg
// goroutine with no turn in flight, instead of only during one.
//
// IT IS THE FOURTH APPLICATION of sessionModelHold's shape, after sessionSlashCommandHold
// (#2004) and sessionBackgroundTaskHold (#2077), and `newSessionParser` chains all four —
// sessionModelHold's doc argues sibling-versus-nested-retention and nothing about a fourth
// link is new. The chain's ORDER carries no meaning: every link stores unconditionally with
// respect to variant and forwards unconditionally, and what puts each retention upstream of
// the fan-in send is being on the parser's side of the channel at all.
//
// # Why it sits upstream of the fan-in send, which is NOT the siblings' reason
//
// All three siblings open with a variant of "`turnMarkFor`'s default arm answers
// turnMarkNone for this variant, so `sinkFor` refuses it at droppableCap under load". THAT
// SENTENCE IS FALSE HERE and must not be copied forward. `turnMarkFor` answers
// turnMarkClose for turnevent.TurnEnd, so this variant rides the streamTurnSinkCloseReserve
// band the droppable class may never take, and it is never refused at droppableCap.
//
// The retention still belongs above the send, for a weaker but real reason: the reserve is
// a reserve and not a guarantee. Past it a closer can still be lost — `sinkFor`'s closer arm
// has a drop path, and logs that loss at Warn precisely because it is a residual rather than
// an impossibility. Retaining above the send means such a refusal can discard the EVENT and
// never the RETENTION. The window a loss would cost is bounded here in a way it is not for
// sessionModelHold, whose one initialize reply per child is never re-sent: claude reports
// windows at every turn end, so a lost closer costs only the gap to the next turn.
//
// Its lifetime is the session's with no registry and no removal hook, for the reason
// sessionModelHold states in full: the hold is a field of the per-session `streamRunner`,
// whose `Session.Runner` is assigned once at construction and never reassigned, and a
// daemon-global session-keyed map would outlive every session it keyed with nothing to
// prune it.
//
// SECURITY: it has no *slog.Logger field and its constructor takes none, so there is no path
// by which a model id or a window value can reach a log record at any level — the #833
// posture internal/relay's v2session_settings.go and internal/sessions' pool.go restate,
// enforced by construction rather than by care. The producer holds the same line from the
// other side: decodeModelWindows discards its decode error unlogged, because encoding/json
// quotes the offending input bytes into its error text. Do not give this type a logger to
// write a retention diagnostic, which is the one edit that would reopen the channel.
type sessionModelWindowHold struct {
	mu     sync.Mutex
	report modelWindowReport
	have   bool
	// next is the downstream sink every event is forwarded to, unchanged. nil forwards
	// nothing — a test convenience; production always supplies the next link of
	// newSessionParser's chain.
	next func(turnevent.Event)
}

// newSessionModelWindowHold mints a hold that forwards to next.
func newSessionModelWindowHold(next func(turnevent.Event)) *sessionModelWindowHold {
	return &sessionModelWindowHold{next: next}
}

// Sink is the decorator, used as a method value: hold.Sink is a func(turnevent.Event) of
// exactly the shape streamsup.NewParser takes and of exactly the shape the next link of the
// chain takes.
//
// # A turn that reports nothing usable is a NO-OP, not an erasure
//
// This is the one behavioural departure from all three siblings, which replace
// unconditionally on their variant. #2101 collapses five "claude said nothing usable"
// shapes — an absent key, a JSON null, a published empty object, a modelUsage that does not
// decode, and a map whose every entry was dropped — into one reading spelled nil. So a
// `result` line without a usable modelUsage is claude saying NOTHING ABOUT WINDOWS, not
// claude saying the window changed. Erasing on silence would make a consumer's reading
// flicker between the true window and a fallback across turns.
//
// sessionBackgroundTaskHold's unconditional replace is correct THERE and would be wrong
// here, and the difference is in the producer rather than in taste: an empty roster is a
// positive statement claude makes on purpose, and an absent window map is not one.
//
// The predicate is len > 0 rather than != nil so it is robust to an empty non-nil slice,
// which the producer does not currently emit and nothing here should depend on it not
// emitting. The Dropped count alone does not make a report usable: it names no model and no
// window, so there is nothing in it for a consumer to join on.
//
// # Replace whole; do not merge by id
//
// A later usable report supersedes the earlier one entirely. Union-by-id would retain a
// window for a model the current turn did not touch and would need a cardinality bound of
// its own; no case has been observed where a turn omits a model whose window is still
// wanted, and a miss already has a safe answer downstream. The two fields are stored
// together for the reason modelWindowReport gives — updating one without the other would
// report a cut that belongs to a different turn's entries.
//
// # Stored through a CLONE, where every sibling stores what it is handed
//
// The siblings' justification — the producer allocates fresh per emit and the parser
// retains no reference, so the hold takes sole ownership — is true here too, and the
// conclusion still does not follow. decodeModelWindows pre-allocates its result with a
// capacity taken from len(modelUsage), CLAUDE'S MAP SIZE rather than maxModelWindowEntries,
// and clones only on the over-cap path. A modelUsage padded with unusable entries therefore
// yields a short slice over a long backing array, bounded only by one parse line under
// streamsup's defaultMaxParseBuf. Storing that as-handed would pin it for the session's
// life; slices.Clone allocates exactly len entries and drops the pad. It is unreachable on
// the silent path, which the predicate above has already excluded, and it preserves
// turnevent's nil-for-nil convention regardless.
//
// Every event of every variant is then forwarded unchanged, this one included: swallowing
// it here would change what the fan-in and the drain observe, which this ticket has no
// reason to do. Only ModelWindows and DroppedModelWindows are retained — Reason is
// turn-specific and belongs to the event, not to the session.
//
// The mutex is a LEAF lock and is never held across the call to next. Holding it across a
// channel send would put a new edge into the daemon's lock order for no benefit; as written
// it participates in no ordering with Pool.mu, Session.lcMu or capMu. The clone is computed
// outside the critical section, leaving one assignment under the lock. The lock is needed
// rather than defensive, and the pair it is needed for is ONE WRITER AND MANY READERS — not
// two writers. Writes are serial across every respawn, because spawnAndWait blocks on
// cmd.Wait, which os/exec documents as joining the goroutine copying the child's stdout into
// a non-*os.File Stdout, so forwarder N+1 cannot start until forwarder N has finished;
// streamsup.Parser's own doc asserts that serialisation. What the lock protects against is
// #2107's reader on a relay-leg goroutine running while that one writer does.
func (h *sessionModelWindowHold) Sink(ev turnevent.Event) {
	if end, ok := ev.(turnevent.TurnEnd); ok && len(end.ModelWindows) > 0 {
		report := modelWindowReport{
			Windows: slices.Clone(end.ModelWindows),
			Dropped: end.DroppedModelWindows,
		}
		h.mu.Lock()
		h.report = report
		h.have = true
		h.mu.Unlock()
	}
	if h.next != nil {
		h.next(ev)
	}
}

// ModelWindows returns the retained report and true, or the zero value and false when this
// session's child has never reported a usable window.
//
// THE BOOL IS THE ONLY SPELLING of the unreported state, and here that is not a choice
// between two spellings the way it is on sessionBackgroundTaskHold, where an empty roster is
// a value a reader really can be handed. There is no reported-but-empty answer to
// distinguish: #2101 drops every unusable entry and reports nil for a map whose entries were
// all dropped, and Sink stores nothing for that shape, so a retained report always carries at
// least one entry.
//
// The returned report is a COPY, so a reader may mutate it freely without corrupting the
// retained value or another reader's copy. It is ONE LEVEL, where cloneBackgroundTaskRoster
// needs two: turnevent.ModelWindow has no slice fields, so the entries slice is the whole
// depth and Dropped rides the struct assignment. Follow that helper's shape, not its depth.
//
// # Two obligations ride the value this hands out
//
// SANITIZATION IS THE CONSUMER'S. Each ModelID is claude-authored, model-influenced text:
// the daemon bounds it at streamsup's maxModelWindowID and does not sanitize it, so it may
// carry control characters or terminal escapes. turnevent.ModelWindow.ModelID's own doc
// places the render obligation on the client and names #2102 as inheriting it; that
// obligation is restated at this level for the reason sessionBackgroundTaskHold restates
// turnevent.BackgroundTask.Description's — this method is a new site that hands the text
// out, to a caller on an arbitrary goroutine, for the session's whole life.
//
// AN ID IS NOT AN ARGV TOKEN. ModelID is claude's map key verbatim: no allowlist check, no
// lookup against any published model list, and no rejection of a leading dash. Joining one
// back onto a spawn's arguments as --model <id> — the shape withApprovalArgs and
// internal/sessions' claudeSettingsArgs compose — would be argument injection into the
// daemon's own child. This value is for sizing a gauge and for joining on ids a consumer
// already holds; it is not a source of spawn arguments.
//
// Nil-receiver-safe, mirroring all three siblings' precedent: a runner whose hold was never
// minted answers the unreported state rather than panicking.
func (h *sessionModelWindowHold) ModelWindows() (modelWindowReport, bool) {
	if h == nil {
		return modelWindowReport{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.have {
		return modelWindowReport{}, false
	}
	return cloneModelWindowReport(h.report), true
}

// cloneModelWindowReport copies a modelWindowReport. ONE level: the Windows slice.
// turnevent.ModelWindow is a string and an int with no slice field, so there is no second
// level to reach — cloneBackgroundTaskRoster is the shape to follow and not the depth, as it
// says of its own twin. Dropped is an int and copies by assignment.
//
// slices.Clone returns nil for a nil input, which preserves turnevent's convention that
// these fields are nil when there is nothing to report and never an empty non-nil slice
// (turnevent.BackgroundTask.TruncatedFields is that convention's single source). A retained
// report never holds a nil Windows — Sink stores only a usable report — so the nil case is
// reached from the zero value alone, and preserving it there is what keeps an unreported
// read from inventing an empty slice.
func cloneModelWindowReport(report modelWindowReport) modelWindowReport {
	out := report
	out.Windows = slices.Clone(report.Windows)
	return out
}
