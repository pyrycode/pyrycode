//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamNoUnrecognizedOnToolTurn is a NEGATIVE test, and it is the
// most valuable assertion in the unrecognized-message feature.
//
// The stream parser splits claude's output two ways. What we knowingly ignore
// stays silent: rate_limit_event whole, and every `system` subtype the parser
// does not map — which since #1380/#1381 is every subtype but the handful
// streamsup.emitSystemSubtype carves out into background-task events (that
// switch is the one enumeration site; restating the set here would only go
// stale). Anything else surfaces to the client as an unrecognized_message frame,
// so a claude version that moves something meaningful into a new message type
// becomes visible the moment it arrives instead of vanishing into a debug log
// the production daemon does not print.
//
// That split rests on a MEASUREMENT, not a guess: streamsup.ignoredLineTypes was
// seeded by driving claude directly on the bare stream-json surface on 2026-07-27,
// three turns each on haiku and the default model, and counting distinct top-level
// types and content-block types. A measurement goes stale the day claude ships a
// new type. This test is what notices.
//
// It goes red the day claude adds a message type — which is exactly the alarm this
// feature exists to provide, arriving in the pre-ship gate before a binary swap
// rather than in front of a user afterwards. Red here does NOT necessarily mean
// something is broken; it means claude's output grew a shape we do not map, and
// somebody must decide whether it deserves a mapping or an ignore-list entry.
//
// WHY A TOOL TURN. The sibling liveness spec drives a plain text turn, which
// exercises assistant/text and result only. A tool-calling turn is the widest line
// inventory a single turn can produce on this surface: assistant/thinking,
// assistant/tool_use, user/tool_result, assistant/text, plus the per-turn
// system/init, the system/thinking_tokens stream, and rate_limit_event. That makes
// it the strongest single sample of "normal output", and the two whose silence is
// load-bearing are system/init (once per TURN, not per session) and
// system/thinking_tokens (roughly ten per turn) — if either ever surfaced, every
// conversation would grow a noise row per turn and the feature would be worthless.
//
// The zero-unrecognized assertion itself lives in the shared drainForCompletedTurn
// (#1153), so the three other stream specs are sentinels too. This file adds the
// tool-turn sample they do not cover.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
)

// Fixed identifiers for the seeded state, distinct from every sibling in this
// package (same package — the files must not redeclare).
const (
	unrecognizedBootstrapUUID = "99999999-9999-4999-8999-999999999999"
	unrecognizedConvID        = "77777777-7777-4777-8777-777777777777"
)

func TestInteractiveStreamNoUnrecognizedOnToolTurn(t *testing.T) {
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// The stream-json interactive runner is the surface under test; the PTY path
	// has no such parser and no such diagnostic.
	writeStreamInteractiveConfig(t, home)

	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBootstrapRegistry(t, home, unrecognizedBootstrapUUID)
	seedBoundConversation(t, home, unrecognizedConvID, unrecognizedBootstrapUUID, workdir)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, fr.URL()+"/v2/server")
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeInteractive(t, phone, pubKey, payload.Token)

	// A prompt that forces a real tool round trip, so the turn carries tool_use and
	// tool_result blocks rather than text alone. The per-run nonce defeats accidental
	// caching; the instruction is deliberately mechanical so the turn is short and
	// the assertion never depends on what claude says.
	nonce := time.Now().UnixNano()
	sealSendMessage(t, phone, initSend, 2, unrecognizedConvID, "m-1",
		fmt.Sprintf("Use the Write tool to create a file called probe.txt containing the single "+
			"word banana, then use the Read tool to read it back. Reply with one short word. run=%d", nonce))

	// The assertion is inside this drain: it fails on the FIRST unrecognized_message
	// frame, naming the offending site, type, and raw payload. Reaching the terminal
	// idle state without one is the pass.
	drainForCompletedTurn(t, phone, initRecv, unrecognizedConvID, 180*time.Second)

	t.Log("a full real-claude tool turn produced zero unrecognized_message frames — " +
		"streamsup.ignoredLineTypes still matches claude's actual output")
}
