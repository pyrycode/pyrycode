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
