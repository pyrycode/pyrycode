package sessions

import (
	"reflect"
	"slices"
	"testing"
)

// alwaysOnPosture is the argv suffix claudeSettingsArgs composes for a stored
// posture that is NOT the escalation (#2065): the unconditional flag, then the
// mode named beside it. escalatedPosture is the suffix for a stored
// bypassPermissions — the flag alone, no mode pair.
//
// Both are spelled as literals and deliberately NOT built by calling
// claudeSettingsArgs: a helper that re-derived production's output would make
// every argv assertion across this package's pool tests vacuous. They exist so
// that a future change to the suffix reddens once here rather than being
// hand-applied to twenty `want` slices, and so each of those slices still reads
// as a claim about a posture rather than as three opaque tokens.
func alwaysOnPosture(mode string) []string {
	return []string{"--dangerously-skip-permissions", "--permission-mode", mode}
}

func escalatedPosture() []string { return []string{"--dangerously-skip-permissions"} }

// assertDowngraded fails unless argv is the shape a NON-escalated stored posture
// composes: the unconditional flag plus a --permission-mode naming an in-band
// mode, which is what the daemon writes the child back to before any turn reaches
// it.
//
// It replaces the pre-#2065 "the flag must be absent" check at every site that
// defended a revocation. The flag's absence stopped being the spelling of "not
// escalated" the moment claudeSettingsArgs began appending it unconditionally, so
// a site still checking for absence would have to be deleted or inverted — and
// inverting it to "the flag is present" asserts nothing, since it always is. The
// mode pair beside it is the signal, and its ABSENCE is what an escalated session
// looks like.
func assertDowngraded(t *testing.T, label string, argv []string) {
	t.Helper()
	if !slices.Contains(argv, "--dangerously-skip-permissions") {
		t.Errorf("%s = %v, want the unconditional escalation flag on every argv (#2065)", label, argv)
	}
	i := slices.Index(argv, "--permission-mode")
	if i < 0 || i+1 >= len(argv) {
		t.Errorf("%s = %v names no permission mode, which is the ESCALATED shape: this session "+
			"would launch in bypass and never be walked back", label, argv)
		return
	}
	if !permissionModeInBand(argv[i+1]) {
		t.Errorf("%s = %v names permission mode %q, which is not one the daemon can deliver "+
			"in-band, so the child stays in the bypass it launched with", label, argv, argv[i+1])
	}
}

// TestAlwaysOnPostureHelpersMatchProduction is what stops the two helpers above
// from drifting into fiction: they are literals, so nothing else ties them to
// what claudeSettingsArgs actually emits. Both directions are checked, on a
// posture-only settings value so the suffix is the whole output.
func TestAlwaysOnPostureHelpersMatchProduction(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{permissionModeDefault, "plan", "acceptEdits", "auto", "dontAsk"} {
		got := claudeSettingsArgs(SessionSettings{PermissionMode: mode})
		if !reflect.DeepEqual(got, alwaysOnPosture(mode)) {
			t.Errorf("claudeSettingsArgs(mode=%q) = %v, want alwaysOnPosture(%q) = %v", mode, got, mode, alwaysOnPosture(mode))
		}
	}
	got := claudeSettingsArgs(SessionSettings{YOLO: true, PermissionMode: permissionModeBypass})
	if !reflect.DeepEqual(got, escalatedPosture()) {
		t.Errorf("claudeSettingsArgs(escalated) = %v, want escalatedPosture() = %v", got, escalatedPosture())
	}
}

// TestClaudeSettingsArgs is the primary AC 1/AC 2 assertion at the logic layer:
// model/effort map to their flags, the escalation flag is now UNCONDITIONAL, and
// a non-bypass posture composes --permission-mode <mode> BESIDE it rather than
// instead of it (#2065).
//
// The mutual exclusion #2043 enforced with a switch is gone, but the property it
// existed for is not, and it is asserted per row below the table: the escalation
// keeps exactly one spelling, so no stored mode is ever emitted as
// --permission-mode bypassPermissions and the flag is never derived from a mode
// string. TestClaudeSettingsArgs_NoModeComposesTheEscalation sweeps the whole
// storable vocabulary for the same property.
func TestClaudeSettingsArgs(t *testing.T) {
	t.Parallel()
	const bypass = "--dangerously-skip-permissions"
	tests := []struct {
		name string
		in   SessionSettings
		want []string
	}{
		// The zero value no longer returns nil, and that is the ticket: every child
		// the daemon spawns launches in bypass and is walked back in-band, so an
		// argv that says nothing about the posture no longer means "default" — it
		// means whatever the unconditional flag says. A hand-built zero literal
		// composes no mode pair because "" is not an in-band member; a Pool-held
		// session cannot reach that state (canonicalSettings runs at both
		// construction sites), which TestRunnerConfigPermissionModeIsAlwaysKnown
		// pins.
		{"zero value is the bare escalation", SessionSettings{}, []string{bypass}},
		{"model only", SessionSettings{Model: "sonnet"}, []string{"--model", "sonnet", bypass}},
		{"effort only", SessionSettings{Effort: "high"}, []string{"--effort", "high", bypass}},
		{"model and effort", SessionSettings{Model: "opus", Effort: "low"}, []string{"--model", "opus", "--effort", "low", bypass}},
		// #2447 AC 2: the --model VALUE is the family alias, not the stored one.
		// A row claude publishes as an exact id pins the session to a model claude
		// supersedes, so the spawn argv names the family and the stored value is
		// left alone for the menu to keep matching by exact equality. A row
		// published as a bare alias already names its family and composes itself,
		// which is why both directions are rows here: a rewrite that fired on
		// everything would be just as wrong as one that fired on nothing.
		{"a stored exact id composes the family alias", SessionSettings{Model: "claude-fable-5-1[1m]"}, []string{"--model", "fable[1m]", bypass}},
		{"a stored bare alias composes itself", SessionSettings{Model: "haiku"}, []string{"--model", "haiku", bypass}},
		{"yolo appends the escalation and no mode", SessionSettings{YOLO: true}, []string{bypass}},
		{"all three in deterministic order", SessionSettings{Model: "opus", Effort: "max", YOLO: true}, []string{"--model", "opus", "--effort", "max", bypass}},
		// The default posture now NAMES itself. Before #2065 it appended nothing,
		// because default IS claude's own default and silence was the equivalent of
		// an empty Model. Silence no longer reads as default beside an unconditional
		// bypass flag, so the reason for the old special case is gone with it.
		{"default posture names itself beside the flag", SessionSettings{PermissionMode: permissionModeDefault}, []string{bypass, "--permission-mode", permissionModeDefault}},
		{"default posture beside a model", SessionSettings{Model: "opus", PermissionMode: permissionModeDefault}, []string{"--model", "opus", bypass, "--permission-mode", permissionModeDefault}},
		{"in-band mode composes beside the flag", SessionSettings{PermissionMode: "plan"}, []string{bypass, "--permission-mode", "plan"}},
		{"mode follows model and effort", SessionSettings{Model: "opus", Effort: "max", PermissionMode: "dontAsk"}, []string{"--model", "opus", "--effort", "max", bypass, "--permission-mode", "dontAsk"}},
		// The escalation keeps EXACTLY ONE spelling. A stored bypassPermissions is
		// the yolo bit's mode, and emitting it as a --permission-mode value would
		// give the fail-safe a second place to be enforced from.
		{"escalation is the flag alone", SessionSettings{YOLO: true, PermissionMode: permissionModeBypass}, []string{bypass}},
		// Both pairs below are only reachable from a hand-built literal, never from
		// a Pool. Neither composes a mode pair: the escalation is not an in-band
		// member, and the YOLO bit stays the authoritative half of the posture.
		{"a mode cannot compose a bypass child", SessionSettings{PermissionMode: permissionModeBypass}, []string{bypass}},
		{"yolo suppresses a contradicting stored mode", SessionSettings{YOLO: true, PermissionMode: "plan"}, []string{bypass}},
		{"an unrecognised mode composes no pair", SessionSettings{PermissionMode: "notAMode"}, []string{bypass}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := claudeSettingsArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("claudeSettingsArgs(%+v) = %v, want %v", tc.in, got, tc.want)
			}
			// AC 1, per row: the escalation flag is on EVERY composition, whatever
			// the other fields say. This is the assertion that reddens on any tree
			// that kept the posture in the launch argv.
			if !slices.Contains(got, bypass) {
				t.Errorf("claudeSettingsArgs(%+v) = %v, want the escalation flag on every composition", tc.in, got)
			}
			// AC 2, per row: exactly one spelling. The flag appears once, and
			// bypassPermissions never appears as a --permission-mode value.
			if n := slices.Index(got, bypass); n >= 0 && slices.Contains(got[n+1:], bypass) {
				t.Errorf("claudeSettingsArgs(%+v) = %v carries a duplicate escalation flag", tc.in, got)
			}
			if i := slices.Index(got, "--permission-mode"); i >= 0 && got[i+1] == permissionModeBypass {
				t.Errorf("claudeSettingsArgs(%+v) = %v composed the escalation as a mode value", tc.in, got)
			}
		})
	}
}

// TestClaudeSettingsArgs_NoModeComposesTheEscalation is AC 2's vocabulary sweep.
// It runs the whole storable vocabulary — the in-band five plus the escalation —
// plus values no writer can produce, through both YOLO settings, and asserts that
// the second flag can never spell the escalation.
//
// The table above pins the same property row by row; this sweeps rather than
// enumerates, so a mode ADDED to permissionModeInBand later is covered here
// without anyone remembering to add a row. That matters more than it reads: the
// escalation losing its single spelling is silent, and the only loud thing about
// it would be this test.
func TestClaudeSettingsArgs_NoModeComposesTheEscalation(t *testing.T) {
	t.Parallel()
	modes := []string{
		permissionModeDefault, "acceptEdits", "plan", "auto", "dontAsk",
		permissionModeBypass, "", "notAMode", "--dangerously-skip-permissions",
	}
	for _, mode := range modes {
		for _, yolo := range []bool{false, true} {
			got := claudeSettingsArgs(SessionSettings{PermissionMode: mode, YOLO: yolo})
			for i, a := range got {
				if a != "--permission-mode" {
					continue
				}
				if i+1 >= len(got) {
					t.Errorf("claudeSettingsArgs(mode=%q, yolo=%v) = %v ends in a dangling --permission-mode", mode, yolo, got)
					continue
				}
				if !permissionModeInBand(got[i+1]) {
					t.Errorf("claudeSettingsArgs(mode=%q, yolo=%v) = %v composed --permission-mode %q, which is not an in-band member; the escalation must keep exactly one spelling",
						mode, yolo, got, got[i+1])
				}
			}
		}
	}
}

// TestOperatorBypass reads the provenance signal #2065 turns both fail-safes on:
// whether the escalation reached a spawn's argv from the OPERATOR's pass-through
// claude args rather than from the daemon's own settings composition.
//
// It is deliberately a property of the settings-free base (Session.spawnBase),
// not of the assembled argv: after this ticket the assembled argv always carries
// the flag, so reading it there answers "yes" for every session and both
// fail-safes invert.
func TestOperatorBypass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		base []string
		want bool
	}{
		{"nil base", nil, false},
		{"empty base", []string{}, false},
		{"a daemon-composed base", []string{"--session-id", "abc", "--settings", "/tmp/s.json"}, false},
		{"the operator's pass-through", []string{"--dangerously-skip-permissions", "--settings", "/tmp/s.json"}, true},
		{"the pass-through last", []string{"--settings", "/tmp/s.json", "--dangerously-skip-permissions"}, true},
		// The joined form is not a spelling claude accepts for this flag (it takes
		// no value), so there is nothing to match beyond the exact token.
		{"a lookalike token is not the flag", []string{"--dangerously-skip-permissions-not"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := operatorBypass(tc.base); got != tc.want {
				t.Errorf("operatorBypass(%q) = %v, want %v", tc.base, got, tc.want)
			}
		})
	}
}
