package streamsup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestMCPStatusEligible(t *testing.T) {
	t.Parallel()

	const daemonConfig = "/run/pyry/mcp.json"
	tests := []struct {
		name       string
		args       []string
		configPath string
		want       bool
	}{
		{
			name:       "daemon config and strict flag",
			args:       []string{"--mcp-config", daemonConfig, "--strict-mcp-config"},
			configPath: daemonConfig,
			want:       true,
		},
		{
			name:       "joined daemon config and strict flag",
			args:       []string{"--mcp-config=" + daemonConfig, "--strict-mcp-config"},
			configPath: daemonConfig,
			want:       true,
		},
		{
			name:       "missing strict flag",
			args:       []string{"--mcp-config", daemonConfig},
			configPath: daemonConfig,
		},
		{
			name:       "missing config flag",
			args:       []string{"--strict-mcp-config"},
			configPath: daemonConfig,
		},
		{
			name:       "wrong config path",
			args:       []string{"--mcp-config", "/home/operator/private.json", "--strict-mcp-config"},
			configPath: daemonConfig,
		},
		{
			name:       "dangling config flag",
			args:       []string{"--mcp-config", "--strict-mcp-config"},
			configPath: daemonConfig,
		},
		{
			name:       "empty daemon path",
			args:       []string{"--mcp-config", daemonConfig, "--strict-mcp-config"},
			configPath: "",
		},
		{
			name: "second private config fails closed",
			args: []string{
				"--mcp-config", daemonConfig,
				"--mcp-config", "/home/operator/private.json",
				"--strict-mcp-config",
			},
			configPath: daemonConfig,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mcpStatusEligible(tc.args, tc.configPath); got != tc.want {
				t.Errorf("mcpStatusEligible(%q, %q) = %v, want %v", tc.args, tc.configPath, got, tc.want)
			}
		})
	}
}

func TestParser_MCPStatusPolicyRequestsOncePerEligibleChild(t *testing.T) {
	t.Parallel()

	var events []turnevent.Event
	requests := 0
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
	p.beginMCPStatusChild(true, func() error {
		requests++
		return nil
	})
	if requests != 0 {
		t.Fatalf("requests before inventory = %d, want 0", requests)
	}

	writePolicyLine(t, p, initializeLineFixture(t, "success", map[string]any{
		"models":   []any{policyModel("first-model")},
		"commands": []any{commandEntryFixture("first-command")},
	}))
	if requests != 1 {
		t.Fatalf("requests after one reply with both inventories = %d, want 1", requests)
	}
	if len(events) != 2 {
		t.Fatalf("events after combined inventory = %d, want ModelList + SlashCommandList: %#v", len(events), events)
	}
	if _, ok := events[0].(turnevent.ModelList); !ok {
		t.Errorf("event[0] = %T, want ModelList", events[0])
	}
	if _, ok := events[1].(turnevent.SlashCommandList); !ok {
		t.Errorf("event[1] = %T, want SlashCommandList", events[1])
	}

	writePolicyLine(t, p, initializeLineFixture(t, "success", map[string]any{
		"models": []any{policyModel("later-turn-model")},
	}))
	if requests != 1 {
		t.Errorf("requests after later inventory on same child = %d, want 1", requests)
	}

	p.beginMCPStatusChild(true, func() error {
		requests++
		return nil
	})
	writePolicyLine(t, p, initializeLineFixture(t, "success", map[string]any{
		"commands": []any{commandEntryFixture("replacement-command")},
	}))
	if requests != 2 {
		t.Errorf("requests after replacement child inventory = %d, want 2", requests)
	}
}

func TestParser_MCPStatusPolicyFiltersOnlyIneligibleStatus(t *testing.T) {
	t.Parallel()

	var got []turnevent.Event
	requests := 0
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
	p.beginMCPStatusChild(false, func() error {
		requests++
		return nil
	})

	writePolicyLine(t, p, initializeLineFixture(t, "success", map[string]any{
		"models":   []any{policyModel("private-model")},
		"commands": []any{commandEntryFixture("private-command")},
	}))
	writePolicyLine(t, p, mcpStatusLineFixture(t, "success", []any{
		map[string]any{"name": "private-server", "status": "connected"},
	}))
	writePolicyLine(t, p, `{"type":"assistant","message":{"id":"ordinary","role":"assistant","content":[{"type":"text","text":"unchanged"}]}}`)

	if requests != 0 {
		t.Errorf("ineligible child requests = %d, want 0", requests)
	}
	if len(got) != 3 {
		t.Fatalf("sink received %d events, want the two inventories and ordinary text only: %#v", len(got), got)
	}
	if _, ok := got[0].(turnevent.ModelList); !ok {
		t.Errorf("event[0] = %T, want ModelList", got[0])
	}
	if _, ok := got[1].(turnevent.SlashCommandList); !ok {
		t.Errorf("event[1] = %T, want SlashCommandList", got[1])
	}
	wantText := turnevent.TextChunk{MessageID: "ordinary", Text: "unchanged"}
	if !reflect.DeepEqual(got[2], wantText) {
		t.Errorf("ordinary event changed: got %#v, want %#v", got[2], wantText)
	}

	got = nil
	p.beginMCPStatusChild(true, func() error {
		requests++
		return nil
	})
	writePolicyLine(t, p, mcpStatusLineFixture(t, "success", []any{}))
	if len(got) != 1 {
		t.Fatalf("eligible sink received %d events, want one empty MCPStatus: %#v", len(got), got)
	}
	if status, ok := got[0].(turnevent.MCPStatus); !ok || status.Servers == nil {
		t.Errorf("eligible event = %#v, want MCPStatus with non-nil empty Servers", got[0])
	}
	if requests != 0 {
		t.Errorf("status reply itself triggered %d requests, want 0", requests)
	}
}

func TestParser_MCPStatusPolicyDeliveryFailureIsContentFreeAndNotRetried(t *testing.T) {
	t.Parallel()

	const secret = "private-request-or-response-content"
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	forwarded := 0
	requests := 0
	p := NewParser(func(turnevent.Event) { forwarded++ }, logger)
	p.beginMCPStatusChild(true, func() error {
		requests++
		return errors.New(secret)
	})

	for _, model := range []string{"first-secret-model", "second-secret-model"} {
		writePolicyLine(t, p, initializeLineFixture(t, "success", map[string]any{
			"models": []any{policyModel(model)},
		}))
	}
	if requests != 1 {
		t.Errorf("failed delivery attempts = %d, want exactly 1", requests)
	}
	if forwarded != 2 {
		t.Errorf("forwarded inventories = %d, want 2 despite request failure", forwarded)
	}
	logText := logs.String()
	if !strings.Contains(logText, "mcp_status.request.write_err") {
		t.Errorf("log missing fixed delivery-failure event: %q", logText)
	}
	for _, forbidden := range []string{secret, "first-secret-model", "second-secret-model"} {
		if strings.Contains(logText, forbidden) {
			t.Errorf("log contains protected content %q: %q", forbidden, logText)
		}
	}
}

func TestRunner_MCPStatusPolicyUsesEachSpawnArgs(t *testing.T) {
	t.Parallel()

	const daemonConfig = "/run/pyry/mcp.json"
	eligibleArgs := []string{"--mcp-config", daemonConfig, "--strict-mcp-config"}
	events := make(chan turnevent.Event, 32)
	parser := NewParser(func(ev turnevent.Event) { events <- ev }, discardLogger())
	stderr := &safeBuffer{}
	cfg := Config{
		ClaudeBin:                os.Args[0],
		WorkDir:                  t.TempDir(),
		SessionID:                testSessionID,
		Args:                     eligibleArgs,
		Stdout:                   parser,
		Stderr:                   stderr,
		Env:                      []string{"GO_STREAMSUP_HELPER=1", "GO_STREAMSUP_HELPER_MODE=mcp_status_policy"},
		Logger:                   discardLogger(),
		RequestInitializeOnSpawn: true,
		MCPStatusConfigPath:      daemonConfig,
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); _ = join() }()

	waitForPolicyInventories(t, events)
	waitForPolicyStatus(t, events)
	waitForContains(t, stderr, "MCP_STATUS_REQUEST", 3*time.Second)
	if got := strings.Count(stderr.String(), "MCP_STATUS_REQUEST"); got != 1 {
		t.Fatalf("first eligible child status requests = %d, want 1", got)
	}

	// A next-spawn argv change cannot revoke the already-running child's
	// eligibility. Its manually requested status still reaches the sink.
	r.SetSpawnArgs(nil)
	if err := r.RequestMCPStatus(); err != nil {
		t.Fatalf("RequestMCPStatus on current eligible child: %v", err)
	}
	waitForPolicyStatus(t, events)
	waitForContainsCount(t, stderr, "MCP_STATUS_REQUEST", 2)

	// Restart onto the installed bypass argv. Its initialize inventories still
	// pass, but no automatic status request follows them.
	r.Restart(nil)
	waitForPolicyInventories(t, events)
	if err := r.WriteUserTurn(context.Background(), "c1", []byte("barrier")); err != nil {
		t.Fatalf("WriteUserTurn barrier on ineligible child: %v", err)
	}
	if statuses := waitForPolicyTurnEnd(t, events); statuses != 0 {
		t.Fatalf("ineligible child forwarded %d automatic statuses before barrier, want 0", statuses)
	}
	if got := strings.Count(stderr.String(), "MCP_STATUS_REQUEST"); got != 2 {
		t.Fatalf("ineligible child changed request count to %d, want 2", got)
	}
	if status, ok := r.QueryMCPStatus(context.Background()); ok {
		t.Fatalf("QueryMCPStatus on ineligible child = (%+v,true), want unavailable", status)
	}
	if got := strings.Count(stderr.String(), "MCP_STATUS_REQUEST"); got != 2 {
		t.Fatalf("ineligible on-demand query changed request count to %d, want 2", got)
	}

	// Even a manually delivered reply is suppressed for the ineligible child.
	if err := r.RequestMCPStatus(); err != nil {
		t.Fatalf("manual RequestMCPStatus on ineligible child: %v", err)
	}
	if err := r.WriteUserTurn(context.Background(), "c1", []byte("second-barrier")); err != nil {
		t.Fatalf("second WriteUserTurn barrier: %v", err)
	}
	if statuses := waitForPolicyTurnEnd(t, events); statuses != 0 {
		t.Fatalf("ineligible child forwarded %d manually solicited statuses, want 0", statuses)
	}
	waitForContainsCount(t, stderr, "MCP_STATUS_REQUEST", 3)

	// A later eligible replacement gets a fresh once-per-child request.
	r.Restart(eligibleArgs)
	waitForPolicyInventories(t, events)
	waitForPolicyStatus(t, events)
	waitForContainsCount(t, stderr, "MCP_STATUS_REQUEST", 4)
}

func policyModel(name string) map[string]any {
	return map[string]any{
		"resolvedModel": name + "-resolved",
		"value":         name,
		"displayName":   name + " display",
	}
}

func writePolicyLine(t *testing.T, p *Parser, line string) {
	t.Helper()
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Parser.Write: %v", err)
	}
}

func waitForPolicyInventories(t *testing.T, events <-chan turnevent.Event) {
	t.Helper()
	models, commands := false, false
	deadline := time.After(5 * time.Second)
	for !models || !commands {
		select {
		case ev := <-events:
			switch ev.(type) {
			case turnevent.ModelList:
				models = true
			case turnevent.SlashCommandList:
				commands = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for inventories; models=%v commands=%v", models, commands)
		}
	}
}

func waitForPolicyStatus(t *testing.T, events <-chan turnevent.Event) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if _, ok := ev.(turnevent.MCPStatus); ok {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for MCPStatus")
		}
	}
}

func waitForPolicyTurnEnd(t *testing.T, events <-chan turnevent.Event) int {
	t.Helper()
	statuses := 0
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			switch ev.(type) {
			case turnevent.MCPStatus:
				statuses++
			case turnevent.TurnEnd:
				return statuses
			}
		case <-deadline:
			t.Fatal("timed out waiting for result barrier")
			return statuses
		}
	}
}

func waitForContainsCount(t *testing.T, b *safeBuffer, marker string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(b.String(), marker) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d copies of %q; got:\n%s", want, marker, b.String())
}
