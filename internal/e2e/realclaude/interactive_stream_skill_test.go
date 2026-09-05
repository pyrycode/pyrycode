//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamSkillInvocationIsSilent is #2087's live proof: invoking a
// skill leaves no trace of the skill's body in the operator's chat.
//
// WHAT WENT WRONG. Claude's harness injects a skill's whole instruction file onto
// the output stream as a `user` line carrying a `text` block. streamsup.emitUser
// surfaced every user/text block but the one known harness nudge, so each skill
// invocation put an "Unrecognized message" row into the chat carrying the entire
// body, cut at the daemon's payload cap. Observed twice in one session on
// 2026-09-04 on Opus 5, at 18681 and 87244 chars.
//
// WHY THE EXISTING SENTINELS MISSED IT, and why this file exists rather than a
// widened sibling. TestInteractiveStreamNoUnrecognizedOnToolTurn drives the widest
// line inventory a single turn can produce — and skill invocation is not in it,
// for the same reason the 2026-07-27 census that seeded streamsup.ignoredLineTypes
// missed it: that measurement drove turns that CALLED TOOLS, and a skill is not a
// tool call. Every zero-unrecognized drain in this package is a sentinel for the
// shapes its own turn produces, and no turn here produced this one.
//
// THE NON-VACUITY PROBLEM, and how it is solved. A turn in which claude never
// invoked the skill also produces zero unrecognized_message frames, so the drain's
// silence alone would read as a pass on a run that proved nothing. Two candidate
// witnesses were rejected before the one below:
//
//   - A token echoed in the reply. claude can obtain a token from a skill file by
//     READING it, which delivers the body as a tool_result and exercises none of
//     this. The witness would be satisfied by the path that is not under test.
//   - A file the skill tells claude to write. Same weakness, plus it would require
//     the test-authored skill to carry tool directives, and this file writes an
//     instruction document that a live claude executes with
//     --dangerously-skip-permissions. The body is deliberately inert.
//
// The witness used instead is the daemon's OWN Debug record for the drop. It is
// reachable only when a user/text block on a flagged line actually arrived at
// emitUser and the new arm fired — which is the whole claim — and it is
// unforgeable by a compliant-but-skill-less turn. That is why the daemon is
// spawned with spawnBootstrapDaemonVerbose: at LevelInfo the record is not
// emitted and the witness would be absent on a healthy run.
//
// It goes red the day claude stops stamping the flag on a skill line, which is the
// one hop in streamsup.harnessNoOutputNudge's #2087 provenance that no committed
// capture pins. Red does NOT mean the daemon broke: it means the inference expired
// and the prefix fallback that docblock names becomes a real ticket.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
)

// Fixed identifiers for the seeded state, distinct from every sibling in this
// package (same package — the files must not redeclare).
const (
	skillBootstrapUUID = "88888888-8888-4888-8888-888888888888"
	skillConvID        = "66666666-6666-4666-8666-666666666666"
)

// skillName is both the skill's declared name and its directory name, which
// claude requires to agree.
const skillName = "pyry-probe"

// skillBodyMarker is a distinctive filler string that exists ONLY inside the
// skill body. It is the needle for the content-free assertion; distinctive rather
// than realistic so a hit cannot be a coincidence in unrelated log prose, and the
// body below tells claude never to repeat it so a hit cannot be claude's own
// words either.
const skillBodyMarker = "pyry-2087-skill-body-marker-do-not-repeat"

// skillDropMsg is the Debug message streamsup's drop site emits, as a LITERAL —
// the same rule streamsup's own harnessNudgeDropMsg states. A witness built from
// the production constant would follow a rewording of it green, and this message
// is the entire witness.
const skillDropMsg = "streamsup: dropping known harness user block"

// writeProbeSkill writes an INERT skill into the test's own temp HOME.
//
// Test-authored on purpose: lifting one from the operator's machine would make the
// assertion depend on a file this repository does not control, and would put that
// file's contents through a live model. Inert on purpose: a live claude runs this
// document as instructions under --dangerously-skip-permissions, so it names no
// tool, no path and no command. Everything it asks for is prose.
//
// 0o700 on the directories and 0o600 on the file, matching the mode
// WithWorktreeAuthenticated already uses for the credential file it seeds beside
// them.
func writeProbeSkill(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "skills", skillName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir skill dir: %v", err)
	}
	body := "---\n" +
		"name: " + skillName + "\n" +
		"description: Pyrycode end-to-end probe skill. Use this skill whenever the user asks " +
		"you to use the " + skillName + " skill. It asks only for a short spoken reply.\n" +
		"---\n\n" +
		"# Pyrycode probe skill\n\n" +
		"This document exists so that an automated test can observe what happens on the\n" +
		"daemon's stream when a skill is loaded. It asks for nothing that touches the\n" +
		"filesystem, the network, or any tool.\n\n" +
		"Internal identifier: " + skillBodyMarker + "\n\n" +
		"When this skill is invoked, reply with the single word `ready` and nothing\n" +
		"else. Never repeat the internal identifier above in your reply.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("realclaude: write SKILL.md: %v", err)
	}
}

func TestInteractiveStreamSkillInvocationIsSilent(t *testing.T) {
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
	writeProbeSkill(t, home)

	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBootstrapRegistry(t, home, skillBootstrapUUID)
	seedBoundConversation(t, home, skillConvID, skillBootstrapUUID, workdir)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// Verbose, so the daemon logs at slog.LevelDebug and the drop record — this
	// test's entire non-vacuity witness — is actually in the haystack. Its only
	// delta from the shared spawner is -pyry-verbose; its model alias is already
	// haiku, so no fourth near-copy of the spawner is needed.
	d := spawnBootstrapDaemonVerbose(t, home, workdir, claudeBin, fr.URL()+"/v2/server")
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

	// The prompt names the skill and asks for nothing else, so the shortest path to
	// complying is to invoke it. The per-run nonce defeats accidental caching. It
	// deliberately does NOT name the skill's path: telling claude where the file is
	// invites a Read, which delivers the body as a tool_result and exercises none
	// of the arm under test.
	nonce := time.Now().UnixNano()
	sealSendMessage(t, phone, initSend, 2, skillConvID, "m-1",
		fmt.Sprintf("Use the %s skill, then do exactly what it says. run=%d", skillName, nonce))

	// The zero-unrecognized assertion is inside this drain: it fails on the FIRST
	// unrecognized_message frame, naming the offending site, type and raw payload.
	// Before #2087 a skill turn failed here with an 87 KB user_block payload.
	drainForCompletedTurn(t, phone, initRecv, skillConvID, 180*time.Second)

	daemonLog := d.stderr.String()

	// THE WITNESS. Without it the drain's silence is satisfied by a turn in which
	// claude ignored the skill entirely, and this test would pass while proving
	// nothing. The record is emitted only from the drop site in streamsup.emitUser,
	// so its presence means a user/text block on a flagged line reached the parser
	// and the arm fired.
	if !strings.Contains(daemonLog, skillDropMsg) {
		t.Fatalf("the daemon never logged %q, so no harness-authored user/text block reached "+
			"streamsup.emitUser on this turn. The drain's zero-unrecognized pass is therefore "+
			"VACUOUS, not a proof. Three readings, in order of likelihood:\n"+
			"  1. claude did not invoke the %s skill at all (read the reply in the daemon log "+
			"above — an unhelpful reply on --model haiku is the common cause, and the fix is "+
			"the prompt, not this assertion);\n"+
			"  2. claude invoked it but no longer stamps the line-level synthetic flag — the ONE "+
			"hop in streamsup.harnessNoOutputNudge's #2087 provenance that no committed capture "+
			"pins. That is the inference expiring, and the prefix fallback that docblock names "+
			"becomes a real ticket;\n"+
			"  3. the daemon is not logging at Debug, which would mean this test stopped using "+
			"spawnBootstrapDaemonVerbose.",
			skillDropMsg, skillName)
	}
	t.Logf("#2087: the daemon dropped a harness-authored user/text block and emitted zero "+
		"unrecognized_message frames — a %s invocation leaves no row in the chat", skillName)

	// AC1's content-free rule, proved on the live surface rather than only against
	// a synthesised line. A skill body is operator-authored instruction, up to tens
	// of kilobytes of it, and the drop path is one careless attribute away from
	// writing all of it into stderr and journald — which would reintroduce one
	// layer down exactly the disclosure this ticket removes.
	//
	// Two readings if this reddens, and they need different fixes: the daemon
	// logged the body (a real regression at the drop site), or claude repeated the
	// identifier despite the body telling it not to (a prompt problem, worth a
	// human look before anything here changes).
	if idx := strings.Index(daemonLog, skillBodyMarker); idx >= 0 {
		t.Errorf("the skill body's internal identifier appears in the daemon's own debug-level "+
			"stderr at offset %d — the drop site logs site and type only, and nothing else in "+
			"the daemon may carry claude's payload either.\ncontext: %q",
			idx, logContextAround(daemonLog, idx, len(skillBodyMarker)))
	}
}
