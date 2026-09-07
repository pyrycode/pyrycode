//go:build e2e_realclaude

package realclaude

// #2093 AC #4 — the live half. The unit tests in internal/sessions pin the
// constant byte for byte and prove the flag reaches both argv composition sites
// against a recording runner. Neither of them can answer the one question that
// needs a real claude: does claude ACCEPT --append-system-prompt-file beside the
// interactive stream-json argv, and does a turn still complete?
//
// The flag is already proven in this repo on the agent-run path
// (streamrunner.BuildArgs passes it next to --input-format stream-json), which is
// why the risk is low rather than nil — the interactive path composes a different
// argv, and "low risk" is not evidence.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -v \
//	  -run TestInteractiveSystemPromptFile_LiveSpawnArgv ./internal/e2e/realclaude/
//
// It executes on any machine with claude credentials — the only skips are the
// package's standard absent-binary / absent-credentials guards. This package is
// behind the e2e_realclaude build tag, so `make check` never compiles it: read
// the count of executed tests, never the exit code. A suite that skips every test
// exits 0, and so does one whose package failed to build.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sysPromptFlag is the flag under test, as it appears in the daemon's own
// "spawning claude" record.
const sysPromptFlag = "--append-system-prompt-file"

// sysPromptSpawnRecords returns the daemon's "spawning claude" log lines.
//
// The daemon logs at Info through a slog TextHandler, rendering the argv as
// args="[... ]", so the whole composed argv is on one line. Reading production's
// own record rather than transcribing an expected argv into this file is what
// keeps the assertion from drifting away from the shape it claims to measure —
// the same reason dropcapArgvHandler observes rather than transcribes.
func sysPromptSpawnRecords(daemonLog string) []string {
	var out []string
	for _, line := range strings.Split(daemonLog, "\n") {
		if strings.Contains(line, `msg="spawning claude"`) {
			out = append(out, line)
		}
	}
	return out
}

// sysPromptArgFromRecord returns the path following sysPromptFlag in one record,
// or "" when the record does not carry the flag. The argv renders space-separated
// inside the bracketed args value, so the path is the next field; the trailing
// bracket and quote are trimmed for the case where it lands last.
func sysPromptArgFromRecord(record string) string {
	fields := strings.Fields(record)
	for i, f := range fields {
		if f == sysPromptFlag && i+1 < len(fields) {
			return strings.TrimRight(fields[i+1], `]"`)
		}
	}
	return ""
}

// TestInteractiveSystemPromptFile_LiveSpawnArgv (AC #4) drives one live
// interactive daemon turn and asserts every claude the pool spawned carried
// --append-system-prompt-file naming a readable daemon-written file.
//
// The turn is the load-bearing half. A claude that rejected the flag would fail
// to come up and the reply would never arrive, so drainForAssistantReply's own
// deadline is the acceptance assertion — the argv reads below only say WHICH
// argv it accepted.
//
// The harness gives both composition sites in one run at no extra cost: the
// seeded bootstrap spawns eagerly at daemon start (Pool.New's site) and the
// conversation's own session spawns on this first message (buildSession's site,
// deferred since #2085). Hence the shape of the assertion — EVERY record carries
// the flag, with a non-zero count guarding against the vacuous "all of nothing".
func TestInteractiveSystemPromptFile_LiveSpawnArgv(t *testing.T) {
	h := startPerConversationHarness(t)
	nonce := time.Now().UnixNano()

	convID := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, 2, nil)

	// Since #2085 this first message is what brings the conversation's claude up.
	sealSendMessage(t, h.phone, h.initSend, 3, convID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d", nonce))
	drainForAssistantReply(t, h.phone, h.initRecv, convID, 1, perTurnReplyBudget)

	records := sysPromptSpawnRecords(h.daemon.stderr.String())
	if len(records) == 0 {
		t.Fatalf("the daemon logged no %q record after a completed live turn; the assertions "+
			"below would be vacuous", "spawning claude")
	}
	t.Logf("#2093: %d spawning-claude record(s) observed", len(records))

	seen := map[string]bool{}
	for i, rec := range records {
		path := sysPromptArgFromRecord(rec)
		if path == "" {
			t.Errorf("AC #4: spawn record %d carries no %s; this claude was spawned reasoning "+
				"about the wrong surface.\nrecord: %s", i, sysPromptFlag, rec)
			continue
		}
		if !filepath.IsAbs(path) {
			t.Errorf("AC #4: spawn record %d names %s %q, want an absolute path", i, sysPromptFlag, path)
			continue
		}
		seen[path] = true
	}

	for path := range seen {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("AC #4: the spawn argv names %q, which is not readable: %v — claude was handed "+
				"a path to nothing", path, err)
			continue
		}
		if len(raw) == 0 {
			t.Errorf("AC #4: the file at %q is empty; the appended prompt says nothing", path)
		}
	}

	// Since #2150 the bootstrap keeps the daemon-scoped file while every
	// conversation's session gets its own, because the appended text is no longer
	// identical for every session: it carries that conversation's operator prompt.
	// This assertion was "the spawns name ONE file" under #2093 and is inverted
	// here — a shared file would mean one conversation's prompt reaching every
	// other session. TestPool_MintedSpawn_UsesPerSessionSystemPromptFile makes the
	// claim deterministically; asserting it live would catch a wiring that only
	// production composes.
	if len(records) > 1 && len(seen) < 2 {
		t.Errorf("AC #4: %d spawns name %d distinct prompt file(s) (%v); the bootstrap and a "+
			"conversation's session must not share one (#2150)", len(records), len(seen), seen)
	}
}
