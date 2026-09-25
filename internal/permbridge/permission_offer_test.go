package permbridge

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// This suggestion list is the checked, path-redacted source from the failing
// live session-approval proof on Claude 2.1.259, captured on 2026-09-11.
func TestAlwaysAllow_LiveMixedOffer(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/permission_suggestions_v2.1.259.json")
	if err != nil {
		t.Fatal(err)
	}
	offer, status := parseAlwaysAllow(raw, false)
	if !offer.Offered() || status != "offered" {
		t.Fatalf("live command rule unavailable: %s", status)
	}
	if got, want := offer.Rules(), []string{"Bash(touch pyrycode-always-allow-witness.txt)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("display rules = %v, want %v", got, want)
	}
	want := `[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"touch pyrycode-always-allow-witness.txt"}],"behavior":"allow","destination":"session"}]`
	if got := string(AllowAlways(nil, offer).UpdatedPermissions); got != want {
		t.Fatalf("session grant = %s, want %s", got, want)
	}
}

func TestAlwaysAllow_MixedOfferValidation(t *testing.T) {
	t.Parallel()
	const rule = `{"type":"addRules","behavior":"allow","rules":[{"toolName":"Bash","ruleContent":"touch witness"}]}`
	const directory = `{"type":"addDirectories","directories":["/test"],"destination":"session"}`
	const mode = `{"type":"setMode","mode":"acceptEdits","destination":"session"}`
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"alternatives only", `[` + directory + `,` + mode + `]`},
		{"unknown after valid rule", `[` + rule + `,` + directory + `,{"type":"futureUpdate"}]`},
		{"deny after valid rule", `[` + rule + `,` + mode + `,{"type":"addRules","behavior":"deny","rules":[{"toolName":"Bash"}]}]`},
		{"malformed rule after valid rule", `[` + rule + `,` + directory + `,{"type":"addRules","behavior":"allow","rules":[{"toolName":"Read","ruleContent":null}]}]`},
		{"missing type after valid rule", `[` + rule + `,` + mode + `,{}]`},
		{"over rule bound", `[` + directory + `,` + string(permissionSuggestionBatch(17, "Bash"))[1:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			offer := ParseAlwaysAllow(json.RawMessage(tc.raw), false)
			if offer.Offered() || offer.Rules() != nil || offer.Updates() != nil || AllowAlways(nil, offer).UpdatedPermissions != nil {
				t.Fatal("invalid mixed suggestions produced a partial offer or grant")
			}
		})
	}

	raw := json.RawMessage(`[` + mode + `,` + rule + `,` + directory + `,{"type":"addRules","behavior":"allow","rules":[{"toolName":"Read"}]}]`)
	offer := ParseAlwaysAllow(raw, false)
	if got, want := offer.Rules(), []string{"Bash(touch witness)", "Read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("interleaved display rules = %v, want %v", got, want)
	}
	want := `[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"touch witness"}],"behavior":"allow","destination":"session"},{"type":"addRules","rules":[{"toolName":"Read"}],"behavior":"allow","destination":"session"}]`
	if got := string(AllowAlways(nil, offer).UpdatedPermissions); got != want {
		t.Fatalf("interleaved grant = %s, want %s", got, want)
	}
	if ParseAlwaysAllow(raw, true).Offered() {
		t.Fatal("suppressed mixed suggestions were offered")
	}
}

func TestSessionGrant(t *testing.T) {
	t.Parallel()
	offer := SessionGrant("touch witness")
	if !offer.Offered() {
		t.Fatal("session grant not offered")
	}
	if got, want := offer.Rules(), []string{"touch witness"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
	if offer.Updates() != nil {
		t.Fatalf("session grant carries Claude updates: %v", offer.Updates())
	}
	for _, label := range []string{"", string(make([]byte, maxRenderedRuleBytes+1))} {
		if SessionGrant(label).Offered() {
			t.Errorf("label of %d bytes offered a grant whose scope cannot be shown", len(label))
		}
	}
}

// TestAllowAlways_SessionGrantKeepsAllowBytes: a session grant marks the
// verdict for the harness without changing the bytes Claude would receive.
func TestAllowAlways_SessionGrantKeepsAllowBytes(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"command":"ls"}`)
	v := AllowAlways(input, SessionGrant("ls"))
	if !v.ForSession || v.Behavior != BehaviorAllow {
		t.Fatalf("verdict = %+v, want a session allow", v)
	}
	got, _ := json.Marshal(v)
	want, _ := json.Marshal(Allow(input))
	if string(got) != string(want) {
		t.Fatalf("session allow bytes = %s, want %s", got, want)
	}
	if AllowAlways(input, AlwaysAllow{}).ForSession || Allow(input).ForSession {
		t.Fatal("a plain allow is marked for the session")
	}
	claude := ParseAlwaysAllow(json.RawMessage(`[{"type":"addRules","behavior":"allow","rules":[{"toolName":"Bash"}]}]`), false)
	if AllowAlways(input, claude).ForSession {
		t.Fatal("a Claude rule grant is marked as a harness session grant")
	}
}
