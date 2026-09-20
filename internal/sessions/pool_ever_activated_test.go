package sessions

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestPool_EverActivated_LiveSessions is the in-pool half of #2521's
// discriminator: a session the pool HOLDS answers from its own timestamps.
//
// The minted-but-never-activated row is the one that matters. Since #2085 a
// conversation's first spawn is deferred to its first message, so that row is
// exactly the state a conversation created but never messaged sits in — and it is
// the state the named new_session path must keep refusing. Pool.buildSession
// stamps createdAt and lastActiveAt from ONE now value, so the two are equal to
// the nanosecond there and the predicate is an exact reading rather than a
// tolerance.
func TestPool_EverActivated_LiveSessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := helperPoolCreate(t, filepath.Join(dir, "sessions.json"), 0)
	runPoolInBackground(t, p)

	id, err := p.Mint("conv", "")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if p.EverActivated(id) {
		t.Fatalf("EverActivated(minted) = true, want false: a conversation created but never " +
			"messaged has never run, and rotating it would draw a delimiter for a chat with no turn")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !p.EverActivated(id) {
		t.Fatalf("EverActivated(activated) = false, want true: the session has run, so a reset " +
			"must not be refused as never-used")
	}
}

// TestPool_EverActivated_DormantEntries is the after-restart half: an entry
// Pool.New did not materialise answers from its PERSISTED timestamps, so the
// reading survives the daemon restart it exists to survive.
//
// Both rows are dormant — neither session is in p.sessions — so a predicate that
// only consulted the live map would answer false for both and the used row would
// redden. That is the whole defect this reader closes for the named reset.
func TestPool_EverActivated_DormantEntries(t *testing.T) {
	dir := t.TempDir()
	when := time.Now().UTC()

	never := helperDormantID(t)
	used := helperDormantID(t)
	p, _ := helperPoolWarmStart(t, filepath.Join(dir, "sessions.json"), dir,
		registryEntry{ID: never, Label: "never", CreatedAt: when, LastActiveAt: when},
		registryEntry{ID: used, Label: "used", CreatedAt: when, LastActiveAt: when.Add(time.Minute)},
	)

	if p.EverActivated(never) {
		t.Errorf("EverActivated(dormant, last_active_at == created_at) = true, want false")
	}
	if !p.EverActivated(used) {
		t.Errorf("EverActivated(dormant, last_active_at > created_at) = false, want true: a channel " +
			"that ran before the restart must be resettable without a preliminary message")
	}
}

// TestPool_EverActivated_SurvivesRevive is the pin for the defect the first pass
// shipped: the discriminator was durable across a daemon RESTART but not across a
// REVIVE, and one of the two routes to a reset revives first.
//
// /clear lands there deterministically. handleSendMessage calls Route for binding
// validation and discards the writer, Route re-materialises a dormant session
// through Pool.Revive WITHOUT activating it, and only then does the intercept
// raise the named new_session. Before the carry in materialise, that sequence
// restamped created_at and last_active_at to one fresh now and deleted the entry
// holding the real pair — so the reset asked "has this ever run?" of a session
// whose evidence its own caller had just destroyed, got false, and stayed inert.
// The desktop Reset control worked on a dormant channel while typing /clear in
// that same channel did nothing, which #2456 requires to be one behaviour.
//
// The never-used row is the other half and is not decoration: it proves the carry
// preserves the entry's answer rather than manufacturing a "used" one, so the
// #2085 refusal still survives a revive too.
func TestPool_EverActivated_SurvivesRevive(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	when := time.Now().UTC()

	never := helperDormantID(t)
	used := helperDormantID(t)
	p, _ := helperPoolWarmStart(t, regPath, dir,
		registryEntry{ID: never, Label: "never", CreatedAt: when, LastActiveAt: when},
		registryEntry{ID: used, Label: "used", CreatedAt: when, LastActiveAt: when.Add(time.Minute)},
	)
	runPoolInBackground(t, p)

	// The dormant arm's answers, before anything materialises either id.
	if !p.EverActivated(used) {
		t.Fatalf("EverActivated(dormant used) = false before the revive, want true — the fixture " +
			"does not state the case this test is about")
	}

	// Revive WITHOUT activating: the state Route leaves a dormant session in on its
	// way through, and the state the reset then asks about.
	for _, id := range []SessionID{used, never} {
		if _, err := p.Revive(id, "conv-"+string(id), ""); err != nil {
			t.Fatalf("Revive(%s): %v", id, err)
		}
	}

	if !p.EverActivated(used) {
		t.Errorf("EverActivated(used) = false after Revive with no Activate, want true: a revive must " +
			"carry the retired entry's timestamps, or /clear on a dormant channel reads its own " +
			"conversation as never-used and stays inert")
	}
	if p.EverActivated(never) {
		t.Errorf("EverActivated(never) = true after Revive, want false: the carry must preserve the " +
			"entry's answer, not manufacture one — the #2085 refusal survives a revive too")
	}

	// Independently correct, and the reason the carry is not merely a reader's
	// convenience: saveLocked persists CreatedAt from s.createdAt, so before this a
	// restart-plus-revive rewrote the session's creation time to now and lost the
	// original permanently.
	entry := entryByID(t, regPath, used)
	if entry == nil {
		t.Fatalf("revived entry %q missing from %q after Revive persisted it", used, regPath)
	}
	if !entry.CreatedAt.Equal(when) {
		t.Errorf("persisted created_at = %s, want the original %s — a revive must not restamp a "+
			"session's age", entry.CreatedAt, when)
	}
	if !entry.LastActiveAt.Equal(when.Add(time.Minute)) {
		t.Errorf("persisted last_active_at = %s, want the original %s", entry.LastActiveAt, when.Add(time.Minute))
	}
}

// TestPool_EverActivated_Refusals pins the two non-answers. The empty id is the
// #678 isolation point resolveBoundSession documents: Pool.Lookup("") resolves to
// the BOOTSTRAP session, so any id-keyed reader that did not refuse it first would
// answer a question about the daemon's shared child instead of the caller's.
func TestPool_EverActivated_Refusals(t *testing.T) {
	dir := t.TempDir()
	p := helperPoolArgvRecorder(t, filepath.Join(dir, "sessions.json"), dir)

	if p.EverActivated("") {
		t.Errorf("EverActivated(\"\") = true, want false: the empty id must never resolve to the bootstrap")
	}
	if p.EverActivated(helperDormantID(t)) {
		t.Errorf("EverActivated(unknown) = true, want false")
	}
}
