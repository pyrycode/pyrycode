package main

import (
	"bytes"
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

// slashCommandListPlan is the per-pool-session answer table slashCommandListRunner
// reads at CALL time rather than at construction. The indirection is load-bearing
// for the bootstrap session: sessions.New invokes the runner factory while
// building it, so the test cannot know the bootstrap id until New has returned and
// the runner already exists. Arming by id afterwards is what lets the refusal
// cases below give the bootstrap a distinguishable inventory.
//
// The mutex is not decorative: the isolation test runs Pool.Run, whose lifecycle
// goroutines hold the same runner values the test goroutine arms through.
//
// It is modelListPlan's shape reproduced rather than shared, which is this
// family's own convention — keeping each twin's rig byte-stable beats folding
// them, exactly as resolveBoundSession states for the resolvers themselves.
type slashCommandListPlan struct {
	mu   sync.Mutex
	byID map[sessions.SessionID]turnevent.SlashCommandList
}

func newSlashCommandListPlan() *slashCommandListPlan {
	return &slashCommandListPlan{byID: map[sessions.SessionID]turnevent.SlashCommandList{}}
}

// arm makes id's runner report list. A session with NO entry reports the
// unreported state instead, which is the nothing-retained fixture — the plan's
// zero state is "this child never answered initialize".
func (p *slashCommandListPlan) arm(id sessions.SessionID, list turnevent.SlashCommandList) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byID[id] = list
}

func (p *slashCommandListPlan) get(id sessions.SessionID) (turnevent.SlashCommandList, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	list, ok := p.byID[id]
	return list, ok
}

// slashCommandListRunner is stubRunner plus the ONE concrete method
// resolveBoundSlashCommandList asserts for. stubRunner itself deliberately does
// not implement it and therefore stays the ready-made not-implemented fixture —
// see the runner-lacks-the-method test below, which builds its pool with
// newRouterTestPool for exactly that reason.
type slashCommandListRunner struct {
	stubRunner
	id   sessions.SessionID
	plan *slashCommandListPlan
}

func (r slashCommandListRunner) SlashCommandList() (turnevent.SlashCommandList, bool) {
	return r.plan.get(r.id)
}

// newSlashCommandListTestPool builds a real *sessions.Pool whose every session's
// runner answers from the returned plan. RegistryPath points into a fresh temp dir
// so Pool.Create has a resolvable data dir for the per-session settings file; a
// cold start there mints a bootstrap without spawning claude, exactly as
// newRouterTestPool relies on.
func newSlashCommandListTestPool(t *testing.T) (*sessions.Pool, *slashCommandListPlan) {
	t.Helper()
	plan := newSlashCommandListPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return slashCommandListRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, plan
}

// sentinelSlashCommandList builds a two-entry inventory in which every string
// carries tag and the two entries differ in every field, so a transposed entry, a
// crossed field or a sibling session's inventory is visible rather than plausible.
//
// The values are CONSPICUOUS SENTINELS rather than realistic command names, for
// sentinelModelList's measured reason: the log negative below is a
// strings.Contains over a whole captured log, and a natural value like "commit"
// or "review" is a substring of unrelated text, which would make the negative RED
// against a correct implementation.
//
// Entry 2 is the deliberately awkward one — nil Aliases, nil TruncatedFields — so
// a resolver that allocates an empty slice or normalises here is visible.
// Normalising is protocol.SlashCommandListPayload.MarshalJSON's job for Commands
// and protocol.SlashCommand.MarshalJSON's for Aliases, and TruncatedFields is
// deliberately EXEMPT from both, so a nil there has to survive this whole path.
// A non-zero DroppedCommands reddens a hard-coded 0.
//
// Both entries stay well inside turnbridge's maxSlashCommandListBytes, so the
// frame cut contributes nothing and DroppedCommands arrives as the armed value —
// the two-term sum is internal/turnbridge's to pin and is not re-tabled here.
func sentinelSlashCommandList(tag string) turnevent.SlashCommandList {
	return turnevent.SlashCommandList{
		Commands: []turnevent.SlashCommand{
			{
				Name:            "ZZNAMEONE" + tag + "ZZ",
				ArgumentHint:    "ZZHINTONE" + tag + "ZZ",
				Description:     "ZZDESCONE" + tag + "ZZ",
				Aliases:         []string{"ZZALIASONEA" + tag + "ZZ", "ZZALIASONEB" + tag + "ZZ"},
				TruncatedFields: []string{"ZZTRUNCONE" + tag + "ZZ"},
			},
			{
				Name:            "ZZNAMETWO" + tag + "ZZ",
				ArgumentHint:    "ZZHINTTWO" + tag + "ZZ",
				Description:     "ZZDESCTWO" + tag + "ZZ",
				Aliases:         nil,
				TruncatedFields: nil,
			},
		},
		DroppedCommands: 4,
	}
}

// assertSlashCommandsCarry compares a resolved payload against the retained event
// it was mapped from, field by field across all five SlashCommand fields and in
// order. Comparing against the SOURCE rather than a hand-spelled copy keeps this
// file from re-tabling internal/turnbridge's mapping (which owns and pins it)
// while still catching a transposed entry or a crossed field, because
// sentinelSlashCommandList makes every value distinct.
//
// Aliases and TruncatedFields are compared with reflect.DeepEqual rather than by
// length or with slices.Equal, because nil and empty must NOT compare equal here:
// slices.Equal(nil, []string{}) reports true, and this path's whole obligation for
// TruncatedFields is that a nil crosses as a nil — the mapping's arm calls that
// nil load-bearing, since protocol.SlashCommand.MarshalJSON exempts the field and
// an emitted [] would tell a client that claude's cut text is complete.
func assertSlashCommandsCarry(t *testing.T, got protocol.SlashCommandListPayload, want turnevent.SlashCommandList) {
	t.Helper()
	if len(got.Commands) != len(want.Commands) {
		t.Fatalf("payload carries %d commands, want %d: %+v", len(got.Commands), len(want.Commands), got.Commands)
	}
	for i := range want.Commands {
		w, g := want.Commands[i], got.Commands[i]
		if g.Name != w.Name {
			t.Errorf("Commands[%d].Name = %q, want %q", i, g.Name, w.Name)
		}
		if g.ArgumentHint != w.ArgumentHint {
			t.Errorf("Commands[%d].ArgumentHint = %q, want %q", i, g.ArgumentHint, w.ArgumentHint)
		}
		if g.Description != w.Description {
			t.Errorf("Commands[%d].Description = %q, want %q", i, g.Description, w.Description)
		}
		if !reflect.DeepEqual(g.Aliases, w.Aliases) {
			t.Errorf("Commands[%d].Aliases = %#v, want %#v", i, g.Aliases, w.Aliases)
		}
		if !reflect.DeepEqual(g.TruncatedFields, w.TruncatedFields) {
			t.Errorf("Commands[%d].TruncatedFields = %#v, want %#v", i, g.TruncatedFields, w.TruncatedFields)
		}
	}
}

// #2005 AC 1: a conversation bound to a session whose runner holds a reported
// inventory resolves to a marshal-ready payload carrying THAT conversation's id
// and THAT session's commands.
//
// DroppedCommands and ConversationID are asserted against literals rather than
// against the fixture's own fields, so the two values a caller could plausibly
// re-derive (a count from len(Commands), an id reflected from the convID
// parameter) are pinned to what was actually configured.
func TestResolveBoundSlashCommandList_ResolvesTheBoundSessionsInventory(t *testing.T) {
	t.Parallel()

	pool, plan := newSlashCommandListTestPool(t)
	held := sentinelSlashCommandList("BOUND")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-bound",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundSlashCommandList(reg, pool, "conv-bound")
	if !ok {
		t.Fatalf("resolveBoundSlashCommandList(conv-bound) refused; want the bound session's inventory")
	}
	if got.ConversationID != "conv-bound" {
		t.Errorf("ConversationID = %q, want %q", got.ConversationID, "conv-bound")
	}
	if got.DroppedCommands != 4 {
		t.Errorf("DroppedCommands = %d, want 4 (the retained count plus the frame cut's zero, never recomputed from len(Commands))", got.DroppedCommands)
	}
	assertSlashCommandsCarry(t, got, held)
}

// #2005 AC 2: a conversation id that is unknown, unbound or empty answers "no
// list" and hands back the ZERO payload — never the bootstrap session's inventory
// and never another session's. The dangling-binding row rides along: it is the
// same refusal one step further down the chain.
//
// The bootstrap session is armed with a distinguishable inventory in every row,
// which is what makes the isolation guard's mutant SOLE-RED rather than
// invisible: delete the `conv.CurrentSessionID == ""` clause from
// resolveBoundSlashCommandList and Pool.Lookup("") hands back the bootstrap
// session, so the unbound and empty-id rows flip to ok == true carrying
// ZZ...BOOTSTRAPZZ commands. An unarmed bootstrap would flip them to ok == false
// and pin nothing.
func TestResolveBoundSlashCommandList_RefusesWithoutAList(t *testing.T) {
	t.Parallel()

	pool, plan := newSlashCommandListTestPool(t)
	plan.arm(pool.BootstrapID(), sentinelSlashCommandList("BOOTSTRAP"))

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
			got, ok := resolveBoundSlashCommandList(reg, pool, tc.convID)
			if ok {
				t.Fatalf("resolveBoundSlashCommandList(%q) = (%+v, true), want a refusal — %s", tc.convID, got, tc.why)
			}
			if !reflect.DeepEqual(got, protocol.SlashCommandListPayload{}) {
				t.Errorf("refusal returned %+v, want the zero payload", got)
			}
			if strings.Contains(got.ConversationID, "BOOTSTRAP") || len(got.Commands) > 0 {
				t.Errorf("refusal leaked a payload: %+v", got)
			}
		})
	}
}

// #2005 AC 2: a session whose runner implements SlashCommandList but has nothing
// retained answers "no list" through the comma-ok rather than an empty payload.
// The bool is the only spelling of the unreported state, and the reason is the
// PRODUCER's rather than the type's — turnevent.SlashCommandList.Commands carries
// no "never empty" guarantee, but streamsup's emitSlashCommandList suppresses the
// empty list (#1877), so nothing with zero entries ever reaches the retention.
//
// Split out of the table above because the resolution here succeeds all the way
// DOWN to the hold and refuses there, which is a different arm from every row
// above; it is the sole red for a `list, _ := lister.SlashCommandList()`
// simplification.
func TestResolveBoundSlashCommandList_UnreportedSessionAnswersNoList(t *testing.T) {
	t.Parallel()

	pool, _ := newSlashCommandListTestPool(t)
	// Nothing armed: the bootstrap's runner implements SlashCommandList and
	// reports the unreported state.
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-silent",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundSlashCommandList(reg, pool, "conv-silent")
	if ok {
		t.Fatalf("resolveBoundSlashCommandList = (%+v, true); a session that reported nothing must answer no list", got)
	}
	if len(got.Commands) != 0 {
		t.Errorf("refusal carried %d commands, want none", len(got.Commands))
	}
	if !reflect.DeepEqual(got, protocol.SlashCommandListPayload{}) {
		t.Errorf("refusal returned %+v, want the zero payload", got)
	}
}

// #2005 AC 2: a runner that does not implement SlashCommandList is a REFUSAL, not
// a panic. newRouterTestPool's plain stubRunner is the fixture — it satisfies
// sessions.Runner and nothing more, which is exactly the shape a non-stream-json
// runner has.
func TestResolveBoundSlashCommandList_RunnerWithoutTheMethodRefuses(t *testing.T) {
	t.Parallel()

	pool := newRouterTestPool(t)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-plain",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got, ok := resolveBoundSlashCommandList(reg, pool, "conv-plain")
	if ok {
		t.Fatalf("resolveBoundSlashCommandList = (%+v, true); a runner without SlashCommandList must refuse", got)
	}
	if !reflect.DeepEqual(got, protocol.SlashCommandListPayload{}) {
		t.Errorf("refusal returned %+v, want the zero payload", got)
	}
}

// #2005 AC 3: two conversations bound to two DIFFERENT pool sessions each get
// their own session's inventory and never the sibling's. This is the pin a
// hard-coded lookup, a shared cache, or a resolver keyed on the wrong hop cannot
// pass, and it is where the reported id being per-record rather than global is
// observable.
//
// Not parallel: t.Setenv confines anything the pool's create path might resolve
// out of HOME, and t.Setenv forbids t.Parallel.
func TestResolveBoundSlashCommandList_IsolatesConversations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pool, plan := newSlashCommandListTestPool(t)
	// Pool.Create schedules the new session on the run group, so the pool has to
	// be running or supervise returns ErrPoolNotRunning.
	ctx := runPoolReady(t, pool)
	sessA := pool.BootstrapID()
	sessB, err := pool.Create(ctx, "session-b")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	listA, listB := sentinelSlashCommandList("ALPHA"), sentinelSlashCommandList("BETA")
	plan.arm(sessA, listA)
	plan.arm(sessB, listB)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-a", CurrentSessionID: string(sessA), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-b", CurrentSessionID: string(sessB), LastUsedAt: now})

	gotA, ok := resolveBoundSlashCommandList(reg, pool, "conv-a")
	if !ok {
		t.Fatal("resolveBoundSlashCommandList(conv-a) refused")
	}
	if gotA.ConversationID != "conv-a" {
		t.Errorf("conv-a payload names %q", gotA.ConversationID)
	}
	assertSlashCommandsCarry(t, gotA, listA)

	gotB, ok := resolveBoundSlashCommandList(reg, pool, "conv-b")
	if !ok {
		t.Fatal("resolveBoundSlashCommandList(conv-b) refused")
	}
	if gotB.ConversationID != "conv-b" {
		t.Errorf("conv-b payload names %q", gotB.ConversationID)
	}
	assertSlashCommandsCarry(t, gotB, listB)

	// Stated as its own assertion rather than left implicit in the two above: the
	// failure this test exists to catch is A being answered with B's inventory,
	// and a reader should not have to compare two sentinel tags to see that.
	if gotA.Commands[0].Name == gotB.Commands[0].Name {
		t.Fatalf("both conversations resolved to the same inventory: %q", gotA.Commands[0].Name)
	}
}

// #2005 AC 4: no command name, argument hint, description or alias reaches a log
// record on this path at any level, and neither does the caller's untrusted
// conversation id.
//
// What this pins, honestly: the function takes no *slog.Logger, so the STRUCTURAL
// half of AC 4 is enforced by the signature and by review, not here. What this
// catches is the one real regression shape — someone reaching for the
// package-level slog.Info / slog.Default() instead of adding a parameter. A
// mutant adding slog.Info("resolved", "commands", list.Commands) to the happy
// path reddens it; nothing else in the package does.
//
// It must NOT call t.Parallel: slog.SetDefault is process-global and would race
// every other parallel test in this package. The pool is built BEFORE the default
// is swapped, so sessions.New captures the old default and the pool's own
// diagnostics cannot land in this buffer — the only writer left is the code under
// test.
func TestResolveBoundSlashCommandList_LogsNothing(t *testing.T) {
	pool, plan := newSlashCommandListTestPool(t)
	held := sentinelSlashCommandList("LOGNEG")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-logneg",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	got, ok := resolveBoundSlashCommandList(reg, pool, "conv-logneg")
	if !ok {
		t.Fatalf("resolveBoundSlashCommandList refused; the log negative needs the happy path")
	}
	// Refused resolutions are the other half: the untrusted convID must not reach
	// a log line either.
	if _, ok := resolveBoundSlashCommandList(reg, pool, "conv-ZZUNTRUSTEDZZ"); ok {
		t.Fatal("resolveBoundSlashCommandList resolved an unknown conversation")
	}

	logs := buf.String()
	if logs != "" {
		t.Fatalf("resolveBoundSlashCommandList wrote %d bytes of log; it must write none:\n%s", len(logs), logs)
	}
	// Belt-and-braces on the values themselves, so a future handler swap that made
	// the emptiness check weaker still names what leaked. The v != "" guard is not
	// decorative: strings.Contains(logs, "") is true unconditionally, so an empty
	// field would make this loop assert nothing.
	for _, c := range got.Commands {
		for _, v := range append([]string{c.Name, c.ArgumentHint, c.Description}, c.Aliases...) {
			if v != "" && strings.Contains(logs, v) {
				t.Errorf("a slash-command value leaked into a log record: %q", v)
			}
		}
	}
	if strings.Contains(logs, "ZZUNTRUSTEDZZ") {
		t.Errorf("the caller's untrusted conversation id leaked into a log record:\n%s", logs)
	}
}

// indexSlashCommandsByConversation keys an enumeration's result on
// ConversationID, which is how every assertion below reads it:
// retainedSlashCommandLists returns List's order (registry insertion order) and
// that is deliberately NOT a contract — the seam's own doc block binds callers to
// correlate by conversation id, and reconcileSlashCommandLists stamps one fixed,
// non-load-bearing envelope id across the batch. It also fails a duplicated id,
// which is the shape a loop that appended the same row twice would take.
//
// It is indexByConversation's shape reproduced rather than shared, this family's
// convention for keeping each twin's rig byte-stable.
func indexSlashCommandsByConversation(t *testing.T, got []protocol.SlashCommandListPayload) map[string]protocol.SlashCommandListPayload {
	t.Helper()
	byID := make(map[string]protocol.SlashCommandListPayload, len(got))
	for _, p := range got {
		if _, dup := byID[p.ConversationID]; dup {
			t.Fatalf("conversation %q contributed twice: %+v", p.ConversationID, got)
		}
		byID[p.ConversationID] = p
	}
	return byID
}

// #2007 AC 1: one conversation bound to a session holding a retained inventory
// yields exactly one payload, carrying that session's commands in claude's order
// under that conversation's id.
//
// The bootstrap session is armed and bound, so a mutant that returned the zero
// payload or an empty slice is red on the count as well as on the contents.
// DroppedCommands is asserted against a literal rather than the fixture's field,
// so a count recomputed from len(Commands) reddens — shipping 0 would tell a
// client that a capped menu is the whole menu.
func TestRetainedSlashCommandLists_EnumeratesTheBoundSessionsInventory(t *testing.T) {
	t.Parallel()

	pool, plan := newSlashCommandListTestPool(t)
	held := sentinelSlashCommandList("ENUM")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-enum",
		CurrentSessionID: string(pool.BootstrapID()),
		LastUsedAt:       time.Now().UTC(),
	})

	got := retainedSlashCommandLists(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedSlashCommandLists returned %d payloads, want exactly 1: %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-enum" {
		t.Errorf("ConversationID = %q, want %q", got[0].ConversationID, "conv-enum")
	}
	if got[0].DroppedCommands != 4 {
		t.Errorf("DroppedCommands = %d, want 4 (carried from the retained list, never recomputed)", got[0].DroppedCommands)
	}
	assertSlashCommandsCarry(t, got[0], held)
}

// #2007 AC 4: each refusal contributes no payload and raises no error, and does so
// WITHOUT aborting the enumeration or contaminating a sibling's payload. The
// resolver's comma-ok is the only filter, so every one of these skips is decided
// inside resolveBoundSlashCommandList and nowhere here.
//
// All four rows live in ONE registry on purpose — that is the whole point of the
// test. A refusal that aborted the enumeration, appended a zero payload, or
// carried the previous row's commands forward is red here and invisible in four
// single-row registries. The three refusing rows are the three arms reachable from
// this entry point; the resolver's unknown-conversation arm is unreachable because
// every id came out of List.
//
// The contributing row is created LAST, after all three refusals, and that order is
// load-bearing: List returns registry insertion order, so a mutant that broke out
// of the loop on the first refusal instead of continuing would still return the
// survivor — and go green — if the survivor came first. Behind three refusals it
// returns nothing.
func TestRetainedSlashCommandLists_SkipsEachRefusalAndKeepsGoing(t *testing.T) {
	t.Parallel()

	pool, plan := newSlashCommandListTestPool(t)
	held := sentinelSlashCommandList("SURVIVOR")
	plan.arm(pool.BootstrapID(), held)

	// A second pool session whose runner implements SlashCommandList but has
	// nothing armed: the "reported nothing" refusal, one step deeper than the two
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

	got := retainedSlashCommandLists(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedSlashCommandLists returned %d payloads, want exactly 1 (only conv-live contributes): %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-live" {
		t.Fatalf("the surviving payload names %q, want %q", got[0].ConversationID, "conv-live")
	}
	assertSlashCommandsCarry(t, got[0], held)
	// An empty menu is never presented as a real one: no payload may carry zero
	// commands, whichever row produced it. This is the assertion a `len(Commands)
	// > 0` filter here would make vacuous — the guarantee belongs to the producer
	// (streamsup's emitSlashCommandList suppresses the empty list, #1877) and is
	// spelled once, in the resolver's bool.
	for _, p := range got {
		if len(p.Commands) == 0 {
			t.Errorf("payload for %q carries an empty inventory; a refusal must contribute nothing at all", p.ConversationID)
		}
	}
}

// #2007 AC 4, the nothing-to-send half: an empty registry and a registry whose
// every row refuses both enumerate to no payloads and no panic. Split from the test
// above because that one always has a survivor, so it cannot distinguish "skipped
// the refusals" from "returned the survivor and stopped".
func TestRetainedSlashCommandLists_NothingToSend(t *testing.T) {
	t.Parallel()

	pool, plan := newSlashCommandListTestPool(t)
	// Armed but never bound: a mutant that enumerated the POOL instead of the
	// registry would contribute here, and an unarmed bootstrap would hide that.
	plan.arm(pool.BootstrapID(), sentinelSlashCommandList("UNREACHABLE"))

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
			if got := retainedSlashCommandLists(tc.reg, pool)(); len(got) != 0 {
				t.Fatalf("retainedSlashCommandLists returned %d payloads, want none: %+v", len(got), got)
			}
		})
	}
}

// #2007 AC 3: the enumeration is unfiltered, so an ARCHIVED conversation whose
// bound session still holds an inventory DOES contribute. Registry.SetArchived
// writes exactly one field and never unbinds CurrentSessionID, and the reconcile
// asserts current control truth — the client decides what to show.
//
// This is the sole red for a mutant that narrows the call to
// List(ListFilter{IsArchived: &f}), which is otherwise invisible: every other test
// here builds unarchived rows.
func TestRetainedSlashCommandLists_ArchivedConversationsContribute(t *testing.T) {
	t.Parallel()

	pool, plan := newSlashCommandListTestPool(t)
	held := sentinelSlashCommandList("ARCHIVED")
	plan.arm(pool.BootstrapID(), held)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:               "conv-archived",
		CurrentSessionID: string(pool.BootstrapID()),
		IsArchived:       true,
		LastUsedAt:       time.Now().UTC(),
	})

	got := retainedSlashCommandLists(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedSlashCommandLists returned %d payloads, want 1 — archiving does not unbind the session: %+v", len(got), got)
	}
	if got[0].ConversationID != "conv-archived" {
		t.Errorf("ConversationID = %q, want %q", got[0].ConversationID, "conv-archived")
	}
	assertSlashCommandsCarry(t, got[0], held)
}

// #2007 AC 2: two conversations bound to two DIFFERENT pool sessions each carry
// their own session's inventory across ONE enumeration. This is the pin a shared
// buffer, an off-by-one, or a loop-variable capture cannot pass, and it is the sole
// red for the #678 fork the double lookup exists to prevent — reading
// CurrentSessionID off the listed row and calling Pool.Lookup directly hands an
// unbound row the bootstrap child's menu stamped with its own conversation id.
// TestResolveBoundSlashCommandList_IsolatesConversations makes the same point one
// call at a time; only the enumerator can cross two rows within a single result
// slice, and a single-conversation fixture cannot redden any of it.
//
// The two sessions also pin that two payloads never share a backing array: each
// resolver call takes its own deep copy out of the hold, so a mutant that hoisted
// one copy out of the loop would cross the two inventories here.
//
// Not parallel: t.Setenv confines anything the pool's create path resolves out of
// HOME, and t.Setenv forbids t.Parallel.
func TestRetainedSlashCommandLists_DoesNotCrossConversations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pool, plan := newSlashCommandListTestPool(t)
	// Pool.Create schedules the new session on the run group, so the pool has to be
	// running or supervise returns ErrPoolNotRunning.
	ctx := runPoolReady(t, pool)
	sessA := pool.BootstrapID()
	sessB, err := pool.Create(ctx, "session-b")
	if err != nil {
		t.Fatalf("Pool.Create: %v", err)
	}

	listA, listB := sentinelSlashCommandList("ALPHAENUM"), sentinelSlashCommandList("BETAENUM")
	plan.arm(sessA, listA)
	plan.arm(sessB, listB)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-a", CurrentSessionID: string(sessA), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-b", CurrentSessionID: string(sessB), LastUsedAt: now})

	got := retainedSlashCommandLists(reg, pool)()
	if len(got) != 2 {
		t.Fatalf("retainedSlashCommandLists returned %d payloads, want 2: %+v", len(got), got)
	}
	byID := indexSlashCommandsByConversation(t, got)
	gotA, ok := byID["conv-a"]
	if !ok {
		t.Fatalf("conv-a contributed no payload: %+v", got)
	}
	gotB, ok := byID["conv-b"]
	if !ok {
		t.Fatalf("conv-b contributed no payload: %+v", got)
	}
	assertSlashCommandsCarry(t, gotA, listA)
	assertSlashCommandsCarry(t, gotB, listB)
	// Stated separately for the same reason the resolver's twin states it: the
	// failure this test exists to catch is A being handed B's menu stamped with A's
	// own id, and a reader should not have to compare two sentinel tags to see it.
	if gotA.Commands[0].Name == gotB.Commands[0].Name {
		t.Fatalf("both conversations enumerated the same inventory: %q", gotA.Commands[0].Name)
	}
}

// #2007 AC 5: no command name, argument hint, description or alias reaches a log
// record on this path at any level, and neither does a conversation id.
//
// What this pins, honestly: retainedSlashCommandLists takes no *slog.Logger, so the
// structural half of AC 5 is enforced by the signature and by review. What this
// catches is the one real regression shape — someone reaching for the package-level
// slog.Info / slog.Default() to explain why a row did not contribute. The registry
// deliberately holds BOTH a contributing row and refusing ones, because that "why
// did this row skip" line is exactly where such a call would be added. The strings
// are workspace-authored — whoever wrote a repository controls them — which is a
// lower-trust origin than the model-list twin's claude-authored values.
//
// It must NOT call t.Parallel: slog.SetDefault is process-global. The pool is built
// BEFORE the default is swapped, so sessions.New captures the old default and the
// pool's own diagnostics cannot land in this buffer.
func TestRetainedSlashCommandLists_LogsNothing(t *testing.T) {
	pool, plan := newSlashCommandListTestPool(t)
	held := sentinelSlashCommandList("ENUMLOGNEG")
	plan.arm(pool.BootstrapID(), held)

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-logneg", CurrentSessionID: string(pool.BootstrapID()), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-ZZUNTRUSTEDENUMZZ", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-gone", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	got := retainedSlashCommandLists(reg, pool)()
	if len(got) != 1 {
		t.Fatalf("retainedSlashCommandLists returned %d payloads, want 1; the log negative needs the happy path", len(got))
	}

	logs := buf.String()
	if logs != "" {
		t.Fatalf("retainedSlashCommandLists wrote %d bytes of log; it must write none:\n%s", len(logs), logs)
	}
	// Belt-and-braces on the values themselves, so a future handler swap that made
	// the emptiness check weaker still names what leaked. The v != "" guard is not
	// decorative: strings.Contains(logs, "") is true unconditionally, so an empty
	// field would make this loop assert nothing.
	for _, c := range got[0].Commands {
		for _, v := range append([]string{c.Name, c.ArgumentHint, c.Description}, c.Aliases...) {
			if v != "" && strings.Contains(logs, v) {
				t.Errorf("a slash-command value leaked into a log record: %q", v)
			}
		}
	}
	// The refusing rows' ids are the other half: a "why did this row not
	// contribute" line would carry one.
	if strings.Contains(logs, "ZZUNTRUSTEDENUMZZ") {
		t.Errorf("a skipped conversation's id leaked into a log record:\n%s", logs)
	}
}
