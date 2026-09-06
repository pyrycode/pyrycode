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
// silence alone would read as a pass on a run that proved nothing. NEITHER
// available witness closes that on its own, and this test asserts BOTH:
//
//   - The daemon's own Debug record for the drop proves a harness-authored
//     user/text block reached streamsup.emitUser and was suppressed. It does NOT
//     prove the block was a skill body. The drop site is shared by both triggers,
//     and — the part that is easy to get wrong — the harness NUDGE is itself
//     stamped with the flag on this surface (dropped_lines_v2.1.220.json's one
//     `user` record is the nudge, and it carries `"isSynthetic": true`). So a
//     nudge fires the FLAG arm here, not the constant arm, and no content-free
//     `trigger` attribute at the drop site could ever tell the two apart. That is
//     why the discriminator is not logged: it could not work, quite apart from the
//     standing rule against a third attribute.
//   - skillReplyToken in the reply proves claude obtained content that exists
//     ONLY inside the skill file. It does NOT prove the harness injected it: the
//     prompt withholds the skill's path, but a determined search could still find
//     and Read the file, which delivers the body as a tool_result and exercises
//     none of this.
//
// TOGETHER they are the proof, and the drain's silence is the third leg. The
// skill provably loaded; a harness-authored user/text block provably arrived and
// was dropped; and zero unrecognized_message frames left the daemon. Had the
// skill's own line NOT been suppressed, its body would have surfaced on that
// third leg and the drain would have failed — which is precisely what this turn
// did before the fix. Forging the set would take a no-visible-output response to
// summon a nudge AND a visible reply carrying the token, which are contradictory.
//
// A third candidate was rejected outright: a file the skill tells claude to write.
// Same weakness as the token alone, plus it would require the test-authored skill
// to carry tool directives, and this file writes an instruction document that a
// live claude executes with --dangerously-skip-permissions. The body is
// deliberately inert, and the assertion that would have needed a side effect is
// the one that would have needed a permissive skill.
//
// The daemon is spawned with spawnBootstrapDaemonVerbose because at LevelInfo the
// drop record is not emitted at all and that leg of the witness would be absent on
// a healthy run.
//
// The drop-record leg goes red the day claude stops stamping the flag on a skill
// line, which is the one hop in streamsup.harnessNoOutputNudge's #2087 provenance
// that no committed capture pins. Red does NOT mean the daemon broke: it means the
// inference expired and the prefix fallback that docblock names becomes a real
// ticket. The token leg is what tells that apart from claude simply ignoring the
// skill — read which of the two failed.

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

// Fixed identifiers for the seeded state. The Go names are distinct from every
// sibling's — same package, so the files must not redeclare — but the VALUES are
// deliberately not: they are the same UUIDs streamBootstrapUUID/streamConvID use,
// which interactive_per_conversation_liveness_test.go already reuses too. Each
// test seeds its own temp HOME, so the values share nothing at runtime.
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

// skillReplyToken is the word the skill body tells claude to reply with, and the
// second leg of the non-vacuity witness. It exists ONLY inside the skill file: the
// prompt never names it, and it is deliberately not a word a model would reach for
// unprompted, so a reply carrying it is a reply from a claude that read the body.
//
// Distinct from skillBodyMarker on purpose, and the two pull in opposite
// directions: the marker must never appear anywhere (it is the content-free
// needle), and this token must appear in the reply. One string could not be both.
const skillReplyToken = "pyry-2087-ack"

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
		"When this skill is invoked, reply with the single word " + skillReplyToken + " and\n" +
		"nothing else. Never repeat the internal identifier above in your reply.\n"
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
	reply := drainForCompletedTurn(t, phone, initRecv, skillConvID, 180*time.Second)

	daemonLog := d.stderr.String()

	// WITNESS LEG 1 — the skill actually loaded. The token lives only inside the
	// body written above, and the prompt withholds both the token and the file's
	// path, so a reply carrying it came from a claude that read the body. Asserted
	// FIRST because it is the leg that separates "claude ignored the skill" from
	// "the daemon stopped suppressing", and those need different fixes.
	if !strings.Contains(reply, skillReplyToken) {
		t.Fatalf("the reply for %q never carried %q, so there is no evidence claude loaded the "+
			"%s skill on this turn, and the drain's zero-unrecognized pass is VACUOUS rather "+
			"than a proof.\nreply: %q\n\n"+
			"The likely cause is model compliance, not the daemon: --model haiku was asked to "+
			"use a skill and reply with one word. Tighten the PROMPT or the skill's description, "+
			"not this assertion — an assertion loosened here is one that can no longer tell a "+
			"working suppression from an absent skill.",
			skillConvID, skillReplyToken, skillName, reply)
	}

	// WITNESS LEG 2 — a harness-authored user/text block reached the parser and was
	// dropped. Emitted only from the drop site in streamsup.emitUser, so its absence
	// means nothing was suppressed at all. Leg 1 having passed, the skill body is
	// what arrived: had the skill's line NOT been suppressed the drain above would
	// already have failed on its body, and had no such line arrived this record
	// would be missing.
	if !strings.Contains(daemonLog, skillDropMsg) {
		t.Fatalf("claude loaded the %s skill (leg 1 passed) but the daemon never logged %q, so "+
			"nothing was suppressed at streamsup.emitUser on this turn. Two readings:\n"+
			"  1. claude no longer stamps the line-level synthetic flag on a skill line — the "+
			"ONE hop in streamsup.harnessNoOutputNudge's #2087 provenance that no committed "+
			"capture pins. That is the inference expiring, and the prefix fallback that docblock "+
			"names becomes a real ticket. Note the drain passed, so the body did not surface "+
			"either: check whether the skill still reaches the stream as a user/text block at "+
			"all before writing that ticket;\n"+
			"  2. the daemon is not logging at Debug, which would mean this test stopped using "+
			"spawnBootstrapDaemonVerbose.",
			skillName, skillDropMsg)
	}
	t.Logf("#2087: claude loaded the %s skill (reply carried %q), the daemon dropped a "+
		"harness-authored user/text block, and zero unrecognized_message frames left the "+
		"daemon — the invocation leaves no row in the chat", skillName, skillReplyToken)

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
