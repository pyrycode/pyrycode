//go:build e2e_realclaude

package realclaude

// #1651 — the deterministic half of #1643's three-arm bypass-revocation probe:
// the table that fixes each arm's STORED launch posture, and the proof that the
// seed which writes those postures actually writes them.
//
// Everything here runs OFFLINE. This file reaches no live claude, no daemon, no
// subprocess and no credential: it must not reach resolveClaudeBin,
// probeClaudeVersion, WithWorktree, WithWorktreeAuthenticated, os.Getenv or
// os.Environ, and TestFinOfflineFilesReachNoExecHelper enforces that over this
// file's AST rather than over this paragraph. t.TempDir is deliberately NOT on
// that list — the seed writes a file, and it writes it there.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestSeedBypassRegistry_StoresRequestedPosture|TestPoolRevokeArms_' \
//	  ./internal/e2e/realclaude/
//
// Both tests must report PASS on a machine with no claude and no credentials.
// That is what makes them different from the rest of this package, and it is the
// point: #1643's first failure mode should cost zero tokens rather than a whole
// three-arm run. Read the RUN count, never the exit code — this package is behind
// the e2e_realclaude tag, `make check` never compiles it, and the suite exits 0
// both on a build failure and on a full credentials skip.
//
// # Why the two postures need different evidence
//
// A cold start also yields YOLO false: loadRegistry returns (nil, nil) for an
// absent file and Pool.New leaves SessionSettings zero-valued. So on the
// control_default arm a totally broken seed and a correct seed read back the same
// false through Pool.DefaultSettings, and that seam discriminates nothing. The
// only evidence separating a stored false from a cold start is the ENTRY'S
// EXISTENCE — `bootstrap` true and an id ValidID accepts — which is why the decode
// test below reads the file rather than the Pool.
//
// The true posture fails closed but VACUOUSLY. registryEntry's own comment records
// the invariant: a missing yolo key decodes to false and a malformed one fails the
// whole parse, so neither absence nor corruption can enable bypass. Safe — but for
// the revoke arm a misspelled key means the child launches WITHOUT bypass, there
// is nothing to revoke, and #1643 measures nothing while staying green. A decode
// performed through revokeSeedEntry round-trips that misspelling and sees nothing
// wrong, so the decode below goes through map[string]json.RawMessage with the
// on-disk key names written as literals.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// --- the arm table -----------------------------------------------------------

// poolRevokeArm is one arm of #1643's three-arm comparison of pyry's in-band
// bypass revocation.
//
// "Pool" is in the name because the three name STRINGS are also setModeArms',
// whose rows mean something else entirely: #1595 drove four children it owned
// through exec.CommandContext and its targetMode field is the mode a hand-written
// control line asks for, not a stored launch posture. Do not extend or reuse that
// table, and do not fold its fourth `enable` row in here.
//
// The row carries these three fields and no more. The sessions.SettingsUpdate
// value, the composed argv, the fixture record and the probe prompt belong to
// #1643 and #1652; claudeSettingsArgs already makes the argv derivable from
// launchYOLO, and carrying it would be a second source of truth.
type poolRevokeArm struct {
	name string

	// launchYOLO is the STORED bootstrap posture the arm's registry entry is
	// seeded with, via seedBypassRegistry. It is not a runtime flag: Pool.New
	// lifts it off the entry through pickBootstrap and claudeSettingsArgs turns
	// true into --dangerously-skip-permissions and false into nothing at all.
	launchYOLO bool

	// takesSettingsUpdate records whether the arm receives a mid-run Pool
	// settings update. Named for the settings frame rather than "takesUpdate"
	// because this repo also ships `pyry update`.
	takesSettingsUpdate bool
}

// poolRevokeArms is READ-ONLY: never append to it, never reassign it. It is
// ranged over from t.Parallel() tests in at least three files — the pin below,
// #1652's name test and #1643's live driver — and a mutation would race in a way
// -race catches only when the runs happen to overlap. Ranging is the only
// supported access; nothing hands the slice out, so no defensive copy is needed
// (contrast dispatcherBaseTools, which does hand its slice to callers).
//
// TestPoolRevokeArms_PinLaunchPostureAndUpdateByName pins every row BY NAME, in
// both directions, so a fourth arm added here fails loudly until somebody pins
// its posture deliberately.
var poolRevokeArms = []poolRevokeArm{
	{name: "revoke", launchYOLO: true, takesSettingsUpdate: true},
	{name: "control_default", launchYOLO: false},
	{name: "control_bypass", launchYOLO: true},
}

// --- the seed's stored posture -------------------------------------------------

// The four on-disk key names, as LITERALS. They are deliberately not read off
// revokeSeedEntry's struct tags: a decode through the struct that wrote the file
// round-trips its own misspelling, and a tag that drifts from registryEntry's is
// exactly the failure this test exists to catch.
const (
	seedKeySessions  = "sessions"
	seedKeyBootstrap = "bootstrap"
	seedKeyYOLO      = "yolo"
	seedKeyID        = "id"
)

// TestSeedBypassRegistry_StoresRequestedPostureUnderBothValues is #1651's AC 2:
// for BOTH postures the seeded file decodes to exactly one bootstrap entry whose
// yolo key is present and carries the requested value, under an id ValidID
// accepts and equal to the one the seed returned.
//
// What it deliberately does NOT assert: the file's bytes (created_at and
// last_active_at come from time.Now()), and `version` — no production reader
// consults it, loadRegistry unmarshals and returns without inspecting it, and the
// only .Version read in internal/sessions belongs to a unit test. The seed keeps
// writing version 1 for shape-consistency with saveRegistryLocked; nothing here
// would catch its removal, and claiming otherwise would be claiming coverage this
// test cannot have.
func TestSeedBypassRegistry_StoresRequestedPostureUnderBothValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yolo bool
	}{
		{name: "the bypass posture", yolo: true},
		{name: "the stored default posture", yolo: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// INSIDE the subtest, never hoisted out of the loop: two parallel
			// subtests sharing one directory would both write sessions.json to
			// the same path, racing on the file and yielding an intermittently
			// wrong yolo — the exact class of silent green this test exists to
			// prevent.
			path := filepath.Join(t.TempDir(), "sessions.json")
			seeded := seedBypassRegistry(t, path, tc.yolo)

			// 0600 is only meaningful on a path that did not already exist —
			// os.WriteFile keeps an existing file's mode. t.TempDir() guarantees
			// that here; seedBypassRegistry's doc comment carries the constraint
			// for every other caller.
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat the seeded registry: %v; the seed reported success, so a "+
					"missing file means it wrote somewhere else entirely", err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("the seeded registry is mode %04o, want %04o — the shape "+
					"saveRegistryLocked writes. A registry readable beyond its owner "+
					"exposes the operator's session ids", perm, 0o600)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read the seeded registry: %v", err)
			}

			// CONTRACT, not implementation: the on-disk key names are the literal
			// subject of every assertion below. A struct-based decode — even one
			// declared locally — cannot express the presence clause without a
			// *bool, and a *bool still asks the reader to notice why it is a
			// pointer.
			var file map[string]json.RawMessage
			if err := json.Unmarshal(data, &file); err != nil {
				t.Fatalf("the seeded registry is not a JSON object: %v; loadRegistry would "+
					"fail the whole parse and sessions.New would error before any child "+
					"spawned", err)
			}
			rawSessions, present := file[seedKeySessions]
			if !present {
				t.Fatalf("the seeded registry carries no %q key (keys: %q); pickBootstrap has "+
					"nothing to select and every arm launches from a cold start",
					seedKeySessions, seedSortedKeys(file))
			}
			var entries []map[string]json.RawMessage
			if err := json.Unmarshal(rawSessions, &entries); err != nil {
				t.Fatalf("the %q value is not an array of objects: %v", seedKeySessions, err)
			}
			if len(entries) != 1 {
				t.Fatalf("the seeded registry carries %d entries, want exactly 1; pickBootstrap "+
					"returns the FIRST bootstrap entry, so a second one silently decides the "+
					"arm's launch posture", len(entries))
			}
			entry := entries[0]

			// bootstrap — without it pickBootstrap returns nil, Pool.New takes the
			// cold-start path, and the arm launches from nothing. On the false row
			// that failure is INVISIBLE at the Pool: a cold start reads back the
			// same YOLO false the row wanted.
			rawBootstrap, present := entry[seedKeyBootstrap]
			if !present {
				t.Errorf("the seeded entry carries no %q key (keys: %q); pickBootstrap selects on "+
					"exactly this key, so the entry is invisible and the arm launches from a "+
					"cold start", seedKeyBootstrap, seedSortedKeys(entry))
			} else {
				var bootstrap bool
				if err := json.Unmarshal(rawBootstrap, &bootstrap); err != nil {
					t.Errorf("the seeded entry's %q is not a bool (%s): %v", seedKeyBootstrap, rawBootstrap, err)
				} else if !bootstrap {
					t.Errorf("the seeded entry's %q is false, want true; pickBootstrap returns nil, "+
						"Pool.New takes the cold-start path, and the arm's stored posture never "+
						"reaches the child", seedKeyBootstrap)
				}
			}

			// yolo — PRESENCE first, value second, and they are separate
			// assertions on purpose. This presence clause is the sole red for an
			// omitempty added to revokeSeedEntry.YOLO: on the false row the
			// vanished key decodes to exactly the value that row wanted, so a
			// value-only check passes over a file that says nothing.
			rawYOLO, present := entry[seedKeyYOLO]
			if !present {
				t.Errorf("the seeded entry carries no %q key (keys: %q). revokeSeedEntry.YOLO is "+
					"tagged WITHOUT omitempty for exactly this reason: a false posture must be a "+
					"PRESENT false, because an absent key decodes to false too and the file then "+
					"cannot say which posture was asked for", seedKeyYOLO, seedSortedKeys(entry))
			} else {
				var yolo bool
				if err := json.Unmarshal(rawYOLO, &yolo); err != nil {
					t.Errorf("the seeded entry's %q is not a bool (%s): %v; loadRegistry fails the "+
						"whole parse closed on a malformed yolo and sessions.New errors",
						seedKeyYOLO, rawYOLO, err)
				} else if yolo != tc.yolo {
					t.Errorf("the seeded entry's %q is %t, want %t; the arm would launch under the "+
						"OPPOSITE posture — with %q true that is --dangerously-skip-permissions "+
						"on a live child, and with it false there is no bypass for #1643 to "+
						"revoke", seedKeyYOLO, yolo, tc.yolo, seedKeyYOLO)
				}
			}

			// id — writeMCPSettings hard-errors on anything ValidID rejects, and
			// claude receives it as --session-id.
			rawID, present := entry[seedKeyID]
			if !present {
				t.Errorf("the seeded entry carries no %q key (keys: %q); the warm-start path has "+
					"no session id to hand claude", seedKeyID, seedSortedKeys(entry))
				return
			}
			var id string
			if err := json.Unmarshal(rawID, &id); err != nil {
				t.Errorf("the seeded entry's %q is not a string (%s): %v", seedKeyID, rawID, err)
				return
			}
			if !sessions.ValidID(id) {
				t.Errorf("the seeded entry's %q is %q, which sessions.ValidID rejects; "+
					"writeMCPSettings hard-errors on anything that is not a canonical UUIDv4, "+
					"so the pool never reaches a spawn", seedKeyID, id)
			}
			if id != string(seeded) {
				t.Errorf("the seed wrote id %q and returned %q; nothing else in this repo would "+
					"catch the divergence, and #1622's failure diagnostics print the RETURNED "+
					"value, so a reader would be handed an id no file carries", id, seeded)
			}
		})
	}
}

// seedSortedKeys returns the object's key names for a failure message. A
// misspelled key is the failure this whole test guards, so the key set the file
// DOES carry is what makes a red readable at a glance. Sorted because Go
// randomizes map iteration and a message that reorders between runs is a message
// nobody trusts.
func seedSortedKeys(obj map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- the arm table's rows ------------------------------------------------------

// poolRevokeArmWant is one row of this test's OWN expectation table, declared
// independently of poolRevokeArms so the two can be swept against each other.
// Each row carries the consequence its own mismatch has for #1643, because "want
// true, got false" is useless here and the consequence is what saves the next run.
type poolRevokeArmWant struct {
	launchYOLO          bool
	takesSettingsUpdate bool
	postureConsequence  string
	updateConsequence   string
}

// TestPoolRevokeArms_PinLaunchPostureAndUpdateByName is #1651's AC 3: every arm's
// stored launch posture and settings-update participation, pinned BY NAME.
//
// By name rather than "the two controls differ", because "they differ" survives a
// straight swap — control_default would launch in bypass, control_bypass in
// default, and every consumer that reads the names would be lied to.
//
// The sweep runs BOTH ways: a missing row and an unpinned extra row both fail
// here. The extra-row direction is deliberate and is the opposite of #1652's name
// test, whose job is to keep working as the table grows; this test's job is to
// make growth a decision somebody makes on purpose.
func TestPoolRevokeArms_PinLaunchPostureAndUpdateByName(t *testing.T) {
	t.Parallel()

	want := map[string]poolRevokeArmWant{
		"revoke": {
			launchYOLO:          true,
			takesSettingsUpdate: true,
			postureConsequence: "seeded WITHOUT bypass there is nothing to revoke: the stored posture " +
				"already equals the update, so Pool.UpdateSettings returns at its no-change check " +
				"having delivered nothing and reported success — no RevokeBypass is issued, no " +
				"delivery is attempted, and the arm measures nothing while staying green",
			updateConsequence: "the measurement arm must take the settings update; without it no " +
				"revocation is issued at all and the run compares three controls",
		},
		"control_default": {
			launchYOLO: false,
			postureConsequence: "a stored true becomes --dangerously-skip-permissions through " +
				"claudeSettingsArgs on a REAL claude child, and #1643's probe deliberately provokes " +
				"a tool call — so this is a privilege fault and not only a measurement one. The two " +
				"controls would also compose the same argv, leaving the comparison nothing to " +
				"discriminate with, and the arm NAME would lie to every consumer that reads it",
			updateConsequence: "a control that takes a settings update is not a control",
		},
		"control_bypass": {
			launchYOLO: true,
			postureConsequence: "the two control arms would compose the same argv — claudeSettingsArgs " +
				"emits nothing at all for false — so #1643's comparison has nothing to discriminate " +
				"with",
			updateConsequence: "a control that takes a settings update is not a control",
		},
	}

	got := make(map[string]poolRevokeArm, len(poolRevokeArms))
	names := make([]string, 0, len(poolRevokeArms))
	updates := 0
	for _, arm := range poolRevokeArms {
		// A duplicate name makes lookup-by-name ambiguous and could satisfy a
		// per-name check below while the table is wrong.
		if _, dup := got[arm.name]; dup {
			t.Errorf("poolRevokeArms carries more than one %q row (all: %q); lookup by name is "+
				"then ambiguous, and #1652 and #1643 both address these rows by name",
				arm.name, names)
		}
		got[arm.name] = arm
		names = append(names, arm.name)
		if arm.takesSettingsUpdate {
			updates++
		}
	}

	// The family guard, and it survives the table growing: an added arm that
	// should be a control fails here as well as on the extra-row sweep below.
	if updates != 1 {
		t.Errorf("%d of poolRevokeArms' %d rows take a settings update, want exactly 1; #1643 "+
			"revokes on ONE arm and compares it against controls that were never updated",
			updates, len(poolRevokeArms))
	}

	// Every row in the table must be pinned here. A fourth arm added later fails
	// until somebody states its posture deliberately.
	for _, arm := range poolRevokeArms {
		if _, pinned := want[arm.name]; !pinned {
			t.Errorf("poolRevokeArms carries a %q row that this test does not pin; its launch "+
				"posture and update participation would then be whatever somebody typed, and "+
				"#1643 would launch a child off an unreviewed posture", arm.name)
		}
	}

	wantNames := make([]string, 0, len(want))
	for name := range want {
		wantNames = append(wantNames, name)
	}
	sort.Strings(wantNames)

	for _, name := range wantNames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			arm, ok := got[name]
			if !ok {
				t.Fatalf("poolRevokeArms carries no %q row; it carries %q. #1652's name test and "+
					"#1643's driver both address the arms by these exact strings", name, names)
			}
			exp := want[name]
			if arm.launchYOLO != exp.launchYOLO {
				t.Errorf("arm %q is seeded with launchYOLO %t, want %t: %s",
					name, arm.launchYOLO, exp.launchYOLO, exp.postureConsequence)
			}
			if arm.takesSettingsUpdate != exp.takesSettingsUpdate {
				t.Errorf("arm %q has takesSettingsUpdate %t, want %t: %s",
					name, arm.takesSettingsUpdate, exp.takesSettingsUpdate, exp.updateConsequence)
			}
		})
	}
}
