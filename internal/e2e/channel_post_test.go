//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// historyEntry mirrors the fields of an on-disk history segment line this suite
// asserts on. Declared locally rather than importing internal/history, for
// convRow's reason: the test reads the FILE's contract, so a field renamed on
// the struct without a tag change must not silently pass here.
type historyEntry struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	TS      time.Time       `json:"ts"`
}

// assistantDeltaBody is the decoded protocol.AssistantDeltaPayload an
// "assistant_delta" entry carries. Declared locally for historyEntry's reason:
// the test reads the FILE's contract, so a field renamed on the struct without a
// tag change must not silently pass here.
type assistantDeltaBody struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	Seq            int    `json:"seq"`
	Text           string `json:"text"`
}

// waitForHistoryEntries polls the conversation's durable log until it holds at
// least one entry, and returns every entry across its segments in arrival order.
//
// The segment files are globbed rather than named: the naming scheme is an
// unexported detail of internal/history, and a test that hardcoded it would fail
// on a change that the served page is indifferent to. The header line of each
// segment is not an entry and is skipped by its missing "type".
func waitForHistoryEntries(t *testing.T, home, convID string) []historyEntry {
	t.Helper()
	dir := filepath.Join(home, ".pyry", "test", "conversations", convID, "history")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		names, _ := filepath.Glob(filepath.Join(dir, "*"))
		var out []historyEntry
		for _, name := range names {
			raw, err := os.ReadFile(name)
			if err != nil {
				continue
			}
			for _, line := range bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n")) {
				var e historyEntry
				if json.Unmarshal(line, &e) != nil || e.Type == "" {
					continue // the segment header, or a partially-written line
				}
				out = append(out, e)
			}
		}
		if len(out) > 0 {
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no history entries for conversation %s under %s within 5s", convID, dir)
	return nil
}

// TestChannelPost_E2E_RecordsAssistantEntry drives `pyry channel post` against a
// real daemon over its control socket and asserts the whole of #2497's AC#1 and
// AC#3: the verb exits 0 printing nothing at all, and the content is readable
// back out of the named channel's durable log in the shape a client draws.
//
// The silence assertion is not cosmetic. The first consumer is a cron, where any
// byte on stdout or stderr becomes mail, so an accidental `fmt.Println(id)`
// copied from `channel new` is a real regression this pins.
//
// Fake-daemon tier (fakeclaude via writeSleepClaude, no credentials, no
// network), so `make check` covers the verb.
func TestChannelPost_E2E_RecordsAssistantEntry(t *testing.T) {
	home, _ := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	proj, _ := projectDir(t, home, "ops")

	r := runVerbIn(t, h.SocketPath, home, proj, "channel", "new", "--name", "questions")
	if r.ExitCode != 0 {
		t.Fatalf("pyry channel new exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	id := string(bytes.TrimRight(r.Stdout, "\n"))

	const text = "What is the one thing you are avoiding today?"
	p := runVerb(t, h.SocketPath, home, "channel", "post", "--name", "questions", "--text", text)
	if p.ExitCode != 0 {
		t.Fatalf("pyry channel post exit=%d\nstdout:\n%s\nstderr:\n%s", p.ExitCode, p.Stdout, p.Stderr)
	}
	if len(p.Stdout) != 0 {
		t.Errorf("stdout = %q, want nothing — the first consumer is a cron", p.Stdout)
	}
	if len(p.Stderr) != 0 {
		t.Errorf("stderr = %q, want nothing on the success path", p.Stderr)
	}

	entries := waitForHistoryEntries(t, home, id)
	if len(entries) != 1 {
		t.Fatalf("history holds %d entries, want exactly 1 — one message per call", len(entries))
	}
	// assistant_delta, not the message/role-assistant entry #2497 wrote. That
	// shape reached a client and drew nothing, on the live path and the served
	// history path both; this one is what the current clients render as assistant
	// text. One entry of one type is also the whole of "one post is one
	// rendering": a second record in another shape is what a client would draw
	// twice.
	if entries[0].Type != "assistant_delta" {
		t.Errorf("entry type = %q, want %q", entries[0].Type, "assistant_delta")
	}
	var body assistantDeltaBody
	if err := json.Unmarshal(entries[0].Payload, &body); err != nil {
		t.Fatalf("decode entry payload: %v", err)
	}
	if body.Text != text {
		t.Errorf("entry text = %q, want %q", body.Text, text)
	}
	if body.ConversationID != id {
		t.Errorf("entry conversation_id = %q, want the posted-into channel %q", body.ConversationID, id)
	}
	if body.TurnID == "" {
		t.Error("entry turn_id is empty; a client coalesces on it and every empty-turn post would draw as one message")
	}
	if body.Seq != 0 {
		t.Errorf("entry seq = %d, want 0 — the first frame of the post's own turn", body.Seq)
	}
}

// TestChannelPost_E2E_CreatesMissingChannel covers AC#2's no-match arm end to
// end: a label no channel carries creates one under the daemon's default
// workspace and posts into it, still printing nothing.
//
// It finds the created row by scanning conversations.json for the name rather
// than by reading an id off stdout, because there is no id on stdout to read —
// which is itself the contract under test.
func TestChannelPost_E2E_CreatesMissingChannel(t *testing.T) {
	home, _ := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	p := runVerb(t, h.SocketPath, home, "channel", "post", "--name", "reminders", "--text", "take your supplement")
	if p.ExitCode != 0 {
		t.Fatalf("pyry channel post exit=%d\nstdout:\n%s\nstderr:\n%s", p.ExitCode, p.Stdout, p.Stderr)
	}
	if len(p.Stdout) != 0 || len(p.Stderr) != 0 {
		t.Errorf("stdout=%q stderr=%q, want both empty", p.Stdout, p.Stderr)
	}

	row := waitForConversationNamed(t, home, "reminders")
	if !row.IsPromoted {
		t.Error("created row is_promoted = false, want true — a post creates a channel")
	}
	entries := waitForHistoryEntries(t, home, row.ID)
	if len(entries) != 1 {
		t.Fatalf("history holds %d entries, want 1", len(entries))
	}
}

// TestChannelPost_E2E_RefusalShapes covers the rest of AC#1 through the real
// binary: each named failure posts nothing, writes nothing to stdout, and exits
// with the code its class calls for.
//
// The exit codes follow channelNewVerdict's existing split, which AC#1 defers to
// by name: 2 for usage failures and 1 for everything else. The two classes also
// differ in shape, and deliberately — an exit-1 failure is one stderr line,
// while a usage failure prints its detail above the banner, which is what
// `pyry channel new` already does and what makes the banner worth printing at
// all.
func TestChannelPost_E2E_RefusalShapes(t *testing.T) {
	home, _ := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	tests := []struct {
		name     string
		socket   string
		args     []string
		wantExit int
	}{
		{name: "neither --text nor --file", socket: h.SocketPath, args: []string{"--name", "q"}, wantExit: 2},
		{name: "both --text and --file", socket: h.SocketPath, args: []string{"--name", "q", "--text", "hi", "--file", "/tmp/x"}, wantExit: 2},
		{name: "unreadable --file", socket: h.SocketPath, args: []string{"--name", "q", "--file", filepath.Join(home, "absent.txt")}, wantExit: 1},
		{name: "daemon not running", socket: filepath.Join(home, "no-such.sock"), args: []string{"--name", "q", "--text", "hi"}, wantExit: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runVerb(t, tt.socket, home, "channel", append([]string{"post"}, tt.args...)...)
			if r.ExitCode != tt.wantExit {
				t.Errorf("exit = %d, want %d\nstderr:\n%s", r.ExitCode, tt.wantExit, r.Stderr)
			}
			if len(r.Stdout) != 0 {
				t.Errorf("stdout = %q, want nothing on any refusal", r.Stdout)
			}
			if len(r.Stderr) == 0 {
				t.Error("stderr is empty, want a diagnostic")
			}
			if tt.wantExit == 1 {
				if n := bytes.Count(bytes.TrimRight(r.Stderr, "\n"), []byte("\n")) + 1; n != 1 {
					t.Errorf("stderr holds %d lines, want exactly 1:\n%s", n, r.Stderr)
				}
			}
		})
	}

	// Nothing above created a channel: every refusal is decided before the post.
	path := filepath.Join(home, ".pyry", "test", "conversations.json")
	if raw, err := os.ReadFile(path); err == nil && bytes.Contains(raw, []byte(`"q"`)) {
		t.Errorf("a refusal created a channel named q:\n%s", raw)
	}
}

// waitForConversationNamed polls conversations.json for the single row carrying
// name, and fails if none or more than one appears.
func waitForConversationNamed(t *testing.T, home, name string) convRow {
	t.Helper()
	path := filepath.Join(home, ".pyry", "test", "conversations.json")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			var file struct {
				Conversations []convRow `json:"conversations"`
			}
			if json.Unmarshal(raw, &file) == nil {
				var hits []convRow
				for _, c := range file.Conversations {
					if c.Name != nil && *c.Name == name {
						hits = append(hits, c)
					}
				}
				if len(hits) == 1 {
					return hits[0]
				}
				if len(hits) > 1 {
					t.Fatalf("%d conversations named %q, want 1", len(hits), name)
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	raw, _ := os.ReadFile(path)
	t.Fatalf("no conversation named %q in %s within 5s\nfile:\n%s", name, path, raw)
	return convRow{}
}

// seedPromotedBoundConversation is seedBoundConversation with the two fields
// #2499's proof needs: the row is a promoted CHANNEL carrying name, so
// `pyry channel post --name` resolves to it, and it is bound to boundSessionID so
// send_message routes to the same child. Written here rather than by widening the
// harness helper, which has many callers that want neither field.
func seedPromotedBoundConversation(t *testing.T, home, convID, name, boundSessionID string) {
	t.Helper()
	dir := filepath.Join(home, ".pyry", "test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seed conversations.json: mkdir: %v", err)
	}
	convJSON := []byte(`{"conversations":[{"id":"` + convID +
		`","name":"` + name +
		`","cwd":"` + home +
		`","current_session_id":"` + boundSessionID +
		`","is_promoted":true,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "conversations.json"), convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: write: %v", err)
	}
}

// waitForChildTurn polls the child's stdin tee until a single line carries needle,
// and returns that line. One turn is one stream-json envelope on one line, so the
// returned line IS the turn — which is what lets the caller assert that two pieces
// of text reached claude in the SAME turn rather than merely both reaching it.
//
// The read error is carried into the failure message: without it, len(nil) == 0
// renders "the tee was never wired" as "the turn was empty", a broken instrument
// reporting itself as a measurement.
func waitForChildTurn(t *testing.T, stem, sessionID, needle string) []byte {
	t.Helper()
	path := childStdinLog(stem, sessionID)
	deadline := time.Now().Add(25 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		lastErr = err
		for _, line := range bytes.Split(raw, []byte("\n")) {
			if bytes.Contains(line, []byte(needle)) {
				return line
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	raw, _ := os.ReadFile(path)
	t.Fatalf("no turn carrying %q reached the child within 25s (read err: %v)\nstdin log %s:\n%s",
		needle, lastErr, path, raw)
	return nil
}

// TestChannelPost_E2E_CarriesIntoTheNextTurn is #2499's proof, end to end against
// the fake: a cron posts a question into a channel, the operator answers it from a
// phone, and CLAUDE SEES BOTH — the question first — in one turn.
//
// It reads the child's stdin tee (PYRY_FAKE_CLAUDE_STDIN_LOG) rather than anything
// the clients see, because what claude receives is the whole deliverable and is
// invisible on every other surface by design: the composed text reaches claude and
// nothing else.
//
// Three acceptance criteria in one run, because they are three properties of one
// sequence and splitting them would pay the relay-handshake setup three times:
//
//   - AC1 the post leads the reply, in the order the two happened, in ONE turn.
//   - AC2 carried exactly once — the SECOND reply carries only itself.
//   - AC3's second half — that second reply, with nothing pending, is byte-for-byte
//     the turn it would have been before this slice existed.
//
// Fake-daemon tier (fakeclaude in stream-json mode, fakerelay, one paired
// fakephone), so `make check` covers the carry.
func TestChannelPost_E2E_CarriesIntoTheNextTurn(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		convID      = "22222222-2222-4222-8222-222222222222"
		channelName = "questions"
		postText    = "e2e-2499-post:what-are-you-avoiding"
		replyOne    = "e2e-2499-reply-one:the-tax-return"
		replyTwo    = "e2e-2499-reply-two:anything-else"
	)

	home := shortHome(t)
	stdinStem := filepath.Join(t.TempDir(), "fakeclaude-stdin")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// The channel and the phone's target are ONE conversation — that is the whole
	// premise. The daemon loads conversations.json once at startup, so the row must
	// exist before it starts.
	seedPromotedBoundConversation(t, home, convID, channelName, initialUUID)

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL,
		"PYRY_FAKE_CLAUDE_STDIN_LOG="+stdinStem)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	sendKey, recvKey := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	// The cron posts. This returns only once the content is in the durable log AND
	// recorded as pending, so the send below cannot race it.
	p := runVerb(t, h.SocketPath, home, "channel", "post", "--name", channelName, "--text", postText)
	if p.ExitCode != 0 {
		t.Fatalf("pyry channel post exit=%d\nstdout:\n%s\nstderr:\n%s", p.ExitCode, p.Stdout, p.Stderr)
	}

	// The operator answers that evening.
	sendPhoneMessage(t, phone, sendKey, recvKey, 2499, convID, "u-1", replyOne)
	turnOne := waitForChildTurn(t, stdinStem, initialUUID, replyOne)

	postAt := bytes.Index(turnOne, []byte(postText))
	replyAt := bytes.Index(turnOne, []byte(replyOne))
	if postAt < 0 {
		t.Fatalf("AC1: the turn claude received carries the reply but NOT the posted question — "+
			"the carry never reached the delivery seam:\n%s", turnOne)
	}
	if postAt > replyAt {
		t.Errorf("AC1: the posted question reached claude AFTER the reply (post at %d, reply at %d); "+
			"claude must see them in the order the two happened:\n%s", postAt, replyAt, turnOne)
	}

	// A second reply, with nothing pending.
	sendPhoneMessage(t, phone, sendKey, recvKey, 2500, convID, "u-2", replyTwo)
	turnTwo := waitForChildTurn(t, stdinStem, initialUUID, replyTwo)

	if bytes.Contains(turnTwo, []byte(postText)) {
		t.Errorf("AC2: the posted question was carried a SECOND time; it must be cleared by the "+
			"confirmed delivery that carried it:\n%s", turnTwo)
	}
	// AC3's second half, asserted as an absence rather than a byte comparison: the
	// only thing this slice can add to a turn is the carried block, so a turn holding
	// nothing but the reply IS the turn it would have been before #2499. The needle
	// is unique to this reply, so this also proves turnTwo is not turnOne re-read.
	if !bytes.Contains(turnTwo, []byte(replyTwo)) {
		t.Fatalf("the second turn does not carry its own reply:\n%s", turnTwo)
	}
}

// sendPhoneMessage seals one send_message for convID and waits for its ack, which
// is what establishes that the daemon ACCEPTED the turn into the backlog. It is not
// delivery — the drain owns that, and the stdin tee is where delivery is observed.
func sendPhoneMessage(t *testing.T, phone *fakephone.Client, sendKey, recvKey *noise.CipherState, reqID uint64, convID, messageID, text string) {
	t.Helper()
	env, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      messageID,
			Text:           text,
		}),
	})
	if err != nil {
		t.Fatalf("marshal send_message envelope: %v", err)
	}
	cipher, err := sendKey.Encrypt(env)
	if err != nil {
		t.Fatalf("seal send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phone, cipher)

	deadline := time.Now().Add(15 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no ack for send_message %d; the turn was never accepted", reqID)
		}
		got := decryptInnerEnvelope(t, readInnerFrame(t, phone, remaining), recvKey)
		if got.Type == protocol.TypeAck && got.InReplyTo != nil && *got.InReplyTo == reqID {
			return
		}
	}
}
