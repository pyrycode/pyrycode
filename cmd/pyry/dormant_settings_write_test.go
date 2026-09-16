package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	dormantWriteBootID   = "550e8400-e29b-41d4-a716-446655440000"
	dormantWriteTargetID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

// newDormantWritePool builds a real *sessions.Pool warm-started from a registry
// holding a live bootstrap and one entry this pool will NOT materialise, so the
// adapter meets a genuinely dormant id rather than a double's idea of one. The
// pattern is TestResolveBoundRunSettings_DormantRealPool's; what it adds is the
// model-list plan, because the adapter's membership gate needs an armed
// vocabulary source and retainedModelVocabulary reaches the BOOTSTRAP's hold for
// an id it cannot look up.
//
// dormantFields is the entry's settings fragment, spliced in as raw JSON so a
// test can persist a posture the sessions package would refuse to write —
// registryEntry is unexported, and hand-writing the file is also what makes the
// warm start real rather than a map poke.
func newDormantWritePool(t *testing.T, dormantFields string) (*sessions.Pool, *modelListPlan) {
	t.Helper()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	registry := `{"version":1,"sessions":[
		{"id":"` + dormantWriteBootID + `","label":"boot","bootstrap":true,"created_at":"2026-09-01T00:00:00Z","last_active_at":"2026-09-01T00:00:00Z"},
		{"id":"` + dormantWriteTargetID + `","label":"conv-1",` + dormantFields + `"created_at":"2026-09-01T00:00:01Z","last_active_at":"2026-09-01T00:00:01Z"}
	]}`
	if err := os.WriteFile(regPath, []byte(registry), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	plan := newModelListPlan()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: regPath,
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return modelListRunner{id: sessions.SessionID(cfg.SessionID), plan: plan}, nil
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	if live := pool.List(); len(live) != 1 || string(live[0].ID) != dormantWriteBootID {
		t.Fatalf("warm start materialised %+v, want the bootstrap alone — the fixture's whole point is an unmaterialised entry", live)
	}
	return pool, plan
}

// dormantSettings reads the target entry's model and effort, failing the test if
// the pool no longer holds it as dormant.
func dormantSettings(t *testing.T, pool *sessions.Pool) sessions.SessionSettings {
	t.Helper()
	got, err := pool.DormantSettingsFor(dormantWriteTargetID)
	if err != nil {
		t.Fatalf("DormantSettingsFor(target): %v — the id must still be dormant", err)
	}
	return got
}

// TestSettingsUpdaterAdapter_DormantWriteLands (AC 1, AC 4 positive): a frame
// naming a model the retained vocabulary offers, and an effort beside it, is
// accepted for a session the daemon holds only as a dormant entry — where it
// used to be relay.ErrSessionUnknown, which the handler replies as
// session.not_found.
//
// The vocabulary is armed on the BOOTSTRAP rather than on the target, which is
// the point of arming it at all: a dormant id has no runner and no hold, so the
// gate can only be running against the same fallback the client's menu was
// published from. Arming the target instead would prove nothing and could not
// even be done — the session does not exist.
//
// The live-set assertion rides along because it is the criterion most easily lost
// to a later "just revive it" simplification (AC 2).
func TestSettingsUpdaterAdapter_DormantWriteLands(t *testing.T) {
	t.Parallel()

	pool, plan := newDormantWritePool(t, `"model":"sonnet","effort":"low",`)
	plan.arm(dormantWriteBootID, turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "opus"}, {Value: "sonnet"}}})
	adapter := settingsUpdaterAdapter{p: pool}

	before := pool.List()
	model, effort := "opus", "high"
	if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &model, Effort: &effort}); err != nil {
		t.Fatalf("UpdateSettings(dormant, offered model) = %v, want nil", err)
	}

	if got := dormantSettings(t, pool); got.Model != model || got.Effort != effort {
		t.Errorf("dormant settings after write = %q/%q, want %q/%q", got.Model, got.Effort, model, effort)
	}
	if after := pool.List(); len(after) != len(before) {
		t.Errorf("live sessions = %d, want %d unchanged — a settings write must materialise nothing", len(after), len(before))
	}
	if _, err := pool.SettingsFor(dormantWriteTargetID); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Errorf("SettingsFor(target) err = %v, want ErrSessionNotFound — the write revived the session", err)
	}
}

// TestSettingsUpdaterAdapter_DormantModelMembership (AC 4): the membership gate
// runs for a dormant id too, and a model the retained vocabulary does not offer
// persists nothing.
//
// This is the criterion the live-lookup gate would silently drop: its Pool.Lookup
// misses for a dormant id, so a version that kept "unknown → refuse" and left the
// dormant write below it would either refuse every dormant write or skip the
// check for all of them. An unvalidated model in a dormant entry becomes the
// revived child's --model, which is the argv-injection boundary.
func TestSettingsUpdaterAdapter_DormantModelMembership(t *testing.T) {
	t.Parallel()

	pool, plan := newDormantWritePool(t, `"model":"sonnet","effort":"low",`)
	plan.arm(dormantWriteBootID, turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "sonnet"}}})
	adapter := settingsUpdaterAdapter{p: pool}

	absent, effort := "opus", "high"
	err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &absent, Effort: &effort})
	if !errors.Is(err, relay.ErrModelNotOffered) {
		t.Fatalf("UpdateSettings(dormant, absent model) = %v, want ErrModelNotOffered", err)
	}
	if got := dormantSettings(t, pool); got.Model != "sonnet" || got.Effort != "low" {
		t.Errorf("dormant settings after refusal = %q/%q, want the entry's own %q/%q — a rejected frame persists no field",
			got.Model, got.Effort, "sonnet", "low")
	}

	// The same frame with an offered model lands, so the refusal above is the
	// membership verdict and not the dormant path being closed outright.
	offered := "sonnet"
	if err := adapter.UpdateSettings(dormantWriteTargetID, relay.SettingsUpdate{Model: &offered, Effort: &effort}); err != nil {
		t.Fatalf("UpdateSettings(dormant, offered model) = %v, want nil", err)
	}
	if got := dormantSettings(t, pool); got.Effort != effort {
		t.Errorf("effort after accepted write = %q, want %q", got.Effort, effort)
	}
}

// TestSettingsUpdaterAdapter_DormantPostureRefused (AC 5, at the wire seam): a
// frame naming yolo or permission_mode for a dormant session is refused as
// relay.ErrSessionUnknown — which the handler replies as session.not_found, the
// code this ticket deliberately did not split — and persists no field of itself.
//
// Each case carries an OFFERED model beside the posture, so the refusal cannot be
// mistaken for the membership gate firing, and so "persists no field of itself"
// is tested against a frame whose other fields would otherwise have landed.
//
// The fixture persists a bypass in both on-disk spellings. Nothing reads it back
// — a revive builds the default posture structurally (#1487) — but a write that
// rewrote or cleared those bytes would be touching state nobody asked it to.
func TestSettingsUpdaterAdapter_DormantPostureRefused(t *testing.T) {
	t.Parallel()

	yes, no := true, false
	plan, bypass := "plan", "bypassPermissions"
	for _, tc := range []struct {
		name   string
		update relay.SettingsUpdate
	}{
		{"an escalation", relay.SettingsUpdate{YOLO: &yes}},
		{"a revoke", relay.SettingsUpdate{YOLO: &no}},
		{"a non-default mode", relay.SettingsUpdate{PermissionMode: &plan}},
		{"the bypass mode", relay.SettingsUpdate{PermissionMode: &bypass}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pool, vocab := newDormantWritePool(t, `"model":"sonnet","effort":"low","yolo":true,"permission_mode":"bypassPermissions",`)
			vocab.arm(dormantWriteBootID, turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "opus"}}})
			adapter := settingsUpdaterAdapter{p: pool}

			model, effort := "opus", "high"
			update := tc.update
			update.Model, update.Effort = &model, &effort

			if err := adapter.UpdateSettings(dormantWriteTargetID, update); !errors.Is(err, relay.ErrSessionUnknown) {
				t.Fatalf("UpdateSettings(dormant, posture) = %v, want ErrSessionUnknown", err)
			}
			if got := dormantSettings(t, pool); got.Model != "sonnet" || got.Effort != "low" {
				t.Errorf("dormant settings after refusal = %q/%q, want the entry's own %q/%q — the model beside the posture must not land",
					got.Model, got.Effort, "sonnet", "low")
			}
		})
	}
}

// TestSettingsUpdaterAdapter_UnknownIDOnWarmPool: an id in neither half is still
// relay.ErrSessionUnknown on a pool that DOES hold dormant entries.
//
// The existing unknown-id assertion runs against a cold pool, where the dormant
// half is empty and a broken fall-through would be invisible. The empty id is the
// ""-is-bootstrap case: Pool.Lookup("") resolves the bootstrap, so the adapter's
// guard has to stand ahead of both writes — neither p.sessions nor p.dormant
// carries a "" key, but the guard is what keeps that from mattering.
func TestSettingsUpdaterAdapter_UnknownIDOnWarmPool(t *testing.T) {
	t.Parallel()

	pool, plan := newDormantWritePool(t, `"model":"sonnet",`)
	plan.arm(dormantWriteBootID, turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "opus"}}})
	adapter := settingsUpdaterAdapter{p: pool}

	unknown, err := sessions.NewID()
	if err != nil {
		t.Fatalf("sessions.NewID: %v", err)
	}
	model := "opus"
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"an id this daemon has no record of", string(unknown)},
		{"the empty id", ""},
	} {
		if err := adapter.UpdateSettings(tc.id, relay.SettingsUpdate{Model: &model}); !errors.Is(err, relay.ErrSessionUnknown) {
			t.Errorf("%s: UpdateSettings = %v, want ErrSessionUnknown", tc.name, err)
		}
	}

	// The refusals reached neither half: the dormant entry the pool does hold is
	// untouched, and the bootstrap did not absorb the "" write.
	if got := dormantSettings(t, pool); got.Model != "sonnet" {
		t.Errorf("dormant model = %q, want %q — an unknown id must not write a sibling entry", got.Model, "sonnet")
	}
	if got, err := pool.SettingsFor(dormantWriteBootID); err != nil || got.Model != "" {
		t.Errorf("bootstrap settings = %+v (err %v), want the model still unset — the empty id must not fall through to the bootstrap", got, err)
	}
}
