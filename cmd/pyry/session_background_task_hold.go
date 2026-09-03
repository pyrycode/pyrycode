package main

import (
	"slices"
	"sync"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// sessionBackgroundTaskHold is one session's retained turnevent.BackgroundTaskRoster
// plus the sink decorator that fills it (#2077). streamsup's emitBackgroundTaskRoster
// decodes claude's system/background_tasks_changed line into exactly one roster and the
// bridge pushes it to whatever interactive connections exist at that instant; nothing
// kept it, so a client that connects mid-run learned nothing about background tasks
// until claude next CHANGED the roster — which may never happen while the work is
// running. This keeps the newest one for the session's life so #2079 can answer a
// connect with current background-task truth instead of nothing.
//
// IT SITS UPSTREAM OF THE FAN-IN SEND, and that placement is sessionSlashCommandHold's
// argument unchanged: `turnMarkFor`'s default arm answers turnMarkNone for this variant,
// so `sinkFor` refuses it at droppableCap under load, and a retention point below that
// send could lose the roster. Retaining above it means the refusal can still discard the
// EVENT and never the RETENTION.
//
// What differs from that twin is the STAKES, not the mechanism, and it is worth stating
// because it cuts both ways. There, one initialize exchange per child produces exactly
// one event and no later one replaces it, so a refusal loses the value for the child's
// whole life. Here claude re-reports on every roster change, so a refusal costs only the
// window until the next change — but that window is unbounded, and the case it covers is
// precisely the one this ticket exists for: a busy session whose roster has stopped
// changing is both the most likely to saturate the fan-in and the one a mid-run connect
// most needs answered.
//
// IT IS A SIBLING of sessionModelHold and sessionSlashCommandHold rather than a third
// retention inside either, and `newSessionParser` chains all three — that type's doc
// argues the choice, and nothing about a third link is new. The chain's ORDER carries no
// meaning: every link stores unconditionally and forwards unconditionally, and what puts
// each retention upstream of the droppable send is being on the parser's side of the
// channel at all.
//
// Its lifetime is the session's with no registry and no removal hook, for the reason
// sessionModelHold states in full: the hold is a field of the per-session `streamRunner`,
// whose `Session.Runner` is assigned once at construction and never reassigned, and a
// daemon-global session-keyed map would outlive every session it keyed with nothing to
// prune it.
//
// SECURITY: it has no *slog.Logger field and its constructor takes none, so there is no
// path by which a TaskID, TaskType, Description or truncated-field name can reach a log
// record. That is the two siblings' #833 posture with the threat at its sharpest in this
// family: for claude's local_bash task type a Description IS the literal command line,
// and emitBackgroundTaskRoster's own drop path already refuses to log even the entry
// COUNT, on the ground that "just the length" is the leak a content-free rule is most
// often bent for. Enforced by construction rather than by care — do not give this type a
// logger to write a retention diagnostic, which is the one edit that would reopen the
// channel.
type sessionBackgroundTaskHold struct {
	mu     sync.Mutex
	roster turnevent.BackgroundTaskRoster
	have   bool
	// next is the downstream sink every event is forwarded to, unchanged. nil
	// forwards nothing — a test convenience; production always supplies the next
	// link of newSessionParser's chain.
	next func(turnevent.Event)
}

// newSessionBackgroundTaskHold mints a hold that forwards to next.
func newSessionBackgroundTaskHold(next func(turnevent.Event)) *sessionBackgroundTaskHold {
	return &sessionBackgroundTaskHold{next: next}
}

// Sink is the decorator, used as a method value: hold.Sink is a func(turnevent.Event) of
// exactly the shape streamsup.NewParser takes and of exactly the shape the next link of
// the chain takes.
//
// REPLACE, NEVER DIFF. A roster is stored replacing any prior value, and this is the
// first place in the daemon that holds a previous roster and a newer one at the same
// instant — so the constraint is stated rather than assumed. turnevent.
// BackgroundTaskRoster's type doc reserves snapshot-diffing for a consumer on its own
// terms and refuses it as a daemon inference, because a task's disappearance has never
// been observed and the daemon reports no finish it cannot detect. Store the newer
// value; derive nothing from the pair.
//
// Every event of every variant is then forwarded unchanged, this variant included:
// swallowing it here would change what the fan-in and the drain observe, which this
// ticket has no reason to do.
//
// Stored WITHOUT copying: emitBackgroundTaskRoster builds its Tasks slice and each
// entry's TruncatedFields fresh per emit, by append into a nil slice, out of a
// function-local decode target the parser retains no reference to — so the hold takes
// sole ownership of what it is handed. The COPY is made on the read side instead.
//
// The mutex is a LEAF lock and is never held across the call to next. Holding it across
// a channel send would put a new edge into the daemon's lock order for no benefit; as
// written it participates in no ordering with Pool.mu, Session.lcMu or capMu. The lock
// is needed rather than defensive, and the pair it is needed for is ONE WRITER AND MANY
// READERS — not two writers. Writes are serial across every respawn, because
// spawnAndWait blocks on cmd.Wait, which os/exec documents as joining the goroutine
// copying the child's stdout into a non-*os.File Stdout, so forwarder N+1 cannot start
// until forwarder N has finished; streamsup.Parser's own doc asserts that serialisation.
// What the lock protects against is #2079's resolver reading on a relay-leg goroutine
// while that one writer runs.
func (h *sessionBackgroundTaskHold) Sink(ev turnevent.Event) {
	if roster, ok := ev.(turnevent.BackgroundTaskRoster); ok {
		h.mu.Lock()
		h.roster = roster
		h.have = true
		h.mu.Unlock()
	}
	if h.next != nil {
		h.next(ev)
	}
}

// BackgroundTaskRoster returns the retained roster and true, or the zero value and false
// when nothing has been reported for this session.
//
// THE BOOL IS LOAD-BEARING HERE, where on both siblings it is merely the only spelling
// of a state nothing else can express. emitBackgroundTaskRoster emits an event for an
// absent or empty tasks array ON PURPOSE — an empty roster positively says nothing is
// alive — so a reported roster with no entries is a value a reader really can be handed,
// and `len(Tasks) == 0` does NOT mean unreported. Both siblings make that shape
// unreachable instead, by different routes: turnevent.ModelList.Models is documented
// "never empty", and emitSlashCommandList returns early on a zero-length list (#1877).
// A reader that reconciles "claude reported nothing alive" the way it reconciles "this
// session has reported no roster at all" is wrong, and this bool is the only thing that
// tells the two apart.
//
// DroppedTasks rides the returned value because it is this variant's ONLY truncation
// report — there is no top-level TruncatedFields naming "tasks", deliberately, since a
// name-only report would lose how many entries were lost. A read that dropped the count
// would let a capped roster be read as a whole one.
//
// The returned roster is a DEEP copy, so a reader may mutate it freely without
// corrupting the retained value or another reader's copy. It is worth the clone because
// this value lives for the session's whole life and will be read repeatedly by different
// consumers on different goroutines.
//
// Its Descriptions are claude-authored untrusted text and, for the local_bash task type,
// are literal command lines: safe to RENDER as text, never to execute or re-shell. The
// warning rides turnevent.BackgroundTask's own field doc, and is repeated here for the
// reason that doc gives for repeating it at the roster level — a LIST of command lines
// is a more tempting shape to feed somewhere structured than a single one, and this
// method is a new site that hands one out.
//
// Nil-receiver-safe, mirroring both siblings' precedent: a runner whose hold was never
// minted answers the unreported state rather than panicking.
func (h *sessionBackgroundTaskHold) BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool) {
	if h == nil {
		return turnevent.BackgroundTaskRoster{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.have {
		return turnevent.BackgroundTaskRoster{}, false
	}
	return cloneBackgroundTaskRoster(h.roster), true
}

// cloneBackgroundTaskRoster deep-copies a BackgroundTaskRoster. It is TWO levels: the
// Tasks slice, and within each entry its TruncatedFields. A clone that stops at Tasks
// leaves two readers sharing backing arrays. DroppedTasks is an int and copies by
// assignment, and this variant has no top-level TruncatedFields — the count dimension
// reports per event and the text dimension per entry.
//
// cloneSlashCommandList is the shape to follow and NOT the depth: turnevent.
// BackgroundTask has exactly one []string field where turnevent.SlashCommand has two, so
// the twin's third level has nothing here to clone.
//
// slices.Clone returns nil for a nil input, which preserves turnevent's convention that
// these fields are nil when there is nothing to report and never an empty non-nil slice
// — BackgroundTask.TruncatedFields being that convention's single source, and
// BackgroundTaskRoster.Tasks stating it for the empty roster the producer deliberately
// still emits.
func cloneBackgroundTaskRoster(roster turnevent.BackgroundTaskRoster) turnevent.BackgroundTaskRoster {
	out := roster
	out.Tasks = slices.Clone(roster.Tasks)
	for i := range out.Tasks {
		out.Tasks[i].TruncatedFields = slices.Clone(out.Tasks[i].TruncatedFields)
	}
	return out
}
