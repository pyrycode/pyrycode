package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// backgroundTaskRosterPlan is the per-pool-session answer table
// backgroundTaskRosterRunner reads at CALL time rather than at construction. The
// indirection is load-bearing for the bootstrap session: sessions.New invokes the
// runner factory while building it, so the test cannot know the bootstrap id until
// New has returned and the runner already exists. Arming by id afterwards is what
// lets the refusal cases below give the bootstrap a distinguishable roster.
//
// The mutex is not decorative: the isolation tests run Pool.Run, whose lifecycle
// goroutines hold the same runner values the test goroutine arms through.
//
// It is slashCommandListPlan's shape reproduced rather than shared, this family's
// convention for keeping each twin's rig byte-stable — with ONE divergence that is
// the whole point of this variant. There, a map entry's presence and a non-empty
// inventory are the same thing, because nothing empty ever reaches the retention.
// Here an EMPTY roster is a value a session really can report, and it must be
// armable distinguishably from having reported nothing at all: presence in the map
// is "reported", the value's Tasks is what was reported. That is the
// sessionBackgroundTaskHold bool made testable.
type backgroundTaskRosterPlan struct {
	mu   sync.Mutex
	byID map[sessions.SessionID]turnevent.BackgroundTaskRoster
}

func newBackgroundTaskRosterPlan() *backgroundTaskRosterPlan {
	return &backgroundTaskRosterPlan{byID: map[sessions.SessionID]turnevent.BackgroundTaskRoster{}}
}

// arm makes id's runner report roster. A session with NO entry reports the
// unreported state instead — the plan's zero state is "this child has never
// reported a roster", which is NOT the same as arming an empty one.
func (p *backgroundTaskRosterPlan) arm(id sessions.SessionID, roster turnevent.BackgroundTaskRoster) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byID[id] = roster
}

func (p *backgroundTaskRosterPlan) get(id sessions.SessionID) (turnevent.BackgroundTaskRoster, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	roster, ok := p.byID[id]
	return roster, ok
}

// backgroundTaskRosterRunner is stubRunner plus the ONE concrete method
// resolveBoundBackgroundTaskRoster asserts for. stubRunner itself deliberately does
// not implement it and therefore stays the ready-made not-implemented fixture — see
// the runner-lacks-the-method test below, which builds its pool with
// newRouterTestPool for exactly that reason.
type backgroundTaskRosterRunner struct {
	stubRunner
	id   sessions.SessionID
	plan *backgroundTaskRosterPlan
}

func (r backgroundTaskRosterRunner) BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool) {
	return r.plan.get(r.id)
}

// newBackgroundTaskRosterTestPool builds a real *sessions.Pool whose every session's
// runner answers from the returned plan. RegistryPath points into a fresh temp dir so
// Pool.Create has a resolvable data dir for the per-session settings file; a cold
// start there mints a bootstrap without spawning claude, exactly as newRouterTestPool
// relies on.
func newBackgroundTaskRosterTestPool(t *testing.T) (*sessions.Pool, *backgroundTaskRosterPlan) {
	t.Helper()
	plan := newBackgroundTaskRosterPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return backgroundTaskRosterRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, plan
}

// sentinelBackgroundTaskRoster builds a two-entry roster in which every string
// carries tag and the two entries differ in every field, so a transposed entry, a
// crossed field or a sibling session's roster is visible rather than plausible.
//
// The values are CONSPICUOUS SENTINELS rather than realistic task ids or command
// lines, for sentinelSlashCommandList's measured reason: the log negative below is a
// strings.Contains over a whole captured log, and a natural value like "npm test"
// would be a substring of unrelated text, making the negative RED against a correct
// implementation. TaskType takes a sentinel too — turnevent.BackgroundTask documents
// it as "a plain string rather than a closed enum", and nothing on this path compares
// it against claude's "local_bash", so no fixture has to spell a real kind.
//
// Entry 2 is the deliberately awkward one — nil TruncatedFields — so a mapping that
// allocates an empty slice is visible. turnevent's convention is that the field is nil
// when nothing was cut and never an empty non-nil slice, and protocol.BackgroundTask
// exempts it from the nil→[] normalisation its parent payload applies to Tasks, so a
// nil there has to survive this whole path.
//
// A non-zero DroppedTasks reddens a count re-derived from len(Tasks). Both entries sit
// far inside the producer's caps, so nothing is cut on the way through and the count
// arrives as the armed value.
func sentinelBackgroundTaskRoster(tag string) turnevent.BackgroundTaskRoster {
	return turnevent.BackgroundTaskRoster{
		Tasks: []turnevent.BackgroundTask{
			{
				TaskID:          "ZZTASKONE" + tag + "ZZ",
				TaskType:        "ZZKINDONE" + tag + "ZZ",
				Description:     "ZZDESCONE" + tag + "ZZ",
				TruncatedFields: []string{"ZZTRUNCONE" + tag + "ZZ"},
			},
			{
				TaskID:          "ZZTASKTWO" + tag + "ZZ",
				TaskType:        "ZZKINDTWO" + tag + "ZZ",
				Description:     "ZZDESCTWO" + tag + "ZZ",
				TruncatedFields: nil,
			},
		},
		DroppedTasks: 3,
	}
}

// assertRosterCarries compares a resolved payload against the retained event it was
// mapped from, field by field across all four BackgroundTask fields and in order.
// Comparing against the SOURCE rather than a hand-spelled copy keeps this file from
// re-tabling internal/turnbridge's mapping (which owns and pins it) while still
// catching a transposed entry or a crossed field, because sentinelBackgroundTaskRoster
// makes every value distinct.
//
// TruncatedFields is compared with reflect.DeepEqual rather than by length or with
// slices.Equal, because nil and empty must NOT compare equal here: slices.Equal(nil,
// []string{}) reports true, and this path's obligation for that field is that a nil
// crosses as a nil. Only Tasks is normalised by
// protocol.BackgroundTaskRosterPayload.MarshalJSON; TruncatedFields is deliberately
// exempt, and an emitted [] would tell a client that claude's cut text is complete.
func assertRosterCarries(t *testing.T, got protocol.BackgroundTaskRosterPayload, want turnevent.BackgroundTaskRoster) {
	t.Helper()
	if len(got.Tasks) != len(want.Tasks) {
		t.Fatalf("payload carries %d tasks, want %d: %+v", len(got.Tasks), len(want.Tasks), got.Tasks)
	}
	for i := range want.Tasks {
		w, g := want.Tasks[i], got.Tasks[i]
		if g.TaskID != w.TaskID {
			t.Errorf("Tasks[%d].TaskID = %q, want %q", i, g.TaskID, w.TaskID)
		}
		if g.TaskType != w.TaskType {
			t.Errorf("Tasks[%d].TaskType = %q, want %q", i, g.TaskType, w.TaskType)
		}
		if g.Description != w.Description {
			t.Errorf("Tasks[%d].Description = %q, want %q", i, g.Description, w.Description)
		}
		if !reflect.DeepEqual(g.TruncatedFields, w.TruncatedFields) {
			t.Errorf("Tasks[%d].TruncatedFields = %#v, want %#v", i, g.TruncatedFields, w.TruncatedFields)
		}
	}
}

// AC 1 + AC 3: a conversation bound to a session whose runner holds a reported roster
// resolves to a marshal-ready payload carrying THAT conversation's id, THAT session's
// tasks in claude's order, and that roster's dropped-task count.
//
// DroppedTasks and ConversationID are asserted against literals rather than against
// the fixture's own fields, so the two values a caller could plausibly re-derive (a
// count from len(Tasks), an id reflected from the convID parameter) are pinned to what
// was actually configured. Shipping 0 would tell a client that a capped roster is the
// whole roster, and DroppedTasks is this variant's ONLY truncation report — there is
// no top-level truncated_fields to fall back on.
func TestResolveBoundBackgroundTaskRoster_ResolvesTheBoundSessionsRoster(t *testing.T) {
	t.Parallel()

	pool, plan := newBackgroundTaskRosterTestPool(t)
	held := sentinelBackgroundTaskRoster("BOUND")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-bound",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundBackgroundTaskRoster(reg, pool, "conv-bound")
	if !ok {
		t.Fatalf("resolveBoundBackgroundTaskRoster(conv-bound) refused; want the bound session's roster")
	}
	if got.ConversationID != "conv-bound" {
		t.Errorf("ConversationID = %q, want %q", got.ConversationID, "conv-bound")
	}
	if got.DroppedTasks != 3 {
		t.Errorf("DroppedTasks = %d, want 3 (carried from the retained roster, never recomputed from len(Tasks))", got.DroppedTasks)
	}
	assertRosterCarries(t, got, held)
}

// AC 2 + AC 5: a conversation id that is unknown, unbound or empty answers "no roster"
// and hands back the ZERO payload — never the bootstrap session's roster and never
// another session's. The dangling-binding row rides along: it is the same refusal one
// step further down the chain.
//
// The bootstrap session is armed with a distinguishable roster in every row, which is
// what makes the #678 isolation guard's mutant SOLE-RED rather than invisible: delete
// the `conv.CurrentSessionID == ""` clause from resolveBoundBackgroundTaskRoster and
// Pool.Lookup("") hands back the bootstrap session, so the unbound and empty-id rows
// flip to ok == true carrying ZZ...BOOTSTRAPZZ tasks. An unarmed bootstrap would flip
// them to ok == false and pin nothing.
func TestResolveBoundBackgroundTaskRoster_RefusesWithoutARoster(t *testing.T) {
	t.Parallel()

	pool, plan := newBackgroundTaskRosterTestPool(t)
	plan.arm(pool.BootstrapID(), sentinelBackgroundTaskRoster("BOOTSTRAP"))

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	tests := []struct {
		name   string
		convID string
		why    string
	}{
		{"unknown conversation", "conv-does-not-exist", "the registry has no such record"},
		{"unbound conversation", "conv-unbound", "CurrentSessionID is empty; Pool.Lookup(\"\") would return the BOOTSTRAP session"},
		{"empty conversation id", "", "no conversation carries an empty id, so the registry misses one step earlier"},
		{"binding names a session the pool lacks", "conv-dangling", "Pool.Lookup reports ErrSessionNotFound"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := resolveBoundBackgroundTaskRoster(reg, pool, tc.convID)
			if ok {
				t.Fatalf("resolveBoundBackgroundTaskRoster(%q) = (%+v, true), want a refusal — %s", tc.convID, got, tc.why)
			}
			if !reflect.DeepEqual(got, protocol.BackgroundTaskRosterPayload{}) {
				t.Errorf("refusal returned %+v, want the zero payload", got)
			}
			if strings.Contains(got.ConversationID, "BOOTSTRAP") || len(got.Tasks) > 0 {
				t.Errorf("refusal leaked a payload: %+v", got)
			}
		})
	}
}

// AC 2: a session whose runner implements BackgroundTaskRoster but has reported NOTHING
// answers "no roster" through the comma-ok.
//
// This is the arm the bool exists for, and it is the one place this variant's contract
// diverges hardest from both twins: `ok == false` means no child has ever reported,
// while `ok == true` with no entries means claude reported that nothing is alive (the
// test below). A resolver that collapsed the two would reconcile a live session as a
// silent one. It is also the sole red for a `roster, _ := reader.BackgroundTaskRoster()`
// simplification.
func TestResolveBoundBackgroundTaskRoster_UnreportedSessionAnswersNoRoster(t *testing.T) {
	t.Parallel()

	pool, _ := newBackgroundTaskRosterTestPool(t)
	// Nothing armed: the bootstrap's runner implements BackgroundTaskRoster and
	// reports the unreported state.
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-silent",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundBackgroundTaskRoster(reg, pool, "conv-silent")
	if ok {
		t.Fatalf("resolveBoundBackgroundTaskRoster = (%+v, true); a session that has reported nothing must answer no roster", got)
	}
	if !reflect.DeepEqual(got, protocol.BackgroundTaskRosterPayload{}) {
		t.Errorf("refusal returned %+v, want the zero payload", got)
	}
}

// AC 2: a runner that does not implement BackgroundTaskRoster is a REFUSAL, not a
// panic. newRouterTestPool's plain stubRunner is the fixture — it satisfies
// sessions.Runner and nothing more, which is exactly the shape a non-stream-json runner
// has.
func TestResolveBoundBackgroundTaskRoster_RunnerWithoutTheMethodRefuses(t *testing.T) {
	t.Parallel()

	pool := newRouterTestPool(t)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-plain",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundBackgroundTaskRoster(reg, pool, "conv-plain")
	if ok {
		t.Fatalf("resolveBoundBackgroundTaskRoster = (%+v, true); a runner without BackgroundTaskRoster must refuse", got)
	}
	if !reflect.DeepEqual(got, protocol.BackgroundTaskRosterPayload{}) {
		t.Errorf("refusal returned %+v, want the zero payload", got)
	}
}

// AC 2, the divergence this ticket exists to get right: a session that reported an
// EMPTY roster contributes a payload carrying an empty task list. Silence is reserved
// for unbound / gone / never-reported; "claude says nothing is alive" is a POSITIVE
// statement and the #1240 signal a client needs.
//
// Both twins say the opposite and their reasons are false here — turnevent.ModelList.
// Models is documented "never empty" and emitSlashCommandList returns early on a
// zero-length list (#1877), while emitBackgroundTaskRoster emits the empty roster ON
// PURPOSE. Reaching for either neighbour's answer produces the wrong one.
//
// Three assertions, each pinning a different way this can go wrong:
//
//   - ok == true with len(Tasks) == 0 is the contract itself; a `len(Tasks) == 0`
//     refusal added anywhere in the resolver reddens here and nowhere else.
//   - Tasks == nil is the sole red for a resolver that forks turnbridge.MapEvent to
//     pre-allocate the slice. That would produce identical wire bytes while hiding the
//     normalisation protocol.BackgroundTaskRosterPayload owns, which is exactly what
//     the mapping's own arm declines to do.
//   - the marshalled bytes carry "tasks":[] and not "tasks":null, which is what makes
//     this an end-to-end pin rather than a statement about a Go value that could
//     serialise either way. MarshalJSON has a VALUE receiver and this path returns a
//     value, so the normalisation fires; a pointer-receiver refactor would silently
//     ship null and both forms decode back to len 0.
func TestResolveBoundBackgroundTaskRoster_EmptyRosterResolvesToAnEmptyTaskList(t *testing.T) {
	t.Parallel()

	pool, plan := newBackgroundTaskRosterTestPool(t)
	// Reported, and reporting nothing alive: Tasks nil is turnevent's spelling of an
	// empty roster (never an empty non-nil slice), and DroppedTasks stays 0 because an
	// empty roster had nothing to cut.
	plan.arm(pool.BootstrapID(), turnevent.BackgroundTaskRoster{})

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-idle",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundBackgroundTaskRoster(reg, pool, "conv-idle")
	if !ok {
		t.Fatalf("resolveBoundBackgroundTaskRoster refused a session that reported an EMPTY roster; " +
			"an empty roster positively says nothing is alive and must contribute a payload")
	}
	if got.ConversationID != "conv-idle" {
		t.Errorf("ConversationID = %q, want %q", got.ConversationID, "conv-idle")
	}
	if len(got.Tasks) != 0 {
		t.Fatalf("Tasks = %+v, want none", got.Tasks)
	}
	if got.Tasks != nil {
		t.Errorf("Tasks = %#v, want a nil slice — the mapping forwards turnevent's nil unforked "+
			"and the payload type owns the nil->[] normalisation; pre-allocating here hides it", got.Tasks)
	}
	if got.DroppedTasks != 0 {
		t.Errorf("DroppedTasks = %d, want 0", got.DroppedTasks)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(payload): %v", err)
	}
	if !strings.Contains(string(raw), `"tasks":[]`) {
		t.Errorf("empty roster serialised as %s, want a payload carrying \"tasks\":[]", raw)
	}
	if strings.Contains(string(raw), `"tasks":null`) {
		t.Errorf("empty roster serialised as %s; null reads as absent/unknown where [] reads as "+
			"\"nothing is alive\", which is the whole signal", raw)
	}
}

// indexRostersByConversation keys an enumeration's result on ConversationID, which is
// how every assertion below reads it: retainedBackgroundTaskRosters returns List's
// order (registry insertion order) and that is deliberately NOT a contract — the seam's
// own doc block binds callers to correlate by conversation id, and
// reconcileBackgroundTaskRosters stamps one fixed, non-load-bearing envelope id across
// the batch.
//
// It also fails a duplicated id, which is the shape a loop that appended the same row
// twice would take. That check assumes the registry holds no duplicate ids, which is
// the REGISTRY's documented caller obligation and not something this path enforces:
// conversations.Registry.Create validates neither uniqueness nor non-emptiness, and
// Registry.Get returns the first match, so duplicate rows would each resolve the first
// one's roster under the shared id. Production mints every id through
// conversations.NewID, so the assumption holds where it matters.
func indexRostersByConversation(t *testing.T, got []protocol.BackgroundTaskRosterPayload) map[string]protocol.BackgroundTaskRosterPayload {
	t.Helper()
	byID := make(map[string]protocol.BackgroundTaskRosterPayload, len(got))
	for _, p := range got {
		if _, dup := byID[p.ConversationID]; dup {
			t.Fatalf("conversation %q contributed twice: %+v", p.ConversationID, got)
		}
		byID[p.ConversationID] = p
	}
	return byID
}

// AC 1 + AC 3: one conversation bound to a session holding a retained roster yields
// exactly one payload, carrying that session's tasks in claude's order under that
// conversation's id, with that roster's dropped-task count.
//
// The bootstrap session is armed and bound, so a mutant that returned the zero payload
// or an empty slice is red on the count as well as on the contents.
func TestRetainedBackgroundTaskRosters_EnumeratesTheBoundSessionsRoster(t *testing.T) {
	t.Parallel()

	pool, plan := newBackgroundTaskRosterTestPool(t)
	held := sentinelBackgroundTaskRoster("ENUM")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-enum",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got := retainedBackgroundTaskRosters(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedBackgroundTaskRosters returned %d payloads, want exactly 1: %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-enum" {
		t.Errorf("ConversationID = %q, want %q", got[0].ConversationID, "conv-enum")
	}
	if got[0].DroppedTasks != 3 {
		t.Errorf("DroppedTasks = %d, want 3 (carried from the retained roster, never recomputed)", got[0].DroppedTasks)
	}
	assertRosterCarries(t, got[0], held)
}

// AC 2 + AC 5: each refusal contributes no payload and raises no error, and does so
// WITHOUT aborting the enumeration or contaminating a sibling's payload — and the
// unbound row is never handed the bootstrap child's roster stamped with its own
// conversation id, even though the bootstrap holds one.
//
// All four rows live in ONE registry on purpose. A refusal that aborted the
// enumeration, appended a zero payload, or carried the previous row's tasks forward is
// red here and invisible in four single-row registries. The three refusing rows are the
// three arms reachable from this entry point; the resolver's unknown-conversation arm
// is unreachable because every id came out of List.
//
// The contributing row is created LAST, and that order is load-bearing: List returns
// registry insertion order, so a mutant that broke out of the loop on the first refusal
// instead of continuing would still return the survivor — and go green — if the
// survivor came first. Behind three refusals it returns nothing.
//
// This is also the sole red for the DOUBLE-LOOKUP fork, which is a security control
// rather than redundancy: reading CurrentSessionID off the listed row and calling
// Pool.Lookup directly forks the #678 empty-binding guard, and Pool.Lookup("") returns
// the BOOTSTRAP session with a nil error — so conv-unbound would contribute a second
// payload carrying the shared bootstrap child's tasks under its own id.
func TestRetainedBackgroundTaskRosters_SkipsRefusalsAndNeverHandsTheBootstrapRoster(t *testing.T) {
	t.Parallel()

	pool, plan := newBackgroundTaskRosterTestPool(t)
	held := sentinelBackgroundTaskRoster("SURVIVOR")
	plan.arm(pool.BootstrapID(), held)

	// A second pool session whose runner implements BackgroundTaskRoster but has
	// nothing armed: the "has reported nothing" refusal, one step deeper than the two
	// below. Bound to its own row so the refusal is reached through the registry.
	ctx := runPoolReady(t, pool)
	silent, err := pool.Create(ctx, "session-silent")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-silent", CurrentSessionID: string(silent), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-live", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})

	got := retainedBackgroundTaskRosters(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedBackgroundTaskRosters returned %d payloads, want exactly 1 (only conv-live contributes): %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-live" {
		t.Fatalf("the surviving payload names %q, want %q", got[0].ConversationID, "conv-live")
	}
	assertRosterCarries(t, got[0], held)
	// Stated separately from the count above, because this is the disclosure the
	// double lookup exists to prevent and a reader should not have to infer it from
	// len(got): no payload may name a conversation that has no bound session, whatever
	// tasks it carries.
	for _, p := range got {
		if p.ConversationID == "conv-unbound" {
			t.Errorf("the unbound conversation contributed %+v; Pool.Lookup(\"\") returns the "+
				"BOOTSTRAP session, so this is that child's roster stamped with another "+
				"conversation's id", p)
		}
	}
}

// AC 2, the divergence at the level where the wrong filter would be written: a row
// whose session reported an EMPTY roster CONTRIBUTES one payload carrying an empty task
// list, while a row whose session has reported nothing at all contributes none.
//
// This is the sole red for a `len(p.Tasks) == 0 { continue }` guard added to the
// enumeration's loop — every other test in this file builds rosters with entries, so
// such a filter is invisible to all of them while deleting the payoff of the whole
// feature. Both twins' enumerations justify having no such filter by arguing the case
// is unreachable; here it is not only reachable but is the case that matters most, so
// the filter's absence has to be pinned by a fixture rather than by an argument.
//
// The two rows sit in ONE registry so the pair is a contrast rather than two facts: the
// same enumeration call must answer them differently, which is the load-bearing bool
// observed from the outside.
func TestRetainedBackgroundTaskRosters_EmptyRosterRowContributes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pool, plan := newBackgroundTaskRosterTestPool(t)
	// Pool.Create schedules the new session on the run group, so the pool has to be
	// running or supervise returns ErrPoolNotRunning.
	ctx := runPoolReady(t, pool)
	silent, err := pool.Create(ctx, "session-silent")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}
	// The bootstrap has REPORTED, and what it reported is that nothing is alive.
	// session-silent is unarmed: it has never reported.
	plan.arm(pool.BootstrapID(), turnevent.BackgroundTaskRoster{})

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-idle", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-never", CurrentSessionID: string(silent), LastUsedAt: now})

	got := retainedBackgroundTaskRosters(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedBackgroundTaskRosters returned %d payloads, want exactly 1 — the session that "+
			"reported an empty roster contributes, the one that reported nothing does not: %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-idle" {
		t.Fatalf("the contributing payload names %q, want %q", got[0].ConversationID, "conv-idle")
	}
	if len(got[0].Tasks) != 0 {
		t.Errorf("Tasks = %+v, want none", got[0].Tasks)
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("json.Marshal(payload): %v", err)
	}
	if !strings.Contains(string(raw), `"tasks":[]`) {
		t.Errorf("the enumerated empty roster serialised as %s, want \"tasks\":[]", raw)
	}
}

// AC 3 + AC 5: two conversations bound to two DIFFERENT pool sessions each carry their
// own session's roster across ONE enumeration, under their own ids. This is the pin a
// shared buffer, an off-by-one, or a loop-variable capture cannot pass, and it is where
// the reported id being per-record rather than global is observable.
//
// The two sessions also pin that two payloads never share a backing array: each
// resolver call takes its own deep copy out of the hold and turnbridge.MapEvent
// allocates a fresh outer slice, so a mutant that hoisted one copy out of the loop
// would cross the two rosters here. That aliasing question is only wrong-able from a
// fan-out caller, so no single-call resolver test can raise it — which is why the
// resolver-level isolation test the twins each shipped is not reproduced separately.
//
// Not parallel: t.Setenv confines anything the pool's create path resolves out of HOME,
// and t.Setenv forbids t.Parallel.
func TestRetainedBackgroundTaskRosters_DoesNotCrossConversations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pool, plan := newBackgroundTaskRosterTestPool(t)
	ctx := runPoolReady(t, pool)
	sessA := pool.BootstrapID()
	sessB, err := pool.Create(ctx, "session-b")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	rosterA, rosterB := sentinelBackgroundTaskRoster("ALPHA"), sentinelBackgroundTaskRoster("BETA")
	plan.arm(sessA, rosterA)
	plan.arm(sessB, rosterB)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-a", CurrentSessionID: string(sessA), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-b", CurrentSessionID: string(sessB), LastUsedAt: now})

	got := retainedBackgroundTaskRosters(reg, pool)()
	if len(got) != 2 {
		t.Fatalf("retainedBackgroundTaskRosters returned %d payloads, want 2: %+v", len(got), got)
	}
	byID := indexRostersByConversation(t, got)
	gotA, ok := byID["conv-a"]
	if !ok {
		t.Fatalf("conv-a contributed no payload: %+v", got)
	}
	gotB, ok := byID["conv-b"]
	if !ok {
		t.Fatalf("conv-b contributed no payload: %+v", got)
	}
	assertRosterCarries(t, gotA, rosterA)
	assertRosterCarries(t, gotB, rosterB)
	// Stated as its own assertion rather than left implicit in the two above: the
	// failure this test exists to catch is A being handed B's roster stamped with A's
	// own id, and a reader should not have to compare two sentinel tags to see it.
	if gotA.Tasks[0].TaskID == gotB.Tasks[0].TaskID {
		t.Fatalf("both conversations enumerated the same roster: %q", gotA.Tasks[0].TaskID)
	}
	// Independent backing arrays: writing through one payload's slice must not reach
	// the other's, nor the retained value.
	gotA.Tasks[0].TaskID = "ZZMUTATEDZZ"
	if gotB.Tasks[0].TaskID == "ZZMUTATEDZZ" {
		t.Error("the two payloads share a Tasks backing array; each resolver call must take its own copy")
	}
}

// AC 3: the enumeration is unfiltered, so an ARCHIVED conversation whose bound session
// still holds a roster DOES contribute. Registry.SetArchived writes exactly one field
// and never unbinds CurrentSessionID, and the reconcile asserts current control truth —
// the client decides what to show.
//
// This is the sole red for a mutant that narrows the call to
// List(ListFilter{IsArchived: &f}), which is otherwise invisible: every other test here
// builds unarchived rows.
func TestRetainedBackgroundTaskRosters_ArchivedConversationsContribute(t *testing.T) {
	t.Parallel()

	pool, plan := newBackgroundTaskRosterTestPool(t)
	held := sentinelBackgroundTaskRoster("ARCHIVED")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-archived",
		CurrentSessionID: string(pool.BootstrapID()),
		IsArchived:       true,
		LastUsedAt:       time.Now().UTC(),
	})

	got := retainedBackgroundTaskRosters(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedBackgroundTaskRosters returned %d payloads, want 1 — archiving does not unbind the session: %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-archived" {
		t.Errorf("ConversationID = %q, want %q", got[0].ConversationID, "conv-archived")
	}
	assertRosterCarries(t, got[0], held)
}

// AC 4: connecting twice with no roster change in between delivers the same payloads
// both times and changes no daemon state.
//
// The idempotence half is a straight re-call: two enumerations over an unchanged
// registry and pool must agree field for field. reflect.DeepEqual is honest here
// because neither result crosses a JSON hop — the nil/[] round-trip asymmetry that
// forces internal/relay's rosterreconcile test to normalise its want side does not
// arise on this side of the wire.
//
// The no-state-change half is pinned three ways, and the last is deliberately negative
// rather than positive: the read must retire nothing (the hold still reports the same
// roster afterwards), open nothing and mint no id. There is no counter to assert a
// mint against, so "mints no id" is pinned as the pool's session set and the registry's
// row set being unchanged across both calls — a minted session or conversation would
// have to appear in one of them. Said plainly rather than overclaimed: this cannot see
// an id minted and immediately discarded, only one that reached daemon state.
func TestRetainedBackgroundTaskRosters_TwoCallsAgreeAndMutateNothing(t *testing.T) {
	t.Parallel()

	pool, plan := newBackgroundTaskRosterTestPool(t)
	held := sentinelBackgroundTaskRoster("IDEMPOTENT")
	plan.arm(pool.BootstrapID(), held)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-again", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})

	enumerate := retainedBackgroundTaskRosters(reg, pool)
	sessionsBefore, rowsBefore := len(pool.List()), len(reg.List())

	first := enumerate()
	second := enumerate()

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two connects delivered different payloads with no roster change between them:\nfirst  %+v\nsecond %+v", first, second)
	}
	if len(first) != 1 {
		t.Fatalf("retainedBackgroundTaskRosters returned %d payloads, want 1; the idempotence pin needs a contributing row", len(first))
	}
	assertRosterCarries(t, first[0], held)

	// Retires nothing: the hold still answers the same roster after two reads.
	third, ok := resolveBoundBackgroundTaskRoster(reg, pool, "conv-again")
	if !ok {
		t.Fatal("the retained roster was consumed by reading it; this path must retire nothing")
	}
	assertRosterCarries(t, third, held)

	// Opens no task and mints no id: anything minted would have to land in the pool's
	// session set or the registry's row set.
	if got := len(pool.List()); got != sessionsBefore {
		t.Errorf("pool holds %d sessions after two enumerations, want %d — the read must mint none", got, sessionsBefore)
	}
	if got := len(reg.List()); got != rowsBefore {
		t.Errorf("registry holds %d conversations after two enumerations, want %d — the read must create none", got, rowsBefore)
	}

	// Independent backing arrays across CALLS, not just across rows: mutating the
	// first result must not reach the second's, nor the retained value.
	first[0].Tasks[0].TaskID = "ZZMUTATEDZZ"
	if second[0].Tasks[0].TaskID == "ZZMUTATEDZZ" {
		t.Error("two enumerations share a Tasks backing array; each call must take its own copy")
	}
	fourth, ok := resolveBoundBackgroundTaskRoster(reg, pool, "conv-again")
	if !ok {
		t.Fatal("resolveBoundBackgroundTaskRoster refused after a caller mutated an earlier payload")
	}
	assertRosterCarries(t, fourth, held)
}

// AC 2, the nothing-to-send half: an empty registry and a registry whose every row
// refuses both enumerate to no payloads and no panic. Split from the skips-refusals
// test because that one always has a survivor, so it cannot distinguish "skipped the
// refusals" from "returned the survivor and stopped".
func TestRetainedBackgroundTaskRosters_NothingToSend(t *testing.T) {
	t.Parallel()

	pool, plan := newBackgroundTaskRosterTestPool(t)
	// Armed but never bound: a mutant that enumerated the POOL instead of the registry
	// would contribute here, and an unarmed bootstrap would hide that.
	plan.arm(pool.BootstrapID(), sentinelBackgroundTaskRoster("UNREACHABLE"))

	now := time.Now().UTC()
	allRefuse := &conversations.Registry{}
	allRefuse.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	allRefuse.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	tests := []struct {
		name string
		reg  *conversations.Registry
	}{
		{"empty registry", &conversations.Registry{}},
		{"every row refuses", allRefuse},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := retainedBackgroundTaskRosters(tc.reg, pool)(); len(got) != 0 {
				t.Fatalf("retainedBackgroundTaskRosters returned %d payloads, want none: %+v", len(got), got)
			}
		})
	}
}

// #833 at BOTH levels in one test: no task id, task type, description or truncated-field
// name reaches a log record on this path, and neither does a conversation id.
//
// One test rather than two, because the enumeration exercises the resolver — the twins
// each proved this at their own level and here the two collapse. The registry
// deliberately holds a contributing row, an EMPTY-roster row and two refusing ones,
// because a "why did this row not contribute" line is exactly the edit that would leak,
// and the empty row is the one a developer is most likely to want to explain.
//
// What this pins, honestly: neither function takes a *slog.Logger, so the STRUCTURAL
// half is enforced by the signatures and by review, not here. What this catches is the
// one real regression shape a signature cannot stop — someone reaching for the
// package-level slog.Info / slog.Default(). The stakes are this family's sharpest: for
// claude's local_bash task type a Description IS the literal command line, and
// emitBackgroundTaskRoster's own drop path refuses to log even the entry COUNT.
//
// It must NOT call t.Parallel: slog.SetDefault is process-global and would race every
// other parallel test in this package. The pool is built BEFORE the default is swapped,
// so sessions.New captures the old default and the pool's own diagnostics cannot land
// in this buffer — the only writer left is the code under test.
func TestRetainedBackgroundTaskRosters_LogsNothing(t *testing.T) {
	pool, plan := newBackgroundTaskRosterTestPool(t)
	held := sentinelBackgroundTaskRoster("LOGNEG")
	plan.arm(pool.BootstrapID(), held)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-logneg", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-ZZUNTRUSTEDZZ", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-gone", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	got := retainedBackgroundTaskRosters(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedBackgroundTaskRosters returned %d payloads, want 1; the log negative needs the happy path", len(got))
	}
	// The resolver's own refusal arm, driven directly with an untrusted id the
	// enumeration can never produce (every id it passes came out of the registry).
	if _, ok := resolveBoundBackgroundTaskRoster(reg, pool, "conv-ZZUNKNOWNZZ"); ok {
		t.Fatal("resolveBoundBackgroundTaskRoster resolved an unknown conversation")
	}

	logs := buf.String()
	if logs != "" {
		t.Fatalf("this path wrote %d bytes of log; it must write none:\n%s", len(logs), logs)
	}
	// Belt-and-braces on the values themselves, so a future handler swap that made the
	// emptiness check weaker still names what leaked. The v != "" guard is not
	// decorative: strings.Contains(logs, "") is true unconditionally, so an empty field
	// would make this loop assert nothing.
	for _, task := range got[0].Tasks {
		for _, v := range append([]string{task.TaskID, task.TaskType, task.Description}, task.TruncatedFields...) {
			if v != "" && strings.Contains(logs, v) {
				t.Errorf("a background-task value leaked into a log record: %q", v)
			}
		}
	}
	// The refusing rows' ids and the untrusted id are the other half: a "why did this
	// row not contribute" line would carry one.
	if strings.Contains(logs, "ZZUNTRUSTEDZZ") {
		t.Errorf("a skipped conversation's id leaked into a log record:\n%s", logs)
	}
	if strings.Contains(logs, "ZZUNKNOWNZZ") {
		t.Errorf("the caller's untrusted conversation id leaked into a log record:\n%s", logs)
	}
}
