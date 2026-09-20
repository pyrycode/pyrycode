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
