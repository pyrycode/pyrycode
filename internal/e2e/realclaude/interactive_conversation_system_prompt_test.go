//go:build e2e_realclaude

package realclaude

// #2150 AC #5 — the live half. The unit tests in internal/sessions prove the
// composed bytes reach the file the spawn argv names, on every path a session is
// built and re-activated by. None of them can answer the question this ticket is
// actually about: does a real claude, spawned by the daemon with a conversation's
// stored prompt appended, BEHAVE differently because of it?
//
// #2093's live test deliberately stops at the argv — it reads the flag back out
// of the daemon's own "spawning claude" record and asserts the file is readable
// and non-empty. That is the right assertion for a constant paragraph whose
// effect is diffuse. It is the wrong one here: a per-conversation prompt whose
// bytes reach a file claude never honoured would pass every argv check ever
// written. So this test asserts the REPLY.
//
// The negative arm is what makes the positive one evidence. A marker token that
// only ever appears in one conversation's reply, when the two conversations are
// driven by the same daemon, the same claude binary and the same user message,
// cannot be explained by anything except the prompt — and a marker that appeared
// in BOTH would mean a per-session file leaking across sessions, which is the
// failure mode the per-session identity exists to prevent.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -v \
//	  -run TestInteractiveConversationSystemPrompt_ReachesTheReply ./internal/e2e/realclaude/
//
// It executes on any machine with claude credentials — the only skips are the
// package's standard absent-binary / absent-credentials guards. This package is
// behind the e2e_realclaude build tag, so `make check` never compiles it: read
// the count of executed tests, never the exit code.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The two conversations and the two sessions they are bound to. The session ids
// are deliberately NOT in the seeded sessions.json: an id the pool lacks is the
// daemon-restart shape, so the first message to each conversation drives
// sessionRouter.revive → Pool.Revive → materialise → buildSession with the
// conversation id as the label. That exercises the second of AC #2's two build
// paths, and it is the only one reachable without a write verb (#2151).
const (
	livePromptedConvID  = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	livePromptedSessID  = "aaaaaaaa-2222-4222-8222-aaaaaaaaaaaa"
	liveBareConvID      = "bbbbbbbb-1111-4111-8111-bbbbbbbbbbbb"
	liveBareSessID      = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	livePromptTurnQuery = "In one short sentence, say hello."
)

// seedPromptedConversations writes conversations.json with the two rows above:
// one carrying a stored system prompt, one carrying none. Hand-written rather
// than reusing seedBoundConversation, which writes a single row and has no
// system_prompt field — and the absent key is exactly the state the control arm
// needs (#2149's contract: an absent key decodes as no prompt).
func seedPromptedConversations(t *testing.T, home, cwd, prompt string) {
	t.Helper()
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[` +
		`{"id":"` + livePromptedConvID + `","cwd":"` + cwd +
		`","current_session_id":"` + livePromptedSessID +
		`","is_promoted":false,"system_prompt":"` + prompt +
		`","last_used_at":"2026-01-01T00:00:00Z"},` +
		`{"id":"` + liveBareConvID + `","cwd":"` + cwd +
		`","current_session_id":"` + liveBareSessID +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}` +
		`]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}
}

// TestInteractiveConversationSystemPrompt_ReachesTheReply (AC #5) drives two
// live turns through one daemon and asserts the marker the stored prompt asks
// for appears in the prompted conversation's reply and in no other.
//
// The marker carries a per-run nonce, so a stale transcript, a resumed session
// or a cached reply cannot produce it. It is a bare token with no dictionary
// word in it, so claude cannot arrive at it by chance and the control arm's
// absence assertion is not a coincidence.
func TestInteractiveConversationSystemPrompt_ReachesTheReply(t *testing.T) {
	marker := fmt.Sprintf("PYRYMARK%d", time.Now().UnixNano()%1000000)
	prompt := "You are in a test harness. Append the token " + marker +
		" on its own line at the end of every reply you write, verbatim and unaltered."

	h := startPerConversationHarnessSeeded(t, func(home, workdir string) []string {
		seedPromptedConversations(t, home, workdir, prompt)
		// No extra pass-through claude arguments (#2320's seam): this case's
		// subject is what the daemon composes for itself, not what an operator
		// added. The spawn argv it asserts on below is unchanged.
		return nil
	})

	// Arm 1 — the conversation whose stored prompt asks for the marker.
	sealSendMessage(t, h.phone, h.initSend, 2, livePromptedConvID, "m-prompted", livePromptTurnQuery)
	prompted := drainForCompletedTurnText(t, h.phone, h.initRecv, livePromptedConvID, perTurnReplyBudget)
	if !strings.Contains(prompted, marker) {
		t.Errorf("AC #5: the reply for the conversation whose stored system prompt asks for %q does not "+
			"carry it — the stored prompt did not reach the spawn.\nreply: %s", marker, prompted)
	}

	// Arm 2 — same daemon, same claude, same message, no stored prompt. Its reply
	// carrying the marker would mean one conversation's prompt reached another
	// conversation's session; its reply NOT carrying it is what makes arm 1
	// evidence about the prompt rather than about claude's habits.
	sealSendMessage(t, h.phone, h.initSend, 3, liveBareConvID, "m-bare", livePromptTurnQuery)
	bare := drainForCompletedTurnText(t, h.phone, h.initRecv, liveBareConvID, perTurnReplyBudget)
	if strings.Contains(bare, marker) {
		t.Errorf("AC #5: the reply for the conversation with NO stored system prompt carries %q — "+
			"a per-session prompt file is being shared across sessions.\nreply: %s", marker, bare)
	}

	// The daemon's own spawn records: each conversation's session must name its
	// own prompt file. Read from production's record rather than transcribed, the
	// same discipline #2093's live test uses.
	seen := map[string]bool{}
	for _, rec := range sysPromptSpawnRecords(h.daemon.stderr.String()) {
		if path := sysPromptArgFromRecord(rec); path != "" {
			seen[path] = true
		}
	}
	for _, sessID := range []string{livePromptedSessID, liveBareSessID} {
		var found string
		for path := range seen {
			if strings.Contains(path, sessID) {
				found = path
			}
		}
		if found == "" {
			t.Errorf("AC #5: no spawn record names a prompt file for session %s; the observed files are %v",
				sessID, seen)
			continue
		}
		raw, err := os.ReadFile(found)
		if err != nil {
			t.Errorf("the spawn argv names %q, which is not readable: %v", found, err)
			continue
		}
		wantMarker := sessID == livePromptedSessID
		if got := strings.Contains(string(raw), marker); got != wantMarker {
			t.Errorf("the file at %q carries the marker = %v, want %v — only the prompted conversation's "+
				"session file may hold it", found, got, wantMarker)
		}
	}
}
